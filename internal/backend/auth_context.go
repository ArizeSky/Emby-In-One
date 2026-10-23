package backend

import (
	"context"
	"net/http"
	"net/url"
	"sort"
	"strings"
)

type requestContextKey struct{}

type RequestContext struct {
	Headers    http.Header
	ProxyToken string
	ProxyUser  *tokenInfo
	// LegacyProxyUserID is the single global virtual user ID that older responses
	// handed to every client. It is kept only so requests that still carry it are
	// recognised as the current user; the identity of the request itself always
	// comes from ProxyUser.
	LegacyProxyUserID string
	// Identifiers is the read-only signing/registration view for this request. It
	// is built once and shared by the identity predicates.
	Identifiers *IdentifierLookup
}

func (a *App) withContext(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := extractToken(r)
		var proxyUser *tokenInfo
		if token != "" {
			proxyUser = a.Auth.ValidateToken(token)
			// A token outlives the account it belongs to: disabling or deleting
			// the user must take effect on the next request, not at the next
			// login. The admin API revokes tokens when it disables an account,
			// but tokens.json and the user store are separate files that can
			// drift (a restored database, a failed store init). Without this
			// re-check requireAuth kept accepting such a token, and
			// handleUserObject even answered it with the admin user object.
			// A user-role token with no user store at all has no backing
			// account either, so it is rejected the same way.
			// A user-role token carries a snapshot of AllowedServers taken at
			// login, and tokens never expire. Re-read the live grants here so
			// narrowing (or widening) a user's server access applies on the
			// next request rather than lingering until the token is revoked or
			// the user logs in again. A missing or disabled account has no
			// backing grants and is rejected.
			if proxyUser != nil && proxyUser.Role == "user" {
				if a.UserStore == nil {
					proxyUser = nil
				} else if live, ok := a.UserStore.LiveGrants(proxyUser.UserID); ok {
					proxyUser.AllowedServers = live
				} else {
					proxyUser = nil
				}
			}
		}
		ctx := context.WithValue(r.Context(), requestContextKey{}, &RequestContext{
			Headers:           r.Header.Clone(),
			ProxyToken:        token,
			ProxyUser:         proxyUser,
			LegacyProxyUserID: a.Auth.ProxyUserID(),
			Identifiers:       a.newRequestIdentifierLookup(),
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

func (a *App) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		reqCtx := requestContextFrom(r.Context())
		if reqCtx == nil || reqCtx.ProxyUser == nil {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"message": "Authentication required"})
			return
		}
		if reqCtx.ProxyUser.Role != "admin" {
			writeJSON(w, http.StatusForbidden, map[string]any{"message": "需要管理员权限"})
			return
		}
		next(w, r)
	}
}

func requestContextFrom(ctx context.Context) *RequestContext {
	reqCtx, _ := ctx.Value(requestContextKey{}).(*RequestContext)
	return reqCtx
}

// allowedClients returns only the online upstream clients that both the
// aggregation switch and the current user's account grants allow. Admins are
// not exempt: turning a server's aggregation off removes it here too.
func (a *App) allowedClients(reqCtx *RequestContext) []*UpstreamClient {
	out := make([]*UpstreamClient, 0)
	for _, client := range a.Upstream.OnlineClients() {
		if a.canUseAggregatedServer(reqCtx, client.ID) {
			out = append(out, client)
		}
	}
	return out
}

// isServerAllowed checks whether the current user is allowed to access the
// upstream server with the given ID. Returns true for admin users (nil AllowedServers).
func (a *App) isServerAllowed(reqCtx *RequestContext, serverID string) bool {
	if reqCtx == nil || reqCtx.ProxyUser == nil {
		return false
	}
	if reqCtx.ProxyUser.AllowedServers == nil {
		return true
	}
	for _, id := range reqCtx.ProxyUser.AllowedServers {
		if id == serverID {
			return true
		}
	}
	return false
}

// requireServerAccess writes a 403 response and returns false when the current
// user is not allowed to access the upstream server that owns resolved.
// A nil resolution returns true so each caller keeps its own not-found response.
func (a *App) requireServerAccess(w http.ResponseWriter, r *http.Request, resolved *routeResolution) bool {
	if resolved == nil || a.canUseAggregatedServer(requestContextFrom(r.Context()), resolved.ServerID) {
		return true
	}
	writeJSON(w, http.StatusForbidden, map[string]any{"message": "Access denied"})
	return false
}

// queryTokenLookup finds a proxy token carried in the URL query and the key it
// came from. Matching is case-insensitive so a client that emits X-Emby-Token,
// x-emby-token, X-EMBY-TOKEN or any other casing authenticates. The api_key
// spellings keep their historical precedence over the X-Emby-Token spellings,
// and among equivalent spellings the order is fixed (the original spellings
// first, then any other casing in sorted order) so the outcome never depends on
// Go's map iteration order.
func queryTokenLookup(values url.Values) (token, key string) {
	for _, k := range []string{"api_key", "ApiKey", "X-Emby-Token", "x-emby-token", "X-EMBY-TOKEN"} {
		if v := values.Get(k); v != "" {
			return v, k
		}
	}
	var extra []string
	for k := range values {
		switch strings.ToLower(k) {
		case "api_key", "apikey", "x-emby-token":
			extra = append(extra, k)
		}
	}
	sort.Strings(extra)
	for _, k := range extra {
		if v := values.Get(k); v != "" {
			return v, k
		}
	}
	return "", ""
}

func extractToken(r *http.Request) string {
	if token := r.Header.Get("X-Emby-Token"); token != "" {
		return token
	}
	if token, _ := queryTokenLookup(r.URL.Query()); token != "" {
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
