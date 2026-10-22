package backend

import "net/http"

func (a *App) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /System/Info/Public", a.handleSystemInfoPublic)
	mux.HandleFunc("GET /System/Info", a.withContext(a.requireAuth(a.handleSystemInfo)))
	mux.HandleFunc("GET /System/Endpoint", a.withContext(a.handleSystemEndpoint))
	mux.HandleFunc("GET /System/Ping", a.withContext(a.handleSystemPing))
	mux.HandleFunc("POST /System/Ping", a.withContext(a.handleSystemPing))

	mux.HandleFunc("POST /Users/AuthenticateByName", a.withContext(a.handleAuthenticateByName))
	mux.HandleFunc("GET /Users/Public", a.withContext(a.handleUsersPublic))
	mux.HandleFunc("GET /Users/{userId}", a.withContext(a.requireAuth(a.handleUserObject)))
	mux.HandleFunc("GET /Users/{userId}/Views", a.withContext(a.requireAuth(a.handleUserViews)))
	mux.HandleFunc("GET /Users/{userId}/GroupingOptions", a.withContext(a.requireAuth(a.handleUserGroupingOptions)))
	mux.HandleFunc("POST /Users/{userId}/Configuration", a.withContext(a.requireAuth(a.handleUserConfiguration)))
	mux.HandleFunc("POST /Users/{userId}/Policy", a.withContext(a.requireAuth(a.handleUserPolicy)))
	a.registerMediaRoutes(mux)
	a.registerLibraryAndImageRoutes(mux)
	a.registerSessionAndUserStateRoutes(mux)

	mux.HandleFunc("POST /admin/api/logout", a.withContext(a.requireAuth(a.handleAdminLogout)))
	mux.HandleFunc("GET /admin/api/client-info", a.withContext(a.requireAdmin(a.handleAdminClientInfo)))
	mux.HandleFunc("GET /admin/api/status", a.withContext(a.requireAdmin(a.handleAdminStatus)))
	mux.HandleFunc("GET /admin/api/upstream", a.withContext(a.requireAdmin(a.handleAdminUpstreamList)))
	mux.HandleFunc("POST /admin/api/upstream", a.withAdminMutation(a.withContext(a.requireAdmin(a.handleAdminUpstreamCreate))))
	mux.HandleFunc("PUT /admin/api/upstream/{id}", a.withAdminMutation(a.withContext(a.requireAdmin(a.handleAdminUpstreamUpdate))))
	mux.HandleFunc("PATCH /admin/api/upstream/{id}/aggregation", a.withAdminMutation(a.withContext(a.requireAdmin(a.handleAdminUpstreamAggregation))))
	mux.HandleFunc("POST /admin/api/upstream/reorder", a.withAdminMutation(a.withContext(a.requireAdmin(a.handleAdminUpstreamReorder))))
	mux.HandleFunc("DELETE /admin/api/upstream/{id}", a.withAdminMutation(a.withContext(a.requireAdmin(a.handleAdminUpstreamDelete))))
	mux.HandleFunc("POST /admin/api/upstream/{id}/reconnect", a.withContext(a.requireAdmin(a.handleAdminUpstreamReconnect)))
	mux.HandleFunc("GET /admin/api/proxies", a.withContext(a.requireAdmin(a.handleAdminProxiesList)))
	mux.HandleFunc("POST /admin/api/proxies", a.withAdminMutation(a.withContext(a.requireAdmin(a.handleAdminProxiesCreate))))
	mux.HandleFunc("POST /admin/api/proxies/test", a.withContext(a.requireAdmin(a.handleAdminProxyTest)))
	mux.HandleFunc("DELETE /admin/api/proxies/{id}", a.withAdminMutation(a.withContext(a.requireAdmin(a.handleAdminProxiesDelete))))
	mux.HandleFunc("GET /admin/api/settings", a.withContext(a.requireAdmin(a.handleAdminSettings)))
	mux.HandleFunc("PUT /admin/api/settings", a.withAdminMutation(a.withContext(a.requireAdmin(a.handleAdminSettingsUpdate))))
	mux.HandleFunc("GET /admin/api/ua", a.withContext(a.requireAdmin(a.handleAdminUAList)))
	mux.HandleFunc("POST /admin/api/ua/resident", a.withAdminMutation(a.withContext(a.requireAdmin(a.handleAdminUAResidentSave))))
	mux.HandleFunc("PATCH /admin/api/ua/resident/{id}", a.withAdminMutation(a.withContext(a.requireAdmin(a.handleAdminUAResidentRename))))
	mux.HandleFunc("POST /admin/api/ua/resident/{id}/replace", a.withAdminMutation(a.withContext(a.requireAdmin(a.handleAdminUAResidentReplace))))
	mux.HandleFunc("DELETE /admin/api/ua/resident/{id}", a.withAdminMutation(a.withContext(a.requireAdmin(a.handleAdminUAResidentDelete))))
	mux.HandleFunc("POST /admin/api/ua/resident/{id}/assignments", a.withAdminMutation(a.withContext(a.requireAdmin(a.handleAdminUAResidentAssignments))))
	mux.HandleFunc("GET /admin/api/logs", a.withContext(a.requireAdmin(a.handleAdminLogs)))
	mux.HandleFunc("GET /admin/api/logs/download", a.withContext(a.requireAdmin(a.handleAdminLogsDownload)))
	mux.HandleFunc("DELETE /admin/api/logs", a.withContext(a.requireAdmin(a.handleAdminLogsClear)))
	mux.HandleFunc("GET /admin/api/users", a.withContext(a.requireAdmin(a.handleAdminUsersList)))
	mux.HandleFunc("POST /admin/api/users", a.withContext(a.requireAdmin(a.handleAdminUsersCreate)))
	mux.HandleFunc("PUT /admin/api/users/{id}", a.withContext(a.requireAdmin(a.handleAdminUsersUpdate)))
	mux.HandleFunc("DELETE /admin/api/users/{id}", a.withContext(a.requireAdmin(a.handleAdminUsersDelete)))
	mux.HandleFunc("GET /admin/api/upstream/{id}/libraries", a.withContext(a.requireAdmin(a.handleAdminUpstreamLibraries)))
	mux.HandleFunc("GET /admin/api/home-libraries", a.withContext(a.requireAdmin(a.handleAdminHomeLibrariesGet)))
	mux.HandleFunc("PUT /admin/api/home-libraries", a.withContext(a.requireAdmin(a.handleAdminHomeLibrariesPut)))
	// The panel answers on /admin/, the URL every install script, the CLI and the
	// README print. "GET /admin/{$}" is the exact-path pattern: without it the
	// "/admin/" subtree below would answer the directory with a file listing.
	mux.HandleFunc("GET /admin/{$}", a.handleAdminPanel)
	mux.Handle("/admin/", a.adminFileServer())
	mux.HandleFunc("GET /admin", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/admin/", http.StatusFound)
	})

	// The web player is a single-page client: /web/ itself serves the document
	// and every sub-path that is not a static asset falls back to it so deep
	// links reach the router. The COOP/COEP headers are scoped to /web/ so the
	// WASM player can use SharedArrayBuffer, while the rest of the API keeps
	// answering without isolation headers.
	mux.Handle("GET /web/{$}", webCrossOriginIsolation(http.HandlerFunc(a.handleWebPanel)))
	mux.Handle("/web/", webCrossOriginIsolation(http.HandlerFunc(a.handleWebSPA)))
	mux.HandleFunc("GET /web", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/web/", http.StatusFound)
	})
	mux.HandleFunc("GET /favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("GET /{$}", a.handleRoot)
	mux.HandleFunc("/", a.withContext(a.requireAuth(a.handleFallbackProxy)))

	return a.bodyLimitMiddleware(a.loggingMiddleware(a.prefixCompatMiddleware(a.corsMiddleware(mux))))
}
