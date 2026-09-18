package backend

import (
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
)

// adminVisibilityUserID is the reserved user_id under which the administrator's
// own home-library hiding config is stored. Regular user IDs are randomHex(16)
// (32 lowercase hex chars), so this constant can never collide with one, and
// unlike the admin's proxyUserID it survives tokens.json being rebuilt.
const adminVisibilityUserID = "__admin__"

// hiddenLibrarySnapshot is the immutable read view: userID → serverID → set of
// hidden library IDs. Snapshots are swapped wholesale by writers; every map in
// a published snapshot is immutable forever after.
type hiddenLibrarySnapshot = map[string]map[string]map[string]struct{}

// HiddenLibraryStore persists which home libraries each user has hidden.
// Reads go through an atomic snapshot pointer (zero-lock); writes serialize
// on writeMu, commit to SQLite first, then publish a copy-on-write snapshot.
// Maps handed to callers are shared with the snapshot and must be treated
// as read-only.
type HiddenLibraryStore struct {
	snapshot atomic.Pointer[hiddenLibrarySnapshot]
	writeMu  sync.Mutex
	db       *sqliteDB
	logger    *Logger
}

func NewHiddenLibraryStore(db *sqliteDB, logger *Logger) (*HiddenLibraryStore, error) {
	if db == nil {
		return nil, fmt.Errorf("hidden library store: SQLite database handle is nil")
	}
	// No secondary index: the composite primary key already serves the
	// user_id-prefixed lookups every read performs.
	if err := db.exec(`
		CREATE TABLE IF NOT EXISTS user_hidden_libraries (
			user_id TEXT NOT NULL,
			server_id TEXT NOT NULL,
			library_id TEXT NOT NULL,
			PRIMARY KEY (user_id, server_id, library_id)
		);
	`); err != nil {
		return nil, fmt.Errorf("hidden library store: create table: %w", err)
	}
	store := &HiddenLibraryStore{db: db, logger: logger}
	if err := store.load(); err != nil {
		return nil, fmt.Errorf("hidden library store: load: %w", err)
	}
	if logger != nil {
		logger.Infof("HiddenLibraryStore initialized")
	}
	return store, nil
}

// visibilityUserID maps a token's identity to the user_id used by this store:
// the administrator's config lives under the reserved constant, regular users
// under their stable token UserID.
func visibilityUserID(proxyUser *tokenInfo) string {
	if proxyUser == nil {
		return ""
	}
	if proxyUser.Role == "admin" {
		return adminVisibilityUserID
	}
	return proxyUser.UserID
}

func (s *HiddenLibraryStore) load() error {
	snap := hiddenLibrarySnapshot{}
	stmt, err := s.db.prepare(`SELECT user_id, server_id, library_id FROM user_hidden_libraries`)
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
		userID := stmt.columnText(0)
		serverID := stmt.columnText(1)
		libraryID := stmt.columnText(2)
		servers := snap[userID]
		if servers == nil {
			servers = map[string]map[string]struct{}{}
			snap[userID] = servers
		}
		libraries := servers[serverID]
		if libraries == nil {
			libraries = map[string]struct{}{}
			servers[serverID] = libraries
		}
		libraries[libraryID] = struct{}{}
	}
	s.snapshot.Store(&snap)
	return nil
}

// HiddenForUser returns the user's hidden map (serverID → libraryID set), or
// nil when the user hides nothing. The returned maps are shared with the
// immutable snapshot and must not be modified.
func (s *HiddenLibraryStore) HiddenForUser(userID string) map[string]map[string]struct{} {
	curr := s.snapshot.Load()
	if curr == nil || userID == "" {
		return nil
	}
	return (*curr)[userID]
}

// HiddenForRequest resolves the hidden map for a request's identity.
func (s *HiddenLibraryStore) HiddenForRequest(reqCtx *RequestContext) map[string]map[string]struct{} {
	if reqCtx == nil {
		return nil
	}
	return s.HiddenForUser(visibilityUserID(reqCtx.ProxyUser))
}

// SetServerHidden replaces the hidden library list of one (user, server) pair.
// An empty list clears the pair: the admin API's patch contract distinguishes
// "absent key" (untouched, resolved by callers) from "empty array" (explicit
// clear, this call).
func (s *HiddenLibraryStore) SetServerHidden(userID, serverID string, libraryIDs []string) error {
	if userID == "" || serverID == "" {
		return fmt.Errorf("hidden library store: user and server are required")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	if err := s.db.withWriteTx(func() error {
		if err := s.db.execParams(`DELETE FROM user_hidden_libraries WHERE user_id = ? AND server_id = ?`, userID, serverID); err != nil {
			return err
		}
		for _, libraryID := range libraryIDs {
			if libraryID == "" {
				continue
			}
			if err := s.db.execParams(`INSERT INTO user_hidden_libraries (user_id, server_id, library_id) VALUES (?, ?, ?)`, userID, serverID, libraryID); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}

	// Publish a copy-on-write snapshot: reuse the immutable inner sets of the
	// other servers, but never mutate any map a reader may still hold.
	next := make(map[string]map[string]struct{})
	if curr := s.snapshot.Load(); curr != nil {
		for serverKey, libraries := range (*curr)[userID] {
			next[serverKey] = libraries
		}
	}
	if len(libraryIDs) > 0 {
		libraries := make(map[string]struct{}, len(libraryIDs))
		for _, libraryID := range libraryIDs {
			if libraryID != "" {
				libraries[libraryID] = struct{}{}
			}
		}
		next[serverID] = libraries
	} else {
		delete(next, serverID)
	}
	s.replaceUserServers(userID, next)
	return nil
}

// RemoveServer drops every hidden-library record of a deleted upstream server.
func (s *HiddenLibraryStore) RemoveServer(serverID string) error {
	if serverID == "" {
		return nil
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	if err := s.db.execParams(`DELETE FROM user_hidden_libraries WHERE server_id = ?`, serverID); err != nil {
		return err
	}

	curr := s.snapshot.Load()
	if curr == nil {
		return nil
	}
	next := hiddenLibrarySnapshot{}
	for userID, servers := range *curr {
		if _, affected := servers[serverID]; !affected {
			next[userID] = servers
			continue
		}
		kept := make(map[string]map[string]struct{}, len(servers))
		for serverKey, libraries := range servers {
			if serverKey != serverID {
				kept[serverKey] = libraries
			}
		}
		if len(kept) > 0 {
			next[userID] = kept
		}
	}
	s.snapshot.Store(&next)
	return nil
}

// RemoveUser drops every hidden-library record of a deleted user.
func (s *HiddenLibraryStore) RemoveUser(userID string) error {
	if userID == "" {
		return nil
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	if err := s.db.execParams(`DELETE FROM user_hidden_libraries WHERE user_id = ?`, userID); err != nil {
		return err
	}

	curr := s.snapshot.Load()
	if curr == nil {
		return nil
	}
	next := hiddenLibrarySnapshot{}
	for userKey, servers := range *curr {
		if userKey != userID {
			next[userKey] = servers
		}
	}
	s.snapshot.Store(&next)
	return nil
}

// PruneUserServers drops every server for which keep returns false. Callers
// must only invoke it with an explicit allow list: in this project's
// permission model an empty AllowedServers means "all servers", and pruning
// under such a list would wipe the user's whole config.
func (s *HiddenLibraryStore) PruneUserServers(userID string, keep func(serverID string) bool) error {
	if userID == "" || keep == nil {
		return nil
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	curr := s.snapshot.Load()
	if curr == nil {
		return nil
	}
	servers := (*curr)[userID]
	if len(servers) == 0 {
		return nil
	}
	drop := make([]string, 0, len(servers))
	for serverID := range servers {
		if !keep(serverID) {
			drop = append(drop, serverID)
		}
	}
	if len(drop) == 0 {
		return nil
	}
	if err := s.db.withWriteTx(func() error {
		for _, serverID := range drop {
			if err := s.db.execParams(`DELETE FROM user_hidden_libraries WHERE user_id = ? AND server_id = ?`, userID, serverID); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}

	kept := make(map[string]map[string]struct{}, len(servers))
	for serverID, libraries := range servers {
		if keep(serverID) {
			kept[serverID] = libraries
		}
	}
	s.replaceUserServers(userID, kept)
	return nil
}

// UserHiddenJSON returns the stored config in the admin API shape
// (serverID → sorted library IDs) so responses are deterministic.
func (s *HiddenLibraryStore) UserHiddenJSON(userID string) map[string][]string {
	out := map[string][]string{}
	for serverID, libraries := range s.HiddenForUser(userID) {
		ids := make([]string, 0, len(libraries))
		for libraryID := range libraries {
			ids = append(ids, libraryID)
		}
		sort.Strings(ids)
		out[serverID] = ids
	}
	return out
}

// replaceUserServers publishes a new server map for one user. The caller has
// already built `servers` sharing only immutable inner sets; this helper
// copies the outer level and drops the user entirely when nothing remains.
// Callers hold writeMu.
func (s *HiddenLibraryStore) replaceUserServers(userID string, servers map[string]map[string]struct{}) {
	next := hiddenLibrarySnapshot{}
	if curr := s.snapshot.Load(); curr != nil {
		for userKey, userServers := range *curr {
			next[userKey] = userServers
		}
	}
	if len(servers) == 0 {
		delete(next, userID)
	} else {
		next[userID] = servers
	}
	s.snapshot.Store(&next)
}
