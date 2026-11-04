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

// Dual-cache budget constants are the single source of truth for the web
// player's strict-admission path. They mirror the vendor validation rules:
// ioBytes + demuxBytes < maxBytes, and maxPacketBytes < demuxBytes. The MSE
// byte budget is derived by the vendor as maxBytes - ioBytes - demuxBytes
// (= 240,000,000). The web player falls back to these same values only when
// an older backend omits the dualCacheBudget field.
const (
	dualCacheMaxBytes          = 300_000_000
	dualCacheIOBytes           = 12_000_000
	dualCacheDemuxBytes        = 48_000_000
	dualCacheMaxPacketBytes    = 16_000_000
	dualCacheMaxForwardSeconds = 120
)

// handlePlaybackFeatures reports the web-player feature flags derived from the
// authoritative config. Like /System/Info, the route is registered behind
// requireAuth, so the client must send its token. The dual-cache flag is
// opt-in: the default (absent key) keeps baseline playback, and the client
// treats a missing field as off.
// dualCacheBudget is always reported alongside the flag so the client uses the
// server-controlled budget instead of its own hardcoded copy.
func (a *App) handlePlaybackFeatures(w http.ResponseWriter, r *http.Request) {
	cfg := a.ConfigStore.Snapshot()
	writeJSON(w, http.StatusOK, map[string]any{
		"dualCache": cfg.Playback.DualCacheEnabled,
		"dualCacheBudget": map[string]any{
			"maxBytes":          dualCacheMaxBytes,
			"ioBytes":           dualCacheIOBytes,
			"demuxBytes":        dualCacheDemuxBytes,
			"maxPacketBytes":    dualCacheMaxPacketBytes,
			"maxForwardSeconds": dualCacheMaxForwardSeconds,
		},
	})
}

func (a *App) handleSystemPing(w http.ResponseWriter, r *http.Request) {
	_, _ = w.Write([]byte("Emby-In-One"))
}

func (a *App) handleRoot(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(w, rootPageHTML)
}
