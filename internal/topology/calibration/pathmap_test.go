// SPDX-License-Identifier: Apache-2.0

package calibration

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
)

// Every test in this file is a PURE STRING assertion. None stats the
// filesystem, dials a daemon or reaches the network, which is what lets them
// name internal paths that do not exist in the public mirror while still
// passing in the mirror's own CI.

// TestMapMirrorPath_ClassifiesEveryAlertPath drives the five REAL alert paths
// from the frozen ground-truth corpus. A mapping rule that fails on the paths
// the alerts actually land on is a mapping rule that reports nothing useful.
func TestMapMirrorPath_ClassifiesEveryAlertPath(t *testing.T) {
	alertPaths := []string{
		"internal/tools/tools_logs_search.go",
		"internal/collector/pdf/font/glyphlist.go",
		"internal/engine/render_explain_timeline.go",
		"internal/segmentdist/manager_bucket_partition.go",
		"internal/collector/parser/indexer_discover_report.go",
	}
	for _, mirror := range alertPaths {
		internal, class, err := MapMirrorPath(mirror)
		if err != nil {
			t.Fatalf("MapMirrorPath(%s): %v", mirror, err)
		}
		if class != PathMapped {
			t.Fatalf("%s classified %s, want mapped", mirror, class)
		}
		want := "cmd/knowledge/" + mirror
		if internal != want {
			t.Fatalf("%s mapped to %q, want %q", mirror, internal, want)
		}
		// The inverse must land back on the input, or the two directions have
		// drifted and a report will name a file the reader cannot find.
		back, backClass, backErr := MapInternalPath(internal)
		if backErr != nil {
			t.Fatalf("MapInternalPath(%s): %v", internal, backErr)
		}
		if back != mirror || backClass != PathMapped {
			t.Fatalf("round trip of %s gave (%q, %s)", mirror, back, backClass)
		}
	}
}

// TestMapMirrorPath_ClassifiesMirrorOnly is the KNOWN-NEGATIVE control. Without
// it a map that returned PathMapped for everything would satisfy the positive
// test above. Every member was verified to exist in the real mirror with git
// ls-files; do not add or substitute one without doing the same, or the fixture
// becomes a statement about an imagined tree.
func TestMapMirrorPath_ClassifiesMirrorOnly(t *testing.T) {
	mirrorOnly := []string{
		"README.md",
		".github/workflows/ci.yml",
		".github/CODEOWNERS",
		"scripts/sync-assets.sh",
		"server.json",
		// The mirror's ROOT-level copy, a NEAR-MISS on the exact-match rule
		// {mirror: ".claude/KNOWLEDGE_TOOLS.md"}: a path map that ever degraded
		// an exact-match rule into a basename or suffix match would classify
		// this PathMapped and this control would fire.
		"KNOWLEDGE_TOOLS.md",
		"docs/diagrams/human-at-the-beginning.mmd",
		// A docs/ path OUTSIDE docs/guides/, which is the case that
		// distinguishes the guides rule from a blanket docs rule.
		"docs/cla/ICLA.md",
	}
	for _, p := range mirrorOnly {
		internal, class, err := MapMirrorPath(p)
		if err != nil {
			t.Fatalf("MapMirrorPath(%s): %v", p, err)
		}
		if class != PathMirrorOnly {
			t.Fatalf("%s classified %s, want mirror-only", p, class)
		}
		if internal != "" {
			t.Fatalf("%s is mirror-only but produced counterpart %q", p, internal)
		}
	}
}

// TestMapPath_ShipSetAdditionsRoundTrip drives the two ship-set members the sync
// script gained when the mirror's CI was repaired: the SHARED test vectors under
// testdata/ and the lint config .golangci.yml. Both are placed at the mirror ROOT
// under the same name they carry here, so each direction must return the path as
// its own counterpart, classified mapped.
//
// WHY THIS IS A TEST AT ALL: nothing derives this table from the sync script, so
// a ship-set addition with no rule here silently degrades every alert on the new
// file to "no counterpart in this repo". This is the only instrument that fires
// on that.
//
// KNOWN-NEGATIVES IN THE SAME RUN, because a rule widened past its member is the
// failure a positive-only assertion cannot see. A path merely NAMED like the
// lint config must stay unmapped, and a testdata directory INSIDE the client
// module must map through the internal/ rule rather than through the new
// root-testdata one — which is what keeps the new prefix from swallowing every
// package fixture directory in the tree.
func TestMapPath_ShipSetAdditionsRoundTrip(t *testing.T) {
	for _, p := range []string{
		"testdata/contribution_hash_vector.json",
		"testdata/practice_graph_name_canonical_cases.json",
		".golangci.yml",
	} {
		internal, class, err := MapMirrorPath(p)
		if err != nil {
			t.Fatalf("MapMirrorPath(%s): %v", p, err)
		}
		if internal != p || class != PathMapped {
			t.Errorf("MapMirrorPath(%s) = (%q,%s), want (%q,%s)", p, internal, class, p, PathMapped)
		}
		mirror, backClass, backErr := MapInternalPath(p)
		if backErr != nil {
			t.Fatalf("MapInternalPath(%s): %v", p, backErr)
		}
		if mirror != p || backClass != PathMapped {
			t.Errorf("MapInternalPath(%s) = (%q,%s), want (%q,%s)", p, mirror, backClass, p, PathMapped)
		}
	}

	// KNOWN-NEGATIVE ONE: near-misses on the exact-match lint rule.
	for _, p := range []string{"scripts/.golangci.yml", ".golangci.yml.bak"} {
		if _, class, err := MapMirrorPath(p); err != nil {
			t.Fatalf("MapMirrorPath(%s): %v", p, err)
		} else if class == PathMapped {
			t.Errorf("MapMirrorPath(%s) classified mapped; the lint rule is file-exact, not a suffix match", p)
		}
	}

	// THE THIRD SHIP-SET ADDITION IS DELIBERATELY NOT A RULE, and this case is
	// what pins that decision so a later reader does not "fix" it.
	//
	// The sync places docs/guides/recipes.md a SECOND time, inside the module at
	// internal/tools/testdata/recipes-guide.md, because a //go:embed there is a
	// build input. No rule is added for that destination, on two grounds.
	//
	// FIRST, CONSISTENCY: it is a generated in-module mirror, and the table
	// already maps every other one through the internal/ prefix rule without
	// chasing its generator — internal/assets/agents/planner.md maps to
	// cmd/knowledge/internal/assets/agents/planner.md, a gitignored copy of
	// .claude/agents/planner.md, and nothing points it at the canonical file.
	// One exception among them would be a rule no reader could predict.
	//
	// SECOND, AMBIGUITY: an exact rule pointing this destination at
	// docs/guides/recipes.md is truthful mirror-to-internal, but the rule list is
	// scanned in ONE order for BOTH directions, so the same rule would then claim
	// the internal-to-mirror direction too and docs/guides/recipes.md — a file
	// that genuinely ships to two places — would answer with the testdata copy
	// instead of its own identity. Truthful one way and false the other is worse
	// than consistent.
	//
	// ADDING that rule is the mutation this case exists to catch, which is the
	// inverse of the deletion the two rules above are proven against.
	const recipesMirror = "internal/tools/testdata/recipes-guide.md"
	internalRecipes, class, err := MapMirrorPath(recipesMirror)
	if err != nil {
		t.Fatalf("MapMirrorPath(%s): %v", recipesMirror, err)
	}
	if want := "cmd/knowledge/" + recipesMirror; internalRecipes != want || class != PathMapped {
		t.Errorf("MapMirrorPath(%s) = (%q,%s), want (%q,%s) — the generated in-module mirror maps through the internal/ prefix rule like every other one",
			recipesMirror, internalRecipes, class, want, PathMapped)
	}
	// And the canonical guide keeps its own identity in BOTH directions, which is
	// what an exact rule for the destination above would take away.
	const canonicalGuide = "docs/guides/recipes.md"
	if got, gotClass, gerr := MapMirrorPath(canonicalGuide); gerr != nil {
		t.Fatalf("MapMirrorPath(%s): %v", canonicalGuide, gerr)
	} else if got != canonicalGuide || gotClass != PathMapped {
		t.Errorf("MapMirrorPath(%s) = (%q,%s), want (%q,%s)", canonicalGuide, got, gotClass, canonicalGuide, PathMapped)
	}
	if got, gotClass, gerr := MapInternalPath(canonicalGuide); gerr != nil {
		t.Fatalf("MapInternalPath(%s): %v", canonicalGuide, gerr)
	} else if got != canonicalGuide || gotClass != PathMapped {
		t.Errorf("MapInternalPath(%s) = (%q,%s), want (%q,%s) — the guide ships to two places and this direction must stay the canonical one",
			canonicalGuide, got, gotClass, canonicalGuide, PathMapped)
	}

	// KNOWN-NEGATIVE TWO: a package fixture directory named testdata inside the
	// module maps through the internal/ rule, so the new root-testdata prefix is
	// anchored at the root rather than matching a path segment anywhere.
	const pkgFixture = "internal/collector/pdf/testdata/corpus/code-heavy/NOTES.md"
	internal, class, err := MapMirrorPath(pkgFixture)
	if err != nil {
		t.Fatalf("MapMirrorPath(%s): %v", pkgFixture, err)
	}
	if want := "cmd/knowledge/" + pkgFixture; internal != want || class != PathMapped {
		t.Errorf("MapMirrorPath(%s) = (%q,%s), want (%q,%s)", pkgFixture, internal, class, want, PathMapped)
	}
}

// TestMapPath_GovernanceClassifiesAsClaudeAsset drives the FLAT governance file
// in BOTH directions: the sync script ships it at an identical path, so each
// direction must return the path as its own counterpart, classified mapped.
//
// The leak gate's scan set derives from these classifications, so a governance
// file the map does not recognize is a shipped file the gate never sees.
//
// KNOWN-NEGATIVE, and it is the whole reason this is a test rather than a grep
// for the literal: a sibling FLAT file under .claude/skills/ that the sync
// script does NOT ship must stay unmapped. A classifier widened to the whole
// directory — ".claude/skills/*.md" instead of the equality this asserts —
// passes the positive half and fails here.
func TestMapPath_GovernanceClassifiesAsClaudeAsset(t *testing.T) {
	const governance = ".claude/skills/GOVERNANCE.md"

	internal, class, err := MapMirrorPath(governance)
	if err != nil {
		t.Fatalf("MapMirrorPath(%s): %v", governance, err)
	}
	if class != PathMapped || internal != governance {
		t.Errorf("MapMirrorPath(%s) = (%q,%s), want (%q,%s)", governance, internal, class, governance, PathMapped)
	}

	mirror, class, err := MapInternalPath(governance)
	if err != nil {
		t.Fatalf("MapInternalPath(%s): %v", governance, err)
	}
	if class != PathMapped || mirror != governance {
		t.Errorf("MapInternalPath(%s) = (%q,%s), want (%q,%s)", governance, mirror, class, governance, PathMapped)
	}

	const unshipped = ".claude/skills/NOT_SHIPPED.md"
	if _, class, err := MapMirrorPath(unshipped); err != nil {
		t.Fatalf("MapMirrorPath(%s): %v", unshipped, err)
	} else if class == PathMapped {
		t.Errorf("MapMirrorPath(%s) classified mapped; the classifier is directory-wide, not file-exact", unshipped)
	}
	if _, class, err := MapInternalPath(unshipped); err != nil {
		t.Fatalf("MapInternalPath(%s): %v", unshipped, err)
	} else if class == PathMapped {
		t.Errorf("MapInternalPath(%s) classified mapped; the classifier is directory-wide, not file-exact", unshipped)
	}
}

// TestMapInternalPath_ClassifiesInternalOnly is the mirror image of the control
// above, for the direction the reporting half consumes. The sync script never
// copies the server binary, the deploy tree or the proto sources.
func TestMapInternalPath_ClassifiesInternalOnly(t *testing.T) {
	internalOnly := []string{
		"cmd/knowledge-server/internal/store/graph.go",
		"proto/knowledge/v1/knowledge.proto",
		"deploy/cloud/main.tf",
		"scripts/sync-to-oss.sh",
		"docs/cla/ICLA.md",
	}
	for _, p := range internalOnly {
		mirror, class, err := MapInternalPath(p)
		if err != nil {
			t.Fatalf("MapInternalPath(%s): %v", p, err)
		}
		if class != PathInternalOnly {
			t.Fatalf("%s classified %s, want internal-only", p, class)
		}
		if mirror != "" {
			t.Fatalf("%s is internal-only but produced counterpart %q", p, mirror)
		}
	}
}

// TestMapPath_RejectsNonRelativeInput proves the repo-relative precondition
// discriminates in all four directions, and that the returned error NAMES the
// offending value rather than only describing the rule. An error that says
// "takes a repo-relative path" without quoting what it was handed leaves the
// caller to guess which of its inputs was rejected.
//
// The rejection also must not leak out as a classification: a refused path
// returns an empty counterpart, never a plausible-looking mapped one.
func TestMapPath_RejectsNonRelativeInput(t *testing.T) {
	cases := []struct {
		name string
		bad  string
		call func(string) (string, PathClass, error)
	}{
		{"absolute mirror", "/etc/passwd", MapMirrorPath},
		{"escaping mirror", "internal/../../etc/passwd", MapMirrorPath},
		{"absolute internal", "/etc/passwd", MapInternalPath},
		{"escaping internal", "cmd/knowledge/../../etc", MapInternalPath},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, _, err := tc.call(tc.bad)
			if err == nil {
				t.Fatalf("%q was accepted; expected an error naming it", tc.bad)
			}
			if !strings.Contains(err.Error(), "repo-relative") {
				t.Fatalf("error must state the precondition, got: %v", err)
			}
			if !strings.Contains(err.Error(), tc.bad) {
				t.Fatalf("error must name the offending value %q, got: %v", tc.bad, err)
			}
			if got != "" {
				t.Fatalf("a refused path must not produce a counterpart, got %q", got)
			}
		})
	}

	// KNOWN-POSITIVE CONTROL. Every case above asserts a rejection, and a
	// mapper that rejected EVERYTHING would satisfy all four while being
	// useless. One well-formed path must still come back clean.
	if _, class, err := MapMirrorPath("internal/tools/tools_logs_search.go"); err != nil || class != PathMapped {
		t.Fatalf("a well-formed path must still map: class=%s err=%v", class, err)
	}
}

// NO CONTIGUOUS MODULE PATH IS SPELLED AS A LITERAL ANYWHERE IN THIS PACKAGE,
// and the import cases below are assembled from the package's split constants
// for that reason. MEASURED: with the module paths written out in full, running
// the sync script's five sed rules over this file rewrote NINE lines,
// TestMapInternalImport_RewritesInSyncOrder degenerated into five identity
// assertions that pass while testing nothing, and TestMapMirrorImport_InvertsRewrite
// went outright RED — in the public mirror's CI, the one environment a green
// here is supposed to promise. TestPackageSurvivesSyncRewrite is the gate that
// keeps a future edit from spelling one out again.

// TestMapInternalImport_RewritesInSyncOrder drives all five sed rules in the
// internal-to-mirror direction. The first two cases together are the
// rule-1-before-rule-2 catcher: applied in the other order the shorter prefix
// wins and the internal tree never reaches its own mirror location.
func TestMapInternalImport_RewritesInSyncOrder(t *testing.T) {
	cases := []struct{ in, want string }{
		{
			in:   internalModule + "/cmd/knowledge/internal/tools",
			want: mirrorModule + "/internal/tools",
		},
		{
			in:   internalModule + "/cmd/knowledge",
			want: mirrorModule,
		},
		{
			in:   internalModule + "/gen/knowledge/v1",
			want: mirrorModule + "/gen/knowledge/v1",
		},
		{
			// THE BARE ROOT MODULE. Rule 4 requires a following non-hyphen
			// character, so it cannot fire here; an implementation with only
			// four rules leaves this unchanged.
			in:   internalModule,
			want: mirrorModule,
		},
		{
			// Already rewritten: rule 4's not-a-hyphen guard is what stops this
			// from growing a second -mcp suffix.
			in:   mirrorModule + "/internal/tools",
			want: mirrorModule + "/internal/tools",
		},
	}
	for _, tc := range cases {
		if got := MapInternalImport(tc.in); got != tc.want {
			t.Fatalf("MapInternalImport(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestMapMirrorImport_InvertsRewrite drives the mirror-to-internal direction.
// Asserting only one direction is what let an earlier inversion of these two
// functions go unnoticed.
func TestMapMirrorImport_InvertsRewrite(t *testing.T) {
	cases := []struct{ in, want string }{
		{
			in:   mirrorModule + "/internal/tools",
			want: internalModule + "/cmd/knowledge/internal/tools",
		},
		{
			in:   mirrorModule + "/gen/knowledge/v1",
			want: internalModule + "/gen/knowledge/v1",
		},
		{
			// The bare mirror root resolves to the CLIENT module, the root
			// every real import in the mirrored subtree came from.
			in:   mirrorModule,
			want: internalModule + "/cmd/knowledge",
		},
		{
			// Already internal: returned unchanged. The double-rewrite catcher.
			in:   internalModule + "/cmd/knowledge/internal/tools",
			want: internalModule + "/cmd/knowledge/internal/tools",
		},
		{
			// An unrelated module must not be touched.
			in:   "github.com/google/go-github/v68/github",
			want: "github.com/google/go-github/v68/github",
		},
	}
	for _, tc := range cases {
		if got := MapMirrorImport(tc.in); got != tc.want {
			t.Fatalf("MapMirrorImport(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestPackageSurvivesSyncRewrite asserts no NON-IMPORT string literal in this
// package would be changed by the sync script's module rewrite. This package
// ships to the public mirror, and the rewrite runs over test files exactly as it
// runs over source, so a spelled-out module path here is silently a DIFFERENT
// test in the mirror than the one that went green in this repo.
//
// IMPORT PATHS ARE EXEMPT, AND THE EXEMPTION IS THE RULE RATHER THAN A HOLE IN
// IT. The sync script rewrites an import line so the mirror's copy imports the
// mirror's module — that is the rewrite working, not a hazard. An earlier form
// of this test flagged every changed line and passed only by accident, because
// the package then imported nothing from this module; the moment score.go
// imported the topology foundation it reported four correct import lines as
// defects. What is hazardous is a literal whose ASSERTION depends on the two
// module paths being DIFFERENT, and only a non-import literal can be one.
//
// WHY THIS PACKAGE AND NOT EVERY PACKAGE, because the distinction is what makes
// the rule worth obeying. A consistent rewrite of BOTH sides of a comparison
// PRESERVES a test: a literal compared against source read at runtime is
// rewritten in the same pass as the source it is compared to, and still asserts
// the same property. The shape that breaks is a test whose assertion depends on
// the two module paths BEING DIFFERENT — rewriting both sides collapses the
// mapping under test to the identity. That is self-referential: a test about
// the rewrite, subjected to the rewrite. This package is exactly that, which is
// why the constants are split here and need not be split elsewhere.
//
// MapInternalImport is the model of the rewrite: it applies the same five rules
// in the same order, and rule five is end-anchored, so applying it per LINE is
// what sed does per line.
func TestPackageSurvivesSyncRewrite(t *testing.T) {
	// KNOWN-POSITIVE CONTROL. Without it, a scan that measured nothing — a
	// mis-set working directory, a model that rewrites nothing — is
	// indistinguishable from a genuinely clean package.
	control := "\t\"" + internalModule + "/cmd/knowledge/internal/tools\""
	if MapInternalImport(control) == control {
		t.Fatal("the rewrite model changed nothing on a known-positive control, so this scan measured nothing")
	}

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package directory: %v", err)
	}
	scanned, literals := 0, 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		fset := token.NewFileSet()
		f, perr := parser.ParseFile(fset, e.Name(), nil, 0)
		if perr != nil {
			t.Fatalf("parse %s: %v", e.Name(), perr)
		}
		scanned++
		// The exact structural test: an import path is a BasicLit at an
		// ImportSpec's own position. Everything else is a plain literal.
		imports := map[token.Pos]bool{}
		for _, spec := range f.Imports {
			imports[spec.Path.Pos()] = true
		}
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING || imports[lit.Pos()] {
				return true
			}
			val, uerr := strconv.Unquote(lit.Value)
			if uerr != nil {
				return true
			}
			literals++
			if got := MapInternalImport(val); got != val {
				t.Errorf("%s:%d holds a module path the sync script would rewrite; assemble it from the split constants instead\n  before: %s\n  after:  %s",
					e.Name(), fset.Position(lit.Pos()).Line, val, got)
			}
			return true
		})
	}
	if scanned == 0 {
		t.Fatal("no Go files were scanned, so this test measured nothing")
	}
	if literals == 0 {
		t.Fatal("no non-import string literal was examined, so this scan measured nothing")
	}
	t.Logf("sync-rewrite scan clean across %d Go files and %d non-import literals", scanned, literals)
}
