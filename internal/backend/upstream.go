package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

var sharedTransport = &http.Transport{
	Proxy:                 http.ProxyFromEnvironment,
	MaxIdleConns:          64,
	MaxIdleConnsPerHost:   16,
	IdleConnTimeout:       90 * time.Second,
	TLSHandshakeTimeout:   10 * time.Second,
	ExpectContinueTimeout: 1 * time.Second,
	// Refuse link-local destinations (cloud instance metadata) while keeping
	// loopback and RFC1918 upstreams reachable.
	DialContext: upstreamTransportDialer(),
	// Disable automatic decompression to ensure we don't return decompressed bytes
	// with mismatching Content-Length headers back to the client.
	DisableCompression: true,
}

var embyClientHeaders = map[string]string{
	"User-Agent":            "Emby Aggregator/1.0",
	"X-Emby-Client":         "Emby Aggregator",
	"X-Emby-Client-Version": "1.0.0",
	"X-Emby-Device-Name":    "EmbyInOne",
	"X-Emby-Device-Id":      "emby-in-one-proxy",
	"Accept":                "application/json",
}

var spoofProfiles = map[string]map[string]string{
	"infuse": {
		"User-Agent":            "Infuse/7.7.1 (iPhone; iOS 17.4.1; Scale/3.00)",
		"X-Emby-Client":         "Infuse",
		"X-Emby-Client-Version": "7.7.1",
		"X-Emby-Device-Name":    "iPhone",
		"X-Emby-Device-Id":      "infuse-spoof-id",
	},
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
	mu            sync.RWMutex
	ServerIndex   int
	Name          string
	BaseURL       string
	StreamBaseURL string
	Online        bool
	UserID        string
	AccessToken   string
	LastError     string
	Config        UpstreamConfig
	serverKey     string
	httpClient    *http.Client
	transport     http.RoundTripper // per-client transport (shared or proxy-specific)
	logger        *Logger
	timeouts      TimeoutsConfig
	recoveryMu    sync.Mutex
	lastRecovery  time.Time
	onAuthError   func(c *UpstreamClient)
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
		if client.Config.SpoofClient == "passthrough" && client.Config.APIKey == "" {
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
			old.mu.RLock()
			newClient.AccessToken = old.AccessToken
			newClient.UserID = old.UserID
			newClient.Online = old.Online
			newClient.LastError = old.LastError
			old.mu.RUnlock()
		}
		clients = append(clients, newClient)
	}
	p.mu.Lock()
	if p.identity == nil {
		p.identity = activeIdentityService()
	}
	p.clients = clients
	p.mu.Unlock()
	p.restartHealthChecks(cfg.Timeouts)
}

func (p *UpstreamPool) handleUpstreamAuthError(c *UpstreamClient) {
	c.recoveryMu.Lock()
	if time.Since(c.lastRecovery) < recoveryDebounce {
		c.recoveryMu.Unlock()
		return
	}
	c.lastRecovery = time.Now()
	c.recoveryMu.Unlock()
	if !p.canAttemptPassthroughLogin(c) {
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

// buildProxyTransport creates an http.Transport that routes requests through the given proxy URL.
// Returns (transport, true) on success, or (sharedTransport, false) on failure.
func buildProxyTransport(proxyURL string, logger *Logger, serverName string) (http.RoundTripper, bool) {
	parsed, err := url.Parse(proxyURL)
	if err != nil {
		if logger != nil {
			logger.Errorf("[%s] Invalid proxy URL %q: %s, falling back to direct", serverName, proxyURL, err)
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
	streamBaseURL := baseURL
	if strings.TrimSpace(upstream.StreamingURL) != "" {
		streamBaseURL = strings.TrimRight(strings.TrimSpace(upstream.StreamingURL), "/")
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

	return &UpstreamClient{
		ServerIndex:   index,
		Name:          upstream.Name,
		BaseURL:       baseURL,
		StreamBaseURL: streamBaseURL,
		Config:        upstream,
		serverKey:     StableUpstreamKey(upstream),
		httpClient: &http.Client{
			Transport:     transport,
			Timeout:       time.Duration(timeouts.API) * time.Millisecond,
			CheckRedirect: redirectPolicy(upstream.FollowRedirects),
		},
		transport: transport,
		logger:    logger,
		timeouts:  timeouts,
	}
}

func (p *UpstreamPool) GetClient(index int) *UpstreamClient {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if index < 0 || index >= len(p.clients) {
		return nil
	}
	return p.clients[index]
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

func (p *UpstreamPool) canAttemptPassthroughLogin(client *UpstreamClient) bool {
	if client == nil || client.Config.SpoofClient != "passthrough" || strings.TrimSpace(client.Config.APIKey) != "" {
		return true
	}
	identity := p.identityService()
	return identity != nil && identity.HasCapturedHeaders(client.serverKey)
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

func (p *UpstreamPool) Reconnect(index int) *UpstreamClient {
	client := p.GetClient(index)
	if client == nil {
		return nil
	}
	if !p.canAttemptPassthroughLogin(client) {
		if p.logger != nil {
			p.logger.Infof("[%s] Passthrough reconnect skipped — waiting for real client", client.Name)
		}
		copyClient := client.snapshot()
		return &copyClient
	}
	client.Login(context.Background(), nil, p.identityService())
	copyClient := client.snapshot()
	return &copyClient
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

func (c *UpstreamClient) snapshot() UpstreamClient {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return UpstreamClient{
		ServerIndex:   c.ServerIndex,
		Name:          c.Name,
		BaseURL:       c.BaseURL,
		StreamBaseURL: c.StreamBaseURL,
		Online:        c.Online,
		UserID:        c.UserID,
		AccessToken:   c.AccessToken,
		LastError:     c.LastError,
		Config:        c.Config,
		serverKey:     c.serverKey,
		httpClient:    c.httpClient,
		transport:     c.transport,
		logger:        c.logger,
		timeouts:      c.timeouts,
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
	if ctx == nil {
		ctx = context.Background()
	}
	// Bound the whole login attempt, API-key validation included, by timeouts.login.
	// That setting used to be ignored, so an upstream that logs in slowly now fails
	// earlier than it did when every request inherited timeouts.api.
	loginCtx, cancelLogin := context.WithTimeout(ctx, c.loginTimeout())
	defer cancelLogin()
	ctx = loginCtx

	if strings.TrimSpace(c.Config.APIKey) != "" {
		if c.logger != nil {
			c.logger.Infof("[%s] Authenticating with API key", c.Name)
		}
		c.mu.Lock()
		c.AccessToken = strings.TrimSpace(c.Config.APIKey)
		c.mu.Unlock()
		c.validateAPIKey(ctx, reqCtx, identity)
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
		c.logger.Infof("[%s] Authenticating: user=%q mode=%s source=%s", c.Name, c.Config.Username, mode, resolvedSource)
		c.logger.Infof("[%s] Login identity: Client=%q Device=%q DeviceId=%q Version=%q",
			c.Name, headers.Get("X-Emby-Client"), headers.Get("X-Emby-Device-Name"),
			headers.Get("X-Emby-Device-Id"), headers.Get("X-Emby-Client-Version"))
		c.logger.Debugf("[%s] Login User-Agent: %s", c.Name, headers.Get("User-Agent"))
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
				c.logger.Warnf("[%s] Passthrough %d — sent identity: Client=%q, Device=%q, DeviceId=%q, UA=%q",
					c.Name, resp.StatusCode, headers.Get("X-Emby-Client"), headers.Get("X-Emby-Device-Name"),
					headers.Get("X-Emby-Device-Id"), headers.Get("User-Agent"))
				c.logger.Warnf("[%s] Passthrough %d — header source: %q", c.Name, resp.StatusCode, resolvedSource)
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
		c.logger.Infof("[%s] Login success, userId=%s", c.Name, userID)
	}
	c.setOnline(accessToken, userID)
	c.recordSuccessfulIdentity(identity, resolvedSource, headers)
}

// validateAPIKey confirms an API key by reading the upstream user object. Only
// the HTTP status is surfaced to callers: LastError is returned to the admin API,
// and the body of an admin-supplied host is not something to reflect there.
func (c *UpstreamClient) validateAPIKey(ctx context.Context, reqCtx *RequestContext, identity *ClientIdentityService) {
	resp, err := c.doRequestForMode(ctx, reqCtx, http.MethodGet, "/Users/Me", nil, nil, c.requestHeaders(reqCtx, identity), false, authModeAPIKeyValidation)
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
		c.logger.Infof("[%s] API key auth success, userId=%s", c.Name, userID)
	}
	c.setOnline(strings.TrimSpace(c.Config.APIKey), userID)
}

func (c *UpstreamClient) setOnline(token, userID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.AccessToken = token
	c.UserID = userID
	c.Online = true
	c.LastError = ""
}

func (c *UpstreamClient) setOffline(message string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Online = false
	c.LastError = message
}

func (c *UpstreamClient) RequestJSON(ctx context.Context, reqCtx *RequestContext, identity *ClientIdentityService, method, path string, params url.Values, body any) (any, error) {
	resp, err := c.doRequest(ctx, reqCtx, method, path, params, body, c.requestHeaders(reqCtx, identity), false)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		payload, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("upstream %s %s failed: %s %s", method, path, resp.Status, strings.TrimSpace(string(payload)))
	}
	var decoded any
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return nil, err
	}
	return decoded, nil
}

func (c *UpstreamClient) Stream(ctx context.Context, reqCtx *RequestContext, identity *ClientIdentityService, path string, params url.Values, extraHeaders ...http.Header) (*http.Response, error) {
	headers := c.identityHeaders(reqCtx, identity)
	for _, extra := range extraHeaders {
		for key, values := range extra {
			for _, v := range values {
				headers.Set(key, v)
			}
		}
	}
	return c.doRequest(ctx, reqCtx, http.MethodGet, path, params, nil, headers, true)
}

// redirectPolicy returns the redirect handling for an upstream. Following is the
// default and is delegated to net/http, which caps the chain at 10 hops; passing
// nil keeps exactly that. When the administrator turns following off, the request
// stops at the upstream's redirect and is reported as an upstream failure: the
// proxy never hands an upstream redirect target to the client as a successful
// response, because that target can carry the upstream's own credentials.
func redirectPolicy(follow bool) func(*http.Request, []*http.Request) error {
	if follow {
		return nil
	}
	return func(*http.Request, []*http.Request) error {
		return errUpstreamRedirectNotFollowed
	}
}

// getAccessToken returns the upstream's access token in a thread-safe manner.
func (c *UpstreamClient) getAccessToken() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.AccessToken
}

// healthCheckTimeout bounds a single health-check login. c.timeouts is written once
// at construction, so it needs no lock. Note the http.Client timeout still applies,
// so this can shorten a probe but never extend it past timeouts.API.
func (c *UpstreamClient) healthCheckTimeout() time.Duration {
	return time.Duration(c.timeouts.HealthCheck) * time.Millisecond
}

// loginTimeout bounds a login attempt or an API-key validation for this upstream.
func (c *UpstreamClient) loginTimeout() time.Duration {
	return time.Duration(c.timeouts.Login) * time.Millisecond
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
	base := c.BaseURL
	if stream {
		base = c.StreamBaseURL
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
func (c *UpstreamClient) doRequestForMode(ctx context.Context, reqCtx *RequestContext, method, path string, params url.Values, body any, headers http.Header, stream bool, mode outboundAuthMode) (*http.Response, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if mode == "" {
		mode = authModeNormal
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	base := c.BaseURL
	if stream {
		base = c.StreamBaseURL
	}

	auth := c.authSnapshot()
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
	request, err := http.NewRequestWithContext(ctx, method, preparedURL.url.String(), reader)
	if err != nil {
		return nil, err
	}
	var requestHeaders http.Header
	if headers != nil {
		requestHeaders = prepareOutboundHeaders(headers, reqCtx, auth, mode)
	} else {
		requestHeaders = c.requestHeaders(reqCtx, nil)
	}
	if bodyContentType != "" && requestHeaders.Get("Content-Type") == "" {
		requestHeaders.Set("Content-Type", bodyContentType)
	}
	for key, values := range requestHeaders {
		for _, value := range values {
			request.Header.Add(key, value)
		}
	}
	client := c.httpClient
	if stream {
		client = &http.Client{Transport: c.transport, Timeout: 0, CheckRedirect: redirectPolicy(c.Config.FollowRedirects)}
	}
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
		if c.logger != nil {
			c.logger.Errorf("[%s] Request failed: %s %s: %s", c.Name, method,
				formatOutboundURLForLog(preparedURL.url.String()), redactURLInError(doErr))
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
		!isUpstreamLoginPath(path) && c.onAuthError != nil {
		go c.onAuthError(c)
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
	if c.onAuthError == nil {
		return
	}
	go c.onAuthError(c)
}

func (c *UpstreamClient) identityHeaders(reqCtx *RequestContext, identity *ClientIdentityService) http.Header {
	_, headers := c.resolveIdentityHeaders(reqCtx, identity, nil)
	return headers
}

func (c *UpstreamClient) resolveIdentityHeaders(reqCtx *RequestContext, identity *ClientIdentityService, override http.Header) (string, http.Header) {
	headers := http.Header{}
	if c.Config.SpoofClient == "passthrough" {
		if hasPassthroughIdentity(override) {
			return "override", mergePassthroughHeaders(override)
		}
		if identity != nil {
			var live http.Header
			var token string
			if reqCtx != nil {
				live = reqCtx.Headers
				token = reqCtx.ProxyToken
			}
			resolved := identity.ResolvePassthroughHeadersForServer(live, token, c.serverKey)
			return resolved.Source, resolved.Headers
		}
		return "infuse-fallback", mergePassthroughHeaders(http.Header{})
	}
	profile := embyClientHeaders
	if spoofed, ok := spoofProfiles[c.Config.SpoofClient]; ok {
		profile = spoofed
	} else if c.Config.SpoofClient == "custom" {
		profile = map[string]string{
			"User-Agent":            c.Config.CustomUserAgent,
			"X-Emby-Client":         c.Config.CustomClient,
			"X-Emby-Client-Version": c.Config.CustomClientVersion,
			"X-Emby-Device-Name":    c.Config.CustomDeviceName,
			"X-Emby-Device-Id":      c.Config.CustomDeviceId,
		}
	}
	for key, value := range profile {
		if value != "" {
			headers.Set(key, value)
		}
	}
	return c.Config.SpoofClient, headers
}

// requestHeaders builds the caller-side header set for one request from the same
// auth snapshot it will be sent with.
func (c *UpstreamClient) requestHeaders(reqCtx *RequestContext, identity *ClientIdentityService) http.Header {
	if c.Config.SpoofClient == "passthrough" {
		return c.passthroughRequestHeaders(reqCtx, identity)
	}
	headers := c.identityHeaders(reqCtx, identity)
	if snapshot := c.authSnapshot(); snapshot.AccessToken != "" {
		headers.Set("X-Emby-Token", snapshot.AccessToken)
	}
	return headers
}

// passthroughRequestHeaders resolves the identity a passthrough upstream should
// see and installs that snapshot's credential. The device parameters are kept in
// the compound header so the upstream still receives the client's real device
// identity; its UserId and Token are replaced by the snapshot's own.
func (c *UpstreamClient) passthroughRequestHeaders(reqCtx *RequestContext, identity *ClientIdentityService) http.Header {
	_, headers := c.resolveIdentityHeaders(reqCtx, identity, nil)
	snapshot := c.authSnapshot()
	if snapshot.AccessToken != "" {
		headers.Set("X-Emby-Token", snapshot.AccessToken)
	}
	compound := headers.Get("X-Emby-Authorization")
	if compound == "" {
		compound = headers.Get("Authorization")
	}
	headers.Del("Authorization")
	headers.Del("X-Emby-Authorization")
	if compound != "" {
		if parsed, ok := parseAuthorizationIdentityStrict(compound); ok {
			headers.Set("X-Emby-Authorization", canonicalAuthorizationHeader(snapshot.UserID,
				parsed["Device"], parsed["DeviceId"], parsed["Version"], parsed["Client"]))
		}
	}
	if headers.Get("X-Emby-Authorization") == "" && snapshot.UserID != "" {
		headers.Set("X-Emby-Authorization", canonicalAuthorizationHeader(snapshot.UserID, "", "", "", ""))
	}
	return headers
}

func (c *UpstreamClient) recordSuccessfulIdentity(identity *ClientIdentityService, source string, headers http.Header) {
	if identity == nil || c.Config.SpoofClient != "passthrough" || !hasPassthroughIdentity(headers) {
		return
	}
	if source == "infuse-fallback" {
		return
	}
	identity.SaveLastSuccess(c.serverKey, headers)
	switch source {
	case "live-request", "captured-token", "captured-latest", "override":
		identity.SaveLatestCapturedHeaders(headers)
	}
}
