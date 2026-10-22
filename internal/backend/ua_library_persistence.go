package backend

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// NewUALibraryStore loads the UA library from dataDir. A missing file is an empty
// library. A file that cannot be parsed, or that carries an unsupported version,
// is reported as an error and kept on disk untouched: the application keeps
// serving passthrough/custom, and UA management answers 503 instead of silently
// wiping the administrator's resident identities.
func NewUALibraryStore(dataDir string, logger *Logger) (*UALibraryStore, error) {
	if strings.TrimSpace(dataDir) == "" {
		dataDir = defaultDataDir()
	}
	store := &UALibraryStore{
		path:     filepath.Join(dataDir, "ua-library.json"),
		logger:   logger,
		snapshot: uaLibrarySnapshot{Version: 1, Recent: []RecentUA{}, Resident: []ResidentUA{}},
	}
	setActiveUALibrary(store)

	raw, err := os.ReadFile(store.path)
	if err != nil {
		if os.IsNotExist(err) {
			return store, nil
		}
		store.loadErr = err
		return store, err
	}
	if len(raw) == 0 {
		return store, nil
	}
	var snapshot uaLibrarySnapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		store.loadErr = err
		return store, fmt.Errorf("ua library %s is unreadable: %w", store.path, err)
	}
	if snapshot.Version != 1 {
		store.loadErr = fmt.Errorf("unsupported ua library version %d", snapshot.Version)
		return store, store.loadErr
	}
	if snapshot.Recent == nil {
		snapshot.Recent = []RecentUA{}
	}
	if snapshot.Resident == nil {
		snapshot.Resident = []ResidentUA{}
	}
	store.snapshot = snapshot
	_ = os.Chmod(store.path, PrivateFileMode())
	return store, nil
}

// persistLocked writes a candidate snapshot through an atomic file replace. The
// caller holds mu; the in-memory snapshot is published only after this succeeds.
func (s *UALibraryStore) persistLocked(snapshot uaLibrarySnapshot) error {
	if s == nil || strings.TrimSpace(s.path) == "" {
		return nil
	}
	snapshot.Version = 1
	encoded, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return err
	}
	return WriteFileAtomic(s.path, encoded, PrivateFileMode())
}

func (s *UALibraryStore) warnf(format string, args ...any) {
	if s != nil && s.logger != nil {
		s.logger.Warnf(format, args...)
	}
}
