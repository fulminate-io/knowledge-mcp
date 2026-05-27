// SPDX-License-Identifier: Apache-2.0

package logs

import (
	"regexp"
	"strings"
)

// Wildcard is the placeholder token for variable parts in log templates.
const Wildcard = "<*>"

// Regex patterns for pre-processing high-cardinality tokens.
var (
	reTimestamp = regexp.MustCompile(
		`\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:?\d{2})?` +
			`|\d{2}:\d{2}:\d{2}(?:\.\d+)?` +
			`|\d{10,13}`)

	reUUID    = regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)
	reIPv4    = regexp.MustCompile(`\b\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}\b`)
	reHexID   = regexp.MustCompile(`\b[0-9a-fA-F]{16,}\b`)
	reNumeric = regexp.MustCompile(`\b\d{4,}\b`)
	reURL     = regexp.MustCompile(`https?://[^\s'")\]]{20,}`)
)

// PreProcess replaces high-cardinality tokens (UUIDs, timestamps, IPs,
// hex IDs, long numbers, URLs) with wildcards before Drain clustering.
func PreProcess(msg string) string {
	msg = reUUID.ReplaceAllString(msg, Wildcard)
	msg = reTimestamp.ReplaceAllString(msg, Wildcard)
	msg = reIPv4.ReplaceAllString(msg, Wildcard)
	msg = reHexID.ReplaceAllString(msg, Wildcard)
	msg = reNumeric.ReplaceAllString(msg, Wildcard)
	msg = reURL.ReplaceAllString(msg, "<url>")
	return msg
}

// Tokenize splits a preprocessed message into tokens.
func Tokenize(msg string) []string {
	return strings.Fields(msg)
}

// tokenCountBucket maps token count to a size category for the Drain
// parse tree's first-level branching.
func tokenCountBucket(n int) string {
	switch {
	case n <= 3:
		return "short"
	case n <= 8:
		return "medium"
	case n <= 15:
		return "long"
	default:
		return "vlong"
	}
}

// isWildcard returns true if the token should be treated as a wildcard
// in the Drain parse tree (either it is the wildcard literal or contains
// digits, which are typically variable).
func isWildcard(token string) bool {
	if token == Wildcard {
		return true
	}
	return containsDigit(token)
}

// containsDigit reports whether s contains any ASCII digit.
func containsDigit(s string) bool {
	for _, r := range s {
		if r >= '0' && r <= '9' {
			return true
		}
	}
	return false
}
