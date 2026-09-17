package backend

import (
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type adminUpstreamInput struct {
	Name                *string   `json:"name"`
	URL                 *string   `json:"url"`
	Username            *string   `json:"username"`
	Password            *string   `json:"password"`
	APIKey              *string   `json:"apiKey"`
	AuthType            *string   `json:"authType"`
	PlaybackMode        *string   `json:"playbackMode"`
	SpoofClient         *string   `json:"spoofClient"`
	FollowRedirects     *bool     `json:"followRedirects"`
	ProxyID             *string   `json:"proxyId"`
	PriorityMetadata    *bool     `json:"priorityMetadata"`
	CustomUserAgent     *string   `json:"customUserAgent"`
	CustomClient        *string   `json:"customClient"`
	CustomClientVersion *string   `json:"customClientVersion"`
	CustomDeviceName    *string   `json:"customDeviceName"`
	CustomDeviceId      *string   `json:"customDeviceId"`
	MaxConcurrent       *int      `json:"maxConcurrent"`
	StreamingURL        *string   `json:"streamingUrl"`
	StreamingURLs       *[]string `json:"streamingUrls"`
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
	Name string `json:"name"`
	URL  string `json:"url"`
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
			"id":           client.ID,
			"index":        client.ServerIndex,
			"name":         client.Name,
			"url":          sanitizeUpstreamURL(client.BaseURL),
			"online":       client.Online,
			"playbackMode": client.Config.PlaybackMode,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"version":        a.Version,
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
		streamingURLs := append([]string(nil), upstream.StreamingURLs...)
		out = append(out, map[string]any{
			"id":                  upstream.ID,
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
			"maxConcurrent":       upstream.MaxConcurrent,
			"streamingUrl":        upstream.StreamingURL,
			"streamingUrls":       streamingURLs,
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
	if validation.Warning != "" {
		payload["warning"] = validation.Warning
	}
	writeJSON(w, http.StatusOK, payload)
}

func parsePathUpstream(r *http.Request, cfg *Config) (int, *UpstreamConfig, bool) {
	id := r.PathValue("id")
	if id == "" {
		id = r.PathValue("index")
	}
	if id == "" {
		return -1, nil, false
	}
	for i := range cfg.Upstream {
		if cfg.Upstream[i].ID == id {
			return i, &cfg.Upstream[i], true
		}
	}
	if idx, err := strconv.Atoi(id); err == nil && idx >= 0 && idx < len(cfg.Upstream) {
		return idx, &cfg.Upstream[idx], true
	}
	return -1, nil, false
}

func (a *App) handleAdminUpstreamUpdate(w http.ResponseWriter, r *http.Request) {
	cfg := a.ConfigStore.Snapshot()
	index, existing, ok := parsePathUpstream(r, &cfg)
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	var body adminUpstreamInput
	if err := decodeJSONBody(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Invalid request body"})
		return
	}

	draft := *existing
	applyAdminUpstreamInput(&draft, body, false)
	draft.ID = existing.ID // Keep existing persistent ID
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
	if client := a.Upstream.ClientByID(draft.ID); client != nil {
		online = client.IsOnline()
	}
	payload := map[string]any{"success": true, "id": draft.ID, "index": index, "name": draft.Name, "online": online}
	if validation.Warning != "" {
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
	if err := a.commitConfig(nextCfg); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}

func (a *App) handleAdminUpstreamDelete(w http.ResponseWriter, r *http.Request) {
	cfg := a.ConfigStore.Snapshot()
	index, existing, ok := parsePathUpstream(r, &cfg)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "Server not found"})
		return
	}
	serverID := existing.ID

	nextCfg := cfg
	nextCfg.Upstream = make([]UpstreamConfig, 0, len(cfg.Upstream)-1)
	nextCfg.Upstream = append(nextCfg.Upstream, cfg.Upstream[:index]...)
	nextCfg.Upstream = append(nextCfg.Upstream, cfg.Upstream[index+1:]...)

	if err := a.commitConfig(nextCfg); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}

	// Clean up database records for this server
	if a.IDStore != nil && serverID != "" {
		if err := a.IDStore.RemoveByServerID(serverID); err != nil && a.Logger != nil {
			a.Logger.Errorf("delete upstream %s: remove ID mappings: %v", serverID, err)
		}
	}
	if a.UserStore != nil && serverID != "" {
		if err := a.UserStore.RemoveServerGrants(serverID); err != nil && a.Logger != nil {
			a.Logger.Errorf("delete upstream %s: remove user server grants: %v", serverID, err)
		}
	}
	if a.WatchStore != nil && serverID != "" {
		if err := a.WatchStore.DeleteServerData(serverID); err != nil && a.Logger != nil {
			a.Logger.Errorf("delete upstream %s: delete watch progress: %v", serverID, err)
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}

func (a *App) handleAdminUpstreamReconnect(w http.ResponseWriter, r *http.Request) {
	cfg := a.ConfigStore.Snapshot()
	index, existing, ok := parsePathUpstream(r, &cfg)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Invalid upstream identifier"})
		return
	}
	var client *UpstreamClient
	if existing.ID != "" {
		client = a.Upstream.ReconnectByID(existing.ID)
	}
	if client == nil {
		client = a.Upstream.Reconnect(index)
	}
	if client == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "Server not found"})
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
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
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
			writeJSON(w, http.StatusOK, map[string]any{"success": false, "latency": int64(0), "error": "Proxy not found"})
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
	// SSRF protection: this endpoint probes egress, so it only accepts public targets.
	parsedTarget, _ := url.Parse(body.TargetURL)
	if parsedTarget != nil {
		if reason, blocked := blockedTargetReason(parsedTarget.Hostname()); blocked {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": probeTargetBlockedMessage(parsedTarget.Hostname(), reason)})
			return
		}
	}
	transport, ok := buildProxyTransport(proxyURL, a.Logger, "proxy-test")
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"success": false, "latency": int64(0), "error": "Failed to create proxy transport (invalid URL or scheme)"})
		return
	}
	// Wrap transport with SSRF-safe dialer that re-checks resolved IPs at connect time,
	// preventing DNS rebinding attacks where a hostname resolves to a public IP during
	// validation but to a private IP during the actual connection.
	safeTransport := wrapTransportWithSSRFCheck(transport)
	client := &http.Client{Transport: safeTransport, Timeout: 10 * time.Second}
	start := time.Now()
	// This is a probe-only exit: it talks to an administrator-supplied URL and
	// carries no client request identity, so it deliberately does not go through
	// the shared outbound preparation layer. If it is ever changed to forward a
	// real business request, it must be wired into that layer instead of relying
	// on this comment.
	resp, err := client.Get(body.TargetURL)
	latency := time.Since(start).Milliseconds()
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"success": false, "latency": latency, "error": err.Error()})
		return
	}
	resp.Body.Close()
	// TCP connection succeeded if we got any HTTP response (even 403/5xx)
	result := map[string]any{"success": true, "latency": latency, "statusCode": resp.StatusCode}
	writeJSON(w, http.StatusOK, result)
}

func (a *App) handleAdminSettings(w http.ResponseWriter, r *http.Request) {
	cfg := a.ConfigStore.Snapshot()
	writeJSON(w, http.StatusOK, map[string]any{
		"serverName":    cfg.Server.Name,
		"port":          cfg.Server.Port,
		"playbackMode":  cfg.Playback.Mode,
		"adminUsername": cfg.Admin.Username,
		"timeouts": map[string]any{
			"api":                 cfg.Timeouts.API,
			"global":              cfg.Timeouts.Global,
			"login":               cfg.Timeouts.Login,
			"healthCheck":         cfg.Timeouts.HealthCheck,
			"healthInterval":      cfg.Timeouts.HealthInterval,
			"searchGracePeriod":   cfg.Timeouts.SearchGracePeriod,
			"metadataGracePeriod": cfg.Timeouts.MetadataGracePeriod,
			"latestGracePeriod":   cfg.Timeouts.LatestGracePeriod,
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
		if username != cfg.Admin.Username {
			currentPassword := ""
			if body.CurrentPassword != nil {
				currentPassword = *body.CurrentPassword
			}
			if !VerifyPassword(currentPassword, cfg.Admin.Password) {
				writeJSON(w, http.StatusForbidden, map[string]any{"error": "当前密码不正确"})
				return
			}
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
		if err := validatePassword("adminPassword", *body.AdminPassword); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		hashed, err := HashPassword(*body.AdminPassword)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		nextCfg.Admin.Password = hashed
	}
	for _, key := range positiveTimeoutKeys {
		if raw, ok := body.Timeouts[key]; ok {
			if parsed, ok := toPositiveInt(raw); ok {
				assignTimeout(&nextCfg.Timeouts, key, parsed)
			}
		}
	}
	for _, key := range nonNegativeTimeoutKeys {
		if raw, ok := body.Timeouts[key]; ok {
			if parsed, ok := toNonNegativeInt(raw); ok {
				assignTimeout(&nextCfg.Timeouts, key, parsed)
			}
		}
	}
	if err := validateTimeouts(nextCfg.Timeouts); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}

	passwordChanged := nextCfg.Admin.Password != cfg.Admin.Password

	if err := a.commitConfigSettingsOnly(nextCfg); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	if passwordChanged {
		a.Auth.RevokeAllTokens()
		if a.Logger != nil {
			a.Logger.Infof("Admin password changed — all proxy tokens revoked")
		}
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

func (a *App) handleAdminUsersList(w http.ResponseWriter, r *http.Request) {
	if a.UserStore == nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	users := a.UserStore.List()
	cfg := a.ConfigStore.Snapshot()
	result := make([]map[string]any, 0, len(users))
	for _, u := range users {
		serverNames := make([]string, 0, len(u.AllowedServers))
		for _, serverID := range u.AllowedServers {
			for _, us := range cfg.Upstream {
				if us.ID == serverID {
					serverNames = append(serverNames, us.Name)
					break
				}
			}
		}
		result = append(result, map[string]any{
			"id":             u.ID,
			"username":       u.Username,
			"enabled":        u.Enabled,
			"allowedServers": u.AllowedServers,
			"serverNames":    serverNames,
			"createdAt":      u.CreatedAt,
		})
	}
	writeJSON(w, http.StatusOK, result)
}

func (a *App) handleAdminUsersCreate(w http.ResponseWriter, r *http.Request) {
	if a.UserStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "multi-user disabled"})
		return
	}
	var input struct {
		Username       string   `json:"username"`
		Password       string   `json:"password"`
		AllowedServers []string `json:"allowedServers"`
	}
	if err := decodeJSONBody(r, &input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Invalid request body"})
		return
	}
	if input.Username == "" || input.Password == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "用户名和密码不能为空"})
		return
	}
	if err := validatePassword("password", input.Password); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	cfg := a.ConfigStore.Snapshot()
	if strings.EqualFold(input.Username, cfg.Admin.Username) {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "用户名与管理员冲突"})
		return
	}
	user, err := a.UserStore.Create(input.Username, input.Password, input.AllowedServers)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") || strings.Contains(err.Error(), "already exists") {
			writeJSON(w, http.StatusConflict, map[string]any{"error": "用户名已存在"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": user.ID, "username": user.Username})
}

func (a *App) handleAdminUsersUpdate(w http.ResponseWriter, r *http.Request) {
	if a.UserStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "multi-user disabled"})
		return
	}
	id := r.PathValue("id")
	var input struct {
		Username       *string   `json:"username"`
		Password       *string   `json:"password"`
		Enabled        *bool     `json:"enabled"`
		AllowedServers *[]string `json:"allowedServers"`
	}
	if err := decodeJSONBody(r, &input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Invalid request body"})
		return
	}
	if input.Username != nil {
		cfg := a.ConfigStore.Snapshot()
		if strings.EqualFold(*input.Username, cfg.Admin.Username) {
			writeJSON(w, http.StatusConflict, map[string]any{"error": "用户名与管理员冲突"})
			return
		}
	}
	if input.Password != nil && *input.Password != "" {
		if err := validatePassword("password", *input.Password); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
	}
	if err := a.UserStore.Update(id, input.Username, input.Password, input.Enabled, input.AllowedServers); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	if input.Enabled != nil && !*input.Enabled {
		a.Auth.RevokeTokensByUserID(id)
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}

func (a *App) handleAdminUsersDelete(w http.ResponseWriter, r *http.Request) {
	if a.UserStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "multi-user disabled"})
		return
	}
	id := r.PathValue("id")
	if err := a.UserStore.Delete(id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	a.Auth.RevokeTokensByUserID(id)
	if a.WatchStore != nil {
		if err := a.WatchStore.DeleteUser(id); err != nil && a.Logger != nil {
			a.Logger.Warnf("failed to delete watch data for user %s: %s", id, redactURLInError(err))
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}

func (a *App) commitConfig(nextCfg Config) error {
	return a.commitConfigFull(nextCfg, true)
}

// commitConfigSettingsOnly applies a change to the global settings. The pool is
// rebuilt but not re-authenticated: the credentials carry over unchanged, and a
// re-login would only interrupt upstreams that are working.
func (a *App) commitConfigSettingsOnly(nextCfg Config) error {
	return a.commitConfigFull(nextCfg, false)
}

// commitConfigFull writes the new config and rebuilds the upstream pool.
//
// Every save rebuilds the pool, because several settings (the API and login
// timeouts, the health-check interval) are read while a client is built: without
// the rebuild a save would report success and leave the running clients on the
// old values. reLogin additionally re-authenticates, which an upstream's own
// connection details need; a settings save leaves the existing sessions alone.
func (a *App) commitConfigFull(nextCfg Config, reLogin bool) error {
	previous := a.ConfigStore.Snapshot()
	a.ConfigStore.Replace(nextCfg)
	if err := a.ConfigStore.Save(); err != nil {
		a.ConfigStore.Replace(previous)
		return err
	}
	a.Upstream.Reload(nextCfg)
	if reLogin {
		go a.Upstream.LoginAll()
	}
	return nil
}
