package backend

import (
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
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

type imageSourceEntry struct {
	ServerID   string
	OriginalID string
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
	imageSources        map[string]imageSourceEntry  // virtualID|type|tag → real image source
	displaySources      map[string]imageSourceEntry  // virtualID → display winner's real instance
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
		imageSources:        map[string]imageSourceEntry{},
		displaySources:      map[string]imageSourceEntry{},
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
	// Persist first: a mapping that only ever lives in memory turns every client-held
	// virtual ID for this item into a 404 after a restart. The row is written before
	// the in-memory maps are touched, so the durable store is updated first and a
	// failure is handled deliberately instead of leaving memory permanently ahead of
	// the database.
	if s.db != nil {
		if err := s.db.writeParams(
			`INSERT OR IGNORE INTO id_mappings (virtual_id, original_id, server_id) VALUES (?, ?, ?)`,
			virtualID, originalID, serverID,
		); err != nil {
			if s.logger != nil {
				// Degraded but still consistent within this process: the mapping is
				// registered below so the item keeps resolving until the process ends.
				// What is lost is durability — the ID will not survive a restart.
				s.logger.Warnf("idstore: persist mapping %s -> %s@%s: %v", virtualID, originalID, serverID, err)
			}
		}
	}
	s.virtualToOriginal[virtualID] = &idEntry{OriginalID: originalID, ServerID: serverID, OtherInstances: []AdditionalInstance{}}
	s.originalToVirtual[key] = virtualID
	s.originalIDToVirtual[originalID] = append(s.originalIDToVirtual[originalID], virtualID)
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
	// Persist first, for the same reason as GetOrCreateVirtualID: the durable store is
	// updated before any in-memory map, so a failure cannot leave memory ahead of it.
	if s.db != nil {
		if err := s.db.writeParams(
			`INSERT OR IGNORE INTO id_additional_instances (virtual_id, original_id, server_id) VALUES (?, ?, ?)`,
			virtualID, originalID, serverID,
		); err != nil && s.logger != nil {
			s.logger.Warnf("idstore: persist additional instance %s -> %s@%s: %v", virtualID, originalID, serverID, err)
		}
	}
	entry.OtherInstances = appendIfMissing(entry.OtherInstances, AdditionalInstance{OriginalID: originalID, ServerID: serverID})
	s.originalToVirtual[compositeKey(originalID, serverID)] = virtualID
	if !containsString(s.originalIDToVirtual[originalID], virtualID) {
		s.originalIDToVirtual[originalID] = append(s.originalIDToVirtual[originalID], virtualID)
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

// AliasVirtualIDs finds previously published IDs connected by a verified
// (original ID, server ID) pair. Bare original-ID collisions are not aliases.
// Existing primary and additional-instance rows make this restart-compatible.
func (s *IDStore) AliasVirtualIDs(virtualID string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	seen := map[string]bool{virtualID: true}
	queue := []string{virtualID}
	for i := 0; i < len(queue); i++ {
		entry := s.virtualToOriginal[queue[i]]
		if entry == nil {
			continue
		}
		pairs := append([]AdditionalInstance{{OriginalID: entry.OriginalID, ServerID: entry.ServerID}}, entry.OtherInstances...)
		for _, pair := range pairs {
			for _, candidate := range s.originalIDToVirtual[pair.OriginalID] {
				if seen[candidate] {
					continue
				}
				other := s.virtualToOriginal[candidate]
				if other == nil {
					continue
				}
				shared := other.OriginalID == pair.OriginalID && other.ServerID == pair.ServerID
				for _, instance := range other.OtherInstances {
					shared = shared || instance == pair
				}
				if shared {
					seen[candidate] = true
					queue = append(queue, candidate)
				}
			}
		}
	}
	sort.Strings(queue[1:])
	return queue
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

type serverIDRemovalGroup struct {
	virtualID     string
	primaryTarget bool
	remove        bool
	route         AdditionalInstance
}

// RemoveByServerID removes an upstream's instances while retaining each virtual
// identity that still has a surviving copy. Durable mappings and watch-state
// rehoming commit together; in-memory indexes and routing hints change only after
// that transaction succeeds.
func (s *IDStore) RemoveByServerID(serverID string) error {
	if serverID == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	virtualIDs := make([]string, 0, len(s.virtualToOriginal))
	for virtualID := range s.virtualToOriginal {
		virtualIDs = append(virtualIDs, virtualID)
	}
	sort.Strings(virtualIDs)
	nextEntries := make(map[string]*idEntry, len(s.virtualToOriginal))
	removedVirtualIDs := map[string]bool{}
	groups := make([]serverIDRemovalGroup, 0)
	for _, virtualID := range virtualIDs {
		entry := s.virtualToOriginal[virtualID]
		next := &idEntry{
			OriginalID:     entry.OriginalID,
			ServerID:       entry.ServerID,
			OtherInstances: append([]AdditionalInstance(nil), entry.OtherInstances...),
		}
		if entry.ServerID == serverID {
			survivors := make([]AdditionalInstance, 0, len(entry.OtherInstances))
			for _, other := range entry.OtherInstances {
				if other.ServerID != serverID {
					survivors = append(survivors, other)
				}
			}
			if len(survivors) == 0 {
				removedVirtualIDs[virtualID] = true
				groups = append(groups, serverIDRemovalGroup{virtualID: virtualID, primaryTarget: true, remove: true})
				continue
			}
			sort.Slice(survivors, func(i, j int) bool {
				if survivors[i].ServerID != survivors[j].ServerID {
					return survivors[i].ServerID < survivors[j].ServerID
				}
				return survivors[i].OriginalID < survivors[j].OriginalID
			})
			promoted := survivors[0]
			next.OriginalID, next.ServerID = promoted.OriginalID, promoted.ServerID
			next.OtherInstances = append([]AdditionalInstance(nil), survivors[1:]...)
			groups = append(groups, serverIDRemovalGroup{virtualID: virtualID, primaryTarget: true, route: promoted})
		} else {
			kept := make([]AdditionalInstance, 0, len(entry.OtherInstances))
			removedTargetInstance := false
			for _, other := range entry.OtherInstances {
				if other.ServerID == serverID {
					removedTargetInstance = true
					continue
				}
				kept = append(kept, other)
			}
			if removedTargetInstance {
				next.OtherInstances = kept
				groups = append(groups, serverIDRemovalGroup{
					virtualID: virtualID,
					route:     AdditionalInstance{OriginalID: entry.OriginalID, ServerID: entry.ServerID},
				})
			}
		}
		nextEntries[virtualID] = next
	}

	if s.db != nil {
		if err := s.db.withWriteTx(func() error {
			hasWatchTable, err := tableExists(s.db, "user_watch_progress")
			if err != nil {
				return err
			}
			if hasWatchTable {
				for _, group := range groups {
					if group.remove {
						if err := s.db.execParams(`DELETE FROM user_watch_progress WHERE virtual_item_id = ?`, group.virtualID); err != nil {
							return err
						}
						continue
					}
					if err := s.db.execParams(`
						UPDATE user_watch_progress
						SET server_id = ?, original_item_id = ?
						WHERE virtual_item_id = ? AND server_id = ?`,
						group.route.ServerID, group.route.OriginalID, group.virtualID, serverID,
					); err != nil {
						return err
					}
				}
				for _, group := range groups {
					if !group.remove {
						if err := s.db.execParams(`
							UPDATE user_watch_progress SET series_original_id = ?
							WHERE series_virtual_id = ?`, group.route.OriginalID, group.virtualID); err != nil {
							return err
						}
					}
				}
				// Remove any stale/orphan watch rows that did not correspond to a
				// surviving identity group. Local-only rows for surviving virtual IDs
				// remain attached to those IDs.
				if err := s.db.execParams(`DELETE FROM user_watch_progress WHERE server_id = ?`, serverID); err != nil {
					return err
				}
			}

			for _, group := range groups {
				if !group.primaryTarget {
					continue
				}
				if group.remove {
					if err := s.db.execParams(`DELETE FROM id_additional_instances WHERE virtual_id = ?`, group.virtualID); err != nil {
						return err
					}
					if err := s.db.execParams(`DELETE FROM id_mappings WHERE virtual_id = ?`, group.virtualID); err != nil {
						return err
					}
					continue
				}
				if err := s.db.execParams(`
					UPDATE id_mappings SET original_id = ?, server_id = ? WHERE virtual_id = ?`,
					group.route.OriginalID, group.route.ServerID, group.virtualID,
				); err != nil {
					return err
				}
				if err := s.db.execParams(`
					DELETE FROM id_additional_instances
					WHERE virtual_id = ? AND original_id = ? AND server_id = ?`,
					group.virtualID, group.route.OriginalID, group.route.ServerID,
				); err != nil {
					return err
				}
			}
			if err := s.db.execParams(`DELETE FROM id_additional_instances WHERE server_id = ?`, serverID); err != nil {
				return err
			}
			return s.db.execParams(`DELETE FROM id_mappings WHERE server_id = ?`, serverID)
		}); err != nil {
			return err
		}
	}

	s.virtualToOriginal = nextEntries
	s.rebuildIndexesLocked()
	for virtualID, stream := range s.activeStreamServer {
		if stream.ServerID == serverID || removedVirtualIDs[virtualID] {
			delete(s.activeStreamServer, virtualID)
		}
	}
	for key, source := range s.imageSources {
		virtualID, _, _ := strings.Cut(key, "|")
		if source.ServerID == serverID || removedVirtualIDs[virtualID] {
			delete(s.imageSources, key)
		}
	}
	for virtualID, source := range s.displaySources {
		if source.ServerID == serverID || removedVirtualIDs[virtualID] {
			delete(s.displaySources, virtualID)
		}
	}
	return nil
}

// rebuildIndexesLocked recomputes both derived indexes from virtualToOriginal.
// Sorting makes duplicate historical alias pairs resolve deterministically.
func (s *IDStore) rebuildIndexesLocked() {
	virtualIDs := make([]string, 0, len(s.virtualToOriginal))
	for virtualID := range s.virtualToOriginal {
		virtualIDs = append(virtualIDs, virtualID)
	}
	sort.Strings(virtualIDs)
	rebuilt := make(map[string]string, len(s.originalToVirtual))
	byOriginal := make(map[string][]string, len(s.originalIDToVirtual))
	for _, virtualID := range virtualIDs {
		entry := s.virtualToOriginal[virtualID]
		primaryKey := compositeKey(entry.OriginalID, entry.ServerID)
		if _, exists := rebuilt[primaryKey]; !exists {
			rebuilt[primaryKey] = virtualID
		}
		byOriginal[entry.OriginalID] = append(byOriginal[entry.OriginalID], virtualID)
		for _, other := range entry.OtherInstances {
			key := compositeKey(other.OriginalID, other.ServerID)
			if _, exists := rebuilt[key]; !exists {
				rebuilt[key] = virtualID
			}
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

// imageSourceKey identifies one (item, image type, tag) image-source record.
func imageSourceKey(virtualID, imageType, tag string) string {
	return virtualID + "|" + imageType + "|" + tag
}

// SetImageSource records which upstream instance actually owns the image bytes a
// virtual item advertises. The merged DTO can borrow a sibling's poster tag while
// keeping its own virtual id (plan §6.4.1), so the image handler needs this to
// fetch from the server that really has the image instead of the primary that 404s.
func (s *IDStore) SetImageSource(virtualID, imageType, tag, serverID, originalID string) {
	if virtualID == "" || tag == "" || serverID == "" || originalID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.imageSources == nil {
		s.imageSources = map[string]imageSourceEntry{}
	}
	s.imageSources[imageSourceKey(virtualID, imageType, tag)] = imageSourceEntry{ServerID: serverID, OriginalID: originalID}
}

// ResolveImageSource returns the recorded real source for a virtual item's image.
func (s *IDStore) ResolveImageSource(virtualID, imageType, tag string) (AdditionalInstance, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	entry, ok := s.imageSources[imageSourceKey(virtualID, imageType, tag)]
	if !ok {
		return AdditionalInstance{}, false
	}
	return AdditionalInstance{OriginalID: entry.OriginalID, ServerID: entry.ServerID}, true
}

// SetDisplaySource records which real upstream instance supplied the display
// metadata (and therefore the non-Primary image tags) for a virtual item. The
// merged DTO can be served from a server other than the IDStore primary instance,
// so the image handler must route Backdrop/Logo/Thumb to this winner instead of
// the primary, which does not own those tags (plan §6.4.2). The source is only a
// server-side fetch hint; it is never a permission grant.
func (s *IDStore) SetDisplaySource(virtualID, serverID, originalID string) {
	if virtualID == "" || serverID == "" || originalID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.displaySources == nil {
		s.displaySources = map[string]imageSourceEntry{}
	}
	s.displaySources[virtualID] = imageSourceEntry{ServerID: serverID, OriginalID: originalID}
}

// ResolveDisplaySource returns the recorded display-winning instance for a
// virtual item, if one has been recorded by a merge.
func (s *IDStore) ResolveDisplaySource(virtualID string) (AdditionalInstance, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	entry, ok := s.displaySources[virtualID]
	if !ok {
		return AdditionalInstance{}, false
	}
	return AdditionalInstance{OriginalID: entry.OriginalID, ServerID: entry.ServerID}, true
}
