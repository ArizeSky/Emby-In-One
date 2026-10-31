package backend

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"
)

// /web theme API: frozen DTOs, strict request decoding, limits and error
// mapping.
//
// The DTOs below are the wire contract shared with the admin panel (T3) and
// the web client (T1/T4). Field names are lower camel case and every field is
// always present (no omitempty) so a client can rely on the shape.
//
// Resource URLs are immutable content-hash URLs:
//
//	/web/themes/<hash>/theme.css
//	/web/themes/<hash>/preview.png | preview.jpg
//	/web/themes/<hash>/assets/<name>.png | .jpg | .jpeg
//	/web/themes/<hash>/preview.html?mode=light|dark   (fixed generated document)
//
// No DTO ever carries a token, a filesystem path, a user ID or an upstream
// address.

// themeAPIMaxJSONBytes is the fixed budget for every theme JSON request. It is
// independent of, and much smaller than, the global 2 MiB body cap.
const themeAPIMaxJSONBytes = 16 << 10

// Fixed response messages. They never echo request content, a theme ID, a
// package detail or a database error.
const (
	themeAPIMsgInvalidRequest     = "请求格式无效"
	themeAPIMsgBodyTooLarge       = "请求体超过限制"
	themeAPIMsgUnsupportedType    = "不支持的内容类型"
	themeAPIMsgAuthRequired       = "需要登录"
	themeAPIMsgAdminRequired      = "需要管理员权限"
	themeAPIMsgHeaderToken        = "写操作需要在请求头中提供令牌"
	themeAPIMsgUnavailable        = "主题服务暂不可用"
	themeAPIMsgBusy               = "主题包正在验证，请稍后重试"
	themeAPIMsgConflict           = "主题状态已变化，请刷新后重试"
	themeAPIMsgNotFound           = "主题不存在或已被删除"
	themeAPIMsgExists             = "主题已安装，请使用更新接口"
	themeAPIMsgBuiltinImmutable   = "内置主题不可修改"
	themeAPIMsgDefaultInUse       = "该主题仍是站点默认主题"
	themeAPIMsgQuota              = "主题数量或空间配额已满"
	themeAPIMsgThemeUnavailable   = "该主题当前不可选"
	themeAPIMsgInvalidPackage     = "主题包无效"
	themeAPIMsgTargetMismatch     = "主题包与目标主题不匹配"
	themeAPIMsgPackageTooLarge    = "主题包超过 1.5 MiB 限制"
	themeAPIMsgPackageUnsupported = "主题包使用了不支持的压缩或加密方式"
	themeAPIMsgMissingRevision    = "缺少有效的 If-Match revision"
	themeAPIMsgMissingIdentity    = "当前账号无法保存主题偏好"
	themeAPIMsgPreviewMode        = "预览仅支持 light 或 dark 模式"
	themeAPIMsgResourceNotFound   = "资源不存在"
)

// Fixed media types.
const (
	themeAPIZipMediaType  = "application/zip"
	themeAPIJSONMediaType = "application/json"
)

// themeAPIRetryAfterSeconds is sent with 503 when the single verification slot
// is busy.
const themeAPIRetryAfterSeconds = "2"

// themeAPIVerificationSlots serializes package parsing/image decoding across
// the whole process. It is deliberately a one-slot, non-blocking semaphore: a
// crowded server answers 503 + Retry-After instead of queueing work, and
// verification never runs while holding adminMutationMu.
var themeAPIVerificationSlots = make(chan struct{}, 1)

// errThemeAPIJSON is the internal classification for malformed theme JSON
// input. It never reaches a response body.
var errThemeAPIJSON = errors.New("theme api: invalid JSON input")

// themeAPISiteDTO is the anonymous site default.
//
// GET /web/api/theme-site returns 200 with available=false and themeId
// "builtin" when the store is unavailable. The admin site response uses the
// same shape and always reports available=true.
//
// Theme carries the selected public theme entry (id, hash, URLs and validated
// definition) so an anonymous login page can render the site default without
// the authenticated catalog. It is null for the builtin default, a disabled or
// removed theme, and for store failures; the admin site shape also leaves it
// null.
type themeAPISiteDTO struct {
	Available bool                    `json:"available"`
	ThemeID   string                  `json:"themeId"`
	Revision  int64                   `json:"revision"`
	Theme     *themeAPIPublicThemeDTO `json:"theme"`
}

// themeAPIPublicThemeDTO is one selectable theme for /web clients. The builtin
// theme is listed first with builtin=true, an empty hash and empty URLs; its
// light/dark tokens live in the client stylesheet.
type themeAPIPublicThemeDTO struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	Author          string   `json:"author"`
	Description     string   `json:"description"`
	Version         string   `json:"version"`
	Hash            string   `json:"hash"`
	Variants        []string `json:"variants"`
	Builtin         bool     `json:"builtin"`
	CSSURL          string   `json:"cssUrl"`
	PreviewURL      string   `json:"previewUrl"`
	BackgroundURL   string   `json:"backgroundUrl"`
	PreviewLightURL string   `json:"previewLightUrl"`
	PreviewDarkURL  string   `json:"previewDarkUrl"`
	// Definition carries the validated tokens so a client can apply a theme
	// before (or without) loading its generated stylesheet. It is null for the
	// builtin theme, whose tokens live in the bundled client stylesheet.
	Definition *ThemeDefinition `json:"definition"`
}

// themeAPIPublicCatalogDTO is the /web selectable catalog (enabled and
// available themes only).
type themeAPIPublicCatalogDTO struct {
	Themes []themeAPIPublicThemeDTO `json:"themes"`
}

// themeAPIAdminThemeDTO is the administrator view of one theme. The builtin
// theme is listed first with builtin=true, enabled=true, available=true,
// revision=0 and empty hashes/URLs.
type themeAPIAdminThemeDTO struct {
	ID              string           `json:"id"`
	Name            string           `json:"name"`
	Author          string           `json:"author"`
	Description     string           `json:"description"`
	Version         string           `json:"version"`
	Hash            string           `json:"hash"`
	Variants        []string         `json:"variants"`
	Builtin         bool             `json:"builtin"`
	Enabled         bool             `json:"enabled"`
	Available       bool             `json:"available"`
	Revision        int64            `json:"revision"`
	PackageBytes    int64            `json:"packageBytes"`
	CurrentHash     string           `json:"currentHash"`
	PreviousHash    string           `json:"previousHash"`
	Problem         string           `json:"problem"`
	CSSURL          string           `json:"cssUrl"`
	PreviewURL      string           `json:"previewUrl"`
	BackgroundURL   string           `json:"backgroundUrl"`
	PreviewLightURL string           `json:"previewLightUrl"`
	PreviewDarkURL  string           `json:"previewDarkUrl"`
	Definition      *ThemeDefinition `json:"definition"`
}

// themeAPIQuotaDTO mirrors the frozen site limits together with current usage.
type themeAPIQuotaDTO struct {
	MaxThemes  int   `json:"maxThemes"`
	UsedThemes int   `json:"usedThemes"`
	MaxBytes   int64 `json:"maxBytes"`
	UsedBytes  int64 `json:"usedBytes"`
}

// themeAPIHealthDTO reports store health. Problems are non-secret diagnostics.
type themeAPIHealthDTO struct {
	Available   bool     `json:"available"`
	Recovered   int      `json:"recovered"`
	Missing     int      `json:"missing"`
	OrphanDirs  int      `json:"orphanDirs"`
	OrphanBytes int64    `json:"orphanBytes"`
	Problems    []string `json:"problems"`
}

// themeAPIAdminCatalogDTO answers GET /admin/api/themes.
type themeAPIAdminCatalogDTO struct {
	Available bool                    `json:"available"`
	Themes    []themeAPIAdminThemeDTO `json:"themes"`
	Site      themeAPISiteDTO         `json:"site"`
	Quota     themeAPIQuotaDTO        `json:"quota"`
	Health    themeAPIHealthDTO       `json:"health"`
}

// themeAPIPreferenceDTO is one user's appearance preference.
//
// themeId is null when the user follows the site default, "builtin" when the
// builtin theme was chosen explicitly, otherwise an installed theme ID. mode
// is light, dark or system. revision is a positive JS-safe opaque integer.
type themeAPIPreferenceDTO struct {
	ThemeID  *string `json:"themeId"`
	Mode     string  `json:"mode"`
	Revision int64   `json:"revision"`
}

// themeAPISuccessDTO answers destructive operations.
type themeAPISuccessDTO struct {
	Success bool `json:"success"`
}

// themeAPIErrorDTO is the only error shape. message is a fixed string.
type themeAPIErrorDTO struct {
	Message string `json:"message"`
}

// themeAPIAppearanceInput is the strict request body of PUT /web/api/appearance.
type themeAPIAppearanceInput struct {
	ThemeID          *string
	Mode             string
	ExpectedRevision int64
}

// themeAPISiteInput is the strict request body of PUT /admin/api/theme-site.
type themeAPISiteInput struct {
	ThemeID          *string
	ExpectedRevision int64
}

// themeAPIEnabledInput is the strict request body of PATCH /admin/api/themes/{id}.
type themeAPIEnabledInput struct {
	Enabled          bool
	ExpectedRevision int64
}

// themeAPIWriteJSON writes a theme API response with nosniff.
func themeAPIWriteJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	writeJSON(w, status, payload)
}

// themeAPIError writes the fixed error body and disables caching.
func themeAPIError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Cache-Control", "no-store")
	themeAPIWriteJSON(w, status, themeAPIErrorDTO{Message: message})
}

// themeAPINoStore marks a JSON response that must never be cached.
func themeAPINoStore(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
}

// themeAPIRevalidate marks a response that may be stored but must be
// revalidated (the anonymous site default).
func themeAPIRevalidate(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-cache")
}

// themeAPIImmutable marks a content-hash resource as immutable.
func themeAPIImmutable(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
}

// themeAPIResourceURL builds an immutable URL for a validated package path.
// hash must already be a lower-case hex content hash; path comes from the
// store's registered descriptor, never from a request.
func themeAPIResourceURL(hash, path string) string {
	if hash == "" || path == "" {
		return ""
	}
	return "/web/themes/" + hash + "/" + path
}

// themeAPICSSURL is the generated stylesheet URL for a content hash.
func themeAPICSSURL(hash string) string {
	if hash == "" {
		return ""
	}
	return "/web/themes/" + hash + "/theme.css"
}

// themeAPIPreviewURL is the fixed generated preview document URL.
func themeAPIPreviewURL(hash, mode string) string {
	if hash == "" || (mode != ThemeModeLight && mode != ThemeModeDark) {
		return ""
	}
	return "/web/themes/" + hash + "/preview.html?mode=" + mode
}

// themeAPIThemeIDString maps a store selection to the explicit wire ID. A nil
// store selection means the builtin theme.
func themeAPIThemeIDString(themeID *string) string {
	if themeID == nil || *themeID == "" {
		return ThemeIDBuiltin
	}
	return *themeID
}

// themeAPIHashValid reports whether name is one of our immutable content hash
// directory names (lower-case hex sha256).
func themeAPIHashValid(name string) bool {
	if len(name) != 64 {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// themeAPIUserKey derives the preference key from the authenticated request.
// Administrators share the existing reserved legacy key; no users row is
// required. The request body never supplies this value.
func themeAPIUserKey(reqCtx *RequestContext) (string, bool) {
	if reqCtx == nil || reqCtx.ProxyUser == nil {
		return "", false
	}
	if reqCtx.ProxyUser.Role == "admin" {
		return adminVisibilityUserID, true
	}
	if reqCtx.ProxyUser.UserID == "" {
		return "", false
	}
	return reqCtx.ProxyUser.UserID, true
}

// themeAPIHeaderToken returns the token carried in a request header, if any.
// Query-parameter tokens are intentionally ignored for write APIs.
func themeAPIHeaderToken(r *http.Request) string {
	if token := r.Header.Get("X-Emby-Token"); token != "" {
		return token
	}
	for _, name := range []string{"X-Emby-Authorization", "Authorization"} {
		if header := r.Header.Get(name); header != "" {
			if token := extractTokenFromAuthHeader(header); token != "" {
				return token
			}
		}
	}
	return ""
}

// themeAPIRequireHeaderToken rejects a write that authenticated through a
// query parameter even though the request context is valid.
func themeAPIRequireHeaderToken(w http.ResponseWriter, r *http.Request) bool {
	reqCtx := requestContextFrom(r.Context())
	if reqCtx == nil || reqCtx.ProxyToken == "" {
		themeAPIError(w, http.StatusUnauthorized, themeAPIMsgAuthRequired)
		return false
	}
	token := themeAPIHeaderToken(r)
	if token == "" {
		themeAPIError(w, http.StatusUnauthorized, themeAPIMsgHeaderToken)
		return false
	}
	if token != reqCtx.ProxyToken {
		themeAPIError(w, http.StatusUnauthorized, themeAPIMsgAuthRequired)
		return false
	}
	return true
}

// themeAPIAcquireVerification enters the single package-verification slot
// without waiting.
func themeAPIAcquireVerification() bool {
	select {
	case themeAPIVerificationSlots <- struct{}{}:
		return true
	default:
		return false
	}
}

// themeAPIReleaseVerification leaves the verification slot.
func themeAPIReleaseVerification() {
	select {
	case <-themeAPIVerificationSlots:
	default:
	}
}

// themeAPIBusy writes the fixed 503 response for a busy verification slot.
func themeAPIBusy(w http.ResponseWriter) {
	w.Header().Set("Retry-After", themeAPIRetryAfterSeconds)
	themeAPIError(w, http.StatusServiceUnavailable, themeAPIMsgBusy)
}

// themeAPIReadPackageBody validates the request type and reads at most
// ThemePackageMaxBytes of raw ZIP bytes. The global 2 MiB cap still applies
// from the outside; this inner limit is the theme package budget.
func themeAPIReadPackageBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	mediaType, _, err := mime.ParseMediaType(strings.TrimSpace(r.Header.Get("Content-Type")))
	if err != nil || mediaType != themeAPIZipMediaType {
		themeAPIError(w, http.StatusUnsupportedMediaType, themeAPIMsgUnsupportedType)
		return nil, false
	}
	if r.ContentLength > ThemePackageMaxBytes {
		themeAPIError(w, http.StatusRequestEntityTooLarge, themeAPIMsgPackageTooLarge)
		return nil, false
	}
	defer r.Body.Close()
	limited := http.MaxBytesReader(w, r.Body, ThemePackageMaxBytes)
	data, err := io.ReadAll(limited)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			themeAPIError(w, http.StatusRequestEntityTooLarge, themeAPIMsgPackageTooLarge)
			return nil, false
		}
		themeAPIError(w, http.StatusBadRequest, themeAPIMsgInvalidRequest)
		return nil, false
	}
	if len(data) == 0 {
		themeAPIError(w, http.StatusBadRequest, themeAPIMsgInvalidPackage)
		return nil, false
	}
	if len(data) > ThemePackageMaxBytes {
		themeAPIError(w, http.StatusRequestEntityTooLarge, themeAPIMsgPackageTooLarge)
		return nil, false
	}
	return data, true
}

// themeAPIDecodeStrictObject parses exactly one JSON object, rejecting
// duplicate keys, non-object roots, invalid UTF-8 and trailing data.
func themeAPIDecodeStrictObject(data []byte) (map[string]json.RawMessage, error) {
	if !utf8.Valid(data) {
		return nil, errThemeAPIJSON
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil {
		return nil, errThemeAPIJSON
	}
	if delim, ok := token.(json.Delim); !ok || delim != '{' {
		return nil, errThemeAPIJSON
	}
	fields := make(map[string]json.RawMessage)
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return nil, errThemeAPIJSON
		}
		key, ok := keyToken.(string)
		if !ok {
			return nil, errThemeAPIJSON
		}
		if _, duplicate := fields[key]; duplicate {
			return nil, errThemeAPIJSON
		}
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return nil, errThemeAPIJSON
		}
		fields[key] = raw
	}
	if _, err := decoder.Token(); err != nil {
		return nil, errThemeAPIJSON
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errThemeAPIJSON
	}
	return fields, nil
}

// themeAPIBodyFieldList reports whether only allowed field names are present.
func themeAPINoUnknownFields(fields map[string]json.RawMessage, allowed []string) error {
	permitted := make(map[string]struct{}, len(allowed))
	for _, name := range allowed {
		permitted[name] = struct{}{}
	}
	for name := range fields {
		if _, ok := permitted[name]; !ok {
			return errThemeAPIJSON
		}
	}
	return nil
}

// themeAPIDecodeJSON reads a strict, size-bounded JSON object and validates its
// field set. It writes the 400/413/415 response itself.
func themeAPIDecodeJSON(w http.ResponseWriter, r *http.Request, allowed []string) (map[string]json.RawMessage, bool) {
	mediaType, _, err := mime.ParseMediaType(strings.TrimSpace(r.Header.Get("Content-Type")))
	if err != nil || mediaType != themeAPIJSONMediaType {
		themeAPIError(w, http.StatusUnsupportedMediaType, themeAPIMsgUnsupportedType)
		return nil, false
	}
	if r.ContentLength > themeAPIMaxJSONBytes {
		themeAPIError(w, http.StatusRequestEntityTooLarge, themeAPIMsgBodyTooLarge)
		return nil, false
	}
	defer r.Body.Close()
	limited := http.MaxBytesReader(w, r.Body, themeAPIMaxJSONBytes)
	data, err := io.ReadAll(limited)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			themeAPIError(w, http.StatusRequestEntityTooLarge, themeAPIMsgBodyTooLarge)
			return nil, false
		}
		themeAPIError(w, http.StatusBadRequest, themeAPIMsgInvalidRequest)
		return nil, false
	}
	if len(data) == 0 || len(data) > themeAPIMaxJSONBytes {
		if len(data) > themeAPIMaxJSONBytes {
			themeAPIError(w, http.StatusRequestEntityTooLarge, themeAPIMsgBodyTooLarge)
		} else {
			themeAPIError(w, http.StatusBadRequest, themeAPIMsgInvalidRequest)
		}
		return nil, false
	}
	fields, err := themeAPIDecodeStrictObject(data)
	if err != nil {
		themeAPIError(w, http.StatusBadRequest, themeAPIMsgInvalidRequest)
		return nil, false
	}
	if err := themeAPINoUnknownFields(fields, allowed); err != nil {
		themeAPIError(w, http.StatusBadRequest, themeAPIMsgInvalidRequest)
		return nil, false
	}
	return fields, true
}

// themeAPIField returns the raw value of a required field.
func themeAPIField(fields map[string]json.RawMessage, name string) (json.RawMessage, error) {
	raw, ok := fields[name]
	if !ok {
		return nil, errThemeAPIJSON
	}
	return raw, nil
}

// themeAPIRequiredString requires a non-null JSON string.
func themeAPIRequiredString(fields map[string]json.RawMessage, name string) (string, error) {
	raw, err := themeAPIField(fields, name)
	if err != nil {
		return "", err
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", errThemeAPIJSON
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", errThemeAPIJSON
	}
	return value, nil
}

// themeAPINullableString requires the field to be present and accepts either a
// JSON string or null. null and "" both mean "no value".
func themeAPINullableString(fields map[string]json.RawMessage, name string) (*string, error) {
	raw, err := themeAPIField(fields, name)
	if err != nil {
		return nil, err
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, nil
	}
	value, err := themeAPIRequiredString(fields, name)
	if err != nil {
		return nil, err
	}
	if value == "" {
		return nil, nil
	}
	return &value, nil
}

// themeAPIRequiredInt requires a non-null JSON integer without a fraction or
// exponent.
func themeAPIRequiredInt(fields map[string]json.RawMessage, name string) (int64, error) {
	raw, err := themeAPIField(fields, name)
	if err != nil {
		return 0, err
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return 0, errThemeAPIJSON
	}
	number, err := strconv.ParseInt(trimmed, 10, 64)
	if err != nil {
		return 0, errThemeAPIJSON
	}
	return number, nil
}

// themeAPIRequiredBool requires a non-null JSON boolean.
func themeAPIRequiredBool(fields map[string]json.RawMessage, name string) (bool, error) {
	raw, err := themeAPIField(fields, name)
	if err != nil {
		return false, err
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return false, errThemeAPIJSON
	}
	var value bool
	if err := json.Unmarshal(raw, &value); err != nil {
		return false, errThemeAPIJSON
	}
	return value, nil
}

// themeAPIRevisionInRange reports whether value can be sent to the store.
// Preference/site expectations of 0 are passed through so the store answers
// with its 409 conflict rule instead of a format error.
func themeAPIRevisionInRange(value int64) bool {
	return value >= 0 && value <= ThemeMaxRevision
}

// themeAPIExpectedRevisionFromIfMatch parses the required If-Match revision.
// Strong values only: bare digits or one quoted integer.
func themeAPIExpectedRevisionFromIfMatch(r *http.Request) (int64, bool) {
	header := strings.TrimSpace(r.Header.Get("If-Match"))
	if header == "" || strings.HasPrefix(header, "W/") {
		return 0, false
	}
	value := strings.Trim(header, "\"")
	if value == "" || strings.ContainsAny(value, "\", ") {
		return 0, false
	}
	revision, err := strconv.ParseInt(value, 10, 64)
	if err != nil || !themeAPIRevisionInRange(revision) {
		return 0, false
	}
	return revision, true
}

// themeAPIStoreStatus maps a store error to the frozen HTTP status and message.
func themeAPIStoreStatus(err error) (int, string) {
	switch {
	case errors.Is(err, ErrThemeStoreUnavailable), errors.Is(err, ErrThemeStoreClosed),
		errors.Is(err, ErrThemeCorrupt):
		return http.StatusServiceUnavailable, themeAPIMsgUnavailable
	case errors.Is(err, ErrThemeRevisionConflict):
		return http.StatusConflict, themeAPIMsgConflict
	case errors.Is(err, ErrThemeNotFound):
		return http.StatusConflict, themeAPIMsgNotFound
	case errors.Is(err, ErrThemeExists):
		return http.StatusConflict, themeAPIMsgExists
	case errors.Is(err, ErrThemeInUse):
		return http.StatusConflict, themeAPIMsgDefaultInUse
	case errors.Is(err, ErrThemeQuota):
		return http.StatusConflict, themeAPIMsgQuota
	case errors.Is(err, ErrThemeUnavailable):
		return http.StatusConflict, themeAPIMsgThemeUnavailable
	case errors.Is(err, ErrThemeBuiltin):
		return http.StatusBadRequest, themeAPIMsgBuiltinImmutable
	case errors.Is(err, ErrThemeInvalid):
		return http.StatusBadRequest, themeAPIMsgInvalidRequest
	default:
		return http.StatusServiceUnavailable, themeAPIMsgUnavailable
	}
}

// themeAPIPackageStatus maps a theme package rejection to a fixed status and
// message. Details are never echoed.
func themeAPIPackageStatus(err error) (int, string) {
	switch ThemePackageErrorCode(err) {
	case ThemeCodePackageTooLarge, ThemeCodeTooManyEntries, ThemeCodeEntryTooLarge,
		ThemeCodeUncompressedBudget, ThemeCodeJSONTooLarge, ThemeCodeImageTooLarge,
		ThemeCodeImageBudget:
		return http.StatusRequestEntityTooLarge, themeAPIMsgPackageTooLarge
	case ThemeCodeUnsupportedCompression, ThemeCodeEncryptedEntry:
		return http.StatusUnsupportedMediaType, themeAPIMsgPackageUnsupported
	default:
		return http.StatusBadRequest, themeAPIMsgInvalidPackage
	}
}
