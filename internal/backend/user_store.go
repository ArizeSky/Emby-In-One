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
	// MaxParentalRating hides items whose Emby parental score exceeds this
	// value (see parental.go). 0 = no restriction.
	MaxParentalRating int
	// BlockUnratedItems hides items that carry no recognisable parental rating
	// when true (plan §8.2). Defaults false, matching Emby.
	BlockUnratedItems bool
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
			created_at INTEGER NOT NULL,
			max_parental_rating INTEGER NOT NULL DEFAULT 0,
			block_unrated_items INTEGER NOT NULL DEFAULT 0
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
	if err := migrateUsersTable(db); err != nil {
		return nil, fmt.Errorf("user_store: migrate: %w", err)
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

// migrateUsersTable adds columns introduced after the initial schema, so
// existing deployments keep their users when upgrading.
func migrateUsersTable(db *sqliteDB) error {
	stmt, err := db.prepare(`PRAGMA table_info(users)`)
	if err != nil {
		return err
	}
	defer stmt.finalize()
	hasParental := false
	hasBlockUnrated := false
	for {
		hasRow, err := stmt.step()
		if err != nil {
			return err
		}
		if !hasRow {
			break
		}
		switch stmt.columnText(1) {
		case "max_parental_rating":
			hasParental = true
		case "block_unrated_items":
			hasBlockUnrated = true
		}
	}
	if !hasParental {
		if err := db.exec(`ALTER TABLE users ADD COLUMN max_parental_rating INTEGER NOT NULL DEFAULT 0`); err != nil {
			return err
		}
	}
	if !hasBlockUnrated {
		if err := db.exec(`ALTER TABLE users ADD COLUMN block_unrated_items INTEGER NOT NULL DEFAULT 0`); err != nil {
			return err
		}
	}
	return nil
}

// SetMaxParentalRating updates a user's parental ceiling (0 = unrestricted).
func (s *UserStore) SetMaxParentalRating(id string, rating int) error {
	if rating < 0 {
		rating = 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.db.execParams(`UPDATE users SET max_parental_rating = ? WHERE id = ?`, rating, id); err != nil {
		return err
	}
	if u, ok := s.users[id]; ok {
		u.MaxParentalRating = rating
	}
	return nil
}

// SetParentalPolicy updates a user's parental ceiling and unrated-blocking flag in
// one statement, so the two policy fields can never be left half-updated (plan §8.2).
func (s *UserStore) SetParentalPolicy(id string, maxRating int, blockUnrated bool) error {
	if maxRating < 0 {
		maxRating = 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.db.execParams(`UPDATE users SET max_parental_rating = ?, block_unrated_items = ? WHERE id = ?`, maxRating, boolToInt(blockUnrated), id); err != nil {
		return err
	}
	if u, ok := s.users[id]; ok {
		u.MaxParentalRating = maxRating
		u.BlockUnratedItems = blockUnrated
	}
	return nil
}

func (s *UserStore) loadAll() error {
	s.users = make(map[string]*User)
	s.byName = make(map[string]*User)

	stmt, err := s.db.prepare(`SELECT id, username, password, enabled, created_at, max_parental_rating, block_unrated_items FROM users`)
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
			ID:                stmt.columnText(0),
			Username:          stmt.columnText(1),
			Password:          stmt.columnText(2),
			Enabled:           stmt.columnInt(3) != 0,
			CreatedAt:         stmt.columnInt64(4),
			MaxParentalRating: stmt.columnInt(5),
			BlockUnratedItems: stmt.columnInt(6) != 0,
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

// Create registers a new user with no parental restriction. It delegates to
// CreateWithPolicy so existing callers keep a stable signature.
func (s *UserStore) Create(username, password string, allowedServers []string) (*User, error) {
	return s.CreateWithPolicy(username, password, allowedServers, 0, false)
}

// CreateWithPolicy registers a new user and its parental policy in a single
// transaction (plan §8.2): the policy columns, the user row and user_servers are
// written atomically, so a policy failure can never leave a created account with
// the wrong (or default) ceiling.
func (s *UserStore) CreateWithPolicy(username, password string, allowedServers []string, maxRating int, blockUnrated bool) (*User, error) {
	if !validParentalRating(maxRating) {
		return nil, fmt.Errorf("invalid max parental rating: %d", maxRating)
	}
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
			`INSERT INTO users (id, username, password, enabled, created_at, max_parental_rating, block_unrated_items) VALUES (?, ?, ?, 1, ?, ?, ?)`,
			id, username, hashed, now, maxRating, boolToInt(blockUnrated),
		); err != nil {
			return err
		}
		return s.replaceAllowedServersParams(id, allowedServers)
	}); err != nil {
		return nil, err
	}

	user := &User{
		ID:                id,
		Username:          username,
		Password:          hashed,
		Enabled:           true,
		AllowedServers:    append([]string(nil), allowedServers...),
		CreatedAt:         now,
		MaxParentalRating: maxRating,
		BlockUnratedItems: blockUnrated,
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

// IsUserEnabled reports whether the given proxy user exists and is enabled.
// It exists so the per-request auth path can re-check the flag without copying
// the whole user record on every request.
func (s *UserStore) IsUserEnabled(id string) bool {
	if id == "" {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	user, ok := s.users[id]
	return ok && user.Enabled
}

// LiveGrants reports whether the user exists and is enabled, and returns a copy
// of its current AllowedServers. The per-request auth path uses it to refresh
// the permission snapshot baked into a token, so narrowing or widening a user's
// server access takes effect on the next request instead of at the next login.
// A nil slice means "all servers" (this project's unrestricted marker).
func (s *UserStore) LiveGrants(id string) (allowed []string, found bool) {
	if id == "" {
		return nil, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	user, ok := s.users[id]
	if !ok || !user.Enabled {
		return nil, false
	}
	return append([]string(nil), user.AllowedServers...), true
}

// Update applies basic field changes without touching the parental policy. It
// delegates to UpdateWithPolicy so existing callers keep a stable signature.
func (s *UserStore) Update(id string, username *string, password *string, enabled *bool, allowedServers *[]string) error {
	return s.UpdateWithPolicy(id, username, password, enabled, allowedServers, nil, nil)
}

// UpdateWithPolicy applies basic field changes and the parental policy in one
// transaction (plan §8.2). A nil policy pointer leaves that column untouched; a
// non-nil pointer sets it. The in-memory copy is updated only after commit, so a
// failed write cannot leave memory ahead of the database.
func (s *UserStore) UpdateWithPolicy(id string, username *string, password *string, enabled *bool, allowedServers *[]string, maxRating *int, blockUnrated *bool) error {
	if maxRating != nil && !validParentalRating(*maxRating) {
		return fmt.Errorf("invalid max parental rating: %d", *maxRating)
	}
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
		if maxRating != nil {
			if err := s.db.execParams(`UPDATE users SET max_parental_rating = ? WHERE id = ?`, *maxRating, id); err != nil {
				return err
			}
		}
		if blockUnrated != nil {
			if err := s.db.execParams(`UPDATE users SET block_unrated_items = ? WHERE id = ?`, boolToInt(*blockUnrated), id); err != nil {
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
	if maxRating != nil {
		user.MaxParentalRating = *maxRating
	}
	if blockUnrated != nil {
		user.BlockUnratedItems = *blockUnrated
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
		if err := s.db.writeParams(`DELETE FROM user_servers WHERE server_id = ?`, serverID); err != nil {
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
		ID:                user.ID,
		Username:          user.Username,
		Password:          user.Password,
		Enabled:           user.Enabled,
		AllowedServers:    append([]string(nil), user.AllowedServers...),
		CreatedAt:         user.CreatedAt,
		MaxParentalRating: user.MaxParentalRating,
		BlockUnratedItems: user.BlockUnratedItems,
	}
}
