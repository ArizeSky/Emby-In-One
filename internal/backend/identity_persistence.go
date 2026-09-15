package backend

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type persistedCapturedHeaders struct {
	Headers    http.Header `json:"headers"`
	CapturedAt string      `json:"capturedAt"`
}

type identityPersistenceSnapshot struct {
	Version             int                                 `json:"version"`
	LatestCaptured      *persistedCapturedHeaders           `json:"latestCaptured,omitempty"`
	LastSuccessByServer map[string]persistedCapturedHeaders `json:"lastSuccessByServer,omitempty"`
}

type IdentityPersistence struct {
	mu     sync.Mutex
	path   string
	logger *Logger
}

var activeIdentityServiceState struct {
	mu       sync.RWMutex
	identity *ClientIdentityService
}

func NewIdentityPersistence(dataDir string, logger *Logger) *IdentityPersistence {
	if strings.TrimSpace(dataDir) == "" {
		dataDir = defaultDataDir()
	}
	return &IdentityPersistence{
		path:   filepath.Join(dataDir, "captured-headers.json"),
		logger: logger,
	}
}

func newIdentityPersistenceFromDetectedConfig() *IdentityPersistence {
	store, err := LoadConfigStore()
	if err != nil {
		return nil
	}
	cfg := store.Snapshot()
	return NewIdentityPersistence(cfg.DataDir, nil)
}

func StableUpstreamKey(upstream UpstreamConfig) string {
	baseURL := strings.TrimRight(strings.TrimSpace(upstream.URL), "/")
	name := strings.TrimSpace(upstream.Name)
	spoofClient := strings.TrimSpace(upstream.SpoofClient)
	if spoofClient == "" {
		spoofClient = "none"
	}
	return baseURL + "|" + name + "|" + spoofClient
}

func setActiveIdentityService(identity *ClientIdentityService) {
	activeIdentityServiceState.mu.Lock()
	defer activeIdentityServiceState.mu.Unlock()
	activeIdentityServiceState.identity = identity
}

func activeIdentityService() *ClientIdentityService {
	activeIdentityServiceState.mu.RLock()
	defer activeIdentityServiceState.mu.RUnlock()
	return activeIdentityServiceState.identity
}

func (p *IdentityPersistence) Load() (identityPersistenceSnapshot, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	snapshot, err := p.loadLocked()
	// Older builds wrote this file world-readable, and it holds the client
	// Authorization headers captured for passthrough mode. Tighten it on startup.
	if p != nil && strings.TrimSpace(p.path) != "" {
		_ = os.Chmod(p.path, PrivateFileMode())
	}
	return snapshot, err
}

func (p *IdentityPersistence) SaveLatestCaptured(headers http.Header, capturedAt string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	snapshot, err := p.loadLocked()
	if err != nil {
		p.warnf("load captured headers before saving latest failed: %v", err)
		return err
	}
	snapshot.Version = 1
	snapshot.LatestCaptured = &persistedCapturedHeaders{
		Headers:    normalizeCapturedHeaders(headers),
		CapturedAt: fallbackCapturedAt(capturedAt),
	}
	if err := p.saveLocked(snapshot); err != nil {
		p.warnf("save latest captured headers failed: %v", err)
		return err
	}
	return nil
}

func (p *IdentityPersistence) SaveLastSuccess(serverKey string, headers http.Header, capturedAt string) error {
	if strings.TrimSpace(serverKey) == "" {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	snapshot, err := p.loadLocked()
	if err != nil {
		p.warnf("load captured headers before saving last-success failed: %v", err)
		return err
	}
	snapshot.Version = 1
	if snapshot.LastSuccessByServer == nil {
		snapshot.LastSuccessByServer = map[string]persistedCapturedHeaders{}
	}
	snapshot.LastSuccessByServer[serverKey] = persistedCapturedHeaders{
		Headers:    normalizeCapturedHeaders(headers),
		CapturedAt: fallbackCapturedAt(capturedAt),
	}
	if err := p.saveLocked(snapshot); err != nil {
		p.warnf("save last-success headers failed: %v", err)
		return err
	}
	return nil
}

func (p *IdentityPersistence) loadLocked() (identityPersistenceSnapshot, error) {
	snapshot := identityPersistenceSnapshot{Version: 1, LastSuccessByServer: map[string]persistedCapturedHeaders{}}
	if p == nil || strings.TrimSpace(p.path) == "" {
		return snapshot, nil
	}
	raw, err := os.ReadFile(p.path)
	if err != nil {
		if os.IsNotExist(err) {
			return snapshot, nil
		}
		return snapshot, err
	}
	if len(raw) == 0 {
		return snapshot, nil
	}
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return snapshot, err
	}
	if snapshot.Version == 0 {
		snapshot.Version = 1
	}
	if snapshot.LastSuccessByServer == nil {
		snapshot.LastSuccessByServer = map[string]persistedCapturedHeaders{}
	}
	sanitizePersistenceSnapshot(&snapshot)
	return snapshot, nil
}

// sanitizePersistenceSnapshot drops credential-bearing headers from every entry
// the file holds. A capture file written by an older version stored the compound
// authorization header verbatim; loading it without cleanup would both forward
// those credentials and write them back on the next save.
func sanitizePersistenceSnapshot(snapshot *identityPersistenceSnapshot) {
	if snapshot == nil {
		return
	}
	if snapshot.LatestCaptured != nil {
		snapshot.LatestCaptured.Headers = normalizeCapturedHeaders(snapshot.LatestCaptured.Headers)
	}
	for serverKey, persisted := range snapshot.LastSuccessByServer {
		persisted.Headers = normalizeCapturedHeaders(persisted.Headers)
		snapshot.LastSuccessByServer[serverKey] = persisted
	}
}

func (p *IdentityPersistence) saveLocked(snapshot identityPersistenceSnapshot) error {
	if p == nil || strings.TrimSpace(p.path) == "" {
		return nil
	}
	if snapshot.LastSuccessByServer == nil {
		snapshot.LastSuccessByServer = map[string]persistedCapturedHeaders{}
	}
	if err := os.MkdirAll(filepath.Dir(p.path), 0o755); err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomically(p.path, encoded, PrivateFileMode())
}

func (p *IdentityPersistence) warnf(format string, args ...any) {
	if p != nil && p.logger != nil {
		p.logger.Warnf(format, args...)
	}
}

func fallbackCapturedAt(capturedAt string) string {
	if strings.TrimSpace(capturedAt) != "" {
		return capturedAt
	}
	return nowRFC3339()
}

func nowRFC3339() string {
	return time.Now().UTC().Format(time.RFC3339)
}
