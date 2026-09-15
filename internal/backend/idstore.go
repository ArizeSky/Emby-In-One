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
	OriginalID  string
	ServerIndex int
}

type ResolvedID struct {
	OriginalID     string
	ServerIndex    int
	OtherInstances []AdditionalInstance
}

type IDStoreStats struct {
	MappingCount int  `json:"mappingCount"`
	Persistent   bool `json:"persistent"`
}

type idEntry struct {
	OriginalID     string
	ServerIndex    int
	OtherInstances []AdditionalInstance
}

type activeStreamEntry struct {
	OriginalID  string
	ServerIndex int
	CreatedAt   time.Time
}

// activeStreamTTL bounds how long a recorded playback server stays valid.
const activeStreamTTL = 4 * time.Hour

type IDStore struct {
	mu         sync.RWMutex
	db         *sqliteDB
	persistent bool
	logger     *Logger

	virtualToOriginal  map[string]*idEntry
	originalToVirtual  map[string]string
	activeStreamServer map[string]activeStreamEntry // virtualItemID → last-chosen server
}

func NewIDStore(dataDir string, logger *Logger) (*IDStore, error) {
	if dataDir == "" {
		dataDir = defaultDataDir()
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, err
	}

	store := &IDStore{
		logger:             logger,
		virtualToOriginal:  map[string]*idEntry{},
		originalToVirtual:  map[string]string{},
		activeStreamServer: map[string]activeStreamEntry{},
	}

	dbPath := filepath.Join(dataDir, "mappings.db")
	db, err := openSQLite(dbPath)
	if err != nil {
		if logger != nil {
			logger.Warnf("SQLite unavailable (%v), using in-memory ID store", err)
		}
		return store, nil
	}
	if err := db.exec(`
        PRAGMA journal_mode = WAL;
        CREATE TABLE IF NOT EXISTS id_mappings (
            virtual_id TEXT PRIMARY KEY,
            original_id TEXT NOT NULL,
            server_index INTEGER NOT NULL
        );
        CREATE INDEX IF NOT EXISTS idx_original ON id_mappings(original_id, server_index);
        CREATE TABLE IF NOT EXISTS id_additional_instances (
            virtual_id TEXT NOT NULL,
            original_id TEXT NOT NULL,
            server_index INTEGER NOT NULL,
            UNIQUE(virtual_id, original_id, server_index)
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
	stmt, err := s.db.prepare(`SELECT virtual_id, original_id, server_index FROM id_mappings`)
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
		serverIndex := stmt.columnInt(2)
		s.virtualToOriginal[virtualID] = &idEntry{OriginalID: originalID, ServerIndex: serverIndex, OtherInstances: []AdditionalInstance{}}
		s.originalToVirtual[compositeKey(originalID, serverIndex)] = virtualID
	}

	addStmt, err := s.db.prepare(`SELECT virtual_id, original_id, server_index FROM id_additional_instances`)
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
		serverIndex := addStmt.columnInt(2)
		if entry, ok := s.virtualToOriginal[virtualID]; ok {
			entry.OtherInstances = appendIfMissing(entry.OtherInstances, AdditionalInstance{OriginalID: originalID, ServerIndex: serverIndex})
			s.originalToVirtual[compositeKey(originalID, serverIndex)] = virtualID
		}
	}
	return nil
}

func (s *IDStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return closeSQLite(s.db)
}

func compositeKey(originalID string, serverIndex int) string {
	return fmt.Sprintf("%s:%d", originalID, serverIndex)
}

func (s *IDStore) GetOrCreateVirtualID(originalID string, serverIndex int) string {
	if originalID == "" {
		return originalID
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := compositeKey(originalID, serverIndex)
	if existing, ok := s.originalToVirtual[key]; ok {
		return existing
	}
	virtualID := randomHex(16)
	s.virtualToOriginal[virtualID] = &idEntry{OriginalID: originalID, ServerIndex: serverIndex, OtherInstances: []AdditionalInstance{}}
	s.originalToVirtual[key] = virtualID
	if s.db != nil {
		stmt, err := s.db.prepare(`INSERT OR IGNORE INTO id_mappings (virtual_id, original_id, server_index) VALUES (?, ?, ?)`)
		if err == nil {
			_ = stmt.bindAll(virtualID, originalID, serverIndex)
			_, _ = stmt.step()
			stmt.finalize()
		}
	}
	return virtualID
}

func (s *IDStore) AssociateAdditionalInstance(virtualID, originalID string, serverIndex int) {
	if virtualID == "" || originalID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.virtualToOriginal[virtualID]
	if !ok {
		return
	}
	if entry.OriginalID == originalID && entry.ServerIndex == serverIndex {
		return
	}
	entry.OtherInstances = appendIfMissing(entry.OtherInstances, AdditionalInstance{OriginalID: originalID, ServerIndex: serverIndex})
	s.originalToVirtual[compositeKey(originalID, serverIndex)] = virtualID
	if s.db != nil {
		stmt, err := s.db.prepare(`INSERT OR IGNORE INTO id_additional_instances (virtual_id, original_id, server_index) VALUES (?, ?, ?)`)
		if err == nil {
			_ = stmt.bindAll(virtualID, originalID, serverIndex)
			_, _ = stmt.step()
			stmt.finalize()
		}
	}
}

func appendIfMissing(instances []AdditionalInstance, candidate AdditionalInstance) []AdditionalInstance {
	for _, instance := range instances {
		if instance.OriginalID == candidate.OriginalID && instance.ServerIndex == candidate.ServerIndex {
			return instances
		}
	}
	return append(instances, candidate)
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
		ServerIndex:    entry.ServerIndex,
		OtherInstances: append([]AdditionalInstance(nil), entry.OtherInstances...),
	}
}

// ResolveByOriginalID finds a ResolvedID by original upstream ID.
// This handles clients that send raw upstream IDs instead of virtual IDs.
func (s *IDStore) ResolveByOriginalID(originalID string) *ResolvedID {
	s.mu.RLock()
	defer s.mu.RUnlock()
	// Scan virtualToOriginal since composite keys include serverIndex
	for _, entry := range s.virtualToOriginal {
		if entry.OriginalID == originalID {
			return &ResolvedID{
				OriginalID:     entry.OriginalID,
				ServerIndex:    entry.ServerIndex,
				OtherInstances: append([]AdditionalInstance(nil), entry.OtherInstances...),
			}
		}
	}
	return nil
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

func (s *IDStore) RemoveByServerIndex(serverIndex int) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	removed := 0
	keysToDelete := []string{}
	for virtualID, entry := range s.virtualToOriginal {
		if entry.ServerIndex == serverIndex {
			keysToDelete = append(keysToDelete, virtualID)
		}
	}
	for _, virtualID := range keysToDelete {
		entry := s.virtualToOriginal[virtualID]
		delete(s.originalToVirtual, compositeKey(entry.OriginalID, entry.ServerIndex))
		delete(s.virtualToOriginal, virtualID)
		removed++
		if s.db != nil {
			stmt, err := s.db.prepare(`DELETE FROM id_additional_instances WHERE virtual_id = ?`)
			if err == nil {
				if bindErr := stmt.bindAll(virtualID); bindErr != nil && s.logger != nil {
					s.logger.Warnf("idstore: bind delete virtual_id=%s: %v", virtualID, bindErr)
				} else if _, stepErr := stmt.step(); stepErr != nil && s.logger != nil {
					s.logger.Warnf("idstore: delete virtual_id=%s: %v", virtualID, stepErr)
				}
				stmt.finalize()
			} else if s.logger != nil {
				s.logger.Warnf("idstore: prepare delete: %v", err)
			}
		}
	}
	for _, entry := range s.virtualToOriginal {
		filtered := make([]AdditionalInstance, 0, len(entry.OtherInstances))
		for _, instance := range entry.OtherInstances {
			if instance.ServerIndex != serverIndex {
				filtered = append(filtered, instance)
			}
		}
		entry.OtherInstances = filtered
	}
	if s.db != nil {
		if stmt, err := s.db.prepare(`DELETE FROM id_mappings WHERE server_index = ?`); err == nil {
			if bindErr := stmt.bindAll(serverIndex); bindErr != nil && s.logger != nil {
				s.logger.Warnf("idstore: bind delete mappings server_index=%d: %v", serverIndex, bindErr)
			} else if _, stepErr := stmt.step(); stepErr != nil && s.logger != nil {
				s.logger.Warnf("idstore: delete mappings server_index=%d: %v", serverIndex, stepErr)
			}
			stmt.finalize()
		} else if s.logger != nil {
			s.logger.Warnf("idstore: prepare delete mappings: %v", err)
		}
		if stmt, err := s.db.prepare(`DELETE FROM id_additional_instances WHERE server_index = ?`); err == nil {
			if bindErr := stmt.bindAll(serverIndex); bindErr != nil && s.logger != nil {
				s.logger.Warnf("idstore: bind delete additional server_index=%d: %v", serverIndex, bindErr)
			} else if _, stepErr := stmt.step(); stepErr != nil && s.logger != nil {
				s.logger.Warnf("idstore: delete additional server_index=%d: %v", serverIndex, stepErr)
			}
			stmt.finalize()
		} else if s.logger != nil {
			s.logger.Warnf("idstore: prepare delete additional: %v", err)
		}
	}
	return removed
}

func (s *IDStore) ReorderServerIndices(fromIndex, toIndex int) {
	if fromIndex == toIndex {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	newOriginalToVirtual := map[string]string{}
	for virtualID, entry := range s.virtualToOriginal {
		entry.ServerIndex = reorderServerIndex(entry.ServerIndex, fromIndex, toIndex)
		newOriginalToVirtual[compositeKey(entry.OriginalID, entry.ServerIndex)] = virtualID
		for i := range entry.OtherInstances {
			entry.OtherInstances[i].ServerIndex = reorderServerIndex(entry.OtherInstances[i].ServerIndex, fromIndex, toIndex)
		}
	}
	s.originalToVirtual = newOriginalToVirtual
	if err := s.persistAllLocked(); err != nil && s.logger != nil {
		s.logger.Warnf("failed to persist reordered ID mappings: %v", err)
	}
}

func (s *IDStore) persistAllLocked() error {
	if s.db == nil {
		return nil
	}
	if err := s.db.exec(`BEGIN IMMEDIATE`); err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = s.db.exec(`ROLLBACK`)
		}
	}()
	if err := s.db.exec(`DELETE FROM id_additional_instances; DELETE FROM id_mappings;`); err != nil {
		return err
	}
	for virtualID, entry := range s.virtualToOriginal {
		stmt, err := s.db.prepare(`INSERT INTO id_mappings (virtual_id, original_id, server_index) VALUES (?, ?, ?)`)
		if err != nil {
			return err
		}
		if err := stmt.bindAll(virtualID, entry.OriginalID, entry.ServerIndex); err != nil {
			stmt.finalize()
			return err
		}
		if _, err := stmt.step(); err != nil {
			stmt.finalize()
			return err
		}
		stmt.finalize()
		for _, other := range entry.OtherInstances {
			addStmt, err := s.db.prepare(`INSERT INTO id_additional_instances (virtual_id, original_id, server_index) VALUES (?, ?, ?)`)
			if err != nil {
				return err
			}
			if err := addStmt.bindAll(virtualID, other.OriginalID, other.ServerIndex); err != nil {
				addStmt.finalize()
				return err
			}
			if _, err := addStmt.step(); err != nil {
				addStmt.finalize()
				return err
			}
			addStmt.finalize()
		}
	}
	if err := s.db.exec(`COMMIT`); err != nil {
		return err
	}
	committed = true
	return nil
}
func (s *IDStore) ShiftServerIndices(deletedIndex int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for virtualID, entry := range s.virtualToOriginal {
		if entry.ServerIndex > deletedIndex {
			delete(s.originalToVirtual, compositeKey(entry.OriginalID, entry.ServerIndex))
			entry.ServerIndex--
			s.originalToVirtual[compositeKey(entry.OriginalID, entry.ServerIndex)] = virtualID
		}
		for i := range entry.OtherInstances {
			if entry.OtherInstances[i].ServerIndex > deletedIndex {
				entry.OtherInstances[i].ServerIndex--
			}
		}
	}
	if s.db != nil {
		if stmt, err := s.db.prepare(`UPDATE id_mappings SET server_index = server_index - 1 WHERE server_index > ?`); err == nil {
			if bindErr := stmt.bindAll(deletedIndex); bindErr != nil && s.logger != nil {
				s.logger.Warnf("idstore: bind shift mappings: %v", bindErr)
			} else if _, stepErr := stmt.step(); stepErr != nil && s.logger != nil {
				s.logger.Warnf("idstore: shift mappings: %v", stepErr)
			}
			stmt.finalize()
		} else if s.logger != nil {
			s.logger.Warnf("idstore: prepare shift mappings: %v", err)
		}
		if stmt, err := s.db.prepare(`UPDATE id_additional_instances SET server_index = server_index - 1 WHERE server_index > ?`); err == nil {
			if bindErr := stmt.bindAll(deletedIndex); bindErr != nil && s.logger != nil {
				s.logger.Warnf("idstore: bind shift additional: %v", bindErr)
			} else if _, stepErr := stmt.step(); stepErr != nil && s.logger != nil {
				s.logger.Warnf("idstore: shift additional: %v", stepErr)
			}
			stmt.finalize()
		} else if s.logger != nil {
			s.logger.Warnf("idstore: prepare shift additional: %v", err)
		}
	}
}

func reorderServerIndex(index, fromIndex, toIndex int) int {
	if fromIndex == toIndex {
		return index
	}
	if index == fromIndex {
		return toIndex
	}
	if fromIndex < toIndex {
		if index > fromIndex && index <= toIndex {
			return index - 1
		}
		return index
	}
	if index >= toIndex && index < fromIndex {
		return index + 1
	}
	return index
}
func randomHex(byteLen int) string {
	bytes := make([]byte, byteLen)
	_, _ = rand.Read(bytes)
	return fmt.Sprintf("%x", bytes)
}

func (s *IDStore) SetActiveStream(virtualItemID string, serverIndex int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.activeStreamServer[virtualItemID] = activeStreamEntry{ServerIndex: serverIndex, CreatedAt: time.Now()}
}

func (s *IDStore) GetActiveStream(virtualItemID string) (int, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	entry, ok := s.activeStreamServer[virtualItemID]
	if !ok {
		return 0, false
	}
	if time.Since(entry.CreatedAt) > activeStreamTTL {
		return 0, false
	}
	return entry.ServerIndex, true
}
