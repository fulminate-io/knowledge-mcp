// SPDX-License-Identifier: Apache-2.0

package tools

// intercept_topology_reporoot_test.go pins the WALK ROOT the topology dispatcher
// hands an analyzer that reads files off disk.
//
// THE DEFECT HAS TWO OBSERVABLE SIGNATURES AND THE SECOND ONE IS SILENT, which is
// why this file carries two tests rather than one. Handing an analyzer the daemon
// root instead of the named repo either ERRORS (the walk lands somewhere the
// process may not read) or returns a CLEAN EMPTY RESULT (the walk lands somewhere
// the requested subtree does not exist). A repair verified only against the loud
// signature looks complete while leaving the worse half in place, so the second
// test asserts the analyzer and the ast engine agree over all three shapes the
// repo argument can take.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/ast"
	"github.com/fulminate-io/knowledge-mcp/internal/corpus"
	"github.com/fulminate-io/knowledge-mcp/internal/enginetest"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/corpusscan"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

// reporootProbeName is the analyzer name this file's probe registers under. It
// is its own probe rather than the arm-parity fixture's because the walk-root
// allowlist has to name it, and adding the parity probe to that allowlist would
// change what the parity harness measures.
const reporootProbeName = "reporoot_probe"

// reporootProbeAnalyzer captures the foundation.Request it is handed. The
// dispatcher's honoring maps are vars rather than const maps precisely so a test
// can register its own probe without a production file ever naming a test symbol
// (intercept_topology.go says so at the path_prefix map).
type reporootProbeAnalyzer struct{}

func (reporootProbeAnalyzer) Name() string { return reporootProbeName }

// reporootLastRequest records the Request the probe was last handed. RepoRoot
// reaches no render and no error message, so a dispatch test has nowhere else to
// read it from. Written and read only from tests in this package, which run
// sequentially (no test here calls t.Parallel).
var reporootLastRequest foundation.Request

func (reporootProbeAnalyzer) Run(_ context.Context, req foundation.Request) ([]foundation.Finding, error) {
	reporootLastRequest = req
	return []foundation.Finding{{
		Algorithm: reporootProbeName,
		Title:     "reporoot probe finding",
		Summary:   "the walk-root dispatch test's registered analyzer",
	}}, nil
}

func init() { foundation.Register(reporootProbeAnalyzer{}) }

// The walk-root allowlist seam, test side. The PRODUCTION map names only the two
// analyzers that genuinely read the tree, and it must never name a test symbol —
// but the resolve is CONDITIONAL on membership, so a probe outside the map is
// handed the daemon root by design and could never observe the resolution. Adding
// the probe here is what makes the dispatch drivable, exactly as the path_prefix
// map's own test-side entry does.
func init() { repoRootRequiringAnalyzers[reporootProbeName] = true }

// TestRunLocalTopology_RepoRootResolvesTheNamedRepo asserts the dispatcher hands
// a repo-reading analyzer the tree the repo argument names, not the daemon's
// --root.
//
// THE SECOND ARM IS THE DISCRIMINATING ONE. Resolving the walk root
// unconditionally would satisfy the first arm and break every knowledge/cloud
// analyzer, because resolveRepoDir has a fail-loud floor and those calls carry no
// repo at all — so a knowledge analyzer with no repo must still dispatch, keeping
// the daemon root.
func TestRunLocalTopology_RepoRootResolvesTheNamedRepo(t *testing.T) {
	m := withTestManifest(t)
	target := t.TempDir()
	require.NoError(t, m.Record("reporoot-target", target))
	// The daemon root is deliberately a DIFFERENT directory: if the dispatcher
	// keeps handing analyzers deps.RootDir(), the assertion below reads this
	// path rather than the recorded one.
	daemonRoot := t.TempDir()

	t.Run("a repo-reading analyzer walks the named repo", func(t *testing.T) {
		reporootLastRequest = foundation.Request{}
		deps := &repoTestDeps{rootDir: daemonRoot, gc: &fakeGraphCaller{}}
		handled, res := InterceptTopology(context.Background(), deps,
			kgtools.CallToolParams{Name: "query", Arguments: json.RawMessage(
				`{"mode":"topology","algorithm":"` + reporootProbeName + `","graph":"code","repo":"reporoot-target"}`)})
		require.True(t, handled)
		require.False(t, res.IsError, "the probe must be served, not refused: %s", res.Content[0].Text)
		assert.Equal(t, target, reporootLastRequest.RepoRoot,
			"the walk root must be the directory the repo argument resolves to, not the daemon --root")
		assert.NotEqual(t, daemonRoot, reporootLastRequest.RepoRoot,
			"handing the analyzer the daemon root is the defect this test exists for")
	})

	t.Run("a knowledge analyzer with no repo still dispatches", func(t *testing.T) {
		qpLastTopologyRequest = foundation.Request{}
		deps := &repoTestDeps{rootDir: daemonRoot, gc: &fakeGraphCaller{}}
		handled, res := InterceptTopology(context.Background(), deps,
			kgtools.CallToolParams{Name: "query", Arguments: json.RawMessage(
				`{"mode":"topology","algorithm":"` + qpTopologyAnalyzer + `","graph":"knowledge"}`)})
		require.True(t, handled)
		require.False(t, res.IsError,
			"an analyzer that reads no files carries no repo and must still dispatch: %s", res.Content[0].Text)
		assert.Equal(t, daemonRoot, qpLastTopologyRequest.RepoRoot,
			"an analyzer that does not declare it reads the tree keeps the daemon root")
	})
}

// TestCorpusScan_AgreesWithTheAstEngineOverThreeRepoShapes drives corpus_scan
// through the dispatcher over a real tree and requires its finding count to equal
// a direct ast.Count run with the SAME pattern and where-tree, read off the
// seeded check node rather than typed a second time.
//
// EQUALITY RATHER THAN A PINNED NUMBER: the count is tree-derived and moves with
// the corpus, so a literal would rot into a false red. Equality is also satisfied
// by 0 == 0 — which is exactly the vacuous state the defect produces — so the
// whole-root shape carries a control leg requiring the ast count to exceed zero.
//
// THE ABSOLUTE-PATH-PLUS-PREFIX SHAPE IS THE FALSIFYING ONE. It fails SILENTLY
// when the walk root is wrong: the requested subtree simply does not exist under
// the wrong root, discovery scans nothing, and the analyzer reports a clean zero.
func TestCorpusScan_AgreesWithTheAstEngineOverThreeRepoShapes(t *testing.T) {
	repoRoot := moduleRootUnderTest(t)
	m := withTestManifest(t)
	const bareName = "corpusscan-walkroot-target"
	require.NoError(t, m.Record(bareName, repoRoot))

	checkNode, badNode, goodNode := seedWalkRootCheck()
	gc := newChecksGraphFake(checkNode, badNode, goodNode)

	// The pattern and where-tree the ast side runs come from the SEEDED NODE,
	// through the contract's own parser. A second hand-typed copy would let the
	// two sides diverge silently, which is the one thing this equivalence cannot
	// afford.
	parsed, isCheck, err := corpus.ParseCheck(checkNode)
	require.NoError(t, err, "the seeded check must be admitted by the contract")
	require.True(t, isCheck, "the seeded node must parse as an executable check")

	// The daemon root is NOT the repo: every shape below has to reach the repo
	// through the repo argument rather than through this.
	daemonRoot := t.TempDir()

	for _, tc := range []struct {
		name    string
		repoArg string
		prefix  string
	}{
		{"repo as a bare name", bareName, ""},
		{"repo as an absolute path", repoRoot, ""},
		// MODULE-relative, paired with the module-root anchor above: this
		// package is internal/tools relative to its own module in both layouts.
		{"repo as an absolute path plus path_prefix", repoRoot, "internal/tools"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := map[string]any{
				"mode":      "topology",
				"algorithm": corpusscan.AnalyzerName,
				"graph":     string(kgtypes.GraphCode),
				"repo":      tc.repoArg,
				"language":  "go",
			}
			if tc.prefix != "" {
				args["path_prefix"] = tc.prefix
			}
			body, merr := json.Marshal(args)
			require.NoError(t, merr)

			deps := &repoTestDeps{rootDir: daemonRoot, gc: gc}
			handled, res := InterceptTopology(context.Background(), deps,
				kgtools.CallToolParams{Name: "query", Arguments: body})
			require.True(t, handled)
			require.False(t, res.IsError, "the scan must run rather than refuse: %s", res.Content[0].Text)

			var findings []foundation.Finding
			require.NoError(t, json.Unmarshal([]byte(res.Content[0].Text), &findings))
			sites := requireOnlySiteFindings(t, findings)

			tally := astCountForCheck(t, parsed, repoRoot, tc.prefix)
			assert.Equal(t, tally.Total, sites,
				"the analyzer and the ast engine must agree over the same root and prefix")
			if tc.prefix == "" {
				// THE CONTROL LEG. Without it the whole equivalence is satisfied
				// by 0 == 0, which is the defect's own signature rather than a
				// clean scan.
				assert.Positive(t, tally.Total,
					"the seeded check must still have live sites in this tree, else the equivalence is vacuous")
			}
		})
	}
}

// requireOnlySiteFindings asserts every finding is a flagged SITE rather than one
// of the analyzer's own disclosures, and returns the count.
//
// It matters because the equivalence compares a finding count against a match
// count: a refusal, a truncation notice or the llm_only disclosure would inflate
// the finding side and make the two agree — or disagree — for a reason that has
// nothing to do with the walk root.
func requireOnlySiteFindings(t *testing.T, findings []foundation.Finding) int {
	t.Helper()
	for _, f := range findings {
		require.NotContains(t, f.Title, corpusscan.RefusalPrefixUnvalidated,
			"the fixture gate refused the seeded check: %s", f.Summary)
		require.NotContains(t, f.Title, corpusscan.RefusalPrefixEnvironment,
			"the fixture could not be placed on disk: %s", f.Summary)
		require.NotContains(t, f.Title, corpusscan.TruncationPrefixCheck,
			"the per-check render ceiling fired, so the finding count is no longer the match count: %s", f.Summary)
		require.NotEqual(t, corpusscan.TruncationTitleRun, f.Title,
			"the run render ceiling fired, so the finding count is no longer the match count")
		require.NotEqual(t, corpusscan.DisclosureTitleLLMOnly, f.Title,
			"the seeded corpus holds no llm_only entry, so this disclosure must not appear")
		require.NotEmpty(t, f.Metadata[corpusscan.MetaKeyFile],
			"a site finding carries the file it flagged; a finding without one is a disclosure")
	}
	return len(findings)
}

// astCountForCheck runs the check's pattern and where-tree straight through the
// ast engine over the same root and prefix the analyzer was given, with the same
// scope the analyzer builds.
func astCountForCheck(t *testing.T, c corpus.Check, repoRoot, prefix string) ast.CountTally {
	t.Helper()
	pat, err := ast.Parse(c.Pattern)
	require.NoError(t, err)
	cp, err := ast.Compile(pat, c.Language, "")
	require.NoError(t, err)
	defer cp.Close()
	where, err := ast.ParseWhere(c.Where)
	require.NoError(t, err)

	var prefixes []string
	if prefix != "" {
		prefixes = []string{prefix}
	}
	tally, _, err := ast.Count(context.Background(), repoRoot, c.Language, cp, where, ast.Scope{
		PackagePrefixes: prefixes,
		IncludeTests:    false,
	})
	require.NoError(t, err)
	return tally
}

// moduleRootUnderTest walks up from the test's working directory to the MODULE
// root — the first directory above this package holding a go.mod.
//
// THE ANCHOR AND THE PREFIX ARE ONE SHAPE, and this is the pairing the ast
// package's own variant test records as the invariant: the module can live at a
// repo subpath or at a repo root, and MODULE-RELATIVE prefixes are the one
// spelling that names these packages in both layouts. An earlier form walked to
// the checkout root and paired it with a repo-relative prefix; that pair names a
// real tree in this repository and NOTHING in the published mirror, where the
// module root IS the checkout root and cmd/knowledge does not exist — so the
// prefix arm below scanned no file there and the analyzer refused the run.
func moduleRootUnderTest(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	require.NoError(t, err)
	for {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		require.NotEqual(t, dir, parent, "walked past the filesystem root without finding a module root")
		dir = parent
	}
}

// The seeded check's node ids. They are fixture-local names rather than real
// corpus ids: no knowledge-graph node id belongs in a shipped Go file.
const (
	walkRootCheckID   = "walkroot-check-bucketcount-arg-is-an-identifier"
	walkRootBadFixID  = "walkroot-fixture-bad"
	walkRootGoodFixID = "walkroot-fixture-good"
)

// seedWalkRootCheck builds the check node and its two fixtures.
//
// THE FIXTURES ARE REAL, not decoration: the analyzer re-runs the admission gate
// on every scan, so the bad example must FIRE, the good one must be SILENT, and
// the good one must fire again once the where-tree is dropped — otherwise the
// scan below never executes the check and the equivalence measures a refusal.
// The good example is silenced by its where-tree alone (its argument is a
// selector expression rather than a bare identifier), which is what makes the
// calibration probe pass.
func seedWalkRootCheck() (check, bad, good *knowledgev1.Node) {
	check = &knowledgev1.Node{
		Id:          walkRootCheckID,
		Type:        string(kgtypes.NodeFinding),
		SymbolName:  "bucket-count-from-a-bare-identifier-argument",
		Description: "a walk-root fixture check: the partition count is computed from a bare identifier argument",
		Metadata: map[string]string{
			corpus.MetaCheckType:   string(corpus.CheckAstPattern),
			corpus.MetaSeverity:    string(foundation.SeverityWarning),
			corpus.MetaLanguage:    "go",
			corpus.MetaDSLPattern:  "searchengine.BucketCountFor(len($X))",
			corpus.MetaCheckWhere:  `{"kind": {"of": "X", "is": "identifier"}}`,
			corpus.MetaFixtureBad:  walkRootBadFixID,
			corpus.MetaFixtureGood: walkRootGoodFixID,
		},
	}
	bad = &knowledgev1.Node{
		Id:      walkRootBadFixID,
		Type:    string(kgtypes.NodeExample),
		Content: "package fixture\n\nfunc bucketsForWindow(items []string) int {\n\treturn searchengine.BucketCountFor(len(items))\n}\n",
		Metadata: map[string]string{
			corpus.MetaLanguage: "go",
		},
	}
	good = &knowledgev1.Node{
		Id:   walkRootGoodFixID,
		Type: string(kgtypes.NodeExample),
		Content: "package fixture\n\ntype corpusView struct{ docs []string }\n\n" +
			"func bucketsForCorpus(v corpusView) int {\n\treturn searchengine.BucketCountFor(len(v.docs))\n}\n",
		Metadata: map[string]string{
			corpus.MetaLanguage: "go",
		},
	}
	return check, bad, good
}

// checksGraphFake serves the ONE checks graph the corpus read addresses.
//
// It FILTERS by node type and by the metadata predicates the read carries. A
// lenient fake would be worse than none here: the corpus loader issues two
// whole-type reads against the same graph, so a fake that ignored the type would
// hand the check node back as a fixture and the fixture nodes back as checks,
// and the test would be measuring the fake.
type checksGraphFake struct{ nodes []*knowledgev1.Node }

func newChecksGraphFake(nodes ...*knowledgev1.Node) *checksGraphFake {
	return &checksGraphFake{nodes: nodes}
}

func (f *checksGraphFake) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	q := req.GetQuery()
	sel := q.GetSelection()
	out := make([]*knowledgev1.Node, 0, len(f.nodes))
	for _, n := range f.nodes {
		if t := sel.GetNodeType(); t != "" && n.GetType() != t {
			continue
		}
		if !checksFakeMetaMatch(n, sel.GetMetadataPredicates()) {
			continue
		}
		if cursor := q.GetAfterId(); cursor != "" && n.GetId() <= cursor {
			continue
		}
		out = append(out, n)
	}
	return enginetest.ResponseWithNodes(out...), nil
}

// checksFakeMetaMatch models the server's MetadataPredicate evaluation: OP_EXISTS
// tests key presence, anything else tests equality. Predicates are AND-ed.
func checksFakeMetaMatch(n *knowledgev1.Node, preds []*knowledgev1.MetadataPredicate) bool {
	for _, p := range preds {
		got, ok := n.GetMetadata()[p.GetKey()]
		if !ok {
			return false
		}
		if p.GetOp() == knowledgev1.MetadataPredicate_OP_EXISTS {
			continue
		}
		if got != p.GetValue() {
			return false
		}
	}
	return true
}
