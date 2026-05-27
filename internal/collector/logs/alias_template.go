// SPDX-License-Identifier: Apache-2.0

package logs

import (
	"strings"
	"unicode"

	wirelogs "github.com/fulminate-io/knowledge-mcp/internal/logwire"
)

// TemplateAliasFor derives a readable alias for a template from its
// pattern and severity. K8s-style "<Reason>: " prefixes are stripped
// (the reason is already implied by the stream's `reason` label, so
// repeating it just adds noise), wildcards and stopwords are removed,
// the first few meaningful tokens are kebab-cased, and a short
// severity suffix is appended after `@`. Examples:
//
//	"Node <*> is not ready" / WARN              -> node-not-ready@warn
//	"OOMKilled container <*>" / ERR             -> oomkilled-container@err
//	"NodeNotReady: Node is not ready" / ERR     -> node-not-ready@err
//	"FailedMount: MountVolume.SetUp ..." / ERR  -> mountvolume-setup-failed-volume@err
//
// Returns the empty string when the pattern is empty or contains no
// tokens after stripping; callers should fall back to a hash-derived
// locator in that case.
func TemplateAliasFor(tmpl *wirelogs.LogTemplate) string {
	if tmpl == nil {
		return ""
	}
	body := stripReasonPrefix(tmpl.Pattern)
	tokens := meaningfulTokens(body, 5)
	if len(tokens) == 0 {
		return ""
	}
	kebab := kebabCase(tokens)
	suffix := severityShort(tmpl.Severity)
	if suffix == "" {
		return kebab
	}
	return kebab + "@" + suffix
}

// stripReasonPrefix removes a leading K8s-style "<Reason>: " prefix
// from a template pattern when present. <Reason> must be a single
// CamelCase identifier (letters only, starts uppercase) followed by
// ": ". When the prefix doesn't match the heuristic, returns the
// pattern unchanged.
func stripReasonPrefix(pattern string) string {
	idx := strings.Index(pattern, ": ")
	if idx <= 0 || idx >= len(pattern)-2 {
		return pattern
	}
	prefix := pattern[:idx]
	if len(prefix) < 2 || !unicode.IsUpper(rune(prefix[0])) {
		return pattern
	}
	for _, r := range prefix {
		if !unicode.IsLetter(r) {
			return pattern
		}
	}
	return pattern[idx+2:]
}

// severityShort maps a canonical severity to its short alias suffix.
// Unknown values fall back to a lowercased copy so the suffix is still
// useful even when the severity comes from a non-standard provider.
func severityShort(sev string) string {
	switch sev {
	case wirelogs.SeverityCritical:
		return "crit"
	case wirelogs.SeverityError:
		return "err"
	case wirelogs.SeverityWarn:
		return "warn"
	case wirelogs.SeverityInfo:
		return "info"
	case wirelogs.SeverityDebug:
		return "debug"
	case wirelogs.SeverityTrace:
		return "trace"
	case "":
		return ""
	default:
		return strings.ToLower(sev)
	}
}

// templateStopwords is the small set of English connectors stripped
// from template aliases. Keeping the list short avoids accidentally
// dropping meaningful terms (e.g. "is" is a stopword, but "in" is too —
// any longer list quickly becomes risky).
var templateStopwords = map[string]struct{}{
	"a": {}, "an": {}, "the": {},
	"of": {}, "on": {}, "for": {}, "to": {}, "in": {}, "is": {},
	"and": {}, "or": {}, "with": {},
}

// stripWildcards removes Drain `<*>` placeholders from the pattern.
// Adjacent whitespace is preserved so token splitting still works.
func stripWildcards(pattern string) string {
	if !strings.Contains(pattern, "<*>") {
		return pattern
	}
	return strings.ReplaceAll(pattern, "<*>", " ")
}

// meaningfulTokens splits a template pattern into the first `max`
// meaningful tokens after wildcard removal, stopword stripping, and
// punctuation filtering. Tokens are lowercased so the resulting alias
// is canonical.
func meaningfulTokens(pattern string, max int) []string {
	if pattern == "" {
		return nil
	}
	cleaned := stripWildcards(pattern)
	out := make([]string, 0, max)
	var cur strings.Builder
	flush := func() {
		if cur.Len() == 0 {
			return
		}
		tok := strings.ToLower(cur.String())
		cur.Reset()
		if _, stop := templateStopwords[tok]; stop {
			return
		}
		if len(out) < max {
			out = append(out, tok)
		}
	}
	for _, r := range cleaned {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			cur.WriteRune(r)
			continue
		}
		flush()
		if len(out) >= max {
			return out
		}
	}
	flush()
	return out
}

// kebabCase joins tokens with `-`. Tokens are assumed to already be
// lowercase and free of unsafe characters (meaningfulTokens enforces
// that), so this is a thin join helper kept separate for clarity at
// the call site.
func kebabCase(tokens []string) string {
	return strings.Join(tokens, "-")
}
