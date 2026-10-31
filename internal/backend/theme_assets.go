package backend

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"
	"strings"
)

// Public, anonymous theme resources.
//
// Only resources registered in the theme store index are served. A request
// path is never joined to a filesystem path: the hash and logical name are
// looked up, and the store returns the validated bytes with their
// content-derived MIME type. Unknown, out-of-range or uninstalled resources are
// a real 404 (text/plain), never the /web SPA document, so a missing theme
// asset cannot silently become HTML.
//
// The generated stylesheet, images and the fixed preview document carry no
// user data or credentials and may be cached by browsers even after a theme is
// disabled; that is the documented product boundary.

// themeStoreInstance returns the store or nil. Every caller stays nil-safe.
func (a *App) themeStoreInstance() *ThemeStore {
	if a == nil {
		return nil
	}
	return a.ThemeStore
}

// themeAPIResourceError writes a non-HTML resource error.
func themeAPIResourceError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(message + "\n"))
}

// themeAPIStoreResourceStatus maps a store read failure to a resource status.
func themeAPIStoreResourceStatus(err error) (int, string) {
	switch {
	case errors.Is(err, ErrThemeResourceNotFound), errors.Is(err, ErrThemeCorrupt):
		return http.StatusNotFound, themeAPIMsgResourceNotFound
	case errors.Is(err, ErrThemeStoreClosed), errors.Is(err, ErrThemeStoreUnavailable):
		return http.StatusServiceUnavailable, themeAPIMsgUnavailable
	default:
		return http.StatusNotFound, themeAPIMsgResourceNotFound
	}
}

// handleThemeCSSResource answers /web/themes/{hash}/theme.css.
func (a *App) handleThemeCSSResource(w http.ResponseWriter, r *http.Request) {
	hash := r.PathValue("hash")
	if !themeAPIHashValid(hash) {
		themeAPIResourceError(w, http.StatusNotFound, themeAPIMsgResourceNotFound)
		return
	}
	store := a.themeStoreInstance()
	if store == nil || !store.Available() {
		themeAPIResourceError(w, http.StatusServiceUnavailable, themeAPIMsgUnavailable)
		return
	}
	definition, err := store.Definition(hash)
	if err != nil {
		status, message := themeAPIStoreResourceStatus(err)
		themeAPIResourceError(w, status, message)
		return
	}
	css, err := renderThemeCSS(hash, definition)
	if err != nil {
		themeAPIResourceError(w, http.StatusNotFound, themeAPIMsgResourceNotFound)
		return
	}
	sum := sha256.Sum256(css)
	etag := `"` + hex.EncodeToString(sum[:]) + `"`
	if themeAPIETagMatches(r, etag) {
		w.Header().Set("ETag", etag)
		themeAPIImmutable(w)
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("ETag", etag)
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(css)))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	themeAPIImmutable(w)
	_, _ = w.Write(css)
}

// handleThemePreviewImagePNG answers /web/themes/{hash}/preview.png.
func (a *App) handleThemePreviewImagePNG(w http.ResponseWriter, r *http.Request) {
	a.serveThemeImage(w, r, "preview.png")
}

// handleThemePreviewImageJPG answers /web/themes/{hash}/preview.jpg.
func (a *App) handleThemePreviewImageJPG(w http.ResponseWriter, r *http.Request) {
	a.serveThemeImage(w, r, "preview.jpg")
}

// handleThemeAssetImage answers /web/themes/{hash}/assets/{asset...}.
func (a *App) handleThemeAssetImage(w http.ResponseWriter, r *http.Request) {
	asset := r.PathValue("asset")
	if asset == "" || strings.Contains(asset, "..") {
		themeAPIResourceError(w, http.StatusNotFound, themeAPIMsgResourceNotFound)
		return
	}
	a.serveThemeImage(w, r, "assets/"+asset)
}

// serveThemeImage serves one registered image descriptor.
func (a *App) serveThemeImage(w http.ResponseWriter, r *http.Request, name string) {
	hash := r.PathValue("hash")
	if !themeAPIHashValid(hash) {
		themeAPIResourceError(w, http.StatusNotFound, themeAPIMsgResourceNotFound)
		return
	}
	store := a.themeStoreInstance()
	if store == nil || !store.Available() {
		themeAPIResourceError(w, http.StatusServiceUnavailable, themeAPIMsgUnavailable)
		return
	}
	resource, err := store.ReadAsset(hash, name)
	if err != nil {
		status, message := themeAPIStoreResourceStatus(err)
		themeAPIResourceError(w, status, message)
		return
	}
	if resource.MIME != "image/png" && resource.MIME != "image/jpeg" {
		themeAPIResourceError(w, http.StatusNotFound, themeAPIMsgResourceNotFound)
		return
	}
	etag := ""
	if resource.SHA256 != "" {
		etag = `"` + resource.SHA256 + `"`
	}
	if etag != "" && themeAPIETagMatches(r, etag) {
		w.Header().Set("ETag", etag)
		themeAPIImmutable(w)
		w.WriteHeader(http.StatusNotModified)
		return
	}
	if etag != "" {
		w.Header().Set("ETag", etag)
	}
	w.Header().Set("Content-Type", resource.MIME)
	w.Header().Set("Content-Length", strconv.Itoa(len(resource.Data)))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	themeAPIImmutable(w)
	_, _ = w.Write(resource.Data)
}

// handleThemePreviewDocument answers /web/themes/{hash}/preview.html. The
// document is generated here, never taken from the package, and has no script,
// no user data and no media reference.
func (a *App) handleThemePreviewDocument(w http.ResponseWriter, r *http.Request) {
	hash := r.PathValue("hash")
	if !themeAPIHashValid(hash) {
		themeAPIResourceError(w, http.StatusNotFound, themeAPIMsgResourceNotFound)
		return
	}
	mode := r.URL.Query().Get("mode")
	if mode != ThemeModeLight && mode != ThemeModeDark {
		themeAPIResourceError(w, http.StatusBadRequest, themeAPIMsgPreviewMode)
		return
	}
	store := a.themeStoreInstance()
	if store == nil || !store.Available() {
		themeAPIResourceError(w, http.StatusServiceUnavailable, themeAPIMsgUnavailable)
		return
	}
	if _, err := store.Definition(hash); err != nil {
		status, message := themeAPIStoreResourceStatus(err)
		themeAPIResourceError(w, status, message)
		return
	}
	previewURL := ""
	if resource, err := store.ReadAsset(hash, "preview.png"); err == nil && resource.MIME == "image/png" {
		previewURL = themeAPIResourceURL(hash, "preview.png")
	} else if resource, err := store.ReadAsset(hash, "preview.jpg"); err == nil && resource.MIME == "image/jpeg" {
		previewURL = themeAPIResourceURL(hash, "preview.jpg")
	}
	document := themePreviewDocument(hash, mode, previewURL)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "SAMEORIGIN")
	w.Header().Set("Content-Security-Policy", themePreviewCSP)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(document)))
	themeAPIRevalidate(w)
	_, _ = w.Write([]byte(document))
}

// handleThemeResourceNotFound is the /web/themes/ catch-all: unknown paths,
// wrong methods and directory listings all end as plain 404, not SPA HTML.
func (a *App) handleThemeResourceNotFound(w http.ResponseWriter, r *http.Request) {
	themeAPIResourceError(w, http.StatusNotFound, themeAPIMsgResourceNotFound)
}

// themePreviewCSP is the fixed preview policy: no script at all, same-origin
// styles and images, same-origin framing only.
const themePreviewCSP = "default-src 'none'; " +
	"script-src 'none'; " +
	"style-src 'self'; " +
	"img-src 'self'; " +
	"base-uri 'none'; " +
	"form-action 'none'; " +
	"frame-ancestors 'self'"

// themeAPIETagMatches implements a strong If-None-Match comparison.
func themeAPIETagMatches(r *http.Request, etag string) bool {
	header := strings.TrimSpace(r.Header.Get("If-None-Match"))
	if header == "" {
		return false
	}
	for _, candidate := range strings.Split(header, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" || candidate == etag {
			return true
		}
	}
	return false
}

// themePreviewDocument builds the fixed preview markup. Only the already
// validated content hash, the fixed mode and a server-built resource URL are
// interpolated.
func themePreviewDocument(hash, mode, previewURL string) string {
	poster := `<span>示例封面</span>`
	if previewURL != "" {
		poster = `<img src="` + previewURL + `" alt="">`
	}
	return `<!DOCTYPE html>
<html lang="zh-CN" data-eio-theme="` + mode + `">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>主题预览</title>
<link rel="stylesheet" href="` + themeAPICSSURL(hash) + `">
</head>
<body class="eio-preview">
<div class="eio-preview-shell">
<aside class="eio-preview-nav">
<div class="eio-preview-brand"><span class="eio-preview-mark">E</span><span>预览</span></div>
<div class="eio-preview-nav-item eio-preview-nav-item-active">首页</div>
<div class="eio-preview-nav-item">媒体库</div>
<div class="eio-preview-nav-item">收藏</div>
<div class="eio-preview-nav-item">设置</div>
</aside>
<main class="eio-preview-main">
<div class="eio-preview-titlebar"><h1>主题预览</h1><span class="eio-preview-badge">示例</span></div>
<div class="eio-preview-grid">
<article class="eio-preview-card"><div class="eio-preview-poster">` + poster + `</div><div class="eio-preview-card-body"><div class="eio-preview-card-title">示例卡片</div><div class="eio-preview-card-sub">副标题文本</div></div></article>
<article class="eio-preview-card"><div class="eio-preview-poster">示例封面</div><div class="eio-preview-card-body"><div class="eio-preview-card-title">示例卡片</div><div class="eio-preview-card-sub">副标题文本</div></div></article>
<article class="eio-preview-card"><div class="eio-preview-poster">示例封面</div><div class="eio-preview-card-body"><div class="eio-preview-card-title">示例卡片</div><div class="eio-preview-card-sub">副标题文本</div></div></article>
</div>
<div class="eio-preview-panel"><p>内容面板</p><p class="eio-preview-sub">固定预览内容，不包含任何账号、媒体或上游数据。</p><p><a class="eio-preview-link" href="#">强调链接示例</a></p></div>
</main>
</div>
</body>
</html>
`
}
