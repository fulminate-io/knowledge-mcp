// SPDX-License-Identifier: Apache-2.0

package bootstrap

import "testing"

// TestResolveReleaseTag_DevStampsRouteToLatest pins the coupling between the
// version stamp and the self-update path, which is not obvious from either side
// alone: bootstrap.Version exists to give the version fields BUILD IDENTITY, and
// the same variable decides which published release `knowledge install` and
// `knowledge setup`'s self-update leg fetch.
//
// The "dev- stamp carrying a sha" row is the one that matters. Stamping the
// Makefile's local build lines gives every locally-built client a version
// naming its commit, and no such tag is ever published — measured against the
// real endpoint, /repos/fulminate-io/knowledge-mcp/releases/tags/
// v0.8.1-312-g214aaf97-dirty answers 404 while /releases/latest answers 200. If
// that row resolved to a pinned tag, self-update would fail on every
// locally-built binary, so this is the assertion that keeps build identity from
// costing the install path.
//
// The "real release tag" rows are the KNOWN-NEGATIVE: without them a
// resolveReleaseTag that returned isLatest for EVERYTHING would satisfy every
// other row here, and a released client would silently stop pulling the server
// matching its own version.
func TestResolveReleaseTag_DevStampsRouteToLatest(t *testing.T) {
	cases := []struct {
		name       string
		version    string
		wantTag    string
		wantLatest bool
	}{
		{name: "bare dev sentinel (plain go build)", version: "dev", wantTag: "", wantLatest: true},
		{name: "dev stamp carrying a sha", version: "dev-v0.8.1-312-g214aaf97", wantTag: "", wantLatest: true},
		{name: "dev stamp from a dirty tree", version: "dev-v0.8.1-312-g214aaf97-dirty", wantTag: "", wantLatest: true},
		{name: "dev stamp outside a git checkout", version: "dev-unknown", wantTag: "", wantLatest: true},
		{name: "released client pins its own tag", version: "v0.8.1", wantTag: "v0.8.1", wantLatest: false},
		{name: "released pre-release pins its own tag", version: "v0.8.1-rc.1", wantTag: "v0.8.1-rc.1", wantLatest: false},
		// "development" is not the sentinel followed by a separator, so it is
		// not a dev build: the check must not widen to a bare prefix match.
		{name: "a tag merely starting with the sentinel letters is not a dev build", version: "development", wantTag: "development", wantLatest: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tag, isLatest := resolveReleaseTag(tc.version)
			if isLatest != tc.wantLatest {
				t.Fatalf("resolveReleaseTag(%q) isLatest = %v, want %v", tc.version, isLatest, tc.wantLatest)
			}
			if tag != tc.wantTag {
				t.Fatalf("resolveReleaseTag(%q) tag = %q, want %q", tc.version, tag, tc.wantTag)
			}
		})
	}
}

// TestCompareReleaseVersions_DevStampStaysUncomparable covers the OTHER
// consumer the stamp reaches: runInstallFull's downgrade guard, which refuses
// an install when the resolved release is older than the running one. That
// guard is documented to skip when a version is unparseable, "e.g. an
// un-ldflagged dev build", and a dev stamp must keep that property — a stamp
// that started parsing would let the guard compare a local build's sha-bearing
// string against a real release and refuse an install on the strength of it.
//
// The final row is the KNOWN-POSITIVE: without a pair the parser DOES compare,
// a parseSemverCore that had stopped parsing anything would satisfy every row
// above and this test would prove nothing.
func TestCompareReleaseVersions_DevStampStaysUncomparable(t *testing.T) {
	for _, dev := range []string{"dev", "dev-v0.8.1-312-g214aaf97", "dev-v0.8.1-312-g214aaf97-dirty", "dev-unknown"} {
		if _, ok := compareReleaseVersions("v0.8.1", dev); ok {
			t.Errorf("compareReleaseVersions(%q, %q) reported a comparable pair; a dev build must stay uncomparable so the downgrade guard skips it", "v0.8.1", dev)
		}
	}
	if cmp, ok := compareReleaseVersions("v0.8.0", "v0.8.1"); !ok || cmp != -1 {
		t.Fatalf("compareReleaseVersions(v0.8.0, v0.8.1) = (%d, %v), want (-1, true) — two real release tags must still compare, or the rows above are vacuous", cmp, ok)
	}
}
