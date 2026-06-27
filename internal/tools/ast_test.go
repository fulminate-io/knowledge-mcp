// SPDX-License-Identifier: Apache-2.0

// ast_test.go — unit tests for the client-side ast intercept. Mirror the
// failure-mode pins from the prior server-side handlers (the explain
// cases a/b/c/d, the bypass-path tie-break, etc.) without
// mocking the GraphClient — the client-side intercept hydrates against
// ast.NoOpBackend so no wire calls fire and every test runs purely against
// the local filesystem.

package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/embed"
	"github.com/fulminate-io/knowledge-mcp/internal/hivemonitor"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector"
)

// astTestDeps is a minimal ClientDeps stub for the ast intercept. Only
// RootDir() is exercised by the ast handlers; GraphClient and Sink return
// nil because the ast tool never calls them (NoOpBackend hydration means
// zero wire traffic).
type astTestDeps struct {
	rootDir string
}

func (d astTestDeps) LocalLiveness() LocalLiveness                 { return nil }
func (d astTestDeps) Sink() collector.Sink                         { return nil }
func (d astTestDeps) RootDir() string                              { return d.rootDir }
func (d astTestDeps) WorkerRuntime() WorkerRuntimeAPI              { return nil }
func (d astTestDeps) WorkerReady() bool                            { return true }
func (d astTestDeps) PropReady() bool                              { return true }
func (d astTestDeps) PipelineReady() bool                          { return true }
func (d astTestDeps) ClaimRegistry() *hivemonitor.Registry         { return nil }
func (d astTestDeps) BanSet() *hivemonitor.BanSet                  { return nil }
func (d astTestDeps) WorkerCRUD() WorkerCRUDAPI                    { return nil }
func (d astTestDeps) GraphTypeCRUD() GraphTypeCRUDAPI              { return nil }
func (d astTestDeps) Embedder() embed.BinaryEmbedder               { return nil }
func (d astTestDeps) BackendResolver() BackendResolver             { return nil }
func (d astTestDeps) GraphCaller() GraphCaller                     { return nil }
func (d astTestDeps) LocalGraphCaller() GraphCaller                { return nil }
func (d astTestDeps) SegmentManager() SegmentSearcher              { return nil }
func (d astTestDeps) SegmentVectorResolver() SegmentVectorResolver { return nil }
func (d astTestDeps) SegmentShipper() SegmentShipper               { return nil }
func (d astTestDeps) SegmentPruner() SegmentPruner                 { return nil }
func (d astTestDeps) SegmentCoverage() SegmentCoverageReader       { return nil }
func (d astTestDeps) PipelineScanner() PipelineScanner             { return nil }
func (d astTestDeps) ReflectionForcer() ReflectionForcer           { return nil }
func (d astTestDeps) SimilarityForcer() SimilarityForcer           { return nil }

func (d astTestDeps) BlindSpotProvider() BlindSpotProvider { return nil }
func (d astTestDeps) ClusterProvider() ClusterProvider     { return nil }
func (d astTestDeps) TensionsProvider() TensionsProvider   { return nil }

// astFixtureRepo writes a single Go file with N function declarations under
// t.TempDir() and returns the directory. The fixture mirrors the prior
// server-side astFixture's shape so the bypass-path / repo-relative-key
// pins port over without behavior drift.
func astFixtureRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	const fixture = `package fix

func A() {}
func B() error { return nil }
func C(x int) int { return x }
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "fix.go"), []byte(fixture), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module fix\n\ngo 1.21\n"), 0o600))
	return dir
}

// callAst invokes InterceptAst with the given JSON args and returns the
// (body, isError, handled) tuple. Mirrors the server-side dispatchAst
// helper so callers read the same shape.
func callAst(t *testing.T, deps ClientDeps, argsJSON string) (string, bool, bool) {
	t.Helper()
	params := kgtools.CallToolParams{
		Name:      "ast",
		Arguments: json.RawMessage(argsJSON),
	}
	handled, res := InterceptAst(deps, params)
	if !handled {
		return "", false, false
	}
	require.NotEmpty(t, res.Content)
	return res.Content[0].Text, res.IsError, true
}

// TestInterceptAst_NameFiltering pins that InterceptAst returns
// (false, zero) for any tool other than "ast". Mirrors the gate at the top
// of every Intercept* function.
func TestInterceptAst_NameFiltering(t *testing.T) {
	deps := astTestDeps{rootDir: t.TempDir()}
	for _, name := range []string{"collect", "manage", "search", "query", ""} {
		params := kgtools.CallToolParams{Name: name, Arguments: json.RawMessage(`{}`)}
		handled, res := InterceptAst(deps, params)
		assert.False(t, handled, "tool %q must not be handled by InterceptAst", name)
		assert.Empty(t, res.Content, "non-ast call must return zero ToolResult")
	}
}

// TestInterceptAst_UnknownOperation pins that the handler
// surfaces "unknown operation" for any operation outside the 4-op set
// (match | count | explain | list_node_kinds).
func TestInterceptAst_UnknownOperation(t *testing.T) {
	deps := astTestDeps{rootDir: t.TempDir()}
	body, isErr, handled := callAst(t, deps, `{"operation":"unknown"}`)
	require.True(t, handled)
	require.True(t, isErr)
	assert.Contains(t, body, "unknown operation")
	assert.NotContains(t, body, "search", "search op was removed in B'.3")
}

// TestHandleAstExplain_FailureModes pins the failure-mode cases (a)/(b)/(c):
// empty snippet, unsupported language. Mirrors the prior server-side test.
func TestHandleAstExplain_FailureModes(t *testing.T) {
	deps := astTestDeps{rootDir: t.TempDir()}

	t.Run("empty_snippet", func(t *testing.T) {
		body, isErr, _ := callAst(t, deps, `{"operation":"explain","language":"go","snippet":""}`)
		require.True(t, isErr)
		assert.Contains(t, body, "snippet")
	})

	t.Run("unsupported_language", func(t *testing.T) {
		body, isErr, _ := callAst(t, deps, `{"operation":"explain","language":"klingon","snippet":"func F() {}"}`)
		require.True(t, isErr)
		assert.Contains(t, body, "unsupported language")
	})

	t.Run("empty_language_string_unsupported", func(t *testing.T) {
		// Empty language resolves to LangUnknown via LanguageGrammar lookup.
		body, isErr, _ := callAst(t, deps, `{"operation":"explain","language":"","snippet":"func F() {}"}`)
		require.True(t, isErr)
		assert.Contains(t, body, "unsupported language")
	})
}

// TestHandleAstExplain_GoSnippet pins that a Go snippet
// returns a non-empty indented node-kind tree containing 'function_declaration'.
func TestHandleAstExplain_GoSnippet(t *testing.T) {
	deps := astTestDeps{rootDir: t.TempDir()}
	body, isErr, _ := callAst(t, deps, `{"operation":"explain","language":"go","snippet":"package p\n\nfunc F() error {\n  return nil\n}\n"}`)
	require.False(t, isErr, "expected non-error result, got: %s", body)
	assert.Contains(t, body, "function_declaration")
	assert.Contains(t, body, "source_file")
}

// TestAstParser_BareDollarRejected pins that the DSL parser rejects bare
// '$' (errParserBareDollar). Authoring sanity: bare '$' is a malformed
// placeholder, not a literal token.
func TestAstParser_BareDollarRejected(t *testing.T) {
	repoDir := astFixtureRepo(t)
	deps := astTestDeps{rootDir: repoDir}
	body, isErr, _ := callAst(t, deps, `{"operation":"match","language":"go","pattern":"$"}`)
	require.True(t, isErr, "DSL parser must reject bare '$'")
	assert.Contains(t, body, "$")
}

// TestHandleAstListNodeKinds_Go sanity-checks list_node_kinds: Go grammar
// exposes 50+ kinds including function_declaration, method_declaration,
// call_expression.
func TestHandleAstListNodeKinds_Go(t *testing.T) {
	deps := astTestDeps{rootDir: t.TempDir()}
	body, isErr, _ := callAst(t, deps, `{"operation":"list_node_kinds","language":"go"}`)
	require.False(t, isErr, "list_node_kinds failed: %s", body)

	var out struct {
		Language  string   `json:"language"`
		NodeKinds []string `json:"node_kinds"`
		Source    string   `json:"source"`
		Count     int      `json:"count"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &out))
	assert.Equal(t, "go", out.Language)
	assert.Equal(t, "dynamic", out.Source)
	assert.Greater(t, out.Count, 50)
	assert.Contains(t, out.NodeKinds, "function_declaration")
	assert.Contains(t, out.NodeKinds, "method_declaration")
	assert.Contains(t, out.NodeKinds, "call_expression")
}

func TestHandleAstListNodeKinds_Unsupported(t *testing.T) {
	deps := astTestDeps{rootDir: t.TempDir()}
	body, isErr, _ := callAst(t, deps, `{"operation":"list_node_kinds","language":"klingon"}`)
	require.True(t, isErr)
	assert.Contains(t, body, "unsupported language")
}

// TestResolveRepoDir covers the cross-repo walk-root resolution: no-repo-arg and
// the current repo's OWN name walk the session cwd; a bare cross-repo NAME is
// NEVER guessed to a directory — it fails loud and points to an absolute path,
// because a repo name is not a portable filesystem path (the same name lives at
// a different location on every machine, and the graph stores no collect-time
// path to map it back); an absolute path walks that dir directly; an empty root
// preserves the existing --root-empty error. Resolution is purely FILESYSTEM-
// based (directory existence + a path match against the real base tree), NOT the
// code-graph catalog — so the fixtures create real temp dirs.
func TestResolveRepoDir(t *testing.T) {
	ctx := context.Background()

	t.Run("no_repo_walks_cwd", func(t *testing.T) {
		parent := t.TempDir()
		dirY := filepath.Join(parent, "knowledge")
		require.NoError(t, os.MkdirAll(dirY, 0o750))
		deps := astTestDeps{rootDir: dirY}
		got, err := resolveRepoDir(ctx, deps, "")
		require.NoError(t, err)
		assert.Equal(t, dirY, got)
	})

	t.Run("same_repo_walks_cwd", func(t *testing.T) {
		parent := t.TempDir()
		dirY := filepath.Join(parent, "knowledge")
		require.NoError(t, os.MkdirAll(dirY, 0o750))
		deps := astTestDeps{rootDir: dirY}
		got, err := resolveRepoDir(ctx, deps, "knowledge")
		require.NoError(t, err)
		assert.Equal(t, dirY, got)
	})

	t.Run("relative_root_is_anchored_to_abs", func(t *testing.T) {
		// REGRESSION: effectiveCwd can hand back a RELATIVE root — notably the
		// daemon's default --root of "." when no session WorkspaceCwd is
		// propagated over HTTP. The current-tree path match needs an absolute
		// base (filepath.Dir(".") is "." with no parent), so resolveRepoDir
		// anchors a relative base via filepath.Abs against the process cwd. Chdir
		// into a repo dir, root="." → resolves to that absolute dir, and the
		// repo's own name resolves the same anchored tree. Without the anchor,
		// "knowledge" here fails loud (the live daemon symptom this pins).
		parent := t.TempDir()
		dirY := filepath.Join(parent, "knowledge")
		require.NoError(t, os.MkdirAll(dirY, 0o750))
		t.Chdir(dirY) // process cwd = .../knowledge
		deps := astTestDeps{rootDir: "."}
		// t.TempDir() lives under a /var → /private/var symlink on macOS, and
		// os.Getwd resolves it, so compare via EvalSymlinks.
		wantY, err := filepath.EvalSymlinks(dirY)
		require.NoError(t, err)

		got, err := resolveRepoDir(ctx, deps, "")
		require.NoError(t, err)
		assert.True(t, filepath.IsAbs(got), "a relative root must be anchored to an absolute path, got %q", got)
		gotEval, err := filepath.EvalSymlinks(got)
		require.NoError(t, err)
		assert.Equal(t, wantY, gotEval)

		got2, err := resolveRepoDir(ctx, deps, "knowledge")
		require.NoError(t, err)
		got2Eval, err := filepath.EvalSymlinks(got2)
		require.NoError(t, err)
		assert.Equal(t, wantY, got2Eval, "the current repo's name must resolve the anchored current tree")
	})

	t.Run("cross_repo_name_does_not_guess_sibling", func(t *testing.T) {
		// A bare cross-repo NAME must NOT be resolved by guessing a sibling
		// directory: repo names are not portable filesystem paths. Even with a
		// real parent/agent sibling on disk, resolution fails loud and directs the
		// user to an absolute path — a name→dir guess is correct only by accident
		// of THIS machine's layout, so we never make it. The manifest is empty
		// here, so the sibling on disk is NOT picked up.
		withTestManifest(t) // empty manifest: no recorded "agent" entry
		parent := t.TempDir()
		dirY := filepath.Join(parent, "knowledge")
		dirX := filepath.Join(parent, "agent")
		require.NoError(t, os.MkdirAll(dirY, 0o750))
		require.NoError(t, os.MkdirAll(dirX, 0o750))
		deps := astTestDeps{rootDir: dirY}
		got, err := resolveRepoDir(ctx, deps, "agent")
		require.Error(t, err)
		assert.Empty(t, got)
		assert.NotEqual(t, dirX, got, "a cross-repo NAME must never resolve to a guessed sibling dir")
		assert.Contains(t, err.Error(), "absolute checkout path", "the error must point the user to an absolute path")
	})

	t.Run("cross_repo_no_checkout_errors", func(t *testing.T) {
		// LOAD-BEARING: parent/agent does NOT exist on disk. The fail-loud floor
		// MUST error rather than silently returning the cwd (knowledge) tree.
		// This pins the never-return-the-cwd-tree property — it fails if the
		// floor regresses.
		withTestManifest(t) // empty manifest
		parent := t.TempDir()
		dirY := filepath.Join(parent, "knowledge")
		require.NoError(t, os.MkdirAll(dirY, 0o750))
		// Deliberately do NOT create parent/agent.
		deps := astTestDeps{rootDir: dirY}
		got, err := resolveRepoDir(ctx, deps, "agent")
		require.Error(t, err)
		assert.Empty(t, got)
		assert.NotEqual(t, dirY, got, "fail-loud floor must NEVER silently return the cwd tree for a cross-repo arg")
	})

	t.Run("cross_repo_name_resolves_via_manifest", func(t *testing.T) {
		// A bare cross-repo NAME recorded in the machine-local manifest resolves
		// to its recorded directory — the recorded-fact path the ticket adds.
		m := withTestManifest(t)
		parent := t.TempDir()
		dirY := filepath.Join(parent, "knowledge")
		dirX := filepath.Join(parent, "agent")
		require.NoError(t, os.MkdirAll(dirY, 0o750))
		require.NoError(t, os.MkdirAll(dirX, 0o750))
		require.NoError(t, m.Record("agent", dirX))
		deps := astTestDeps{rootDir: dirY}
		got, err := resolveRepoDir(ctx, deps, "agent")
		require.NoError(t, err)
		assert.Equal(t, dirX, got, "a manifest-recorded cross-repo name must resolve to its recorded dir")
	})

	t.Run("cross_repo_manifest_stale_dir_fails_loud", func(t *testing.T) {
		// A manifest entry whose recorded checkout has since been removed must
		// fall through to the fail-loud floor, never walk the phantom path.
		m := withTestManifest(t)
		parent := t.TempDir()
		dirY := filepath.Join(parent, "knowledge")
		require.NoError(t, os.MkdirAll(dirY, 0o750))
		goneDir := filepath.Join(parent, "agent-was-here")
		require.NoError(t, m.Record("agent", goneDir)) // recorded but never created
		deps := astTestDeps{rootDir: dirY}
		got, err := resolveRepoDir(ctx, deps, "agent")
		require.Error(t, err)
		assert.Empty(t, got)
	})

	t.Run("abs_path_walks_existing_dir", func(t *testing.T) {
		// An absolute path to an existing directory is the user's direct
		// instruction: walk it as-is, no sibling probe, no ResolveCwd gate.
		parent := t.TempDir()
		dirY := filepath.Join(parent, "knowledge")
		require.NoError(t, os.MkdirAll(dirY, 0o750))
		absTarget := t.TempDir() // an unrelated existing absolute dir
		deps := astTestDeps{rootDir: dirY}
		got, err := resolveRepoDir(ctx, deps, absTarget)
		require.NoError(t, err)
		assert.Equal(t, absTarget, got, "an existing absolute path must be walked directly")
	})

	t.Run("abs_path_missing_errors", func(t *testing.T) {
		// A non-existent absolute path hits the fail-loud floor — never a
		// silent fallback to the cwd tree.
		parent := t.TempDir()
		dirY := filepath.Join(parent, "knowledge")
		require.NoError(t, os.MkdirAll(dirY, 0o750))
		missing := filepath.Join(t.TempDir(), "does-not-exist")
		deps := astTestDeps{rootDir: dirY}
		got, err := resolveRepoDir(ctx, deps, missing)
		require.Error(t, err)
		assert.Empty(t, got)
	})

	t.Run("empty_root_errors", func(t *testing.T) {
		deps := astTestDeps{rootDir: ""}
		_, err := resolveRepoDir(ctx, deps, "")
		require.Error(t, err)
	})
}

// TestFlexInt_NumberAndString pins the flex-decode behavior for the
// limit field: both JSON numbers (5) and JSON strings ("5") deserialize
// cleanly. LLMs frequently quote numeric params.
func TestFlexInt_NumberAndString(t *testing.T) {
	t.Run("number", func(t *testing.T) {
		var v flexInt
		require.NoError(t, v.UnmarshalJSON([]byte(`5`)))
		assert.Equal(t, flexInt(5), v)
	})
	t.Run("string", func(t *testing.T) {
		var v flexInt
		require.NoError(t, v.UnmarshalJSON([]byte(`"42"`)))
		assert.Equal(t, flexInt(42), v)
	})
	t.Run("invalid_string", func(t *testing.T) {
		var v flexInt
		require.Error(t, v.UnmarshalJSON([]byte(`"abc"`)))
	})
}

// Smoke test: explain works without store/ctx plumbing — InterceptAst wraps
// every call in context.Background() internally.
func TestHandleAstExplain_NoStoreNeeded(t *testing.T) {
	deps := astTestDeps{rootDir: t.TempDir()}
	body, isErr, _ := callAst(t, deps, `{"operation":"explain","language":"go","snippet":"package p"}`)
	require.False(t, isErr, "expected non-error for valid Go snippet")
	assert.Contains(t, body, "source_file")
	assert.True(t, strings.HasPrefix(body, "source_file"))
}
