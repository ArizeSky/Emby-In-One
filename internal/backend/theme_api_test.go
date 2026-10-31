package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// HTTP-level contract tests for the /web theme API and its public resources.
// They invoke the same handlers and route registrations the application uses,
// with a temp-dir ThemeStore and no real upstream or deployed application.

func themeAPITestRequestWithContext(r *http.Request, reqCtx *RequestContext) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), requestContextKey{}, reqCtx))
}

func themeAPITestDecodeMap(t *testing.T, rr *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode JSON response %q: %v", rr.Body.String(), err)
	}
	return payload
}

func themeAPITestMessage(t *testing.T, rr *httptest.ResponseRecorder) string {
	t.Helper()
	var payload themeAPIErrorDTO
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode error response %q: %v", rr.Body.String(), err)
	}
	return payload.Message
}

func TestWebThemeSiteNilStoreStaysAnonymousBuiltin(t *testing.T) {
	app := &App{}
	rr := httptest.NewRecorder()
	app.handleWebThemeSite(rr, httptest.NewRequest(http.MethodGet, "/web/api/theme-site", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if got := rr.Header().Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("Cache-Control = %q, want no-cache", got)
	}
	raw := themeAPITestDecodeMap(t, rr)
	if available, _ := raw["available"].(bool); available {
		t.Fatalf("nil store must report available=false: %#v", raw)
	}
	if raw["themeId"] != ThemeIDBuiltin {
		t.Fatalf("themeId = %#v, want %q", raw["themeId"], ThemeIDBuiltin)
	}
	if revision, ok := raw["revision"].(float64); !ok || revision != 0 {
		t.Fatalf("revision = %#v, want 0", raw["revision"])
	}
	theme, present := raw["theme"]
	if !present {
		t.Fatalf("theme key must always be present: %#v", raw)
	}
	if theme != nil {
		t.Fatalf("nil store theme = %#v, want null", theme)
	}
}

func TestWebThemeProtectedRoutesRequireAuth(t *testing.T) {
	app := &App{}
	cases := []struct {
		name    string
		handler http.HandlerFunc
	}{
		{name: "catalog", handler: app.requireAuth(app.handleWebThemeCatalog)},
		{name: "appearance get", handler: app.requireAuth(app.handleWebAppearanceGet)},
		{name: "appearance put", handler: app.requireAuth(app.handleWebAppearancePut)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			tc.handler(rr, httptest.NewRequest(http.MethodGet, "/web/api/", nil))
			if rr.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401 (body=%s)", rr.Code, rr.Body.String())
			}
			if got := themeAPITestMessage(t, rr); got != "Authentication required" {
				t.Fatalf("message = %q", got)
			}
		})
	}
}

func TestWebAppearanceWriteRejectsQueryTokenAndForgedHeader(t *testing.T) {
	store := newThemeStoreTestStore(t, t.TempDir(), newThemeStoreTestDB(t))
	app := &App{ThemeStore: store}
	preference, err := store.GetPreference("user-1")
	if err != nil {
		t.Fatalf("seed preference: %v", err)
	}
	body := fmt.Sprintf(`{"themeId":"builtin","mode":"dark","expectedRevision":%d}`, preference.Revision)
	reqCtx := &RequestContext{
		ProxyToken: "secret-token",
		ProxyUser:  &tokenInfo{UserID: "user-1", Role: "user"},
	}

	cases := []struct {
		name      string
		query     string
		header    string
		value     string
		wantError string
	}{
		{
			name:      "query token only",
			query:     "?api_key=secret-token",
			wantError: themeAPIMsgHeaderToken,
		},
		{
			name:      "query token plus forged X-Emby-Token",
			query:     "?api_key=secret-token",
			header:    "X-Emby-Token",
			value:     "forged-token",
			wantError: themeAPIMsgAuthRequired,
		},
		{
			name:      "forged Authorization header",
			header:    "Authorization",
			value:     `MediaBrowser Token="forged-token"`,
			wantError: themeAPIMsgAuthRequired,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPut, "/web/api/appearance"+tc.query, bytes.NewBufferString(body))
			req.Header.Set("Content-Type", themeAPIJSONMediaType)
			if tc.header != "" {
				req.Header.Set(tc.header, tc.value)
			}
			req = themeAPITestRequestWithContext(req, reqCtx)
			rr := httptest.NewRecorder()
			app.handleWebAppearancePut(rr, req)
			if rr.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401 (body=%s)", rr.Code, rr.Body.String())
			}
			if got := themeAPITestMessage(t, rr); got != tc.wantError {
				t.Fatalf("message = %q, want %q", got, tc.wantError)
			}
		})
	}

	after, err := store.GetPreference("user-1")
	if err != nil {
		t.Fatalf("read preference after rejected writes: %v", err)
	}
	if after.Revision != preference.Revision || after.Mode != preference.Mode ||
		(after.ThemeID == nil) != (preference.ThemeID == nil) {
		t.Fatalf("rejected writes changed the preference: before=%#v after=%#v", preference, after)
	}
	if after.ThemeID != nil && *after.ThemeID != *preference.ThemeID {
		t.Fatalf("rejected writes changed themeId: before=%#v after=%#v", preference, after)
	}

	// Positive control: the same context with the matching header token passes
	// the header-only gate, so the 401s above are about the token source, not a
	// permanently closed endpoint.
	req := httptest.NewRequest(http.MethodPut, "/web/api/appearance", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", themeAPIJSONMediaType)
	req.Header.Set("X-Emby-Token", "secret-token")
	req = themeAPITestRequestWithContext(req, reqCtx)
	rr := httptest.NewRecorder()
	app.handleWebAppearancePut(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("matching header token status = %d, want 200 (body=%s)", rr.Code, rr.Body.String())
	}
	var saved themeAPIPreferenceDTO
	if err := json.Unmarshal(rr.Body.Bytes(), &saved); err != nil {
		t.Fatalf("decode updated preference: %v", err)
	}
	if saved.Mode != ThemeModeDark || saved.Revision == preference.Revision {
		t.Fatalf("updated preference = %#v, want dark with a new revision", saved)
	}
}

func TestThemeResourceRouteUnknownPathsArePlain404(t *testing.T) {
	app := &App{}
	mux := http.NewServeMux()
	app.registerThemeRoutes(mux)
	paths := []string{
		"/web/themes/",
		"/web/themes/not-a-hash/theme.css",
		"/web/themes/not-a-hash/unknown.txt",
		"/web/themes/" + strings.Repeat("a", 64) + "/missing.png",
	}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
			if rr.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want 404 (body=%s)", rr.Code, rr.Body.String())
			}
			if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
				t.Fatalf("Content-Type = %q, want text/plain (SPA fallback must not match)", ct)
			}
			body := rr.Body.String()
			if strings.Contains(strings.ToLower(body), "<html") || strings.Contains(strings.ToLower(body), "<!doctype") {
				t.Fatalf("theme resource 404 returned HTML/SPA body: %q", body)
			}
		})
	}
}

func TestThemePreviewDocumentHasScriptFreeCSP(t *testing.T) {
	store := newThemeStoreTestStore(t, t.TempDir(), newThemeStoreTestDB(t))
	entry := installThemeStoreTestPackage(t, store, "preview-csp-theme", 8)
	app := &App{ThemeStore: store}

	req := httptest.NewRequest(http.MethodGet, "/web/themes/"+entry.CurrentHash+"/preview.html?mode=light", nil)
	req.SetPathValue("hash", entry.CurrentHash)
	rr := httptest.NewRecorder()
	app.handleThemePreviewDocument(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("preview status = %d, want 200 (body=%s)", rr.Code, rr.Body.String())
	}
	csp := rr.Header().Get("Content-Security-Policy")
	if csp != themePreviewCSP {
		t.Fatalf("CSP = %q, want %q", csp, themePreviewCSP)
	}
	if !strings.Contains(csp, "script-src 'none'") || !strings.Contains(csp, "default-src 'none'") {
		t.Fatalf("CSP must forbid scripts: %q", csp)
	}
	if got := rr.Header().Get("X-Frame-Options"); got != "SAMEORIGIN" {
		t.Fatalf("X-Frame-Options = %q", got)
	}
	body := rr.Body.String()
	if strings.Contains(strings.ToLower(body), "<script") || strings.Contains(strings.ToLower(body), "onload=") {
		t.Fatalf("preview document must not contain scripts: %q", body)
	}
	if !strings.Contains(body, themeAPICSSURL(entry.CurrentHash)) {
		t.Fatalf("preview document must reference the generated stylesheet: %q", body)
	}

	badMode := httptest.NewRequest(http.MethodGet, "/web/themes/"+entry.CurrentHash+"/preview.html?mode=blue", nil)
	badMode.SetPathValue("hash", entry.CurrentHash)
	rr = httptest.NewRecorder()
	app.handleThemePreviewDocument(rr, badMode)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("invalid preview mode status = %d, want 400 (body=%s)", rr.Code, rr.Body.String())
	}
}

func TestWebThemeSiteCarriesOnlySelectedCustomTheme(t *testing.T) {
	store := newThemeStoreTestStore(t, t.TempDir(), newThemeStoreTestDB(t))
	siteEntry := installThemeStoreTestPackage(t, store, "site-default-theme", 8)
	otherEntry := installThemeStoreTestPackage(t, store, "unrelated-theme", 9)
	site, err := store.Site()
	if err != nil {
		t.Fatalf("Site: %v", err)
	}
	site, err = store.SetSiteDefault(&siteEntry.ID, site.Revision)
	if err != nil {
		t.Fatalf("SetSiteDefault: %v", err)
	}

	app := &App{ThemeStore: store}
	rr := httptest.NewRecorder()
	app.handleWebThemeSite(rr, httptest.NewRequest(http.MethodGet, "/web/api/theme-site", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var payload themeAPISiteDTO
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode site payload %q: %v", rr.Body.String(), err)
	}
	if !payload.Available || payload.ThemeID != siteEntry.ID || payload.Revision != site.Revision {
		t.Fatalf("site payload = %#v", payload)
	}
	if payload.Theme == nil {
		t.Fatalf("custom site default must carry theme: %s", rr.Body.String())
	}
	if payload.Theme.ID != siteEntry.ID || payload.Theme.Hash != siteEntry.CurrentHash || payload.Theme.Builtin {
		t.Fatalf("site theme identity = %#v", payload.Theme)
	}
	if payload.Theme.CSSURL != themeAPICSSURL(siteEntry.CurrentHash) {
		t.Fatalf("site theme cssUrl = %q", payload.Theme.CSSURL)
	}
	if payload.Theme.Definition == nil {
		t.Fatalf("site theme must carry its definition: %s", rr.Body.String())
	}
	if _, ok := payload.Theme.Definition.Variants[ThemeVariantLight]; !ok {
		t.Fatalf("site theme definition is missing the light variant: %#v", payload.Theme.Definition)
	}
	if _, ok := payload.Theme.Definition.Variants[ThemeVariantDark]; !ok {
		t.Fatalf("site theme definition is missing the dark variant: %#v", payload.Theme.Definition)
	}
	if _, ok := themeAPITestDecodeMap(t, rr)["themes"]; ok {
		t.Fatalf("anonymous site response must not expose a theme list: %s", rr.Body.String())
	}
	body := rr.Body.String()
	if strings.Contains(body, otherEntry.ID) || strings.Contains(body, otherEntry.CurrentHash) {
		t.Fatalf("anonymous site response leaked an unrelated theme: %s", body)
	}

	// The DTO definition must be a deep copy: mutating the decoded value cannot
	// change what the store returns on the next read.
	payload.Theme.Definition.Variants[ThemeVariantLight] = ThemeVariant{Accent: "#000000"}
	stored, err := store.Definition(siteEntry.CurrentHash)
	if err != nil {
		t.Fatalf("store.Definition: %v", err)
	}
	if stored.Variants[ThemeVariantLight].Accent == "#000000" {
		t.Fatal("site DTO definition aliases store state")
	}

	// The builtin default carries theme=null, and the key stays present.
	builtinSite, err := store.SetSiteDefault(nil, site.Revision)
	if err != nil {
		t.Fatalf("reset site default: %v", err)
	}
	rr = httptest.NewRecorder()
	app.handleWebThemeSite(rr, httptest.NewRequest(http.MethodGet, "/web/api/theme-site", nil))
	var builtinPayload themeAPISiteDTO
	if err := json.Unmarshal(rr.Body.Bytes(), &builtinPayload); err != nil {
		t.Fatalf("decode builtin site payload: %v", err)
	}
	if builtinPayload.ThemeID != ThemeIDBuiltin || builtinPayload.Revision != builtinSite.Revision {
		t.Fatalf("builtin site payload = %#v", builtinPayload)
	}
	if builtinPayload.Theme != nil {
		t.Fatalf("builtin site theme = %#v, want null", builtinPayload.Theme)
	}
	raw := themeAPITestDecodeMap(t, rr)
	theme, present := raw["theme"]
	if !present || theme != nil {
		t.Fatalf("builtin site response theme = %#v (present=%v), want null", theme, present)
	}
}

func TestWebThemeCatalogAlwaysEmitsDefinitionField(t *testing.T) {
	store := newThemeStoreTestStore(t, t.TempDir(), newThemeStoreTestDB(t))
	entry := installThemeStoreTestPackage(t, store, "catalog-definition-theme", 8)
	app := &App{ThemeStore: store}
	handler := app.requireAuth(app.handleWebThemeCatalog)

	req := themeAPITestRequestWithContext(
		httptest.NewRequest(http.MethodGet, "/web/api/themes", nil),
		&RequestContext{ProxyUser: &tokenInfo{UserID: "user-1", Role: "user"}},
	)
	rr := httptest.NewRecorder()
	handler(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("catalog status = %d, want 200 (body=%s)", rr.Code, rr.Body.String())
	}
	var payload themeAPIPublicCatalogDTO
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode catalog: %v", err)
	}
	if len(payload.Themes) != 2 {
		t.Fatalf("catalog has %d themes, want builtin + custom: %#v", len(payload.Themes), payload.Themes)
	}
	if !payload.Themes[0].Builtin || payload.Themes[0].ID != ThemeIDBuiltin {
		t.Fatalf("first catalog theme = %#v, want builtin", payload.Themes[0])
	}
	if payload.Themes[0].Definition != nil {
		t.Fatalf("builtin definition = %#v, want null", payload.Themes[0].Definition)
	}
	custom := payload.Themes[1]
	if custom.ID != entry.ID || custom.Definition == nil {
		t.Fatalf("custom catalog theme = %#v", custom)
	}
	body := rr.Body.String()
	if !strings.Contains(body, `"definition":null`) {
		t.Fatalf("builtin entry must serialize definition:null: %s", body)
	}
	if !strings.Contains(body, `"definition":{`) {
		t.Fatalf("custom entry must serialize its definition: %s", body)
	}
}
