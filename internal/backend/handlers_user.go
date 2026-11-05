package backend

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
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

// Local login diagnostics.
//
// A login line used to print the account name, the client and device names and —
// at debug level — the full User-Agent, DeviceId and client version. Those are
// real identities, and ordinary runtime logs must not carry them (design §4.8
// and §8). What replaces them is a fixed vocabulary: where the identity came
// from, what the request was for, which target answered it, the revision of the
// identity record it wrote, and a stable outcome code. No argument below can hold
// a captured value, so a later edit cannot reintroduce one by accident. The
// administrator's own identity management (identity capture and the UA library)
// is unchanged; only its log output is.

// loginDiagnosticPurpose names what a local login line is about. It shares the
// outbound purpose vocabulary so a login line and an outbound preparation error
// can be correlated without naming the account or the client.
const loginDiagnosticPurpose = "local-login"

// loginDiagnosticTarget names which component answered the login request.
type loginDiagnosticTarget string

const (
	// loginTargetLocalProxy is this proxy answering the credential check itself.
	loginTargetLocalProxy loginDiagnosticTarget = "local-proxy"
	// loginTargetUALibrary is the local UA library an administrator login writes
	// the captured client identity to.
	loginTargetUALibrary loginDiagnosticTarget = "local-ua-library"
)

// loginIdentitySource names where the client identity of a login request came
// from, in the same shape as the identity resolver's own source vocabulary.
type loginIdentitySource string

const (
	// loginSourceWebClient is the built-in web player: it sends the marker header
	// and deliberately no Emby client identity.
	loginSourceWebClient loginIdentitySource = "web-client"
	// loginSourceClientIdentity is a request that presented at least one Emby
	// client identity carrier (Client, DeviceId or an Emby-family User-Agent).
	loginSourceClientIdentity loginIdentitySource = "client-identity"
	// loginSourceNone is a request with no client identity at all.
	loginSourceNone loginIdentitySource = "no-identity"
)

// loginDiagnosticCode is a stable, non-sensitive login outcome.
type loginDiagnosticCode string

const (
	// loginCodeRateLimited is a refusal by the per-IP login limiter.
	loginCodeRateLimited loginDiagnosticCode = "login-rate-limited"
	// loginCodeAdminWebClientRejected is an administrator credential presented
	// from the built-in web client, which only /admin may use.
	loginCodeAdminWebClientRejected loginDiagnosticCode = "admin-web-client-forbidden"
	// loginCodeInvalidCredentials is a credential check that matched no local
	// account. It does not say which account name was tried.
	loginCodeInvalidCredentials loginDiagnosticCode = "invalid-credentials"
	// loginCodeUALibraryWriteFailed is a failed identity-library write after a
	// successful administrator login. The login itself still succeeds.
	loginCodeUALibraryWriteFailed loginDiagnosticCode = "ua-library-write-failed"
)

// loginIdentitySourceFor classifies a login request without reading any value
// out of it. It asks the same questions the handler asks later (is this the web
// player? is a passthrough identity present?) so the log line and the code path
// cannot disagree.
func loginIdentitySourceFor(r *http.Request) loginIdentitySource {
	if r == nil {
		return loginSourceNone
	}
	switch {
	case isWebClientLogin(r):
		return loginSourceWebClient
	case hasPassthroughIdentity(r.Header):
		return loginSourceClientIdentity
	default:
		return loginSourceNone
	}
}

// loginIdentityShape reports which identity carriers a login request supplied as
// presence flags. It replaces the debug line that printed their values.
func loginIdentityShape(r *http.Request) string {
	var header http.Header
	if r != nil {
		header = r.Header
	}
	return fmt.Sprintf("ua=%s client=%s deviceId=%s deviceName=%s version=%s",
		presenceToken(header.Get("User-Agent")),
		presenceToken(header.Get("X-Emby-Client")),
		presenceToken(header.Get("X-Emby-Device-Id")),
		presenceToken(header.Get("X-Emby-Device-Name")),
		presenceToken(header.Get("X-Emby-Client-Version")))
}

func presenceToken(value string) string {
	if strings.TrimSpace(value) == "" {
		return "absent"
	}
	return "present"
}

// logLoginEvent writes one credential-check diagnostic. Every field is a fixed
// token or a counter; the account name, User-Agent, Client,
// DeviceName and DeviceId a request carried are never arguments, so a captured
// identity cannot reach a log line from here.
func (a *App) logLoginEvent(level, event string, source loginIdentitySource, target loginDiagnosticTarget, ip string, code loginDiagnosticCode) {
	if a == nil || a.Logger == nil {
		return
	}
	message := fmt.Sprintf("%s: purpose=%s target=%s source=%s", event, loginDiagnosticPurpose, target, source)
	if code != "" {
		message += " code=" + string(code)
	}
	switch level {
	case "warn":
		a.Logger.Warnf("%s", message)
	case "debug":
		a.Logger.Debugf("%s", message)
	default:
		a.Logger.Infof("%s", message)
	}
}

// logLoginSuccess records which local role authenticated. The role is the fixed
// enum "admin" or "user"; the account name stays out of the log.
func (a *App) logLoginSuccess(source loginIdentitySource, ip, role string) {
	if a == nil || a.Logger == nil {
		return
	}
	a.Logger.Infof("Login success: purpose=%s target=%s source=%s role=%s",
		loginDiagnosticPurpose, loginTargetLocalProxy, source, role)
}

// logAdminIdentityCapture records the UA-library half of an administrator login:
// how many resident identities exist and the highest revision among them. The
// two counters correlate the capture with the records the admin panel manages;
// the captured User-Agent, Client, DeviceName and DeviceId stay out of the log.
func (a *App) logAdminIdentityCapture(source loginIdentitySource, residents int, revision uint64) {
	if a == nil || a.Logger == nil {
		return
	}
	a.Logger.Infof("Admin identity captured: purpose=%s target=%s source=%s records=%d revision=%d",
		loginDiagnosticPurpose, loginTargetUALibrary, source, residents, revision)
}

// uaLibraryDiagnosticState reports the resident identity count and the highest
// resident revision of the UA library. Both are counters, never identity values.
func (a *App) uaLibraryDiagnosticState() (int, uint64) {
	if a == nil || a.UALibrary == nil {
		return 0, 0
	}
	snapshot := a.UALibrary.Snapshot()
	revision := uint64(0)
	for _, resident := range snapshot.Resident {
		if resident.Revision > revision {
			revision = resident.Revision
		}
	}
	return len(snapshot.Resident), revision
}

func (a *App) handleAuthenticateByName(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r, a.ConfigStore.Snapshot().Server.TrustProxy)
	source := loginIdentitySourceFor(r)
	if !a.loginLimiter.allowed(ip) {
		a.logLoginEvent("warn", "Login rate limited", source, loginTargetLocalProxy, ip, loginCodeRateLimited)
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
	a.logLoginEvent("info", "Login attempt", source, loginTargetLocalProxy, ip, "")
	if a.Logger != nil {
		a.Logger.Debugf("Login identity shape: %s", loginIdentityShape(r))
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
			a.logLoginEvent("warn", "Login rejected", source, loginTargetLocalProxy, ip, loginCodeAdminWebClientRejected)
			writeJSON(w, http.StatusForbidden, map[string]any{"message": "管理员账号不能通过 Web 客户端登录，请使用 /admin 管理面板"})
			return
		}
		// 登录成功不能清除本 IP 的失败记录：低权账号的成功登录
		// 否则可为本 IP 的暴力破解刷新预算（失败记录只随窗口到期清理）。
		if token, _ := result["AccessToken"].(string); token != "" {
			if hasPassthroughIdentity(r.Header) {
				a.Identity.SetCaptured(token, r.Header)
				// Record the real administrator client identity in the UA library.
				// A library write failure must never fail the login itself, and the
				// store already reports the failed write; the diagnostic here is the
				// non-sensitive outcome code.
				if err := a.UALibrary.CaptureAuthenticatedUA(r.Header); err != nil {
					a.logLoginEvent("warn", "Admin identity capture failed", source, loginTargetUALibrary, ip, loginCodeUALibraryWriteFailed)
				} else if a.UALibrary != nil {
					residents, revision := a.uaLibraryDiagnosticState()
					a.logAdminIdentityCapture(source, residents, revision)
				}
			}
		}
		a.logLoginSuccess(source, ip, "admin")
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
			a.logLoginSuccess(source, ip, "user")
			writeJSON(w, http.StatusOK, response)
			return
		}
	}

	// 3. No match
	a.loginLimiter.recordFailure(ip)
	a.logLoginEvent("warn", "Login failed", source, loginTargetLocalProxy, ip, loginCodeInvalidCredentials)
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
