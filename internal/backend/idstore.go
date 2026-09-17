package backend

import (
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type AdditionalInstance struct {
	OriginalID string
	ServerID   string
}

type ResolvedID struct {
	OriginalID     string
	ServerID       string
	OtherInstances []AdditionalInstance
}

type IDStoreStats struct {
	MappingCount int  `json:"mappingCount"`
	Persistent   bool `json:"persistent"`
}

type idEntry struct {
	OriginalID     string
	ServerID       string
	OtherInstances []AdditionalInstance
}

type activeStreamEntry struct {
	OriginalID string
	ServerID   string
	CreatedAt  time.Time
}

// activeStreamTTL bounds how long a recorded playback server stays valid.
const activeStreamTTL = 4 * time.Hour

type IDStore struct {
	mu         sync.RWMutex
	db         *sqliteDB
	persistent bool
	logger     *Logger

	virtualToOriginal   map[string]*idEntry
	originalToVirtual   map[string]string
	originalIDToVirtual map[string][]string          // originalID → virtual IDs known for it
	activeStreamServer  map[string]activeStreamEntry // virtualItemID → last-chosen server
}

func NewIDStore(dataDir string, logger *Logger, upstreamIDs ...string) (*IDStore, error) {
	if dataDir == "" {
		dataDir = defaultDataDir()
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, err
	}

	store := &IDStore{
		logger:              logger,
		virtualToOriginal:   map[string]*idEntry{},
		originalToVirtual:   map[string]string{},
		originalIDToVirtual: map[string][]string{},
		activeStreamServer:  map[string]activeStreamEntry{},
	}

	dbPath := filepath.Join(dataDir, "mappings.db")
	db, err := openSQLite(dbPath)
	if err != nil {
		if logger != nil {
			logger.Warnf("SQLite unavailable (%v), using in-memory ID store", err)
		}
		return store, nil
	}
	if err := migrateDatabaseToServerID(db, upstreamIDs, logger); err != nil {
		if logger != nil {
			logger.Errorf("SQLite database migration failed: %v", err)
		}
		_ = closeSQLite(db)
		return nil, fmt.Errorf("migrate database to server_id: %w", err)
	}
	if err := db.exec(`
        PRAGMA journal_mode = WAL;
        CREATE TABLE IF NOT EXISTS id_mappings (
            virtual_id TEXT PRIMARY KEY,
            original_id TEXT NOT NULL,
            server_id TEXT NOT NULL
        );
        CREATE INDEX IF NOT EXISTS idx_original ON id_mappings(original_id, server_id);
        CREATE TABLE IF NOT EXISTS id_additional_instances (
            virtual_id TEXT NOT NULL,
            original_id TEXT NOT NULL,
            server_id TEXT NOT NULL,
            UNIQUE(virtual_id, original_id, server_id)
        );
        CREATE INDEX IF NOT EXISTS idx_additional_virtual ON id_additional_instances(virtual_id);
    `); err != nil {
		_ = closeSQLite(db)
		return nil, err
	}
	store.db = db
	store.persistent = true
	// The database holds user password hashes and per-user watch history, and sqlite
	// creates its files with the process umask. WAL mode adds -wal/-shm siblings.
	chmodPrivate(dbPath, dbPath+"-wal", dbPath+"-shm")
	if err := store.load(); err != nil {
		_ = closeSQLite(db)
		return nil, err
	}
	if logger != nil {
		logger.Infof("SQLite ID store initialized: %d primary mapping(s), %d additional instance mapping(s) loaded", len(store.virtualToOriginal), store.additionalCount())
	}
	return store, nil
}

// DB returns the underlying database handle for shared access.
func (s *IDStore) DB() *sqliteDB {
	return s.db
}

func (s *IDStore) additionalCount() int {
	count := 0
	for _, entry := range s.virtualToOriginal {
		count += len(entry.OtherInstances)
	}
	return count
}

func (s *IDStore) load() error {
	if s.db == nil {
		return nil
	}
	stmt, err := s.db.prepare(`SELECT virtual_id, original_id, server_id FROM id_mappings`)
	if err != nil {
		return err
	}
	defer stmt.finalize()
	for {
		hasRow, err := stmt.step()
		if err != nil {
			return err
		}
		if !hasRow {
			break
		}
		virtualID := stmt.columnText(0)
		originalID := stmt.columnText(1)
		serverID := stmt.columnText(2)
		s.virtualToOriginal[virtualID] = &idEntry{OriginalID: originalID, ServerID: serverID, OtherInstances: []AdditionalInstance{}}
		s.originalToVirtual[compositeKey(originalID, serverID)] = virtualID
	}

	addStmt, err := s.db.prepare(`SELECT virtual_id, original_id, server_id FROM id_additional_instances`)
	if err != nil {
		return err
	}
	defer addStmt.finalize()
	for {
		hasRow, err := addStmt.step()
		if err != nil {
			return err
		}
		if !hasRow {
			break
		}
		virtualID := addStmt.columnText(0)
		originalID := addStmt.columnText(1)
		serverID := addStmt.columnText(2)
		if entry, ok := s.virtualToOriginal[virtualID]; ok {
			entry.OtherInstances = appendIfMissing(entry.OtherInstances, AdditionalInstance{OriginalID: originalID, ServerID: serverID})
			s.originalToVirtual[compositeKey(originalID, serverID)] = virtualID
		}
	}
	s.rebuildIndexesLocked()
	return nil
}

func (s *IDStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return closeSQLite(s.db)
}

func compositeKey(originalID, serverID string) string {
	return fmt.Sprintf("%s:%s", originalID, serverID)
}

func (s *IDStore) GetOrCreateVirtualID(originalID, serverID string) string {
	if originalID == "" {
		return originalID
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := compositeKey(originalID, serverID)
	if existing, ok := s.originalToVirtual[key]; ok {
		return existing
	}
	virtualID := randomHex(16)
	s.virtualToOriginal[virtualID] = &idEntry{OriginalID: originalID, ServerID: serverID, OtherInstances: []AdditionalInstance{}}
	s.originalToVirtual[key] = virtualID
	s.originalIDToVirtual[originalID] = append(s.originalIDToVirtual[originalID], virtualID)
	if s.db != nil {
		if err := s.db.writeParams(
			`INSERT OR IGNORE INTO id_mappings (virtual_id, original_id, server_id) VALUES (?, ?, ?)`,
			virtualID, originalID, serverID,
		); err != nil && s.logger != nil {
			// The in-memory mapping stays authoritative for this process; without the row
			// every client-held virtual ID for this item turns into a 404 after a restart.
			s.logger.Warnf("idstore: persist mapping %s -> %s@%s: %v", virtualID, originalID, serverID, err)
		}
	}
	return virtualID
}

func (s *IDStore) AssociateAdditionalInstance(virtualID, originalID, serverID string) {
	if virtualID == "" || originalID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.virtualToOriginal[virtualID]
	if !ok {
		return
	}
	if entry.OriginalID == originalID && entry.ServerID == serverID {
		return
	}
	entry.OtherInstances = appendIfMissing(entry.OtherInstances, AdditionalInstance{OriginalID: originalID, ServerID: serverID})
	s.originalToVirtual[compositeKey(originalID, serverID)] = virtualID
	if !containsString(s.originalIDToVirtual[originalID], virtualID) {
		s.originalIDToVirtual[originalID] = append(s.originalIDToVirtual[originalID], virtualID)
	}
	if s.db != nil {
		if err := s.db.writeParams(
			`INSERT OR IGNORE INTO id_additional_instances (virtual_id, original_id, server_id) VALUES (?, ?, ?)`,
			virtualID, originalID, serverID,
		); err != nil && s.logger != nil {
			s.logger.Warnf("idstore: persist additional instance %s -> %s@%s: %v", virtualID, originalID, serverID, err)
		}
	}
}

func appendIfMissing(instances []AdditionalInstance, candidate AdditionalInstance) []AdditionalInstance {
	for _, instance := range instances {
		if instance.OriginalID == candidate.OriginalID && instance.ServerID == candidate.ServerID {
			return instances
		}
	}
	return append(instances, candidate)
}

// ContainsVirtualID reports whether value is a virtual resource ID issued by this
// store. It answers the membership question inside the store's own lock instead of
// making every caller copy the whole mapping.
func (s *IDStore) ContainsVirtualID(value string) bool {
	if value == "" {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.virtualToOriginal[value]
	return ok
}

// ContainsOriginalID reports whether value is an upstream ID this store has mapped.
func (s *IDStore) ContainsOriginalID(value string) bool {
	if value == "" {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.originalIDToVirtual[value]) > 0
}

func (s *IDStore) ResolveVirtualID(virtualID string) *ResolvedID {
	s.mu.RLock()
	defer s.mu.RUnlock()
	entry, ok := s.virtualToOriginal[virtualID]
	if !ok {
		return nil
	}
	return &ResolvedID{
		OriginalID:     entry.OriginalID,
		ServerID:       entry.ServerID,
		OtherInstances: append([]AdditionalInstance(nil), entry.OtherInstances...),
	}
}

// ResolveByOriginalID finds a ResolvedID by original upstream ID.
// This handles clients that send raw upstream IDs instead of virtual IDs.
// ResolveByOriginalID finds a ResolvedID by original upstream ID. This handles clients
// that send raw upstream IDs instead of virtual IDs. The same upstream ID can be known to
// more than one server; the oldest mapping for it wins, which is the one the store issued
// first.
func (s *IDStore) ResolveByOriginalID(originalID string) *ResolvedID {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, virtualID := range s.originalIDToVirtual[originalID] {
		entry, ok := s.virtualToOriginal[virtualID]
		if !ok {
			continue
		}
		return &ResolvedID{
			OriginalID:     entry.OriginalID,
			ServerID:       entry.ServerID,
			OtherInstances: append([]AdditionalInstance(nil), entry.OtherInstances...),
		}
	}
	return nil
}

func containsString(values []string, candidate string) bool {
	for _, value := range values {
		if value == candidate {
			return true
		}
	}
	return false
}

func (s *IDStore) Stats() IDStoreStats {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return IDStoreStats{MappingCount: len(s.virtualToOriginal), Persistent: s.persistent}
}

// evictExpiredStreamState drops active-stream records that outlived their TTL.
func (s *IDStore) evictExpiredStreamState() {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for k, entry := range s.activeStreamServer {
		if now.Sub(entry.CreatedAt) > activeStreamTTL {
			delete(s.activeStreamServer, k)
		}
	}
}

// RemoveByServerID removes every mapping belonging to the deleted upstream,
// both primary entries registered for it and additional-instance entries on surviving primaries.
// It cascades to drop additional instances belonging to primaries on the deleted server.
func (s *IDStore) RemoveByServerID(serverID string) error {
	if serverID == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.db != nil {
		err := s.db.withWriteTx(func() error {
			// 1. Cascade cleanup: delete additional instances owned by primaries that belong to this server.
			if err := s.db.execParams(
				`DELETE FROM id_additional_instances WHERE virtual_id IN (SELECT virtual_id FROM id_mappings WHERE server_id = ?)`,
				serverID,
			); err != nil {
				return err
			}
			// 2. Delete additional instances that point directly to this server.
			if err := s.db.execParams(`DELETE FROM id_additional_instances WHERE server_id = ?`, serverID); err != nil {
				return err
			}
			// 3. Delete primary mappings for this server.
			return s.db.execParams(`DELETE FROM id_mappings WHERE server_id = ?`, serverID)
		})
		if err != nil {
			return err
		}
	}

	for virtualID, entry := range s.virtualToOriginal {
		if entry.ServerID == serverID {
			delete(s.virtualToOriginal, virtualID)
			continue
		}
		kept := entry.OtherInstances[:0]
		for _, other := range entry.OtherInstances {
			if other.ServerID == serverID {
				continue
			}
			kept = append(kept, other)
		}
		entry.OtherInstances = kept
	}
	s.rebuildIndexesLocked()
	return nil
}

// rebuildIndexesLocked recomputes both derived indexes from virtualToOriginal.
func (s *IDStore) rebuildIndexesLocked() {
	rebuilt := make(map[string]string, len(s.originalToVirtual))
	byOriginal := make(map[string][]string, len(s.originalIDToVirtual))
	for virtualID, entry := range s.virtualToOriginal {
		rebuilt[compositeKey(entry.OriginalID, entry.ServerID)] = virtualID
		byOriginal[entry.OriginalID] = append(byOriginal[entry.OriginalID], virtualID)
		for _, other := range entry.OtherInstances {
			rebuilt[compositeKey(other.OriginalID, other.ServerID)] = virtualID
			if !containsString(byOriginal[other.OriginalID], virtualID) {
				byOriginal[other.OriginalID] = append(byOriginal[other.OriginalID], virtualID)
			}
		}
	}
	s.originalToVirtual = rebuilt
	s.originalIDToVirtual = byOriginal
}

func randomHex(byteLen int) string {
	bytes := make([]byte, byteLen)
	if _, err := rand.Read(bytes); err != nil {
		// Every virtual ID, proxy user ID and session token is derived from this. A
		// zero-filled fallback would hand out predictable identifiers, and no caller can
		// do anything meaningful with the failure.
		panic("crypto/rand unavailable: " + err.Error())
	}
	return fmt.Sprintf("%x", bytes)
}

func (s *IDStore) SetActiveStream(virtualItemID string, serverID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.activeStreamServer[virtualItemID] = activeStreamEntry{ServerID: serverID, CreatedAt: time.Now()}
}

func (s *IDStore) GetActiveStream(virtualItemID string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	entry, ok := s.activeStreamServer[virtualItemID]
	if !ok {
		return "", false
	}
	if time.Since(entry.CreatedAt) > activeStreamTTL {
		return "", false
	}
	return entry.ServerID, true
}
