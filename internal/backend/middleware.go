package backend

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	allowMethods = "GET, POST, PUT, DELETE, OPTIONS"
	allowHeaders = "Content-Type, Authorization, X-Emby-Token, X-Emby-Authorization, X-Emby-Client, X-Emby-Client-Version, X-Emby-Device-Name, X-Emby-Device-Id"
)

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

// clientIP extracts the real client IP. Proxy headers are only trusted when trustProxy is true.
func clientIP(r *http.Request, trustProxy bool) string {
	if trustProxy {
		if ip := strings.TrimSpace(r.Header.Get("X-Real-IP")); ip != "" {
			return ip
		}
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			if ip, _, _ := strings.Cut(xff, ","); strings.TrimSpace(ip) != "" {
				return strings.TrimSpace(ip)
			}
		}
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

func (a *App) loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.Logger == nil || r.URL.Path == "/favicon.ico" {
			next.ServeHTTP(w, r)
			return
		}
		if isAdminAPIPath(r.URL.Path) {
			a.serveAdminAPIWithAudit(next, w, r)
			return
		}
		if isAdminPath(r.URL.Path) {
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

// serveAdminAPIWithAudit records admin API activity. Successful reads are skipped
// (the panel polls status and logs), but every state-changing call and every failed
// one is logged with the acting account. Request bodies and query strings are never
// logged: they carry passwords and API keys.
func (a *App) serveAdminAPIWithAudit(next http.Handler, w http.ResponseWriter, r *http.Request) {
	sc := &statusCapture{ResponseWriter: w, status: http.StatusOK}
	start := time.Now()
	next.ServeHTTP(sc, r)
	if r.Method == http.MethodGet && sc.status < http.StatusBadRequest {
		return
	}
	actor := "unknown token"
	if info := a.Auth.ValidateToken(extractToken(r)); info != nil {
		actor = info.Username
		if info.Role != "admin" {
			actor += " (role:" + info.Role + ")"
		}
	}
	a.Logger.Infof("admin %q: %s %s → %d (%dms)",
		actor, r.Method, r.URL.Path, sc.status, time.Since(start).Milliseconds())
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
			if origin := r.Header.Get("Origin"); origin != "" && sameOrigin(origin, adminRequestOrigin(r)) {
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

// adminPanelCSP names the panel's own resources and nothing else. Vue, lucide,
// Tailwind and Inter are all served from public/vendor/ (see
// public/vendor/README.md), so no third-party origin appears here any more: an
// injected script has nowhere external to load from and nowhere to send data.
//
// 'unsafe-eval' is still required — the panel mounts an in-DOM template and
// vue.global.prod.js compiles it with new Function() at runtime. Dropping it means
// precompiling the template, which is a separate change.
//
// 'unsafe-inline' is gone from both script-src and style-src: the panel has no
// inline <script>, no on*= handler, no style= attribute and no inline <style>
// block (those rules moved into assets/panel.css and are part of the generated
// stylesheet). The admin panel asset tests assert that, so a future inline
// attribute cannot silently outrun the policy.
const adminPanelCSP = "default-src 'self'; " +
	"script-src 'self' 'unsafe-eval'; " +
	"style-src 'self'; " +
	"font-src 'self'; " +
	"img-src 'self' data:; " +
	"connect-src 'self'; " +
	"object-src 'none'; " +
	"base-uri 'none'; " +
	"form-action 'self'; " +
	"frame-ancestors 'self'"

func applyAdminSecurityHeaders(w http.ResponseWriter) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "SAMEORIGIN")
	// X-XSS-Protection is deliberately not set: browsers have removed the filter,
	// and the legacy Safari auditor it enabled introduced bypasses of its own.
	w.Header().Set("Content-Security-Policy", adminPanelCSP)
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

func adminRequestOrigin(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if proto := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Proto"), ",")[0]); proto != "" {
		scheme = proto
	}
	return scheme + "://" + r.Host
}
