// SPDX-License-Identifier: Apache-2.0

package logwire

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// Severity levels ordered from least to most severe.
const (
	SeverityTrace    = "TRACE"
	SeverityDebug    = "DEBUG"
	SeverityInfo     = "INFO"
	SeverityWarn     = "WARN"
	SeverityError    = "ERROR"
	SeverityCritical = "CRITICAL"
)

// severityOrder maps severity strings to numeric order for comparison.
var severityOrder = map[string]int{
	SeverityTrace:    0,
	SeverityDebug:    1,
	SeverityInfo:     2,
	SeverityWarn:     3,
	SeverityError:    4,
	SeverityCritical: 5,
}

// ParseSeverity normalizes a severity string to canonical form.
// Returns SeverityInfo for unrecognized values.
func ParseSeverity(s string) string {
	upper := strings.ToUpper(strings.TrimSpace(s))
	switch upper {
	case SeverityTrace:
		return SeverityTrace
	case SeverityDebug, "DBG":
		return SeverityDebug
	case SeverityInfo, "INFORMATION", "NOTICE":
		return SeverityInfo
	case SeverityWarn, "WARNING", "WARNI":
		return SeverityWarn
	case SeverityError, "ERR", "SEVERE", "FATAL":
		return SeverityError
	case SeverityCritical, "CRIT", "ALERT", "EMERGENCY", "EMERG", "PANIC":
		return SeverityCritical
	default:
		return SeverityInfo
	}
}

// SeverityAtLeast returns true if severity is at or above minSeverity.
func SeverityAtLeast(severity, minSeverity string) bool {
	return severityOrder[severity] >= severityOrder[minSeverity]
}

// SeverityIndex returns the numeric index of a severity level.
// Useful for cross-package severity comparisons.
func SeverityIndex(severity string) int {
	return severityOrder[severity]
}

// reEmbeddedLevel matches common embedded severity indicators in log messages.
// GKE marks all stderr output as ERROR severity regardless of actual log level,
// so we detect the real level from message content and reclassify downward.
var reEmbeddedLevel = regexp.MustCompile(
	`(?i)` +
		`(?:` +
		`"?level"?\s*[:=]\s*"?(\w+)"?` + // level=info, "level":"info", level: warn
		`|` +
		`\t(trace|debug|info|warn(?:ing)?|error|fatal|panic)\t` + // \tinfo\t (zerolog tab-delimited)
		`|` +
		`\[(TRACE|DEBUG|INFO|WARN(?:ING)?|ERROR|FATAL|PANIC)\]` + // [INFO] (bracket style)
		`|` +
		`^(TRACE|DEBUG|INFO|WARNI?(?:NG)?|ERROR|FATAL|PANIC|CRITICAL)\s` + // ERROR [asyncio] (severity at start)
		`)`,
)

// DetectEmbeddedSeverity checks the beginning of a log message for an embedded
// severity indicator. Returns the parsed canonical severity or "" if none found.
func DetectEmbeddedSeverity(msg string) string {
	// Only check first 200 chars for performance.
	check := msg
	if len(check) > 200 {
		check = check[:200]
	}
	m := reEmbeddedLevel.FindStringSubmatch(check)
	if m == nil {
		return ""
	}
	// One of the capture groups will be non-empty.
	for _, g := range m[1:] {
		if g != "" {
			return ParseSeverity(g)
		}
	}
	return ""
}

// ExtractMessageFromMap extracts a message string from a JSON payload map.
// Tries common field names in priority order.
func ExtractMessageFromMap(m map[string]any) string {
	for _, key := range []string{"message", "msg", "event", "textPayload", "log", "body"} {
		if s := extractField(m, key); s != "" {
			return s
		}
	}

	// Fall back to JSON representation.
	b, err := json.Marshal(m)
	if err != nil {
		return fmt.Sprintf("%v", m)
	}
	return string(b)
}

// extractField tries to extract a string from m[key]. If the value is a nested
// map, it recurses via ExtractMessageFromMap.
func extractField(m map[string]any, key string) string {
	v, ok := m[key]
	if !ok {
		return ""
	}
	if s, ok := v.(string); ok && s != "" {
		return s
	}
	if nested, ok := v.(map[string]any); ok {
		return ExtractMessageFromMap(nested)
	}
	return ""
}
