// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector"
	"github.com/fulminate-io/knowledge-mcp/internal/embed"
	"github.com/fulminate-io/knowledge-mcp/internal/hivemonitor"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/session"
)

// repoTestDeps satisfies ClientDeps for the InjectRepoIfCodeGraph tests.
// The fields are wired to: rootDir (the cwd shown to the intercept),
// resolver (the cwd→graph-name resolver, optionally backed by
// listGraphsCaller), and gcCallCount (a counter so tests can assert
// "intercept did NOT forward the call").
type repoTestDeps struct {
	rootDir  string
	resolver *RepoResolver
	gcCount  *int32
	gc       GraphCaller
}

func (d *repoTestDeps) LocalLiveness() LocalLiveness         { return nil }
func (d *repoTestDeps) Sink() collector.Sink                 { return nil }
func (d *repoTestDeps) RootDir() string                      { return d.rootDir }
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
func (d *repoTestDeps) RepoResolver() *RepoResolver                  { return d.resolver }
func (d *repoTestDeps) SegmentManager() SegmentSearcher              { return nil }
func (d *repoTestDeps) SegmentVectorResolver() SegmentVectorResolver { return nil }
func (d *repoTestDeps) SegmentShipper() SegmentShipper               { return nil }
func (d *repoTestDeps) SegmentCoverage() SegmentCoverageReader       { return nil }
func (d *repoTestDeps) PipelineScanner() PipelineScanner             { return nil }
func (d *repoTestDeps) ReflectionForcer() ReflectionForcer           { return nil }
func (d *repoTestDeps) SimilarityForcer() SimilarityForcer           { return nil }

func (d *repoTestDeps) BlindSpotProvider() BlindSpotProvider { return nil }

// buildResolver returns a RepoResolver pre-loaded with the given graph
// names. listGraphsCaller backs the resolver; the first ResolveCwd call
// triggers the canned response.
func buildResolver(_ *testing.T, names ...string) *RepoResolver {
	gc := newListGraphsCaller(codeGraphs(names...))
	return NewRepoResolver(gc)
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
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
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
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
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
	// query with graph=knowledge must NOT be injected — knowledge graph
	// calls don't carry repo:.
	dir := gitRepoFixture(t)
	deps := &repoTestDeps{rootDir: dir, resolver: buildResolver(t, "knowledge")}
	out, handled, _ := InjectRepoIfCodeGraph(context.Background(), deps,
		paramsFor("query", `{"graph":"knowledge","id":"some-id"}`))
	assert.False(t, handled)
	got := callArgs(t, out.Arguments)
	_, has := got["repo"]
	assert.False(t, has, "knowledge-graph query should not gain repo:")
}

func TestInjectRepoIfCodeGraph_QueryCodeGraph_IsInjected(t *testing.T) {
	// query with graph=code (or unset) from a matching cwd MUST be
	// injected with repo: + branch:.
	dir := gitRepoFixture(t)
	repoName := filepath.Base(dir)
	deps := &repoTestDeps{rootDir: dir, resolver: buildResolver(t, repoName)}
	out, handled, _ := InjectRepoIfCodeGraph(context.Background(), deps,
		paramsFor("query", `{"graph":"code","text":"x"}`))
	assert.False(t, handled)
	got := callArgs(t, out.Arguments)
	assert.Equal(t, repoName, got["repo"])
	assert.Equal(t, "main", got["branch"])
}

func TestInjectRepoIfCodeGraph_SearchKnowledgeGraph_NotInjected(t *testing.T) {
	// search with graph=knowledge must NOT be injected — the knowledge
	// graph is the default memory/thought graph, not a code repo.
	dir := gitRepoFixture(t)
	deps := &repoTestDeps{rootDir: dir, resolver: buildResolver(t, "knowledge")}
	out, handled, _ := InjectRepoIfCodeGraph(context.Background(), deps,
		paramsFor("search", `{"graph":"knowledge","query":"summarizer pipeline"}`))
	assert.False(t, handled)
	got := callArgs(t, out.Arguments)
	_, hasRepo := got["repo"]
	_, hasBranch := got["branch"]
	assert.False(t, hasRepo, "knowledge-graph search should not gain repo:")
	assert.False(t, hasBranch, "knowledge-graph search should not gain branch:")
}

func TestInjectRepoIfCodeGraph_SearchCodeGraph_IsInjected(t *testing.T) {
	// search with graph=code from a matching cwd MUST be injected with
	// repo: + branch:, preserving the code-search shortcut behavior.
	dir := gitRepoFixture(t)
	repoName := filepath.Base(dir)
	deps := &repoTestDeps{rootDir: dir, resolver: buildResolver(t, repoName)}
	out, handled, _ := InjectRepoIfCodeGraph(context.Background(), deps,
		paramsFor("search", `{"graph":"code","query":"x"}`))
	assert.False(t, handled)
	got := callArgs(t, out.Arguments)
	assert.Equal(t, repoName, got["repo"])
	assert.Equal(t, "main", got["branch"])
}

func TestInjectRepoIfCodeGraph_FileSymbols_NonMatchingCwd_ErrorsBeforeRPC(t *testing.T) {
	// file_symbols without repo: from a non-matching cwd must produce a
	// client-side error AND NOT invoke GraphCaller.
	var gcCount int32
	deps := &repoTestDeps{
		rootDir:  t.TempDir(), // basename doesn't match resolver list
		resolver: buildResolver(t, "knowledge"),
		gcCount:  &gcCount,
	}
	_, handled, res := InjectRepoIfCodeGraph(context.Background(), deps,
		paramsFor("file_symbols", `{"file_path":"foo.go"}`))
	assert.True(t, handled)
	assert.True(t, res.IsError)
	assert.Contains(t, res.Content[0].Text, `graph="code" requires repo`)
	assert.Equal(t, int32(0), atomic.LoadInt32(&gcCount), "GraphCaller must not be invoked on missing-repo error")
}

func TestInjectRepoIfCodeGraph_Search_StalenessTrue_PopulatesGitFields(t *testing.T) {
	// search with staleness:true MUST populate current_head +
	// uncommitted_count + commits_behind from git subprocess calls.
	dir := gitRepoFixture(t)
	repoName := filepath.Base(dir)
	deps := &repoTestDeps{rootDir: dir, resolver: buildResolver(t, repoName)}
	out, handled, _ := InjectRepoIfCodeGraph(context.Background(), deps,
		paramsFor("search", `{"query":"x","staleness":true}`))
	assert.False(t, handled)
	got := callArgs(t, out.Arguments)
	headSHA, _ := got["current_head"].(string)
	assert.Len(t, headSHA, 40)
	uncommitted, _ := got["uncommitted_count"].(float64)
	assert.InDelta(t, float64(0), uncommitted, 0.0001)
	commitsBehind, _ := got["commits_behind"].(float64)
	assert.InDelta(t, float64(0), commitsBehind, 0.0001)
}

func TestInjectRepoIfCodeGraph_Search_StalenessFalse_SkipsGitSubprocess(t *testing.T) {
	// search with staleness:false must NOT shell out to git for the
	// staleness trio. Timing assertion: a non-staleness search call
	// against a t.TempDir() (no .git) must complete fast and NOT emit
	// the three staleness fields. Use a git repo fixture so DetectBranch
	// succeeds — only the staleness trio is gated.
	dir := gitRepoFixture(t)
	repoName := filepath.Base(dir)
	deps := &repoTestDeps{rootDir: dir, resolver: buildResolver(t, repoName)}

	start := time.Now()
	out, handled, _ := InjectRepoIfCodeGraph(context.Background(), deps,
		paramsFor("search", `{"query":"x","staleness":false}`))
	elapsed := time.Since(start)

	assert.False(t, handled)
	got := callArgs(t, out.Arguments)
	_, hasHead := got["current_head"]
	_, hasUnc := got["uncommitted_count"]
	_, hasBehind := got["commits_behind"]
	assert.False(t, hasHead, "current_head should not be set when staleness:false")
	assert.False(t, hasUnc, "uncommitted_count should not be set when staleness:false")
	assert.False(t, hasBehind, "commits_behind should not be set when staleness:false")
	// Timing budget: detect-branch + resolver-cache lookup. Three subprocess calls would push us over.
	assert.Less(t, elapsed, 500*time.Millisecond, "staleness:false should not pay for three additional git subprocesses")
}

func TestInjectRepoIfCodeGraph_ExplicitRepo_NotOverwritten(t *testing.T) {
	// When caller passes repo: explicitly, the resolver match must NOT
	// overwrite it.
	dir := gitRepoFixture(t)
	repoName := filepath.Base(dir)
	// Resolver knows about a DIFFERENT graph name; explicit "myrepo" wins.
	deps := &repoTestDeps{rootDir: dir, resolver: buildResolver(t, repoName, "myrepo")}
	out, handled, _ := InjectRepoIfCodeGraph(context.Background(), deps,
		paramsFor("search", `{"query":"x","repo":"myrepo"}`))
	assert.False(t, handled)
	got := callArgs(t, out.Arguments)
	assert.Equal(t, "myrepo", got["repo"], "explicit repo must not be overwritten")
}

func TestInjectRepoIfCodeGraph_CrossRepoExplicit_NoBranchStamp(t *testing.T) {
	// A cross-repo read — explicit repo:"agent" issued from a session whose
	// cwd resolves to repoA — must NOT stamp repoA's git branch onto the
	// agent target. The cwd's git HEAD is meaningless for a different repo,
	// so branch is left UNSET and the call falls through to the wire RPC
	// (handled==false) with repo:"agent" and no branch. The caller passes
	// branch: explicitly if it wants a cross-repo overlay.
	dir := gitRepoFixture(t)
	repoA := filepath.Base(dir)
	deps := &repoTestDeps{rootDir: dir, resolver: buildResolver(t, repoA, "agent")}
	out, handled, _ := InjectRepoIfCodeGraph(context.Background(), deps,
		paramsFor("search", `{"query":"x","repo":"agent"}`))
	assert.False(t, handled, "cross-repo read must not short-circuit")
	got := callArgs(t, out.Arguments)
	assert.Equal(t, "agent", got["repo"], "explicit cross-repo target preserved")
	_, hasBranch := got["branch"]
	assert.False(t, hasBranch, "cross-repo read must not stamp the cwd repo's branch")
}

func TestInjectRepoIfCodeGraph_SameRepoExplicit_StampsBranch(t *testing.T) {
	// A same-repo read — explicit repo: equal to the cwd's resolved repo —
	// still auto-fills branch via DetectBranch (the legitimate same-repo
	// overlay flow: knowledge session → knowledge@<branch>, real data).
	dir := gitRepoFixture(t)
	repoA := filepath.Base(dir)
	deps := &repoTestDeps{rootDir: dir, resolver: buildResolver(t, repoA)}
	out, handled, _ := InjectRepoIfCodeGraph(context.Background(), deps,
		paramsFor("search", `{"query":"x","repo":"`+repoA+`"}`))
	assert.False(t, handled)
	got := callArgs(t, out.Arguments)
	assert.Equal(t, repoA, got["repo"])
	assert.Equal(t, "main", got["branch"], "same-repo explicit read still stamps the cwd git branch")
}

func TestInjectRepoIfCodeGraph_CrossRepoUnresolvableCwd_FallsThroughNoError(t *testing.T) {
	// An explicit repo: that does not resolve from the cwd is a CROSS-REPO
	// read: the cwd basename (random t.TempDir()) matches no loaded graph,
	// so cwdRepo="" != the explicit target. Under the same-repo stamp
	// boundary this falls through to base WITHOUT a branch-required error —
	// branch is left unstamped and GraphCaller is reached normally
	// (handled==false). (Previously this scenario produced a branch-required
	// short-circuit; that contract moved to the same-repo non-git path.)
	var gcCount int32
	deps := &repoTestDeps{
		rootDir:  t.TempDir(), // basename matches no loaded graph → unresolvable cwd
		resolver: buildResolver(t, "knowledge"),
		gcCount:  &gcCount,
	}
	out, handled, _ := InjectRepoIfCodeGraph(context.Background(), deps,
		paramsFor("file_symbols", `{"file_path":"foo.go","repo":"knowledge"}`))
	assert.False(t, handled, "cross-repo unresolvable-cwd read must fall through, not error")
	got := callArgs(t, out.Arguments)
	_, hasBranch := got["branch"]
	assert.False(t, hasBranch, "cross-repo read must not stamp a branch")
	assert.Equal(t, int32(0), atomic.LoadInt32(&gcCount), "GraphCaller is invoked downstream, not inside the intercept")
}

func TestInjectRepoIfCodeGraph_SameRepoNonGitCwd_BranchRequired(t *testing.T) {
	// A SAME-REPO non-git cwd still errors with "branch is required": the
	// cwd basename equals the target repo name (resolver match → same-repo),
	// but the .git dir was removed so DetectBranch fails. The same-repo flow
	// needs a branch, so the client-side branch-required short-circuit fires
	// (handled==true) WITHOUT invoking GraphCaller. This preserves the
	// branch-required contract that the cross-repo re-characterization no
	// longer covers.
	dir := gitRepoFixture(t)
	repoName := filepath.Base(dir)
	require.NoError(t, os.RemoveAll(filepath.Join(dir, ".git"))) // basename match survives; DetectBranch now fails
	var gcCount int32
	deps := &repoTestDeps{
		rootDir:  dir,
		resolver: buildResolver(t, repoName),
		gcCount:  &gcCount,
	}
	_, handled, res := InjectRepoIfCodeGraph(context.Background(), deps,
		paramsFor("file_symbols", `{"file_path":"foo.go","repo":"`+repoName+`"}`))
	assert.True(t, handled)
	assert.True(t, res.IsError)
	assert.Contains(t, res.Content[0].Text, "branch is required")
	assert.Equal(t, int32(0), atomic.LoadInt32(&gcCount), "GraphCaller must not be invoked on missing-branch error")
}

func TestInjectRepoIfCodeGraph_PerSessionWorkspaceCwd_OverridesRootDir(t *testing.T) {
	// Two concurrent sessions from DIFFERENT repos: each carries its own
	// workspace cwd on ctx (session.ContextWithWorkspaceCwd). The resolver
	// knows both graph names. deps.RootDir() points at repoA, but the session
	// ctx for repoB MUST override it so repoB's query resolves to repoB — and
	// vice versa. This is the core multi-session repo-routing invariant.
	dirA := gitRepoFixture(t)
	dirB := gitRepoFixture(t)
	repoA := filepath.Base(dirA)
	repoB := filepath.Base(dirB)

	// RootDir = dirA (the stdio --root). The session ctx is what differentiates.
	deps := &repoTestDeps{rootDir: dirA, resolver: buildResolver(t, repoA, repoB)}

	ctxA := session.ContextWithWorkspaceCwd(context.Background(), dirA)
	ctxB := session.ContextWithWorkspaceCwd(context.Background(), dirB)

	outA, handledA, _ := InjectRepoIfCodeGraph(ctxA, deps, paramsFor("query", `{"graph":"code","text":"x"}`))
	assert.False(t, handledA)
	assert.Equal(t, repoA, callArgs(t, outA.Arguments)["repo"], "session A must resolve to repoA")

	outB, handledB, _ := InjectRepoIfCodeGraph(ctxB, deps, paramsFor("query", `{"graph":"code","text":"x"}`))
	assert.False(t, handledB)
	assert.Equal(t, repoB, callArgs(t, outB.Arguments)["repo"], "session B must resolve to repoB even though RootDir is repoA")
}

func TestInjectRepoIfCodeGraph_NoWorkspaceCwd_FallsBackToRootDir(t *testing.T) {
	// The stdio path carries no workspace cwd on ctx, so repo resolution must
	// fall back to deps.RootDir() exactly as before B. (Criterion: stdio path
	// unchanged.)
	dir := gitRepoFixture(t)
	repoName := filepath.Base(dir)
	deps := &repoTestDeps{rootDir: dir, resolver: buildResolver(t, repoName)}
	// context.Background() carries NO workspace cwd.
	out, handled, _ := InjectRepoIfCodeGraph(context.Background(), deps,
		paramsFor("query", `{"graph":"code","text":"x"}`))
	assert.False(t, handled)
	assert.Equal(t, repoName, callArgs(t, out.Arguments)["repo"], "no-cwd ctx must fall back to RootDir")
}

func TestInjectRepoIfCodeGraph_NonCodeGraphTool_PassesThrough(t *testing.T) {
	// Tools outside the codeGraphToolNames allowlist must pass through
	// unchanged (no decode, no error).
	deps := &repoTestDeps{rootDir: t.TempDir()}
	out, handled, _ := InjectRepoIfCodeGraph(context.Background(), deps,
		paramsFor("manage", `{"operation":"status"}`))
	assert.False(t, handled)
	assert.JSONEq(t, `{"operation":"status"}`, string(out.Arguments))
}
