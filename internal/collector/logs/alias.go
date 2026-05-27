// SPDX-License-Identifier: Apache-2.0

// Package logs alias derivation: human-readable identifiers for streams
// and templates so agents and operators can reference log objects by
// recognizable names instead of raw SHA-256 hashes.
//
// Aliases are computed from observable structure (label set for streams,
// pattern + severity for templates) and are stable across runs. They are
// NOT guaranteed unique on their own — collisions are resolved by the
// QueryEngine layer (Phase 2) which appends a short hash suffix when two
// streams or two templates would otherwise share an alias. This file
// contains shared helpers used by both alias_stream.go and
// alias_template.go.
//
// Case is preserved verbatim in display: a Kubernetes reason of
// `OOMKilled` renders as `OOMKilled`, not `oomkilled`. Case
// normalisation happens only inside lookup paths (ResolveStreamID /
// ResolveTemplateID) so users can type either form.
package logs

import (
	"strings"
	"unicode"
)

// firstNonEmpty returns the first non-empty value at the supplied keys.
func firstNonEmpty(labels map[string]string, keys ...string) string {
	for _, k := range keys {
		if v := labels[k]; v != "" {
			return v
		}
	}
	return ""
}

// sanitize normalises a label value for inclusion in an alias. Spaces
// and runs of unsafe characters collapse to `-`; case is preserved so
// `OOMKilled` survives intact. Empty input yields empty output.
func sanitize(s string) string {
	if s == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(s))
	prevDash := false
	for _, r := range s {
		switch {
		case isAliasSafe(r):
			b.WriteRune(r)
			prevDash = false
		default:
			if !prevDash {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

// isAliasSafe returns true for characters allowed verbatim in an alias
// component. Letters (any case), digits, `-`, `_` and `:` survive; `.`
// and `@` are reserved separators so they are NOT considered safe at
// the component level (they appear only between components).
func isAliasSafe(r rune) bool {
	switch r {
	case '-', '_', ':':
		return true
	}
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}

// ShortHash returns the first 8 hex characters of an ID — the canonical
// short-hash suffix used when an alias collides with another. Inputs
// shorter than 8 characters are returned verbatim. Exported so the
// QueryEngine and tool-rendering layers share a single source of truth
// for the suffix length.
func ShortHash(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}
