// SPDX-License-Identifier: Apache-2.0

package engine

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// keysetCorpus builds an id-ascending corpus of n ids, zero-padded so string
// order and numeric order agree — the drain's cursor contract is defined over
// the backend's id ordering, so the fixture must not smuggle in a different one.
func keysetCorpus(n int) []string {
	ids := make([]string, 0, n)
	for i := range n {
		ids = append(ids, fmt.Sprintf("id-%04d", i))
	}
	return ids
}

// keysetPage is the scripted backend: it serves the corpus slice STRICTLY AFTER
// afterID, capped at pageSize. An empty cursor serves from the head, which is the
// page-1 contract the drain depends on.
func keysetPage(corpus []string, afterID string, pageSize int) []string {
	start := 0
	if afterID != "" {
		for i, id := range corpus {
			if id == afterID {
				start = i + 1
				break
			}
		}
	}
	end := min(start+pageSize, len(corpus))
	return corpus[start:end]
}

func TestDrainKeysetPages(t *testing.T) {
	const pageSize = 4
	// 10 = two full pages plus a SHORT third, so termination is exercised on a
	// short-but-non-empty page rather than only on the empty-page path.
	corpus := keysetCorpus(10)

	var cursors []string
	got, err := DrainKeysetPages(func(afterID string) ([]*knowledgev1.Node, error) {
		cursors = append(cursors, afterID)
		page := keysetPage(corpus, afterID, pageSize)
		nodes := make([]*knowledgev1.Node, 0, len(page))
		for _, id := range page {
			nodes = append(nodes, &knowledgev1.Node{Id: id})
		}
		return nodes, nil
	}, pageSize)
	require.NoError(t, err)

	// The cursor SEQUENCE is the real assertion: a drain that ignored the cursor
	// and re-served page 1 forever would still produce the right set through the
	// seen-set while looping, so the returned set alone cannot pin the behavior.
	assert.Equal(t, []string{"", "id-0003", "id-0007"}, cursors,
		"page 1 must receive the EMPTY cursor and each later page the previous page's LAST id")
	assert.Len(t, cursors, 3, "drain must terminate on the first SHORT page")

	gotIDs := make([]string, 0, len(got))
	for _, n := range got {
		gotIDs = append(gotIDs, n.GetId())
	}
	assert.Equal(t, corpus, gotIDs, "the drain returns the whole corpus, in order, with no duplicates")
}

func TestDrainKeysetIDs(t *testing.T) {
	const pageSize = 4
	corpus := keysetCorpus(10)

	var cursors []string
	got, err := DrainKeysetIDs(func(afterID string) ([]string, error) {
		cursors = append(cursors, afterID)
		return keysetPage(corpus, afterID, pageSize), nil
	}, pageSize)
	require.NoError(t, err)

	assert.Equal(t, []string{"", "id-0003", "id-0007"}, cursors,
		"page 1 must receive the EMPTY cursor and each later page the previous page's LAST id")
	assert.Len(t, cursors, 3, "drain must terminate on the first SHORT page")
	assert.Equal(t, corpus, got, "the drain returns the whole corpus, in order, with no duplicates")
}

// TestDrainKeysetIDsDedupes pins the seen-set invariant guard on the ids twin: a
// backend that re-emits a row at or before the cursor is a real bug, absorbed
// here rather than propagated to the caller as a duplicate.
func TestDrainKeysetIDsDedupes(t *testing.T) {
	const pageSize = 2
	pages := [][]string{{"a", "b"}, {"b", "c"}, {"d"}}
	i := 0
	got, err := DrainKeysetIDs(func(string) ([]string, error) {
		p := pages[i]
		i++
		return p, nil
	}, pageSize)
	require.NoError(t, err)
	assert.Equal(t, []string{"a", "b", "c", "d"}, got)
}

// pivotEdgeServer is the scripted edges backend: incident maps a pivot id to the
// edges the server yields for it. A page's answer is the CONCATENATION over its
// pivots with NO cross-pivot dedup — the real pivot-SET behavior, which is what
// makes the drain's dedup load-bearing rather than defensive.
type pivotEdgeServer struct {
	incident map[string][]knowledgev1.Edge
	pages    [][]string
}

func (s *pivotEdgeServer) fetch(pivots []string) ([]knowledgev1.Edge, error) {
	s.pages = append(s.pages, append([]string(nil), pivots...))
	out := make([]knowledgev1.Edge, 0, len(pivots))
	for _, p := range pivots {
		for i := range s.incident[p] {
			e := &s.incident[p][i]
			out = append(out, knowledgev1.Edge{FromId: e.FromId, ToId: e.ToId, Type: e.Type})
		}
	}
	return out, nil
}

func edgeKeys(edges []knowledgev1.Edge) []string {
	keys := make([]string, 0, len(edges))
	for i := range edges {
		keys = append(keys, edges[i].FromId+"->"+edges[i].ToId+":"+edges[i].Type)
	}
	return keys
}

// TestDrainPivotEdges pins the bounded pivot-page edge drain: the pages it asks
// for are really bounded, the union it returns is complete and deduped, and a
// server answer at the per-page ceiling is refused rather than silently accepted
// as a complete set.
func TestDrainPivotEdges(t *testing.T) {
	t.Run("empty_ids_issues_no_fetch", func(t *testing.T) {
		calls := 0
		got, err := DrainPivotEdges(nil, 10, 100, func([]string) ([]knowledgev1.Edge, error) {
			calls++
			return nil, nil
		})
		require.NoError(t, err)
		assert.Empty(t, got, "an empty pivot set has no edges")
		assert.Zero(t, calls, "an empty pivot set must not reach the server at all")
	})

	t.Run("pages_never_exceed_EdgePivotPageSize_pivots", func(t *testing.T) {
		// Deliberately not a multiple of the page size, so the short final page is
		// exercised too.
		ids := keysetCorpus(EdgePivotPageSize*2 + 7)
		srv := &pivotEdgeServer{incident: map[string][]knowledgev1.Edge{}}
		for _, id := range ids {
			srv.incident[id] = []knowledgev1.Edge{{FromId: id, ToId: id + "-t", Type: "relates-to"}}
		}
		// pageSize 0 selects the default, so the constant itself is under test.
		got, err := DrainPivotEdges(ids, 0, 0, srv.fetch)
		require.NoError(t, err)

		require.Len(t, srv.pages, 3, "2 full pages + 1 short page for 2*size+7 pivots")
		for i, p := range srv.pages {
			assert.LessOrEqual(t, len(p), EdgePivotPageSize, "page %d exceeds the pivot bound", i)
		}
		assert.Len(t, got, len(ids), "every pivot's edge is in the union")
	})

	t.Run("dedups_an_edge_yielded_from_both_endpoints", func(t *testing.T) {
		// One edge a->b, with BOTH endpoints in the pivot set: the server yields it
		// once per endpoint within a single page.
		//
		// Built fresh per entry rather than copied from one variable: a protobuf
		// message embeds a mutex, so copying a filled-in Edge value trips vet's
		// copylocks check. Nothing is lost — the drain dedups on an
		// (from, to, type) key, so these two literals ARE the same edge to it,
		// which is exactly what this test asserts.
		sharedEdge := func() knowledgev1.Edge {
			return knowledgev1.Edge{FromId: "a", ToId: "b", Type: "calls"}
		}
		srv := &pivotEdgeServer{incident: map[string][]knowledgev1.Edge{
			"a": {sharedEdge()},
			"b": {sharedEdge()},
		}}
		got, err := DrainPivotEdges([]string{"a", "b"}, 10, 0, srv.fetch)
		require.NoError(t, err)
		require.Len(t, srv.pages, 1, "both pivots fit one page")
		assert.Equal(t, []string{"a->b:calls"}, edgeKeys(got), "the shared edge is returned exactly once")
	})

	t.Run("halves_a_saturated_page_and_returns_the_full_union", func(t *testing.T) {
		const edgeCap = 4
		// Four pivots, one distinct edge each: the whole-page answer is exactly the
		// cap, so the page is refused and split until the answers come in under it.
		srv := &pivotEdgeServer{incident: map[string][]knowledgev1.Edge{
			"p1": {{FromId: "p1", ToId: "x1", Type: "rel"}},
			"p2": {{FromId: "p2", ToId: "x2", Type: "rel"}},
			"p3": {{FromId: "p3", ToId: "x3", Type: "rel"}},
			"p4": {{FromId: "p4", ToId: "x4", Type: "rel"}},
		}}
		got, err := DrainPivotEdges([]string{"p1", "p2", "p3", "p4"}, 4, edgeCap, srv.fetch)
		require.NoError(t, err)

		assert.Greater(t, len(srv.pages), 1, "a saturated page must be split, not accepted")
		assert.ElementsMatch(t,
			[]string{"p1->x1:rel", "p2->x2:rel", "p3->x3:rel", "p4->x4:rel"},
			edgeKeys(got),
			"the halved reads union back into the COMPLETE set, not a sample")
	})

	t.Run("a_single_pivot_that_saturates_is_an_error", func(t *testing.T) {
		const edgeCap = 3
		// One pivot alone at the cap: no further split exists, so a short union
		// would be a silent lie and the drain must say so instead.
		srv := &pivotEdgeServer{incident: map[string][]knowledgev1.Edge{
			"hot": {
				{FromId: "hot", ToId: "a", Type: "rel"},
				{FromId: "hot", ToId: "b", Type: "rel"},
				{FromId: "hot", ToId: "c", Type: "rel"},
			},
		}}
		got, err := DrainPivotEdges([]string{"hot"}, 10, edgeCap, srv.fetch)
		require.Error(t, err, "a saturated single pivot cannot be served completely")
		assert.Contains(t, err.Error(), "hot", "the error names the pivot the caller must deal with")
		assert.Nil(t, got, "no partial union is handed back alongside the error")
	})
}
