// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// --- recognizeNegationOp: the four negation shapes, and only those ---

func TestRecognizeNegationOp(t *testing.T) {
	cases := []struct {
		name     string
		tool     string
		a        mutateArgs
		t        thinkArgs
		wantOK   bool
		wantNode string
		wantQ    string
	}{
		{
			name:     "mutate link contradicts",
			tool:     "mutate",
			a:        mutateArgs{Operation: "link", Relationship: "contradicts", To: "n1", VerifiedQuote: "q"},
			wantOK:   true,
			wantNode: "n1",
			wantQ:    "q",
		},
		{
			name:     "mutate update invalidated",
			tool:     "mutate",
			a:        mutateArgs{Operation: "update", Status: "invalidated", ID: "n2", VerifiedQuote: "q2"},
			wantOK:   true,
			wantNode: "n2",
			wantQ:    "q2",
		},
		{
			name:     "thoughts think branches_from supersession",
			tool:     "thoughts",
			t:        thinkArgs{BranchesFrom: "n3", VerifiedQuote: "q3"},
			wantOK:   true,
			wantNode: "n3",
			wantQ:    "q3",
		},
		{
			name:     "thoughts think branches_from + status invalidated",
			tool:     "thoughts",
			t:        thinkArgs{BranchesFrom: "n4", Status: "invalidated", VerifiedQuote: "q4"},
			wantOK:   true,
			wantNode: "n4",
			wantQ:    "q4",
		},
		// --- non-negation shapes: must NOT be recognized ---
		{
			name:   "mutate link relates-to (not contradicts)",
			tool:   "mutate",
			a:      mutateArgs{Operation: "link", Relationship: "relates-to", To: "n1"},
			wantOK: false,
		},
		{
			name:   "mutate update status validated (not invalidated)",
			tool:   "mutate",
			a:      mutateArgs{Operation: "update", Status: "validated", ID: "n2"},
			wantOK: false,
		},
		{
			name:   "mutate create (not a negation)",
			tool:   "mutate",
			a:      mutateArgs{Operation: "create", Type: "finding"},
			wantOK: false,
		},
		{
			name:   "thoughts think with no branches_from (fresh thought)",
			tool:   "thoughts",
			t:      thinkArgs{Status: "invalidated"},
			wantOK: false,
		},
		{
			name:   "thoughts charge (not think)",
			tool:   "thoughts",
			t:      thinkArgs{},
			wantOK: false,
		},
		{
			name:   "unrelated tool",
			tool:   "search",
			wantOK: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			op, ok := recognizeNegationOp(tc.tool, tc.a, tc.t)
			assert.Equal(t, tc.wantOK, ok)
			if tc.wantOK {
				assert.Equal(t, tc.wantNode, op.ContradictedID)
				assert.Equal(t, tc.wantQ, op.Quote)
			}
		})
	}
}

// --- validateNegationQuote: deterministic substring match, fail-closed ---

func TestValidateNegationQuote(t *testing.T) {
	const (
		thoughtID = "th-own"
		summary   = "The summary claim"
		content   = "func DoThing() {\n\treturn nil\n}"
	)
	// Own-content source: no code-ref edge → render.FetchNode fallback serves the
	// thought node's Summary+Content.
	ownFake := func() *citedSourceFake {
		return &citedSourceFake{
			thoughtNode: &knowledgev1.Node{
				Id: thoughtID, Type: string(kgtypes.NodeThought),
				Summary: summary, Content: content,
			},
		}
	}

	cases := []struct {
		name    string
		op      negationOp
		wantErr bool
	}{
		{
			name:    "exact substring of content passes",
			op:      negationOp{ContradictedID: thoughtID, Quote: "return nil"},
			wantErr: false,
		},
		{
			name:    "substring of summary passes",
			op:      negationOp{ContradictedID: thoughtID, Quote: "The summary claim"},
			wantErr: false,
		},
		{
			name:    "whitespace-only differences still pass (normalize collapses newlines+indentation)",
			op:      negationOp{ContradictedID: thoughtID, Quote: "func DoThing() {     return nil }"},
			wantErr: false,
		},
		{
			name:    "empty quote rejects (fail-closed)",
			op:      negationOp{ContradictedID: thoughtID, Quote: ""},
			wantErr: true,
		},
		{
			name:    "whitespace-only quote rejects",
			op:      negationOp{ContradictedID: thoughtID, Quote: "   \n\t "},
			wantErr: true,
		},
		{
			name:    "hallucinated quote (not in source) rejects",
			op:      negationOp{ContradictedID: thoughtID, Quote: "this text was never in the node"},
			wantErr: true,
		},
		{
			name:    "unresolvable node rejects (fail-closed)",
			op:      negationOp{ContradictedID: "does-not-exist", Quote: "anything"},
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateNegationQuote(context.Background(), ownFake(), tc.op)
			if tc.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// --- quoteLocalToRange: coarse cited-path consistency over Origin ---

func TestQuoteLocalToRange(t *testing.T) {
	codeSrc := currentSource{Text: "x", Origin: "code:pkg/file.go:Sym"}
	thoughtSrc := currentSource{Text: "x", Origin: "thought:th-1"}

	// Cited path matches the code node's path → local.
	assert.True(t, quoteLocalToRange(codeSrc, "pkg/file.go:120-140"))
	// Cited path does NOT match → not local.
	assert.False(t, quoteLocalToRange(codeSrc, "other/path.go:1-2"))
	// Empty / malformed range constrains nothing → local.
	assert.True(t, quoteLocalToRange(codeSrc, ""))
	assert.True(t, quoteLocalToRange(codeSrc, "norange"))
	// Own-content (thought) source has no file path → a path-scoped range cannot
	// constrain it → treated as local.
	assert.True(t, quoteLocalToRange(thoughtSrc, "pkg/file.go:1-2"))
}

func TestNormalize(t *testing.T) {
	assert.Equal(t, "a b c", normalize("  a\t b\n\nc  "))
	assert.Equal(t, "func X() { return }", normalize("func X() {\n\treturn\n}"))
	assert.Empty(t, normalize("   \n\t "))
}

// --- InterceptNegationGate dispatch: handled/IsError vs fall-through ---

// negationParams builds CallToolParams for a tool with the given JSON arg map.
func negationParams(t *testing.T, name string, args map[string]any) kgtools.CallToolParams {
	t.Helper()
	raw, err := json.Marshal(args)
	require.NoError(t, err)
	return kgtools.CallToolParams{Name: name, Arguments: raw}
}

func TestInterceptNegationGate_Dispatch(t *testing.T) {
	const (
		nodeID  = "th-target"
		summary = "the contradicted claim"
		content = "the live reasoning body of the node"
	)
	// deps whose GraphCaller resolves nodeID's own current source.
	mkDeps := func() interceptTestDeps {
		return interceptTestDeps{gc: &citedSourceFake{
			thoughtNode: &knowledgev1.Node{
				Id: nodeID, Type: string(kgtypes.NodeThought),
				Summary: summary, Content: content,
			},
		}}
	}

	t.Run("contradicts-link no quote → handled+IsError", func(t *testing.T) {
		handled, res := InterceptNegationGate(opCtx(), mkDeps(), negationParams(t, "mutate", map[string]any{
			"operation": "link", "relationship": "contradicts", "to": nodeID,
		}))
		assert.True(t, handled, "missing-quote negation is claimed by the gate")
		assert.True(t, res.IsError, "and rejected")
	})

	t.Run("contradicts-link matching quote → falls through", func(t *testing.T) {
		handled, _ := InterceptNegationGate(opCtx(), mkDeps(), negationParams(t, "mutate", map[string]any{
			"operation": "link", "relationship": "contradicts", "to": nodeID,
			"verified_quote": "live reasoning body",
		}))
		assert.False(t, handled, "a validated negation falls through to the real handler")
	})

	t.Run("non-negation mutate(update status:validated) → falls through", func(t *testing.T) {
		handled, _ := InterceptNegationGate(opCtx(), mkDeps(), negationParams(t, "mutate", map[string]any{
			"operation": "update", "id": nodeID, "status": "validated",
		}))
		assert.False(t, handled, "a non-negation update is never claimed by the gate")
	})

	t.Run("unquoted think branches_from supersession → handled+IsError", func(t *testing.T) {
		handled, res := InterceptNegationGate(opCtx(), mkDeps(), negationParams(t, "thoughts", map[string]any{
			"operation": "think", "content": "new", "summary": "new", "branches_from": nodeID,
		}))
		assert.True(t, handled, "unquoted supersession is claimed by the gate")
		assert.True(t, res.IsError, "and rejected")
	})

	t.Run("nil gc → fail-open fall-through", func(t *testing.T) {
		handled, _ := InterceptNegationGate(opCtx(), interceptTestDeps{gc: nil}, negationParams(t, "mutate", map[string]any{
			"operation": "link", "relationship": "contradicts", "to": nodeID,
		}))
		assert.False(t, handled, "no graph access → fail-open to the existing handler")
	})

	t.Run("unrelated tool → falls through", func(t *testing.T) {
		handled, _ := InterceptNegationGate(opCtx(), mkDeps(), negationParams(t, "search", map[string]any{"query": "x"}))
		assert.False(t, handled)
	})
}
