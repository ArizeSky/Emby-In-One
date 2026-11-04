package backend

import (
	"encoding/json"
	"io"
	"net/http"
)

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func decodeJSONBody(r *http.Request, out any) error {
	defer r.Body.Close()
	decoder := json.NewDecoder(r.Body)
	return decoder.Decode(out)
}

func (a *App) handleSystemInfoPublic(w http.ResponseWriter, r *http.Request) {
	cfg := a.ConfigStore.Snapshot()
	writeJSON(w, http.StatusOK, map[string]any{
		"LocalAddress":                         a.requestOrigin(r),
		"ServerName":                           cfg.Server.Name,
		"Version":                              "4.7.14.0",
		"ProductName":                          "Emby Server",
		"Id":                                   cfg.Server.ID,
		"StartupWizardCompleted":               true,
		"OperatingSystem":                      "Linux",
		"CanSelfRestart":                       false,
		"CanLaunchWebBrowser":                  false,
		"HasUpdateAvailable":                   false,
		"SupportsAutoRunAtStartup":             false,
		"HardwareAccelerationRequiresPremiere": false,
	})
}

func (a *App) handleSystemInfo(w http.ResponseWriter, r *http.Request) {
	cfg := a.ConfigStore.Snapshot()
	writeJSON(w, http.StatusOK, map[string]any{
		"LocalAddress":                         a.requestOrigin(r),
		"WanAddress":                           "",
		"ServerName":                           cfg.Server.Name,
		"Version":                              "4.7.14.0",
		"ProductName":                          "Emby Server",
		"Id":                                   cfg.Server.ID,
		"StartupWizardCompleted":               true,
		"OperatingSystem":                      "Linux",
		"OperatingSystemDisplayName":           "Linux",
		"CanSelfRestart":                       false,
		"CanLaunchWebBrowser":                  false,
		"HasUpdateAvailable":                   false,
		"SupportsAutoRunAtStartup":             false,
		"SystemUpdateLevel":                    "Release",
		"HardwareAccelerationRequiresPremiere": false,
		"HasPendingRestart":                    false,
		"IsShuttingDown":                       false,
		"TranscodingTempPath":                  "/tmp",
		"LogPath":                              "/tmp",
		"InternalMetadataPath":                 "/tmp",
		"CachePath":                            "/tmp",
		"ProgramDataPath":                      "/tmp",
		"ItemsByNamePath":                      "/tmp",
	})
}

func (a *App) handleSystemEndpoint(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"IsLocal": false, "IsInNetwork": false})
}

// handlePlaybackFeatures reports the web-player feature flags derived from the
// authoritative config. The dual-cache flag is opt-in: the default (absent
// key) keeps baseline playback, and the client treats a missing field as off.
func (a *App) handlePlaybackFeatures(w http.ResponseWriter, r *http.Request) {
	cfg := a.ConfigStore.Snapshot()
	writeJSON(w, http.StatusOK, map[string]any{
		"dualCache": cfg.Playback.DualCacheEnabled,
	})
}

func (a *App) handleSystemPing(w http.ResponseWriter, r *http.Request) {
	_, _ = w.Write([]byte("Emby-In-One"))
}

func (a *App) handleRoot(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(w, rootPageHTML)
}
