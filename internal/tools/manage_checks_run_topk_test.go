// SPDX-License-Identifier: Apache-2.0

package tools

// manage_checks_run_topk_test.go pins the two properties a cap and a scope must
// have on the run operation: the caller's top_k bounds the RENDERED body and
// never the classification, and a scope the run could not honor is refused
// rather than folded into a verdict.
//
// BOTH TESTS DRIVE THE REAL MCP PATH through InterceptManageChecks over a seeded
// corpus and a seeded tree. A fold-order defect is INVISIBLE to a test that hands
// ClassifyRun a hand-built finding slice — the four verdict tests in the sibling
// file all do exactly that, and none of them could see this one, because the
// clipping happens between the analyzer and the classifier rather than inside
// either.
//
// A SEPARATE FILE RATHER THAN AN APPENDIX: manage_checks_run_test.go already sits
// against the repo's 300-line per-file warning threshold.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/corpus"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// checksRunLLMOnlyNode builds the corpus's accepted llm_only member.
//
// IT IS WHAT MAKES THE DEFECT'S PRECONDITION REAL. The llm_only disclosure is a
// LEAD finding, so it is the one that survives a top_k of 1 — and ClassifyRun
// counts it as neither a flagged site nor a refusal. That is the only reason a
// dirty corpus could ever have read CLEAN under a cap: the findings that would
// have flagged it were clipped away and the one that survived flags nothing.
//
// It carries exactly the llm_only marker and the language, and NO check body
// key: the contract refuses an llm_only node that also carries one.
func checksRunLLMOnlyNode() *knowledgev1.Node {
	return &knowledgev1.Node{
		Id: "go:llm-only-guidance", Type: string(kgtypes.NodeFinding),
		SymbolName:  "prefer a named constant over a magic number",
		Description: "prose guidance with no deterministic expression",
		Metadata: map[string]string{
			corpus.MetaLLMOnly:  "true",
			corpus.MetaLanguage: "go",
		},
	}
}

// driveChecksRunWithLLMOnly runs the operation against the shared two-check
// corpus PLUS the llm_only member, through caller-supplied deps.
//
// deps IS A PARAMETER rather than built here, which is the one way this differs
// from the sibling driveChecksRun: both tests below make several calls against
// ONE seeded corpus and ONE recorded manifest, and a helper that built fresh deps
// per call would silently give each call its own graph.
func driveChecksRunWithLLMOnly(t *testing.T, deps *repoTestDeps, args json.RawMessage) kgtools.ToolResult {
	t.Helper()
	handled, res := InterceptManageChecks(context.Background(), deps,
		kgtools.CallToolParams{Name: "manage_checks", Arguments: args})
	require.True(t, handled)
	return res
}

// checksRunSubtreeFixture writes a tree whose sites live UNDER A SUBDIRECTORY,
// which the sibling fixture deliberately does not: its sites.go sits at the root,
// so no prefix both resolves and narrows. Returns the root.
func checksRunSubtreeFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "go.mod"),
		[]byte("module example.com/subtreefixture\n\ngo 1.22\n"), 0o600))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "pkg"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(root, "pkg", "sites.go"), []byte(
		"package pkg\n\nfunc alpha() { fmt.Println(1) }\n\nfunc beta() { log.Println(2) }\n"), 0o600))
	return root
}

// TestManageChecks_RunCapBoundsTheRenderNotTheVerdict is the catcher for the
// defect this file exists for: a classification folded over a render-side slice.
//
// THE CORPUS IS DIRTY AND THE CAP IS SMALL. Three findings come back — the
// llm_only disclosure and one site for each of the two seeded checks — and a cap
// of one keeps only the disclosure. The verdict must still read FLAGGED with both
// sites counted, because it is folded over what the RUN produced, while the body
// carries one finding and says so.
func TestManageChecks_RunCapBoundsTheRenderNotTheVerdict(t *testing.T) {
	root := checksRunFixtureTree(t)
	m := withTestManifest(t)
	require.NoError(t, m.Record("runfixture", root))
	empty := t.TempDir()
	require.NoError(t, m.Record("emptyfixture", empty))
	deps := &repoTestDeps{rootDir: t.TempDir(),
		gc: newChecksGraphFake(append(checksRunCorpus(), checksRunLLMOnlyNode())...)}

	// (a) UNCAPPED CONTROL. Without it a capped FLAGGED says nothing: the counts
	// below have to be shown to be the ones an uncapped run reports.
	whole := driveChecksRunWithLLMOnly(t, deps, runChecksArgs(t, "runfixture", nil))
	require.False(t, whole.IsError, "the uncapped run must succeed: %s", whole.Content[0].Text)
	assert.Contains(t, whole.Content[0].Text, VerdictFlagged)
	assert.Contains(t, whole.Content[0].Text, "sites_flagged=2")
	assert.Contains(t, whole.Content[0].Text, "llm_only_not_executed=1",
		"the llm_only member must reach the run, or the cap below clips a different finding than this test means")
	assert.NotContains(t, whole.Content[0].Text, "returning ",
		"an uncapped run withholds nothing and must print no count line")

	// (b) THE DEFECT'S OWN SHAPE: dirty corpus, cap of one.
	capped := driveChecksRunWithLLMOnly(t, deps,
		runChecksArgs(t, "runfixture", map[string]any{"top_k": 1}))
	require.False(t, capped.IsError, "the capped run must succeed: %s", capped.Content[0].Text)
	body := capped.Content[0].Text
	assert.Contains(t, body, VerdictFlagged,
		"the verdict folds over every finding the run produced, so a render cap cannot make a dirty corpus read clean")
	assert.Contains(t, body, "sites_flagged=2",
		"both flagged sites must be counted even though only one finding is rendered")
	assert.Contains(t, body, "truncated=true",
		"the body IS incomplete, and truncated says so — computed from rendered vs total, not inferred from a notice")
	assert.Contains(t, body, "returning 1 of 3 findings",
		"a clipped render must state what it rendered and what the run produced")
	assert.NotContains(t, body, "no-fmt-println",
		"the clip must actually have happened — a flagged check's site surviving a cap of one means nothing was withheld")

	// (c) THE ADMITTED CAP RANGE. Zero is no cap, and a cap at or above the finding
	// count withholds nothing; all three must be byte-equivalent to the uncapped run.
	for _, k := range []int{0, 3, 5} {
		res := driveChecksRunWithLLMOnly(t, deps,
			runChecksArgs(t, "runfixture", map[string]any{"top_k": k}))
		require.Falsef(t, res.IsError, "top_k=%d must succeed: %s", k, res.Content[0].Text)
		assert.Containsf(t, res.Content[0].Text, VerdictFlagged, "top_k=%d", k)
		assert.Containsf(t, res.Content[0].Text, "sites_flagged=2", "top_k=%d", k)
		assert.Containsf(t, res.Content[0].Text, "truncated=false",
			"top_k=%d withholds nothing, so the body is complete", k)
		assert.NotContainsf(t, res.Content[0].Text, "returning ",
			"top_k=%d withholds nothing and must print no count line", k)
	}

	// (d) A NEGATIVE CAP IS REFUSED, naming the value. Coercing it to no-cap would
	// make -1 a second spelling of 0, which is a value the caller did not write.
	neg := driveChecksRunWithLLMOnly(t, deps,
		runChecksArgs(t, "runfixture", map[string]any{"top_k": -1}))
	require.True(t, neg.IsError, "a negative top_k must be refused, never coerced to no-cap: %s", neg.Content[0].Text)
	assert.Contains(t, neg.Content[0].Text, "top_k=-1", "the refusal must name the offending value")
	assert.NotContains(t, neg.Content[0].Text, "sites_flagged=",
		"a refused call scanned nothing and must render no verdict line for it")

	// (e) A CLEAN TREE UNDER THE SAME CAP still reads CLEAN. Without it every
	// assertion above is satisfied by a tool that reports FLAGGED unconditionally.
	clean := driveChecksRunWithLLMOnly(t, deps,
		runChecksArgs(t, "emptyfixture", map[string]any{"top_k": 1}))
	require.False(t, clean.IsError, "a scan of an empty tree is a clean answer, not an error: %s", clean.Content[0].Text)
	assert.Contains(t, clean.Content[0].Text, VerdictClean)
	assert.Contains(t, clean.Content[0].Text, "sites_flagged=0")
	assert.Contains(t, clean.Content[0].Text, "truncated=false")
	assert.NotContains(t, clean.Content[0].Text, "returning ",
		"nothing was withheld here, so no count line may be printed")
}

// TestManageChecks_RunRefusesAScopeThatReachedNoFile pins the scope half.
//
// A path_prefix that reached NO FILE of the corpus language is refused naming the
// prefix, rather than rendering a verdict over a scan that never opened anything.
// The property is "reached no file", not "the directory is absent" — which is why
// the docs leg exists: a directory that is really there and holds only markdown
// produces exactly the same clean-looking zero as a typo.
func TestManageChecks_RunRefusesAScopeThatReachedNoFile(t *testing.T) {
	root := checksRunSubtreeFixture(t)
	m := withTestManifest(t)
	require.NoError(t, m.Record("subtreefixture", root))
	deps := &repoTestDeps{rootDir: t.TempDir(),
		gc: newChecksGraphFake(append(checksRunCorpus(), checksRunLLMOnlyNode())...)}

	// (f) A RESOLVING PREFIX STILL SCANS. Without it, leg (h) is satisfied by a
	// tool that refuses every prefix it is given.
	scoped := driveChecksRunWithLLMOnly(t, deps,
		runChecksArgs(t, "subtreefixture", map[string]any{"path_prefix": "pkg"}))
	require.False(t, scoped.IsError, "a prefix that resolves must scan: %s", scoped.Content[0].Text)
	assert.Contains(t, scoped.Content[0].Text, VerdictFlagged)
	assert.Contains(t, scoped.Content[0].Text, "sites_flagged=2")

	// (g) AN EMPTY PREFIX STAYS WHOLE-REPO. Without it, the refusal below is
	// satisfied by a tool that refuses every run.
	unscoped := driveChecksRunWithLLMOnly(t, deps, runChecksArgs(t, "subtreefixture", nil))
	require.False(t, unscoped.IsError, "no prefix means the whole repo and must scan: %s", unscoped.Content[0].Text)
	assert.Contains(t, unscoped.Content[0].Text, "sites_flagged=2")

	// (h) A PREFIX THAT REACHED NO FILE IS REFUSED, naming it.
	missing := driveChecksRunWithLLMOnly(t, deps,
		runChecksArgs(t, "subtreefixture", map[string]any{"path_prefix": "no-such-subtree"}))
	require.True(t, missing.IsError,
		"a prefix that reached no file must be refused, never reported CLEAN: %s", missing.Content[0].Text)
	assert.Contains(t, missing.Content[0].Text, "no-such-subtree", "the refusal must name the prefix")
	// THE VERDICT LINE'S OWN SIGNATURE, not the CLEAN token: the refusal's prose
	// contains the word clean, so an assertion on the token collides with correct
	// work. sites_flagged= is a shape no prose produces.
	assert.NotContains(t, missing.Content[0].Text, "sites_flagged=",
		"a refused scope must render no verdict line at all")

	// (i) A PREFIX THAT EXISTS ON DISK BUT HOLDS NO FILE OF THE LANGUAGE is refused
	// on the same terms — the dangerous case, because the directory is really there.
	require.NoError(t, os.MkdirAll(filepath.Join(root, "docs"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(root, "docs", "readme.md"), []byte("# docs\n"), 0o600))
	docs := driveChecksRunWithLLMOnly(t, deps,
		runChecksArgs(t, "subtreefixture", map[string]any{"path_prefix": "docs"}))
	require.True(t, docs.IsError,
		"a directory that exists and holds no go file reached no file, and must be refused: %s", docs.Content[0].Text)
	assert.Contains(t, docs.Content[0].Text, "docs", "the refusal must name the prefix")
	assert.NotContains(t, docs.Content[0].Text, "sites_flagged=",
		"a refused scope must render no verdict line at all")
}
