// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// TestResolveEdges_PreservesMetadata verifies that resolveEdges carries
// Weight/Confidence/Method/Evidence/LastValidated through the raw-ID →
// node-ID rewrite. Regression test for the bug where tree-sitter CALLS
// edges silently lost their Weight (call-site count) in the resolver,
// producing Weight-zero edges in the code graph.
func TestResolveEdges_PreservesMetadata(t *testing.T) {
	// knowledgev1.Edge.LastValidated is an int64 (unix nanos); carry a
	// representative timestamp as int64.
	lv := time.Now().UTC().Truncate(time.Second).UnixNano()
	edges := []*knowledgev1.Edge{
		{
			FromId:        "pkgA.Caller",
			ToId:          "pkgB.Callee",
			Type:          string(kgtypes.EdgeCalls),
			Weight:        7,
			Confidence:    0.82,
			Method:        "tree-sitter",
			Evidence:      "3 call sites in function body",
			LastValidated: lv,
		},
	}
	symbolMap := map[string]string{
		"pkgA.Caller": "a/caller.go:Caller",
		"pkgB.Callee": "b/callee.go:Callee",
	}
	nodeIDs := map[string]bool{
		"a/caller.go:Caller": true,
		"b/callee.go:Callee": true,
	}

	got := resolveEdges(edges, symbolMap, nodeIDs)

	assert.Len(t, got, 1, "resolver must emit exactly one resolved edge")
	e := got[0]
	assert.Equal(t, "a/caller.go:Caller", e.FromId)
	assert.Equal(t, "b/callee.go:Callee", e.ToId)
	assert.Equal(t, string(kgtypes.EdgeCalls), e.Type)
	assert.InDelta(t, 7.0, e.Weight, 1e-9, "Weight must be preserved")
	assert.InDelta(t, 0.82, e.Confidence, 1e-9, "Confidence must be preserved")
	assert.Equal(t, "tree-sitter", e.Method, "Method must be preserved")
	assert.Equal(t, "3 call sites in function body", e.Evidence, "Evidence must be preserved")
	assert.Equal(t, lv, e.LastValidated, "LastValidated must be preserved")
}
