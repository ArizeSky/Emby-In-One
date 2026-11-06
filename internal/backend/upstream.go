package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// upstreamHeaderTimeout bounds how long an upstream may take to start answering. Without
// it a streaming request to an upstream that accepted the connection and then went quiet
// held its goroutine until the client disconnected. It is deliberately generous: this
// transport is shared with the API requests and the fallback proxy, whose own client
// timeouts are normally much shorter.
const upstreamHeaderTimeout = 5 * time.Minute

var sharedTransport = &http.Transport{
	Proxy:                 http.ProxyFromEnvironment,
	MaxIdleConns:          64,
	MaxIdleConnsPerHost:   16,
	IdleConnTimeout:       90 * time.Second,
	TLSHandshakeTimeout:   10 * time.Second,
	ExpectContinueTimeout: 1 * time.Second,
	ResponseHeaderTimeout: upstreamHeaderTimeout,
	// Refuse link-local destinations (cloud instance metadata) while keeping
	// loopback and RFC1918 upstreams reachable.
	DialContext: upstreamTransportDialer(),
	// Disable automatic decompression to ensure we don't return decompressed bytes
	// with mismatching Content-Length headers back to the client.
	DisableCompression: true,
}

const recoveryDebounce = 30 * time.Second

func isUpstreamLoginPath(path string) bool {
	return path == "/Users/AuthenticateByName" || path == "/Users/Me"
}

type rawRequestBody struct {
	data        []byte
	contentType string
}

type UpstreamClient struct {
	mu               sync.RWMutex
	ID               string
	ServerIndex      int
	Name             string
	BaseURL          string
	StreamBaseURL    string   // effective stream base: first live entry of StreamBaseURLs
	StreamBaseURLs   []string // ordered stream bases: [0] primary, [1:] fallbacks
	Online           bool
	UserID           string
	AccessToken      string
	LastError        string
	Config           UpstreamConfig
	residentIdentity *residentIdentitySnapshot
	// Published atomically with token/UserID. Unknown legacy/test credentials
	// have zero compatibility metadata and cannot authenticate a strict attempt.
	authCompatibility playbackIdentityAuth
	authGeneration    uint64
	serverKey         string
	httpClient        *http.Client
	transport         http.RoundTripper // per-client transport (shared or proxy-specific)
	logger            *Logger
	timeouts          TimeoutsConfig
	recoveryMu        sync.Mutex
	loginMu           sync.Mutex
	lastRecovery      time.Time
	onAuthError       func(c *UpstreamClient)
	// streamFailures tracks per-stream-base connect-level failure times for
	// liveness marking; guarded by mu. A base marked dead is skipped by
	// streamBaseCandidates until streamFailureCooldown passes.
	streamFailures map[string]time.Time
}

type UpstreamPool struct {
	mu       sync.RWMutex
	clients  []*UpstreamClient
	logger   *Logger
	identity *ClientIdentityService
	health   *healthCheckRunner
}

func NewUpstreamPool(cfg Config, logger *Logger) *UpstreamPool {
	identity := activeIdentityService()
	pool := &UpstreamPool{logger: logger, identity: identity}
	if identity != nil {
		identity.RegisterCaptureListener(pool.handleCapturedIdentity)
	}
	pool.Reload(cfg)
	return pool
}

func (p *UpstreamPool) LoginAll() {
	p.mu.RLock()
	clients := append([]*UpstreamClient(nil), p.clients...)
	p.mu.RUnlock()
	identity := p.identityService()
	for _, client := range clients {
		if client.Config.SpoofClient == "passthrough" {
			if identity == nil || !identity.HasCapturedHeaders(client.serverKey) {
				if p.logger != nil {
					p.logger.Infof("[%s] Passthrough upstream skipped initial login — waiting for real client", client.Name)
				}
				continue
			}
		}
		client.Login(context.Background(), nil, identity)
	}
	if p.logger != nil {
		online := 0
		for _, client := range clients {
			if client.IsOnline() {
				online++
			}
		}
		p.logger.Infof("Go upstream login complete: %d/%d configured server(s) online", online, len(clients))
	}
}

func (p *UpstreamPool) Reload(cfg Config) {
	p.mu.RLock()
	oldClients := append([]*UpstreamClient(nil), p.clients...)
	p.mu.RUnlock()

	oldByKey := make(map[string]*UpstreamClient, len(oldClients))
	for _, c := range oldClients {
		if c != nil {
			oldByKey[c.serverKey] = c
		}
	}

	clients := make([]*UpstreamClient, 0, len(cfg.Upstream))
	for i, upstream := range cfg.Upstream {
		newClient := newUpstreamClient(cfg, upstream, i, p.logger)
		newClient.onAuthError = p.handleUpstreamAuthError
		if old, ok := oldByKey[newClient.serverKey]; ok {
			// A resident identity change (new profile or new identity version) must
			// not inherit the old session's token/user, or a new identity would be
			// paired with credentials issued to a different client identity.
			if !residentIdentityChanged(old, newClient) {
				old.mu.RLock()
				newClient.AccessToken = old.AccessToken
				newClient.UserID = old.UserID
				newClient.Online = old.Online
				newClient.LastError = old.LastError
				old.mu.RUnlock()
			}
		}
		clients = append(clients, newClient)
	}
	p.mu.Lock()
	if p.identity == nil {
		p.identity = activeIdentityService()
	}
	p.clients = clients
	p.mu.Unlock()

	// Every reload builds fresh per-proxy transports, and once its client is replaced
	// nothing else holds a reference to the old one: its pooled connections stayed open
	// until IdleConnTimeout expired, so each config save left a set of idle sockets behind
	// for 90 seconds. The shared transport is excluded — it is shared with the fallback
	// proxy and outlives any single client.
	stillInUse := make(map[http.RoundTripper]bool, len(clients))
	for _, client := range clients {
		stillInUse[client.transport] = true
	}
	for _, old := range oldClients {
		if old == nil || old.transport == nil || old.transport == sharedTransport || stillInUse[old.transport] {
			continue
		}
		if closer, ok := old.transport.(interface{ CloseIdleConnections() }); ok {
			closer.CloseIdleConnections()
		}
	}

	p.restartHealthChecks(cfg.Timeouts)
}

// requestAuthRecovery schedules the debounced recovery re-login. The cheap
// debounce check under the lock runs inline, so a burst of 401/403 answers does
// not spawn a goroutine per response; only the first answer in each debounce
// window pays for the spawn. The goroutine re-checks the debounce itself, so a
// racing spawn from another request still collapses there.
func (c *UpstreamClient) requestAuthRecovery() {
	if c.onAuthError == nil {
		return
	}
	c.recoveryMu.Lock()
	within := time.Since(c.lastRecovery) < recoveryDebounce
	c.recoveryMu.Unlock()
	if within {
		return
	}
	go c.onAuthError(c)
}

func (p *UpstreamPool) handleUpstreamAuthError(c *UpstreamClient) {
	c.recoveryMu.Lock()
	if time.Since(c.lastRecovery) < recoveryDebounce {
		c.recoveryMu.Unlock()
		return
	}
	c.lastRecovery = time.Now()
	c.recoveryMu.Unlock()
	if !p.canAttemptIdentityLogin(c) {
		if p.logger != nil {
			p.logger.Warnf("[%s] Upstream auth error but passthrough recovery skipped — waiting for real client", c.Name)
		}
		c.setOffline("upstream auth expired")
		return
	}
	if p.logger != nil {
		p.logger.Warnf("[%s] Upstream auth error on normal request, triggering recovery re-login", c.Name)
	}
	c.setOffline("upstream auth expired")
	c.Login(context.Background(), nil, p.identityService())
	if c.IsOnline() && p.logger != nil {
		p.logger.Infof("[%s] Recovery re-login succeeded", c.Name)
	}
}

// findProxy looks up a proxy by ID in the proxy list. Returns nil if not found or id is empty.
func findProxy(proxies []ProxyConfig, id string) *ProxyConfig {
	if id == "" {
		return nil
	}
	for i := range proxies {
		if proxies[i].ID == id {
			return &proxies[i]
		}
	}
	return nil
}

// redactProxyURLForLog keeps a proxy URL's scheme, host, path and query but
// never its userinfo, so a credential-bearing URL cannot reach the log intact
// even when it is too malformed to parse (the userinfo is stripped by hand in
// that case).
func redactProxyURLForLog(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if parsed, err := url.Parse(trimmed); err == nil && parsed.Host != "" {
		parsed.User = nil
		return parsed.String()
	}
	if at := strings.LastIndex(trimmed, "@"); at >= 0 {
		return "<redacted-userinfo>@" + trimmed[at+1:]
	}
	return "<unparsable-url>"
}

// buildProxyTransport creates an http.Transport that routes requests through the given proxy URL.
// Returns (transport, true) on success, or (sharedTransport, false) on failure.
func buildProxyTransport(proxyURL string, logger *Logger, serverName string) (http.RoundTripper, bool) {
	parsed, err := url.Parse(proxyURL)
	if err != nil {
		if logger != nil {
			// Both the URL and the parse error can carry the credentials (the
			// error embeds the raw URL); only redacted forms are logged.
			logger.Errorf("[%s] Invalid proxy URL %q: %s, falling back to direct", serverName, redactProxyURLForLog(proxyURL), redactURLInError(err))
		}
		return sharedTransport, false
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		if logger != nil {
			logger.Errorf("[%s] Proxy URL must be http or https, got %q, falling back to direct", serverName, parsed.Scheme)
		}
		return sharedTransport, false
	}
	return &http.Transport{
		Proxy:                 http.ProxyURL(parsed),
		MaxIdleConns:          64,
		MaxIdleConnsPerHost:   16,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ResponseHeaderTimeout: upstreamHeaderTimeout,
		DialContext:           upstreamTransportDialer(),
		DisableCompression:    true,
	}, true
}

func newUpstreamClient(cfg Config, upstream UpstreamConfig, index int, logger *Logger) *UpstreamClient {
	timeouts := cfg.Timeouts
	if timeouts.API == 0 {
		timeouts.API = 30000
	}
	if timeouts.Login == 0 {
		timeouts.Login = 30000
	}
	if timeouts.HealthCheck == 0 {
		timeouts.HealthCheck = 30000
	}
	baseURL := strings.TrimRight(upstream.URL, "/")
	streamBases := append([]string(nil), upstream.StreamingURLs...)
	if len(streamBases) == 0 {
		streamBases = []string{baseURL}
	}

	// Resolve proxy: per-upstream transport if proxyId is set, otherwise shared
	transport := http.RoundTripper(sharedTransport)
	if proxy := findProxy(cfg.Proxies, upstream.ProxyID); proxy != nil {
		if t, ok := buildProxyTransport(proxy.URL, logger, upstream.Name); ok {
			transport = t
			if logger != nil {
				// Log proxy usage (mask credentials in URL)
				masked := proxy.URL
				if p, err := url.Parse(proxy.URL); err == nil && p.User != nil {
					p.User = url.UserPassword("***", "***")
					masked = p.String()
				}
				logger.Infof("[%s] Using proxy: %s (%s)", upstream.Name, proxy.Name, masked)
			}
		}
	}

	client := &UpstreamClient{
		ID:             upstream.ID,
		ServerIndex:    index,
		Name:           upstream.Name,
		BaseURL:        baseURL,
		StreamBaseURL:  streamBases[0],
		StreamBaseURLs: streamBases,
		Config:         upstream,
		serverKey:      StableUpstreamKey(upstream),
		httpClient: &http.Client{
			Transport:     transport,
			Timeout:       time.Duration(timeouts.API) * time.Millisecond,
			CheckRedirect: nil,
		},
		transport:      transport,
		logger:         logger,
		timeouts:       timeouts,
		streamFailures: make(map[string]time.Time),
	}
	// The template client never follows a redirect without projection. Every
	// request rebuilds its own policy from the purpose it is sent for, so this
	// default only covers a caller that reaches the template directly.
	client.httpClient.CheckRedirect = client.redirectPolicyFor(upstream.FollowRedirects, authModeNormal)
	if upstream.SpoofClient == "resident" {
		client.residentIdentity = residentSnapshotFromLibrary(upstream.UAProfileID)
	}
	return client
}

// streamFailureCooldown is how long a stream base stays marked dead after a
// connect-level failure or a failed liveness probe. Short enough that a flapped
// line recovers on the next request; long enough that a dead line is not
// retried on every segment.
const streamFailureCooldown = 60 * time.Second

// markStreamBaseFailed records a connect-level failure for one stream base.
func (c *UpstreamClient) markStreamBaseFailed(base string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.streamFailures == nil {
		c.streamFailures = make(map[string]time.Time)
	}
	c.streamFailures[base] = time.Now()
}

// markStreamBaseAlive clears the failure mark for one stream base. A successful
// request or liveness probe through the line proves it works again.
func (c *UpstreamClient) markStreamBaseAlive(base string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.streamFailures, base)
}

// streamBaseCandidates returns the stream bases in configured order, skipping
// those marked dead within the cooldown. When every base is marked dead the
// full ordered list is returned anyway: a wrong liveness verdict must never
// leave the upstream with no stream base at all.
func (c *UpstreamClient) streamBaseCandidates() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	bases := c.StreamBaseURLs
	if len(bases) == 0 {
		return []string{c.StreamBaseURL}
	}
	live := make([]string, 0, len(bases))
	for _, base := range bases {
		if failed, ok := c.streamFailures[base]; ok && time.Since(failed) < streamFailureCooldown {
			continue
		}
		live = append(live, base)
	}
	if len(live) == 0 {
		return append([]string(nil), bases...)
	}
	return live
}

func (p *UpstreamPool) GetClient(index int) *UpstreamClient {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if index < 0 || index >= len(p.clients) {
		return nil
	}
	return p.clients[index]
}

func (p *UpstreamPool) ClientByID(id string) *UpstreamClient {
	if id == "" {
		return nil
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	for _, client := range p.clients {
		if client != nil && client.ID == id {
			return client
		}
	}
	return nil
}

func (p *UpstreamPool) ReconnectByID(id string) *UpstreamClient {
	client := p.ClientByID(id)
	if client == nil {
		return nil
	}
	identity := p.identityService()
	if !p.canAttemptIdentityLogin(client) {
		if p.logger != nil {
			p.logger.Infof("[%s] Manual reconnect skipped passthrough login — waiting for real client", client.Name)
		}
		return client
	}
	client.Login(context.Background(), nil, identity)
	return client
}

func (p *UpstreamPool) Reconnect(index int) *UpstreamClient {
	client := p.GetClient(index)
	if client == nil {
		return nil
	}
	identity := p.identityService()
	if !p.canAttemptIdentityLogin(client) {
		if p.logger != nil {
			p.logger.Infof("[%s] Manual reconnect skipped passthrough login — waiting for real client", client.Name)
		}
		return client
	}
	client.Login(context.Background(), nil, identity)
	return client
}

func (p *UpstreamPool) Clients() []*UpstreamClient {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]*UpstreamClient, len(p.clients))
	for i, client := range p.clients {
		copyClient := client.snapshot()
		out[i] = &copyClient
	}
	return out
}

func (p *UpstreamPool) canAttemptIdentityLogin(client *UpstreamClient) bool {
	if client == nil {
		return false
	}
	switch client.Config.SpoofClient {
	case "passthrough":
		identity := p.identityService()
		return identity != nil && identity.HasCapturedHeaders(client.serverKey)
	case "resident":
		return client.residentIdentity != nil
	default:
		return true
	}
}

func (p *UpstreamPool) OnlineClients() []*UpstreamClient {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := []*UpstreamClient{}
	for _, client := range p.clients {
		if client.IsOnline() {
			out = append(out, client)
		}
	}
	return out
}

func (p *UpstreamPool) identityService() *ClientIdentityService {
	p.mu.RLock()
	identity := p.identity
	p.mu.RUnlock()
	if identity != nil {
		return identity
	}
	identity = activeIdentityService()
	if identity != nil {
		p.mu.Lock()
		if p.identity == nil {
			p.identity = identity
		}
		p.mu.Unlock()
	}
	return identity
}

func (p *UpstreamPool) handleCapturedIdentity(token string, headers http.Header) {
	go p.retryOfflinePassthrough(token, headers)
}

func (p *UpstreamPool) retryOfflinePassthrough(token string, headers http.Header) {
	_ = token
	identity := p.identityService()
	if identity == nil {
		return
	}
	p.mu.RLock()
	clients := append([]*UpstreamClient(nil), p.clients...)
	p.mu.RUnlock()
	for _, client := range clients {
		if client == nil || client.Config.SpoofClient != "passthrough" || client.IsOnline() {
			continue
		}
		client.loginWithHeaders(context.Background(), nil, identity, cloneHeader(headers))
	}
}

// ConfigSnapshot returns a copy of the client's locked upstream configuration.
// Callers that must bind an in-flight request to the account it was sent under
// use this instead of reading the mutable Config field directly.
func (c *UpstreamClient) ConfigSnapshot() UpstreamConfig {
	if c == nil {
		return UpstreamConfig{}
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.Config
}

func (c *UpstreamClient) snapshot() UpstreamClient {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return UpstreamClient{
		ID:                c.ID,
		ServerIndex:       c.ServerIndex,
		Name:              c.Name,
		BaseURL:           c.BaseURL,
		StreamBaseURL:     c.StreamBaseURL,
		StreamBaseURLs:    append([]string(nil), c.StreamBaseURLs...),
		Online:            c.Online,
		UserID:            c.UserID,
		AccessToken:       c.AccessToken,
		authCompatibility: c.authCompatibility,
		authGeneration:    c.authGeneration,
		LastError:         c.LastError,
		Config:            c.Config,
		serverKey:         c.serverKey,
		httpClient:        c.httpClient,
		transport:         c.transport,
		logger:            c.logger,
		timeouts:          c.timeouts,
		residentIdentity:  c.residentIdentity,
	}
}

func (c *UpstreamClient) IsOnline() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.Online && c.AccessToken != "" && c.UserID != ""
}

func (c *UpstreamClient) Login(ctx context.Context, reqCtx *RequestContext, identity *ClientIdentityService) {
	c.loginWithHeaders(ctx, reqCtx, identity, nil)
}

func (c *UpstreamClient) loginWithHeaders(ctx context.Context, reqCtx *RequestContext, identity *ClientIdentityService, override http.Header) {
	// Serialize logins on this client: a targeted reconnect, a health-check
	// retry and an auth recovery can otherwise race and the last writer
	// silently clobbers the others' session state (execution plan §7.3).
	c.loginMu.Lock()
	defer c.loginMu.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}
	// Bound the whole login attempt, API-key validation included, by timeouts.login.
	// That setting used to be ignored, so an upstream that logs in slowly now fails
	// earlier than it did when every request inherited timeouts.api.
	loginCtx, cancelLogin := context.WithTimeout(ctx, c.loginTimeout())
	defer cancelLogin()
	ctx = loginCtx
	if c.Config.SpoofClient == "resident" && c.residentIdentity == nil {
		c.setOffline("常驻 UA 引用缺失或身份库不可用")
		return
	}

	if strings.TrimSpace(c.Config.APIKey) != "" {
		if c.logger != nil {
			c.logger.Infof("[%s] Authenticating with API key", c.Name)
		}
		c.mu.Lock()
		c.authGeneration++
		c.authCompatibility = playbackIdentityAuth{}
		c.AccessToken = strings.TrimSpace(c.Config.APIKey)
		c.mu.Unlock()
		c.validateAPIKey(ctx, reqCtx, identity, override)
		return
	}

	body := map[string]any{
		"Username": c.Config.Username,
		"Pw":       c.Config.Password,
	}
	resolvedSource, headers := c.resolveIdentityHeaders(reqCtx, identity, override)
	if c.logger != nil {
		isPassthrough := c.Config.SpoofClient == "passthrough"
		mode := c.Config.SpoofClient
		if isPassthrough {
			mode = "passthrough"
		}
		c.logger.Infof("[%s] Authenticating: mode=%s source=%s", c.Name, mode, diagnosticIdentitySource(resolvedSource))
	}
	headers.Set("Content-Type", "application/json")
	// A username/password login authenticates with the body and presents no user
	// ID, and must never reuse a client token or a previous upstream token.
	resp, err := c.doRequestForMode(ctx, reqCtx, http.MethodPost, "/Users/AuthenticateByName", nil, body, headers, false, authModePasswordLogin)
	if err != nil {
		if c.logger != nil {
			c.logger.Errorf("[%s] Login failed: %s", c.Name, err.Error())
		}
		c.setOffline(err.Error())
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if c.logger != nil {
			c.logger.Errorf("[%s] Login failed: %s", c.Name, resp.Status)
			if c.Config.SpoofClient == "passthrough" && (resp.StatusCode == 401 || resp.StatusCode == 403) {
				c.logger.Warnf("[%s] Passthrough authentication rejected: status=%d source=%s", c.Name, resp.StatusCode, diagnosticIdentitySource(resolvedSource))
			}
		}
		c.setOffline(resp.Status)
		return
	}
	var payload map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		if c.logger != nil {
			c.logger.Errorf("[%s] Login response decode failed: %s", c.Name, err.Error())
		}
		c.setOffline(err.Error())
		return
	}
	accessToken, _ := payload["AccessToken"].(string)
	userBlock, _ := payload["User"].(map[string]any)
	userID, _ := userBlock["Id"].(string)
	if accessToken == "" || userID == "" {
		if c.logger != nil {
			c.logger.Errorf("[%s] Login failed: response missing token or user id", c.Name)
		}
		c.setOffline("AuthenticateByName response missing token or user id")
		return
	}
	if c.logger != nil {
		c.logger.Infof("[%s] Login success", c.Name)
	}
	c.setOnlineWithIdentity(accessToken, userID, identitySnapshot{Source: resolvedSource, Headers: headers})
	c.recordSuccessfulIdentity(identity, resolvedSource, headers)
}

// validateAPIKey confirms an API key by reading the upstream user object. Only
// the HTTP status is surfaced to callers: LastError is returned to the admin API,
// and the body of an admin-supplied host is not something to reflect there.
// The override carries the captured client identity that triggered a deferred
// retry; without it the request would resolve no identity at all and leave as a
// bare request, which is exactly what the deferred-login design forbids.
func (c *UpstreamClient) validateAPIKey(ctx context.Context, reqCtx *RequestContext, identity *ClientIdentityService, override http.Header) {
	source, resolved := c.resolveIdentityHeaders(reqCtx, identity, override)
	if c.logger != nil {
		c.logger.Infof("[%s] API key validation: source=%s", c.Name, diagnosticIdentitySource(source))
	}
	resp, err := c.doRequestForMode(ctx, reqCtx, http.MethodGet, "/Users/Me", nil, nil, resolved, false, authModeAPIKeyValidation)
	if err != nil {
		if c.logger != nil {
			c.logger.Errorf("[%s] API key validation failed: %s", c.Name, err.Error())
		}
		c.setOffline(err.Error())
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if c.logger != nil {
			c.logger.Errorf("[%s] API key validation failed: %s", c.Name, resp.Status)
		}
		c.setOffline(resp.Status)
		return
	}
	var data map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		if c.logger != nil {
			c.logger.Errorf("[%s] API key validation failed: decode Users/Me: %s", c.Name, err.Error())
		}
		c.setOffline("Users/Me response decode failed")
		return
	}
	userID, _ := data["Id"].(string)
	if userID == "" {
		if c.logger != nil {
			c.logger.Errorf("[%s] API key validation failed: Users/Me response missing Id", c.Name)
		}
		c.setOffline("Users/Me response missing Id")
		return
	}
	if c.logger != nil {
		c.logger.Infof("[%s] API key auth success", c.Name)
	}
	c.setOnlineWithIdentity(strings.TrimSpace(c.Config.APIKey), userID, identitySnapshot{Source: source, Headers: resolved})
	// A successful API-key validation is a passthrough login success too:
	// persist the resolved identity so every later request (web clients
	// included) resolves it from last-success / captured-latest instead of
	// leaving with no client identity at all.
	c.recordSuccessfulIdentity(identity, source, resolved)
}

func (c *UpstreamClient) RequestJSON(ctx context.Context, reqCtx *RequestContext, identity *ClientIdentityService, method, path string, params url.Values, body any) (any, error) {
	resp, err := c.doRequest(ctx, reqCtx, method, path, params, body, c.requestHeaders(reqCtx, identity), false)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// The body is only echoed into the error message, so a bounded read is enough.
		// The structured error carries the status code so a scanner can branch on a
		// window-too-large 400 without string matching (plan §6.2).
		payload, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		return nil, &upstreamResponseError{StatusCode: resp.StatusCode, Method: method, Path: path, Detail: trimErrorDetail(payload)}
	}
	var decoded any
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return nil, err
	}
	return decoded, nil
}

func (c *UpstreamClient) Stream(ctx context.Context, reqCtx *RequestContext, identity *ClientIdentityService, path string, params url.Values, extraHeaders ...http.Header) (*http.Response, error) {
	snapshot := c.resolveIdentitySnapshot(reqCtx, identity, nil)
	return c.StreamWithIdentity(ctx, reqCtx, path, params, snapshot, extraHeaders...)
}

// StreamWithIdentity sends a media read with an identity resolved once for this
// playback request. The same snapshot must be passed to every manual follow of
// the response, so the upstream sees one client identity for the whole fetch
// instead of a captured value that may have moved in between.
func (c *UpstreamClient) StreamWithIdentity(ctx context.Context, reqCtx *RequestContext, path string, params url.Values, snapshot identitySnapshot, extraHeaders ...http.Header) (*http.Response, error) {
	headers := cloneHeader(snapshot.Headers)
	// Transport headers are merged through the public allow-list: a Range or
	// Accept header may never replace the identity fields of the snapshot.
	for _, extra := range extraHeaders {
		mergePublicOutboundHeaders(headers, extra)
	}
	return c.doRequest(ctx, reqCtx, http.MethodGet, path, params, nil, headers, true)
}

// resolveStreamRedirectTarget classifies a 3xx the upstream answered on a stream
// request. It returns the absolute target and whether this proxy must fetch it
// itself: a target on the proxy's own address would loop, and one on the
// upstream's own hosts would leak the upstream's address and identity to the
// client. Only a target on an unrelated host — the signed CDN link of a STRM
// source, typically — is safe to hand over. The target is reported in both cases.
func (c *UpstreamClient) resolveStreamRedirectTarget(resp *http.Response, proxyHost string) (string, bool) {
	location := strings.TrimSpace(resp.Header.Get("Location"))
	if location == "" {
		return "", true
	}
	var base *url.URL
	if resp.Request != nil && resp.Request.URL != nil {
		base = resp.Request.URL
	}
	abs, err := resolveRedirectTarget(base, location)
	if err != nil || abs == nil || c.classifyOutboundTarget(abs) == outboundTargetRejected {
		return "", true
	}
	if proxyHost != "" && strings.EqualFold(abs.Host, proxyHost) {
		return abs.String(), true
	}
	for _, host := range c.selfHosts() {
		if strings.EqualFold(abs.Host, host) {
			return abs.String(), true
		}
	}
	return abs.String(), false
}

// fetchRedirectTarget retrieves one upstream redirect target through this
// proxy's own line. The target is taken as issued — the upstream built it for
// exactly this request — so the path and query stay as they are apart from a
// credential parameter that has to be refreshed from this hop's auth snapshot.
// Single hop on purpose: each response is returned as-is (ErrUseLastResponse),
// so the stream handler can re-evaluate every hop — an internal hop that
// bounces to an external CDN must still reach the player instead of being
// silently relayed from here.
//
// snapshot is the identity resolved once for the whole fetch. Reusing it is what
// keeps a same-origin follow from degrading to the Go default User-Agent after
// the first hop.
func (c *UpstreamClient) fetchRedirectTarget(ctx context.Context, reqCtx *RequestContext, rawURL string, snapshot identitySnapshot, transport http.Header) (*http.Response, error) {
	return c.sendOutboundHop(ctx, reqCtx, rawURL, outboundHop{
		Method:     http.MethodGet,
		Headers:    snapshot.Headers,
		Transport:  transport,
		Provenance: requestContextResourceQueryProvenance(reqCtx).deriveRedirect(rawURL, resourceQueryPrivateTokens(reqCtx, c.authSnapshot().AccessToken)...),
	})
}

// isSelfURL reports whether rawURL points at one of this upstream's own
// configured origins — scheme, host and effective port all matching — so its
// credentials may be attached. External/CDN targets return false, and so does a
// URL that only shares the host with a configured origin but arrives over a
// different scheme or port (plan §7.3: the trust set is the configured origins,
// not "same host").
func (c *UpstreamClient) isSelfURL(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	origin := canonicalURLOrigin(u)
	if origin == "" {
		return false
	}
	for _, base := range c.selfOrigins() {
		if origin == base {
			return true
		}
	}
	return false
}

// selfOrigins lists the canonical origins this upstream answers on, across its
// API base and its live stream bases. The proxy's own address is per-request and
// is passed to the redirect classifier separately.
func (c *UpstreamClient) selfOrigins() []string {
	c.mu.RLock()
	bases := append([]string{c.BaseURL, c.StreamBaseURL}, c.StreamBaseURLs...)
	c.mu.RUnlock()
	origins := make([]string, 0, len(bases))
	seen := map[string]bool{}
	for _, base := range bases {
		u, err := url.Parse(base)
		if err != nil || u.Host == "" {
			continue
		}
		origin := canonicalURLOrigin(u)
		if origin == "" || seen[origin] {
			continue
		}
		seen[origin] = true
		origins = append(origins, origin)
	}
	return origins
}

// canonicalURLOrigin renders a URL's origin with the scheme's default port made
// explicit, so http://h and http://h:80 are one origin and https://h and
// https://h:443 are another. A plaintext URL never canonicalises onto an HTTPS
// origin: the scheme is part of the identity.
func canonicalURLOrigin(u *url.URL) string {
	if u == nil || u.Hostname() == "" {
		return ""
	}
	port := u.Port()
	if port == "" {
		switch u.Scheme {
		case "http":
			port = "80"
		case "https":
			port = "443"
		default:
			return ""
		}
	}
	return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Hostname()) + ":" + port
}

// selfHosts lists the hosts this upstream answers on, across its API and stream
// bases. The proxy's own address is per-request and passed in separately.
func (c *UpstreamClient) selfHosts() []string {
	bases := append([]string{c.BaseURL}, c.streamBaseCandidates()...)
	hosts := make([]string, 0, len(bases))
	seen := map[string]bool{}
	for _, base := range bases {
		u, err := url.Parse(base)
		if err != nil || u.Host == "" || seen[u.Host] {
			continue
		}
		seen[u.Host] = true
		hosts = append(hosts, u.Host)
	}
	return hosts
}

// resolveRedirectTarget turns a possibly relative Location into an absolute URL,
// resolved against the request that produced it.
func resolveRedirectTarget(base *url.URL, location string) (*url.URL, error) {
	loc, err := url.Parse(location)
	if err != nil {
		return nil, err
	}
	if loc.IsAbs() {
		return loc, nil
	}
	if base == nil {
		return nil, errors.New("relative redirect target without a request URL")
	}
	return base.ResolveReference(loc), nil
}

func isRedirectStatus(status int) bool {
	switch status {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
		http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		return true
	}
	return false
}

// BuildURL prepares an upstream URL outside doRequest. It is used by the stream
// redirect path, whose client talks to the upstream directly, so it must apply
// exactly the same URL rules as a proxied request: identity normalization and one
// credential written from this snapshot.
func (c *UpstreamClient) BuildURL(path string, params url.Values, stream bool, reqCtx *RequestContext) (string, error) {
	return c.BuildURLForMode(path, params, stream, reqCtx, authModeNormal)
}

// BuildURLForMode is BuildURL with an explicit authentication mode.
func (c *UpstreamClient) BuildURLForMode(path string, params url.Values, stream bool, reqCtx *RequestContext, mode outboundAuthMode) (string, error) {
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	if c.Config.SpoofClient == "resident" && c.residentIdentity == nil {
		return "", newResidentIdentityMissingError()
	}
	base := c.BaseURL
	if stream {
		// The redirect hands one URL to the client for the whole playback, so it
		// must be a line that currently answers: take the first live candidate.
		if candidates := c.streamBaseCandidates(); len(candidates) > 0 {
			base = candidates[0]
		} else {
			base = c.StreamBaseURL
		}
	}
	fullURL, err := url.Parse(base + path)
	if err != nil {
		return "", newClientInputPreparationError("unparsable-url", "path")
	}
	query := fullURL.Query()
	for key, values := range params {
		for _, value := range values {
			query.Add(key, value)
		}
	}
	fullURL.RawQuery = query.Encode()

	auth := c.authSnapshot()
	if err := c.validatePlaybackAuth(reqCtx, auth); err != nil {
		return "", err
	}
	businessPath := strings.TrimPrefix(fullURL.Path, urlPathPrefix(base))
	if businessPath == "" {
		businessPath = "/"
	}
	policy := resolveOutboundPolicy(businessPath, http.MethodGet, stream, mode, base)
	result, err := prepareOutboundURLWithReport(fullURL, reqCtx, auth, policy)
	if err != nil {
		return "", err
	}
	return result.url.String(), nil
}

func (c *UpstreamClient) doRequest(ctx context.Context, reqCtx *RequestContext, method, path string, params url.Values, body any, headers http.Header, stream bool) (*http.Response, error) {
	return c.doRequestForMode(ctx, reqCtx, method, path, params, body, headers, stream, authModeNormal)
}

// doRequestForMode is the single exit for every HTTP request this proxy makes to
// an upstream. It freezes one authentication snapshot per request and uses that
// same snapshot for the URL, the body and the authentication headers, so a user
// ID can never be paired with another login's token.
//
// For stream requests with fallback stream bases configured, a connect-level
// failure (DNS, TCP, TLS — anything that never produced a response) is retried
// against the next base. Any HTTP response, including a 4xx or 5xx, stops the
// attempts: the upstream answered, so the failure is not the line's.
func (c *UpstreamClient) doRequestForMode(ctx context.Context, reqCtx *RequestContext, method, path string, params url.Values, body any, headers http.Header, stream bool, mode outboundAuthMode) (*http.Response, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if mode == "" {
		mode = authModeNormal
	}
	if c.Config.SpoofClient == "resident" && c.residentIdentity == nil {
		return nil, newResidentIdentityMissingError()
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	if !stream {
		return c.doRequestOnce(ctx, reqCtx, method, path, params, body, headers, stream, mode, c.BaseURL)
	}

	bases := c.streamBaseCandidates()
	var lastErr error
	for _, base := range bases {
		resp, err := c.doRequestOnce(ctx, reqCtx, method, path, params, body, headers, stream, mode, base)
		if err == nil {
			c.markStreamBaseAlive(base)
			return resp, nil
		}
		lastErr = err
		// A cancelled request is the client going away, not a dead line; and a
		// preparation error never touched the network. Neither justifies a retry.
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		if _, isPrep := asPreparationError(err); isPrep {
			return nil, err
		}
		c.markStreamBaseFailed(base)
		if c.logger != nil {
			c.logger.Warnf("[%s] Stream base %s failed (%s); trying next fallback", c.Name, base, redactURLInError(err))
		}
	}
	return nil, lastErr
}

// doRequestOnce performs one outbound request against one base URL. It is the
// body of the pre-fallback doRequestForMode; every identity rule it applies is
// unchanged.
func (c *UpstreamClient) doRequestOnce(ctx context.Context, reqCtx *RequestContext, method, path string, params url.Values, body any, headers http.Header, stream bool, mode outboundAuthMode, base string) (*http.Response, error) {
	if c.Config.SpoofClient == "resident" && c.residentIdentity == nil {
		return nil, newResidentIdentityMissingError()
	}
	auth := c.authSnapshot()
	if err := c.validatePlaybackAuth(reqCtx, auth); err != nil {
		return nil, err
	}
	if reqCtx != nil && reqCtx.Playback != nil {
		pinned := cloneHeader(reqCtx.Playback.Identity.Headers)
		mergePublicOutboundHeaders(pinned, headers)
		headers = pinned
	}
	policy := resolveOutboundPolicy(path, method, stream, mode, base)

	fullURL, err := url.Parse(base + path)
	if err != nil {
		return nil, newClientInputPreparationError("unparsable-url", "path")
	}
	query := fullURL.Query()
	for key, values := range params {
		for _, value := range values {
			query.Add(key, value)
		}
	}
	fullURL.RawQuery = query.Encode()

	preparedURL, err := prepareOutboundURLWithReport(fullURL, reqCtx, auth, policy)
	if err != nil {
		c.scheduleRecoveryForPreparationError(err)
		return nil, err
	}

	preparedBody, bodyOutcome, err := prepareOutboundBodyWithReport(body, reqCtx, auth, policy)
	if err != nil {
		c.scheduleRecoveryForPreparationError(err)
		return nil, err
	}

	var reader io.Reader
	bodyContentType := ""
	switch typed := preparedBody.(type) {
	case nil:
	case rawRequestBody:
		reader = bytes.NewReader(typed.data)
		bodyContentType = typed.contentType
	case *rawRequestBody:
		if typed != nil {
			reader = bytes.NewReader(typed.data)
			bodyContentType = typed.contentType
		}
	default:
		// The map decoder loses typed numbers, so an already-object body is
		// re-serialized directly rather than through the generic path.
		encoded, encodeErr := json.Marshal(preparedBody)
		if encodeErr != nil {
			return nil, encodeErr
		}
		reader = bytes.NewReader(encoded)
		bodyContentType = "application/json"
	}
	if reqCtx != nil {
		ctx = context.WithValue(ctx, requestContextKey{}, reqCtx)
	}
	request, err := http.NewRequestWithContext(ctx, method, preparedURL.url.String(), reader)
	if err != nil {
		return nil, err
	}
	var requestHeaders http.Header
	if headers != nil {
		c.applyResidentIdentity(headers)
		requestHeaders = prepareOutboundHeaders(headers, reqCtx, auth, mode)
	} else {
		// Defensive branch: no caller passes nil today. The process-wide service
		// keeps a passthrough upstream from degrading to the spoof profile here.
		requestHeaders = c.requestHeaders(reqCtx, activeIdentityService())
	}
	if bodyContentType != "" && requestHeaders.Get("Content-Type") == "" {
		requestHeaders.Set("Content-Type", bodyContentType)
	}
	for key, values := range requestHeaders {
		for _, value := range values {
			request.Header.Add(key, value)
		}
	}
	config := c.ConfigSnapshot()
	// Every request carries the purpose it is sent for, because the redirect
	// policy has to refuse a password-login body that would cross origins. The
	// policy also decides per hop whether the target may see this upstream's
	// identity and credential at all, so it is rebuilt from the current
	// configuration instead of a client built before an admin change.
	redirectPolicy := c.redirectPolicyFor(config.FollowRedirects, mode)
	if reqCtx != nil && reqCtx.Playback != nil && mode == authModeNormal {
		redirectPolicy = c.strictPlaybackRedirectPolicy(redirectPolicy, reqCtx)
	}
	timeout := c.httpClient.Timeout
	if stream {
		// Timeout stays 0 on purpose: a stream is supposed to stay open for the length of a
		// film, and the overall deadline would cut long playback short. What is bounded is
		// the wait for response headers (transport-level, see upstreamHeaderTimeout)
		// and the request's own lifetime — the request carries the client's context, so a
		// client that goes away takes the upstream call with it.
		timeout = 0
		if config.DirectRedirect {
			// The stream handler decides per response what a redirect means: an
			// external target goes to the client, an internal one is fetched here.
			// The probe must therefore see the 3xx itself instead of net/http
			// following it transparently.
			redirectPolicy = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		}
	}
	client := &http.Client{Transport: c.transport, Timeout: timeout, CheckRedirect: redirectPolicy}
	if c.logger != nil {
		changed := preparedURL.changed
		if bodyOutcome.changed {
			changed = append(changed, carrierBody)
		}
		c.logger.Debugf("[%s] -> %s %s (stream=%v, changed=%s, body=%s)",
			c.Name, method, formatOutboundURLForLog(preparedURL.url.String()), stream,
			outboundChangeSummary(changed), bodyOutcome.support)
	}
	// This is a reverse proxy forwarding client requests to admin-configured upstream Emby
	// servers. The base URL (c.BaseURL/c.StreamBaseURL) is set by the administrator.
	// User-controlled path segments are inherent to proxy functionality.
	resp, doErr := client.Do(request) // CodeQL: intentional proxy forwarding to admin-configured upstream
	if doErr != nil {
		// net/http's redirect error includes the Location URL. Replace it before
		// any diagnostic output, not merely before returning it to the caller.
		if errors.Is(doErr, errUpstreamLoginRedirectUntrusted) {
			doErr = errUpstreamLoginRedirectUntrusted
		}
		if c.logger != nil {
			c.logger.Errorf("[%s] Request failed: %s %s: %s", c.Name, method,
				formatOutboundURLForLog(preparedURL.url.String()), redactURLInError(doErr))
		}
		// A refused password-login hop must not carry the target URL: net/http
		// wraps it in a *url.Error built from the external Location, and this
		// error reaches LastError, the admin API and the log.
		if errors.Is(doErr, errUpstreamLoginRedirectUntrusted) {
			return nil, errUpstreamLoginRedirectUntrusted
		}
		// net/http builds this error from the request URL, which for a stream
		// request carries the upstream token. Handlers surface upstream errors to
		// the client, so the wrapped message is redacted here rather than at each
		// of them; Unwrap keeps errors.Is/As working for cancellation checks.
		return nil, &redactedError{err: doErr}
	}
	if c.logger != nil {
		c.logger.Debugf("[%s] <- %s %s %d", c.Name, method, formatOutboundURLForLog(preparedURL.url.String()), resp.StatusCode)
	}
	if (resp.StatusCode == 401 || resp.StatusCode == 403) &&
		!isUpstreamLoginPath(path) {
		c.requestAuthRecovery()
	}
	return resp, nil
}

// scheduleRecoveryForPreparationError handles a preparation error that never
// reached the upstream. A missing authentication state has no upstream response
// to trigger the existing 401/403 recovery, so the debounced recovery callback is
// scheduled here instead. Client-input errors and unclassified values never
// trigger a reconnect.
func (c *UpstreamClient) scheduleRecoveryForPreparationError(err error) {
	prep, ok := asPreparationError(err)
	if !ok || prep.Kind != "missing-upstream-auth-state" {
		return
	}
	c.requestAuthRecovery()
}
