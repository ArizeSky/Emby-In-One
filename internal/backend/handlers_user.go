package backend

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
)

// firstNonEmpty 返回第一个非空字符串。
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func (a *App) handleAuthenticateByName(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r, a.ConfigStore.Snapshot().Server.TrustProxy)
	if !a.loginLimiter.allowed(ip) {
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
	raw, readErr := io.ReadAll(r.Body)
	defer r.Body.Close()
	if readErr == nil {
		// 先按 JSON 解（Emby Web / Flymby 等）
		if err := json.Unmarshal(raw, &body); err != nil || body.Username == "" {
			// 回退：Emby 官方 Android TV 客户端用 application/x-www-form-urlencoded
			if vals, perr := url.ParseQuery(string(raw)); perr == nil && len(vals) > 0 {
				body.Username = firstNonEmpty(vals.Get("username"), vals.Get("Username"), vals.Get("name"))
				body.Pw = firstNonEmpty(vals.Get("pw"), vals.Get("Pw"), vals.Get("password"), vals.Get("Password"))
			}
		}
	}
	if readErr != nil || body.Username == "" {
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

	// 1. Try admin match
	result, ok, err := a.Auth.Authenticate(body.Username, password)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": err.Error()})
		return
	}
	if ok {
		// The admin account must not be playable from the web client: the token
		// it would receive carries admin rights, and /admin is the only supported
		// admin entry point. The web player identifies itself with this marker
		// because it deliberately sends no Emby client identity headers (see
		// public/web/app.js); the admin panel and real Emby clients never set it
		// and keep logging in as before.
		if isWebClientLogin(r) {
			if a.Logger != nil {
				a.Logger.Warnf("Web client admin login rejected: user=%q ip=%s", body.Username, r.RemoteAddr)
			}
			writeJSON(w, http.StatusForbidden, map[string]any{"message": "管理员账号不能通过 Web 客户端登录，请使用 /admin 管理面板"})
			return
		}
		// 登录成功不能清除本 IP 的失败记录：低权账号的成功登录
		// 否则可为本 IP 的暴力破解刷新预算（失败记录只随窗口到期清理）。
		if token, _ := result["AccessToken"].(string); token != "" {
			if hasPassthroughIdentity(r.Header) {
				a.Identity.SetCaptured(token, r.Header)
				// Record the real administrator client identity in the UA library.
				// A library write failure must never fail the login itself.
				if err := a.UALibrary.CaptureAuthenticatedUA(r.Header); err != nil && a.Logger != nil {
					a.Logger.Warnf("UA library capture failed: %v", err)
				}
			}
		}
		if a.Logger != nil {
			a.Logger.Infof("Login success: user=%q role=admin ip=%s", body.Username, r.RemoteAddr)
		}
		writeJSON(w, http.StatusOK, result)
		return
	}

	// 2. Try regular user match
	if a.UserStore != nil {
		user := a.UserStore.Authenticate(body.Username, password)
		if user != nil {
			response, _, authErr := a.Auth.AuthenticateUser(user)
			if authErr != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]any{"message": authErr.Error()})
				return
			}
			// 同 S1：成功登录保留既有失败记录，不清零、不延长。
			if a.Logger != nil {
				a.Logger.Infof("Login success: user=%q role=user ip=%s", user.Username, r.RemoteAddr)
			}
			writeJSON(w, http.StatusOK, response)
			return
		}
	}

	// 3. No match
	a.loginLimiter.recordFailure(ip)
	if a.Logger != nil {
		a.Logger.Warnf("Login failed: user=%q ip=%s client=%q", body.Username, ip, r.Header.Get("X-Emby-Client"))
	}
	writeJSON(w, http.StatusUnauthorized, map[string]any{"message": "Invalid username or password"})
}

// isWebClientLogin reports whether the request comes from the built-in web
// player. The web client deliberately sends no Emby client identity headers
// (they would claim the passthrough live identity), so it marks its login with
// this header instead; the admin panel and real Emby clients never set it.
func isWebClientLogin(r *http.Request) bool {
	return r.Header.Get("X-EIO-Web-Client") == "1"
}

func (a *App) handleUsersPublic(w http.ResponseWriter, r *http.Request) {
	cfg := a.ConfigStore.Snapshot()
	users := []map[string]any{{
		"Name":                      cfg.Admin.Username,
		"ServerId":                  cfg.Server.ID,
		"Id":                        a.clientFacingUserIDFor(r),
		"HasPassword":               true,
		"HasConfiguredPassword":     true,
		"HasConfiguredEasyPassword": false,
	}}
	if a.UserStore != nil {
		for _, user := range a.UserStore.List() {
			if user.Enabled {
				users = append(users, map[string]any{
					"Name":                      user.Username,
					"ServerId":                  cfg.Server.ID,
					"Id":                        user.ID,
					"HasPassword":               true,
					"HasConfiguredPassword":     true,
					"HasConfiguredEasyPassword": false,
				})
			}
		}
	}
	writeJSON(w, http.StatusOK, users)
}

func (a *App) handleUserObject(w http.ResponseWriter, r *http.Request) {
	reqCtx := requestContextFrom(r.Context())
	if reqCtx != nil && reqCtx.ProxyUser != nil && reqCtx.ProxyUser.Role == "user" {
		if a.UserStore == nil {
			// A user-role token with no user store behind it has no backing
			// account; answering with the admin object would hand over the
			// administrator's profile.
			writeJSON(w, http.StatusUnauthorized, map[string]any{"message": "User store unavailable"})
			return
		}
		user := a.UserStore.Get(reqCtx.ProxyUser.UserID)
		if user != nil && user.Enabled {
			writeJSON(w, http.StatusOK, a.Auth.BuildUserObjectForUser(user))
			return
		}
		// A missing or disabled account must never fall through to the admin
		// object below: that would hand a disabled user the administrator's
		// profile — including its IsAdministrator flag — as its own.
		writeJSON(w, http.StatusUnauthorized, map[string]any{"message": "User no longer exists or is disabled"})
		return
	}
	writeJSON(w, http.StatusOK, a.Auth.BuildUserObject())
}

func (a *App) handleUserViews(w http.ResponseWriter, r *http.Request) {
	reqCtx := requestContextFrom(r.Context())
	onlineClients := a.allowedClients(reqCtx)
	cfg := a.ConfigStore.Snapshot()
	multiSource := len(onlineClients) > 1
	hidden := a.hiddenLibrariesFor(reqCtx)

	groups := fanOutClients(onlineClients, func(c *UpstreamClient) []map[string]any {
		query := cloneValues(r.URL.Query())
		query.Set("UserId", c.clientUserID())
		payload, err := c.RequestJSON(r.Context(), reqCtx, a.Identity, http.MethodGet, "/Users/"+c.clientUserID()+"/Views", query, nil)
		if err != nil {
			return nil
		}
		var items []map[string]any
		for _, item := range asItems(payload) {
			if isHiddenLibraryItem(hidden, c.ID, item) {
				continue
			}
			rewritten := deepCloneMap(item)
			rewriteResponseIDs(rewritten, c.ID, a.IDStore, cfg.Server.ID, a.clientFacingUserIDFor(r))
			if multiSource {
				if name, _ := rewritten["Name"].(string); name != "" {
					rewritten["Name"] = name + " (" + c.Name + ")"
				}
			}
			items = append(items, rewritten)
		}
		return items
	})
	allViews := flattenItems(groups)
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
