package backend

// Tokens never expire — removed by explicit logout, admin password reset, or manual revocation.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type tokenInfo struct {
	UserID         string `json:"userId"`
	Username       string `json:"username"`
	Role           string `json:"role"`
	AllowedServers []int  `json:"allowedServers,omitempty"`
	CreatedAt      int64  `json:"createdAt"`
}

type AuthManager struct {
	mu          sync.RWMutex
	configStore *ConfigStore
	identity    *ClientIdentityService
	logger      *Logger
	tokenFile   string
	proxyUserID string
	tokens      map[string]tokenInfo
}

func NewAuthManager(configStore *ConfigStore, identity *ClientIdentityService, logger *Logger) (*AuthManager, error) {
	cfg := configStore.Snapshot()
	tokenFile := filepath.Join(cfg.DataDir, "tokens.json")
	if tokenFile == "" || tokenFile == "." || tokenFile == string(filepath.Separator) {
		tokenFile = filepath.Join(defaultDataDir(), "tokens.json")
	}
	manager := &AuthManager{
		configStore: configStore,
		identity:    identity,
		logger:      logger,
		tokenFile:   tokenFile,
		tokens:      map[string]tokenInfo{},
		proxyUserID: randomHex(16),
	}
	if err := manager.ensureAdminPasswordHashed(); err != nil {
		return nil, err
	}
	if err := manager.load(); err != nil {
		return nil, err
	}
	if err := manager.save(); err != nil {
		return nil, err
	}
	return manager, nil
}

func (m *AuthManager) ensureAdminPasswordHashed() error {
	cfg := m.configStore.Snapshot()
	if IsHashedPassword(cfg.Admin.Password) {
		return nil
	}
	hashed, err := HashPassword(cfg.Admin.Password)
	if err != nil {
		return err
	}
	if err := m.configStore.Mutate(func(config *Config) error {
		config.Admin.Password = hashed
		return nil
	}); err != nil {
		return err
	}
	return m.configStore.Save()
}

func (m *AuthManager) load() error {
	if err := os.MkdirAll(filepath.Dir(m.tokenFile), 0o755); err != nil {
		return err
	}
	raw, err := os.ReadFile(m.tokenFile)
	if err != nil {
		if os.IsNotExist(err) {
			if m.logger != nil {
				m.logger.Infof("Token file not found, starting fresh: %s", m.tokenFile)
			}
			return nil
		}
		return err
	}
	payload := map[string]json.RawMessage{}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return err
	}
	if rawProxy, ok := payload["_proxyUserId"]; ok {
		_ = json.Unmarshal(rawProxy, &m.proxyUserID)
		delete(payload, "_proxyUserId")
	}
	for token, rawToken := range payload {
		var info tokenInfo
		if err := json.Unmarshal(rawToken, &info); err == nil {
			if info.Role == "" && info.UserID == m.proxyUserID {
				info.Role = "admin"
			}
			m.tokens[token] = info
		}
	}
	if m.logger != nil {
		m.logger.Infof("Loaded %d proxy token(s) from %s", len(m.tokens), m.tokenFile)
	}
	return nil
}

func (m *AuthManager) save() error {
	m.mu.RLock()
	payload := map[string]any{"_proxyUserId": m.proxyUserID}
	for token, info := range m.tokens {
		payload[token] = info
	}
	m.mu.RUnlock()

	encoded, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomically(m.tokenFile, encoded, PrivateFileMode())
}

func (m *AuthManager) Authenticate(username, password string) (map[string]any, bool, error) {
	cfg := m.configStore.Snapshot()
	if username != cfg.Admin.Username || !VerifyPassword(password, cfg.Admin.Password) {
		return nil, false, nil
	}
	token := randomHex(16)
	m.mu.Lock()
	m.tokens[token] = tokenInfo{UserID: m.proxyUserID, Username: username, Role: "admin", CreatedAt: time.Now().UnixMilli()}
	m.mu.Unlock()
	if err := m.save(); err != nil {
		return nil, false, err
	}
	response := map[string]any{
		"User":        m.BuildUserObject(),
		"AccessToken": token,
		"ServerId":    cfg.Server.ID,
		"SessionInfo": map[string]any{
			"UserId":                m.proxyUserID,
			"UserName":              username,
			"ServerId":              cfg.Server.ID,
			"Id":                    randomHex(16),
			"DeviceId":              "proxy",
			"DeviceName":            "Proxy Session",
			"Client":                "Emby Aggregator",
			"ApplicationVersion":    "1.0.0",
			"SupportsRemoteControl": false,
			"PlayableMediaTypes":    []string{"Audio", "Video"},
			"SupportedCommands":     []any{},
		},
	}
	return response, true, nil
}

func (m *AuthManager) ValidateToken(token string) *tokenInfo {
	if token == "" {
		return nil
	}
	m.mu.RLock()
	info, ok := m.tokens[token]
	knownTokens := len(m.tokens)
	m.mu.RUnlock()
	if !ok {
		if m.logger != nil {
			short := token
			if len(short) > 8 {
				short = short[:8] + "..."
			}
			m.logger.Debugf("Token rejected (not found): %s, known tokens=%d", short, knownTokens)
		}
		return nil
	}
	infoCopy := info
	return &infoCopy
}

func (m *AuthManager) RevokeToken(token string) bool {
	if token == "" {
		return false
	}
	m.mu.Lock()
	_, ok := m.tokens[token]
	if ok {
		delete(m.tokens, token)
	}
	m.mu.Unlock()
	if ok {
		m.identity.DeleteCaptured(token)
		_ = m.save()
	}
	return ok
}

// RevokeAllTokens removes all proxy tokens and their associated captured identity data.
// Used when admin password is changed or reset via CLI.
func (m *AuthManager) RevokeAllTokens() {
	m.mu.Lock()
	old := m.tokens
	m.tokens = make(map[string]tokenInfo)
	m.mu.Unlock()
	for token := range old {
		m.identity.DeleteCaptured(token)
	}
	_ = m.save()
}

// AuthenticateUser generates a token for an already-verified user from UserStore.
func (m *AuthManager) AuthenticateUser(user *User) (map[string]any, string, error) {
	token := randomHex(16)
	m.mu.Lock()
	m.tokens[token] = tokenInfo{
		UserID:         user.ID,
		Username:       user.Username,
		Role:           "user",
		AllowedServers: append([]int(nil), user.AllowedServers...),
		CreatedAt:      time.Now().UnixMilli(),
	}
	m.mu.Unlock()
	if err := m.save(); err != nil {
		return nil, "", err
	}
	cfg := m.configStore.Snapshot()
	response := map[string]any{
		"User":        m.BuildUserObjectForUser(user),
		"AccessToken": token,
		"ServerId":    cfg.Server.ID,
		"SessionInfo": map[string]any{
			"UserId":                user.ID,
			"UserName":              user.Username,
			"ServerId":              cfg.Server.ID,
			"Id":                    randomHex(16),
			"DeviceId":              "proxy",
			"DeviceName":            "Proxy Session",
			"Client":                "Emby Aggregator",
			"ApplicationVersion":    "1.0.0",
			"SupportsRemoteControl": false,
			"PlayableMediaTypes":    []string{"Audio", "Video"},
			"SupportedCommands":     []any{},
		},
	}
	return response, token, nil
}

// BuildUserObjectForUser returns an Emby-compatible User object for a regular user.
func (m *AuthManager) BuildUserObjectForUser(user *User) map[string]any {
	cfg := m.configStore.Snapshot()
	return map[string]any{
		"Name":                      user.Username,
		"ServerId":                  cfg.Server.ID,
		"Id":                        user.ID,
		"HasPassword":               true,
		"HasConfiguredPassword":     true,
		"HasConfiguredEasyPassword": false,
		"EnableAutoLogin":           false,
		"Policy": map[string]any{
			"IsAdministrator":                false,
			"IsHidden":                       false,
			"IsDisabled":                     false,
			"EnableUserPreferenceAccess":     true,
			"EnableContentDownloading":       true,
			"EnableRemoteAccess":             true,
			"EnableLiveTvAccess":             true,
			"EnableLiveTvManagement":         false,
			"EnableMediaPlayback":            true,
			"EnableAudioPlaybackTranscoding": true,
			"EnableVideoPlaybackTranscoding": true,
			"EnablePlaybackRemuxing":         true,
			"EnableContentDeletion":          false,
			"EnableSyncTranscoding":          true,
			"EnableMediaConversion":          true,
			"EnableAllDevices":               true,
			"EnableAllChannels":              true,
			"EnableAllFolders":               true,
			"EnablePublicSharing":            true,
			"InvalidLoginAttemptCount":       0,
			"RemoteClientBitrateLimit":       0,
		},
		"Configuration": map[string]any{
			"PlayDefaultAudioTrack":      true,
			"DisplayMissingEpisodes":     false,
			"EnableLocalPassword":        false,
			"HidePlayedInLatest":         true,
			"RememberAudioSelections":    true,
			"RememberSubtitleSelections": true,
			"EnableNextEpisodeAutoPlay":  true,
		},
	}
}

// RevokeTokensByUserID removes all tokens belonging to a specific user.
func (m *AuthManager) RevokeTokensByUserID(userID string) {
	m.mu.Lock()
	for token, info := range m.tokens {
		if info.UserID == userID {
			delete(m.tokens, token)
			if m.identity != nil {
				m.identity.DeleteCaptured(token)
			}
		}
	}
	m.mu.Unlock()
	_ = m.save()
}

func (m *AuthManager) ProxyUserID() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.proxyUserID
}

func (m *AuthManager) BuildUserObject() map[string]any {
	cfg := m.configStore.Snapshot()
	return map[string]any{
		"Name":                      cfg.Admin.Username,
		"ServerId":                  cfg.Server.ID,
		"Id":                        m.ProxyUserID(),
		"HasPassword":               true,
		"HasConfiguredPassword":     true,
		"HasConfiguredEasyPassword": false,
		"EnableAutoLogin":           false,
		"Policy": map[string]any{
			"IsAdministrator":                true,
			"IsHidden":                       false,
			"IsDisabled":                     false,
			"EnableUserPreferenceAccess":     true,
			"EnableContentDownloading":       true,
			"EnableRemoteAccess":             true,
			"EnableLiveTvAccess":             true,
			"EnableLiveTvManagement":         true,
			"EnableMediaPlayback":            true,
			"EnableAudioPlaybackTranscoding": true,
			"EnableVideoPlaybackTranscoding": true,
			"EnablePlaybackRemuxing":         true,
			"EnableContentDeletion":          false,
			"EnableSyncTranscoding":          true,
			"EnableMediaConversion":          true,
			"EnableAllDevices":               true,
			"EnableAllChannels":              true,
			"EnableAllFolders":               true,
			"EnablePublicSharing":            true,
			"InvalidLoginAttemptCount":       0,
			"RemoteClientBitrateLimit":       0,
		},
		"Configuration": map[string]any{
			"PlayDefaultAudioTrack":      true,
			"DisplayMissingEpisodes":     false,
			"EnableLocalPassword":        false,
			"HidePlayedInLatest":         true,
			"RememberAudioSelections":    true,
			"RememberSubtitleSelections": true,
			"EnableNextEpisodeAutoPlay":  true,
		},
	}
}
