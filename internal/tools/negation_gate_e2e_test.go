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
	handled, res := InterceptNegationGate(opCtx(), interceptTestDeps{gc: gc}, negationParams(t, tool, args))
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
		proxyNodes: []*knowledgev1.Node{mkSourceCodeProxy(proxyID, codeRefCodeID)},
		codeNodes:  []*knowledgev1.Node{{Id: codeRefCodeID, Type: "function", Content: codeRefContent}},
	}
}

// --- Citation-shape fixtures: content-less (file-level) vs content-bearing ---

// The file-level citation fixture. A FILE code node is keyed by a bare path with no
// symbol suffix and carries NO Content — the code graph stores source on symbol
// nodes, not on the file node above them. That empty Content is the whole subject
// of these tests: an empty resolution must never be silently compared against.
const (
	fileOnlyCodeID  = "pkg/cache.go"
	fileOnlyProxyID = "proxy:knowledge:file-only"
)

// fileOnlyFake serves a contradicted thought whose ONLY code-ref born-link resolves
// to the content-less file node above, plus that thought's own Summary+Content for
// the require-own-content path. Parameterised by id/summary/content so a caller can
// build it sharing a node ID and body with ownContentFake.
func fileOnlyFake(id, summary, content string) *citedSourceFake {
	return &citedSourceFake{
		edges: []*knowledgev1.Edge{
			{FromId: id, ToId: fileOnlyProxyID, Type: string(kgtypes.EdgeRelatesTo), Method: "code-ref"},
		},
		proxyNodes: []*knowledgev1.Node{mkSourceCodeProxy(fileOnlyProxyID, fileOnlyCodeID)},
		// Content deliberately unset — this is the content-less file node.
		codeNodes: []*knowledgev1.Node{{Id: fileOnlyCodeID, Type: "file"}},
		thoughtNode: &knowledgev1.Node{
			Id: id, Type: string(kgtypes.NodeThought),
			Summary: summary, Content: content,
		},
	}
}

// mixedFake serves a thought carrying TWO code-ref born-links: one to the
// content-less file node, one to the symbol node that carries real source
// (codeRefCodeID/codeRefContent). It is the guard fixture — excluding the empty
// source must not lower the bar the surviving symbol source sets.
func mixedFake(id, summary, content string) *citedSourceFake {
	const symbolProxyID = "proxy:knowledge:mixed-symbol"
	return &citedSourceFake{
		edges: []*knowledgev1.Edge{
			{FromId: id, ToId: fileOnlyProxyID, Type: string(kgtypes.EdgeRelatesTo), Method: "code-ref"},
			{FromId: id, ToId: symbolProxyID, Type: string(kgtypes.EdgeRelatesTo), Method: "code-ref"},
		},
		proxyNodes: []*knowledgev1.Node{
			mkSourceCodeProxy(fileOnlyProxyID, fileOnlyCodeID),
			mkSourceCodeProxy(symbolProxyID, codeRefCodeID),
		},
		codeNodes: []*knowledgev1.Node{
			{Id: fileOnlyCodeID, Type: "file"},
			{Id: codeRefCodeID, Type: "function", Content: codeRefContent},
		},
		thoughtNode: &knowledgev1.Node{
			Id: id, Type: string(kgtypes.NodeThought),
			Summary: summary, Content: content,
		},
	}
}

// linksParamFake serves a thought born-linked by the plain links param: a relates-to
// edge with Method left EMPTY rather than "code-ref". codeRefProxiesByThought
// (cmd/knowledge/internal/thought/cited_code_staleness.go:121) drops every
// relates-to edge whose Method is not code-ref, so this edge never reaches the
// code-ref resolution at all and the thought is served by its own content.
func linksParamFake(id, summary, content string) *citedSourceFake {
	return &citedSourceFake{
		edges: []*knowledgev1.Edge{
			{FromId: id, ToId: fileOnlyProxyID, Type: string(kgtypes.EdgeRelatesTo)},
		},
		proxyNodes: []*knowledgev1.Node{mkSourceCodeProxy(fileOnlyProxyID, fileOnlyCodeID)},
		codeNodes:  []*knowledgev1.Node{{Id: fileOnlyCodeID, Type: "file"}},
		thoughtNode: &knowledgev1.Node{
			Id: id, Type: string(kgtypes.NodeThought),
			Summary: summary, Content: content,
		},
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

// --- Citation-shape battery: file-level, mixed, and links-param born-links ---

// TestNegationGateE2E_FileLevelCitation_FallsBackToOwnContent: a thought whose only
// code-ref born-links resolve to content-less file nodes is negatable by a verbatim
// quote of its OWN current Summary+Content — the same bar every thought citing no
// code already meets. The second subtest varies ONLY the cited file node's
// UpdatedAt, the single axis a staleness explanation would move, and shows the
// outcome does not turn on it.
func TestNegationGateE2E_FileLevelCitation_FallsBackToOwnContent(t *testing.T) {
	const (
		nodeID  = "th-file-only"
		summary = "claim: the writer flushes synchronously"
		content = "the negator disagrees with this reasoning body"
		quote   = "disagrees with this reasoning body"
	)
	args := map[string]any{
		"operation": "update", "id": nodeID, "status": "invalidated", "verified_quote": quote,
	}

	t.Run("own-content quote passes", func(t *testing.T) {
		handled, rejected := runGate(t, fileOnlyFake(nodeID, summary, content), "mutate", args)
		assert.False(t, handled, "a content-less file citation leaves the own-content quote as the bar")
		assert.False(t, rejected)
	})

	t.Run("fresh UpdatedAt on the cited file node changes nothing", func(t *testing.T) {
		fake := fileOnlyFake(nodeID, summary, content)
		fake.codeNodes[0].UpdatedAt = 1_800_000_000_000_000_000
		handled, rejected := runGate(t, fake, "mutate", args)
		assert.False(t, handled, "the cited node's UpdatedAt is not an input to the quote match")
		assert.False(t, rejected)
	})
}

// TestNegationGateE2E_MixedFileAndSymbolCitation: when a thought cites BOTH a
// content-less file node and a symbol node carrying real source, the symbol source
// is still the bar. Both subtests are required — the first alone would pass against
// a change that dropped code sources entirely, the second alone against a change
// that resolved nothing.
func TestNegationGateE2E_MixedFileAndSymbolCitation(t *testing.T) {
	const (
		nodeID   = "th-mixed"
		summary  = "claim: flushing happens on a timer"
		content  = "the thought's own reasoning, which quotes no source line"
		ownQuote = "which quotes no source line"
	)
	mkArgs := func(quote string) map[string]any {
		return map[string]any{
			"operation": "link", "relationship": "contradicts", "to": nodeID, "verified_quote": quote,
		}
	}

	t.Run("symbol source quote passes", func(t *testing.T) {
		handled, rejected := runGate(t, mixedFake(nodeID, summary, content), "mutate", mkArgs("return w.Sync()"))
		assert.False(t, handled, "the surviving symbol source still validates a verbatim quote of itself")
		assert.False(t, rejected)
	})

	t.Run("own-content quote is still REJECTED", func(t *testing.T) {
		handled, rejected := runGate(t, mixedFake(nodeID, summary, content), "mutate", mkArgs(ownQuote))
		assert.True(t, handled, "a thought citing real symbol source may not be negated by quoting itself")
		assert.True(t, rejected)
	})
}

// TestNegationGateE2E_LinksParamEdgeUnaffected: a born-link written by the plain
// links param carries no code-ref method, is dropped before the code-ref resolution,
// and leaves the thought on the own-content path exactly as before.
func TestNegationGateE2E_LinksParamEdgeUnaffected(t *testing.T) {
	const (
		nodeID  = "th-links-param"
		summary = "claim: a method-less relates-to edge is not a citation"
		content = "a plain relates-to edge never reaches the code-ref resolution"
		quote   = "never reaches the code-ref resolution"
	)
	handled, rejected := runGate(t, linksParamFake(nodeID, summary, content), "mutate", map[string]any{
		"operation": "update", "id": nodeID, "status": "invalidated", "verified_quote": quote,
	})
	assert.False(t, handled, "the method-less edge is filtered out, so the own-content quote is the bar")
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
