// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// TestPracticeSearch_HubScopedSearchToolNarrowsToTheHub is requirement 3 on the
// SEARCH tool's practice arm: the S column of the arm matrix.
//
// THE SHARED COMPOSER IS NOT THE OBSERVER. composePracticeSearchClient is pinned
// by the query tool's parity row, so a hub it receives is narrowed correctly;
// what nothing pinned was the search tool's own hand-off of ITS caller's
// `source_hub` into that composer. With the hand-off replaced by the empty
// string, a hub-scoped search silently answered from the whole combined graph
// and every suite stayed green — a widening of a read the caller narrowed,
// which is the silent-coercion class this repository refuses.
//
// Two discriminants, both dead under that mutation: the member-id resolve
// issues a browse carrying the hub as a metadata predicate BEFORE the ranked
// search (the client-side resolution section 4 chose), and no node outside the
// hub reaches the render.
func TestPracticeSearch_HubScopedSearchToolNarrowsToTheHub(t *testing.T) {
	gc, h := newFanOutHarnessWithHandler(t, []string{"default", "go"},
		practiceNode("p:in-hub", "InHubPattern", "under hub-1"),
		practiceNode("p:outside", "OutsidePattern", "under another hub"),
	)
	mgr := newFanOutSegmentSearcher(map[string][]searchengine.Hit{
		"default": {{ID: "p:in-hub", Score: 0.90}, {ID: "p:outside", Score: 0.95}},
		"go":      {{ID: "p:go", Score: 0.99}},
	})
	deps := &interceptDeps{gc: gc, segMgr: mgr}

	handled, out := InterceptSearch(opCtx(), deps, searchParams(t, map[string]any{
		"graph": "practice", "query": "pool", "source_hub": "hub-1",
	}))
	require.True(t, handled)

	// Discriminant one: the hub reached the composer, which resolved its
	// membership through the graph caller with the hub as an equality predicate
	// on the dedicated hub key. The empty-hub path issues no such read.
	var hubReads int
	for _, req := range h.recordedReqs() {
		for _, p := range req.GetQuery().GetSelection().GetMetadataPredicates() {
			if p.GetKey() == kgtypes.MetaKeySourceHub && p.GetValue() == "hub-1" &&
				p.GetOp() == knowledgev1.MetadataPredicate_OP_EQ {
				hubReads++
			}
		}
	}
	require.NotZero(t, hubReads,
		"a hub-scoped search resolves the hub's members by a source_hub=hub-1 predicate before ranking; "+
			"with the search tool's hand-off dropped, no such read is issued and the whole graph answers")

	// Discriminant two: the pool searched is still the one combined graph, and
	// nothing outside the hub is rendered. Under the widening mutation the
	// unfiltered pool returns both hits and OutsidePattern reaches the caller.
	assert.Equal(t, []string{"default"}, mgr.searchedNames(),
		"a hub narrows INSIDE the combined graph; it never selects a legacy pool")
	assert.NotContains(t, textBodyTools(out), "OutsidePattern",
		"a node outside the named hub never reaches a hub-scoped caller")
}
