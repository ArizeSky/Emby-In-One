package backend

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// outboundAuthCarrier names the carriers that the preparation layer may rewrite.
// The names are used in the log summary: the values never are.
const (
	carrierPath  = "path"
	carrierQuery = "query"
	carrierBody  = "body"
	carrierAuth  = "auth"
)

// bodyEncodingSupport states whether a body could be inspected as JSON. An
// unsupported body is left byte-for-byte alone and reported as such.
const (
	bodyInspected   = "inspected"
	bodyNotJSON     = "not-json"
	bodyUnsupported = "unsupported-shape"
	bodyEmpty       = "empty"
)

// urlPreparationResult is the outcome of preparing a URL: the URL to send plus
// what changed, for the log summary.
type urlPreparationResult struct {
	url     *url.URL
	changed []string
}

// queryUserIDOutcome records how the UserId query rule resolved.
type queryUserIDOutcome struct {
	signal  userIDQuerySignal
	changed bool
}

// bodyUserIDOutcome records how the body rule resolved.
type bodyUserIDOutcome struct {
	support string
	changed bool
}

// hasJSONContentTypeHeader reports whether a request declares a JSON body.
func hasJSONContentTypeHeader(contentType string) bool {
	return isJSONContentType(contentType)
}

// findUserIDKey returns the key of m whose name equals UserId by a
// case-insensitive match, and whether such a key exists.
func findUserIDKey(m map[string]any) (string, bool) {
	for key := range m {
		if strings.EqualFold(key, "UserId") {
			return key, true
		}
	}
	return "", false
}

// copyValues keeps url.Values' multi-value shape while never sharing the
// underlying slices with the caller.
func copyValues(values url.Values) url.Values {
	cloned := make(url.Values, len(values))
	for key, rawValues := range values {
		cloned[key] = append([]string(nil), rawValues...)
	}
	return cloned
}

// userIDVariants lists the keys of values that name UserId in any case.
func userIDVariants(values url.Values) []string {
	var keys []string
	for key := range values {
		if strings.EqualFold(key, "UserId") {
			keys = append(keys, key)
		}
	}
	return keys
}

// classifyUserIDQuery inspects the final merged query and reports whether the
// values it carries name the current user.
func classifyUserIDQuery(values url.Values, reqCtx *RequestContext, auth upstreamAuthSnapshot, lookup *IdentifierLookup) queryUserIDOutcome {
	keys := userIDVariants(values)
	if len(keys) == 0 {
		return queryUserIDOutcome{signal: signalAbsent}
	}
	allCurrent := true
	any := false
	for _, key := range keys {
		for _, raw := range values[key] {
			any = true
			if !IsCurrentUserAlias(raw, reqCtx, auth, lookup) {
				allCurrent = false
			}
		}
	}
	if !any {
		return queryUserIDOutcome{signal: signalUnknown}
	}
	if allCurrent {
		return queryUserIDOutcome{signal: signalCurrentUser}
	}
	return queryUserIDOutcome{signal: signalUnknown}
}

// applyUserIDQuery removes every UserId spelling from the merged query and writes
// exactly one canonical UserId=userID. A field that was absent stays absent: the
// preparation layer never adds identity a client did not send.
func applyUserIDQuery(values url.Values, userID string) bool {
	keys := userIDVariants(values)
	if len(keys) == 0 {
		return false
	}
	changed := false
	for _, key := range keys {
		if values.Get(key) != userID || len(values[key]) != 1 {
			changed = true
		}
		values.Del(key)
	}
	if values.Get("UserId") != userID {
		changed = true
	}
	values.Set("UserId", userID)
	return changed
}

// stripAPIKeyVariants removes api_key/ApiKey in every spelling, whatever the case
// and whether it came from params or from the base URL. The upstream token is
// written by this same snapshot afterwards, so query token and identity can never
// come from two different authentication states.
func stripAPIKeyVariants(values url.Values) (bool, string) {
	changed := false
	token := ""
	for key := range values {
		switch strings.ToLower(key) {
		case "api_key", "apikey":
			if !changed && len(values[key]) > 0 {
				token = values[key][0]
			}
			changed = true
			values.Del(key)
		}
	}
	return changed, token
}

// urlPathPrefix separates the request path from the upstream base URL's own path
// prefix. The identity rules only inspect the business path, so a deployment
// prefix can never be mistaken for a user segment.
func urlPathPrefix(base string) string {
	if base == "" {
		return ""
	}
	basePath := base
	if parsed, err := url.Parse(base); err == nil {
		basePath = parsed.Path
	}
	return strings.TrimRight(basePath, "/")
}

// prepareOutboundURL applies the URL half of the identity rules to one outbound
// request. input, reqCtx and params stay owned by the caller; the returned URL is
// owned by this request.
func prepareOutboundURL(
	input *url.URL,
	reqCtx *RequestContext,
	auth upstreamAuthSnapshot,
	policy outboundIdentityPolicy,
) (*url.URL, error) {
	result, err := prepareOutboundURLWithReport(input, reqCtx, auth, policy)
	if err != nil {
		return nil, err
	}
	return result.url, nil
}

// prepareOutboundURLWithReport is prepareOutboundURL plus the change summary the
// log line needs.
func prepareOutboundURLWithReport(
	input *url.URL,
	reqCtx *RequestContext,
	auth upstreamAuthSnapshot,
	policy outboundIdentityPolicy,
) (urlPreparationResult, error) {
	result := urlPreparationResult{}
	if input == nil {
		return result, newClientInputPreparationError("unparsable-url", "url")
	}
	final := *input

	query := final.Query()

	// api_key handling comes first: a local token must never reach the upstream as
	// an authentication parameter.
	if policy.stripAPIKey {
		if changed, _ := stripAPIKeyVariants(query); changed {
			result.changed = append(result.changed, carrierQuery)
		}
		if auth.AccessToken != "" {
			query.Set("api_key", auth.AccessToken)
		}
	}

	// The identity rules read the business path only. The upstream base URL may
	// carry a deployment prefix, and a prefix that happens to start with /Users
	// must not be read as a user segment.
	prefix := urlPathPrefix(policy.baseURL)
	businessPath := strings.TrimPrefix(final.Path, prefix)
	if businessPath == "" {
		businessPath = "/"
	}
	segment, hasSegment := userIDPathSegment(businessPath)
	if hasSegment && !isStaticUserRoute(segment) {
		normalizeSegment := policy.pathAction == actionNormalizeToCurrent
		if !normalizeSegment && policy.pathClass == pathClassSelfAlias {
			// The path-side compatibility exception, for any method: an
			// unclassified path whose user segment is a trusted alias of this
			// request keeps working, so a client still holding the legacy global ID
			// is not locked out of an endpoint the proxy does not model. Another
			// local user's ID is not an alias and passes through untouched. The
			// write-body rule is separate and never widened by this.
			normalizeSegment = IsCurrentUserAlias(segment, reqCtx, auth, policyLookup(reqCtx))
		}
		if normalizeSegment {
			if auth.UserID == "" {
				return result, newMissingAuthStateError("path.UserId")
			}
			rewritten := replaceUserIDSegment(businessPath, segment, auth.UserID)
			if rewritten != businessPath {
				final.Path = joinBusinessPath(prefix, rewritten)
				final.RawPath = ""
				result.changed = append(result.changed, carrierPath)
			}
		}
	}

	queryChanged, err := applyQueryUserIDPolicy(query, reqCtx, auth, policy)
	if err != nil {
		return result, err
	}
	if queryChanged {
		result.changed = append(result.changed, carrierQuery)
	}

	final.RawQuery = query.Encode()
	result.url = &final
	return result, nil
}

// replaceUserIDSegment replaces exactly the /Users/{segment} user segment of a
// business path. It never does a ReplaceAll over the whole string, so the same
// text elsewhere in the path is untouched.
func replaceUserIDSegment(path string, segment string, replacement string) string {
	const prefix = "/Users/"
	if len(path) < len(prefix) || !strings.EqualFold(path[:len(prefix)], prefix) {
		return path
	}
	rest := path[len(prefix):]
	end := len(rest)
	if slash := strings.IndexByte(rest, '/'); slash >= 0 {
		end = slash
	}
	if rest[:end] != segment {
		return path
	}
	return prefix + replacement + rest[end:]
}

// applyQueryUserIDPolicy resolves the query UserId field for the declared policy
// and reports whether the query changed. It only ever touches the UserId field.
func applyQueryUserIDPolicy(
	query url.Values,
	reqCtx *RequestContext,
	auth upstreamAuthSnapshot,
	policy outboundIdentityPolicy,
) (bool, error) {
	switch policy.queryUserID {
	case actionNormalizeToCurrent:
		if len(userIDVariants(query)) == 0 {
			return false, nil
		}
		if auth.UserID == "" {
			return false, newMissingAuthStateError("query.UserId")
		}
		return applyUserIDQuery(query, auth.UserID), nil
	case actionOmitOnBootstrap:
		// Bootstrap requests carry no business identity; strip any that a client
		// sent so the upstream does not validate it as an existing user.
		keys := userIDVariants(query)
		if len(keys) == 0 {
			return false, nil
		}
		changed := false
		for _, key := range keys {
			if len(query[key]) > 0 {
				changed = true
			}
			query.Del(key)
		}
		return changed, nil
	default:
		// Unclassified: a GET/HEAD read whose every UserId is a trusted alias of
		// this request may be normalized. Anything else keeps its original value,
		// including other local users and foreign upstream IDs.
		if !policy.fallbackRead {
			return false, nil
		}
		outcome := classifyUserIDQuery(query, reqCtx, auth, policyLookup(reqCtx))
		if outcome.signal != signalCurrentUser {
			return false, nil
		}
		if auth.UserID == "" {
			return false, newMissingAuthStateError("query.UserId")
		}
		return applyUserIDQuery(query, auth.UserID), nil
	}
}

// prepareOutboundBody applies the body half of the identity rules. It only ever
// touches the declared top-level UserId field of a JSON body; nested user fields
// and every other value stay as the client sent them.
func prepareOutboundBody(
	body any,
	reqCtx *RequestContext,
	auth upstreamAuthSnapshot,
	policy outboundIdentityPolicy,
) (any, error) {
	prepared, _, err := prepareOutboundBodyWithReport(body, reqCtx, auth, policy)
	return prepared, err
}

// prepareOutboundBodyWithReport is prepareOutboundBody plus the support state and
// change flag the log summary needs.
func prepareOutboundBodyWithReport(
	body any,
	reqCtx *RequestContext,
	auth upstreamAuthSnapshot,
	policy outboundIdentityPolicy,
) (any, bodyUserIDOutcome, error) {
	outcome := bodyUserIDOutcome{}
	if body == nil {
		outcome.support = bodyEmpty
		return nil, outcome, nil
	}

	switch policy.bodyUserID {
	case actionNormalizeToCurrent:
		return normalizeBodyUserID(body, auth, &outcome)
	case actionOmitOnBootstrap:
		return omitBodyUserID(body, &outcome)
	default:
		// The fallback read exception is a query rule and never a body rule: an
		// unclassified JSON body keeps its values even when one equals a local ID,
		// because a numeric match cannot tell "current user" from "target user".
		outcome.support = bodyPassthroughSupport(body)
		return body, outcome, nil
	}
}

func bodyPassthroughSupport(body any) string {
	switch typed := body.(type) {
	case map[string]any:
		return bodyInspected
	case rawRequestBody:
		if hasJSONContentTypeHeader(typed.contentType) {
			return bodyInspected
		}
		return bodyNotJSON
	case *rawRequestBody:
		if typed != nil && hasJSONContentTypeHeader(typed.contentType) {
			return bodyInspected
		}
		return bodyNotJSON
	default:
		return bodyInspected
	}
}

// normalizeBodyUserID writes the current user into the declared top-level UserId
// field. A body without that field keeps it absent: no identity is injected.
func normalizeBodyUserID(body any, auth upstreamAuthSnapshot, outcome *bodyUserIDOutcome) (any, bodyUserIDOutcome, error) {
	asMap, err := bodyAsMap(body)
	if err != nil {
		return nil, *outcome, err
	}
	if asMap == nil {
		outcome.support = bodyUnsupported
		return body, *outcome, nil
	}
	key, hasUserID := findUserIDKey(asMap)
	if !hasUserID {
		outcome.support = bodyInspected
		return body, *outcome, nil
	}
	if auth.UserID == "" {
		return nil, *outcome, newMissingAuthStateError("body.UserId")
	}
	outcome.support = bodyInspected
	if asMap[key] == auth.UserID {
		return body, *outcome, nil
	}
	// Shallow copy: only the top-level field is modified, nested values are shared.
	copied := make(map[string]any, len(asMap))
	for k, v := range asMap {
		copied[k] = v
	}
	copied[key] = auth.UserID
	outcome.changed = true
	return copied, *outcome, nil
}

// omitBodyUserID removes the declared top-level UserId field for bootstrap
// requests that must not present a business identity.
func omitBodyUserID(body any, outcome *bodyUserIDOutcome) (any, bodyUserIDOutcome, error) {
	asMap, err := bodyAsMap(body)
	if err != nil {
		return nil, *outcome, err
	}
	if asMap == nil {
		outcome.support = bodyUnsupported
		return body, *outcome, nil
	}
	key, hasUserID := findUserIDKey(asMap)
	outcome.support = bodyInspected
	if !hasUserID {
		return body, *outcome, nil
	}
	copied := make(map[string]any, len(asMap))
	for k, v := range asMap {
		copied[k] = v
	}
	delete(copied, key)
	outcome.changed = true
	return copied, *outcome, nil
}

// bodyAsMap decodes a JSON body into a mutable top-level object. A raw body keeps
// its declared Content-Type; only a JSON-declared payload is decoded, and a
// decode failure is returned rather than silently ignored. A JSON root that is
// not an object (array, scalar, null) is reported as an unsupported shape and
// left untouched.
func bodyAsMap(body any) (map[string]any, error) {
	switch typed := body.(type) {
	case map[string]any:
		return typed, nil
	case nil:
		return nil, nil
	case rawRequestBody:
		return decodeJSONBodyMap(typed.data, typed.contentType)
	case *rawRequestBody:
		if typed == nil {
			return nil, nil
		}
		return decodeJSONBodyMap(typed.data, typed.contentType)
	default:
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		var decoded any
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			return nil, err
		}
		asMap, ok := decoded.(map[string]any)
		if !ok {
			return nil, nil
		}
		return asMap, nil
	}
}

func decodeJSONBodyMap(data []byte, contentType string) (map[string]any, error) {
	if len(data) == 0 || !hasJSONContentTypeHeader(contentType) {
		return nil, nil
	}
	var decoded any
	if err := json.Unmarshal(data, &decoded); err != nil {
		return nil, err
	}
	asMap, ok := decoded.(map[string]any)
	if !ok {
		return nil, nil
	}
	return asMap, nil
}

// canonicalAuthorizationHeader builds the single X-Emby-Authorization value an
// upstream should see. Every quoted parameter is escaped, so a client-supplied
// value containing a quote or comma cannot forge another parameter.
func canonicalAuthorizationHeader(userID, deviceName, deviceID, version, client string) string {
	parts := []string{fmt.Sprintf("UserId=%s", quoteHeaderParam(userID))}
	for _, field := range []struct {
		name  string
		value string
	}{
		{"Client", client},
		{"Device", deviceName},
		{"DeviceId", deviceID},
		{"Version", version},
	} {
		if field.value == "" {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s=%s", field.name, quoteHeaderParam(field.value)))
	}
	return "Emby " + strings.Join(parts, ", ")
}

// quoteHeaderParam renders a value as an RFC 7230 quoted-string with backslash
// escaping for quotes and backslashes.
func quoteHeaderParam(value string) string {
	var builder strings.Builder
	builder.WriteByte('"')
	for _, r := range value {
		switch r {
		case '"', '\\':
			builder.WriteByte('\\')
			builder.WriteRune(r)
		case '\r', '\n':
			// A header value cannot carry a line break; drop it rather than emit a
			// value that could split the header.
		default:
			builder.WriteRune(r)
		}
	}
	builder.WriteByte('"')
	return builder.String()
}

// prepareOutboundHeaders rebuilds the authentication part of the outbound
// headers from one auth snapshot. Client, device and language behaviour headers
// are preserved; the client's own credentials and its compound authorization
// header are removed so no captured local token or user ID can reach the
// upstream.
func prepareOutboundHeaders(headers http.Header, reqCtx *RequestContext, auth upstreamAuthSnapshot, mode outboundAuthMode) http.Header {
	if headers == nil {
		headers = http.Header{}
	}
	prepared := cloneHeader(headers)

	// Device identity is read before the credential-bearing headers are removed.
	client := prepared.Get("X-Emby-Client")
	deviceName := prepared.Get("X-Emby-Device-Name")
	deviceID := prepared.Get("X-Emby-Device-Id")
	version := prepared.Get("X-Emby-Client-Version")
	if client == "" || deviceName == "" || deviceID == "" || version == "" {
		for _, authKey := range []string{"X-Emby-Authorization", "Authorization"} {
			authValue := prepared.Get(authKey)
			if authValue == "" {
				continue
			}
			parsed, ok := parseAuthorizationIdentityStrict(authValue)
			if !ok {
				continue
			}
			if client == "" {
				client = parsed["Client"]
			}
			if deviceName == "" {
				deviceName = parsed["Device"]
			}
			if deviceID == "" {
				deviceID = parsed["DeviceId"]
			}
			if version == "" {
				version = parsed["Version"]
			}
		}
	}

	prepared.Del("Authorization")
	prepared.Del("X-Emby-Authorization")
	prepared.Del("X-MediaBrowser-Token")
	prepared.Del("X-Emby-Token")

	for _, field := range []struct {
		header string
		value  string
	}{
		{"X-Emby-Client", client},
		{"X-Emby-Device-Name", deviceName},
		{"X-Emby-Device-Id", deviceID},
		{"X-Emby-Client-Version", version},
	} {
		if field.value != "" {
			prepared.Set(field.header, field.value)
		}
	}

	// A per-request identity the caller built from this same auth snapshot (a
	// passthrough identity resolved for this client, or the configured API key) is
	// already consistent: it is re-sanitized, never rebuilt from the device
	// profile, because rebuilding would replace the client's real device identity
	// or the upstream API key.
	if mode == authModeNormal && explicitAuthHeader(prepared) {
		return sanitizeCompoundAuthorization(prepared)
	}

	if mode != authModePasswordLogin && auth.AccessToken != "" {
		prepared.Set("X-Emby-Token", auth.AccessToken)
	}
	prepared.Set("X-Emby-Authorization", canonicalAuthorizationHeader(auth.UserID, deviceName, deviceID, version, client))
	return prepared
}

// explicitAuthHeader reports whether the caller already installed a credential
// header that this preparation must not rebuild.
func explicitAuthHeader(headers http.Header) bool {
	return headers.Get("X-Emby-Token") != "" || headers.Get("X-MediaBrowser-Token") != "" ||
		headers.Get("X-Emby-Authorization") != "" || headers.Get("Authorization") != ""
}

// sanitizeCompoundAuthorization removes the identity-bearing parameters from a
// caller-supplied compound authorization header while keeping its device
// parameters. The credential header is then rebuilt from the parameters that
// remain, so a captured UserId or Token cannot travel inside it.
func sanitizeCompoundAuthorization(headers http.Header) http.Header {
	compound := headers.Get("X-Emby-Authorization")
	if compound == "" {
		compound = headers.Get("Authorization")
	}
	headers.Del("Authorization")
	headers.Del("X-Emby-Authorization")
	if compound == "" {
		return headers
	}
	parsed, ok := parseAuthorizationIdentityStrict(compound)
	if !ok {
		return headers
	}
	headers.Set("X-Emby-Authorization", canonicalAuthorizationHeader("",
		parsed["Device"], parsed["DeviceId"], parsed["Version"], parsed["Client"]))
	return headers
}

// clientFacingUserIDFor returns the response identity for a handler's request.
// A request with no proxy user resolves to the empty string, which makes the
// response rewriter fall back to the resource virtual-ID mapping instead of
// claiming an identity the request never had.
func (a *App) clientFacingUserIDFor(r *http.Request) string {
	if r == nil {
		return ""
	}
	return a.clientFacingUserID(requestContextFrom(r.Context()))
}

// joinBusinessPath re-attaches the upstream's own path prefix, keeping RawPath
// consistent so nothing is double-encoded.
func joinBusinessPath(prefix string, businessPath string) string {
	if prefix == "" {
		return businessPath
	}
	if businessPath == "/" {
		return prefix + "/"
	}
	return prefix + businessPath
}
