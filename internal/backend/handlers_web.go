package backend

import (
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	publicfs "emby-in-one/public"
)

// webFileServer serves the /web/ client's static assets the same way
// adminFileServer serves the panel: from public/ on disk when the file is
// there, from the embedded copy otherwise.
//
// The handler is rooted at the embedded FS root — not at the web/ subdirectory
// — because the client's markup references the shared vendor/ tree
// (vue.global.prod.js, tailwind.css, vendor/web/*.wasm) that lives beside
// admin.html, not inside public/web/. Rooting at "." makes /web/vendor/... and
// /web/web/app.js both resolve, exactly as /admin/vendor/... does for the panel.
func (a *App) webFileServer() http.Handler {
	sub, _ := fs.Sub(publicfs.Assets, ".")
	handler := http.FileServer(http.FS(sub))
	if dir := detectWebDir(); dir != "" {
		handler = preferDiskFileServer(dir, handler)
	}
	return http.StripPrefix("/web/", handler)
}

// handleWebPanel answers /web/ with the client markup instead of a directory
// listing, mirroring handleAdminPanel.
func (a *App) handleWebPanel(w http.ResponseWriter, r *http.Request) {
	a.serveWebDocument(w, r)
}

// handleWebSPA serves the single-page client. Any /web/ path that does not map
// to a static asset falls back to the document so deep links such as
// /web/library/abc (or a reload on them) reach the client router instead of a
// 404.
//
// Static assets are recognised by their extension: a request for
// "vendor/vue.global.prod.js" must never receive HTML, because a script served
// with the wrong content type breaks the page in a way that is hard to diagnose.
func (a *App) handleWebSPA(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/web/")
	if path == "" {
		a.handleWebPanel(w, r)
		return
	}
	if hasStaticExtension(path) {
		a.webFileServer().ServeHTTP(w, r)
		return
	}
	a.serveWebDocument(w, r)
}

// serveWebDocument writes the client document (public/web/web.html).
//
// The document is served directly rather than through webFileServer because
// the file server is rooted at the public root, so reaching the file from a
// /web/ URL would mean rewriting the request to /web/web/web.html. The file is
// also deliberately not named index.html: http.FileServer redirects any path
// ending in /index.html to "./" (its canonical-URL rule), which would turn a
// rewrite into a redirect loop. Writing the bytes explicitly sidesteps both.
func (a *App) serveWebDocument(w http.ResponseWriter, r *http.Request) {
	data, err := publicfs.Assets.ReadFile("web/web.html")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if dir := detectWebDir(); dir != "" {
		if b, readErr := os.ReadFile(filepath.Join(dir, "web", "web.html")); readErr == nil {
			data = b
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(data)
}

// hasStaticExtension reports whether the path names a concrete asset rather
// than a client route. Prefix matching keeps ".wasm" and ".js" apart from the
// extensionless route segments the SPA uses.
func hasStaticExtension(path string) bool {
	for _, ext := range []string{
		".js", ".mjs", ".css", ".html", ".wasm", ".map",
		".woff2", ".woff", ".ttf", ".otf",
		".png", ".jpg", ".jpeg", ".gif", ".webp", ".svg", ".ico",
		".json", ".mem", ".txt",
	} {
		if strings.HasSuffix(strings.ToLower(path), ext) {
			return true
		}
	}
	return false
}

// webCrossOriginIsolation sets the headers that let the WASM player use
// SharedArrayBuffer, which its decoders need for multi-threaded operation.
//
// Both headers have to be present together: COEP: require-corp on its own
// would block the cross-origin resources the client loads, and COOP alone does
// not enable crossOriginIsolated. They are scoped to /web/ so the Emby API
// surface keeps answering clients that know nothing about isolation — a
// credential-less CORP header on every API response would be wrong for the
// header-based auth this proxy uses.
func webCrossOriginIsolation(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
		w.Header().Set("Cross-Origin-Embedder-Policy", "require-corp")
		next.ServeHTTP(w, r)
	})
}

// detectWebDir finds the on-disk public root that carries the web client,
// mirroring detectPublicDir for the admin panel. It returns the public root
// (not public/web) because the client also loads the shared vendor/ tree from
// there; the probe file is web/web.html.
func detectWebDir() string {
	for _, candidate := range []string{"public", filepath.Join("..", "public")} {
		if _, err := os.Stat(filepath.Join(candidate, "web", "web.html")); err == nil {
			return candidate
		}
	}
	return ""
}
