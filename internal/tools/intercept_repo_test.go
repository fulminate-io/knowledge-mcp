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
	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
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

func (d *repoTestDeps) GraphClient() *graphclient.GraphClient { return nil }
func (d *repoTestDeps) Sink() collector.Sink                  { return nil }
func (d *repoTestDeps) RootDir() string                       { return d.rootDir }
func (d *repoTestDeps) WorkerRuntime() WorkerRuntimeAPI       { return nil }
func (d *repoTestDeps) WorkerCRUD() WorkerCRUDAPI             { return nil }
func (d *repoTestDeps) Embedder() embed.BinaryEmbedder        { return nil }
func (d *repoTestDeps) BackendResolver() BackendResolver      { return nil }
func (d *repoTestDeps) GraphCaller() GraphCaller {
	if d.gcCount != nil {
		atomic.AddInt32(d.gcCount, 1)
	}
	return d.gc
}
func (d *repoTestDeps) LocalGraphCaller() GraphCaller { return d.gc }
func (d *repoTestDeps) RepoResolver() *RepoResolver   { return d.resolver }

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
	assert.Contains(t, res.Content[0].Text, "repo is required")
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

func TestInjectRepoIfCodeGraph_MissingBranch_NonGitCwd_ErrorsBeforeRPC(t *testing.T) {
	// file_symbols WITH explicit repo: but missing branch: from a non-git
	// cwd must return a client-side "branch is required" error WITHOUT
	// invoking GraphCaller.
	var gcCount int32
	deps := &repoTestDeps{
		rootDir:  t.TempDir(), // no .git here
		resolver: buildResolver(t, "knowledge"),
		gcCount:  &gcCount,
	}
	_, handled, res := InjectRepoIfCodeGraph(context.Background(), deps,
		paramsFor("file_symbols", `{"file_path":"foo.go","repo":"knowledge"}`))
	assert.True(t, handled)
	assert.True(t, res.IsError)
	assert.Contains(t, res.Content[0].Text, "branch is required")
	assert.Equal(t, int32(0), atomic.LoadInt32(&gcCount), "GraphCaller must not be invoked on missing-branch error")
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
