// SPDX-License-Identifier: Apache-2.0

package stackdriver

import (
	"fmt"
	"strings"
	"time"

	"cloud.google.com/go/logging"

	logwire "github.com/fulminate-io/knowledge-mcp/internal/logwire"
)

// buildStackdriverFilter constructs an Advanced Logs Filter string from a
// logwire.Query. Reference:
// https://cloud.google.com/logging/docs/view/logging-query-language
//
// The filter joins each clause with a newline, which GCP treats as a logical
// AND. RawQuery is appended verbatim so advanced callers can layer custom
// predicates on top of the structured fields.
func buildStackdriverFilter(projectID string, q logwire.Query) string {
	var parts []string

	if !q.StartTime.IsZero() {
		parts = append(parts, fmt.Sprintf(`timestamp >= "%s"`, q.StartTime.UTC().Format(time.RFC3339)))
	}
	if !q.EndTime.IsZero() {
		parts = append(parts, fmt.Sprintf(`timestamp <= "%s"`, q.EndTime.UTC().Format(time.RFC3339)))
	}

	if q.SeverityMin != "" {
		if gcpSev := mapSeverityToGCP(q.SeverityMin); gcpSev != "" {
			parts = append(parts, fmt.Sprintf("severity >= %s", gcpSev))
		}
	}

	if q.Source != "" {
		parts = append(parts, buildLogNameClause(projectID, q.Source))
	}

	if q.TextFilter != "" {
		parts = append(parts, escapeGCPTextFilter(q.TextFilter))
	}

	if len(q.FieldFilters) > 0 {
		mapped := logwire.MapFieldFilters("gcp", q.FieldFilters)
		// Sort for deterministic filter output (test friendliness).
		for _, field := range sortedKeys(mapped) {
			parts = append(parts, fmt.Sprintf(`%s="%s"`, field, escapeGCPValue(mapped[field])))
		}
	}

	if q.RawQuery != "" {
		parts = append(parts, q.RawQuery)
	}

	return strings.Join(parts, "\n")
}

// buildLogNameClause accepts both short ("stderr") and fully-qualified
// ("projects/other/logs/stdout") log names. Short names are expanded using
// the configured project so operators don't have to type the prefix.
//
// Source is LLM-controllable (MCP tool args), so we route it through
// SanitizeSourceName (parity with Loki) and escapeGCPValue (defense in
// depth) to neutralize Advanced-Logs-Filter injection via embedded quotes.
func buildLogNameClause(projectID, source string) string {
	safe := escapeGCPValue(logwire.SanitizeSourceName(source))
	if strings.HasPrefix(safe, "projects/") {
		return fmt.Sprintf(`logName="%s"`, safe)
	}
	return fmt.Sprintf(`logName="projects/%s/logs/%s"`, projectID, safe)
}

// escapeGCPTextFilter wraps an unquoted text filter in double quotes,
// escaping any embedded quotes or backslashes. Already-quoted filters pass
// through untouched so operators can hand-craft exact-match strings.
func escapeGCPTextFilter(text string) string {
	if strings.HasPrefix(text, `"`) && strings.HasSuffix(text, `"`) {
		return text
	}
	return `"` + escapeGCPValue(text) + `"`
}

// escapeGCPValue escapes a value for use inside a GCP filter expression.
// GCP accepts standard C-style escapes inside double-quoted strings.
func escapeGCPValue(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return s
}

// sortedKeys returns the keys of m in ascending order. Kept inline to
// avoid a dependency on "sort" for a single filter builder.
func sortedKeys(m map[string]string) []string {
	if len(m) == 0 {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j-1] > keys[j]; j-- {
			keys[j-1], keys[j] = keys[j], keys[j-1]
		}
	}
	return keys
}

// mapSeverityToGCP maps the canonical severity strings onto the GCP
// Stackdriver severity names used inside filter expressions.
func mapSeverityToGCP(severity string) string {
	switch severity {
	case logwire.SeverityTrace, logwire.SeverityDebug:
		return "DEBUG"
	case logwire.SeverityInfo:
		return "INFO"
	case logwire.SeverityWarn:
		return "WARNING"
	case logwire.SeverityError:
		return "ERROR"
	case logwire.SeverityCritical:
		return "CRITICAL"
	default:
		return ""
	}
}

// mapGCPSeverity is the inverse of mapSeverityToGCP: it maps the SDK's
// numeric logging.Severity values back to canonical strings. The switch
// uses >= comparisons because GCP allows intermediate numeric values (e.g.
// between Warning and Error) — we round up to the next canonical bucket
// the same way logsift's backend does.
func mapGCPSeverity(s logging.Severity) string {
	switch {
	case s >= logging.Emergency, s >= logging.Alert, s >= logging.Critical:
		return logwire.SeverityCritical
	case s >= logging.Error:
		return logwire.SeverityError
	case s >= logging.Warning:
		return logwire.SeverityWarn
	case s >= logging.Notice, s >= logging.Info:
		return logwire.SeverityInfo
	case s >= logging.Debug:
		return logwire.SeverityDebug
	default:
		return logwire.SeverityInfo
	}
}
