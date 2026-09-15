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
		"LocalAddress":                         requestOrigin(r),
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
		"LocalAddress":                         requestOrigin(r),
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

func (a *App) handleSystemPing(w http.ResponseWriter, r *http.Request) {
	_, _ = w.Write([]byte("Emby Aggregator"))
}

func (a *App) handleRoot(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(w, `<!DOCTYPE html><html lang="zh-CN"><head><meta charset="UTF-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Emby-in-One</title><style>body{font-family:sans-serif;background:#f8fafc;display:flex;align-items:center;justify-content:center;min-height:100vh;margin:0}.box{background:#fff;border:1px solid #e2e8f0;border-radius:12px;padding:2.5rem 3rem;text-align:center;max-width:400px}.icon{font-size:3rem;margin-bottom:1rem}.title{font-size:1.4rem;font-weight:700;color:#1e293b;margin-bottom:.5rem}.sub{color:#64748b;font-size:.95rem;margin-bottom:1.5rem}.btn{display:inline-block;background:#2563eb;color:#fff;padding:.6rem 1.4rem;border-radius:8px;text-decoration:none;font-weight:600;font-size:.9rem}</style></head><body><div class="box"><div class="icon">⛔</div><div class="title">此地址仅供 Emby 客户端使用</div><div class="sub">请将 Emby 客户端连接地址设置为本地址。<br>管理面板请访问 <code>/admin</code> 路径。</div><a class="btn" href="/admin">前往管理面板</a></div></body></html>`)
}
