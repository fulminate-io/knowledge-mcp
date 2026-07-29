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
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector"
)

// astTestDeps is a minimal ClientDeps stub for the ast intercept. Only
// RootDir() is exercised by the ast handlers; GraphClient and Sink return
// nil because the ast tool never calls them (NoOpBackend hydration means
// zero wire traffic).
type astTestDeps struct {
	rootDir    string
	rootDirSet bool
}

func (d astTestDeps) LocalLiveness() LocalLiveness                 { return nil }
func (d astTestDeps) Sink() collector.Sink                         { return nil }
func (d astTestDeps) RootDir() string                              { return d.rootDir }
func (d astTestDeps) RootDirSet() bool                             { return d.rootDirSet }
func (d astTestDeps) UsageAnalyzer() UsageAnalyzerAPI              { return nil }
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

func (d astTestDeps) ClearHealLatch(kgtypes.GraphType, string) {}
func (d astTestDeps) ReflectionForcer() ReflectionForcer       { return nil }
func (d astTestDeps) SimilarityForcer() SimilarityForcer       { return nil }

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
	handled, res := InterceptAst(context.Background(), deps, params)
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
		handled, res := InterceptAst(context.Background(), deps, params)
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

// Smoke test: explain works without store plumbing — callAst threads a
// context.Background() through InterceptAst; explain needs no session cwd.
func TestHandleAstExplain_NoStoreNeeded(t *testing.T) {
	deps := astTestDeps{rootDir: t.TempDir()}
	body, isErr, _ := callAst(t, deps, `{"operation":"explain","language":"go","snippet":"package p"}`)
	require.False(t, isErr, "expected non-error for valid Go snippet")
	assert.Contains(t, body, "source_file")
	assert.True(t, strings.HasPrefix(body, "source_file"))
}
