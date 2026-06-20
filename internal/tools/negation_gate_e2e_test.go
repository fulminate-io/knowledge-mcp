// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// runGate is the e2e helper: build the CallToolParams, run it through
// InterceptNegationGate with the given fake gc, and report (handled, rejected).
func runGate(t *testing.T, gc GraphCaller, tool string, args map[string]any) (handled, rejected bool) {
	t.Helper()
	handled, res := InterceptNegationGate(interceptTestDeps{gc: gc}, negationParams(t, tool, args))
	return handled, res.IsError
}

// --- E2E battery: every negation op, accept + reject, both source paths ---

// ownContentFake serves a contradicted thought's OWN current Summary+Content (the
// no-code-ref REQUIRE-OWN-CONTENT path: no code-ref edges → render.FetchNode).
func ownContentFake(id, summary, content string) *citedSourceFake {
	return &citedSourceFake{
		thoughtNode: &knowledgev1.Node{
			Id: id, Type: string(kgtypes.NodeThought),
			Summary: summary, Content: content,
		},
	}
}

// Code-ref e2e fixture: a contradicted thought (codeRefThoughtID) whose
// ResolveCitedCodeNodes boundary resolves the cited code node (codeRefCodeID)
// carrying codeRefContent as its live source. codeRefThoughtID MUST match the
// `to` the gate validates — ResolveCitedCodeNodes filters the code-ref edge by
// FromId == the resolved thought ID — so the const is shared with the gate calls.
const (
	codeRefThoughtID = "th-code"
	codeRefCodeID    = "pkg/cache.go:Flush"
	codeRefContent   = "func Flush() error {\n\treturn w.Sync()\n}"
)

// codeRefFake serves the code-ref e2e fixture above.
func codeRefFake() *citedSourceFake {
	const proxyID = "proxy:knowledge:code-ref"
	return &citedSourceFake{
		edges: []*knowledgev1.Edge{
			{FromId: codeRefThoughtID, ToId: proxyID, Type: string(kgtypes.EdgeRelatesTo), Method: "code-ref"},
		},
		proxyNodes: []*knowledgev1.Node{mkSourceCodeProxy(proxyID, "knowledge", codeRefCodeID)},
		codeNodes:  []*knowledgev1.Node{{Id: codeRefCodeID, Type: "function", Content: codeRefContent}},
	}
}

func TestNegationGateE2E_AllOps_OwnContentPath(t *testing.T) {
	const (
		nodeID  = "th-1"
		summary = "claim: the cache is write-through"
		content = "the implementation flushes on every write"
		good    = "flushes on every write"
		stale   = "flushes on a timer" // never in the source
	)

	// Each op is exercised accept (matching quote) + reject (hallucinated quote).
	ops := []struct {
		name string
		tool string
		args func(quote string) map[string]any
	}{
		{
			name: "mutate link contradicts",
			tool: "mutate",
			args: func(q string) map[string]any {
				return map[string]any{"operation": "link", "relationship": "contradicts", "to": nodeID, "verified_quote": q}
			},
		},
		{
			name: "mutate update invalidated",
			tool: "mutate",
			args: func(q string) map[string]any {
				return map[string]any{"operation": "update", "id": nodeID, "status": "invalidated", "verified_quote": q}
			},
		},
		{
			name: "thoughts think branches_from + status invalidated",
			tool: "thoughts",
			args: func(q string) map[string]any {
				return map[string]any{"operation": "think", "content": "c", "summary": "s", "branches_from": nodeID, "status": "invalidated", "verified_quote": q}
			},
		},
		{
			name: "thoughts think branches_from supersession (no status)",
			tool: "thoughts",
			args: func(q string) map[string]any {
				return map[string]any{"operation": "think", "content": "c", "summary": "s", "branches_from": nodeID, "verified_quote": q}
			},
		},
	}

	for _, op := range ops {
		t.Run(op.name+"/accept", func(t *testing.T) {
			handled, rejected := runGate(t, ownContentFake(nodeID, summary, content), op.tool, op.args(good))
			assert.False(t, handled, "a verbatim current-source quote passes (falls through)")
			assert.False(t, rejected)
		})
		t.Run(op.name+"/reject-stale", func(t *testing.T) {
			handled, rejected := runGate(t, ownContentFake(nodeID, summary, content), op.tool, op.args(stale))
			assert.True(t, handled, "a stale/hallucinated quote is claimed")
			assert.True(t, rejected, "and rejected")
		})
		t.Run(op.name+"/reject-missing", func(t *testing.T) {
			args := op.args("")
			delete(args, "verified_quote")
			handled, rejected := runGate(t, ownContentFake(nodeID, summary, content), op.tool, args)
			assert.True(t, handled, "a missing quote is claimed")
			assert.True(t, rejected, "and rejected")
		})
	}
}

func TestNegationGateE2E_CodeRefPath(t *testing.T) {
	nodeID := codeRefThoughtID
	t.Run("verbatim quote of cited code passes", func(t *testing.T) {
		handled, rejected := runGate(t, codeRefFake(), "mutate", map[string]any{
			"operation": "link", "relationship": "contradicts", "to": nodeID,
			"verified_quote": "return w.Sync()",
		})
		assert.False(t, handled)
		assert.False(t, rejected)
	})
	t.Run("stale quote of cited code rejects", func(t *testing.T) {
		handled, rejected := runGate(t, codeRefFake(), "mutate", map[string]any{
			"operation": "link", "relationship": "contradicts", "to": nodeID,
			"verified_quote": "return w.Close()", // not in current source
		})
		assert.True(t, handled)
		assert.True(t, rejected)
	})
	t.Run("matching cited_range path passes locality", func(t *testing.T) {
		handled, rejected := runGate(t, codeRefFake(), "mutate", map[string]any{
			"operation": "link", "relationship": "contradicts", "to": nodeID,
			"verified_quote": "return w.Sync()", "cited_range": "pkg/cache.go:1-3",
		})
		assert.False(t, handled)
		assert.False(t, rejected)
	})
	t.Run("mismatched cited_range path fails locality", func(t *testing.T) {
		handled, rejected := runGate(t, codeRefFake(), "mutate", map[string]any{
			"operation": "link", "relationship": "contradicts", "to": nodeID,
			"verified_quote": "return w.Sync()", "cited_range": "other/file.go:1-3",
		})
		assert.True(t, handled, "quote is real but cited path is wrong → locality fails → reject")
		assert.True(t, rejected)
	})
}

func TestNegationGateE2E_WhitespaceNormalization(t *testing.T) {
	const (
		nodeID  = "th-ws"
		summary = "s"
		// Source has tabs + newlines; the quote differs ONLY in run-of-whitespace.
		content = "func DoThing() {\n\t\tif x {\n\t\t\treturn nil\n\t\t}\n}"
	)
	handled, rejected := runGate(t, ownContentFake(nodeID, summary, content), "mutate", map[string]any{
		"operation": "update", "id": nodeID, "status": "invalidated",
		"verified_quote": "if x { return nil }",
	})
	assert.False(t, handled, "a quote differing only in whitespace/indentation still passes")
	assert.False(t, rejected)
}

// --- Mechanical no-LLM determinism assertion (CEO-locked) ---

// forbiddenImportSubstrings are the import-path fragments the gate surface must
// never pull in: any LLM client, embedder, summarizer, or model provider. The
// validation path is string + graph-read ONLY.
var forbiddenImportSubstrings = []string{
	"/llm", "/embed", "summari", "voyage", "anthropic", "openai", "/provider",
}

// TestNegationGate_NoLLMImports parses the import blocks of the two gate-surface
// files (negation_gate.go + negation_source_resolve.go) and asserts NONE import an
// LLM/embed/summarizer/provider package. This mechanically enforces the CEO-locked
// determinism constraint at the FILE level — the tools package legitimately imports
// LLM code in OTHER files (search/embed paths), so the assertion is deliberately
// file-scoped, not package-scoped, to avoid a false-fail.
func TestNegationGate_NoLLMImports(t *testing.T) {
	files := []string{"negation_gate.go", "negation_source_resolve.go"}
	fset := token.NewFileSet()
	for _, f := range files {
		t.Run(f, func(t *testing.T) {
			parsed, err := parser.ParseFile(fset, f, nil, parser.ImportsOnly)
			require.NoError(t, err, "parse %s", f)
			for _, imp := range parsed.Imports {
				path := strings.Trim(imp.Path.Value, `"`)
				for _, bad := range forbiddenImportSubstrings {
					assert.NotContains(t, strings.ToLower(path), bad,
						"%s imports %q which matches forbidden fragment %q — the gate path must be LLM-free", f, path, bad)
				}
			}
		})
	}
}
