package backend

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// catalogSnapshotKeyVersion is part of the hash material. Bump it whenever the
// key composition or fill semantics change so old entries cannot be reused.
const catalogSnapshotKeyVersion = "catalog-snapshot-key-v1"

// catalogSnapshotKey hashes everything that can change one upstream's raw
// candidate set. It deliberately returns only a hex digest: callers may log or
// compare keys, but never see credentials, tokens or raw query material.
//
// The key includes:
//   - the full configured and runtime upstream identity/config, including a
//     consistent UserID/AccessToken pair read under the client lock;
//   - the requesting proxy user, role, live grants, auth token and parental /
//     hidden-library policy;
//   - path, body, and the canonical full query with the proxy-owned
//     StartIndex/Limit removed.
//
// Fields, filters, sort order, RandomSeed, ParentId and every other client
// parameter therefore stay distinct. A query prepared with an internal
// Limit=5000 maps to the same key as the unmodified client query.
func catalogSnapshotKey(a *App, reqCtx *RequestContext, c *UpstreamClient, pathTemplate string, query url.Values, body any) (string, error) {
	if c == nil {
		return "", errors.New("catalog snapshot: nil upstream")
	}
	bodyCanonical, err := catalogSnapshotCanonicalBody(body)
	if err != nil {
		return "", err
	}
	upstream := c.snapshot()
	resolvedPath := strings.Replace(pathTemplate, "%s", upstream.UserID, 1)

	var key strings.Builder
	writeCatalogKeyField(&key, "version", catalogSnapshotKeyVersion)
	writeCatalogKeyField(&key, "path.template", pathTemplate)
	writeCatalogKeyField(&key, "path", resolvedPath)
	writeCatalogKeyField(&key, "body", bodyCanonical)
	writeCatalogKeyField(&key, "query", catalogSnapshotCanonicalQuery(query))
	writeCatalogUpstreamKeyFields(&key, &upstream)
	writeCatalogProxyIdentityKeyFields(&key, a, reqCtx)
	if reqCtx != nil {
		headers, marshalErr := json.Marshal(reqCtx.Headers)
		if marshalErr != nil {
			return "", marshalErr
		}
		writeCatalogKeyField(&key, "request.headers", catalogSnapshotSecretDigest(string(headers)))
	}
	if a != nil && a.ConfigStore != nil {
		configuration, marshalErr := json.Marshal(a.ConfigStore.Snapshot())
		if marshalErr != nil {
			return "", marshalErr
		}
		writeCatalogKeyField(&key, "configuration", catalogSnapshotSecretDigest(string(configuration)))
	}
	if a != nil && a.UALibrary != nil {
		profiles, marshalErr := json.Marshal(a.UALibrary.Snapshot().Resident)
		if marshalErr != nil {
			return "", marshalErr
		}
		writeCatalogKeyField(&key, "ua.profiles", catalogSnapshotSecretDigest(string(profiles)))
	}

	sum := sha256.Sum256([]byte(key.String()))
	return hex.EncodeToString(sum[:]), nil
}

func writeCatalogKeyField(b *strings.Builder, name, value string) {
	b.WriteString(name)
	b.WriteByte('=')
	b.WriteString(strconv.Itoa(len(value)))
	b.WriteByte(':')
	b.WriteString(value)
	b.WriteByte('\n')
}

func writeCatalogKeyInt(b *strings.Builder, name string, value int) {
	writeCatalogKeyField(b, name, strconv.Itoa(value))
}

func writeCatalogKeyBool(b *strings.Builder, name string, value bool) {
	writeCatalogKeyField(b, name, strconv.FormatBool(value))
}

// writeCatalogUpstreamKeyFields records every configuration field that can
// affect the upstream's response or its authenticated identity. Credentials are
// reduced to digests before they reach the key material.
func writeCatalogUpstreamKeyFields(b *strings.Builder, c *UpstreamClient) {
	cfg := c.Config
	writeCatalogKeyField(b, "upstream.id", c.ID)
	writeCatalogKeyInt(b, "upstream.index", c.ServerIndex)
	writeCatalogKeyField(b, "upstream.name", c.Name)
	writeCatalogKeyField(b, "upstream.baseURL", c.BaseURL)
	writeCatalogKeyField(b, "upstream.streamBaseURL", c.StreamBaseURL)
	for i, base := range c.StreamBaseURLs {
		writeCatalogKeyField(b, "upstream.streamBaseURL."+strconv.Itoa(i), base)
	}
	writeCatalogKeyBool(b, "upstream.online", c.Online)
	writeCatalogKeyField(b, "upstream.userID", c.UserID)
	writeCatalogKeyField(b, "upstream.accessToken", catalogSnapshotSecretDigest(c.AccessToken))
	writeCatalogKeyField(b, "upstream.serverKey", c.serverKey)

	writeCatalogKeyField(b, "config.id", cfg.ID)
	writeCatalogKeyField(b, "config.name", cfg.Name)
	writeCatalogKeyField(b, "config.url", cfg.URL)
	writeCatalogKeyField(b, "config.username", cfg.Username)
	writeCatalogKeyField(b, "config.password", catalogSnapshotSecretDigest(cfg.Password))
	writeCatalogKeyField(b, "config.apiKey", catalogSnapshotSecretDigest(cfg.APIKey))
	writeCatalogKeyField(b, "config.playbackMode", cfg.PlaybackMode)
	writeCatalogKeyField(b, "config.spoofClient", cfg.SpoofClient)
	writeCatalogKeyField(b, "config.uaProfileID", cfg.UAProfileID)
	writeCatalogKeyBool(b, "config.followRedirects", cfg.FollowRedirects)
	writeCatalogKeyField(b, "config.proxyID", cfg.ProxyID)
	writeCatalogKeyBool(b, "config.priorityMetadata", cfg.PriorityMetadata)
	writeCatalogKeyBool(b, "config.aggregationDisabled", cfg.AggregationDisabled)
	writeCatalogKeyField(b, "config.streamingURL", cfg.StreamingURL)
	for i, rawURL := range cfg.StreamingURLs {
		writeCatalogKeyField(b, "config.streamingURL."+strconv.Itoa(i), rawURL)
	}
	writeCatalogKeyField(b, "config.customUserAgent", cfg.CustomUserAgent)
	writeCatalogKeyField(b, "config.customClient", cfg.CustomClient)
	writeCatalogKeyField(b, "config.customClientVersion", cfg.CustomClientVersion)
	writeCatalogKeyField(b, "config.customDeviceName", cfg.CustomDeviceName)
	writeCatalogKeyField(b, "config.customDeviceId", cfg.CustomDeviceId)
	writeCatalogKeyInt(b, "config.maxConcurrent", cfg.MaxConcurrent)
	writeCatalogKeyBool(b, "config.pagedScan", cfg.PagedScan)
	writeCatalogKeyBool(b, "config.directRedirect", cfg.DirectRedirect)
}

func writeCatalogProxyIdentityKeyFields(b *strings.Builder, a *App, reqCtx *RequestContext) {
	if reqCtx == nil {
		writeCatalogKeyField(b, "proxy.user", "")
		return
	}
	if user := reqCtx.ProxyUser; user != nil {
		writeCatalogKeyField(b, "proxy.userID", user.UserID)
		writeCatalogKeyField(b, "proxy.username", user.Username)
		writeCatalogKeyField(b, "proxy.role", user.Role)
		writeCatalogKeyField(b, "proxy.token", catalogSnapshotSecretDigest(reqCtx.ProxyToken))
		allowed := append([]string(nil), user.AllowedServers...)
		sort.Strings(allowed)
		for i, serverID := range allowed {
			writeCatalogKeyField(b, "proxy.allowed."+strconv.Itoa(i), serverID)
		}
	}
	if reqCtx.Parental != nil {
		writeCatalogKeyInt(b, "policy.maxRating", reqCtx.Parental.maxRating)
		writeCatalogKeyBool(b, "policy.blockUnrated", reqCtx.Parental.blockUnrated)
	}
	if a == nil {
		return
	}
	// The live user record is the authoritative policy source. Reading it here
	// makes a parental/grants change invalidate snapshots even before a new
	// token is issued.
	if a.UserStore != nil && reqCtx.ProxyUser != nil {
		if live := a.UserStore.Get(reqCtx.ProxyUser.UserID); live != nil {
			writeCatalogKeyInt(b, "policy.live.maxRating", live.MaxParentalRating)
			writeCatalogKeyBool(b, "policy.live.blockUnrated", live.BlockUnratedItems)
			writeCatalogKeyBool(b, "policy.live.enabled", live.Enabled)
			allowed := append([]string(nil), live.AllowedServers...)
			sort.Strings(allowed)
			for i, serverID := range allowed {
				writeCatalogKeyField(b, "policy.live.allowed."+strconv.Itoa(i), serverID)
			}
		}
	}
	if a.HiddenLibraries != nil && reqCtx.ProxyUser != nil {
		hidden := a.HiddenLibraries.UserHiddenJSON(visibilityUserID(reqCtx.ProxyUser))
		servers := make([]string, 0, len(hidden))
		for serverID := range hidden {
			servers = append(servers, serverID)
		}
		sort.Strings(servers)
		for _, serverID := range servers {
			ids := append([]string(nil), hidden[serverID]...)
			sort.Strings(ids)
			for i, libraryID := range ids {
				writeCatalogKeyField(b, "policy.hidden."+serverID+"."+strconv.Itoa(i), libraryID)
			}
		}
	}
}

// catalogSnapshotSecretDigest keeps raw credentials out of intermediate key
// material as well as out of logs. An empty secret has an empty digest so its
// presence stays distinguishable.
func catalogSnapshotSecretDigest(secret string) string {
	if secret == "" {
		return ""
	}
	sum := sha256.Sum256([]byte("catalog-snapshot-secret\x00" + secret))
	return hex.EncodeToString(sum[:])
}

// catalogSnapshotCanonicalBody converts a request body to deterministic JSON.
// Non-JSON byte bodies are kept as-is; they are still hashed and never logged.
func catalogSnapshotCanonicalBody(body any) (string, error) {
	if body == nil {
		return "", nil
	}
	var raw []byte
	switch typed := body.(type) {
	case []byte:
		raw = typed
	case json.RawMessage:
		raw = typed
	default:
		encoded, err := json.Marshal(body)
		if err != nil {
			return "", err
		}
		raw = encoded
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return string(raw), nil
	}
	canonical, err := json.Marshal(decoded)
	if err != nil {
		return "", err
	}
	return string(canonical), nil
}
