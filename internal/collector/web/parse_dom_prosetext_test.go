// SPDX-License-Identifier: Apache-2.0

package web

import (
	"strings"
	"testing"
)

// TestCollectProseText_BrSeparatesTokens pins the de-welding fix: a <br> is
// a childless element, so before this change emitProseText wrote nothing for
// it and the tokens on either side welded into one word. Observed on a live
// page across 150 <br> elements as `$_POST["user"];$command`.
func TestCollectProseText_BrSeparatesTokens(t *testing.T) {
	t.Parallel()

	welded := parseFirstElement(t,
		`<html><body><div id="host">$account = $_GET["account"];<br/>$listing = 'ls -l';<br/>shell_exec($listing);</div></body></html>`,
		"div")

	got := collectProseText(welded)
	if strings.Contains(got, `;$listing`) {
		t.Errorf("tokens welded across <br>: %q", got)
	}
	want := `$account = $_GET["account"]; $listing = 'ls -l'; shell_exec($listing);`
	if got != want {
		t.Errorf("collectProseText across <br>\n got %q\nwant %q", got, want)
	}

	// KNOWN-NEGATIVE LEG. br-free input must be byte-identical to what the
	// flat collapse produced before this change, so the fix cannot be
	// satisfied by rewriting whitespace handling generally.
	plain := parseFirstElement(t,
		"<html><body><div id=\"host\">one   two\n\tthree    four</div></body></html>",
		"div")
	if got, want := collectProseText(plain), "one two three four"; got != want {
		t.Errorf("br-free input changed shape\n got %q\nwant %q", got, want)
	}
}

// TestCollectProseText_SkipsScriptAndStyle pins the leak fix. Script and
// style source were previously stored verbatim as page content by both
// collectProseText and collectText.
//
// The surrounding-prose leg is what stops the fix being satisfied by
// returning empty.
func TestCollectProseText_SkipsScriptAndStyle(t *testing.T) {
	t.Parallel()

	const jsToken = "analyticsBeaconFired"
	const cssToken = "nav-rail-collapsed"
	const proseToken = "the prose that must survive"

	host := parseFirstElement(t,
		`<html><body><div id="host">`+
			`<style>.`+cssToken+` { display: none; }</style>`+
			`before `+proseToken+` after`+
			`<script>var `+jsToken+` = true;</script>`+
			`</div></body></html>`,
		"div")

	for _, probe := range []struct {
		name string
		got  string
	}{
		{"collectProseText", collectProseText(host)},
		{"collectText", collectText(host)},
	} {
		if strings.Contains(probe.got, jsToken) {
			t.Errorf("%s leaked script source (%q): %q", probe.name, jsToken, probe.got)
		}
		if strings.Contains(probe.got, cssToken) {
			t.Errorf("%s leaked style source (%q): %q", probe.name, cssToken, probe.got)
		}
		if !strings.Contains(probe.got, proseToken) {
			t.Errorf("%s dropped the surrounding prose (%q): %q", probe.name, proseToken, probe.got)
		}
	}
}

// TestCollapseProseLines pins the per-line collapse phase 4's inline runs
// depend on.
func TestCollapseProseLines(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "line boundaries survive",
			in:   "first line\nsecond line\nthird line",
			want: "first line\nsecond line\nthird line",
		},
		{
			name: "intra-line whitespace collapses",
			in:   "first    line\n\tsecond \t line  ",
			want: "first line\nsecond line",
		},
		{
			name: "blank leading and trailing lines drop",
			in:   "\n  \nkept one\nkept two\n \n\n",
			want: "kept one\nkept two",
		},
		{
			name: "interior blank line is preserved as a boundary",
			in:   "para one\n\npara two",
			want: "para one\n\npara two",
		},
		{
			name: "empty input stays empty",
			in:   "   \n\t\n",
			want: "",
		},
	}
	for _, tc := range tests {
		if got := collapseProseLines(tc.in); got != tc.want {
			t.Errorf("%s: collapseProseLines(%q)\n got %q\nwant %q", tc.name, tc.in, got, tc.want)
		}
	}

	// SINGLE-LINE INPUT IS BYTE-IDENTICAL TO THE FLAT COLLAPSE
	// collectProseText already performs. This is the leg that keeps the new
	// helper from drifting away from the behaviour every existing caller
	// still relies on.
	for _, in := range []string{
		"one   two three",
		"  leading and trailing  ",
		"a\tb\tc",
		"",
	} {
		flat := strings.Join(strings.Fields(in), " ")
		if got := collapseProseLines(in); got != flat {
			t.Errorf("single-line input %q: collapseProseLines = %q, flat collapse = %q", in, got, flat)
		}
	}
}
