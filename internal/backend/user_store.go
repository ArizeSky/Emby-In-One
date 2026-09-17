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
	AllowedServers []string
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
			server_id TEXT NOT NULL,
			PRIMARY KEY (user_id, server_id),
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
			CreatedAt: stmt.columnInt64(4),
		}
		s.users[user.ID] = user
		s.byName[strings.ToLower(user.Username)] = user
	}

	stmtServers, err := s.db.prepare(`SELECT user_id, server_id FROM user_servers ORDER BY server_id`)
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
		serverID := stmtServers.columnText(1)
		if user, ok := s.users[userID]; ok {
			user.AllowedServers = append(user.AllowedServers, serverID)
		}
	}
	return nil
}

func (s *UserStore) Create(username, password string, allowedServers []string) (*User, error) {
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

	if err := s.db.withWriteTx(func() error {
		if err := s.db.execParams(
			`INSERT INTO users (id, username, password, enabled, created_at) VALUES (?, ?, ?, 1, ?)`,
			id, username, hashed, now,
		); err != nil {
			return err
		}
		return s.replaceAllowedServersParams(id, allowedServers)
	}); err != nil {
		return nil, err
	}

	user := &User{
		ID:             id,
		Username:       username,
		Password:       hashed,
		Enabled:        true,
		AllowedServers: append([]string(nil), allowedServers...),
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
		// Same reason as AuthManager.Authenticate: an unknown or disabled account must not
		// answer faster than a wrong password.
		spendVerifyTime(password)
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

func (s *UserStore) Update(id string, username *string, password *string, enabled *bool, allowedServers *[]string) error {
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

	hashed := ""
	if password != nil && *password != "" {
		value, err := HashPassword(*password)
		if err != nil {
			return fmt.Errorf("hash password: %w", err)
		}
		hashed = value
	}

	if err := s.db.withWriteTx(func() error {
		if username != nil {
			if err := s.db.execParams(`UPDATE users SET username = ? WHERE id = ?`, *username, id); err != nil {
				return err
			}
		}
		if hashed != "" {
			if err := s.db.execParams(`UPDATE users SET password = ? WHERE id = ?`, hashed, id); err != nil {
				return err
			}
		}
		if enabled != nil {
			if err := s.db.execParams(`UPDATE users SET enabled = ? WHERE id = ?`, boolToInt(*enabled), id); err != nil {
				return err
			}
		}
		if allowedServers != nil {
			return s.replaceAllowedServersParams(id, *allowedServers)
		}
		return nil
	}); err != nil {
		return err
	}

	// Applied only after the transaction committed, so a failed write cannot leave the
	// in-memory user ahead of the database.
	if username != nil {
		delete(s.byName, strings.ToLower(user.Username))
		user.Username = *username
		s.byName[strings.ToLower(user.Username)] = user
	}
	if hashed != "" {
		user.Password = hashed
	}
	if enabled != nil {
		user.Enabled = *enabled
	}
	if allowedServers != nil {
		user.AllowedServers = append([]string(nil), *allowedServers...)
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

	// foreign_keys is already on for this connection (set when the store was created); a
	// PRAGMA issued inside a transaction would be silently ignored anyway.
	if err := s.db.withWriteTx(func() error {
		if err := s.db.execParams(`DELETE FROM user_servers WHERE user_id = ?`, id); err != nil {
			return err
		}
		return s.db.execParams(`DELETE FROM users WHERE id = ?`, id)
	}); err != nil {
		return err
	}

	delete(s.byName, strings.ToLower(user.Username))
	delete(s.users, id)
	return nil
}

// RemoveServerGrants removes all access grants for the deleted upstream server.
func (s *UserStore) RemoveServerGrants(serverID string) error {
	if serverID == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.db != nil {
		if err := s.db.execParams(`DELETE FROM user_servers WHERE server_id = ?`, serverID); err != nil {
			return err
		}
	}

	for _, user := range s.users {
		kept := make([]string, 0, len(user.AllowedServers))
		for _, id := range user.AllowedServers {
			if id != serverID {
				kept = append(kept, id)
			}
		}
		user.AllowedServers = kept
	}
	return nil
}

// replaceAllowedServersParams replaces a user's stored server list using the caller's
// transaction or write lock. The caller holds the store lock.
func (s *UserStore) replaceAllowedServersParams(id string, servers []string) error {
	if err := s.db.execParams(`DELETE FROM user_servers WHERE user_id = ?`, id); err != nil {
		return err
	}
	for _, serverID := range servers {
		if err := s.db.execParams(
			`INSERT INTO user_servers (user_id, server_id) VALUES (?, ?)`,
			id, serverID,
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
		AllowedServers: append([]string(nil), user.AllowedServers...),
		CreatedAt:      user.CreatedAt,
	}
}
