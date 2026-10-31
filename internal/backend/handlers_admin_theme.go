package backend

import (
	"errors"
	"net/http"
)

// Administrator theme management API:
//
//	GET    /admin/api/themes
//	POST   /admin/api/themes                          application/zip
//	PUT    /admin/api/themes/{id}/package             application/zip + If-Match
//	PATCH  /admin/api/themes/{id}                     JSON {enabled, expectedRevision}
//	DELETE /admin/api/themes/{id}                     If-Match
//	PUT    /admin/api/theme-site                      JSON {themeId, expectedRevision}
//
// Package verification runs in the single process-wide slot and never holds
// adminMutationMu: a long unzip/image decode must not serialize unrelated admin
// writes. The store's own short lock plus its revision re-check inside the
// commit transaction provide the CAS, and a busy slot answers 503 with
// Retry-After instead of queueing.

// registerThemeRoutes wires the theme API and its public resources.
func (a *App) registerThemeRoutes(mux *http.ServeMux) {
	// Anonymous site default (no user, no credentials).
	mux.HandleFunc("GET /web/api/theme-site", a.handleWebThemeSite)
	// Authenticated user preferences and selectable catalog.
	mux.HandleFunc("GET /web/api/themes", a.withContext(a.requireAuth(a.handleWebThemeCatalog)))
	mux.HandleFunc("GET /web/api/appearance", a.withContext(a.requireAuth(a.handleWebAppearanceGet)))
	mux.HandleFunc("PUT /web/api/appearance", a.withContext(a.requireAuth(a.handleWebAppearancePut)))
	// Administrator management.
	mux.HandleFunc("GET /admin/api/themes", a.withContext(a.requireAdmin(a.handleAdminThemeList)))
	mux.HandleFunc("POST /admin/api/themes", a.withContext(a.requireAdmin(a.handleAdminThemeInstall)))
	mux.HandleFunc("PUT /admin/api/themes/{id}/package", a.withContext(a.requireAdmin(a.handleAdminThemeUpdate)))
	mux.HandleFunc("PATCH /admin/api/themes/{id}", a.withContext(a.requireAdmin(a.handleAdminThemePatch)))
	mux.HandleFunc("DELETE /admin/api/themes/{id}", a.withContext(a.requireAdmin(a.handleAdminThemeDelete)))
	mux.HandleFunc("PUT /admin/api/theme-site", a.withContext(a.requireAdmin(a.handleAdminThemeSitePut)))

	// Public immutable resources and the fixed preview document. Unknown paths
	// under /web/themes/ are a plain 404 and never reach the SPA fallback.
	mux.HandleFunc("GET /web/themes/{hash}/theme.css", a.handleThemeCSSResource)
	mux.HandleFunc("GET /web/themes/{hash}/preview.png", a.handleThemePreviewImagePNG)
	mux.HandleFunc("GET /web/themes/{hash}/preview.jpg", a.handleThemePreviewImageJPG)
	mux.HandleFunc("GET /web/themes/{hash}/assets/{asset...}", a.handleThemeAssetImage)
	mux.HandleFunc("GET /web/themes/{hash}/preview.html", a.handleThemePreviewDocument)
	mux.HandleFunc("/web/themes/", a.handleThemeResourceNotFound)
}

// handleAdminThemeList returns the catalog, site default, quota and health.
func (a *App) handleAdminThemeList(w http.ResponseWriter, r *http.Request) {
	store := a.themeStoreInstance()
	if store == nil || !store.Available() {
		themeAPIError(w, http.StatusServiceUnavailable, themeAPIMsgUnavailable)
		return
	}
	catalog, err := store.Catalog()
	if err != nil {
		status, message := themeAPIStoreStatus(err)
		themeAPIError(w, status, message)
		return
	}
	payload := themeAPIAdminCatalogDTO{
		Available: true,
		Themes:    make([]themeAPIAdminThemeDTO, 0, len(catalog.Themes)),
		Site:      themeAPISiteFrom(catalog.Site, true),
		Quota: themeAPIQuotaDTO{
			MaxThemes:  catalog.Quota.MaxThemes,
			UsedThemes: catalog.Quota.UsedThemes,
			MaxBytes:   catalog.Quota.MaxBytes,
			UsedBytes:  catalog.Quota.UsedBytes,
		},
		Health: themeAPIHealthDTO{
			Available:   catalog.Health.Available,
			Recovered:   catalog.Health.Recovered,
			Missing:     catalog.Health.Missing,
			OrphanDirs:  catalog.Health.OrphanDirs,
			OrphanBytes: catalog.Health.OrphanBytes,
			Problems:    append([]string(nil), catalog.Health.Problems...),
		},
	}
	if payload.Health.Problems == nil {
		payload.Health.Problems = []string{}
	}
	for _, entry := range catalog.Themes {
		payload.Themes = append(payload.Themes, themeAPIAdminThemeFrom(entry, themeAPIDefinitionFor(store, entry.CurrentHash)))
	}
	themeAPINoStore(w)
	themeAPIWriteJSON(w, http.StatusOK, payload)
}

// handleAdminThemeInstall installs a first version of a theme.
func (a *App) handleAdminThemeInstall(w http.ResponseWriter, r *http.Request) {
	if !themeAPIRequireHeaderToken(w, r) {
		return
	}
	store := a.themeStoreInstance()
	if store == nil || !store.Available() {
		themeAPIError(w, http.StatusServiceUnavailable, themeAPIMsgUnavailable)
		return
	}
	// The single verification slot must cover the entire package workload:
	// reading the body, parsing/decoding images and the store's re-validation
	// inside InstallUpdate. Acquiring it after the body read let a second
	// upload parse images while this request was still publishing.
	if !themeAPIAcquireVerification() {
		themeAPIBusy(w)
		return
	}
	defer themeAPIReleaseVerification()
	data, ok := themeAPIReadPackageBody(w, r)
	if !ok {
		return
	}
	pkg, err := ParseThemePackage(data)
	if err != nil {
		status, message := themeAPIPackageStatus(err)
		themeAPIError(w, status, message)
		return
	}
	entry, err := store.InstallUpdate(pkg, 0)
	if err != nil {
		status, message := themeAPIStoreStatus(err)
		themeAPIError(w, status, message)
		return
	}
	themeAPINoStore(w)
	themeAPIWriteJSON(w, http.StatusCreated, themeAPIAdminThemeFrom(entry, themeAPIDefinitionFor(store, entry.CurrentHash)))
}

// handleAdminThemeUpdate publishes a new version with an If-Match revision.
func (a *App) handleAdminThemeUpdate(w http.ResponseWriter, r *http.Request) {
	if !themeAPIRequireHeaderToken(w, r) {
		return
	}
	target := r.PathValue("id")
	revision, ok := themeAPIExpectedRevisionFromIfMatch(r)
	if target == "" {
		themeAPIError(w, http.StatusBadRequest, themeAPIMsgInvalidRequest)
		return
	}
	if !ok {
		themeAPIError(w, http.StatusBadRequest, themeAPIMsgMissingRevision)
		return
	}
	store := a.themeStoreInstance()
	if store == nil || !store.Available() {
		themeAPIError(w, http.StatusServiceUnavailable, themeAPIMsgUnavailable)
		return
	}
	// See handleAdminThemeInstall: the slot spans the body read, package
	// parsing and the store's InstallUpdate re-validation.
	if !themeAPIAcquireVerification() {
		themeAPIBusy(w)
		return
	}
	defer themeAPIReleaseVerification()
	data, ok := themeAPIReadPackageBody(w, r)
	if !ok {
		return
	}
	pkg, err := ParseThemePackage(data)
	if err != nil {
		status, message := themeAPIPackageStatus(err)
		themeAPIError(w, status, message)
		return
	}
	if pkg.Manifest.ID != target {
		themeAPIError(w, http.StatusBadRequest, themeAPIMsgTargetMismatch)
		return
	}
	entry, err := store.InstallUpdate(pkg, revision)
	if err != nil {
		status, message := themeAPIStoreStatus(err)
		themeAPIError(w, status, message)
		return
	}
	themeAPINoStore(w)
	themeAPIWriteJSON(w, http.StatusOK, themeAPIAdminThemeFrom(entry, themeAPIDefinitionFor(store, entry.CurrentHash)))
}

// handleAdminThemePatch toggles enabled with the expected revision.
func (a *App) handleAdminThemePatch(w http.ResponseWriter, r *http.Request) {
	if !themeAPIRequireHeaderToken(w, r) {
		return
	}
	themeID := r.PathValue("id")
	if themeID == "" {
		themeAPIError(w, http.StatusBadRequest, themeAPIMsgInvalidRequest)
		return
	}
	fields, ok := themeAPIDecodeJSON(w, r, []string{"enabled", "expectedRevision"})
	if !ok {
		return
	}
	enabled, err := themeAPIRequiredBool(fields, "enabled")
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
	entry, err := store.SetEnabled(themeID, enabled, revision)
	if err != nil {
		status, message := themeAPIStoreStatus(err)
		themeAPIError(w, status, message)
		return
	}
	themeAPINoStore(w)
	themeAPIWriteJSON(w, http.StatusOK, themeAPIAdminThemeFrom(entry, themeAPIDefinitionFor(store, entry.CurrentHash)))
}

// handleAdminThemeDelete removes a theme with an If-Match revision.
func (a *App) handleAdminThemeDelete(w http.ResponseWriter, r *http.Request) {
	if !themeAPIRequireHeaderToken(w, r) {
		return
	}
	themeID := r.PathValue("id")
	if themeID == "" {
		themeAPIError(w, http.StatusBadRequest, themeAPIMsgInvalidRequest)
		return
	}
	revision, ok := themeAPIExpectedRevisionFromIfMatch(r)
	if !ok {
		themeAPIError(w, http.StatusBadRequest, themeAPIMsgMissingRevision)
		return
	}
	store := a.themeStoreInstance()
	if store == nil || !store.Available() {
		themeAPIError(w, http.StatusServiceUnavailable, themeAPIMsgUnavailable)
		return
	}
	if err := store.Delete(themeID, revision); err != nil {
		status, message := themeAPIStoreStatus(err)
		themeAPIError(w, status, message)
		return
	}
	themeAPINoStore(w)
	themeAPIWriteJSON(w, http.StatusOK, themeAPISuccessDTO{Success: true})
}

// handleAdminThemeSitePut updates the anonymous site default with CAS.
func (a *App) handleAdminThemeSitePut(w http.ResponseWriter, r *http.Request) {
	if !themeAPIRequireHeaderToken(w, r) {
		return
	}
	fields, ok := themeAPIDecodeJSON(w, r, []string{"themeId", "expectedRevision"})
	if !ok {
		return
	}
	themeID, err := themeAPINullableString(fields, "themeId")
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
	site, err := store.SetSiteDefault(themeID, revision)
	if err != nil {
		status, message := themeAPIStoreStatus(err)
		themeAPIError(w, status, message)
		return
	}
	themeAPINoStore(w)
	themeAPIWriteJSON(w, http.StatusOK, themeAPISiteFrom(site, true))
}

// themeAPISiteFrom maps the store selection to the frozen site DTO. A nil
// selection is always reported as the explicit builtin ID.
func themeAPISiteFrom(site ThemeSiteSettings, available bool) themeAPISiteDTO {
	return themeAPISiteDTO{
		Available: available,
		ThemeID:   themeAPIThemeIDString(site.ThemeID),
		Revision:  site.Revision,
	}
}

// themeAPIPreferenceFrom maps one preference to the frozen DTO.
func themeAPIPreferenceFrom(preference ThemePreference) themeAPIPreferenceDTO {
	payload := themeAPIPreferenceDTO{
		Mode:     preference.Mode,
		Revision: preference.Revision,
	}
	if preference.ThemeID != nil {
		value := *preference.ThemeID
		payload.ThemeID = &value
	}
	if payload.Mode == "" {
		payload.Mode = ThemeModeDefault
	}
	return payload
}

// themeAPIDefinitionFor loads the validated token definition for one content
// hash. The builtin theme and an entry without recoverable resources return
// nil, which the DTO serializes as JSON null.
func themeAPIDefinitionFor(store *ThemeStore, contentHash string) *ThemeDefinition {
	if store == nil || contentHash == "" {
		return nil
	}
	definition, err := store.Definition(contentHash)
	if err != nil {
		return nil
	}
	return &definition
}

// themeAPIPublicThemeFrom maps one public catalog entry and derives its
// immutable resource URLs.
func themeAPIPublicThemeFrom(entry ThemePublicEntry, definition *ThemeDefinition) themeAPIPublicThemeDTO {
	variants := entry.Variants
	if len(variants) == 0 {
		variants = themeStoreThemeVariants()
	}
	if len(variants) > 0 {
		variants = append([]string(nil), variants...)
	}
	return themeAPIPublicThemeDTO{
		ID:              entry.ID,
		Name:            entry.Name,
		Author:          entry.Author,
		Description:     entry.Description,
		Version:         entry.Version,
		Hash:            entry.Hash,
		Variants:        variants,
		Builtin:         false,
		CSSURL:          themeAPICSSURL(entry.Hash),
		PreviewURL:      themeAPIResourceURL(entry.Hash, entry.Preview),
		BackgroundURL:   themeAPIResourceURL(entry.Hash, entry.BackgroundImage),
		PreviewLightURL: themeAPIPreviewURL(entry.Hash, ThemeModeLight),
		PreviewDarkURL:  themeAPIPreviewURL(entry.Hash, ThemeModeDark),
		Definition:      definition,
	}
}

// themeAPIAdminThemeFrom maps one admin catalog entry and derives its
// immutable resource URLs.
func themeAPIAdminThemeFrom(entry ThemeCatalogEntry, definition *ThemeDefinition) themeAPIAdminThemeDTO {
	variants := entry.Variants
	if len(variants) == 0 {
		variants = themeStoreThemeVariants()
	}
	if len(variants) > 0 {
		variants = append([]string(nil), variants...)
	}
	hash := entry.CurrentHash
	return themeAPIAdminThemeDTO{
		ID:              entry.ID,
		Name:            entry.Name,
		Author:          entry.Author,
		Description:     entry.Description,
		Version:         entry.Version,
		Hash:            hash,
		Variants:        variants,
		Builtin:         entry.Builtin,
		Enabled:         entry.Enabled,
		Available:       entry.Available,
		Revision:        entry.Revision,
		PackageBytes:    entry.PackageBytes,
		CurrentHash:     entry.CurrentHash,
		PreviousHash:    entry.PreviousHash,
		Problem:         entry.Problem,
		CSSURL:          themeAPICSSURL(entry.CurrentHash),
		PreviewURL:      themeAPIResourceURL(entry.CurrentHash, entry.Preview),
		BackgroundURL:   themeAPIResourceURL(entry.CurrentHash, entry.BackgroundImage),
		PreviewLightURL: themeAPIPreviewURL(entry.CurrentHash, ThemeModeLight),
		PreviewDarkURL:  themeAPIPreviewURL(entry.CurrentHash, ThemeModeDark),
		Definition:      definition,
	}
}

// themeAPIPublicBuiltinTheme is the implicit builtin entry of the /web catalog.
func themeAPIPublicBuiltinTheme() themeAPIPublicThemeDTO {
	return themeAPIPublicThemeDTO{
		ID:       ThemeIDBuiltin,
		Name:     "内置主题",
		Variants: []string{ThemeVariantLight, ThemeVariantDark},
		Builtin:  true,
	}
}

// themeAPIAdminBuiltinTheme is the synthetic read-only builtin catalog entry.
func themeAPIAdminBuiltinTheme() themeAPIAdminThemeDTO {
	return themeAPIAdminThemeDTO{
		ID:        ThemeIDBuiltin,
		Name:      "内置主题",
		Variants:  []string{ThemeVariantLight, ThemeVariantDark},
		Builtin:   true,
		Enabled:   true,
		Available: true,
	}
}

// themeAPIWritePreferenceStoreFailure maps a preference store error.
func themeAPIWritePreferenceStoreFailure(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrThemeStoreUnavailable), errors.Is(err, ErrThemeStoreClosed),
		errors.Is(err, ErrThemeCorrupt):
		themeAPIError(w, http.StatusServiceUnavailable, themeAPIMsgUnavailable)
	case errors.Is(err, ErrThemeRevisionConflict):
		themeAPIError(w, http.StatusConflict, themeAPIMsgConflict)
	case errors.Is(err, ErrThemeNotFound), errors.Is(err, ErrThemeUnavailable):
		themeAPIError(w, http.StatusConflict, themeAPIMsgThemeUnavailable)
	case errors.Is(err, ErrThemeInvalid):
		themeAPIError(w, http.StatusBadRequest, themeAPIMsgInvalidRequest)
	default:
		themeAPIError(w, http.StatusServiceUnavailable, themeAPIMsgUnavailable)
	}
}
