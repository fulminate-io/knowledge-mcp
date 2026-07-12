// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector"
	"github.com/fulminate-io/knowledge-mcp/internal/embed"
	"github.com/fulminate-io/knowledge-mcp/internal/hivemonitor"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// repoTestDeps satisfies ClientDeps for the InjectRepoIfCodeGraph tests.
// rootDir is the cwd shown to the intercept; gcCount counts GraphCaller
// invocations so a test can assert "the intercept did NOT forward the call".
// There is NO resolver field: repo identity is explicit and never inferred from
// cwd, so the intercept has no cwd→graph-name resolver to consult.
type repoTestDeps struct {
	rootDir string
	gcCount *int32
	gc      GraphCaller
}

func (d *repoTestDeps) LocalLiveness() LocalLiveness         { return nil }
func (d *repoTestDeps) Sink() collector.Sink                 { return nil }
func (d *repoTestDeps) RootDir() string                      { return d.rootDir }
func (d *repoTestDeps) UsageAnalyzer() UsageAnalyzerAPI      { return nil }
func (d *repoTestDeps) WorkerRuntime() WorkerRuntimeAPI      { return nil }
func (d *repoTestDeps) WorkerReady() bool                    { return true }
func (d *repoTestDeps) PropReady() bool                      { return true }
func (d *repoTestDeps) PipelineReady() bool                  { return true }
func (d *repoTestDeps) ClaimRegistry() *hivemonitor.Registry { return nil }
func (d *repoTestDeps) BanSet() *hivemonitor.BanSet          { return nil }
func (d *repoTestDeps) WorkerCRUD() WorkerCRUDAPI            { return nil }
func (d *repoTestDeps) GraphTypeCRUD() GraphTypeCRUDAPI      { return nil }
func (d *repoTestDeps) Embedder() embed.BinaryEmbedder       { return nil }
func (d *repoTestDeps) BackendResolver() BackendResolver     { return nil }
func (d *repoTestDeps) GraphCaller() GraphCaller {
	if d.gcCount != nil {
		atomic.AddInt32(d.gcCount, 1)
	}
	return d.gc
}
func (d *repoTestDeps) LocalGraphCaller() GraphCaller                { return d.gc }
func (d *repoTestDeps) SegmentManager() SegmentSearcher              { return nil }
func (d *repoTestDeps) SegmentVectorResolver() SegmentVectorResolver { return nil }
func (d *repoTestDeps) SegmentShipper() SegmentShipper               { return nil }
func (d *repoTestDeps) SegmentPruner() SegmentPruner                 { return nil }
func (d *repoTestDeps) SegmentCoverage() SegmentCoverageReader       { return nil }
func (d *repoTestDeps) PipelineScanner() PipelineScanner             { return nil }
func (d *repoTestDeps) ReflectionForcer() ReflectionForcer           { return nil }
func (d *repoTestDeps) SimilarityForcer() SimilarityForcer           { return nil }

func (d *repoTestDeps) BlindSpotProvider() BlindSpotProvider { return nil }
func (d *repoTestDeps) ClusterProvider() ClusterProvider     { return nil }
func (d *repoTestDeps) TensionsProvider() TensionsProvider   { return nil }

// hermeticGitEnv returns os.Environ() with every GIT_* entry stripped, then
// re-adds GIT_TERMINAL_PROMPT=0. Test fixtures that spawn git subprocesses MUST
// use this instead of raw os.Environ(): inside a worktree or a git hook, git
// exports GIT_DIR / GIT_INDEX_FILE / GIT_WORK_TREE / etc. into child processes,
// and those override `git -C <dir>`, so a fixture's `git init` would re-init the
// host worktree gitdir (flipping core.bare=true) and its commits would land on
// the host branch. Scrubbing GIT_* makes the fixture operate only in its own
// temp dir regardless of the ambient env. Intentionally duplicated
// from the coderun package: the no-shared-packages-outside-gen-proto invariant
// (AGENTS.md) forbids a hand-written shared test-helper package between these
// two internal packages.
func hermeticGitEnv() []string {
	base := os.Environ()
	out := make([]string, 0, len(base)+1)
	for _, kv := range base {
		if strings.HasPrefix(kv, "GIT_") {
			continue
		}
		out = append(out, kv)
	}
	return append(out, "GIT_TERMINAL_PROMPT=0")
}

// gitRepoFixture creates a temp directory, runs `git init`, and writes a
// single committed file so HEAD resolves. Returns the directory.
func gitRepoFixture(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "--initial-branch=main"},
		{"config", "user.email", "test@example.invalid"},
		{"config", "user.name", "test"},
		{"config", "commit.gpgsign", "false"},
	} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = hermeticGitEnv()
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello"), 0o600))
	for _, args := range [][]string{
		{"add", "a.txt"},
		{"commit", "-m", "initial"},
	} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = hermeticGitEnv()
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return dir
}

// callArgs decodes a CallToolParams.Arguments into a per-key map for
// post-condition assertions.
func callArgs(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	out := map[string]any{}
	require.NoError(t, json.Unmarshal(raw, &out))
	return out
}

func paramsFor(name string, body string) kgtools.CallToolParams {
	return kgtools.CallToolParams{Name: name, Arguments: json.RawMessage(body)}
}

func TestInjectRepoIfCodeGraph_QueryKnowledgeGraph_NotInjected(t *testing.T) {
	// query with graph=knowledge must NOT be touched — knowledge graph calls
	// don't carry repo:, and the code-graph gate passes them straight through.
	deps := &repoTestDeps{rootDir: t.TempDir()}
	out, handled, _ := InjectRepoIfCodeGraph(context.Background(), deps,
		paramsFor("query", `{"graph":"knowledge","id":"some-id"}`))
	assert.False(t, handled)
	_, has := callArgs(t, out.Arguments)["repo"]
	assert.False(t, has, "knowledge-graph query must not gain repo:")
}

func TestInjectRepoIfCodeGraph_SearchKnowledgeGraph_NotInjected(t *testing.T) {
	// search with graph=knowledge must pass through untouched.
	deps := &repoTestDeps{rootDir: t.TempDir()}
	out, handled, _ := InjectRepoIfCodeGraph(context.Background(), deps,
		paramsFor("search", `{"graph":"knowledge","query":"summarizer pipeline"}`))
	assert.False(t, handled)
	got := callArgs(t, out.Arguments)
	_, hasRepo := got["repo"]
	_, hasBranch := got["branch"]
	assert.False(t, hasRepo, "knowledge-graph search must not gain repo:")
	assert.False(t, hasBranch, "knowledge-graph search must not gain branch:")
}

func TestInjectRepoIfCodeGraph_QueryTraverse_OmittedGraph_KnowledgeDefault(t *testing.T) {
	// query and traverse DEFAULT to the knowledge graph: an OMITTED graph must
	// pass through untouched (no repo required), NOT be treated as a code query.
	// Pins the fix for the confusing "graph=code requires repo" on a plain
	// query(type:"ticket"). (Explicit graph="code" still requires repo — covered
	// by TestInjectRepoIfCodeGraph_CodeGraph_NoRepo_ErrorsBeforeRPC.)
	deps := &repoTestDeps{rootDir: t.TempDir()}
	for _, tc := range []struct{ name, body string }{
		{"query", `{"type":"ticket"}`},
		{"traverse", `{"start":"some-node"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, handled, _ := InjectRepoIfCodeGraph(context.Background(), deps, paramsFor(tc.name, tc.body))
			assert.False(t, handled, "omitted-graph %s must pass through to the knowledge graph", tc.name)
			_, hasRepo := callArgs(t, out.Arguments)["repo"]
			assert.False(t, hasRepo, "omitted-graph %s must not gain repo:", tc.name)
		})
	}
}

func TestInjectRepoIfCodeGraph_CodeGraph_NoRepo_ErrorsBeforeRPC(t *testing.T) {
	// Every code-graph tool with graph=code (or unset) and NO explicit repo:
	// must fail loud client-side WITHOUT invoking GraphCaller — repo is required;
	// it is never inferred from cwd. Run from inside a real git repo to prove the
	// error is about the missing repo, not anything cwd-derived.
	dir := gitRepoFixture(t)
	for _, tc := range []struct {
		name string
		body string
	}{
		{"query", `{"graph":"code","text":"x"}`},
		{"search", `{"graph":"code","query":"x"}`},
		{"traverse", `{"graph":"code","start":"a.go:F"}`},
		{"file_symbols", `{"file_path":"foo.go"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var gcCount int32
			deps := &repoTestDeps{rootDir: dir, gcCount: &gcCount}
			_, handled, res := InjectRepoIfCodeGraph(context.Background(), deps, paramsFor(tc.name, tc.body))
			assert.True(t, handled, "missing repo must short-circuit")
			assert.True(t, res.IsError)
			assert.Contains(t, res.Content[0].Text, `graph="code" requires repo`)
			assert.Equal(t, int32(0), atomic.LoadInt32(&gcCount), "GraphCaller must not be invoked on missing-repo error")
		})
	}
}

func TestInjectRepoIfCodeGraph_ExplicitRepo_NotInManifest_NoBranchStamp(t *testing.T) {
	// An explicit repo: passes through untouched, and branch is NOT stamped when
	// the repo is absent from the machine-local manifest — even when the cwd IS a
	// git repo whose basename equals the target. Branch is NEVER inferred from cwd;
	// the only auto-detect source is a manifest hit (see the manifest-hit test
	// below). An omitted branch with no manifest entry reads the base graph.
	withTestManifest(t) // empty manifest: repoName not recorded
	dir := gitRepoFixture(t)
	repoName := filepath.Base(dir)
	deps := &repoTestDeps{rootDir: dir}
	out, handled, _ := InjectRepoIfCodeGraph(context.Background(), deps,
		paramsFor("search", `{"query":"x","repo":"`+repoName+`"}`))
	assert.False(t, handled)
	got := callArgs(t, out.Arguments)
	assert.Equal(t, repoName, got["repo"], "explicit repo must be preserved verbatim")
	_, hasBranch := got["branch"]
	assert.False(t, hasBranch, "branch must not be stamped when the repo is not in the manifest")
}

func TestInjectRepoIfCodeGraph_RepoInManifest_StampsBranch(t *testing.T) {
	// When the repo is recorded in the machine-local manifest, an omitted branch
	// is auto-detected by running git in the repo's recorded on-disk directory —
	// stamping the branch the searched repo is ACTUALLY on (here "main"). This is
	// machine-correct and works cross-repo: the dir comes from the manifest, not cwd.
	m := withTestManifest(t)
	dir := gitRepoFixture(t) // a real git repo checked out on "main"
	repoName := filepath.Base(dir)
	require.NoError(t, m.Record(repoName, dir))
	// Session cwd is a DIFFERENT dir to prove branch is read from the manifest dir,
	// not from cwd.
	deps := &repoTestDeps{rootDir: t.TempDir()}
	out, handled, _ := InjectRepoIfCodeGraph(context.Background(), deps,
		paramsFor("search", `{"query":"x","repo":"`+repoName+`"}`))
	assert.False(t, handled)
	got := callArgs(t, out.Arguments)
	assert.Equal(t, "main", got["branch"], "branch must be auto-detected from the manifest-recorded dir")
}

func TestInjectRepoIfCodeGraph_ExplicitBranch_NotOverwritten(t *testing.T) {
	// A caller-supplied branch is preserved verbatim even when the repo is in the
	// manifest — auto-detect only fills a MISSING branch.
	m := withTestManifest(t)
	dir := gitRepoFixture(t)
	repoName := filepath.Base(dir)
	require.NoError(t, m.Record(repoName, dir))
	deps := &repoTestDeps{rootDir: t.TempDir()}
	out, handled, _ := InjectRepoIfCodeGraph(context.Background(), deps,
		paramsFor("search", `{"query":"x","repo":"`+repoName+`","branch":"feature-x"}`))
	assert.False(t, handled)
	got := callArgs(t, out.Arguments)
	assert.Equal(t, "feature-x", got["branch"], "an explicit branch must never be overwritten by auto-detect")
}

func TestInjectRepoIfCodeGraph_RepoAll_NoBranchStamp(t *testing.T) {
	// repo="all" is a cross-repo fan-out — there is no single checkout to detect a
	// branch from, so branch is never stamped.
	m := withTestManifest(t)
	dir := gitRepoFixture(t)
	require.NoError(t, m.Record("all", dir)) // even a stray "all" entry must not stamp
	deps := &repoTestDeps{rootDir: t.TempDir()}
	out, handled, _ := InjectRepoIfCodeGraph(context.Background(), deps,
		paramsFor("search", `{"query":"x","repo":"all"}`))
	assert.False(t, handled)
	got := callArgs(t, out.Arguments)
	_, hasBranch := got["branch"]
	assert.False(t, hasBranch, `repo="all" must never be branch-stamped`)
}

func TestInjectRepoIfCodeGraph_Search_StalenessTrue_PopulatesGitFields(t *testing.T) {
	// search with an explicit repo: + staleness:true MUST populate current_head
	// + uncommitted_count from git subprocess calls against the session cwd.
	dir := gitRepoFixture(t)
	repoName := filepath.Base(dir)
	deps := &repoTestDeps{rootDir: dir}
	out, handled, _ := InjectRepoIfCodeGraph(context.Background(), deps,
		paramsFor("search", `{"query":"x","repo":"`+repoName+`","staleness":true}`))
	assert.False(t, handled)
	got := callArgs(t, out.Arguments)
	headSHA, _ := got["current_head"].(string)
	assert.Len(t, headSHA, 40)
	uncommitted, _ := got["uncommitted_count"].(float64)
	assert.InDelta(t, float64(0), uncommitted, 0.0001)
}

func TestInjectRepoIfCodeGraph_Search_StalenessFalse_SkipsGitSubprocess(t *testing.T) {
	// search with staleness:false must NOT shell out to git for the staleness
	// trio: no current_head / uncommitted_count, and it completes fast.
	dir := gitRepoFixture(t)
	repoName := filepath.Base(dir)
	deps := &repoTestDeps{rootDir: dir}

	start := time.Now()
	out, handled, _ := InjectRepoIfCodeGraph(context.Background(), deps,
		paramsFor("search", `{"query":"x","repo":"`+repoName+`","staleness":false}`))
	elapsed := time.Since(start)

	assert.False(t, handled)
	got := callArgs(t, out.Arguments)
	_, hasHead := got["current_head"]
	_, hasUnc := got["uncommitted_count"]
	assert.False(t, hasHead, "current_head must not be set when staleness:false")
	assert.False(t, hasUnc, "uncommitted_count must not be set when staleness:false")
	assert.Less(t, elapsed, 500*time.Millisecond, "staleness:false must not pay for git subprocesses")
}

func TestInjectManageRepo_CodeOp_RequiresName(t *testing.T) {
	// A code-graph manage op (list_branches) with NO name: must fail loud —
	// the repo (carried in name:) is required and never inferred from cwd.
	deps := &repoTestDeps{rootDir: gitRepoFixture(t)}
	_, handled, res := InjectRepoIfCodeGraph(context.Background(), deps,
		paramsFor("manage", `{"operation":"list_branches"}`))
	assert.True(t, handled)
	assert.True(t, res.IsError)
	assert.Contains(t, res.Content[0].Text, "repo is required")
}

func TestInjectManageRepo_CodeOp_ExplicitName_PassesThrough(t *testing.T) {
	// list_branches WITH name: passes through unchanged.
	deps := &repoTestDeps{rootDir: t.TempDir()}
	out, handled, _ := InjectRepoIfCodeGraph(context.Background(), deps,
		paramsFor("manage", `{"operation":"list_branches","name":"knowledge"}`))
	assert.False(t, handled)
	assert.Equal(t, "knowledge", callArgs(t, out.Arguments)["name"])
}

func TestInjectRepoIfCodeGraph_NonCodeGraphTool_PassesThrough(t *testing.T) {
	// Tools outside the codeGraphToolNames allowlist (and non-code manage ops)
	// must pass through unchanged (no decode, no error).
	deps := &repoTestDeps{rootDir: t.TempDir()}
	out, handled, _ := InjectRepoIfCodeGraph(context.Background(), deps,
		paramsFor("manage", `{"operation":"status"}`))
	assert.False(t, handled)
	assert.JSONEq(t, `{"operation":"status"}`, string(out.Arguments))
}
