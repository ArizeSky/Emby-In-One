package backend

import (
	"net/url"
	"sort"
	"strings"
)

// outboundDiagnosticsBudget bounds how much input one scan inspects. It is an
// explicit input to the scan, so a caller can never be told "clean" by a scan
// that stopped early without saying so.
type outboundDiagnosticsBudget struct {
	// MaxBytes caps the total size of the inspected URL, header values and body.
	MaxBytes int
	// MaxFindings caps how many findings are recorded before the scan reports
	// truncation.
	MaxFindings int
	// MaxCandidates caps how many candidate strings are looked up per scan.
	MaxCandidates int
}

func defaultOutboundDiagnosticsBudget() outboundDiagnosticsBudget {
	return outboundDiagnosticsBudget{MaxBytes: 1 << 20, MaxFindings: 256, MaxCandidates: 4096}
}

// outboundDiagnosticFinding is one classified identifier found in an outbound
// request. It carries a location and a class, never the value: a diagnostic
// result must be safe to place in a log.
type outboundDiagnosticFinding struct {
	Carrier   string
	Location  string
	Class     IdentifierClass
	Truncated bool
}

// outboundDiagnosticsResult is the outcome of one scan.
type outboundDiagnosticsResult struct {
	Findings []outboundDiagnosticFinding
	// Coverage states what the scan could inspect: "full" when every supported
	// carrier was read, "partial" when an input or candidate budget stopped it.
	Coverage string
	// Truncated reports that the scan stopped early. A truncated scan never
	// reports full coverage.
	Truncated bool
	// Unsupported lists carriers whose encoding this scan cannot read.
	Unsupported []string
}

const (
	diagnosticsCoverageFull    = "full"
	diagnosticsCoveragePartial = "partial"
)

// outboundDiagnosticsInput is the read-only material one scan inspects. The scan
// never modifies it, never reads the live request reader and never sends a
// request.
type outboundDiagnosticsInput struct {
	URL     string
	Headers map[string][]string
	Body    []byte
}

// scanOutboundIdentity looks for registered local identifiers and upstream
// identities in an already-prepared outbound request. It reports what it found
// and how complete the scan was; it never fails a test by itself.
func scanOutboundIdentity(input outboundDiagnosticsInput, lookup *IdentifierLookup, budget outboundDiagnosticsBudget) outboundDiagnosticsResult {
	result := outboundDiagnosticsResult{Coverage: diagnosticsCoverageFull}
	if lookup == nil {
		lookup = newIdentifierLookup(IdentifierSources{})
	}
	if budget.MaxBytes <= 0 {
		budget.MaxBytes = defaultOutboundDiagnosticsBudget().MaxBytes
	}
	if budget.MaxFindings <= 0 {
		budget.MaxFindings = defaultOutboundDiagnosticsBudget().MaxFindings
	}
	if budget.MaxCandidates <= 0 {
		budget.MaxCandidates = defaultOutboundDiagnosticsBudget().MaxCandidates
	}

	spent := 0
	candidates := 0

	record := func(carrier, location string, value string) bool {
		if value == "" {
			return true
		}
		candidates++
		if candidates > budget.MaxCandidates {
			result.Truncated = true
			result.Coverage = diagnosticsCoveragePartial
			return false
		}
		class := classifyForDiagnostics(value, lookup)
		if class == IdentifierUnknown {
			return true
		}
		if len(result.Findings) >= budget.MaxFindings {
			result.Truncated = true
			result.Coverage = diagnosticsCoveragePartial
			return false
		}
		result.Findings = append(result.Findings, outboundDiagnosticFinding{Carrier: carrier, Location: location, Class: class})
		return true
	}

	if input.URL != "" {
		spent += len(input.URL)
		if spent > budget.MaxBytes {
			result.Truncated = true
			result.Coverage = diagnosticsCoveragePartial
		} else {
			parsed, err := url.Parse(input.URL)
			if err != nil {
				result.Unsupported = append(result.Unsupported, "url")
			} else {
				if segment, ok := userIDPathSegment(parsed.Path); ok && !isStaticUserRoute(segment) {
					if !record(carrierPath, "path./Users", segment) {
						return result
					}
				}
				keys := make([]string, 0, len(parsed.Query()))
				for key := range parsed.Query() {
					keys = append(keys, key)
				}
				sort.Strings(keys)
				for _, key := range keys {
					if !isDiagnosticIdentityQueryKey(key) {
						continue
					}
					for _, value := range parsed.Query()[key] {
						if !record(carrierQuery, "query."+key, value) {
							return result
						}
					}
				}
			}
		}
	}

	headerKeys := make([]string, 0, len(input.Headers))
	for key := range input.Headers {
		headerKeys = append(headerKeys, key)
	}
	sort.Strings(headerKeys)
	for _, key := range headerKeys {
		for _, value := range input.Headers[key] {
			spent += len(key) + len(value)
			if spent > budget.MaxBytes {
				result.Truncated = true
				result.Coverage = diagnosticsCoveragePartial
				return result
			}
			if isIdentityBearingHeader(key) {
				parsed, ok := parseAuthorizationIdentityStrict(value)
				if !ok {
					result.Unsupported = append(result.Unsupported, "header."+key)
					continue
				}
				for _, param := range []string{"UserId", "Token"} {
					if !record(carrierAuth, "header."+key+"."+param, parsed[param]) {
						return result
					}
				}
				continue
			}
			if strings.EqualFold(key, "X-Emby-Token") || strings.EqualFold(key, "X-MediaBrowser-Token") {
				if !record(carrierAuth, "header."+key, value) {
					return result
				}
			}
		}
	}

	if len(input.Body) > 0 {
		spent += len(input.Body)
		if spent > budget.MaxBytes {
			result.Truncated = true
			result.Coverage = diagnosticsCoveragePartial
			return result
		}
		values, supported := jsonTopLevelStrings(input.Body)
		if !supported {
			result.Unsupported = append(result.Unsupported, "body")
			result.Coverage = diagnosticsCoveragePartial
			return result
		}
		keys := make([]string, 0, len(values))
		for key := range values {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if !strings.EqualFold(key, "UserId") {
				continue
			}
			if !record(carrierBody, "body."+key, values[key]) {
				return result
			}
		}
	}
	return result
}

// diagnosticIdentityQueryKeys are the query parameters a scan reads: the declared
// current-user field plus the resource identifiers this proxy maps. A value is
// only reported when it actually matches a registered identifier, so reading a
// wider key set cannot produce a finding on its own.
var diagnosticIdentityQueryKeys = map[string]bool{
	"userid":        true,
	"itemid":        true,
	"id":            true,
	"parentid":      true,
	"seriesid":      true,
	"seasonid":      true,
	"mediasourceid": true,
	"playsessionid": true,
	"sessionid":     true,
}

func isDiagnosticIdentityQueryKey(key string) bool {
	return diagnosticIdentityQueryKeys[strings.ToLower(key)]
}

// isIdentityBearingHeader reports whether a header carries a credential or an
// identity in a compound form.
func isIdentityBearingHeader(key string) bool {
	return strings.EqualFold(key, "X-Emby-Authorization") || strings.EqualFold(key, "Authorization")
}

// jsonTopLevelStrings decodes the top-level string fields of a JSON object.
// Arrays, scalars and malformed documents are reported as unsupported rather
// than silently reported clean.
func jsonTopLevelStrings(body []byte) (map[string]string, bool) {
	asMap, err := decodeJSONBodyMap(body, "application/json")
	if err != nil || asMap == nil {
		return nil, false
	}
	out := make(map[string]string, len(asMap))
	for key, value := range asMap {
		if text, ok := value.(string); ok {
			out[key] = text
		}
	}
	return out, true
}

// classifyForDiagnostics wraps Match with the diagnostic classes. A data source
// that cannot be consulted is reported as unknown with partial coverage by the
// caller; it is never reported as "confirmed not local".
func classifyForDiagnostics(value string, lookup *IdentifierLookup) IdentifierClass {
	facts := lookup.Match(value)
	switch {
	case facts.LocalUser:
		return IdentifierLocalUser
	case facts.LocalToken:
		return IdentifierLocalToken
	case facts.VirtualResource:
		return IdentifierVirtualResource
	case len(facts.UpstreamServers) > 0:
		return IdentifierForeignUpstream
	default:
		return IdentifierUnknown
	}
}

// findingClasses lists the distinct classes in a scan result, sorted. It exists
// so a test can assert on classes without depending on finding order.
func findingClasses(result outboundDiagnosticsResult) []IdentifierClass {
	seen := map[IdentifierClass]bool{}
	for _, finding := range result.Findings {
		seen[finding.Class] = true
	}
	out := make([]IdentifierClass, 0, len(seen))
	for class := range seen {
		out = append(out, class)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
