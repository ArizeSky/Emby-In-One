package backend

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// HTTP-level contract tests for the administrator theme API. They call the
// production handlers inside requireAdmin with a hand-built RequestContext, a
// temp-dir ThemeStore and the existing ZIP/PNG fixtures. No Auth, UserStore,
// upstream, deployed application or network is involved. State changes are
// always re-read from the store after the response, so a fake success cannot
// pass. Tests are intentionally not parallel because the package verification
// slot is process-wide.

const themeAdminTestToken = "theme-admin-header-token"

type themeAdminTestHarness struct {
	t     *testing.T
	db    *sqliteDB
	store *ThemeStore
	app   *App
}

func newThemeAdminTestHarness(t *testing.T) *themeAdminTestHarness {
	t.Helper()
	db := newThemeStoreTestDB(t)
	store := newThemeStoreTestStore(t, t.TempDir(), db)
	return &themeAdminTestHarness{
		t:     t,
		db:    db,
		store: store,
		app:   &App{ThemeStore: store},
	}
}

func themeAdminTestAdminContext() *RequestContext {
	return &RequestContext{
		ProxyToken: themeAdminTestToken,
		ProxyUser:  &tokenInfo{UserID: "admin-user", Username: "admin", Role: "admin"},
	}
}

// request builds an admin request with a matching header token and context.
// Tests that must exercise a rejected token source delete or replace the header
// afterwards.
func (h *themeAdminTestHarness) request(method, target string, body []byte, contentType string) *http.Request {
	h.t.Helper()
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req := httptest.NewRequest(method, target, reader)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("X-Emby-Token", themeAdminTestToken)
	return themeAPITestRequestWithContext(req, themeAdminTestAdminContext())
}

// serve runs one admin handler through the production requireAdmin gate.
func (h *themeAdminTestHarness) serve(handler http.HandlerFunc, req *http.Request) *httptest.ResponseRecorder {
	h.t.Helper()
	rr := httptest.NewRecorder()
	h.app.requireAdmin(handler)(rr, req)
	return rr
}

func (h *themeAdminTestHarness) packageZip(themeID string, backgroundSize int) []byte {
	h.t.Helper()
	return themeTestBuildZip(h.t,
		themeTestZipEntry{Name: "manifest.json", Data: []byte(themeTestManifestJSON(themeID))},
		themeTestZipEntry{Name: "theme.json", Data: []byte(themeTestThemeJSON("assets/background.png"))},
		themeTestZipEntry{Name: "preview.png", Data: themeTestPNGBytes(h.t, 8, 8)},
		themeTestZipEntry{Name: "assets/background.png", Data: themeTestPNGBytes(h.t, backgroundSize, backgroundSize)},
	)
}

func (h *themeAdminTestHarness) install(themeID string, backgroundSize int) *httptest.ResponseRecorder {
	h.t.Helper()
	req := h.request(http.MethodPost, "/admin/api/themes", h.packageZip(themeID, backgroundSize), themeAPIZipMediaType)
	return h.serve(h.app.handleAdminThemeInstall, req)
}

func (h *themeAdminTestHarness) update(themeID string, backgroundSize int, ifMatch string) *httptest.ResponseRecorder {
	h.t.Helper()
	req := h.request(http.MethodPut, "/admin/api/themes/"+themeID+"/package",
		h.packageZip(themeID, backgroundSize), themeAPIZipMediaType)
	// serve calls the handler directly, so mirror the ServeMux wildcard value.
	req.SetPathValue("id", themeID)
	req.Header.Set("If-Match", ifMatch)
	return h.serve(h.app.handleAdminThemeUpdate, req)
}

func (h *themeAdminTestHarness) patch(themeID, body string, contentType string) *httptest.ResponseRecorder {
	h.t.Helper()
	req := h.request(http.MethodPatch, "/admin/api/themes/"+themeID, []byte(body), contentType)
	// serve calls the handler directly, so mirror the ServeMux wildcard value.
	req.SetPathValue("id", themeID)
	return h.serve(h.app.handleAdminThemePatch, req)
}

func (h *themeAdminTestHarness) delete(themeID string, ifMatch string) *httptest.ResponseRecorder {
	h.t.Helper()
	req := h.request(http.MethodDelete, "/admin/api/themes/"+themeID, nil, "")
	// serve calls the handler directly, so mirror the ServeMux wildcard value.
	req.SetPathValue("id", themeID)
	req.Header.Set("If-Match", ifMatch)
	return h.serve(h.app.handleAdminThemeDelete, req)
}

func (h *themeAdminTestHarness) sitePut(body string, contentType string) *httptest.ResponseRecorder {
	h.t.Helper()
	req := h.request(http.MethodPut, "/admin/api/theme-site", []byte(body), contentType)
	return h.serve(h.app.handleAdminThemeSitePut, req)
}

func (h *themeAdminTestHarness) catalog() ThemeCatalog {
	h.t.Helper()
	catalog, err := h.store.Catalog()
	if err != nil {
		h.t.Fatalf("store.Catalog: %v", err)
	}
	return catalog
}

func (h *themeAdminTestHarness) catalogEntry(themeID string) ThemeCatalogEntry {
	h.t.Helper()
	for _, entry := range h.catalog().Themes {
		if entry.ID == themeID {
			return entry
		}
	}
	h.t.Fatalf("theme %q is not in the store catalog", themeID)
	return ThemeCatalogEntry{}
}

func (h *themeAdminTestHarness) site() ThemeSiteSettings {
	h.t.Helper()
	site, err := h.store.Site()
	if err != nil {
		h.t.Fatalf("store.Site: %v", err)
	}
	return site
}

func themeAdminTestDecodeAdmin(t *testing.T, rr *httptest.ResponseRecorder) themeAPIAdminThemeDTO {
	t.Helper()
	var payload themeAPIAdminThemeDTO
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode admin theme %q: %v", rr.Body.String(), err)
	}
	return payload
}

func themeAdminTestDecodeCatalog(t *testing.T, rr *httptest.ResponseRecorder) themeAPIAdminCatalogDTO {
	t.Helper()
	var payload themeAPIAdminCatalogDTO
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode admin catalog %q: %v", rr.Body.String(), err)
	}
	return payload
}

func themeAdminTestDecodeSite(t *testing.T, rr *httptest.ResponseRecorder) themeAPISiteDTO {
	t.Helper()
	var payload themeAPISiteDTO
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode site %q: %v", rr.Body.String(), err)
	}
	return payload
}

func themeAdminTestDecodeSuccess(t *testing.T, rr *httptest.ResponseRecorder) themeAPISuccessDTO {
	t.Helper()
	var payload themeAPISuccessDTO
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode success %q: %v", rr.Body.String(), err)
	}
	return payload
}

func themeAdminTestRequireStatus(t *testing.T, rr *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rr.Code != want {
		t.Fatalf("status = %d, want %d (body=%s)", rr.Code, want, rr.Body.String())
	}
}

func themeAdminTestRequireMessage(t *testing.T, rr *httptest.ResponseRecorder, want string) {
	t.Helper()
	if got := themeAPITestMessage(t, rr); got != want {
		t.Fatalf("message = %q, want %q (body=%s)", got, want, rr.Body.String())
	}
}

// themeAdminTestRequireSlotFree asserts the process-wide verification slot is
// not leaked after a request, whether it succeeded or was rejected.
func themeAdminTestRequireSlotFree(t *testing.T) {
	t.Helper()
	if held := len(themeAPIVerificationSlots); held != 0 {
		t.Fatalf("verification slot leaked after request: %d held", held)
	}
}

func TestAdminThemeInstallUpdateCatalogContract(t *testing.T) {
	h := newThemeAdminTestHarness(t)
	themeAdminTestRequireSlotFree(t)

	rr := h.install("contract-theme", 8)
	themeAdminTestRequireStatus(t, rr, http.StatusCreated)
	installed := themeAdminTestDecodeAdmin(t, rr)
	if installed.ID != "contract-theme" || installed.Builtin || !installed.Enabled || !installed.Available {
		t.Fatalf("installed admin theme = %#v", installed)
	}
	if installed.Revision <= 0 || installed.CurrentHash == "" || installed.Hash != installed.CurrentHash {
		t.Fatalf("installed identity = %#v", installed)
	}
	if installed.Definition == nil || len(installed.Definition.Variants) == 0 {
		t.Fatalf("installed definition = %#v", installed.Definition)
	}
	themeAdminTestRequireSlotFree(t)

	catalog := h.catalog()
	if len(catalog.Themes) != 2 {
		t.Fatalf("store catalog has %d themes, want builtin + custom: %#v", len(catalog.Themes), catalog.Themes)
	}
	if builtin := catalog.Themes[0]; !builtin.Builtin || builtin.ID != ThemeIDBuiltin || builtin.Revision != 0 {
		t.Fatalf("store catalog first entry = %#v, want builtin", builtin)
	}
	stored := catalog.Themes[1]
	if stored.ID != installed.ID || stored.CurrentHash != installed.CurrentHash || stored.Revision != installed.Revision {
		t.Fatalf("stored entry = %#v, response = %#v", stored, installed)
	}

	rr = h.serve(h.app.handleAdminThemeList, h.request(http.MethodGet, "/admin/api/themes", nil, ""))
	themeAdminTestRequireStatus(t, rr, http.StatusOK)
	adminCatalog := themeAdminTestDecodeCatalog(t, rr)
	if !adminCatalog.Available || len(adminCatalog.Themes) != 2 {
		t.Fatalf("admin catalog = %#v", adminCatalog)
	}
	if first := adminCatalog.Themes[0]; !first.Builtin || first.ID != ThemeIDBuiltin {
		t.Fatalf("admin catalog first entry = %#v, want builtin", first)
	}
	if adminCatalog.Site.ThemeID != ThemeIDBuiltin || adminCatalog.Quota.UsedThemes != 1 || !adminCatalog.Health.Available {
		t.Fatalf("admin catalog metadata = %#v", adminCatalog)
	}

	oldHash := stored.CurrentHash
	oldRevision := stored.Revision

	// A stale If-Match is a 409 and must not publish the new package.
	rr = h.update("contract-theme", 9, strconv.FormatInt(oldRevision+1, 10))
	themeAdminTestRequireStatus(t, rr, http.StatusConflict)
	themeAdminTestRequireMessage(t, rr, themeAPIMsgConflict)
	if after := h.catalogEntry("contract-theme"); after.CurrentHash != oldHash || after.Revision != oldRevision {
		t.Fatalf("stale update changed the store: %#v", after)
	}
	themeAdminTestRequireSlotFree(t)

	// The package manifest ID must match the target path. The body names
	// other-theme while the path targets contract-theme, and the If-Match is the
	// current revision, so only the mismatch check can reject the publish.
	req := h.request(http.MethodPut, "/admin/api/themes/contract-theme/package",
		h.packageZip("other-theme", 9), themeAPIZipMediaType)
	req.SetPathValue("id", "contract-theme")
	req.Header.Set("If-Match", strconv.FormatInt(oldRevision, 10))
	rr = h.serve(h.app.handleAdminThemeUpdate, req)
	themeAdminTestRequireStatus(t, rr, http.StatusBadRequest)
	themeAdminTestRequireMessage(t, rr, themeAPIMsgTargetMismatch)
	if after := h.catalogEntry("contract-theme"); after.CurrentHash != oldHash || after.Revision != oldRevision {
		t.Fatalf("mismatched update changed the store: %#v", after)
	}
	themeAdminTestRequireSlotFree(t)

	// If-Match is mandatory.
	rr = h.update("contract-theme", 9, "")
	themeAdminTestRequireStatus(t, rr, http.StatusBadRequest)
	themeAdminTestRequireMessage(t, rr, themeAPIMsgMissingRevision)
	themeAdminTestRequireSlotFree(t)

	// The correct revision replaces the package and keeps the old hash as the
	// recovery version.
	rr = h.update("contract-theme", 9, strconv.FormatInt(oldRevision, 10))
	themeAdminTestRequireStatus(t, rr, http.StatusOK)
	updated := themeAdminTestDecodeAdmin(t, rr)
	if updated.ID != "contract-theme" || updated.CurrentHash == "" || updated.CurrentHash == oldHash {
		t.Fatalf("updated admin theme = %#v (old hash %q)", updated, oldHash)
	}
	if updated.Revision == oldRevision || updated.PreviousHash != oldHash {
		t.Fatalf("updated revision/previous = %#v", updated)
	}
	stored = h.catalogEntry("contract-theme")
	if stored.CurrentHash != updated.CurrentHash || stored.Revision != updated.Revision ||
		stored.PreviousHash != oldHash {
		t.Fatalf("store after update = %#v, response = %#v", stored, updated)
	}
	themeAdminTestRequireSlotFree(t)

	// The published content hash serves its immutable stylesheet.
	mux := http.NewServeMux()
	h.app.registerThemeRoutes(mux)
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, themeAPICSSURL(updated.CurrentHash), nil))
	themeAdminTestRequireStatus(t, rr, http.StatusOK)
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/css") {
		t.Fatalf("updated stylesheet Content-Type = %q, want text/css", ct)
	}
}

func TestAdminThemePatchStrictJSONAndEnabledState(t *testing.T) {
	h := newThemeAdminTestHarness(t)
	entry := installThemeStoreTestPackage(t, h.store, "patch-theme", 8)
	revision := entry.Revision

	oversized := bytes.Repeat([]byte(" "), themeAPIMaxJSONBytes+1)
	cases := []struct {
		name        string
		body        []byte
		contentType string
		wantStatus  int
		wantMessage string
	}{
		{
			name:        "unknown field",
			body:        []byte(fmt.Sprintf(`{"enabled":false,"expectedRevision":%d,"extra":true}`, revision)),
			contentType: themeAPIJSONMediaType,
			wantStatus:  http.StatusBadRequest,
			wantMessage: themeAPIMsgInvalidRequest,
		},
		{
			name:        "duplicate key",
			body:        []byte(fmt.Sprintf(`{"enabled":false,"enabled":true,"expectedRevision":%d}`, revision)),
			contentType: themeAPIJSONMediaType,
			wantStatus:  http.StatusBadRequest,
			wantMessage: themeAPIMsgInvalidRequest,
		},
		{
			name:        "trailing data",
			body:        []byte(fmt.Sprintf(`{"enabled":false,"expectedRevision":%d}{}`, revision)),
			contentType: themeAPIJSONMediaType,
			wantStatus:  http.StatusBadRequest,
			wantMessage: themeAPIMsgInvalidRequest,
		},
		{
			name:        "missing enabled",
			body:        []byte(fmt.Sprintf(`{"expectedRevision":%d}`, revision)),
			contentType: themeAPIJSONMediaType,
			wantStatus:  http.StatusBadRequest,
			wantMessage: themeAPIMsgInvalidRequest,
		},
		{
			name:        "missing expectedRevision",
			body:        []byte(`{"enabled":false}`),
			contentType: themeAPIJSONMediaType,
			wantStatus:  http.StatusBadRequest,
			wantMessage: themeAPIMsgInvalidRequest,
		},
		{
			name:        "wrong enabled type",
			body:        []byte(fmt.Sprintf(`{"enabled":"false","expectedRevision":%d}`, revision)),
			contentType: themeAPIJSONMediaType,
			wantStatus:  http.StatusBadRequest,
			wantMessage: themeAPIMsgInvalidRequest,
		},
		{
			name:        "null enabled",
			body:        []byte(fmt.Sprintf(`{"enabled":null,"expectedRevision":%d}`, revision)),
			contentType: themeAPIJSONMediaType,
			wantStatus:  http.StatusBadRequest,
			wantMessage: themeAPIMsgInvalidRequest,
		},
		{
			name:        "wrong revision type",
			body:        []byte(`{"enabled":false,"expectedRevision":"1"}`),
			contentType: themeAPIJSONMediaType,
			wantStatus:  http.StatusBadRequest,
			wantMessage: themeAPIMsgInvalidRequest,
		},
		{
			name:        "negative revision",
			body:        []byte(`{"enabled":false,"expectedRevision":-1}`),
			contentType: themeAPIJSONMediaType,
			wantStatus:  http.StatusBadRequest,
			wantMessage: themeAPIMsgInvalidRequest,
		},
		{
			name:        "array root",
			body:        []byte(`[]`),
			contentType: themeAPIJSONMediaType,
			wantStatus:  http.StatusBadRequest,
			wantMessage: themeAPIMsgInvalidRequest,
		},
		{
			name:        "empty body",
			body:        []byte{},
			contentType: themeAPIJSONMediaType,
			wantStatus:  http.StatusBadRequest,
			wantMessage: themeAPIMsgInvalidRequest,
		},
		{
			name:        "wrong content type",
			body:        []byte(fmt.Sprintf(`{"enabled":false,"expectedRevision":%d}`, revision)),
			contentType: "text/plain",
			wantStatus:  http.StatusUnsupportedMediaType,
			wantMessage: themeAPIMsgUnsupportedType,
		},
		{
			name:        "oversized JSON",
			body:        oversized,
			contentType: themeAPIJSONMediaType,
			wantStatus:  http.StatusRequestEntityTooLarge,
			wantMessage: themeAPIMsgBodyTooLarge,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rr := h.patch("patch-theme", string(tc.body), tc.contentType)
			themeAdminTestRequireStatus(t, rr, tc.wantStatus)
			themeAdminTestRequireMessage(t, rr, tc.wantMessage)
			stored := h.catalogEntry("patch-theme")
			if stored.Revision != revision || !stored.Enabled {
				t.Fatalf("rejected PATCH changed the store: %#v", stored)
			}
		})
	}

	rr := h.patch("patch-theme", fmt.Sprintf(`{"enabled":false,"expectedRevision":%d}`, revision), themeAPIJSONMediaType)
	themeAdminTestRequireStatus(t, rr, http.StatusOK)
	disabled := themeAdminTestDecodeAdmin(t, rr)
	if disabled.ID != "patch-theme" || disabled.Enabled || disabled.Revision == revision {
		t.Fatalf("disabled admin theme = %#v", disabled)
	}
	stored := h.catalogEntry("patch-theme")
	if stored.Enabled || stored.Revision != disabled.Revision {
		t.Fatalf("store after disable = %#v, response = %#v", stored, disabled)
	}
	public, err := h.store.PublicCatalog()
	if err != nil {
		t.Fatalf("PublicCatalog: %v", err)
	}
	for _, publicEntry := range public.Themes {
		if publicEntry.ID == "patch-theme" {
			t.Fatalf("disabled theme is still in the public catalog: %#v", public)
		}
	}

	rr = h.patch("patch-theme", fmt.Sprintf(`{"enabled":true,"expectedRevision":%d}`, disabled.Revision), themeAPIJSONMediaType)
	themeAdminTestRequireStatus(t, rr, http.StatusOK)
	enabled := themeAdminTestDecodeAdmin(t, rr)
	if !enabled.Enabled || enabled.Revision == disabled.Revision {
		t.Fatalf("re-enabled admin theme = %#v", enabled)
	}
	if stored := h.catalogEntry("patch-theme"); !stored.Enabled || stored.Revision != enabled.Revision {
		t.Fatalf("store after enable = %#v", stored)
	}

	// The previous revision is now stale.
	rr = h.patch("patch-theme", fmt.Sprintf(`{"enabled":false,"expectedRevision":%d}`, disabled.Revision), themeAPIJSONMediaType)
	themeAdminTestRequireStatus(t, rr, http.StatusConflict)
	themeAdminTestRequireMessage(t, rr, themeAPIMsgConflict)
	if stored := h.catalogEntry("patch-theme"); !stored.Enabled || stored.Revision != enabled.Revision {
		t.Fatalf("conflicting PATCH changed the store: %#v", stored)
	}
}

func TestAdminThemeSiteDefaultBlocksDisableAndDelete(t *testing.T) {
	h := newThemeAdminTestHarness(t)
	entry := installThemeStoreTestPackage(t, h.store, "site-default-theme", 8)
	themeAdminTestRequireSlotFree(t)

	site := h.site()
	rr := h.sitePut(fmt.Sprintf(`{"themeId":%q,"expectedRevision":%d}`, entry.ID, site.Revision), themeAPIJSONMediaType)
	themeAdminTestRequireStatus(t, rr, http.StatusOK)
	selected := themeAdminTestDecodeSite(t, rr)
	if selected.ThemeID != entry.ID || selected.Revision == site.Revision {
		t.Fatalf("selected site = %#v", selected)
	}
	if stored := h.site(); stored.ThemeID == nil || *stored.ThemeID != entry.ID || stored.Revision != selected.Revision {
		t.Fatalf("store site = %#v, response = %#v", stored, selected)
	}

	// Strict JSON schema for the site PUT, checked against the unchanged site.
	oversized := strings.Repeat(" ", themeAPIMaxJSONBytes+1)
	siteJSON := []struct {
		name        string
		body        string
		contentType string
		wantStatus  int
		wantMessage string
	}{
		{
			name:        "unknown field",
			body:        fmt.Sprintf(`{"themeId":%q,"expectedRevision":%d,"extra":true}`, entry.ID, selected.Revision),
			contentType: themeAPIJSONMediaType,
			wantStatus:  http.StatusBadRequest,
			wantMessage: themeAPIMsgInvalidRequest,
		},
		{
			name:        "duplicate key",
			body:        fmt.Sprintf(`{"themeId":%q,"themeId":"builtin","expectedRevision":%d}`, entry.ID, selected.Revision),
			contentType: themeAPIJSONMediaType,
			wantStatus:  http.StatusBadRequest,
			wantMessage: themeAPIMsgInvalidRequest,
		},
		{
			name:        "trailing data",
			body:        fmt.Sprintf(`{"themeId":%q,"expectedRevision":%d}null`, entry.ID, selected.Revision),
			contentType: themeAPIJSONMediaType,
			wantStatus:  http.StatusBadRequest,
			wantMessage: themeAPIMsgInvalidRequest,
		},
		{
			name:        "missing themeId",
			body:        fmt.Sprintf(`{"expectedRevision":%d}`, selected.Revision),
			contentType: themeAPIJSONMediaType,
			wantStatus:  http.StatusBadRequest,
			wantMessage: themeAPIMsgInvalidRequest,
		},
		{
			name:        "wrong themeId type",
			body:        fmt.Sprintf(`{"themeId":5,"expectedRevision":%d}`, selected.Revision),
			contentType: themeAPIJSONMediaType,
			wantStatus:  http.StatusBadRequest,
			wantMessage: themeAPIMsgInvalidRequest,
		},
		{
			name:        "missing expectedRevision",
			body:        fmt.Sprintf(`{"themeId":%q}`, entry.ID),
			contentType: themeAPIJSONMediaType,
			wantStatus:  http.StatusBadRequest,
			wantMessage: themeAPIMsgInvalidRequest,
		},
		{
			name:        "wrong revision type",
			body:        fmt.Sprintf(`{"themeId":%q,"expectedRevision":"1"}`, entry.ID),
			contentType: themeAPIJSONMediaType,
			wantStatus:  http.StatusBadRequest,
			wantMessage: themeAPIMsgInvalidRequest,
		},
		{
			name:        "empty body",
			body:        "",
			contentType: themeAPIJSONMediaType,
			wantStatus:  http.StatusBadRequest,
			wantMessage: themeAPIMsgInvalidRequest,
		},
		{
			name:        "wrong content type",
			body:        fmt.Sprintf(`{"themeId":%q,"expectedRevision":%d}`, entry.ID, selected.Revision),
			contentType: "text/plain",
			wantStatus:  http.StatusUnsupportedMediaType,
			wantMessage: themeAPIMsgUnsupportedType,
		},
		{
			name:        "oversized JSON",
			body:        oversized,
			contentType: themeAPIJSONMediaType,
			wantStatus:  http.StatusRequestEntityTooLarge,
			wantMessage: themeAPIMsgBodyTooLarge,
		},
	}
	for _, tc := range siteJSON {
		t.Run(tc.name, func(t *testing.T) {
			rr := h.sitePut(tc.body, tc.contentType)
			themeAdminTestRequireStatus(t, rr, tc.wantStatus)
			themeAdminTestRequireMessage(t, rr, tc.wantMessage)
			stored := h.site()
			if stored.ThemeID == nil || *stored.ThemeID != entry.ID || stored.Revision != selected.Revision {
				t.Fatalf("rejected site PUT changed the store: %#v", stored)
			}
		})
	}

	// The site default cannot be disabled or deleted.
	rr = h.patch(entry.ID, fmt.Sprintf(`{"enabled":false,"expectedRevision":%d}`, entry.Revision), themeAPIJSONMediaType)
	themeAdminTestRequireStatus(t, rr, http.StatusConflict)
	themeAdminTestRequireMessage(t, rr, themeAPIMsgDefaultInUse)
	if stored := h.catalogEntry(entry.ID); !stored.Enabled || stored.Revision != entry.Revision {
		t.Fatalf("PATCH of the site default changed the store: %#v", stored)
	}

	rr = h.delete(entry.ID, strconv.FormatInt(entry.Revision, 10))
	themeAdminTestRequireStatus(t, rr, http.StatusConflict)
	themeAdminTestRequireMessage(t, rr, themeAPIMsgDefaultInUse)
	if stored := h.catalogEntry(entry.ID); stored.Revision != entry.Revision {
		t.Fatalf("DELETE of the site default changed the store: %#v", stored)
	}

	// The builtin theme is immutable regardless of revisions.
	rr = h.patch(ThemeIDBuiltin, `{"enabled":false,"expectedRevision":0}`, themeAPIJSONMediaType)
	themeAdminTestRequireStatus(t, rr, http.StatusBadRequest)
	themeAdminTestRequireMessage(t, rr, themeAPIMsgBuiltinImmutable)
	rr = h.delete(ThemeIDBuiltin, "0")
	themeAdminTestRequireStatus(t, rr, http.StatusBadRequest)
	themeAdminTestRequireMessage(t, rr, themeAPIMsgBuiltinImmutable)

	// Switching back to builtin releases the reference.
	site = h.site()
	rr = h.sitePut(fmt.Sprintf(`{"themeId":%q,"expectedRevision":%d}`, ThemeIDBuiltin, site.Revision), themeAPIJSONMediaType)
	themeAdminTestRequireStatus(t, rr, http.StatusOK)
	if reset := themeAdminTestDecodeSite(t, rr); reset.ThemeID != ThemeIDBuiltin {
		t.Fatalf("reset site = %#v", reset)
	}
	if stored := h.site(); stored.ThemeID != nil {
		t.Fatalf("store site after reset = %#v, want builtin", stored)
	}

	// A personal preference referencing the theme is reset to the explicit
	// builtin ID in the same delete transaction.
	seed, err := h.store.GetPreference("user-1")
	if err != nil {
		t.Fatalf("GetPreference seed: %v", err)
	}
	if seed.Revision <= 0 {
		t.Fatalf("seeded preference revision = %d, want positive", seed.Revision)
	}
	preference, err := h.store.SavePreference("user-1", &entry.ID, ThemeModeDark, seed.Revision)
	if err != nil {
		t.Fatalf("SavePreference: %v", err)
	}
	if preference.ThemeID == nil || *preference.ThemeID != entry.ID {
		t.Fatalf("seeded preference = %#v", preference)
	}

	rr = h.delete(entry.ID, strconv.FormatInt(entry.Revision, 10))
	themeAdminTestRequireStatus(t, rr, http.StatusOK)
	if !themeAdminTestDecodeSuccess(t, rr).Success {
		t.Fatalf("delete response = %s", rr.Body.String())
	}
	catalog := h.catalog()
	if len(catalog.Themes) != 1 || !catalog.Themes[0].Builtin || catalog.Quota.UsedThemes != 0 {
		t.Fatalf("catalog after delete = %#v", catalog)
	}
	resetPreference, err := h.store.GetPreference("user-1")
	if err != nil {
		t.Fatalf("GetPreference after delete: %v", err)
	}
	if resetPreference.ThemeID == nil || *resetPreference.ThemeID != ThemeIDBuiltin ||
		resetPreference.Revision == preference.Revision {
		t.Fatalf("preference after delete = %#v", resetPreference)
	}
	if stored := themeStoreTestStoredPreferenceThemeID(t, h.db, "user-1"); stored != ThemeIDBuiltin {
		t.Fatalf("stored preference theme_id = %q, want %q", stored, ThemeIDBuiltin)
	}

	// The deleted content hash no longer serves any resource and never falls
	// back to the SPA HTML.
	mux := http.NewServeMux()
	h.app.registerThemeRoutes(mux)
	for _, path := range []string{
		"/web/themes/" + entry.CurrentHash + "/theme.css",
		"/web/themes/" + entry.CurrentHash + "/preview.html?mode=light",
		"/web/themes/" + entry.CurrentHash + "/preview.png",
		"/web/themes/" + entry.CurrentHash + "/assets/background.png",
	} {
		rr = httptest.NewRecorder()
		mux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
		themeAdminTestRequireStatus(t, rr, http.StatusNotFound)
		if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
			t.Fatalf("%s Content-Type = %q, want text/plain", path, ct)
		}
		if body := strings.ToLower(rr.Body.String()); strings.Contains(body, "<html") || strings.Contains(body, "<!doctype") {
			t.Fatalf("%s returned HTML: %q", path, rr.Body.String())
		}
	}

	rr = h.serve(h.app.handleAdminThemeList, h.request(http.MethodGet, "/admin/api/themes", nil, ""))
	themeAdminTestRequireStatus(t, rr, http.StatusOK)
	if payload := themeAdminTestDecodeCatalog(t, rr); len(payload.Themes) != 1 || payload.Quota.UsedThemes != 0 {
		t.Fatalf("admin catalog after delete = %#v", payload)
	}
}

// themeAdminTestCountingReader records any read attempt. The busy path must
// answer 503 before it touches the request body.
type themeAdminTestCountingReader struct {
	data  []byte
	reads int
}

func (r *themeAdminTestCountingReader) Read(p []byte) (int, error) {
	r.reads++
	if len(r.data) == 0 {
		return 0, io.EOF
	}
	n := copy(p, r.data)
	r.data = r.data[n:]
	return n, nil
}

func TestAdminThemePackageErrorsAndVerificationSlotScope(t *testing.T) {
	h := newThemeAdminTestHarness(t)
	themeAdminTestRequireSlotFree(t)

	validZip := h.packageZip("slot-theme", 8)

	rr := h.serve(h.app.handleAdminThemeInstall,
		h.request(http.MethodPost, "/admin/api/themes", validZip, "text/plain"))
	themeAdminTestRequireStatus(t, rr, http.StatusUnsupportedMediaType)
	themeAdminTestRequireMessage(t, rr, themeAPIMsgUnsupportedType)
	themeAdminTestRequireSlotFree(t)

	// A parse failure must release the slot for the next upload.
	rr = h.serve(h.app.handleAdminThemeInstall,
		h.request(http.MethodPost, "/admin/api/themes", []byte("this is not a zip archive"), themeAPIZipMediaType))
	themeAdminTestRequireStatus(t, rr, http.StatusBadRequest)
	themeAdminTestRequireMessage(t, rr, themeAPIMsgInvalidPackage)
	themeAdminTestRequireSlotFree(t)

	rr = h.serve(h.app.handleAdminThemeInstall,
		h.request(http.MethodPost, "/admin/api/themes", []byte{}, themeAPIZipMediaType))
	themeAdminTestRequireStatus(t, rr, http.StatusBadRequest)
	themeAdminTestRequireMessage(t, rr, themeAPIMsgInvalidPackage)
	themeAdminTestRequireSlotFree(t)

	oversized := make([]byte, ThemePackageMaxBytes+1)
	rr = h.serve(h.app.handleAdminThemeInstall,
		h.request(http.MethodPost, "/admin/api/themes", oversized, themeAPIZipMediaType))
	themeAdminTestRequireStatus(t, rr, http.StatusRequestEntityTooLarge)
	themeAdminTestRequireMessage(t, rr, themeAPIMsgPackageTooLarge)
	themeAdminTestRequireSlotFree(t)

	if catalog := h.catalog(); len(catalog.Themes) != 1 || catalog.Quota.UsedThemes != 0 {
		t.Fatalf("rejected packages changed the store: %#v", catalog)
	}

	// While the single verification slot is busy, an upload is rejected with
	// 503 + Retry-After and the body is never read.
	if !themeAPIAcquireVerification() {
		t.Fatal("verification slot was already held")
	}
	released := false
	t.Cleanup(func() {
		if !released {
			themeAPIReleaseVerification()
		}
	})
	reader := &themeAdminTestCountingReader{data: append([]byte(nil), validZip...)}
	busyReq := httptest.NewRequest(http.MethodPost, "/admin/api/themes", reader)
	busyReq.ContentLength = int64(len(validZip))
	busyReq.Header.Set("Content-Type", themeAPIZipMediaType)
	busyReq.Header.Set("X-Emby-Token", themeAdminTestToken)
	busyReq = themeAPITestRequestWithContext(busyReq, themeAdminTestAdminContext())
	rr = h.serve(h.app.handleAdminThemeInstall, busyReq)
	themeAdminTestRequireStatus(t, rr, http.StatusServiceUnavailable)
	if got := rr.Header().Get("Retry-After"); got != themeAPIRetryAfterSeconds {
		t.Fatalf("Retry-After = %q, want %q", got, themeAPIRetryAfterSeconds)
	}
	themeAdminTestRequireMessage(t, rr, themeAPIMsgBusy)
	if reader.reads != 0 {
		t.Fatalf("busy response consumed the body: %d reads", reader.reads)
	}
	if catalog := h.catalog(); len(catalog.Themes) != 1 {
		t.Fatalf("busy upload changed the store: %#v", catalog)
	}
	themeAPIReleaseVerification()
	released = true
	themeAdminTestRequireSlotFree(t)

	// The slot is usable again after the busy response.
	rr = h.install("slot-theme", 8)
	themeAdminTestRequireStatus(t, rr, http.StatusCreated)
	themeAdminTestRequireSlotFree(t)

	// InstallUpdate failure (theme already installed with expectedRevision 0)
	// must also release the slot.
	rr = h.install("slot-theme", 9)
	themeAdminTestRequireStatus(t, rr, http.StatusConflict)
	themeAdminTestRequireMessage(t, rr, themeAPIMsgExists)
	themeAdminTestRequireSlotFree(t)

	rr = h.install("after-exists-failure", 8)
	themeAdminTestRequireStatus(t, rr, http.StatusCreated)
	themeAdminTestRequireSlotFree(t)

	// Body rejection after the slot is acquired releases it.
	entry := h.catalogEntry("after-exists-failure")
	req := h.request(http.MethodPut, "/admin/api/themes/"+entry.ID+"/package",
		h.packageZip(entry.ID, 9), "text/plain")
	req.SetPathValue("id", entry.ID)
	req.Header.Set("If-Match", strconv.FormatInt(entry.Revision, 10))
	rr = h.serve(h.app.handleAdminThemeUpdate, req)
	themeAdminTestRequireStatus(t, rr, http.StatusUnsupportedMediaType)
	themeAdminTestRequireSlotFree(t)

	// InstallUpdate revision conflict on update releases the slot too.
	rr = h.update(entry.ID, 9, strconv.FormatInt(entry.Revision+1, 10))
	themeAdminTestRequireStatus(t, rr, http.StatusConflict)
	themeAdminTestRequireMessage(t, rr, themeAPIMsgConflict)
	themeAdminTestRequireSlotFree(t)

	rr = h.install("after-update-failure", 8)
	themeAdminTestRequireStatus(t, rr, http.StatusCreated)
	themeAdminTestRequireSlotFree(t)
}

func TestAdminThemeWritesRequireHeaderTokenAndAdminRole(t *testing.T) {
	h := newThemeAdminTestHarness(t)
	entry := installThemeStoreTestPackage(t, h.store, "auth-theme", 8)
	body := fmt.Sprintf(`{"enabled":false,"expectedRevision":%d}`, entry.Revision)
	path := "/admin/api/themes/auth-theme"

	// No request context at all: requireAdmin answers 401.
	rr := httptest.NewRecorder()
	h.app.requireAdmin(h.app.handleAdminThemePatch)(rr,
		httptest.NewRequest(http.MethodPatch, path, bytes.NewReader([]byte(body))))
	themeAdminTestRequireStatus(t, rr, http.StatusUnauthorized)

	// A valid user token is authenticated but not an administrator.
	userReq := httptest.NewRequest(http.MethodPatch, path, bytes.NewReader([]byte(body)))
	userReq.Header.Set("Content-Type", themeAPIJSONMediaType)
	userReq.Header.Set("X-Emby-Token", "user-token")
	userReq = themeAPITestRequestWithContext(userReq, &RequestContext{
		ProxyToken: "user-token",
		ProxyUser:  &tokenInfo{UserID: "user-1", Role: "user"},
	})
	rr = httptest.NewRecorder()
	h.app.requireAdmin(h.app.handleAdminThemePatch)(rr, userReq)
	themeAdminTestRequireStatus(t, rr, http.StatusForbidden)
	themeAdminTestRequireMessage(t, rr, themeAPIMsgAdminRequired)

	// Query-parameter tokens are never accepted for theme writes.
	queryReq := h.request(http.MethodPatch, path+"?api_key="+themeAdminTestToken, []byte(body), themeAPIJSONMediaType)
	queryReq.SetPathValue("id", "auth-theme")
	queryReq.Header.Del("X-Emby-Token")
	rr = h.serve(h.app.handleAdminThemePatch, queryReq)
	themeAdminTestRequireStatus(t, rr, http.StatusUnauthorized)
	themeAdminTestRequireMessage(t, rr, themeAPIMsgHeaderToken)

	// A header token that does not match the authenticated context is forged.
	forgedReq := h.request(http.MethodPatch, path, []byte(body), themeAPIJSONMediaType)
	forgedReq.SetPathValue("id", "auth-theme")
	forgedReq.Header.Set("X-Emby-Token", "forged-token")
	rr = h.serve(h.app.handleAdminThemePatch, forgedReq)
	themeAdminTestRequireStatus(t, rr, http.StatusUnauthorized)
	themeAdminTestRequireMessage(t, rr, themeAPIMsgAuthRequired)

	// Rejected token sources never change state.
	if stored := h.catalogEntry("auth-theme"); !stored.Enabled || stored.Revision != entry.Revision {
		t.Fatalf("rejected auth attempts changed the store: %#v", stored)
	}

	// Positive control: a matching Authorization header is accepted.
	authReq := h.request(http.MethodPatch, path, []byte(body), themeAPIJSONMediaType)
	authReq.SetPathValue("id", "auth-theme")
	authReq.Header.Del("X-Emby-Token")
	authReq.Header.Set("Authorization", `MediaBrowser Token="`+themeAdminTestToken+`"`)
	rr = h.serve(h.app.handleAdminThemePatch, authReq)
	themeAdminTestRequireStatus(t, rr, http.StatusOK)
	disabled := themeAdminTestDecodeAdmin(t, rr)
	if disabled.Enabled || disabled.Revision == entry.Revision {
		t.Fatalf("positive control response = %#v", disabled)
	}
	if stored := h.catalogEntry("auth-theme"); stored.Enabled || stored.Revision != disabled.Revision {
		t.Fatalf("authorized PATCH did not reach the store: %#v", stored)
	}
}
