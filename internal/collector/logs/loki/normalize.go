// SPDX-License-Identifier: Apache-2.0

package loki

import (
	"encoding/json"
	"fmt"
	"maps"
	"sort"
	"strconv"
	"strings"
	"time"

	logwire "github.com/fulminate-io/knowledge-mcp/internal/logwire"
)

// normalizeEntry converts a Loki stream entry into a LogEntry. All stream
// labels are copied into Labels, the nanosecond-epoch timestamp is parsed,
// and the log line is inspected for JSON structure and embedded severity.
func normalizeEntry(stream map[string]string, tsStr, line string) logwire.LogEntry {
	entry := logwire.LogEntry{
		Timestamp: time.Now(),
		Severity:  logwire.SeverityInfo,
		Labels:    make(map[string]string, len(stream)),
	}

	// Copy all stream labels.
	maps.Copy(entry.Labels, stream)

	// Parse nanosecond timestamp.
	if ns, err := strconv.ParseInt(tsStr, 10, 64); err == nil {
		entry.Timestamp = time.Unix(0, ns)
	}

	// Check for severity in stream labels first.
	if sev := extractSeverityFromLabels(stream); sev != "" {
		entry.Severity = sev
	}

	// Parse the log line — may override severity from JSON fields.
	entry.Message, entry.Severity = parseLogLine(line, entry.Severity)

	return entry
}

// extractSeverityFromLabels checks common label keys for a severity value.
func extractSeverityFromLabels(stream map[string]string) string {
	for _, key := range []string{"level", "severity", "detected_level"} {
		if v, ok := stream[key]; ok && v != "" {
			return logwire.ParseSeverity(v)
		}
	}
	return ""
}

// severityFromJSON checks common JSON keys (level, severity, lvl) for a
// severity string. Returns the parsed severity or empty string if none found.
func severityFromJSON(m map[string]any) string {
	for _, key := range []string{"level", "severity", "lvl"} {
		v, ok := m[key]
		if !ok {
			continue
		}
		if s, ok := v.(string); ok && s != "" {
			return logwire.ParseSeverity(s)
		}
	}
	return ""
}

// parseLogLine extracts the message and severity from a raw log line. If the
// line is JSON, the message is extracted from standard fields and severity from
// level/severity/lvl. For plain text, DetectEmbeddedSeverity is used. The
// defaultSev parameter carries severity already extracted from stream labels.
func parseLogLine(line, defaultSev string) (message, severity string) {
	message = line
	severity = defaultSev

	if len(line) == 0 {
		return
	}

	// Try JSON parsing for structured logwire.
	trimmed := strings.TrimSpace(line)
	if msg, sev, ok := tryParseJSONLine(trimmed); ok {
		if msg != trimmed {
			message = msg
		}
		if sev != "" {
			severity = sev
		}
		return
	}

	// Fall back to embedded severity detection for plain text.
	if embedded := logwire.DetectEmbeddedSeverity(message); embedded != "" {
		severity = embedded
	}
	return
}

// tryParseJSONLine attempts to parse the line as JSON and extract the message
// and severity. Returns ok=false if the line is not valid JSON.
func tryParseJSONLine(trimmed string) (message, severity string, ok bool) {
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return "", "", false
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(trimmed), &m); err != nil {
		return "", "", false
	}
	return logwire.ExtractMessageFromMap(m), severityFromJSON(m), true
}

// buildLogQL constructs a LogQL stream selector from a logwire.Query. Field
// filters are translated via logwire.MapFieldFilters, the source maps to
// the namespace label, and text filters become line filters.
func buildLogQL(q logwire.Query) string {
	mapped := logwire.MapFieldFilters("loki", q.FieldFilters)

	var matchers []string

	// Source maps to namespace (matching K8s convention).
	if q.Source != "" {
		src := logwire.SanitizeSourceName(q.Source)
		matchers = append(matchers, fmt.Sprintf(`namespace=%q`, src))
	}

	for label, value := range mapped {
		// Skip level — severity filtering is done client-side.
		if label == "level" {
			continue
		}
		matchers = append(matchers, fmt.Sprintf(`%s=%q`, label, value))
	}

	// Loki requires at least one matcher in the stream selector.
	var selector string
	if len(matchers) > 0 {
		sort.Strings(matchers)
		selector = "{" + strings.Join(matchers, ", ") + "}"
	} else {
		selector = `{namespace=~".+"}`
	}

	var query strings.Builder
	query.WriteString(selector)

	if q.TextFilter != "" {
		fmt.Fprintf(&query, ` |= %q`, q.TextFilter)
	}
	if q.RawQuery != "" {
		query.WriteString(" ")
		query.WriteString(q.RawQuery)
	}

	return query.String()
}
