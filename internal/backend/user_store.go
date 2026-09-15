package backend

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

type User struct {
	ID             string
	Username       string
	Password       string // scrypt hash
	Enabled        bool
	AllowedServers []int
	CreatedAt      int64 // Unix milliseconds
}

type UserStore struct {
	db     *sqliteDB
	mu     sync.RWMutex
	users  map[string]*User // key: User.ID
	byName map[string]*User // key: lowercase(Username)
	logger *Logger
}

func NewUserStore(db *sqliteDB, logger *Logger) (*UserStore, error) {
	if db == nil {
		return nil, fmt.Errorf("user_store: SQLite database handle is nil")
	}
	if err := db.exec(`
		PRAGMA foreign_keys = ON;
		CREATE TABLE IF NOT EXISTS users (
			id TEXT PRIMARY KEY,
			username TEXT UNIQUE NOT NULL COLLATE NOCASE,
			password TEXT NOT NULL,
			enabled INTEGER NOT NULL DEFAULT 1,
			created_at INTEGER NOT NULL
		);
		CREATE TABLE IF NOT EXISTS user_servers (
			user_id TEXT NOT NULL,
			server_index INTEGER NOT NULL,
			PRIMARY KEY (user_id, server_index),
			FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
		);
	`); err != nil {
		return nil, fmt.Errorf("user_store: create tables: %w", err)
	}
	store := &UserStore{
		db:     db,
		users:  make(map[string]*User),
		byName: make(map[string]*User),
		logger: logger,
	}
	if err := store.loadAll(); err != nil {
		return nil, fmt.Errorf("user_store: load: %w", err)
	}
	if logger != nil {
		logger.Infof("UserStore initialized: %d user(s) loaded", len(store.users))
	}
	return store, nil
}

func (s *UserStore) loadAll() error {
	s.users = make(map[string]*User)
	s.byName = make(map[string]*User)

	stmt, err := s.db.prepare(`SELECT id, username, password, enabled, created_at FROM users`)
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
		user := &User{
			ID:        stmt.columnText(0),
			Username:  stmt.columnText(1),
			Password:  stmt.columnText(2),
			Enabled:   stmt.columnInt(3) != 0,
			CreatedAt: int64(stmt.columnInt(4)),
		}
		s.users[user.ID] = user
		s.byName[strings.ToLower(user.Username)] = user
	}

	stmtServers, err := s.db.prepare(`SELECT user_id, server_index FROM user_servers ORDER BY server_index`)
	if err != nil {
		return err
	}
	defer stmtServers.finalize()
	for {
		hasRow, err := stmtServers.step()
		if err != nil {
			return err
		}
		if !hasRow {
			break
		}
		userID := stmtServers.columnText(0)
		serverIndex := stmtServers.columnInt(1)
		if user, ok := s.users[userID]; ok {
			user.AllowedServers = append(user.AllowedServers, serverIndex)
		}
	}
	return nil
}

func (s *UserStore) Create(username, password string, allowedServers []int) (*User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.byName[strings.ToLower(username)]; exists {
		return nil, fmt.Errorf("username already exists")
	}

	hashed, err := HashPassword(password)
	if err != nil {
		return nil, fmt.Errorf("hash password: %w", err)
	}

	id := randomHex(16)
	now := time.Now().UnixMilli()

	if err := s.db.execParams(
		`INSERT INTO users (id, username, password, enabled, created_at) VALUES (?, ?, ?, 1, ?)`,
		id, username, hashed, now,
	); err != nil {
		return nil, err
	}

	for _, idx := range allowedServers {
		if err := s.db.execParams(
			`INSERT INTO user_servers (user_id, server_index) VALUES (?, ?)`,
			id, idx,
		); err != nil {
			return nil, err
		}
	}

	user := &User{
		ID:             id,
		Username:       username,
		Password:       hashed,
		Enabled:        true,
		AllowedServers: append([]int(nil), allowedServers...),
		CreatedAt:      now,
	}
	s.users[id] = user
	s.byName[strings.ToLower(username)] = user
	return user, nil
}

func (s *UserStore) Authenticate(username, password string) *User {
	s.mu.RLock()
	defer s.mu.RUnlock()

	user, ok := s.byName[strings.ToLower(username)]
	if !ok || !user.Enabled {
		return nil
	}
	if !VerifyPassword(password, user.Password) {
		return nil
	}
	// Return a copy
	return s.copyUser(user)
}

// ContainsUserID reports whether value is a locally registered proxy user ID.
// Membership stays inside the store rather than copying List() to every caller.
func (s *UserStore) ContainsUserID(value string) bool {
	if value == "" {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.users[value]
	return ok
}

func (s *UserStore) Get(id string) *User {
	s.mu.RLock()
	defer s.mu.RUnlock()
	user, ok := s.users[id]
	if !ok {
		return nil
	}
	return s.copyUser(user)
}

func (s *UserStore) GetByUsername(username string) *User {
	s.mu.RLock()
	defer s.mu.RUnlock()
	user, ok := s.byName[strings.ToLower(username)]
	if !ok {
		return nil
	}
	return s.copyUser(user)
}

func (s *UserStore) List() []*User {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]*User, 0, len(s.users))
	for _, user := range s.users {
		result = append(result, s.copyUser(user))
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].CreatedAt < result[j].CreatedAt
	})
	return result
}

func (s *UserStore) Update(id string, username *string, password *string, enabled *bool, allowedServers *[]int) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	user, ok := s.users[id]
	if !ok {
		return fmt.Errorf("user not found: %s", id)
	}

	if username != nil && strings.ToLower(*username) != strings.ToLower(user.Username) {
		if _, exists := s.byName[strings.ToLower(*username)]; exists {
			return fmt.Errorf("username already exists")
		}
	}

	if username != nil {
		if err := s.db.execParams(
			`UPDATE users SET username = ? WHERE id = ?`,
			*username, id,
		); err != nil {
			return err
		}
		delete(s.byName, strings.ToLower(user.Username))
		user.Username = *username
		s.byName[strings.ToLower(user.Username)] = user
	}

	if password != nil && *password != "" {
		hashed, err := HashPassword(*password)
		if err != nil {
			return fmt.Errorf("hash password: %w", err)
		}
		if err := s.db.execParams(
			`UPDATE users SET password = ? WHERE id = ?`,
			hashed, id,
		); err != nil {
			return err
		}
		user.Password = hashed
	}

	if enabled != nil {
		enabledInt := 0
		if *enabled {
			enabledInt = 1
		}
		if err := s.db.execParams(
			`UPDATE users SET enabled = ? WHERE id = ?`,
			enabledInt, id,
		); err != nil {
			return err
		}
		user.Enabled = *enabled
	}

	if allowedServers != nil {
		if err := s.writeAllowedServersLocked(id, *allowedServers); err != nil {
			return err
		}
		user.AllowedServers = append([]int(nil), *allowedServers...)
	}

	return nil
}

func (s *UserStore) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	user, ok := s.users[id]
	if !ok {
		return fmt.Errorf("user not found: %s", id)
	}

	// Enable foreign keys for cascade delete
	_ = s.db.exec(`PRAGMA foreign_keys = ON`)
	if err := s.db.execParams(`DELETE FROM user_servers WHERE user_id = ?`, id); err != nil {
		return err
	}
	if err := s.db.execParams(`DELETE FROM users WHERE id = ?`, id); err != nil {
		return err
	}

	delete(s.byName, strings.ToLower(user.Username))
	delete(s.users, id)
	return nil
}

func (s *UserStore) ShiftServerIndices(deletedIndex int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Remove associations for the deleted server and shift higher indices down
	if err := s.db.execParams(`DELETE FROM user_servers WHERE server_index = ?`, deletedIndex); err != nil {
		if s.logger != nil {
			s.logger.Warnf("UserStore: failed to delete server_index=%d: %v", deletedIndex, err)
		}
	}
	if err := s.db.execParams(`UPDATE user_servers SET server_index = server_index - 1 WHERE server_index > ?`, deletedIndex); err != nil {
		if s.logger != nil {
			s.logger.Warnf("UserStore: failed to shift server indices > %d: %v", deletedIndex, err)
		}
	}

	// Update in-memory state
	for _, user := range s.users {
		newServers := make([]int, 0, len(user.AllowedServers))
		for _, idx := range user.AllowedServers {
			if idx == deletedIndex {
				continue
			}
			if idx > deletedIndex {
				newServers = append(newServers, idx-1)
			} else {
				newServers = append(newServers, idx)
			}
		}
		user.AllowedServers = newServers
	}
}

// ReorderServerIndices moves every user's allowed-server list through the same
// reorder the upstream list itself went through, so a permission keeps naming the
// server it was granted for. It is the reorder counterpart of ShiftServerIndices,
// which handles a deletion.
//
// Without it a reorder leaves each list pointing at whatever moved into those
// positions, which changes who may reach which upstream without anything in the
// panel showing it.
func (s *UserStore) ReorderServerIndices(fromIndex, toIndex int) {
	if fromIndex == toIndex {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, user := range s.users {
		remapped := make([]int, 0, len(user.AllowedServers))
		moved := false
		for _, idx := range user.AllowedServers {
			next := reorderServerIndex(idx, fromIndex, toIndex)
			if next != idx {
				moved = true
			}
			remapped = append(remapped, next)
		}
		if !moved {
			continue
		}
		// A reorder can move a later server ahead of an earlier one, so keep the
		// list in index order: the panel renders it in the order it is stored.
		sort.Ints(remapped)
		if err := s.writeAllowedServersLocked(user.ID, remapped); err != nil && s.logger != nil {
			s.logger.Warnf("UserStore: failed to persist reordered server indices: %v", err)
		}
		user.AllowedServers = remapped
	}
}

// writeAllowedServersLocked replaces a user's stored server list. The caller
// holds the write lock.
func (s *UserStore) writeAllowedServersLocked(id string, indices []int) error {
	if err := s.db.execParams(`DELETE FROM user_servers WHERE user_id = ?`, id); err != nil {
		return err
	}
	for _, idx := range indices {
		if err := s.db.execParams(
			`INSERT INTO user_servers (user_id, server_index) VALUES (?, ?)`,
			id, idx,
		); err != nil {
			return err
		}
	}
	return nil
}

func (s *UserStore) copyUser(user *User) *User {
	return &User{
		ID:             user.ID,
		Username:       user.Username,
		Password:       user.Password,
		Enabled:        user.Enabled,
		AllowedServers: append([]int(nil), user.AllowedServers...),
		CreatedAt:      user.CreatedAt,
	}
}
