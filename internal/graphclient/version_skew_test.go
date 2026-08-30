// SPDX-License-Identifier: Apache-2.0

package graphclient

import (
	"strings"
	"testing"
)

// TestVersionSkewLine covers the full truth table: equal stamps are quiet,
// differing non-empty stamps fire with both versions in the line, either side
// empty is quiet (unknown), and the dev sentinel behaves like any other stamp
// (dev-vs-dev quiet, dev-vs-release fires).
func TestVersionSkewLine(t *testing.T) {
	cases := []struct {
		name       string
		clientVer  string
		daemonVer  string
		wantSkewed bool
		wantInLine []string // substrings that MUST be present when skewed
	}{
		{name: "equal release stamps -> quiet", clientVer: "v0.4.10", daemonVer: "v0.4.10", wantSkewed: false},
		{name: "differing release stamps -> skew", clientVer: "v0.4.11", daemonVer: "v0.4.10", wantSkewed: true, wantInLine: []string{"v0.4.11", "v0.4.10"}},
		{name: "client empty -> quiet (unknown)", clientVer: "", daemonVer: "v0.4.10", wantSkewed: false},
		{name: "daemon empty -> quiet (unknown, e.g. failed probe)", clientVer: "v0.4.10", daemonVer: "", wantSkewed: false},
		{name: "both empty -> quiet", clientVer: "", daemonVer: "", wantSkewed: false},
		{name: "dev vs dev -> quiet", clientVer: "dev", daemonVer: "dev", wantSkewed: false},
		{name: "dev vs release -> skew", clientVer: "dev", daemonVer: "v0.4.10", wantSkewed: true, wantInLine: []string{"dev", "v0.4.10"}},

		// GIT-DESCRIBE STAMPS — the shape the local build lines now inject
		// (BUILD_VERSION in the Makefile). These are the pairs that carry the
		// instrument: two binaries cut from different commits, and a binary
		// built from a dirty tree against the clean commit it sits on. The
		// "dev vs dev" row above is the state that made this check blind —
		// unstamped, both sides are the same constant no matter how far the
		// daemon has drifted, so no comparison between them can ever differ.
		{name: "stamps from different commits -> skew", clientVer: "v0.8.1-312-g214aaf97", daemonVer: "v0.8.1-311-g13eb7af7", wantSkewed: true, wantInLine: []string{"g214aaf97", "g13eb7af7"}},
		{name: "dirty tree vs the clean commit it sits on -> skew", clientVer: "v0.8.1-312-g214aaf97-dirty", daemonVer: "v0.8.1-312-g214aaf97", wantSkewed: true, wantInLine: []string{"-dirty"}},
		{name: "same commit stamp both sides -> quiet", clientVer: "v0.8.1-312-g214aaf97", daemonVer: "v0.8.1-312-g214aaf97", wantSkewed: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			line, skewed := VersionSkewLine(tc.clientVer, tc.daemonVer)
			if skewed != tc.wantSkewed {
				t.Fatalf("VersionSkewLine(%q,%q) skewed=%v, want %v", tc.clientVer, tc.daemonVer, skewed, tc.wantSkewed)
			}
			if !tc.wantSkewed {
				if line != "" {
					t.Fatalf("VersionSkewLine(%q,%q) not skewed but line=%q, want empty", tc.clientVer, tc.daemonVer, line)
				}
				return
			}
			for _, sub := range tc.wantInLine {
				if !strings.Contains(line, sub) {
					t.Fatalf("VersionSkewLine(%q,%q) line=%q, want it to contain %q", tc.clientVer, tc.daemonVer, line, sub)
				}
			}
		})
	}
}
