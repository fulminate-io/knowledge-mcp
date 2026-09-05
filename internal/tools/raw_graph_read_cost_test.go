// SPDX-License-Identifier: Apache-2.0

package tools

// raw_graph_read_cost_test.go measures the ROUND-TRIP COST of the raw-graph
// ranked read at two corpus sizes. It is the gate on the cost change this whole
// changeset exists to make: the read used to drain the entire document over the
// wire on every query, so its cost was linear in the document; the segment-backed
// read asks the shipped segments and costs a fixed four reads.
//
// THE EQUALITY ACROSS SIZES IS THE DISCRIMINATING LEG, NOT THE CEILING. At 500
// chunks the old drain costs THREE round trips, which is UNDER the cap — so a
// criterion asserting only a ceiling would pass the very implementation this
// phase removes, at the smaller of its two sizes. Only equality separates a
// constant-cost read from one that scales.

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/paging"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// rawReadCostCorpusSizes is the SINGLE AUTHORITATIVE DECLARATION of the two
// scales. Both the measurement and its assertion read it, so a later resize
// cannot leave a stale pinned number behind.
//
// 3,500 is derived from a measurement rather than picked: the collected Stopford
// graph holds 578 paragraph chunks across 166 pages, 3.48 per page, so a
// 1000-page document is about 3,500 chunks.
var rawReadCostCorpusSizes = []int{500, 3500}

// rawReadCostMaxRoundTrips is the ceiling the constant-cost read must sit at or
// under. It is FOUR: the collected-graph catalog read, the bulk ids[] hit
// hydrate, the CONTAINS pivot-edge read, and the bulk ids[] parent hydrate.
const rawReadCostMaxRoundTrips = 4

// rawCostHitID is the one node the ranked read returns, and rawCostSectionID its
// containing section. THE SECTION IS NEVER RANKED, so it can only reach the
// renderer through the parent hydrate — which is what lets the heading control
// below distinguish a four-read arm from a heading-dropping three-read one.
const (
	rawCostHitID     = "p0000001"
	rawCostSectionID = "p0000000-section"
	rawCostHeading   = "Retry Semantics"
)

// pagingCorpusHandler serves a raw graph FAITHFULLY, and all four parts of that
// contract are load-bearing:
//
//  1. an ids[] read answers THOSE ids;
//  2. an edges read answers the CONTAINS edges pointing at the requested ids;
//  3. a keyset browse answers ONE BOUNDED PAGE after the cursor — not the whole
//     corpus and not a single row;
//  4. a GRAPH_NAMES read answers the collected set, because the segment arm's
//     existence gate asks before it ranks.
//
// PART 3 IS THE ONE THAT DECIDES WHETHER THE MEASUREMENT IS REAL. A handler that
// returned the whole corpus to a browse would make the old drain look free; one
// that returned a single row would make it look ruinous. Serving exactly one page
// per request is what reproduces the production shape of one request per 500
// nodes.
//
// It embeds dispatchEngineHandler for the rest of the EngineService surface, so
// the fake keeps satisfying the generated interface as that interface grows.
type pagingCorpusHandler struct {
	nodes []*knowledgev1.Node // sorted by Id — the keyset browse depends on it
	byID  map[string]*knowledgev1.Node
	edges []*knowledgev1.Edge

	execCalls int
}

func (h *pagingCorpusHandler) Execute(
	_ context.Context, req *knowledgev1.ExecuteRequest,
) (*knowledgev1.ExecuteResponse, error) {
	h.execCalls++
	q := req.GetQuery()

	switch {
	case q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_GRAPH_NAMES:
		return &knowledgev1.ExecuteResponse{
			GraphNames: []*knowledgev1.GraphInfo{{Name: "doc-slug"}},
		}, nil

	case q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_EDGES:
		want := map[string]bool{}
		for _, id := range q.GetIds() {
			want[id] = true
		}
		out := make([]*knowledgev1.Edge, 0, len(h.edges))
		for _, e := range h.edges {
			if want[e.GetToId()] {
				out = append(out, e)
			}
		}
		return &knowledgev1.ExecuteResponse{Edges: out}, nil

	case len(q.GetIds()) > 0:
		out := make([]*knowledgev1.Node, 0, len(q.GetIds()))
		for _, id := range q.GetIds() {
			if n, ok := h.byID[id]; ok {
				out = append(out, n)
			}
		}
		return &knowledgev1.ExecuteResponse{Nodes: out}, nil

	default:
		// The keyset browse: ONE page strictly after the cursor.
		after := q.GetAfterId()
		start := 0
		if after != "" {
			for i, n := range h.nodes {
				if n.GetId() > after {
					start = i
					break
				}
				start = i + 1
			}
		}
		end := min(start+int(q.GetLimit()), len(h.nodes))
		if q.GetLimit() <= 0 {
			end = len(h.nodes)
		}
		return &knowledgev1.ExecuteResponse{Nodes: h.nodes[start:end]}, nil
	}
}

func (h *pagingCorpusHandler) Stats(
	_ context.Context, _ *knowledgev1.StatsRequest,
) (*knowledgev1.StatsResponse, error) {
	return &knowledgev1.StatsResponse{GraphStats: &knowledgev1.GraphStats{}}, nil
}

// rawReadCostCorpus builds exactly total nodes with SORTED ids: one section
// carrying the heading, the ranked hit it contains, and filler paragraphs whose
// bodies deliberately do NOT carry the query terms.
func rawReadCostCorpus(total int) *pagingCorpusHandler {
	nodes := make([]*knowledgev1.Node, 0, total)
	nodes = append(nodes, &knowledgev1.Node{
		Id: rawCostSectionID, Type: "section", Source: "web-collect",
		SymbolName: rawCostHeading,
	})
	nodes = append(nodes, &knowledgev1.Node{
		Id: rawCostHitID, Type: "paragraph", Source: "web-collect",
		Content:  "idempotent retries deduplicate a replayed request",
		Metadata: map[string]string{"page_first": "42"},
	})
	for i := len(nodes); i < total; i++ {
		nodes = append(nodes, &knowledgev1.Node{
			Id: fmt.Sprintf("p%07d", i+100), Type: "paragraph", Source: "web-collect",
			Content: "unrelated filler body about typography and page layout",
		})
	}
	byID := make(map[string]*knowledgev1.Node, len(nodes))
	for _, n := range nodes {
		byID[n.GetId()] = n
	}
	return &pagingCorpusHandler{
		nodes: nodes,
		byID:  byID,
		edges: []*knowledgev1.Edge{
			{FromId: rawCostSectionID, ToId: rawCostHitID, Type: string(kgtypes.EdgeContains)},
		},
	}
}

// TestRawGraphReadCost_RoundTripsAreIndependentOfCorpusSize is the performance
// gate: the ranked read must cost the SAME number of round trips at 500 chunks
// and at 3,500, and that number must sit at or under the four-read ceiling.
func TestRawGraphReadCost_RoundTripsAreIndependentOfCorpusSize(t *testing.T) {
	bySize := make(map[int]int, len(rawReadCostCorpusSizes))

	for _, size := range rawReadCostCorpusSizes {
		h := rawReadCostCorpus(size)
		require.Len(t, h.nodes, size, "the fixture must hold exactly the declared corpus size")
		mgr := &fakeSegmentSearcher{hits: []searchengine.Hit{{ID: rawCostHitID, Score: 0.9}}}
		deps := interceptTestDeps{gc: h, searcher: mgr}

		res := composeRawGraphSegmentSearch(opCtx(), deps, mgr, kgtypes.GraphType("web"), "doc-slug",
			segmentSearchArgs{Query: "idempotent retries", Mode: "text"})
		body := textBodyTools(res)

		// CONTROL ONE: a low round-trip count must not come from an early exit.
		require.Contains(t, body, rawCostHitID,
			"the ranked hit is missing at size %d — a cheap read that returns nothing is not a cheap read", size)
		// CONTROL TWO: nor from an arm that skipped the parent hydrate. Without
		// this a heading-dropping implementation greens at a LOWER count.
		require.Contains(t, body, "under: "+rawCostHeading,
			"the containing heading is missing at size %d — the parent hydrate did not run, so the count "+
				"below is that of a broken arm rather than a cheaper one", size)

		bySize[size] = h.execCalls
	}

	t.Logf("Execute round trips by corpus size: %v", bySize)

	first := rawReadCostCorpusSizes[0]
	for _, size := range rawReadCostCorpusSizes[1:] {
		assert.Equal(t, bySize[first], bySize[size],
			"ranked-read round trips scale with corpus size (%d chunks cost %d, %d chunks cost %d) — "+
				"the read is still draining the graph",
			first, bySize[first], size, bySize[size])
	}
	for _, size := range rawReadCostCorpusSizes {
		assert.LessOrEqual(t, bySize[size], rawReadCostMaxRoundTrips,
			"the ranked read costs %d round trips at %d chunks, over the %d-read ceiling",
			bySize[size], size, rawReadCostMaxRoundTrips)
	}
	// The page size the browse would have used, asserted so the sizes above stay
	// meaningfully larger than one page: a corpus that fitted in a single page
	// would cost a drain nothing and the equality leg would prove nothing.
	require.Greater(t, rawReadCostCorpusSizes[len(rawReadCostCorpusSizes)-1], paging.BrowsePageSize,
		"the larger corpus must exceed one browse page or a drain would not scale within this test")
}
