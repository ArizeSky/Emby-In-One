package backend

import (
	"context"
	"net/http"
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

// allowedClients returns only the online upstream clients that the
// current user is permitted to access. Admin users see all online servers.
func (a *App) allowedClients(reqCtx *RequestContext) []*UpstreamClient {
	all := a.Upstream.OnlineClients()
	if reqCtx == nil || reqCtx.ProxyUser == nil {
		return nil
	}
	if reqCtx.ProxyUser.AllowedServers == nil {
		return all
	}
	allowed := make(map[string]bool, len(reqCtx.ProxyUser.AllowedServers))
	for _, id := range reqCtx.ProxyUser.AllowedServers {
		allowed[id] = true
	}
	filtered := make([]*UpstreamClient, 0, len(all))
	for _, c := range all {
		if allowed[c.ID] {
			filtered = append(filtered, c)
		}
	}
	return filtered
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
	if resolved == nil || a.isServerAllowed(requestContextFrom(r.Context()), resolved.ServerID) {
		return true
	}
	writeJSON(w, http.StatusForbidden, map[string]any{"message": "Access denied"})
	return false
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
