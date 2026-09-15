package backend

import (
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
)

type CapturedClientInfo struct {
	UserAgent     string `json:"userAgent"`
	Client        string `json:"client"`
	ClientVersion string `json:"clientVersion"`
	DeviceName    string `json:"deviceName"`
	DeviceID      string `json:"deviceId"`
	CapturedAt    string `json:"capturedAt"`
}

type capturedEntry struct {
	headers    http.Header
	capturedAt string
	sequence   uint64
}

type ResolvedPassthroughHeaders struct {
	Source  string
	Headers http.Header
}

type ClientIdentityService struct {
	mu                  sync.RWMutex
	entries             map[string]capturedEntry
	latestInfo          *capturedEntry
	latestCaptured      *capturedEntry
	lastSuccessByServer map[string]capturedEntry
	captureListeners    []func(string, http.Header)
	persistence         *IdentityPersistence
	sequence            atomic.Uint64
}

func NewClientIdentityService() *ClientIdentityService {
	return newClientIdentityService(nil)
}

func NewClientIdentityServiceFromDetectedConfig() *ClientIdentityService {
	return newClientIdentityService(newIdentityPersistenceFromDetectedConfig())
}

func newClientIdentityService(persistence *IdentityPersistence) *ClientIdentityService {
	svc := &ClientIdentityService{
		entries:             map[string]capturedEntry{},
		lastSuccessByServer: map[string]capturedEntry{},
		persistence:         persistence,
	}
	if svc.persistence != nil {
		if snapshot, err := svc.persistence.Load(); err == nil {
			svc.applyPersistenceSnapshot(snapshot)
		}
	}
	setActiveIdentityService(svc)
	return svc
}

func (s *ClientIdentityService) SetCaptured(token string, headers http.Header) {
	if token == "" {
		return
	}
	entry := s.newCapturedEntry(headers)
	listeners := s.storeCapturedEntry(token, entry)
	s.notifyCaptureListeners(listeners, token, entry.headers)
}

func (s *ClientIdentityService) SaveLatestCapturedHeaders(headers http.Header) {
	entry := s.newCapturedEntry(headers)
	s.mu.Lock()
	s.latestCaptured = cloneCapturedEntry(entry)
	s.mu.Unlock()
	s.persistLatest(entry)
}

func (s *ClientIdentityService) storeCapturedEntry(token string, entry capturedEntry) []func(string, http.Header) {
	s.mu.Lock()
	s.entries[token] = entry
	s.latestInfo = cloneCapturedEntry(entry)
	listeners := append([]func(string, http.Header){}, s.captureListeners...)
	s.mu.Unlock()
	return listeners
}

func (s *ClientIdentityService) GetCaptured(token string) http.Header {
	s.mu.RLock()
	defer s.mu.RUnlock()
	entry, ok := s.entries[token]
	if !ok {
		return http.Header{}
	}
	return cloneHeader(entry.headers)
}

func (s *ClientIdentityService) DeleteCaptured(token string) {
	if token == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.entries, token)
}

func (s *ClientIdentityService) Clear() {
	s.mu.Lock()
	s.entries = map[string]capturedEntry{}
	s.latestInfo = nil
	s.latestCaptured = nil
	s.lastSuccessByServer = map[string]capturedEntry{}
	s.captureListeners = nil
	s.persistence = nil
	s.mu.Unlock()
	s.sequence.Store(0)
}

func (s *ClientIdentityService) GetInfo() *CapturedClientInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	entry := s.latestInfo
	if entry == nil {
		entry = s.latestCaptured
	}
	if entry == nil {
		return nil
	}
	return &CapturedClientInfo{
		UserAgent:     entry.headers.Get("User-Agent"),
		Client:        entry.headers.Get("X-Emby-Client"),
		ClientVersion: entry.headers.Get("X-Emby-Client-Version"),
		DeviceName:    entry.headers.Get("X-Emby-Device-Name"),
		DeviceID:      entry.headers.Get("X-Emby-Device-Id"),
		CapturedAt:    entry.capturedAt,
	}
}

func (s *ClientIdentityService) ResolvePassthroughHeadersForServer(liveHeaders http.Header, token, serverKey string) ResolvedPassthroughHeaders {
	if hasPassthroughIdentity(liveHeaders) {
		return ResolvedPassthroughHeaders{Source: "live-request", Headers: mergePassthroughHeaders(liveHeaders)}
	}
	if token != "" {
		if captured := s.GetCaptured(token); hasPassthroughIdentity(captured) {
			return ResolvedPassthroughHeaders{Source: "captured-token", Headers: mergePassthroughHeaders(captured)}
		}
	}
	if serverKey != "" {
		if captured := s.GetLastSuccess(serverKey); hasPassthroughIdentity(captured) {
			return ResolvedPassthroughHeaders{Source: "last-success", Headers: mergePassthroughHeaders(captured)}
		}
	}
	if captured := s.GetLatestCaptured(); hasPassthroughIdentity(captured) {
		return ResolvedPassthroughHeaders{Source: "captured-latest", Headers: mergePassthroughHeaders(captured)}
	}
	return ResolvedPassthroughHeaders{Source: "infuse-fallback", Headers: mergePassthroughHeaders(http.Header{})}
}

func (s *ClientIdentityService) GetLatestCaptured() http.Header {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.latestCaptured == nil {
		return http.Header{}
	}
	return cloneHeader(s.latestCaptured.headers)
}

func (s *ClientIdentityService) SaveLastSuccess(serverKey string, headers http.Header) {
	if serverKey == "" {
		return
	}
	entry := s.newCapturedEntry(headers)
	s.mu.Lock()
	s.lastSuccessByServer[serverKey] = entry
	s.mu.Unlock()
	s.persistLastSuccess(serverKey, entry)
}

func (s *ClientIdentityService) GetLastSuccess(serverKey string) http.Header {
	s.mu.RLock()
	defer s.mu.RUnlock()
	entry, ok := s.lastSuccessByServer[serverKey]
	if !ok {
		return http.Header{}
	}
	return cloneHeader(entry.headers)
}

// HasCapturedHeaders returns true if we have any captured identity usable for
// the given server key: a specific last-success entry, or a global latestCaptured
// fallback (indicating a real client has connected recently).
func (s *ClientIdentityService) HasCapturedHeaders(serverKey string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if entry, ok := s.lastSuccessByServer[serverKey]; ok && isRealClientIdentity(entry.headers) {
		return true
	}
	if s.latestCaptured != nil && isRealClientIdentity(s.latestCaptured.headers) {
		return true
	}
	return false
}

func (s *ClientIdentityService) RegisterCaptureListener(listener func(string, http.Header)) {
	if listener == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.captureListeners = append(s.captureListeners, listener)
}

func (s *ClientIdentityService) newCapturedEntry(headers http.Header) capturedEntry {
	return capturedEntry{
		headers:    normalizeCapturedHeaders(headers),
		capturedAt: nowRFC3339(),
		sequence:   s.sequence.Add(1),
	}
}

func (s *ClientIdentityService) applyPersistenceSnapshot(snapshot identityPersistenceSnapshot) {
	if snapshot.LatestCaptured != nil {
		entry := s.hydrateCapturedEntry(snapshot.LatestCaptured.Headers, snapshot.LatestCaptured.CapturedAt)
		s.latestCaptured = cloneCapturedEntry(entry)
		s.latestInfo = cloneCapturedEntry(entry)
	}
	for serverKey, persisted := range snapshot.LastSuccessByServer {
		entry := s.hydrateCapturedEntry(persisted.Headers, persisted.CapturedAt)
		s.lastSuccessByServer[serverKey] = entry
	}
}

func (s *ClientIdentityService) hydrateCapturedEntry(headers http.Header, capturedAt string) capturedEntry {
	return capturedEntry{
		headers:    normalizeCapturedHeaders(headers),
		capturedAt: capturedAt,
		sequence:   s.sequence.Add(1),
	}
}

func (s *ClientIdentityService) persistLatest(entry capturedEntry) {
	if s.persistence == nil {
		return
	}
	_ = s.persistence.SaveLatestCaptured(entry.headers, entry.capturedAt)
}

func (s *ClientIdentityService) persistLastSuccess(serverKey string, entry capturedEntry) {
	if s.persistence == nil {
		return
	}
	_ = s.persistence.SaveLastSuccess(serverKey, entry.headers, entry.capturedAt)
}

func (s *ClientIdentityService) notifyCaptureListeners(listeners []func(string, http.Header), token string, headers http.Header) {
	for _, listener := range listeners {
		listener(token, cloneHeader(headers))
	}
}

// mergePassthroughHeaders builds the device-identity header set for one outbound
// request. Only client/device behaviour fields are copied from the source; the
// compound authorization header is read for its device fields and then dropped,
// so a captured local token or user ID can never be forwarded upstream. The
// separate device headers win over the compound header's fields, so a real client
// is not pushed back to the default profile.
func mergePassthroughHeaders(source http.Header) http.Header {
	headers := http.Header{}
	headers.Set("User-Agent", "Infuse/7.7.1 (iPhone; iOS 17.4.1; Scale/3.00)")
	headers.Set("X-Emby-Client", "Infuse")
	headers.Set("X-Emby-Client-Version", "7.7.1")
	headers.Set("X-Emby-Device-Name", "iPhone")
	headers.Set("X-Emby-Device-Id", "infuse-spoof-id")

	for _, key := range []string{"User-Agent", "X-Emby-Client", "X-Emby-Client-Version", "X-Emby-Device-Name", "X-Emby-Device-Id", "Accept", "Accept-Language"} {
		if value := source.Get(key); value != "" {
			headers.Set(key, value)
		}
	}

	// Fill only what the client did not send as its own header.
	if headers.Get("X-Emby-Client") == "Infuse" || headers.Get("X-Emby-Device-Id") == "infuse-spoof-id" {
		for _, authKey := range []string{"X-Emby-Authorization", "Authorization"} {
			authValue := source.Get(authKey)
			if authValue == "" {
				continue
			}
			parsed, ok := parseAuthorizationIdentityStrict(authValue)
			if !ok {
				break
			}
			for _, field := range []struct {
				header string
				param  string
			}{
				{"X-Emby-Client", "Client"},
				{"X-Emby-Client-Version", "Version"},
				{"X-Emby-Device-Name", "Device"},
				{"X-Emby-Device-Id", "DeviceId"},
			} {
				if headers.Get(field.header) != "" && headers.Get(field.header) != "Infuse" && headers.Get(field.header) != "infuse-spoof-id" {
					continue
				}
				if value := parsed[field.param]; value != "" {
					headers.Set(field.header, value)
				}
			}
			break
		}
	}
	return headers
}

// parseAuthorizationIdentityStrict parses an Emby/MediaBrowser compound
// authorization header into its parameters. It is a real quoted-string tokenizer:
// parameters are split on the commas that sit outside a quoted value, and quoted
// values honour backslash escapes. A malformed header is reported as not parsed
// rather than half-read, because a half-read header is what lets a client value
// smuggle an extra parameter into a rebuilt header.
func parseAuthorizationIdentityStrict(header string) (map[string]string, bool) {
	result := map[string]string{}
	trimmed := strings.TrimSpace(header)
	if trimmed == "" {
		return result, true
	}
	for _, prefix := range []string{"MediaBrowser ", "Emby "} {
		if len(trimmed) >= len(prefix) && strings.EqualFold(trimmed[:len(prefix)], prefix) {
			trimmed = trimmed[len(prefix):]
			break
		}
	}

	index := 0
	for index < len(trimmed) {
		for index < len(trimmed) && (trimmed[index] == ',' || trimmed[index] == ' ' || trimmed[index] == '	') {
			index++
		}
		if index >= len(trimmed) {
			break
		}
		keyStart := index
		for index < len(trimmed) && trimmed[index] != '=' && trimmed[index] != ',' {
			index++
		}
		if index >= len(trimmed) || trimmed[index] != '=' {
			return nil, false
		}
		key := strings.TrimSpace(trimmed[keyStart:index])
		index++ // consume '='
		for index < len(trimmed) && (trimmed[index] == ' ' || trimmed[index] == '	') {
			index++
		}
		var value string
		if index < len(trimmed) && trimmed[index] == '"' {
			index++
			var builder strings.Builder
			closed := false
			for index < len(trimmed) {
				ch := trimmed[index]
				if ch == 0x5C && index+1 < len(trimmed) {
					builder.WriteByte(trimmed[index+1])
					index += 2
					continue
				}
				if ch == '"' {
					index++
					closed = true
					break
				}
				builder.WriteByte(ch)
				index++
			}
			if !closed {
				return nil, false
			}
			value = builder.String()
		} else {
			valueStart := index
			for index < len(trimmed) && trimmed[index] != ',' {
				index++
			}
			value = strings.TrimSpace(trimmed[valueStart:index])
		}
		if key == "" {
			return nil, false
		}
		result[key] = value
	}
	return result, true
}

// parseAuthorizationIdentity keeps the permissive contract for callers that read
// best-effort device details. It returns an empty map for a malformed header.
func parseAuthorizationIdentity(header string) map[string]string {
	parsed, ok := parseAuthorizationIdentityStrict(header)
	if !ok {
		return map[string]string{}
	}
	return parsed
}

// capturedHeaderKeys are the only fields persisted for a captured client
// identity: device and client behaviour, never a credential.
var capturedHeaderKeys = []string{"User-Agent", "X-Emby-Client", "X-Emby-Client-Version", "X-Emby-Device-Name", "X-Emby-Device-Id", "Accept", "Accept-Language"}

// normalizeCapturedHeaders keeps only device/client information from a set of
// request headers. The client's UserId, Token and raw Authorization credential
// are read for their device fields and then dropped, so a stored identity can
// never be replayed as a credential.
func normalizeCapturedHeaders(headers http.Header) http.Header {
	copied := http.Header{}
	for _, key := range capturedHeaderKeys {
		if values := headers.Values(key); len(values) > 0 {
			copied[key] = append([]string(nil), values...)
		}
	}
	// Fill missing individual headers from compound authorization header. Both the
	// compound header's own parameters (aside from UserId/Token) and the
	// X-Emby-* headers are considered, with the explicit header winning.
	if copied.Get("X-Emby-Client") == "" || copied.Get("X-Emby-Device-Name") == "" || copied.Get("X-Emby-Device-Id") == "" || copied.Get("X-Emby-Client-Version") == "" {
		for _, authKey := range []string{"X-Emby-Authorization", "Authorization"} {
			authVal := headers.Get(authKey)
			if authVal == "" {
				continue
			}
			parsed, ok := parseAuthorizationIdentityStrict(authVal)
			if !ok {
				break
			}
			mapping := map[string]string{
				"X-Emby-Client":         parsed["Client"],
				"X-Emby-Client-Version": parsed["Version"],
				"X-Emby-Device-Name":    parsed["Device"],
				"X-Emby-Device-Id":      parsed["DeviceId"],
			}
			for hdr, val := range mapping {
				if copied.Get(hdr) == "" && val != "" {
					copied.Set(hdr, val)
				}
			}
			break
		}
	}
	return copied
}

func isRealClientIdentity(headers http.Header) bool {
	return hasPassthroughIdentity(headers) && headers.Get("X-Emby-Device-Id") != "infuse-spoof-id"
}

func hasPassthroughIdentity(headers http.Header) bool {
	if headers.Get("X-Emby-Client") != "" || headers.Get("X-Emby-Authorization") != "" || headers.Get("X-Emby-Device-Id") != "" || headers.Get("Authorization") != "" {
		return true
	}
	ua := strings.ToLower(strings.TrimSpace(headers.Get("User-Agent")))
	return strings.Contains(ua, "emby") || strings.Contains(ua, "infuse") || strings.Contains(ua, "jellyfin") || strings.Contains(ua, "swiftfin")
}

func cloneHeader(header http.Header) http.Header {
	cloned := http.Header{}
	for key, values := range header {
		cloned[key] = append([]string(nil), values...)
	}
	return cloned
}

func cloneCapturedEntry(entry capturedEntry) *capturedEntry {
	return &capturedEntry{
		headers:    cloneHeader(entry.headers),
		capturedAt: entry.capturedAt,
		sequence:   entry.sequence,
	}
}
