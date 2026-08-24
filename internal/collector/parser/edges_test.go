// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// refSiteFor builds the per-file reference site a chunker would attach.
//
// IT HARDCODES GO whatever the file path says. That was invisible while every
// rung behaved identically in every language; the sibling rung is now
// per-language, so a hand-built site with a .ts path still resolves as Go and
// is gated. A case about a language's own rung must say which language it
// means — refSiteForLang is how.
func refSiteFor(file, scope, parent string) *treesitter.RefSite {
	return &treesitter.RefSite{File: file, Scope: scope, Parent: parent, Lang: treesitter.LangGo}
}

// refSiteForLang is refSiteFor with the language as a parameter, for the cases
// whose subject is a per-language rung rather than the ladder's shape.
func refSiteForLang(file, scope, parent string, lang treesitter.Language) *treesitter.RefSite {
	ref := refSiteFor(file, scope, parent)
	ref.Lang = lang
	return ref
}

// indexOf builds a declIndex from (nodeID, scope, parent, name) tuples.
func indexOf(t *testing.T, recs ...*declRec) *declIndex {
	t.Helper()
	ix := newDeclIndex(len(recs))
	for _, r := range recs {
		require.NoError(t, ix.add(r))
	}
	return ix
}

// TestResolveEdges_PreservesMetadata pins the metadata the resolver must put on
// the edges it emits.
//
// The resolver no longer REWRITES caller-supplied edges — it CONSTRUCTS graph
// edges from the chunker's results — so the property under test is that the
// chunker's Weight survives the construction, and that a multi-candidate
// reference gets the three residue fields rather than being narrowed to one
// edge. The original regression this name guards (tree-sitter CALLS edges
// silently losing their call-site Weight) is still the first assertion.
func TestResolveEdges_PreservesMetadata(t *testing.T) {
	t.Run("bound_reference_keeps_weight", func(t *testing.T) {
		results := []*treesitter.Result{{
			FilePath: "a/caller.go",
			Language: treesitter.LangGo,
			Edges: []treesitter.Edge{{
				FromID: "a/caller.go:Caller",
				ToID:   "Callee",
				Type:   treesitter.EdgeCalls,
				Weight: 7,
				Ref:    refSiteFor("a/caller.go", "dir:a", ""),
			}},
		}}
		ix := indexOf(t, &declRec{NodeID: "a/callee.go:Callee", File: "a/callee.go", Scope: "dir:a", Name: "Callee"})
		nodeIDs := map[string]bool{"a/caller.go:Caller": true, "a/callee.go:Callee": true}

		got := resolveEdges(results, ix, nodeIDs)

		require.Len(t, got, 1, "one candidate binds to exactly one edge")
		e := got[0]
		assert.Equal(t, "a/caller.go:Caller", e.FromId)
		assert.Equal(t, "a/callee.go:Callee", e.ToId)
		assert.Equal(t, string(kgtypes.EdgeCalls), e.Type)
		assert.InDelta(t, 7.0, e.Weight, 1e-9, "Weight must survive construction")
		// A bound edge carries the RUNG that resolved it on Method, and none of
		// the residue fields: Confidence and Evidence mean "this edge is one of
		// several guesses", which a bound edge is not, while Method is an
		// ATTRIBUTION that every bound edge can carry truthfully.
		assert.InDelta(t, 0.0, e.Confidence, 1e-9)
		assert.Equal(t, string(RuleOwnScope), e.Method,
			"a bound edge carries the rung that resolved it")
		assert.Empty(t, e.Evidence)
	})

	t.Run("ambiguous_reference_carries_confidence_method_and_group_key", func(t *testing.T) {
		ref := refSiteFor("a/caller.go", "dir:a", "")
		results := []*treesitter.Result{{
			FilePath: "a/caller.go",
			Language: treesitter.LangGo,
			Edges: []treesitter.Edge{{
				FromID: "a/caller.go:Caller",
				ToID:   "Callee",
				Type:   treesitter.EdgeCalls,
				Weight: 3,
				Ref:    ref,
			}},
		}}
		// TWO surviving declarations under one key — the shape the replaced
		// scalar map could not represent, because the second assignment
		// deleted the first.
		ix := indexOf(t,
			&declRec{NodeID: "a/one.go:Callee", File: "a/one.go", Scope: "dir:a", Name: "Callee"},
			&declRec{NodeID: "a/two.go:Callee", File: "a/two.go", Scope: "dir:a", Name: "Callee"},
		)
		nodeIDs := map[string]bool{"a/caller.go:Caller": true, "a/one.go:Callee": true, "a/two.go:Callee": true}

		got := resolveEdges(results, ix, nodeIDs)

		require.Len(t, got, 2, "an ambiguous reference emits one edge per candidate, never a narrowed guess")
		wantKey := groupKey("Callee", string(kgtypes.EdgeCalls), "a/caller.go:Caller", 0)
		for _, e := range got {
			assert.InDelta(t, 0.5, e.Confidence, 1e-9, "Confidence is 1/N")
			assert.Equal(t, kgtypes.EdgeMethodAmbiguousName, e.Method)
			assert.Equal(t, wantKey, e.Evidence, "every member of one group shares one key")
		}
		// File order, which the index preserves by construction.
		assert.Equal(t, "a/one.go:Callee", got[0].ToId)
		assert.Equal(t, "a/two.go:Callee", got[1].ToId)
	})
}
