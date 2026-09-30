package backend

import (
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	publicfs "emby-in-one/public"
)

// webFileServer serves the /web/ player client the same way adminFileServer
// serves the panel: from public/web/ when the file is on disk, from the
// embedded copy otherwise. Keeping the disk-copy-or-embedded decision shared
// with the admin panel means an operator can edit public/web/app.js and reload
// without rebuilding, while an installed binary still ships a working client.
func (a *App) webFileServer() http.Handler {
	sub, _ := fs.Sub(publicfs.Assets, "web")
	handler := http.FileServer(http.FS(sub))
	if dir := detectWebDir(); dir != "" {
		handler = preferDiskFileServer(dir, handler)
	}
	return http.StripPrefix("/web/", handler)
}

// handleWebPanel answers /web/ with the client markup instead of a directory
// listing, mirroring handleAdminPanel. The document URL has to end in a slash
// because the page's asset references are relative.
func (a *App) handleWebPanel(w http.ResponseWriter, r *http.Request) {
	page := r.Clone(r.Context())
	pageURL := *page.URL
	page.URL = &pageURL
	page.URL.Path = "/web/index.html"
	page.URL.RawPath = ""
	a.webFileServer().ServeHTTP(w, page)
}

// handleWebSPA serves the single-page client. Any /web/ path that does not map
// to a file on disk or in the embedded tree falls back to index.html so deep
// links such as /web/library/abc (or a reload on them) reach the router instead
// of a 404.
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
	page := r.Clone(r.Context())
	pageURL := *page.URL
	page.URL = &pageURL
	page.URL.Path = "/web/index.html"
	page.URL.RawPath = ""
	a.webFileServer().ServeHTTP(w, page)
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

// detectWebDir finds the on-disk public/web directory, mirroring detectPublicDir
// for the admin panel. The probes look for index.html because that is the
// document the panel serves.
func detectWebDir() string {
	for _, candidate := range []string{"public/web", filepath.Join("..", "public", "web")} {
		if _, err := os.Stat(filepath.Join(candidate, "index.html")); err == nil {
			return candidate
		}
	}
	return ""
}
