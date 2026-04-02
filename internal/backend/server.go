package backend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	allowMethods = "GET, POST, PUT, DELETE, OPTIONS"
	allowHeaders = "Content-Type, Authorization, X-Emby-Token, X-Emby-Authorization, X-Emby-Client, X-Emby-Client-Version, X-Emby-Device-Name, X-Emby-Device-Id"
)

type requestContextKey struct{}

type RequestContext struct {
	Headers    http.Header
	ProxyToken string
	ProxyUser  *tokenInfo
}

type App struct {
	ConfigStore *ConfigStore
	Logger      *Logger
	IDStore     *IDStore
	Identity    *ClientIdentityService
	Auth        *AuthManager
	Upstream    *UpstreamPool
	loginLimiter loginRateLimiter
}

// loginRateLimiter tracks per-IP failed login attempts.
type loginRateLimiter struct {
	mu       sync.Mutex
	attempts map[string]*loginAttempt
}

type loginAttempt struct {
	failures int
	lastFail time.Time
}

const (
	loginMaxFailures   = 5
	loginLockoutWindow = 15 * time.Minute
)

// checkAndRecord atomically checks rate limit and pre-records a failure.
// Returns true if the request is allowed. On successful auth, call recordSuccess to clear.
func (l *loginRateLimiter) checkAndRecord(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.attempts == nil {
		l.attempts = make(map[string]*loginAttempt)
	}
	a, ok := l.attempts[ip]
	if ok && time.Since(a.lastFail) > loginLockoutWindow {
		delete(l.attempts, ip)
		a = nil
		ok = false
	}
	if ok && a.failures >= loginMaxFailures {
		return false
	}
	if !ok {
		a = &loginAttempt{}
		l.attempts[ip] = a
	}
	a.failures++
	a.lastFail = time.Now()
	return true
}

func (l *loginRateLimiter) recordSuccess(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.attempts != nil {
		delete(l.attempts, ip)
	}
}

func (l *loginRateLimiter) cleanup() {
	l.mu.Lock()
	defer l.mu.Unlock()
	for ip, a := range l.attempts {
		if time.Since(a.lastFail) > loginLockoutWindow {
			delete(l.attempts, ip)
		}
	}
}

type adminUpstreamInput struct {
	Name                *string `json:"name"`
	URL                 *string `json:"url"`
	Username            *string `json:"username"`
	Password            *string `json:"password"`
	APIKey              *string `json:"apiKey"`
	PlaybackMode        *string `json:"playbackMode"`
	SpoofClient         *string `json:"spoofClient"`
	FollowRedirects     *bool   `json:"followRedirects"`
	ProxyID             *string `json:"proxyId"`
	PriorityMetadata    *bool   `json:"priorityMetadata"`
	StreamingURL        *string `json:"streamingUrl"`
	CustomUserAgent     *string `json:"customUserAgent"`
	CustomClient        *string `json:"customClient"`
	CustomClientVersion *string `json:"customClientVersion"`
	CustomDeviceName    *string `json:"customDeviceName"`
	CustomDeviceId      *string `json:"customDeviceId"`
}

type adminSettingsInput struct {
	ServerName      *string        `json:"serverName"`
	PlaybackMode    *string        `json:"playbackMode"`
	AdminUsername   *string        `json:"adminUsername"`
	AdminPassword   *string        `json:"adminPassword"`
	CurrentPassword *string        `json:"currentPassword"`
	Timeouts        map[string]any `json:"timeouts"`
}

type adminProxyInput struct {
	URL  string `json:"url"`
	Name string `json:"name"`
}

func NewApp() (*App, error) {
	configStore, err := LoadConfigStore()
	if err != nil {
		return nil, err
	}
	cfg := configStore.Snapshot()
	logger := NewLogger(LogConfig{DataDir: cfg.DataDir})
	idStore, err := NewIDStore(cfg.DataDir, logger)
	if err != nil {
		return nil, err
	}
	identity := NewClientIdentityServiceFromDetectedConfig()
	auth, err := NewAuthManager(configStore, identity, logger)
	if err != nil {
		return nil, err
	}
	upstream := NewUpstreamPool(cfg, logger)
	upstream.LoginAll()
	return &App{ConfigStore: configStore, Logger: logger, IDStore: idStore, Identity: identity, Auth: auth, Upstream: upstream}, nil
}

func (a *App) Close() error {
	if a.Upstream != nil {
		a.Upstream.stopHealthChecks()
	}
	if a.IDStore != nil {
		_ = a.IDStore.Close()
	}
	if a.Logger != nil {
		_ = a.Logger.Close()
	}
	return nil
}

func (a *App) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /System/Info/Public", a.handleSystemInfoPublic)
	mux.HandleFunc("GET /System/Info", a.withContext(a.requireAuth(a.handleSystemInfo)))
	mux.HandleFunc("GET /System/Endpoint", a.withContext(a.handleSystemEndpoint))
	mux.HandleFunc("GET /System/Ping", a.withContext(a.handleSystemPing))
	mux.HandleFunc("POST /System/Ping", a.withContext(a.handleSystemPing))

	mux.HandleFunc("POST /Users/AuthenticateByName", a.withContext(a.handleAuthenticateByName))
	mux.HandleFunc("GET /Users/Public", a.withContext(a.handleUsersPublic))
	mux.HandleFunc("GET /Users/{userId}", a.withContext(a.requireAuth(a.handleUserObject)))
	mux.HandleFunc("GET /Users/{userId}/Views", a.withContext(a.requireAuth(a.handleUserViews)))
	mux.HandleFunc("GET /Users/{userId}/GroupingOptions", a.withContext(a.requireAuth(a.handleUserGroupingOptions)))
	mux.HandleFunc("POST /Users/{userId}/Configuration", a.withContext(a.requireAuth(a.handleUserConfiguration)))
	mux.HandleFunc("POST /Users/{userId}/Policy", a.withContext(a.requireAuth(a.handleUserPolicy)))
	a.registerMediaRoutes(mux)
	a.registerLibraryAndImageRoutes(mux)
	a.registerSessionAndUserStateRoutes(mux)

	mux.HandleFunc("POST /admin/api/logout", a.withContext(a.requireAuth(a.handleAdminLogout)))
	mux.HandleFunc("GET /admin/api/client-info", a.withContext(a.requireAuth(a.handleAdminClientInfo)))
	mux.HandleFunc("GET /admin/api/status", a.withContext(a.requireAuth(a.handleAdminStatus)))
	mux.HandleFunc("GET /admin/api/upstream", a.withContext(a.requireAuth(a.handleAdminUpstreamList)))
	mux.HandleFunc("POST /admin/api/upstream", a.withContext(a.requireAuth(a.handleAdminUpstreamCreate)))
	mux.HandleFunc("PUT /admin/api/upstream/{index}", a.withContext(a.requireAuth(a.handleAdminUpstreamUpdate)))
	mux.HandleFunc("POST /admin/api/upstream/reorder", a.withContext(a.requireAuth(a.handleAdminUpstreamReorder)))
	mux.HandleFunc("DELETE /admin/api/upstream/{index}", a.withContext(a.requireAuth(a.handleAdminUpstreamDelete)))
	mux.HandleFunc("POST /admin/api/upstream/{index}/reconnect", a.withContext(a.requireAuth(a.handleAdminUpstreamReconnect)))
	mux.HandleFunc("GET /admin/api/proxies", a.withContext(a.requireAuth(a.handleAdminProxiesList)))
	mux.HandleFunc("POST /admin/api/proxies", a.withContext(a.requireAuth(a.handleAdminProxiesCreate)))
	mux.HandleFunc("POST /admin/api/proxies/test", a.withContext(a.requireAuth(a.handleAdminProxyTest)))
	mux.HandleFunc("DELETE /admin/api/proxies/{id}", a.withContext(a.requireAuth(a.handleAdminProxiesDelete)))
	mux.HandleFunc("GET /admin/api/settings", a.withContext(a.requireAuth(a.handleAdminSettings)))
	mux.HandleFunc("PUT /admin/api/settings", a.withContext(a.requireAuth(a.handleAdminSettingsUpdate)))
	mux.HandleFunc("GET /admin/api/logs", a.withContext(a.requireAuth(a.handleAdminLogs)))
	mux.HandleFunc("GET /admin/api/logs/download", a.withContext(a.requireAuth(a.handleAdminLogsDownload)))
	mux.HandleFunc("DELETE /admin/api/logs", a.withContext(a.requireAuth(a.handleAdminLogsClear)))
	mux.Handle("/admin/", a.adminFileServer())
	mux.HandleFunc("GET /admin", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/admin/admin.html", http.StatusFound)
	})
	mux.HandleFunc("GET /favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("GET /{$}", a.handleRoot)
	mux.HandleFunc("/", a.withContext(a.requireAuth(a.handleFallbackProxy)))

	return a.bodyLimitMiddleware(a.loggingMiddleware(a.prefixCompatMiddleware(a.corsMiddleware(mux))))
}

// statusCapture wraps http.ResponseWriter to capture the status code for logging.
type statusCapture struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (sc *statusCapture) WriteHeader(code int) {
	if !sc.wrote {
		sc.status = code
		sc.wrote = true
	}
	sc.ResponseWriter.WriteHeader(code)
}

func (sc *statusCapture) Write(b []byte) (int, error) {
	if !sc.wrote {
		sc.status = http.StatusOK
		sc.wrote = true
	}
	return sc.ResponseWriter.Write(b)
}

func (sc *statusCapture) Flush() {
	if f, ok := sc.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

const maxRequestBodySize = 2 << 20 // 2 MB

func (a *App) bodyLimitMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil && r.ContentLength != 0 {
			r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodySize)
		}
		next.ServeHTTP(w, r)
	})
}

// clientIP extracts the real client IP, respecting reverse proxy headers.
func clientIP(r *http.Request) string {
	if ip := strings.TrimSpace(r.Header.Get("X-Real-IP")); ip != "" {
		return ip
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if ip, _, _ := strings.Cut(xff, ","); strings.TrimSpace(ip) != "" {
			return strings.TrimSpace(ip)
		}
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

func (a *App) loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.Logger == nil || isAdminAPIPath(r.URL.Path) || isAdminPath(r.URL.Path) || r.URL.Path == "/favicon.ico" {
			next.ServeHTTP(w, r)
			return
		}
		start := time.Now()
		tokenSource := identifyTokenSource(r)
		sc := &statusCapture{ResponseWriter: w, status: http.StatusOK}
		a.Logger.Debugf("→ %s %s [auth:%s]", r.Method, r.URL.Path, tokenSource)
		next.ServeHTTP(sc, r)
		ms := time.Since(start).Milliseconds()
		msg := fmt.Sprintf("%s %s → %d (%dms) [auth:%s]", r.Method, r.URL.Path, sc.status, ms, tokenSource)
		switch {
		case sc.status >= 500:
			a.Logger.Errorf("%s", msg)
		case sc.status >= 400:
			a.Logger.Warnf("%s", msg)
		default:
			a.Logger.Debugf("%s", msg)
		}
	})
}

func identifyTokenSource(r *http.Request) string {
	if r.Header.Get("X-Emby-Token") != "" {
		return "X-Emby-Token"
	}
	if r.URL.Query().Get("api_key") != "" {
		return "api_key"
	}
	if r.URL.Query().Get("ApiKey") != "" {
		return "ApiKey"
	}
	if r.Header.Get("X-Emby-Authorization") != "" {
		return "X-Emby-Authorization"
	}
	if r.Header.Get("Authorization") != "" {
		return "Authorization"
	}
	return "none"
}

func (a *App) withContext(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := extractToken(r)
		var proxyUser *tokenInfo
		if token != "" {
			proxyUser = a.Auth.ValidateToken(token)
		}
		ctx := context.WithValue(r.Context(), requestContextKey{}, &RequestContext{
			Headers:    r.Header.Clone(),
			ProxyToken: token,
			ProxyUser:  proxyUser,
		})
		next(w, r.WithContext(ctx))
	}
}

func (a *App) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if reqCtx := requestContextFrom(r.Context()); reqCtx == nil || reqCtx.ProxyUser == nil {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"message": "Authentication required"})
			return
		}
		next(w, r)
	}
}

func requestContextFrom(ctx context.Context) *RequestContext {
	reqCtx, _ := ctx.Value(requestContextKey{}).(*RequestContext)
	return reqCtx
}

func extractToken(r *http.Request) string {
	if token := r.Header.Get("X-Emby-Token"); token != "" {
		return token
	}
	if token := r.URL.Query().Get("api_key"); token != "" {
		return token
	}
	if token := r.URL.Query().Get("ApiKey"); token != "" {
		return token
	}
	for _, headerName := range []string{"X-Emby-Authorization", "Authorization"} {
		if auth := r.Header.Get(headerName); auth != "" {
			if token := extractTokenFromAuthHeader(auth); token != "" {
				return token
			}
		}
	}
	return ""
}

func extractTokenFromAuthHeader(header string) string {
	for _, marker := range []string{"Token=\"", "Token="} {
		idx := strings.Index(header, marker)
		if idx < 0 {
			continue
		}
		rest := header[idx+len(marker):]
		if marker == "Token=\"" {
			if end := strings.Index(rest, "\""); end >= 0 {
				return rest[:end]
			}
		}
		end := strings.IndexAny(rest, ", ")
		if end >= 0 {
			return rest[:end]
		}
		return rest
	}
	return ""
}

func (a *App) prefixCompatMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/emby" || r.URL.Path == "/emby/" {
			clone := r.Clone(r.Context())
			copiedURL := *clone.URL
			clone.URL = &copiedURL
			clone.URL.Path = "/"
			next.ServeHTTP(w, clone)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/emby/") {
			clone := r.Clone(r.Context())
			copiedURL := *clone.URL
			clone.URL = &copiedURL
			clone.URL.Path = strings.TrimPrefix(r.URL.Path, "/emby")
			if clone.URL.Path == "" {
				clone.URL.Path = "/"
			}
			next.ServeHTTP(w, clone)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *App) corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isAdminPath(r.URL.Path) {
			applyAdminSecurityHeaders(w)
		}
		if isAdminAPIPath(r.URL.Path) {
			if origin := r.Header.Get("Origin"); origin != "" && sameOrigin(origin, requestOrigin(r)) {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Vary", "Origin")
			}
		} else {
			w.Header().Set("Access-Control-Allow-Origin", "*")
		}
		w.Header().Set("Access-Control-Allow-Methods", allowMethods)
		w.Header().Set("Access-Control-Allow-Headers", allowHeaders)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func isAdminPath(path string) bool {
	return path == "/admin" || strings.HasPrefix(path, "/admin/")
}

func isAdminAPIPath(path string) bool {
	return strings.HasPrefix(path, "/admin/api/")
}

func applyAdminSecurityHeaders(w http.ResponseWriter) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "SAMEORIGIN")
	w.Header().Set("X-XSS-Protection", "1; mode=block")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-inline' 'unsafe-eval' https://cdn.tailwindcss.com https://unpkg.com; style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; font-src https://fonts.gstatic.com; img-src 'self' data: https:; connect-src 'self'")
}

func requestOrigin(r *http.Request) string {
	scheme := "http"
	if forwarded := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Proto"), ",")[0]); forwarded != "" {
		scheme = forwarded
	} else if r.TLS != nil {
		scheme = "https"
	}
	host := r.Host
	if forwardedHost := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Host"), ",")[0]); forwardedHost != "" {
		host = forwardedHost
	}
	return scheme + "://" + host
}

func sameOrigin(aOrigin, bOrigin string) bool {
	parsedA, errA := url.Parse(aOrigin)
	parsedB, errB := url.Parse(bOrigin)
	if errA != nil || errB != nil {
		return false
	}
	return strings.EqualFold(parsedA.Scheme, parsedB.Scheme) && strings.EqualFold(parsedA.Host, parsedB.Host)
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func decodeJSONBody(r *http.Request, out any) error {
	defer r.Body.Close()
	decoder := json.NewDecoder(r.Body)
	return decoder.Decode(out)
}

func (a *App) handleSystemInfoPublic(w http.ResponseWriter, r *http.Request) {
	cfg := a.ConfigStore.Snapshot()
	writeJSON(w, http.StatusOK, map[string]any{
		"LocalAddress":                         requestOrigin(r),
		"ServerName":                           cfg.Server.Name,
		"Version":                              "4.7.14.0",
		"ProductName":                          "Emby Server",
		"Id":                                   cfg.Server.ID,
		"StartupWizardCompleted":               true,
		"OperatingSystem":                      "Linux",
		"CanSelfRestart":                       false,
		"CanLaunchWebBrowser":                  false,
		"HasUpdateAvailable":                   false,
		"SupportsAutoRunAtStartup":             false,
		"HardwareAccelerationRequiresPremiere": false,
	})
}

func (a *App) handleSystemInfo(w http.ResponseWriter, r *http.Request) {
	cfg := a.ConfigStore.Snapshot()
	writeJSON(w, http.StatusOK, map[string]any{
		"LocalAddress":                         requestOrigin(r),
		"WanAddress":                           "",
		"ServerName":                           cfg.Server.Name,
		"Version":                              "4.7.14.0",
		"ProductName":                          "Emby Server",
		"Id":                                   cfg.Server.ID,
		"StartupWizardCompleted":               true,
		"OperatingSystem":                      "Linux",
		"OperatingSystemDisplayName":           "Linux",
		"CanSelfRestart":                       false,
		"CanLaunchWebBrowser":                  false,
		"HasUpdateAvailable":                   false,
		"SupportsAutoRunAtStartup":             false,
		"SystemUpdateLevel":                    "Release",
		"HardwareAccelerationRequiresPremiere": false,
		"HasPendingRestart":                    false,
		"IsShuttingDown":                       false,
		"TranscodingTempPath":                  "/tmp",
		"LogPath":                              "/tmp",
		"InternalMetadataPath":                 "/tmp",
		"CachePath":                            "/tmp",
		"ProgramDataPath":                      "/tmp",
		"ItemsByNamePath":                      "/tmp",
	})
}

func (a *App) handleSystemEndpoint(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"IsLocal": false, "IsInNetwork": false})
}

func (a *App) handleSystemPing(w http.ResponseWriter, r *http.Request) {
	_, _ = w.Write([]byte("Emby Aggregator"))
}

func (a *App) handleAuthenticateByName(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if !a.loginLimiter.checkAndRecord(ip) {
		if a.Logger != nil {
			a.Logger.Warnf("Login rate limited: ip=%s", ip)
		}
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"message": "Too many failed login attempts, please try again later"})
		return
	}
	var body struct {
		Username string `json:"Username"`
		Pw       string `json:"Pw"`
		Password string `json:"Password"`
	}
	if err := decodeJSONBody(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": "Invalid request body"})
		return
	}
	password := body.Pw
	if password == "" {
		password = body.Password
	}
	if a.Logger != nil {
		a.Logger.Infof("Login attempt: user=%q client=%q device=%q ip=%s",
			body.Username, r.Header.Get("X-Emby-Client"), r.Header.Get("X-Emby-Device-Name"), r.RemoteAddr)
		a.Logger.Debugf("Login headers: UA=%q DeviceId=%q Version=%q",
			r.Header.Get("User-Agent"), r.Header.Get("X-Emby-Device-Id"), r.Header.Get("X-Emby-Client-Version"))
	}
	result, ok, err := a.Auth.Authenticate(body.Username, password)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": err.Error()})
		return
	}
	if !ok {
		if a.Logger != nil {
			a.Logger.Warnf("Login failed: user=%q ip=%s client=%q", body.Username, ip, r.Header.Get("X-Emby-Client"))
		}
		writeJSON(w, http.StatusUnauthorized, map[string]any{"message": "Invalid username or password"})
		return
	}
	a.loginLimiter.recordSuccess(ip)
	if token, _ := result["AccessToken"].(string); token != "" {
		a.Identity.SetCaptured(token, r.Header)
	}
	if a.Logger != nil {
		a.Logger.Infof("Login success: user=%q ip=%s", body.Username, r.RemoteAddr)
	}
	writeJSON(w, http.StatusOK, result)
}

func (a *App) handleUsersPublic(w http.ResponseWriter, r *http.Request) {
	cfg := a.ConfigStore.Snapshot()
	writeJSON(w, http.StatusOK, []map[string]any{{
		"Name":                      cfg.Admin.Username,
		"ServerId":                  cfg.Server.ID,
		"Id":                        a.Auth.ProxyUserID(),
		"HasPassword":               true,
		"HasConfiguredPassword":     true,
		"HasConfiguredEasyPassword": false,
	}})
}

func (a *App) handleUserObject(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.Auth.BuildUserObject())
}

func (a *App) handleUserViews(w http.ResponseWriter, r *http.Request) {
	onlineClients := a.Upstream.OnlineClients()
	cfg := a.ConfigStore.Snapshot()
	multiSource := len(onlineClients) > 1
	type slot struct {
		items []map[string]any
	}
	slots := make([]slot, len(onlineClients))
	var wg sync.WaitGroup
	for i, client := range onlineClients {
		wg.Add(1)
		go func(idx int, c *UpstreamClient) {
			defer wg.Done()
			payload, err := c.RequestJSON(r.Context(), requestContextFrom(r.Context()), a.Identity, http.MethodGet, "/Users/"+c.UserID+"/Views", cloneValues(r.URL.Query()), nil)
			if err != nil {
				return
			}
			var items []map[string]any
			for _, item := range asItems(payload) {
				rewritten := deepCloneMap(item)
				rewriteResponseIDs(rewritten, c.ServerIndex, a.IDStore, cfg.Server.ID, a.Auth.ProxyUserID())
				if multiSource {
					if name, _ := rewritten["Name"].(string); name != "" {
						rewritten["Name"] = name + " (" + c.Name + ")"
					}
				}
				items = append(items, rewritten)
			}
			slots[idx] = slot{items: items}
		}(i, client)
	}
	wg.Wait()
	allViews := make([]map[string]any, 0)
	for _, s := range slots {
		allViews = append(allViews, s.items...)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"Items":            toAnySlice(allViews),
		"TotalRecordCount": len(allViews),
		"StartIndex":       0,
	})
}

func (a *App) handleUserGroupingOptions(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, []any{})
}

func (a *App) handleUserConfiguration(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) handleUserPolicy(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) handleAdminLogout(w http.ResponseWriter, r *http.Request) {
	token := extractToken(r)
	if token != "" {
		a.Auth.RevokeToken(token)
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}

func (a *App) handleAdminClientInfo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.Identity.GetInfo())
}

func (a *App) handleAdminStatus(w http.ResponseWriter, r *http.Request) {
	cfg := a.ConfigStore.Snapshot()
	clients := a.Upstream.Clients()
	upstream := make([]map[string]any, 0, len(clients))
	online := 0
	for _, client := range clients {
		if client.Online {
			online++
		}
		upstream = append(upstream, map[string]any{
			"index":        client.ServerIndex,
			"name":         client.Name,
			"host":         sanitizeUpstreamURL(client.BaseURL),
			"online":       client.Online,
			"playbackMode": client.Config.PlaybackMode,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"serverName":     cfg.Server.Name,
		"serverId":       cfg.Server.ID,
		"port":           cfg.Server.Port,
		"playbackMode":   cfg.Playback.Mode,
		"idMappings":     a.IDStore.Stats(),
		"upstreamCount":  len(clients),
		"upstreamOnline": online,
		"upstream":       upstream,
	})
}

func (a *App) handleAdminUpstreamList(w http.ResponseWriter, r *http.Request) {
	cfg := a.ConfigStore.Snapshot()
	clients := a.Upstream.Clients()
	onlineByIndex := map[int]bool{}
	for _, client := range clients {
		onlineByIndex[client.ServerIndex] = client.Online
	}
	out := make([]map[string]any, 0, len(cfg.Upstream))
	for index, upstream := range cfg.Upstream {
		authType := "password"
		if upstream.APIKey != "" {
			authType = "apiKey"
		}
		out = append(out, map[string]any{
			"index":               index,
			"name":                upstream.Name,
			"url":                 sanitizeUpstreamURL(upstream.URL),
			"username":            upstream.Username,
			"authType":            authType,
			"online":              onlineByIndex[index],
			"playbackMode":        upstream.PlaybackMode,
			"spoofClient":         upstream.SpoofClient,
			"followRedirects":     upstream.FollowRedirects,
			"proxyId":             valueOrNil(upstream.ProxyID),
			"priorityMetadata":    upstream.PriorityMetadata,
			"customUserAgent":     upstream.CustomUserAgent,
			"customClient":        upstream.CustomClient,
			"customClientVersion": upstream.CustomClientVersion,
			"customDeviceName":    upstream.CustomDeviceName,
			"customDeviceId":      upstream.CustomDeviceId,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *App) handleAdminUpstreamCreate(w http.ResponseWriter, r *http.Request) {
	var body adminUpstreamInput
	if err := decodeJSONBody(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Invalid request body"})
		return
	}

	cfg := a.ConfigStore.Snapshot()
	draft := UpstreamConfig{FollowRedirects: true}
	applyAdminUpstreamInput(&draft, body, true)
	normalizeUpstream(&draft, len(cfg.Upstream), &cfg)
	if err := validateUpstreamDraft(draft); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	validation, err := a.validateUpstreamConnectivity(cfg, draft, len(cfg.Upstream), requestContextFrom(r.Context()))
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}

	nextCfg := cfg
	nextCfg.Upstream = append(append([]UpstreamConfig(nil), cfg.Upstream...), draft)
	if err := a.commitConfig(nextCfg); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	online := validation.Online
	if client := a.Upstream.GetClient(len(cfg.Upstream)); client != nil {
		online = client.IsOnline()
	}
	payload := map[string]any{"success": true, "index": len(cfg.Upstream), "name": draft.Name, "online": online}
	if validation.Warning != "" && !online {
		payload["warning"] = validation.Warning
	}
	writeJSON(w, http.StatusOK, payload)
}

func (a *App) handleAdminUpstreamUpdate(w http.ResponseWriter, r *http.Request) {
	index, ok := parsePathIndex(r, "index")
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Invalid upstream index"})
		return
	}
	cfg := a.ConfigStore.Snapshot()
	if index < 0 || index >= len(cfg.Upstream) {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	var body adminUpstreamInput
	if err := decodeJSONBody(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Invalid request body"})
		return
	}

	draft := cfg.Upstream[index]
	applyAdminUpstreamInput(&draft, body, false)
	normalizeUpstream(&draft, index, &cfg)
	if err := validateUpstreamDraft(draft); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	validation, err := a.validateUpstreamConnectivity(cfg, draft, index, requestContextFrom(r.Context()))
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}

	nextCfg := cfg
	nextCfg.Upstream = append([]UpstreamConfig(nil), cfg.Upstream...)
	nextCfg.Upstream[index] = draft
	if err := a.commitConfig(nextCfg); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	online := validation.Online
	if client := a.Upstream.GetClient(index); client != nil {
		online = client.IsOnline()
	}
	payload := map[string]any{"success": true, "index": index, "name": draft.Name, "online": online}
	if validation.Warning != "" && !online {
		payload["warning"] = validation.Warning
	}
	writeJSON(w, http.StatusOK, payload)
}

func (a *App) handleAdminUpstreamReorder(w http.ResponseWriter, r *http.Request) {
	var body struct {
		FromIndex int `json:"fromIndex"`
		ToIndex   int `json:"toIndex"`
	}
	if err := decodeJSONBody(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Invalid request body"})
		return
	}
	cfg := a.ConfigStore.Snapshot()
	if body.FromIndex < 0 || body.FromIndex >= len(cfg.Upstream) || body.ToIndex < 0 || body.ToIndex >= len(cfg.Upstream) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Index out of bounds"})
		return
	}
	nextCfg := cfg
	nextCfg.Upstream = append([]UpstreamConfig(nil), cfg.Upstream...)
	item := nextCfg.Upstream[body.FromIndex]
	nextCfg.Upstream = append(nextCfg.Upstream[:body.FromIndex], nextCfg.Upstream[body.FromIndex+1:]...)
	reordered := append([]UpstreamConfig(nil), nextCfg.Upstream[:body.ToIndex]...)
	reordered = append(reordered, item)
	reordered = append(reordered, nextCfg.Upstream[body.ToIndex:]...)
	nextCfg.Upstream = reordered
	a.IDStore.ReorderServerIndices(body.FromIndex, body.ToIndex)
	if err := a.commitConfig(nextCfg); err != nil {
		a.IDStore.ReorderServerIndices(body.ToIndex, body.FromIndex)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}

func (a *App) handleAdminUpstreamDelete(w http.ResponseWriter, r *http.Request) {
	index, ok := parsePathIndex(r, "index")
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Invalid upstream index"})
		return
	}
	cfg := a.ConfigStore.Snapshot()
	if index < 0 || index >= len(cfg.Upstream) {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	nextCfg := cfg
	nextCfg.Upstream = append([]UpstreamConfig(nil), cfg.Upstream[:index]...)
	nextCfg.Upstream = append(nextCfg.Upstream, cfg.Upstream[index+1:]...)
	if err := a.commitConfig(nextCfg); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	a.IDStore.RemoveByServerIndex(index)
	a.IDStore.ShiftServerIndices(index)
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}

func (a *App) handleAdminUpstreamReconnect(w http.ResponseWriter, r *http.Request) {
	index, ok := parsePathIndex(r, "index")
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Invalid upstream index"})
		return
	}
	client := a.Upstream.Reconnect(index)
	if client == nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "online": client.Online})
}

func (a *App) handleAdminProxiesList(w http.ResponseWriter, r *http.Request) {
	cfg := a.ConfigStore.Snapshot()
	out := make([]map[string]any, 0, len(cfg.Proxies))
	for _, proxy := range cfg.Proxies {
		out = append(out, map[string]any{
			"id":   proxy.ID,
			"name": proxy.Name,
			"url":  sanitizeProxyURL(proxy.URL),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *App) handleAdminProxiesCreate(w http.ResponseWriter, r *http.Request) {
	var body adminProxyInput
	if err := decodeJSONBody(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Invalid request body"})
		return
	}
	if err := validateHTTPURL(body.URL); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	proxy := ProxyConfig{ID: randomHex(16), Name: body.Name, URL: strings.TrimRight(body.URL, "/")}
	if proxy.Name == "" {
		proxy.Name = "Proxy"
	}
	cfg := a.ConfigStore.Snapshot()
	nextCfg := cfg
	nextCfg.Proxies = append(append([]ProxyConfig(nil), cfg.Proxies...), proxy)
	if err := a.commitConfig(nextCfg); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": proxy.ID, "name": proxy.Name, "url": sanitizeProxyURL(proxy.URL)})
}

func (a *App) handleAdminProxiesDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	cfg := a.ConfigStore.Snapshot()
	next := make([]ProxyConfig, 0, len(cfg.Proxies))
	for _, proxy := range cfg.Proxies {
		if proxy.ID != id {
			next = append(next, proxy)
		}
	}
	nextCfg := cfg
	nextCfg.Proxies = next
	if err := a.commitConfig(nextCfg); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) handleAdminProxyTest(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ProxyURL  string `json:"proxyUrl"`
		ProxyID   string `json:"proxyId"`
		TargetURL string `json:"targetUrl"`
	}
	if err := decodeJSONBody(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Invalid request body"})
		return
	}
	// Resolve proxy URL: prefer proxyId lookup (avoids sanitized/masked URL issue), fall back to direct proxyUrl.
	proxyURL := strings.TrimSpace(body.ProxyURL)
	if strings.TrimSpace(body.ProxyID) != "" {
		cfg := a.ConfigStore.Snapshot()
		proxy := findProxy(cfg.Proxies, body.ProxyID)
		if proxy == nil {
			writeJSON(w, http.StatusOK, map[string]any{"success": false, "error": "Proxy not found"})
			return
		}
		proxyURL = proxy.URL
	}
	if err := validateHTTPURL(proxyURL); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Invalid proxy URL: " + err.Error()})
		return
	}
	if err := validateHTTPURL(body.TargetURL); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Invalid target URL: " + err.Error()})
		return
	}
	transport, ok := buildProxyTransport(proxyURL, a.Logger, "proxy-test")
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"success": false, "error": "Failed to create proxy transport (invalid URL or scheme)"})
		return
	}
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second}
	start := time.Now()
	resp, err := client.Get(body.TargetURL)
	latency := time.Since(start).Milliseconds()
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"success": false, "latency": latency, "error": err.Error()})
		return
	}
	resp.Body.Close()
	writeJSON(w, http.StatusOK, map[string]any{"success": resp.StatusCode >= 200 && resp.StatusCode < 400, "latency": latency, "statusCode": resp.StatusCode})
}

func (a *App) handleAdminSettings(w http.ResponseWriter, r *http.Request) {
	cfg := a.ConfigStore.Snapshot()
	writeJSON(w, http.StatusOK, map[string]any{
		"serverName":    cfg.Server.Name,
		"port":          cfg.Server.Port,
		"playbackMode":  cfg.Playback.Mode,
		"adminUsername": cfg.Admin.Username,
		"timeouts": map[string]any{
			"api":            cfg.Timeouts.API,
			"global":         cfg.Timeouts.Global,
			"login":          cfg.Timeouts.Login,
			"healthCheck":    cfg.Timeouts.HealthCheck,
			"healthInterval": cfg.Timeouts.HealthInterval,
		},
	})
}

func (a *App) handleAdminSettingsUpdate(w http.ResponseWriter, r *http.Request) {
	var body adminSettingsInput
	if err := decodeJSONBody(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Invalid request body"})
		return
	}
	cfg := a.ConfigStore.Snapshot()
	nextCfg := cfg

	if body.ServerName != nil {
		name := strings.TrimSpace(*body.ServerName)
		if err := validateRequiredLength("serverName", name, 1, 100); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		nextCfg.Server.Name = name
	}
	if body.PlaybackMode != nil && strings.TrimSpace(*body.PlaybackMode) != "" {
		mode := strings.TrimSpace(*body.PlaybackMode)
		if err := validatePlaybackMode(mode); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		nextCfg.Playback.Mode = mode
	}
	if body.AdminUsername != nil && strings.TrimSpace(*body.AdminUsername) != "" {
		username := strings.TrimSpace(*body.AdminUsername)
		if err := validateRequiredLength("adminUsername", username, 1, 50); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		nextCfg.Admin.Username = username
	}
	if body.AdminPassword != nil && *body.AdminPassword != "" {
		currentPassword := ""
		if body.CurrentPassword != nil {
			currentPassword = *body.CurrentPassword
		}
		if !VerifyPassword(currentPassword, cfg.Admin.Password) {
			writeJSON(w, http.StatusForbidden, map[string]any{"error": "当前密码不正确"})
			return
		}
		hashed, err := HashPassword(*body.AdminPassword)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		nextCfg.Admin.Password = hashed
	}

	passwordChanged := nextCfg.Admin.Password != cfg.Admin.Password

	for _, key := range []string{"api", "global", "login", "healthCheck", "healthInterval"} {
		if raw, ok := body.Timeouts[key]; ok {
			if parsed, ok := toPositiveInt(raw); ok {
				switch key {
				case "api":
					nextCfg.Timeouts.API = parsed
				case "global":
					nextCfg.Timeouts.Global = parsed
				case "login":
					nextCfg.Timeouts.Login = parsed
				case "healthCheck":
					nextCfg.Timeouts.HealthCheck = parsed
				case "healthInterval":
					nextCfg.Timeouts.HealthInterval = parsed
				}
			}
		}
	}

	if err := a.commitConfigSettingsOnly(nextCfg); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	// Revoke all tokens when password changes so old sessions must re-authenticate
	if passwordChanged {
		a.Auth.RevokeAllTokens()
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}

func (a *App) handleAdminLogs(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	writeJSON(w, http.StatusOK, a.Logger.Entries(limit))
}

func (a *App) handleAdminLogsDownload(w http.ResponseWriter, r *http.Request) {
	logFile := a.Logger.FilePath()
	if logFile == "" {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "Log file not found"})
		return
	}
	if _, err := os.Stat(logFile); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "Log file not found"})
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=\"emby-in-one.log\"")
	http.ServeFile(w, r, logFile)
}

func (a *App) handleAdminLogsClear(w http.ResponseWriter, r *http.Request) {
	if err := a.Logger.ClearFile(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	a.Logger.Infof("Log file cleared by admin")
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}

func (a *App) commitConfig(nextCfg Config) error {
	return a.commitConfigFull(nextCfg, true)
}

func (a *App) commitConfigSettingsOnly(nextCfg Config) error {
	return a.commitConfigFull(nextCfg, false)
}

func (a *App) commitConfigFull(nextCfg Config, reloadUpstreams bool) error {
	previous := a.ConfigStore.Snapshot()
	a.ConfigStore.Replace(nextCfg)
	if err := a.ConfigStore.Save(); err != nil {
		a.ConfigStore.Replace(previous)
		return err
	}
	if reloadUpstreams {
		a.Upstream.Reload(nextCfg)
		go a.Upstream.LoginAll()
	}
	return nil
}

func (a *App) adminFileServer() http.Handler {
	publicDir := detectPublicDir()
	fs := http.FileServer(http.Dir(publicDir))
	return http.StripPrefix("/admin/", fs)
}

func detectPublicDir() string {
	for _, candidate := range []string{"public", filepath.Join("..", "public")} {
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return candidate
		}
	}
	return "public"
}

func (a *App) handleRoot(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(w, `<!DOCTYPE html><html lang="zh-CN"><head><meta charset="UTF-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Emby-in-One</title><style>body{font-family:sans-serif;background:#f8fafc;display:flex;align-items:center;justify-content:center;min-height:100vh;margin:0}.box{background:#fff;border:1px solid #e2e8f0;border-radius:12px;padding:2.5rem 3rem;text-align:center;max-width:400px}.icon{font-size:3rem;margin-bottom:1rem}.title{font-size:1.4rem;font-weight:700;color:#1e293b;margin-bottom:.5rem}.sub{color:#64748b;font-size:.95rem;margin-bottom:1.5rem}.btn{display:inline-block;background:#2563eb;color:#fff;padding:.6rem 1.4rem;border-radius:8px;text-decoration:none;font-weight:600;font-size:.9rem}</style></head><body><div class="box"><div class="icon">⛔</div><div class="title">此地址仅供 Emby 客户端使用</div><div class="sub">请将 Emby 客户端连接地址设置为本地址。<br>管理面板请访问 <code>/admin</code> 路径。</div><a class="btn" href="/admin">前往管理面板</a></div></body></html>`)
}

func valueOrNil(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func parsePathIndex(r *http.Request, name string) (int, bool) {
	value := r.PathValue(name)
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, false
	}
	return parsed, true
}

func validateUpstreamDraft(draft UpstreamConfig) error {
	if err := validateRequiredLength("upstream.name", strings.TrimSpace(draft.Name), 1, 100); err != nil {
		return err
	}
	if err := validatePlaybackMode(draft.PlaybackMode); err != nil {
		return err
	}
	if err := validateSpoofClient(draft.SpoofClient); err != nil {
		return err
	}
	if err := validateHTTPURL(draft.URL); err != nil {
		return err
	}
	if draft.StreamingURL != "" {
		if err := validateHTTPURL(draft.StreamingURL); err != nil {
			return err
		}
	}
	hasAPIKey := strings.TrimSpace(draft.APIKey) != ""
	hasUserPassword := strings.TrimSpace(draft.Username) != "" && draft.Password != ""
	if hasAPIKey == hasUserPassword {
		return &httpError{message: "上游认证方式必须为 apiKey 或 用户名+密码 二选一"}
	}
	return nil
}

type upstreamValidationResult struct {
	Online  bool
	Warning string
}

const passthroughDeferredWarning = "透传模式上游已保存，但当前没有可用的客户端身份信息，登录将稍后自动重试"

func (a *App) validateUpstreamConnectivity(cfg Config, draft UpstreamConfig, index int, reqCtx *RequestContext) (upstreamValidationResult, error) {
	client := newUpstreamClient(cfg, draft, index, a.Logger)
	client.Login(context.Background(), reqCtx, a.Identity)
	snapshot := client.snapshot()
	if snapshot.Online && snapshot.AccessToken != "" && snapshot.UserID != "" {
		return upstreamValidationResult{Online: true}, nil
	}
	if draft.SpoofClient == "passthrough" {
		if shouldDeferPassthroughValidation(snapshot.LastError) {
			return upstreamValidationResult{Online: false, Warning: passthroughDeferredWarning}, nil
		}
		source, _ := client.resolveIdentityHeaders(reqCtx, a.Identity, nil)
		if source == "infuse-fallback" {
			return upstreamValidationResult{Online: false, Warning: passthroughDeferredWarning}, nil
		}
	}
	if snapshot.LastError != "" {
		return upstreamValidationResult{}, errors.New(snapshot.LastError)
	}
	return upstreamValidationResult{}, errors.New("上游服务器验证失败")
}

func shouldDeferPassthroughValidation(lastError string) bool {
	lastError = strings.TrimSpace(lastError)
	return strings.HasPrefix(lastError, "401 ") || strings.HasPrefix(lastError, "403 ")
}

func validateHTTPURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return &httpError{message: "URL 必须以 http:// 或 https:// 开头"}
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return &httpError{message: "URL 必须以 http:// 或 https:// 开头"}
	}
	return nil
}

func sanitizeUpstreamURL(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" {
		return raw
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}

func sanitizeProxyURL(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" {
		return raw
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}

func validateRequiredLength(field, value string, minLen, maxLen int) error {
	if len(value) < minLen || len(value) > maxLen {
		return &httpError{message: field + " length is invalid"}
	}
	return nil
}

func validatePlaybackMode(mode string) error {
	switch strings.TrimSpace(mode) {
	case "proxy", "redirect":
		return nil
	default:
		return &httpError{message: "playbackMode 必须为 proxy 或 redirect"}
	}
}

func validateSpoofClient(mode string) error {
	switch strings.TrimSpace(mode) {
	case "none", "passthrough", "infuse", "custom":
		return nil
	default:
		return &httpError{message: "spoofClient 必须为 none、passthrough、infuse 或 custom"}
	}
}

type httpError struct {
	message string
}

func (e *httpError) Error() string { return e.message }

func applyAdminUpstreamInput(dst *UpstreamConfig, body adminUpstreamInput, isCreate bool) {
	if isCreate {
		if body.Name != nil {
			dst.Name = strings.TrimSpace(*body.Name)
		}
		if body.URL != nil {
			dst.URL = strings.TrimSpace(*body.URL)
		}
		if body.Username != nil {
			dst.Username = *body.Username
		}
		if body.Password != nil {
			dst.Password = *body.Password
		}
		if body.APIKey != nil {
			dst.APIKey = *body.APIKey
		}
		if body.PlaybackMode != nil {
			dst.PlaybackMode = strings.TrimSpace(*body.PlaybackMode)
		}
		if body.SpoofClient != nil {
			dst.SpoofClient = strings.TrimSpace(*body.SpoofClient)
		}
		if body.FollowRedirects != nil {
			dst.FollowRedirects = *body.FollowRedirects
		}
		if body.ProxyID != nil {
			dst.ProxyID = strings.TrimSpace(*body.ProxyID)
		}
		if body.PriorityMetadata != nil {
			dst.PriorityMetadata = *body.PriorityMetadata
		}
		if body.StreamingURL != nil {
			dst.StreamingURL = strings.TrimSpace(*body.StreamingURL)
		}
		if body.CustomUserAgent != nil {
			dst.CustomUserAgent = strings.TrimSpace(*body.CustomUserAgent)
		}
		if body.CustomClient != nil {
			dst.CustomClient = strings.TrimSpace(*body.CustomClient)
		}
		if body.CustomClientVersion != nil {
			dst.CustomClientVersion = strings.TrimSpace(*body.CustomClientVersion)
		}
		if body.CustomDeviceName != nil {
			dst.CustomDeviceName = strings.TrimSpace(*body.CustomDeviceName)
		}
		if body.CustomDeviceId != nil {
			dst.CustomDeviceId = strings.TrimSpace(*body.CustomDeviceId)
		}
		return
	}

	if body.Name != nil {
		dst.Name = strings.TrimSpace(*body.Name)
	}
	if body.URL != nil {
		dst.URL = strings.TrimSpace(*body.URL)
	}
	if body.Username != nil {
		dst.Username = *body.Username
	}
	if body.Password != nil && *body.Password != "" {
		dst.Password = *body.Password
	}
	if body.APIKey != nil && *body.APIKey != "" {
		dst.APIKey = *body.APIKey
	}
	if body.PlaybackMode != nil {
		dst.PlaybackMode = strings.TrimSpace(*body.PlaybackMode)
	}
	if body.SpoofClient != nil {
		dst.SpoofClient = strings.TrimSpace(*body.SpoofClient)
	}
	if body.FollowRedirects != nil {
		dst.FollowRedirects = *body.FollowRedirects
	}
	if body.ProxyID != nil {
		dst.ProxyID = strings.TrimSpace(*body.ProxyID)
	}
	if body.PriorityMetadata != nil {
		dst.PriorityMetadata = *body.PriorityMetadata
	}
	if body.StreamingURL != nil {
		dst.StreamingURL = strings.TrimSpace(*body.StreamingURL)
	}
	if body.CustomUserAgent != nil {
		dst.CustomUserAgent = strings.TrimSpace(*body.CustomUserAgent)
	}
	if body.CustomClient != nil {
		dst.CustomClient = strings.TrimSpace(*body.CustomClient)
	}
	if body.CustomClientVersion != nil {
		dst.CustomClientVersion = strings.TrimSpace(*body.CustomClientVersion)
	}
	if body.CustomDeviceName != nil {
		dst.CustomDeviceName = strings.TrimSpace(*body.CustomDeviceName)
	}
	if body.CustomDeviceId != nil {
		dst.CustomDeviceId = strings.TrimSpace(*body.CustomDeviceId)
	}
}

func toPositiveInt(raw any) (int, bool) {
	switch value := raw.(type) {
	case float64:
		if value > 0 {
			return int(value), true
		}
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(value))
		if err == nil && parsed > 0 {
			return parsed, true
		}
	case json.Number:
		parsed, err := value.Int64()
		if err == nil && parsed > 0 {
			return int(parsed), true
		}
	}
	return 0, false
}

func intToString(v int) string {
	return strconv.Itoa(v)
}

func (a *App) Run() error {
	cfg := a.ConfigStore.Snapshot()
	server := &http.Server{
		Addr:              ":" + intToString(cfg.Server.Port),
		Handler:           a.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	// Start periodic stream URL eviction
	evictCtx, evictCancel := context.WithCancel(context.Background())
	go func() {
		ticker := time.NewTicker(30 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-evictCtx.Done():
				return
			case <-ticker.C:
				a.IDStore.evictExpiredStreamURLs()
				a.loginLimiter.cleanup()
			}
		}
	}()

	// Graceful shutdown on SIGINT/SIGTERM
	shutdownCh := make(chan os.Signal, 1)
	signal.Notify(shutdownCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-shutdownCh
		a.Logger.Infof("Shutdown signal received, draining connections...")
		evictCancel()
		a.Upstream.stopHealthChecks()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	}()

	a.Logger.Infof("Go Emby-in-One listening on port %d", cfg.Server.Port)
	err := server.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
