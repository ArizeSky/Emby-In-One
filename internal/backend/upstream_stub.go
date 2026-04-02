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
	// Disable automatic decompression to ensure we don't return decompressed bytes
	// with mismatching Content-Length headers back to the client.
	DisableCompression: true,
}

const recoveryDebounce = 30 * time.Second

// isUpstreamLoginPath returns true for paths used during upstream login,
// to avoid triggering recovery loops when login itself returns 401.
func isUpstreamLoginPath(path string) bool {
	return path == "/Users/AuthenticateByName" || path == "/Users/Me"
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
	onAuthError   func(c *UpstreamClient) // set by pool for auto-recovery
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
		DisableCompression:    true,
	}, true
}

func newUpstreamClient(cfg Config, upstream UpstreamConfig, index int, logger *Logger) *UpstreamClient {
	timeouts := cfg.Timeouts
	if timeouts.API == 0 {
		timeouts.API = 30000
	}
	if timeouts.Login == 0 {
		timeouts.Login = 10000
	}
	if timeouts.HealthCheck == 0 {
		timeouts.HealthCheck = 10000
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
			Transport: transport,
			Timeout:   time.Duration(timeouts.API) * time.Millisecond,
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

// handleUpstreamAuthError is called (in a goroutine) when a non-login upstream
// request receives 401/403 — it marks the client offline and triggers re-login.
func (p *UpstreamPool) handleUpstreamAuthError(c *UpstreamClient) {
	c.recoveryMu.Lock()
	if time.Since(c.lastRecovery) < recoveryDebounce {
		c.recoveryMu.Unlock()
		return
	}
	c.lastRecovery = time.Now()
	c.recoveryMu.Unlock()

	if p.logger != nil {
		p.logger.Warnf("[%s] Upstream auth error on normal request, triggering recovery re-login", c.Name)
	}
	c.setOffline("upstream auth expired")
	c.Login(context.Background(), nil, p.identityService())
	if c.IsOnline() && p.logger != nil {
		p.logger.Infof("[%s] Recovery re-login succeeded", c.Name)
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
	if strings.TrimSpace(c.Config.APIKey) != "" {
		if c.logger != nil {
			c.logger.Infof("[%s] Authenticating with API key", c.Name)
		}
		c.mu.Lock()
		c.AccessToken = strings.TrimSpace(c.Config.APIKey)
		c.mu.Unlock()
		payload, err := c.RequestJSON(ctx, reqCtx, identity, http.MethodGet, "/Users/Me", nil, nil)
		if err != nil {
			if c.logger != nil {
				c.logger.Errorf("[%s] API key validation failed: %s", c.Name, err.Error())
			}
			c.setOffline(err.Error())
			return
		}
		if data, ok := payload.(map[string]any); ok {
			if id, ok := data["Id"].(string); ok && id != "" {
				if c.logger != nil {
					c.logger.Infof("[%s] API key auth success, userId=%s", c.Name, id)
				}
				c.setOnline(strings.TrimSpace(c.Config.APIKey), id)
				return
			}
		}
		if c.logger != nil {
			c.logger.Errorf("[%s] API key validation failed: Users/Me response missing Id", c.Name)
		}
		c.setOffline("Users/Me response missing Id")
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
	headers.Set("X-Emby-Authorization", fmt.Sprintf("Emby UserId=\"\", Client=\"%s\", Device=\"%s\", DeviceId=\"%s\", Version=\"%s\"", headers.Get("X-Emby-Client"), headers.Get("X-Emby-Device-Name"), headers.Get("X-Emby-Device-Id"), headers.Get("X-Emby-Client-Version")))
	resp, err := c.doRequest(ctx, http.MethodPost, "/Users/AuthenticateByName", nil, body, headers, false)
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
	resp, err := c.doRequest(ctx, method, path, params, body, c.requestHeaders(reqCtx, identity), false)
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
	return c.doRequest(ctx, http.MethodGet, path, params, nil, headers, true)
}

// getAccessToken returns the upstream's access token in a thread-safe manner.
func (c *UpstreamClient) getAccessToken() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.AccessToken
}

func (c *UpstreamClient) BuildURL(path string, params url.Values, stream bool) string {
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	base := c.BaseURL
	if stream {
		base = c.StreamBaseURL
	}
	fullURL, _ := url.Parse(base + path)
	query := fullURL.Query()
	for key, values := range params {
		for _, value := range values {
			query.Add(key, value)
		}
	}
	fullURL.RawQuery = query.Encode()
	return fullURL.String()
}

func (c *UpstreamClient) doRequest(ctx context.Context, method, path string, params url.Values, body any, headers http.Header, stream bool) (*http.Response, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	base := c.BaseURL
	if stream {
		base = c.StreamBaseURL
	}
	fullURL, err := url.Parse(base + path)
	if err != nil {
		return nil, err
	}
	query := fullURL.Query()
	for key, values := range params {
		for _, value := range values {
			query.Add(key, value)
		}
	}
	fullURL.RawQuery = query.Encode()

	var reader io.Reader
	bodyContentType := ""
	switch typed := body.(type) {
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
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(encoded)
		bodyContentType = "application/json"
	}
	request, err := http.NewRequestWithContext(ctx, method, fullURL.String(), reader)
	if err != nil {
		return nil, err
	}
	var requestHeaders http.Header
	if headers != nil {
		requestHeaders = cloneHeader(headers)
		c.mu.RLock()
		accessToken := c.AccessToken
		c.mu.RUnlock()
		if accessToken != "" {
			requestHeaders.Set("X-Emby-Token", accessToken)
		}
	} else {
		requestHeaders = c.requestHeaders(nil, nil)
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
		client = &http.Client{Transport: c.transport, Timeout: 0}
	}
	if c.logger != nil {
		c.logger.Debugf("[%s] → %s %s (stream=%v)", c.Name, method, path, stream)
	}
	resp, doErr := client.Do(request)
	if doErr != nil {
		if c.logger != nil {
			c.logger.Errorf("[%s] Request failed: %s %s: %s", c.Name, method, path, doErr.Error())
		}
		return nil, doErr
	}
	if c.logger != nil {
		c.logger.Debugf("[%s] ← %s %s %d", c.Name, method, path, resp.StatusCode)
	}
	// Auto-recovery: if upstream returns 401/403 on a non-login request,
	// trigger async re-login to restore the session.
	if (resp.StatusCode == 401 || resp.StatusCode == 403) && !isUpstreamLoginPath(path) && c.onAuthError != nil {
		go c.onAuthError(c)
	}
	return resp, nil
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

func (c *UpstreamClient) requestHeaders(reqCtx *RequestContext, identity *ClientIdentityService) http.Header {
	headers := c.identityHeaders(reqCtx, identity)
	c.mu.RLock()
	accessToken := c.AccessToken
	c.mu.RUnlock()
	if accessToken != "" {
		headers.Set("X-Emby-Token", accessToken)
	}
	return headers
}

func (c *UpstreamClient) recordSuccessfulIdentity(identity *ClientIdentityService, source string, headers http.Header) {
	if identity == nil || c.Config.SpoofClient != "passthrough" || !hasPassthroughIdentity(headers) {
		return
	}
	identity.SaveLastSuccess(c.serverKey, headers)
	switch source {
	case "live-request", "captured-token", "captured-latest", "override":
		identity.SaveLatestCapturedHeaders(headers)
	}
}
