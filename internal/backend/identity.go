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

func mergePassthroughHeaders(source http.Header) http.Header {
	headers := http.Header{}
	headers.Set("User-Agent", "Infuse/7.7.1 (iPhone; iOS 17.4.1; Scale/3.00)")
	headers.Set("X-Emby-Client", "Infuse")
	headers.Set("X-Emby-Client-Version", "7.7.1")
	headers.Set("X-Emby-Device-Name", "iPhone")
	headers.Set("X-Emby-Device-Id", "infuse-spoof-id")
	if source.Get("User-Agent") != "" {
		headers.Set("User-Agent", source.Get("User-Agent"))
	}
	for _, key := range []string{"X-Emby-Client", "X-Emby-Client-Version", "X-Emby-Device-Name", "X-Emby-Device-Id", "Accept", "Accept-Language", "X-Emby-Authorization", "Authorization"} {
		if source.Get(key) != "" {
			headers.Set(key, source.Get(key))
		}
	}
	return headers
}

func parseAuthorizationIdentity(header string) map[string]string {
	result := map[string]string{}
	if header == "" {
		return result
	}
	for _, prefix := range []string{"MediaBrowser ", "Emby "} {
		if strings.HasPrefix(header, prefix) {
			header = header[len(prefix):]
			break
		}
	}
	for _, part := range strings.Split(header, ",") {
		part = strings.TrimSpace(part)
		eqIdx := strings.Index(part, "=")
		if eqIdx < 0 {
			continue
		}
		key := strings.TrimSpace(part[:eqIdx])
		val := strings.TrimSpace(part[eqIdx+1:])
		if len(val) >= 2 && val[0] == '"' && val[len(val)-1] == '"' {
			val = val[1 : len(val)-1]
		}
		result[key] = val
	}
	return result
}

func normalizeCapturedHeaders(headers http.Header) http.Header {
	copied := http.Header{}
	for _, key := range []string{"User-Agent", "X-Emby-Client", "X-Emby-Client-Version", "X-Emby-Device-Name", "X-Emby-Device-Id", "Accept", "Accept-Language", "X-Emby-Authorization", "Authorization"} {
		if values := headers.Values(key); len(values) > 0 {
			copied[key] = append([]string(nil), values...)
		}
	}
	// Fill missing individual headers from compound authorization header
	if copied.Get("X-Emby-Client") == "" || copied.Get("X-Emby-Device-Name") == "" || copied.Get("X-Emby-Device-Id") == "" {
		for _, authKey := range []string{"X-Emby-Authorization", "Authorization"} {
			authVal := copied.Get(authKey)
			if authVal == "" {
				continue
			}
			parsed := parseAuthorizationIdentity(authVal)
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
