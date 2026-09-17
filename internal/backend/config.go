package backend

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

type Config struct {
	Path     string
	DataDir  string
	Server   ServerConfig
	Admin    AdminConfig
	Playback PlaybackConfig
	Timeouts TimeoutsConfig
	Proxies  []ProxyConfig
	Upstream []UpstreamConfig
}

type ServerConfig struct {
	Port       int
	Name       string
	ID         string
	TrustProxy bool
}

type AdminConfig struct {
	Username string
	Password string
}

type PlaybackConfig struct {
	Mode string
}

type TimeoutsConfig struct {
	API                 int
	Global              int
	Login               int
	HealthCheck         int
	HealthInterval      int
	SearchGracePeriod   int // 搜索/批量聚合宽恕期 (ms)，默认 3000
	MetadataGracePeriod int // 元数据多实例获取宽恕期 (ms)，默认 3000
	LatestGracePeriod   int // 最新添加宽恕期 (ms)，0=禁用
}

type ProxyConfig struct {
	ID   string
	Name string
	URL  string
}

type UpstreamConfig struct {
	ID                  string
	Name                string
	URL                 string
	Username            string
	Password            string
	APIKey              string
	PlaybackMode        string
	SpoofClient         string
	FollowRedirects     bool
	ProxyID             string
	PriorityMetadata    bool
	StreamingURL        string   // first entry of StreamingURLs; kept as the single-value view
	StreamingURLs       []string // ordered stream bases: [0] primary, [1:] fallbacks
	CustomUserAgent     string
	CustomClient        string
	CustomClientVersion string
	CustomDeviceName    string
	CustomDeviceId      string
	MaxConcurrent       int
}

type ConfigStore struct {
	mu     sync.RWMutex
	config *Config
}

func DetectConfigPath() string {
	candidates := []string{
		"/app/config/config.yaml",
		filepath.Join("config", "config.yaml"),
		"config.yaml",
		filepath.Join("..", "config", "config.yaml"),
		filepath.Join("..", "config.yaml"),
	}
	for _, candidate := range candidates {
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return filepath.Join("config", "config.yaml")
}

func LoadConfigStore() (*ConfigStore, error) {
	path := DetectConfigPath()
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	cfg, err := parseConfigYAML(string(raw))
	if err != nil {
		return nil, err
	}
	cfg.Path = path
	if cfg.Server.Port == 0 {
		cfg.Server.Port = 8096
	}
	if cfg.Server.Name == "" {
		cfg.Server.Name = "Emby In One"
	}
	dirty := false
	if cfg.Server.ID == "" {
		cfg.Server.ID = randomHex(16)
		dirty = true
	}
	if cfg.Playback.Mode == "" {
		cfg.Playback.Mode = "proxy"
	}
	if cfg.Timeouts.API == 0 {
		cfg.Timeouts.API = 30000
	}
	if cfg.Timeouts.Global == 0 {
		cfg.Timeouts.Global = 15000
	}
	// 30000 keeps the login as forgiving as it was before the setting was wired up:
	// every request used to inherit timeouts.API.
	if cfg.Timeouts.Login == 0 {
		cfg.Timeouts.Login = 30000
	}
	// 30000 keeps the health check as forgiving as it was before the setting was
	// wired up: every request used to inherit timeouts.API.
	if cfg.Timeouts.HealthCheck == 0 {
		cfg.Timeouts.HealthCheck = 30000
	}
	if cfg.Timeouts.HealthInterval == 0 {
		cfg.Timeouts.HealthInterval = 60000
	}
	if cfg.Timeouts.SearchGracePeriod == 0 {
		cfg.Timeouts.SearchGracePeriod = 3000
	}
	if cfg.Timeouts.MetadataGracePeriod == 0 {
		cfg.Timeouts.MetadataGracePeriod = 3000
	}
	// LatestGracePeriod 默认 0：禁用，等待所有服务器返回
	if err := validateTimeouts(cfg.Timeouts); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	if cfg.Admin.Username == "" || cfg.Admin.Password == "" {
		return nil, errors.New("config: admin.username and admin.password are required")
	}
	seenIDs := make(map[string]bool)
	for i := range cfg.Upstream {
		u := &cfg.Upstream[i]
		if u.ID == "" || seenIDs[u.ID] {
			dirty = true
			u.ID = ""
		}
		normalizeUpstream(u, i, cfg)
		seenIDs[u.ID] = true
	}
	if cfg.DataDir == "" {
		cfg.DataDir = defaultDataDir()
	}
	store := &ConfigStore{config: cfg}
	if dirty {
		_ = store.Save()
	}
	return store, nil
}

func normalizeUpstream(upstream *UpstreamConfig, index int, cfg *Config) {
	if upstream.ID == "" {
		taken := make(map[string]bool)
		for i, u := range cfg.Upstream {
			if i != index && u.ID != "" {
				taken[u.ID] = true
			}
		}
		for {
			id := randomHex(8)
			if !taken[id] {
				upstream.ID = id
				break
			}
		}
	}
	upstream.URL = strings.TrimRight(upstream.URL, "/")
	normalizeStreamingURLs(upstream)
	if upstream.Name == "" {
		upstream.Name = fmt.Sprintf("Server %d", index+1)
	}
	if upstream.PlaybackMode == "" {
		upstream.PlaybackMode = cfg.Playback.Mode
	}
	if upstream.SpoofClient == "" {
		upstream.SpoofClient = "none"
	}
	// Migrate legacy "official" spoofClient to "custom" with the original official profile values.
	if upstream.SpoofClient == "official" {
		upstream.SpoofClient = "custom"
		if upstream.CustomUserAgent == "" {
			upstream.CustomUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Emby/1.0.0"
		}
		if upstream.CustomClient == "" {
			upstream.CustomClient = "Emby Web"
		}
		if upstream.CustomClientVersion == "" {
			upstream.CustomClientVersion = "4.8.3.0"
		}
		if upstream.CustomDeviceName == "" {
			upstream.CustomDeviceName = "Chrome Windows"
		}
		if upstream.CustomDeviceId == "" {
			upstream.CustomDeviceId = "official-spoof-id"
		}
	}
}

// normalizeStreamingURLs canonicalizes the ordered stream-base list: trims and
// drops empties, folds the legacy single streamingUrl key in, removes
// duplicates, and keeps StreamingURL pointing at the first entry (or "" when
// the list is empty, which means "same as the API address").
func normalizeStreamingURLs(upstream *UpstreamConfig) {
	merged := make([]string, 0, len(upstream.StreamingURLs)+1)
	merged = append(merged, upstream.StreamingURLs...)
	if upstream.StreamingURL != "" {
		merged = append(merged, upstream.StreamingURL)
	}
	seen := make(map[string]bool, len(merged))
	upstream.StreamingURLs = upstream.StreamingURLs[:0]
	for _, raw := range merged {
		trimmed := strings.TrimRight(strings.TrimSpace(raw), "/")
		if trimmed == "" || seen[trimmed] {
			continue
		}
		seen[trimmed] = true
		upstream.StreamingURLs = append(upstream.StreamingURLs, trimmed)
	}
	if len(upstream.StreamingURLs) > 0 {
		upstream.StreamingURL = upstream.StreamingURLs[0]
	} else {
		upstream.StreamingURL = ""
	}
}

func (s *ConfigStore) Snapshot() Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	clone := *s.config
	clone.Proxies = append([]ProxyConfig(nil), s.config.Proxies...)
	clone.Upstream = append([]UpstreamConfig(nil), s.config.Upstream...)
	for i := range clone.Upstream {
		clone.Upstream[i].StreamingURLs = append([]string(nil), s.config.Upstream[i].StreamingURLs...)
	}
	return clone
}

func (s *ConfigStore) Mutate(fn func(cfg *Config) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return fn(s.config)
}

func (s *ConfigStore) Replace(cfg Config) {
	s.mu.Lock()
	defer s.mu.Unlock()
	clone := cfg
	clone.Proxies = append([]ProxyConfig(nil), cfg.Proxies...)
	clone.Upstream = append([]UpstreamConfig(nil), cfg.Upstream...)
	for i := range clone.Upstream {
		clone.Upstream[i].StreamingURLs = append([]string(nil), cfg.Upstream[i].StreamingURLs...)
	}
	s.config = &clone
}

func (s *ConfigStore) Save() error {
	s.mu.RLock()
	content := renderConfigYAML(s.config)
	path := s.config.Path
	s.mu.RUnlock()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return WriteFileAtomic(path, []byte(content), 0o600)
}

func parseConfigYAML(raw string) (*Config, error) {
	cfg := &Config{Proxies: []ProxyConfig{}, Upstream: []UpstreamConfig{}}
	section := ""
	inList := false
	listName := ""
	currentIndex := -1

	lines := strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n")
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		line = stripYAMLComment(line)
		indent := len(line) - len(strings.TrimLeft(line, " "))
		trimmed := strings.TrimSpace(line)

		if indent == 0 {
			inList = false
			currentIndex = -1
			listName = ""
			parts := strings.SplitN(trimmed, ":", 2)
			key := strings.TrimSpace(parts[0])
			val := ""
			if len(parts) > 1 {
				val = strings.TrimSpace(parts[1])
			}
			switch key {
			case "server", "admin", "playback", "timeouts":
				section = key
			case "proxies", "upstream":
				section = key
				if val == "[]" || val == "" {
					continue
				}
			case "dataDir":
				cfg.DataDir = parseStringValue(val)
			}
			continue
		}

		if (section == "proxies" || section == "upstream") && indent == 2 && strings.HasPrefix(trimmed, "-") {
			inList = true
			listName = section
			currentIndex++
			if listName == "proxies" {
				cfg.Proxies = append(cfg.Proxies, ProxyConfig{})
			} else {
				cfg.Upstream = append(cfg.Upstream, UpstreamConfig{FollowRedirects: true})
			}
			trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, "-"))
			if trimmed != "" {
				key, value := parseKeyValue(trimmed)
				assignListField(cfg, listName, currentIndex, key, value)
			}
			continue
		}

		key, value := parseKeyValue(trimmed)
		if inList && currentIndex >= 0 {
			assignListField(cfg, listName, currentIndex, key, value)
			continue
		}
		assignSectionField(cfg, section, key, value)
	}
	return cfg, nil
}

// stripYAMLComment removes a trailing # comment while respecting quoted strings.
func stripYAMLComment(line string) string {
	inSingle := false
	inDouble := false
	for i, ch := range line {
		switch ch {
		case '\'':
			if !inDouble {
				inSingle = !inSingle
			}
		case '"':
			if !inSingle {
				inDouble = !inDouble
			}
		case '#':
			if !inSingle && !inDouble {
				return line[:i]
			}
		}
	}
	return line
}

func parseKeyValue(line string) (string, string) {
	parts := strings.SplitN(line, ":", 2)
	key := strings.TrimSpace(parts[0])
	value := ""
	if len(parts) > 1 {
		value = strings.TrimSpace(parts[1])
	}
	return key, value
}

// parseStringValue unquotes a scalar the way renderConfigYAML writes it. Only a
// matching pair of outer quotes is removed, so a value that legitimately starts or
// ends with a quote survives a save/load cycle.
func parseStringValue(value string) string {
	value = strings.TrimSpace(value)
	if value == "null" {
		return ""
	}
	if len(value) < 2 || value[0] != value[len(value)-1] || (value[0] != '\'' && value[0] != '"') {
		return value
	}
	inner := value[1 : len(value)-1]
	if value[0] == '\'' {
		// yamlStr escapes a single quote inside a scalar by doubling it.
		return strings.ReplaceAll(inner, "''", "'")
	}
	return inner
}

func parseBoolValue(value string) bool {
	value = strings.ToLower(parseStringValue(value))
	return value == "true" || value == "yes" || value == "1"
}

// parseStringListValue parses a YAML flow sequence of scalars, e.g.
// ["https://a", 'https://b']. It exists because the config parser is line
// based and cannot read a block list nested inside an upstream entry; a flow
// sequence is a single line and survives it. A bare scalar (no brackets)
// yields a one-element list so hand-edited files can write either shape.
func parseStringListValue(value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	if !strings.HasPrefix(value, "[") || !strings.HasSuffix(value, "]") {
		return []string{parseStringValue(value)}
	}
	inner := strings.TrimSpace(value[1 : len(value)-1])
	if inner == "" {
		return nil
	}
	items := strings.Split(inner, ",")
	out := make([]string, 0, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		out = append(out, parseStringValue(item))
	}
	return out
}

func parseIntValue(value string) int {
	n, _ := strconv.Atoi(parseStringValue(value))
	return n
}

func assignSectionField(cfg *Config, section, key, value string) {
	switch section {
	case "server":
		switch key {
		case "port":
			cfg.Server.Port = parseIntValue(value)
		case "name":
			cfg.Server.Name = parseStringValue(value)
		case "id":
			cfg.Server.ID = parseStringValue(value)
		case "trustProxy":
			cfg.Server.TrustProxy = parseBoolValue(value)
		}
	case "admin":
		switch key {
		case "username":
			cfg.Admin.Username = parseStringValue(value)
		case "password":
			cfg.Admin.Password = parseStringValue(value)
		}
	case "playback":
		if key == "mode" {
			cfg.Playback.Mode = parseStringValue(value)
		}
	case "timeouts":
		switch key {
		case "api":
			cfg.Timeouts.API = parseIntValue(value)
		case "global":
			cfg.Timeouts.Global = parseIntValue(value)
		case "login":
			cfg.Timeouts.Login = parseIntValue(value)
		case "healthCheck":
			cfg.Timeouts.HealthCheck = parseIntValue(value)
		case "healthInterval":
			cfg.Timeouts.HealthInterval = parseIntValue(value)
		case "searchGracePeriod":
			cfg.Timeouts.SearchGracePeriod = parseIntValue(value)
		case "metadataGracePeriod":
			cfg.Timeouts.MetadataGracePeriod = parseIntValue(value)
		case "latestGracePeriod":
			cfg.Timeouts.LatestGracePeriod = parseIntValue(value)
		}
	}
}

func assignListField(cfg *Config, listName string, index int, key, value string) {
	switch listName {
	case "proxies":
		proxy := &cfg.Proxies[index]
		switch key {
		case "id":
			proxy.ID = parseStringValue(value)
		case "name":
			proxy.Name = parseStringValue(value)
		case "url":
			proxy.URL = parseStringValue(value)
		}
	case "upstream":
		upstream := &cfg.Upstream[index]
		switch key {
		case "id":
			upstream.ID = parseStringValue(value)
		case "name":
			upstream.Name = parseStringValue(value)
		case "url":
			upstream.URL = parseStringValue(value)
		case "username":
			upstream.Username = parseStringValue(value)
		case "password":
			upstream.Password = parseStringValue(value)
		case "apiKey":
			upstream.APIKey = parseStringValue(value)
		case "playbackMode":
			upstream.PlaybackMode = parseStringValue(value)
		case "spoofClient":
			upstream.SpoofClient = parseStringValue(value)
		case "followRedirects":
			upstream.FollowRedirects = parseBoolValue(value)
		case "proxyId":
			upstream.ProxyID = parseStringValue(value)
		case "priorityMetadata":
			upstream.PriorityMetadata = parseBoolValue(value)
		case "streamingUrl":
			upstream.StreamingURL = parseStringValue(value)
		case "streamingUrls":
			upstream.StreamingURLs = append(upstream.StreamingURLs, parseStringListValue(value)...)
		case "customUserAgent":
			upstream.CustomUserAgent = parseStringValue(value)
		case "customClient":
			upstream.CustomClient = parseStringValue(value)
		case "customClientVersion":
			upstream.CustomClientVersion = parseStringValue(value)
		case "customDeviceName":
			upstream.CustomDeviceName = parseStringValue(value)
		case "customDeviceId":
			upstream.CustomDeviceId = parseStringValue(value)
		case "maxConcurrent":
			upstream.MaxConcurrent = parseIntValue(value)
		}
	}
}

func yamlStr(s string) string {
	// Use single-quoted YAML scalar; escape internal single quotes by doubling them.
	// Values containing newlines are not supported: the parser reads line by line.
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// joinQuoted renders a flow-sequence body: 'a', 'b'. Used only by
// renderConfigYAML for streamingUrls.
func joinQuoted(items []string) string {
	parts := make([]string, 0, len(items))
	for _, item := range items {
		parts = append(parts, yamlStr(item))
	}
	return strings.Join(parts, ", ")
}

func renderConfigYAML(cfg *Config) string {
	var b strings.Builder
	b.WriteString("server:\n")
	fmt.Fprintf(&b, "  port: %d\n", cfg.Server.Port)
	fmt.Fprintf(&b, "  name: %s\n", yamlStr(cfg.Server.Name))
	fmt.Fprintf(&b, "  id: %s\n", yamlStr(cfg.Server.ID))
	if cfg.Server.TrustProxy {
		b.WriteString("  trustProxy: true\n")
	}
	b.WriteString("\n")

	b.WriteString("admin:\n")
	fmt.Fprintf(&b, "  username: %s\n", yamlStr(cfg.Admin.Username))
	fmt.Fprintf(&b, "  password: %s\n\n", yamlStr(cfg.Admin.Password))

	b.WriteString("playback:\n")
	fmt.Fprintf(&b, "  mode: %s\n\n", yamlStr(cfg.Playback.Mode))

	b.WriteString("timeouts:\n")
	fmt.Fprintf(&b, "  api: %d\n", cfg.Timeouts.API)
	fmt.Fprintf(&b, "  global: %d\n", cfg.Timeouts.Global)
	fmt.Fprintf(&b, "  login: %d\n", cfg.Timeouts.Login)
	fmt.Fprintf(&b, "  healthCheck: %d\n", cfg.Timeouts.HealthCheck)
	fmt.Fprintf(&b, "  healthInterval: %d\n", cfg.Timeouts.HealthInterval)
	fmt.Fprintf(&b, "  searchGracePeriod: %d\n", cfg.Timeouts.SearchGracePeriod)
	fmt.Fprintf(&b, "  metadataGracePeriod: %d\n", cfg.Timeouts.MetadataGracePeriod)
	fmt.Fprintf(&b, "  latestGracePeriod: %d\n\n", cfg.Timeouts.LatestGracePeriod)

	if len(cfg.Proxies) == 0 {
		b.WriteString("proxies: []\n\n")
	} else {
		b.WriteString("proxies:\n")
		for _, proxy := range cfg.Proxies {
			fmt.Fprintf(&b, "  - id: %s\n", yamlStr(proxy.ID))
			fmt.Fprintf(&b, "    name: %s\n", yamlStr(proxy.Name))
			fmt.Fprintf(&b, "    url: %s\n", yamlStr(proxy.URL))
		}
		b.WriteString("\n")
	}

	if len(cfg.Upstream) == 0 {
		b.WriteString("upstream: []\n")
	} else {
		b.WriteString("upstream:\n")
		for _, upstream := range cfg.Upstream {
			if upstream.ID != "" {
				fmt.Fprintf(&b, "  - id: %s\n", yamlStr(upstream.ID))
				fmt.Fprintf(&b, "    name: %s\n", yamlStr(upstream.Name))
			} else {
				fmt.Fprintf(&b, "  - name: %s\n", yamlStr(upstream.Name))
			}
			fmt.Fprintf(&b, "    url: %s\n", yamlStr(upstream.URL))
			if upstream.APIKey != "" {
				fmt.Fprintf(&b, "    apiKey: %s\n", yamlStr(upstream.APIKey))
			} else {
				fmt.Fprintf(&b, "    username: %s\n", yamlStr(upstream.Username))
				fmt.Fprintf(&b, "    password: %s\n", yamlStr(upstream.Password))
			}
			if upstream.PlaybackMode != cfg.Playback.Mode && upstream.PlaybackMode != "" {
				fmt.Fprintf(&b, "    playbackMode: %s\n", yamlStr(upstream.PlaybackMode))
			}
			if upstream.SpoofClient != "" && upstream.SpoofClient != "none" {
				fmt.Fprintf(&b, "    spoofClient: %s\n", yamlStr(upstream.SpoofClient))
			}
			if len(upstream.StreamingURLs) > 1 {
				fmt.Fprintf(&b, "    streamingUrls: [%s]\n", joinQuoted(upstream.StreamingURLs))
			} else if len(upstream.StreamingURLs) == 1 {
				fmt.Fprintf(&b, "    streamingUrl: %s\n", yamlStr(upstream.StreamingURLs[0]))
			}
			if upstream.SpoofClient == "custom" {
				if upstream.CustomUserAgent != "" {
					fmt.Fprintf(&b, "    customUserAgent: %s\n", yamlStr(upstream.CustomUserAgent))
				}
				if upstream.CustomClient != "" {
					fmt.Fprintf(&b, "    customClient: %s\n", yamlStr(upstream.CustomClient))
				}
				if upstream.CustomClientVersion != "" {
					fmt.Fprintf(&b, "    customClientVersion: %s\n", yamlStr(upstream.CustomClientVersion))
				}
				if upstream.CustomDeviceName != "" {
					fmt.Fprintf(&b, "    customDeviceName: %s\n", yamlStr(upstream.CustomDeviceName))
				}
				if upstream.CustomDeviceId != "" {
					fmt.Fprintf(&b, "    customDeviceId: %s\n", yamlStr(upstream.CustomDeviceId))
				}
			}
			if !upstream.FollowRedirects {
				fmt.Fprintf(&b, "    followRedirects: false\n")
			}
			if upstream.ProxyID != "" {
				fmt.Fprintf(&b, "    proxyId: %s\n", yamlStr(upstream.ProxyID))
			}
			if upstream.PriorityMetadata {
				fmt.Fprintf(&b, "    priorityMetadata: true\n")
			}
			if upstream.MaxConcurrent > 0 {
				fmt.Fprintf(&b, "    maxConcurrent: %d\n", upstream.MaxConcurrent)
			}
		}
	}
	return b.String()
}
