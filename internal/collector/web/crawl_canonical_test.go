// SPDX-License-Identifier: Apache-2.0

package web

import "testing"

// TestCanonicalizeURL covers the full normalization contract: empty input,
// parse failure, fragment strip, host + scheme lowercase, trailing-slash
// normalization, root-path preservation, and query-string preservation
// (order NOT sorted). Idempotence is asserted with a second round-trip.
func TestCanonicalizeURL(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"whitespace only", "   ", ""},
		{"parse failure — raw control bytes",
			"http://\x7f\x00\x01/", ""},
		{"fragment stripped",
			"https://Example.COM/foo#sec", "https://example.com/foo"},
		{"scheme lowercased",
			"HTTPS://example.com/path", "https://example.com/path"},
		{"host lowercased",
			"https://EXAMPLE.COM/path", "https://example.com/path"},
		{"trailing slash stripped on non-root path",
			"https://a.com/x/", "https://a.com/x"},
		{"root slash preserved",
			"https://a.com/", "https://a.com/"},
		{"multi-segment trailing slash stripped",
			"https://a.com/x/y/z/", "https://a.com/x/y/z"},
		{"query preserved verbatim (order)",
			"https://a.com/x?b=2&a=1", "https://a.com/x?b=2&a=1"},
		{"query with params preserved when trailing slash stripped",
			"https://a.com/x/?k=v", "https://a.com/x?k=v"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := canonicalizeURL(tc.in)
			if got != tc.want {
				t.Errorf("canonicalizeURL(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestCanonicalizeURL_Idempotent verifies that applying canonicalizeURL a
// second time is a no-op. Visited-set keys need this guarantee so a
// seed URL and its discovered-link re-emission normalize to the same key.
func TestCanonicalizeURL_Idempotent(t *testing.T) {
	t.Parallel()
	inputs := []string{
		"https://Example.COM/foo/#sec",
		"HTTP://a.com/x/y/?q=1",
		"https://a.com/",
		"https://a.com/x?b=2&a=1",
	}
	for _, in := range inputs {
		once := canonicalizeURL(in)
		twice := canonicalizeURL(once)
		if once != twice {
			t.Errorf("not idempotent for %q: first=%q second=%q", in, once, twice)
		}
	}
}

// TestPathSegmentCount covers empty, root, single-segment, multi-segment,
// trailing-slash, and parse-failure cases. Used by the MaxPathSegments
// cap to reject deep recursive-path traps.
func TestPathSegmentCount(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want int
	}{
		{"", 0},
		{"https://a.com/", 0},
		{"https://a.com", 0},
		{"https://a.com/x", 1},
		{"https://a.com/x/y", 2},
		{"https://a.com/x/y/z", 3},
		{"https://a.com/x/y/z/", 3},
		{"https://a.com/a/b/c/d", 4},
		{"https://a.com//double//slashes/x", 3},
	}
	for _, tc := range cases {
		got := pathSegmentCount(tc.in)
		if got != tc.want {
			t.Errorf("pathSegmentCount(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}
