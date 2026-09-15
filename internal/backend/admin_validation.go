package backend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	publicfs "emby-in-one/public"
)

func (a *App) adminFileServer() http.Handler {
	sub, _ := fs.Sub(publicfs.Assets, ".")
	handler := http.FileServer(http.FS(sub))
	if dir := detectPublicDir(); dir != "" {
		handler = preferDiskFileServer(dir, handler)
	}
	return http.StripPrefix("/admin/", handler)
}

// handleAdminPanel answers /admin/ with the panel markup instead of the directory
// listing http.FileServer would otherwise produce there.
//
// The panel is served from the directory URL rather than from /admin/admin.html so
// that the address bar names the panel and not a file inside it. It has to be the
// directory URL and not the bare "/admin": the page's own asset references are
// relative ("vendor/vue.global.prod.js", "admin.js"), so they only resolve when the
// document URL ends in a slash — which is also why "/admin" still redirects here
// rather than serving the same bytes.
//
// Reusing adminFileServer keeps the disk-copy-or-embedded-copy decision in one place:
// it is rooted at /admin/, so handing it the path it already knows how to resolve is
// enough. http.StripPrefix copies the request before rewriting the path, so the
// original is left alone for the redirect handler that shares it.
func (a *App) handleAdminPanel(w http.ResponseWriter, r *http.Request) {
	page := r.Clone(r.Context())
	pageURL := *page.URL
	page.URL = &pageURL
	page.URL.Path = "/admin/admin.html"
	page.URL.RawPath = ""
	a.adminFileServer().ServeHTTP(w, page)
}

// preferDiskFileServer answers from dir when that file is on disk and from the
// embedded copy otherwise.
//
// A public/ directory used to be all-or-nothing: the moment it held an admin.html,
// every embedded asset was ignored. The install scripts fetch admin.html and admin.js
// into public/ but have no way to create public/vendor/, so an installed panel would
// load its markup and then 404 on its own stylesheet. Deciding per file keeps the
// developer override (edit public/admin.html, reload, no rebuild) while letting the
// binary supply whatever the directory is missing.
//
// This only ever narrows what reaches http.Dir: a path that used to be served from
// disk still is, and everything else goes to the embedded FS.
func preferDiskFileServer(dir string, embedded http.Handler) http.Handler {
	disk := http.FileServer(http.Dir(dir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hasDiskFile(dir, r.URL.Path) {
			disk.ServeHTTP(w, r)
			return
		}
		embedded.ServeHTTP(w, r)
	})
}

// hasDiskFile reports whether dir holds a servable file at the request path. A
// directory counts only when it has an index.html, because that is the one case the
// two servers can agree on: http.FileServer would otherwise render a directory
// listing for the on-disk copy, which the embedded copy has no equivalent for.
func hasDiskFile(dir, urlPath string) bool {
	clean := path.Clean("/" + urlPath)
	// http.Dir refuses to open a name containing a backslash on Windows, but
	// os.Stat would happily read one as a separator, so reject them here and let the
	// embedded copy answer.
	if strings.ContainsRune(clean, '\\') {
		return false
	}
	info, err := os.Stat(filepath.Join(dir, filepath.FromSlash(clean)))
	if err != nil {
		return false
	}
	if !info.IsDir() {
		return true
	}
	_, err = os.Stat(filepath.Join(dir, filepath.FromSlash(clean), "index.html"))
	return err == nil
}

func detectPublicDir() string {
	for _, candidate := range []string{"public", filepath.Join("..", "public")} {
		if _, err := os.Stat(filepath.Join(candidate, "admin.html")); err == nil {
			return candidate
		}
	}
	return ""
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
	if draft.SpoofClient == "passthrough" && strings.TrimSpace(draft.APIKey) == "" {
		source, _ := client.resolveIdentityHeaders(reqCtx, a.Identity, nil)
		if source == "infuse-fallback" {
			if a.Logger != nil {
				a.Logger.Infof("[%s] Passthrough admin validation deferred — no captured client identity yet", draft.Name)
			}
			return upstreamValidationResult{Online: false, Warning: passthroughDeferredWarning}, nil
		}
	}
	client.Login(context.Background(), reqCtx, a.Identity)
	snapshot := client.snapshot()
	if snapshot.Online && snapshot.AccessToken != "" && snapshot.UserID != "" {
		return upstreamValidationResult{Online: true}, nil
	}
	if draft.SpoofClient == "passthrough" {
		if shouldDeferPassthroughValidation(snapshot.LastError) {
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

// probeTargetBlockedMessage explains why the connectivity probe refused a target
// and how the admin can verify the same path instead.
func probeTargetBlockedMessage(host, reason string) string {
	switch reason {
	case "link-local":
		return "目标地址 " + host + " 属于链路本地地址（169.254.0.0/16、fe80::/10），通常由云服务器元数据服务占用，既不能作为代理测试目标，也不能添加为上游服务器。"
	default:
		return "目标地址 " + host + " 属于内网/本机地址，代理连通性测试只接受公网目标。如果它是你的 Emby 服务器，请直接在「上游节点」中添加它：添加时会用真实登录验证经过该代理的连通性。"
	}
}

// Timeout bounds in milliseconds. The upper bounds stay far below the point where
// time.Duration(value)*time.Millisecond overflows (about 9.2e12 ms), so a value
// that can only be a mistake cannot silently disable or shorten a timeout.
const (
	maxTimeoutMillis  = 3_600_000  // 1 hour
	maxIntervalMillis = 86_400_000 // 24 hours
	maxGraceMillis    = 60_000     // 1 minute
)

// positiveTimeoutKeys must be at least 1 ms. The grace periods additionally accept
// 0, which disables the grace timer.
var (
	positiveTimeoutKeys    = []string{"api", "global", "login", "healthCheck", "healthInterval"}
	nonNegativeTimeoutKeys = []string{"searchGracePeriod", "metadataGracePeriod", "latestGracePeriod"}
)

// assignTimeout copies one panel-supplied timeout override into the config.
func assignTimeout(timeouts *TimeoutsConfig, key string, value int) {
	switch key {
	case "api":
		timeouts.API = value
	case "global":
		timeouts.Global = value
	case "login":
		timeouts.Login = value
	case "healthCheck":
		timeouts.HealthCheck = value
	case "healthInterval":
		timeouts.HealthInterval = value
	case "searchGracePeriod":
		timeouts.SearchGracePeriod = value
	case "metadataGracePeriod":
		timeouts.MetadataGracePeriod = value
	case "latestGracePeriod":
		timeouts.LatestGracePeriod = value
	}
}

// validateTimeouts rejects timeout values that are negative, out of range, or large
// enough to overflow once converted to a time.Duration. It guards both the admin
// panel and a hand-edited config file.
func validateTimeouts(timeouts TimeoutsConfig) error {
	checks := []struct {
		name     string
		value    int
		minValue int
		maxValue int
	}{
		{"api", timeouts.API, 1, maxTimeoutMillis},
		{"global", timeouts.Global, 1, maxTimeoutMillis},
		{"login", timeouts.Login, 1, maxTimeoutMillis},
		{"healthCheck", timeouts.HealthCheck, 1, maxTimeoutMillis},
		{"healthInterval", timeouts.HealthInterval, 1, maxIntervalMillis},
		{"searchGracePeriod", timeouts.SearchGracePeriod, 0, maxGraceMillis},
		{"metadataGracePeriod", timeouts.MetadataGracePeriod, 0, maxGraceMillis},
		{"latestGracePeriod", timeouts.LatestGracePeriod, 0, maxGraceMillis},
	}
	for _, check := range checks {
		if check.value < check.minValue || check.value > check.maxValue {
			return &httpError{message: fmt.Sprintf("timeouts.%s 必须介于 %d 和 %d 之间（毫秒），当前值 %d",
				check.name, check.minValue, check.maxValue, check.value)}
		}
	}
	return nil
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
		if body.MaxConcurrent != nil {
			if *body.MaxConcurrent >= 0 {
				dst.MaxConcurrent = *body.MaxConcurrent
			}
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
	if body.MaxConcurrent != nil {
		if *body.MaxConcurrent >= 0 {
			dst.MaxConcurrent = *body.MaxConcurrent
		}
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

func toNonNegativeInt(raw any) (int, bool) {
	switch value := raw.(type) {
	case float64:
		if value >= 0 {
			return int(value), true
		}
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(value))
		if err == nil && parsed >= 0 {
			return parsed, true
		}
	case json.Number:
		parsed, err := value.Int64()
		if err == nil && parsed >= 0 {
			return int(parsed), true
		}
	}
	return 0, false
}

func intToString(v int) string {
	return strconv.Itoa(v)
}
