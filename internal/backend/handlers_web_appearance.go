package backend

import (
	"net/http"
)

// /web appearance API:
//
//	GET /web/api/theme-site    anonymous site default (no-cache, may degrade)
//	GET /web/api/themes        authenticated selectable catalog (no-store)
//	GET /web/api/appearance    the caller's own preference (no-store)
//	PUT /web/api/appearance    strict JSON, header token, CAS revision
//
// The preference key always comes from the authenticated request context. A
// body userID is not part of the schema, so a caller cannot read or write
// another account's selection. Administrators reuse the reserved legacy key so
// no users row is required.

// handleWebThemeSite reports the anonymous site default. When the store is nil,
// closed or its database is unavailable the response stays 200 but honestly
// declares available=false and the builtin theme, so /web can boot with the
// builtin palette while an administrator sees the failure elsewhere.
func (a *App) handleWebThemeSite(w http.ResponseWriter, r *http.Request) {
	payload := themeAPISiteDTO{Available: false, ThemeID: ThemeIDBuiltin}
	if store := a.themeStoreInstance(); store != nil && store.Available() {
		if site, err := store.Site(); err == nil {
			payload = themeAPISiteFrom(site, true)
			payload.Theme = themeAPISiteTheme(store, site)
		}
	}
	themeAPIRevalidate(w)
	themeAPIWriteJSON(w, http.StatusOK, payload)
}

// themeAPISiteTheme resolves the site default to its public entry. The builtin
// default, a theme that is no longer enabled/available and any catalog failure
// yield nil (JSON null), so an anonymous client falls back to the builtin
// palette instead of receiving a partial or fabricated theme. Only the selected
// entry is returned; no other installed theme is exposed anonymously.
func themeAPISiteTheme(store *ThemeStore, site ThemeSiteSettings) *themeAPIPublicThemeDTO {
	if store == nil || site.ThemeID == nil {
		return nil
	}
	themeID := *site.ThemeID
	if themeID == "" || themeID == ThemeIDBuiltin {
		return nil
	}
	catalog, err := store.PublicCatalog()
	if err != nil {
		return nil
	}
	for _, entry := range catalog.Themes {
		if entry.ID != themeID {
			continue
		}
		payload := themeAPIPublicThemeFrom(entry, themeAPIDefinitionFor(store, entry.Hash))
		return &payload
	}
	return nil
}

// handleWebThemeCatalog lists enabled and available themes for /web.
func (a *App) handleWebThemeCatalog(w http.ResponseWriter, r *http.Request) {
	store := a.themeStoreInstance()
	if store == nil || !store.Available() {
		themeAPIError(w, http.StatusServiceUnavailable, themeAPIMsgUnavailable)
		return
	}
	catalog, err := store.PublicCatalog()
	if err != nil {
		status, message := themeAPIStoreStatus(err)
		themeAPIError(w, status, message)
		return
	}
	payload := themeAPIPublicCatalogDTO{
		Themes: make([]themeAPIPublicThemeDTO, 0, len(catalog.Themes)+1),
	}
	payload.Themes = append(payload.Themes, themeAPIPublicBuiltinTheme())
	for _, entry := range catalog.Themes {
		payload.Themes = append(payload.Themes, themeAPIPublicThemeFrom(entry, themeAPIDefinitionFor(store, entry.Hash)))
	}
	themeAPINoStore(w)
	themeAPIWriteJSON(w, http.StatusOK, payload)
}

// handleWebAppearanceGet returns the caller's preference. GetPreference seeds a
// row with a positive opaque revision on first access, so the client always
// has a valid expectation for the next PUT.
func (a *App) handleWebAppearanceGet(w http.ResponseWriter, r *http.Request) {
	userKey, ok := themeAPIUserKey(requestContextFrom(r.Context()))
	if !ok {
		themeAPIError(w, http.StatusBadRequest, themeAPIMsgMissingIdentity)
		return
	}
	store := a.themeStoreInstance()
	if store == nil || !store.Available() {
		themeAPIError(w, http.StatusServiceUnavailable, themeAPIMsgUnavailable)
		return
	}
	preference, err := store.GetPreference(userKey)
	if err != nil {
		status, message := themeAPIStoreStatus(err)
		themeAPIError(w, status, message)
		return
	}
	themeAPINoStore(w)
	themeAPIWriteJSON(w, http.StatusOK, themeAPIPreferenceFrom(preference))
}

// handleWebAppearancePut saves the caller's preference with a full payload and
// a CAS revision.
func (a *App) handleWebAppearancePut(w http.ResponseWriter, r *http.Request) {
	if !themeAPIRequireHeaderToken(w, r) {
		return
	}
	userKey, ok := themeAPIUserKey(requestContextFrom(r.Context()))
	if !ok {
		themeAPIError(w, http.StatusBadRequest, themeAPIMsgMissingIdentity)
		return
	}
	fields, ok := themeAPIDecodeJSON(w, r, []string{"themeId", "mode", "expectedRevision"})
	if !ok {
		return
	}
	themeID, err := themeAPINullableString(fields, "themeId")
	if err != nil {
		themeAPIError(w, http.StatusBadRequest, themeAPIMsgInvalidRequest)
		return
	}
	mode, err := themeAPIRequiredString(fields, "mode")
	if err != nil {
		themeAPIError(w, http.StatusBadRequest, themeAPIMsgInvalidRequest)
		return
	}
	revision, err := themeAPIRequiredInt(fields, "expectedRevision")
	if err != nil || !themeAPIRevisionInRange(revision) {
		themeAPIError(w, http.StatusBadRequest, themeAPIMsgInvalidRequest)
		return
	}
	store := a.themeStoreInstance()
	if store == nil || !store.Available() {
		themeAPIError(w, http.StatusServiceUnavailable, themeAPIMsgUnavailable)
		return
	}
	preference, err := store.SavePreference(userKey, themeID, mode, revision)
	if err != nil {
		themeAPIWritePreferenceStoreFailure(w, err)
		return
	}
	themeAPINoStore(w)
	themeAPIWriteJSON(w, http.StatusOK, themeAPIPreferenceFrom(preference))
}
