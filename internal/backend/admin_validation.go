package backend

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	publicfs "emby-in-one/public"
)

func (a *App) adminFileServer() http.Handler {
	if dir := detectPublicDir(); dir != "" {
		return http.StripPrefix("/admin/", http.FileServer(http.Dir(dir)))
	}
	sub, _ := fs.Sub(publicfs.Assets, ".")
	return http.StripPrefix("/admin/", http.FileServer(http.FS(sub)))
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
