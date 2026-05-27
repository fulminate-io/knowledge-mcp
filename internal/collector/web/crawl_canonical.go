// SPDX-License-Identifier: Apache-2.0

package web

import (
	"net/url"
	"strings"
)

// canonicalizeURL returns a canonical form of raw suitable for use as a
// visited-set key and as a loop-detection comparison key. Normalizations
// applied:
//
//   - Trim leading/trailing whitespace.
//   - Parse via net/url; empty input or parse failure → "".
//   - Lowercase scheme + host.
//   - Strip #fragment.
//   - Normalize trailing slash: strip unless Path == "/". So
//     "https://a.com/foo/" → "https://a.com/foo"; "https://a.com/" → "https://a.com/".
//   - Query is preserved verbatim (RawQuery left intact; keys NOT sorted,
//     params NOT dropped) — legitimate variant pages like ?sid=1 vs ?sid=2
//     remain distinct in the visited set.
//
// This is the single source of truth for visited-set and loop-detection
// keys; normalizeURL delegates here so every consumer sees the same form.
func canonicalizeURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u == nil {
		return ""
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	u.Fragment = ""
	if len(u.Path) > 1 && strings.HasSuffix(u.Path, "/") {
		u.Path = strings.TrimRight(u.Path, "/")
	}
	return u.String()
}

// recordHost returns the lowercased host of the page record's URL (or
// FinalURL when URL fails to parse). Returns "" when neither parses,
// which signals callers to treat the host as "unknown" and skip host
// accounting rather than collapsing all unknown pages into one bucket.
func recordHost(record *pageRecord) string {
	for _, raw := range []string{record.URL, record.FinalURL} {
		if raw == "" {
			continue
		}
		u, err := url.Parse(raw)
		if err == nil && u != nil && u.Host != "" {
			return strings.ToLower(u.Host)
		}
	}
	return ""
}

// pathSegmentCount returns the number of non-empty path segments in raw.
// A URL whose parse fails, or one with an empty/"/" path, returns 0.
// Examples: "https://a.com/" → 0; "https://a.com/x" → 1;
// "https://a.com/x/y/z" → 3; "https://a.com/x/y/z/" → 3 (trailing slash
// not counted).
func pathSegmentCount(raw string) int {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u == nil || u.Path == "" {
		return 0
	}
	count := 0
	for seg := range strings.SplitSeq(u.Path, "/") {
		if seg != "" {
			count++
		}
	}
	return count
}
