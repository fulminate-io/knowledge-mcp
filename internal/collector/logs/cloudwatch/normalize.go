// SPDX-License-Identifier: Apache-2.0

package cloudwatch

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	cwTypes "github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"

	logwire "github.com/fulminate-io/knowledge-mcp/internal/logwire"
)

// normalizeEntry converts a CloudWatch FilteredLogEvent into a LogEntry.
// Labels are populated with log_group, log_stream, and service.
func normalizeEntry(event cwTypes.FilteredLogEvent, logGroupName string) logwire.LogEntry {
	entry := logwire.LogEntry{
		Timestamp: time.Now(),
		Severity:  logwire.SeverityInfo,
		Labels: map[string]string{
			"log_group": logGroupName,
			"service":   extractService(logGroupName),
		},
	}

	if event.Timestamp != nil {
		entry.Timestamp = time.UnixMilli(*event.Timestamp)
	}
	if event.LogStreamName != nil {
		stream := *event.LogStreamName
		if len(stream) > 60 {
			stream = stream[:57] + "..."
		}
		entry.Labels["log_stream"] = stream
	}

	msg := aws.ToString(event.Message)
	entry.Message, entry.Severity = parseLogMessage(msg)
	return entry
}

// parseLogMessage extracts message content and severity from a raw CloudWatch
// log line. Handles JSON, logger-prefixed JSON, severity-prefixed text, and
// container log wrappers with nested JSON.
func parseLogMessage(line string) (message, severity string) {
	message = strings.TrimSpace(line)
	severity = logwire.SeverityInfo
	if message == "" {
		return
	}

	// Strip logger prefix: [some.logger.name] {json...}
	jsonBody := message
	if message[0] == '[' {
		if _, after, found := strings.Cut(message, "] "); found {
			rest := strings.TrimSpace(after)
			if len(rest) > 0 && rest[0] == '{' {
				jsonBody = rest
			}
		}
	}

	// Try JSON parsing for structured logwire.
	if len(jsonBody) > 0 && jsonBody[0] == '{' {
		if msg, sev, ok := parseJSON(jsonBody, 0); ok {
			return msg, sev
		}
	}

	// Severity prefix: "ERROR ..." or "[ERROR] ..."
	if msg, sev, ok := parseSeverityPrefix(message); ok {
		return msg, sev
	}

	// Embedded severity detection for plain text.
	if embedded := logwire.DetectEmbeddedSeverity(message); embedded != "" {
		severity = embedded
	}
	return
}

// parseSeverityPrefix extracts a leading severity token from the message.
func parseSeverityPrefix(msg string) (string, string, bool) {
	idx := strings.IndexByte(msg, ' ')
	if idx <= 0 || idx > 8 {
		return "", "", false
	}
	prefix := strings.Trim(msg[:idx], "[]")
	sev := logwire.ParseSeverity(prefix)
	if sev == logwire.SeverityInfo && !strings.EqualFold(prefix, "INFO") {
		return "", "", false
	}
	rest := strings.TrimSpace(msg[idx+1:])
	if rest == "" {
		return "", "", false
	}
	// Strip logger prefix after severity: INFO [logger.name] {json...}
	if rest[0] == '[' {
		if end := strings.Index(rest, "] "); end >= 0 {
			after := strings.TrimSpace(rest[end+2:])
			if len(after) > 0 && after[0] == '{' {
				rest = after
			}
		}
	}
	// If the rest is JSON, parse it.
	if rest[0] == '{' {
		if inner, innerSev, ok := parseJSON(rest, 0); ok {
			if innerSev != logwire.SeverityInfo {
				sev = innerSev
			}
			return inner, sev, true
		}
	}
	return rest, sev, true
}

// parseJSON recursively parses a JSON log line (up to 3 levels of nesting).
// Returns (message, severity, ok).
func parseJSON(line string, depth int) (string, string, bool) {
	if depth > 3 {
		return line, logwire.SeverityInfo, false
	}

	var m map[string]any
	if err := json.Unmarshal([]byte(line), &m); err != nil {
		return "", "", false
	}

	msg := logwire.ExtractMessageFromMap(m)
	sev := extractSeverityFromMap(m)

	// Unwrap container log wrappers: the extracted message may itself be JSON.
	msg, sev = unwrapNested(msg, sev, depth+1)
	return msg, sev, true
}

// unwrapNested handles container log wrappers where the extracted message
// is itself JSON or has a "SEVERITY [logger] {json}" prefix.
func unwrapNested(msg, sev string, depth int) (string, string) {
	if len(msg) == 0 {
		return msg, sev
	}

	// Direct JSON: recurse into it.
	if msg[0] == '{' {
		if inner, innerSev, ok := parseJSON(msg, depth); ok {
			if innerSev != logwire.SeverityInfo {
				sev = innerSev
			}
			return inner, sev
		}
		return msg, sev
	}

	// "SEVERITY [logger] {json}" pattern inside the extracted message.
	rest := msg
	if idx := strings.IndexByte(rest, ' '); idx > 0 && idx <= 8 {
		prefix := strings.Trim(rest[:idx], "[]")
		if prefixSev := logwire.ParseSeverity(prefix); prefixSev != logwire.SeverityInfo || strings.EqualFold(prefix, "INFO") {
			if prefixSev != logwire.SeverityInfo {
				sev = prefixSev
			}
			rest = strings.TrimSpace(rest[idx+1:])
		}
	}
	// Strip logger prefix: [logger.name] {json}
	if len(rest) > 0 && rest[0] == '[' {
		if end := strings.Index(rest, "] "); end >= 0 {
			after := strings.TrimSpace(rest[end+2:])
			if len(after) > 0 && after[0] == '{' {
				rest = after
			}
		}
	}
	if len(rest) > 0 && rest[0] == '{' {
		if inner, innerSev, ok := parseJSON(rest, depth); ok {
			if innerSev != logwire.SeverityInfo {
				sev = innerSev
			}
			return inner, sev
		}
	}
	return msg, sev
}

// extractSeverityFromMap finds severity in a JSON map.
func extractSeverityFromMap(m map[string]any) string {
	for _, key := range []string{"level", "severity", "lvl", "log_level"} {
		if v, ok := m[key]; ok {
			if s, ok := v.(string); ok && s != "" {
				return logwire.ParseSeverity(s)
			}
		}
	}
	// Check nested message/log objects for severity.
	for _, key := range []string{"message", "msg", "log"} {
		if v, ok := m[key]; ok {
			if nested, ok := v.(map[string]any); ok {
				if sev := extractSeverityFromMap(nested); sev != logwire.SeverityInfo {
					return sev
				}
			}
		}
	}
	return logwire.SeverityInfo
}

// extractService extracts a service name from a CloudWatch log group path.
// Returns the last path segment: /ecs/prod/api-server -> api-server.
func extractService(logGroup string) string {
	parts := strings.Split(strings.TrimPrefix(logGroup, "/"), "/")
	if len(parts) == 0 {
		return logGroup
	}
	return parts[len(parts)-1]
}
