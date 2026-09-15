package backend

import (
	"net/url"
	"sort"
	"strings"
)

// Sensitive query parameter names whose values must never reach a log line or an
// error message. The comparison is case-insensitive.
var outboundSensitiveQueryKeys = map[string]bool{
	"api_key":       true,
	"apikey":        true,
	"token":         true,
	"access_token":  true,
	"accesstoken":   true,
	"authorization": true,
	"password":      true,
	"pw":            true,
	"pwd":           true,
}

// outboundVisibleQueryKeys are the query parameters worth keeping in a log line:
// they name a route or a resource, never a credential.
var outboundVisibleQueryKeys = map[string]bool{
	"userid":           true,
	"itemid":           true,
	"parentid":         true,
	"seriesid":         true,
	"seasonid":         true,
	"mediasourceid":    true,
	"playsessionid":    true,
	"ids":              true,
	"limit":            true,
	"startindex":       true,
	"sortby":           true,
	"sortorder":        true,
	"fields":           true,
	"includeitemtypes": true,
	"recursive":        true,
	"static":           true,
	"filters":          true,
	"searchterm":       true,
}

// redactSecretValue keeps a value's shape in a log line without revealing it.
func redactSecretValue(value string) string {
	if value == "" {
		return ""
	}
	if len(value) <= 4 {
		return "***"
	}
	return "***(" + intToString(len(value)) + ")"
}

// formatOutboundURLForLog renders a URL for the log: scheme, host, path, the
// query parameters that name a route or resource, and a count of everything
// else. Credentials, tokens, the userinfo section and the fragment never appear.
// The URL passed in is never modified.
func formatOutboundURLForLog(target string) string {
	if target == "" {
		return ""
	}
	parsed, err := url.Parse(target)
	if err != nil {
		return "<unparsable-url>"
	}
	var builder strings.Builder
	builder.WriteString(parsed.Scheme)
	builder.WriteString("://")
	builder.WriteString(parsed.Host)
	builder.WriteString(parsed.Path)

	values := parsed.Query()
	visible := make([]string, 0, len(values))
	hidden := 0
	for key, rawValues := range values {
		lower := strings.ToLower(key)
		if outboundSensitiveQueryKeys[lower] {
			hidden++
			continue
		}
		if !outboundVisibleQueryKeys[lower] {
			hidden++
			continue
		}
		for _, raw := range rawValues {
			visible = append(visible, key+"="+raw)
		}
	}
	sort.Strings(visible)
	if len(visible) > 0 {
		builder.WriteString("?")
		builder.WriteString(strings.Join(visible, "&"))
	}
	if hidden > 0 {
		builder.WriteString(" [redacted ")
		builder.WriteString(intToString(hidden))
		builder.WriteString(" param(s)]")
	}
	return builder.String()
}

// redactURLInError rewrites any URL appearing in an error message so a token
// carried in the query string of a network error cannot reach the log.
func redactURLInError(err error) string {
	if err == nil {
		return ""
	}
	message := err.Error()
	var builder strings.Builder
	rest := message
	for {
		scheme := strings.Index(rest, "://")
		if scheme < 0 {
			builder.WriteString(rest)
			return builder.String()
		}
		start := scheme
		for start > 0 && !isURLBoundary(rest[start-1]) {
			start--
		}
		end := len(rest)
		for i := scheme + 3; i < len(rest); i++ {
			if isURLBoundary(rest[i]) {
				end = i
				break
			}
		}
		builder.WriteString(rest[:start])
		builder.WriteString(formatOutboundURLForLog(rest[start:end]))
		rest = rest[end:]
	}
}

func isURLBoundary(b byte) bool {
	switch b {
	case ' ', '\t', '\n', '\r', '"', '\'', '(', ')', '<', '>', ',':
		return true
	}
	return false
}

// outboundChangeSummary reports which carriers the preparation layer rewrote,
// without ever including the values.
func outboundChangeSummary(changed []string) string {
	if len(changed) == 0 {
		return "none"
	}
	sort.Strings(changed)
	return strings.Join(changed, ",")
}

// formatValuesForLog renders url.Values the way formatOutboundURLForLog renders a
// query: route and resource names visible, credentials hidden.
func formatValuesForLog(values url.Values) string {
	if len(values) == 0 {
		return ""
	}
	visible := make([]string, 0, len(values))
	hidden := 0
	for key, rawValues := range values {
		lower := strings.ToLower(key)
		if outboundSensitiveQueryKeys[lower] || !outboundVisibleQueryKeys[lower] {
			hidden++
			continue
		}
		for _, raw := range rawValues {
			visible = append(visible, key+"="+raw)
		}
	}
	sort.Strings(visible)
	joined := strings.Join(visible, "&")
	if hidden > 0 {
		if joined != "" {
			joined += " "
		}
		joined += "[redacted " + intToString(hidden) + " param(s)]"
	}
	return joined
}
