package backend

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

// ThemeStore is the persistence layer for installed /web themes. It owns only
// the immutable resource tree under dataDir/themes and rows in the shared
// SQLite handle owned by IDStore; it never opens, closes or reconfigures that
// handle. Callers are expected to construct it after IDStore and to keep the
// shared database alive for at least as long as the store.
//
// Lock order is ThemeStore.mu -> sqliteDB.mu. No callback inside a database
// transaction calls back into another store, and no public method is invoked
// while the store lock is held.
type ThemeStore struct {
	mu     sync.RWMutex
	root   string
	db     *sqliteDB
	closed bool

	// entries mirrors theme_catalog for reads. versions indexes every
	// recoverable (current or previous) theme version by content hash and is
	// the only source of resource file paths.
	entries  map[string]*themeStoreEntry
	versions map[string]*themeStoreVersion
	site     ThemeSiteSettings
	health   ThemeStoreHealth
}

const (
	// ThemeIDBuiltin is the permanent builtin theme identifier. It is never a
	// catalog row and can never be installed, renamed, disabled or deleted.
	ThemeIDBuiltin = "builtin"

	// ThemeModeLight, ThemeModeDark and ThemeModeSystem are the only accepted
	// user appearance modes.
	ThemeModeLight  = "light"
	ThemeModeDark   = "dark"
	ThemeModeSystem = "system"

	// ThemeModeDefault preserves the previous dark /web experience.
	ThemeModeDefault = ThemeModeDark

	// ThemeMaxCustomThemes is the hard cap of installed custom themes,
	// independent of their enabled/unavailable state.
	ThemeMaxCustomThemes = 20

	// ThemeMaxRevision bounds revisions to 2^53-1, exactly representable in
	// JavaScript. Revisions are opaque random positive numbers.
	ThemeMaxRevision int64 = 1<<53 - 1

	// ThemeStoreDirName is the resource root below dataDir.
	ThemeStoreDirName = "themes"

	// themeStoreMaxUserIDLength bounds the opaque preference key.
	themeStoreMaxUserIDLength = 200
)

// themeStoreMaxBytes is the aggregate resource budget (current, previous,
// staged and orphaned bytes below dataDir/themes). It is a variable only so
// tests can exercise the quota path with small fixtures.
var themeStoreMaxBytes int64 = 64 << 20

// Store errors. Every public method wraps these with context, so callers can
// classify failures with errors.Is without parsing messages.
var (
	// ErrThemeStoreUnavailable means the store is nil, has no shared database,
	// or the shared database handle is closed. Browsing and playback must fall
	// back to the builtin theme instead of treating this as an empty catalog.
	ErrThemeStoreUnavailable = errors.New("theme store unavailable")
	// ErrThemeStoreClosed means the store was safely closed.
	ErrThemeStoreClosed = errors.New("theme store closed")
	// ErrThemeInvalid means caller input failed validation.
	ErrThemeInvalid = errors.New("theme store: invalid input")
	// ErrThemeNotFound means the theme ID is not installed.
	ErrThemeNotFound = errors.New("theme store: theme not found")
	// ErrThemeExists means a first install collided with an installed theme.
	ErrThemeExists = errors.New("theme store: theme already installed")
	// ErrThemeRevisionConflict means the expected revision no longer matches.
	ErrThemeRevisionConflict = errors.New("theme store: revision conflict")
	// ErrThemeBuiltin means the operation is not allowed on the builtin theme.
	ErrThemeBuiltin = errors.New("theme store: builtin theme is immutable")
	// ErrThemeInUse means the theme is referenced by the site default.
	ErrThemeInUse = errors.New("theme store: theme is referenced by the site default")
	// ErrThemeQuota means the theme count or resource byte budget is exceeded.
	ErrThemeQuota = errors.New("theme store: quota exceeded")
	// ErrThemeUnavailable means the theme is installed but its resources are
	// not recoverable (or it is disabled, for selection purposes).
	ErrThemeUnavailable = errors.New("theme store: theme resources unavailable")
	// ErrThemeResourceNotFound means the content hash or logical resource name
	// is not registered. It must be served as 404, never as an HTML fallback.
	ErrThemeResourceNotFound = errors.New("theme store: resource not found")
	// ErrThemeCorrupt means stored descriptors or files no longer match the
	// validated content hash.
	ErrThemeCorrupt = errors.New("theme store: stored theme failed integrity validation")
)

// ThemeSiteSettings is the anonymous site-default appearance selection.
// ThemeID nil means the builtin theme; the JSON contract may also send the
// literal "builtin", which SetSiteDefault normalizes to nil. Revisions are
// opaque random positive numbers.
type ThemeSiteSettings struct {
	ThemeID  *string `json:"themeId"`
	Revision int64   `json:"revision"`
}

// ThemePreference is one user's appearance selection. UserID is a store key
// only: it is never serialized and never accepted from a request body. ThemeID
// nil means "follow the site default"; the literal builtin selects the builtin
// theme regardless of the site default.
type ThemePreference struct {
	UserID   string  `json:"-"`
	ThemeID  *string `json:"themeId"`
	Mode     string  `json:"mode"`
	Revision int64   `json:"revision"`
}

// ThemeQuota reports the frozen site limits together with the current usage.
type ThemeQuota struct {
	MaxThemes  int   `json:"maxThemes"`
	UsedThemes int   `json:"usedThemes"`
	MaxBytes   int64 `json:"maxBytes"`
	UsedBytes  int64 `json:"usedBytes"`
}

// ThemeStoreHealth reports startup recovery and runtime cleanup results.
// Available here means the store itself is operational; per-theme availability
// is reported on the catalog entry. Problems are non-secret diagnostics.
type ThemeStoreHealth struct {
	Available   bool     `json:"available"`
	Recovered   int      `json:"recovered"`
	Missing     int      `json:"missing"`
	OrphanDirs  int      `json:"orphanDirs"`
	OrphanBytes int64    `json:"orphanBytes"`
	Problems    []string `json:"problems"`
}

// ThemeCatalogEntry is the administrator view of one theme. The builtin theme
// is included as a synthetic, read-only entry so the admin UI can offer it as
// the site default.
type ThemeCatalogEntry struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	Author          string   `json:"author"`
	Description     string   `json:"description"`
	Version         string   `json:"version"`
	Enabled         bool     `json:"enabled"`
	Builtin         bool     `json:"builtin"`
	Available       bool     `json:"available"`
	Revision        int64    `json:"revision"`
	CurrentHash     string   `json:"currentHash,omitempty"`
	PreviousHash    string   `json:"previousHash,omitempty"`
	Variants        []string `json:"variants"`
	Preview         string   `json:"preview,omitempty"`
	BackgroundImage string   `json:"backgroundImage,omitempty"`
	PackageBytes    int64    `json:"packageBytes"`
	Problem         string   `json:"problem,omitempty"`
}

// ThemeCatalog is the administrator catalog plus quota, site default and
// recovery health.
type ThemeCatalog struct {
	Themes []ThemeCatalogEntry `json:"themes"`
	Site   ThemeSiteSettings   `json:"site"`
	Quota  ThemeQuota          `json:"quota"`
	Health ThemeStoreHealth    `json:"health"`
}

// ThemePublicEntry is the anonymous/user-facing catalog entry. It carries only
// validation-approved public data and the immutable content hash used in
// resource URLs.
type ThemePublicEntry struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	Author          string   `json:"author"`
	Description     string   `json:"description"`
	Version         string   `json:"version"`
	Hash            string   `json:"hash"`
	Variants        []string `json:"variants"`
	Preview         string   `json:"preview,omitempty"`
	BackgroundImage string   `json:"backgroundImage,omitempty"`
}

// ThemePublicCatalog is the enabled, available catalog. The builtin theme is
// implicit and therefore not listed.
type ThemePublicCatalog struct {
	Themes []ThemePublicEntry `json:"themes"`
}

// ThemeResource is one validated, immutable public resource. Data is a copy, so
// callers may hand it to an HTTP writer without exposing store state.
type ThemeResource struct {
	ContentHash string `json:"-"`
	Path        string `json:"path"`
	MIME        string `json:"mime"`
	Bytes       int    `json:"bytes"`
	SHA256      string `json:"sha256"`
	Data        []byte `json:"-"`
}

// NewThemeStore initializes the theme tables on the shared database, validates
// or recovers the resource tree and returns the store. A nil/unavailable
// database, an unusable root directory or a failed initial recovery is an
// explicit error; the caller keeps the builtin theme and reports the failure
// instead of silently disabling the feature.
func NewThemeStore(dataDir string, db *sqliteDB) (*ThemeStore, error) {
	if db == nil {
		return nil, fmt.Errorf("%w: no shared database", ErrThemeStoreUnavailable)
	}
	// Probe the handle under its own lock so a closed database fails here
	// rather than after the resource tree was prepared.
	if err := db.withReadLock(func() error { return nil }); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrThemeStoreUnavailable, err)
	}
	if dataDir == "" {
		dataDir = defaultDataDir()
	}
	root := filepath.Join(dataDir, ThemeStoreDirName)
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("theme store root: %w", err)
	}
	info, err := os.Lstat(root)
	if err != nil {
		return nil, fmt.Errorf("theme store root: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%w: theme root is not a plain directory", ErrThemeStoreUnavailable)
	}
	store := &ThemeStore{
		root:     root,
		db:       db,
		entries:  make(map[string]*themeStoreEntry),
		versions: make(map[string]*themeStoreVersion),
		health:   ThemeStoreHealth{Available: true},
	}
	if err := store.initSchemaAndSite(); err != nil {
		return nil, err
	}
	if err := store.recoverOnOpen(); err != nil {
		return nil, err
	}
	return store, nil
}

// Available reports whether the store has a live shared handle. A closed store
// is reported as unavailable; a closed database handle is detected by the
// operation itself so it cannot be mistaken for a healthy empty store.
func (s *ThemeStore) Available() bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.db != nil && !s.closed
}

// liveLocked rejects a nil/closed store. Callers hold s.mu.
func (s *ThemeStore) liveLocked() error {
	if s == nil || s.db == nil {
		return ErrThemeStoreUnavailable
	}
	if s.closed {
		return ErrThemeStoreClosed
	}
	return nil
}

// CloseSafety stops the store without touching the shared database and without
// pretending success for a nil, already closed or unavailable store.
func (s *ThemeStore) CloseSafety() error {
	if s == nil {
		return fmt.Errorf("%w: nil store", ErrThemeStoreUnavailable)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return fmt.Errorf("%w: already closed", ErrThemeStoreClosed)
	}
	if s.db == nil {
		s.closed = true
		return fmt.Errorf("%w: no shared database", ErrThemeStoreUnavailable)
	}
	if err := s.db.withReadLock(func() error { return nil }); err != nil {
		s.closed = true
		return fmt.Errorf("%w: %v", ErrThemeStoreUnavailable, err)
	}
	s.closed = true
	// Drop validated copies; files and the shared DB remain owned elsewhere.
	s.entries = make(map[string]*themeStoreEntry)
	s.versions = make(map[string]*themeStoreVersion)
	return nil
}

// nextThemeRevision returns a positive opaque revision different from the
// previous value. Random values prevent ABA when a row is deleted and
// recreated. crypto/rand failure falls back to a monotonic bump.
func nextThemeRevision(previous int64) int64 {
	var buffer [8]byte
	if _, err := rand.Read(buffer[:]); err == nil {
		value := int64(binary.BigEndian.Uint64(buffer[:]) & ((1 << 52) - 1))
		if value > 0 && value != previous {
			return value
		}
	}
	value := previous + 1
	if value <= 0 || value > ThemeMaxRevision {
		value = 1
	}
	return value
}

func validThemeMode(mode string) bool {
	return mode == ThemeModeLight || mode == ThemeModeDark || mode == ThemeModeSystem
}

// validateThemeStoreUserID accepts an opaque store key. HTTP handlers are
// responsible for deriving it from the request context; the body must never
// provide it. The reserved administrator key "__admin__" is a valid value.
func validateThemeStoreUserID(userID string) error {
	if userID == "" {
		return fmt.Errorf("%w: user id is empty", ErrThemeInvalid)
	}
	if len(userID) > themeStoreMaxUserIDLength {
		return fmt.Errorf("%w: user id is too long", ErrThemeInvalid)
	}
	if !utf8.ValidString(userID) {
		return fmt.Errorf("%w: user id is not valid UTF-8", ErrThemeInvalid)
	}
	for _, r := range userID {
		if r == 0 || unicode.IsControl(r) {
			return fmt.Errorf("%w: user id contains control characters", ErrThemeInvalid)
		}
	}
	return nil
}

func cloneThemeSiteSettings(settings ThemeSiteSettings) ThemeSiteSettings {
	out := ThemeSiteSettings{Revision: settings.Revision}
	if settings.ThemeID != nil {
		value := *settings.ThemeID
		out.ThemeID = &value
	}
	return out
}

func cloneThemeDefinition(definition ThemeDefinition) ThemeDefinition {
	out := ThemeDefinition{
		Appearance:      definition.Appearance,
		BackgroundImage: definition.BackgroundImage,
		Variants:        make(map[string]ThemeVariant, len(definition.Variants)),
	}
	for name, variant := range definition.Variants {
		out.Variants[name] = variant
	}
	return out
}

func cloneThemePreference(preference ThemePreference) ThemePreference {
	out := ThemePreference{
		UserID:   preference.UserID,
		Mode:     preference.Mode,
		Revision: preference.Revision,
	}
	if preference.ThemeID != nil {
		value := *preference.ThemeID
		out.ThemeID = &value
	}
	return out
}

// themeStoreHashName reports whether name is one of our immutable content hash
// directory names (lower-case hex sha256).
func themeStoreHashName(name string) bool {
	if len(name) != 64 {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// noteProblemLocked records a bounded, non-secret health diagnostic. Callers
// hold s.mu for writing.
func (s *ThemeStore) noteProblemLocked(format string, args ...any) {
	if len(s.health.Problems) >= 32 {
		return
	}
	s.health.Problems = append(s.health.Problems, fmt.Sprintf(format, args...))
}

// sortedThemeIDs returns the deterministic catalog order. Callers hold s.mu.
func (s *ThemeStore) sortedThemeIDs() []string {
	ids := make([]string, 0, len(s.entries))
	for id := range s.entries {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// themeStoreThemeVariants is the frozen list exposed for every custom theme.
func themeStoreThemeVariants() []string {
	return []string{ThemeVariantLight, ThemeVariantDark}
}

// themeStoreValidCustomID rejects malformed or reserved non-builtin IDs without
// returning package parser types to preference callers.
func themeStoreValidCustomID(id string) error {
	if id == "" {
		return fmt.Errorf("%w: theme id is empty", ErrThemeInvalid)
	}
	if err := validateThemeID(id); err != nil {
		return fmt.Errorf("%w: %v", ErrThemeInvalid, err)
	}
	return nil
}

// themeStoreNormalizeThemeID treats nil, empty and "builtin" as the builtin
// selection; other values must be a syntactically valid custom theme ID.
func themeStoreNormalizeThemeID(themeID *string) (*string, error) {
	if themeID == nil || *themeID == "" || *themeID == ThemeIDBuiltin {
		return nil, nil
	}
	value := *themeID
	if err := themeStoreValidCustomID(value); err != nil {
		return nil, err
	}
	return &value, nil
}

// themeStoreNormalizePreferenceThemeID keeps the literal builtin as an explicit
// personal choice while nil/empty still mean "follow the site default".
func themeStoreNormalizePreferenceThemeID(themeID *string) (*string, error) {
	if themeID == nil || *themeID == "" {
		return nil, nil
	}
	value := *themeID
	if value == ThemeIDBuiltin {
		return &value, nil
	}
	if err := themeStoreValidCustomID(value); err != nil {
		return nil, err
	}
	return &value, nil
}

// stringsJoinNonEmpty is a small helper for diagnostics without importing more
// packages in the catalog file.
func stringsJoinNonEmpty(parts ...string) string {
	filtered := make([]string, 0, len(parts))
	for _, part := range parts {
		if strings.TrimSpace(part) != "" {
			filtered = append(filtered, part)
		}
	}
	return strings.Join(filtered, ": ")
}
