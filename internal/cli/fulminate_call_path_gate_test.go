// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// clientTreeRoot is cmd/knowledge, two levels up from this package.
const clientTreeRoot = "../.."

// TestFulminateBoundCallPathCensus is the census gate.
//
// It fails when a construction site exists that the manifest does not carry —
// the case that matters, because that is a new outbound path escaping the
// version stamp, and under a security-blocking gate one ungated path defeats
// the block. It also fails when the manifest names a site that no longer
// exists, so the manifest cannot rot into decoration while still reading as
// coverage.
func TestFulminateBoundCallPathCensus(t *testing.T) {
	sites, err := walkCensus(clientTreeRoot)
	if err != nil {
		t.Fatalf("walk the client tree: %v", err)
	}

	// KNOWN-POSITIVE ON THE REAL TREE. A parse that matched nothing, or a shape
	// set that quietly stopped recognizing the request forms, would otherwise
	// report the same clean green as a fully dispositioned tree. These are the
	// two chokepoints the whole version feature is built on: one CONSTRUCTS its
	// request, the other CLONES one, and the walk must see both.
	found := map[string]bool{}
	for _, s := range sites {
		found[s.key()] = true
	}
	for _, want := range []string{
		"internal/auth/sync_transport.go:http.NewRequestWithContext#1",
		"internal/graphclient/cloud_auth.go:req.Clone#1",
	} {
		if !found[want] {
			t.Fatalf("the walk did not locate the known-stamped chokepoint %q, so this census examined nothing meaningful and its green proves nothing", want)
		}
	}
	if len(sites) < 20 {
		t.Fatalf("the walk found only %d sites across the whole client tree, which is far below the known population; the parse or the root is wrong", len(sites))
	}

	// SET EQUALITY, both directions.
	manifest := map[string]censusRow{}
	for _, r := range fulminateCallPathManifest {
		if _, dup := manifest[r.key()]; dup {
			t.Errorf("manifest carries %q twice", r.key())
		}
		manifest[r.key()] = r
	}

	for _, s := range sites {
		if _, ok := manifest[s.key()]; !ok {
			t.Errorf("UNDISPOSITIONED outbound construction site %s (%s:%d).\n"+
				"  Every call this binary makes must be classified: if it reaches a Fulminate service it has to carry the client version, and if it does not it has to say so.\n"+
				"  Add a row to fulminateCallPathManifest with one of: %s, %s, %s, %s.\n"+
				"  The shapes this census recognizes are: %s.",
				s.key(), s.file, s.line, dispStampedHere, dispStampedTransport, dispReaches, dispExcluded,
				strings.Join(callShapes, ", "))
		}
	}
	for key := range manifest {
		if !found[key] {
			t.Errorf("STALE manifest row %q names a site that no longer exists; a manifest that outlives its sites reads as coverage while covering nothing", key)
		}
	}

	// THE REFERENTIAL ASSERTION on the real manifest.
	for _, p := range checkReferential(fulminateCallPathManifest) {
		t.Errorf("manifest referential rule: %s", p)
	}
}

// TestFulminateCensus_ReferentialRuleRejectsBadReferents drives the referential
// assertion against THIRD INPUTS the real manifest cannot supply.
//
// These are permanent rather than one-off. The rule exists because
// reaches-a-dispositioned-site is the only disposition making a claim about
// ANOTHER row: without the check a future author could label a constructor
// covered whether or not any downstream exists — including when the request it
// actually produces is dispositioned out of scope, which records an ungated
// path as a gated one.
func TestFulminateCensus_ReferentialRuleRejectsBadReferents(t *testing.T) {
	// THE CONTROL, in the same run: a well-formed manifest produces no
	// problems, so a rule that rejected everything reads differently from one
	// that discriminates.
	wellFormed := []censusRow{
		{File: "a.go", Symbol: "http.NewRequest#1", Disposition: dispStampedHere},
		{File: "b.go", Symbol: "New#1", Disposition: dispReaches, Reaches: "a.go:http.NewRequest#1"},
	}
	if problems := checkReferential(wellFormed); len(problems) != 0 {
		t.Fatalf("the control failed: a well-formed manifest reported %v", problems)
	}

	cases := []struct {
		name string
		rows []censusRow
		want string
	}{
		{
			name: "a dangling referent naming no row at all",
			rows: []censusRow{
				{File: "a.go", Symbol: "http.NewRequest#1", Disposition: dispStampedHere},
				{File: "b.go", Symbol: "New#1", Disposition: dispReaches, Reaches: "nowhere.go:http.NewRequest#9"},
			},
			want: "not a row in this manifest",
		},
		{
			name: "a referent resolving to an EXCLUDED row",
			rows: []censusRow{
				{File: "a.go", Symbol: "http.NewRequest#1", Disposition: dispExcluded},
				{File: "b.go", Symbol: "New#1", Disposition: dispReaches, Reaches: "a.go:http.NewRequest#1"},
			},
			want: "recorded as out of scope",
		},
		{
			name: "a referring row carrying no referent",
			rows: []censusRow{
				{File: "b.go", Symbol: "New#1", Disposition: dispReaches},
			},
			want: "carries no reaches referent",
		},
		{
			name: "a stray referent on a non-referring row",
			rows: []censusRow{
				{File: "a.go", Symbol: "http.NewRequest#1", Disposition: dispStampedHere},
				{File: "b.go", Symbol: "New#1", Disposition: dispExcluded, Reaches: "a.go:http.NewRequest#1"},
			},
			want: "as much a mislabel",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			problems := checkReferential(tc.rows)
			if len(problems) == 0 {
				t.Fatalf("the referential rule accepted a manifest it must reject")
			}
			joined := strings.Join(problems, "\n")
			if !strings.Contains(joined, tc.want) {
				t.Errorf("the rejection does not name the defect (%q): %s", tc.want, joined)
			}
		})
	}
}

// TestFulminateCensus_WalkSeesEveryNarrowingAxis is the narrowing-axis third
// input, and it is permanent.
//
// All THREE narrowings below were real and were removed together. A gate
// narrowed once will be narrowed again by a future edit, and this leg fails
// when it is: it synthesizes a tree carrying a Fulminate-bound request that is
// out of scope on EVERY axis at once —
//
//   - built from a CloudEndpoint VARIABLE rather than passed as a call
//     argument, so an endpoint-keyed sweep alone cannot see it;
//   - located in internal/bootstrap, the package an earlier allowlist excluded
//     and the likeliest home for a new outbound call;
//   - reached through net.DialTimeout, a package FUNCTION that a
//     dialer-method-only shape set misses entirely.
func TestFulminateCensus_WalkSeesEveryNarrowingAxis(t *testing.T) {
	root := t.TempDir()

	// OUT OF SCOPE ON EVERY AXIS AT ONCE.
	bootstrapDir := filepath.Join(root, "internal", "bootstrap")
	if err := os.MkdirAll(bootstrapDir, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	const hidden = `package bootstrap

import (
	"context"
	"net"
	"net/http"
	"time"
)

const CloudEndpoint = "https://api.example.invalid"

func reachOut(ctx context.Context) {
	base := CloudEndpoint
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/thing", nil)
	_ = req
	c, _ := net.DialTimeout("tcp", "host:443", time.Second)
	_ = c
}
`
	if err := os.WriteFile(filepath.Join(bootstrapDir, "hidden.go"), []byte(hidden), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	// THE IN-SCOPE CONTROL, in the same run: a site the narrowest form of the
	// walk would still have found. Without it, a walk that resolved NOTHING
	// would be indistinguishable from one that found everything.
	cliDir := filepath.Join(root, "internal", "cli")
	if err := os.MkdirAll(cliDir, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	const control = `package cli

import (
	"context"
	"net/http"
)

const CloudEndpoint = "https://api.example.invalid"

func obvious(ctx context.Context) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, CloudEndpoint, nil)
	_ = req
}
`
	if err := os.WriteFile(filepath.Join(cliDir, "obvious.go"), []byte(control), 0o600); err != nil {
		t.Fatalf("write control: %v", err)
	}

	sites, err := walkCensus(root)
	if err != nil {
		t.Fatalf("walk the fixture tree: %v", err)
	}
	got := map[string]bool{}
	for _, s := range sites {
		got[s.key()] = true
	}

	if !got["internal/cli/obvious.go:http.NewRequestWithContext#1"] {
		t.Fatalf("the IN-SCOPE CONTROL was not found, so this fixture measures nothing")
	}
	for _, want := range []struct {
		key  string
		axis string
	}{
		{"internal/bootstrap/hidden.go:http.NewRequestWithContext#1",
			"a request built from a CloudEndpoint VARIABLE, in a package an earlier allowlist excluded"},
		{"internal/bootstrap/hidden.go:net.DialTimeout#1",
			"net.DialTimeout, a package function a dialer-method-only shape set misses"},
	} {
		if !got[want.key] {
			t.Errorf("the walk missed %s — %s. A narrowing has come back; every one of these was removed deliberately and none may return.", want.key, want.axis)
		}
	}
}
