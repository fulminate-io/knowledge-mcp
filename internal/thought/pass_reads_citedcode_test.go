// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"context"
	"slices"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/enginetest"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// pass_reads_citedcode_test.go holds the SINGLE authoritative cited-code fixture
// seed, plus the two gates that consume it. It exists as its own file because
// corpus_equivalence_test.go is at 489 lines against the project's 500-line
// convention and cannot absorb it.
//
// WHY THE SEED IS NEEDED AT ALL. The equivalence fixture files every seeded edge
// under EdgeRelatesTo with NO Method and seeds no proxy nodes, while
// codeRefProxiesByThought requires Method == codeRefMethod. So against the bare
// fixture ResolveCitedCodeNodes returns an empty map, buildCitedCodeUpdatedAt
// returns an empty map, and facetCodeChanged never fires — the whole-surface
// equivalence gate compares two empty cited-code inputs and would stay green if the
// cited-code resolution were dropped entirely. This seed is what makes that surface
// observable.

const (
	// citedCodeThoughtID is 'a1' from defaultEquivSpec deliberately: the code-ref
	// edge's From must sit INSIDE the adjacency pivot set, because
	// codeRefProxiesByThought drops an edge whose From is out of scope.
	citedCodeThoughtID = "a1"
	// citedCodeUncitedID is a corpus thought carrying no code-ref edge — the
	// negative half of the facet gate.
	citedCodeUncitedID = "b1"

	citedCodeProxyID = "proxy:knowledge:pkg/file.go:Sym"
	citedCodeNodeID  = "pkg/file.go:Sym"
	citedCodeRepo    = "knowledge"
)

// citedCodeUpdatedAtNanos is the cited code node's UpdatedAt, and
// citedCodeChargeNanos is the CreatedAt stamped on every fixture charge.
//
// THE COMPARAND IS THE NEWEST CHARGE TIME, NOT THE THOUGHT'S UpdatedAt.
// facetCodeChanged (blindspots.go) fires on
// `newest, ok := newestChargeTime(thoughtCharges); ok && citedCodeUpdatedAt > newest.UnixNano()`.
// Two consequences, both load-bearing and both easy to get wrong:
//   - citedCodeUpdatedAtNanos must exceed the newest CHARGE CreatedAt. Beating the
//     thought's UpdatedAt is irrelevant — the thought's timestamp is not consulted.
//   - newestChargeTime returns ok=false when every charge has CreatedAt==0, which
//     is how the bare equivalence fixture leaves them (corpus_equivalence_test.go
//     stamps no CreatedAt on charges). Under ok=false the facet cannot fire AT ALL,
//     so a cited-code regression would be invisible to any comparison of the
//     resulting report. Stamping the charges is what makes this fixture non-vacuous.
const (
	citedCodeUpdatedAtNanos int64 = 1_700_000_000_000_000_000
	citedCodeChargeNanos    int64 = 1_699_000_000_000_000_000
)

// citedCodeSeed is the thought--relates-to(Method=code-ref)-->proxy--(repo +
// foreign_id)-->code-node chain, seeded once and layered onto reflectEquivFake.
type citedCodeSeed struct {
	edges     []*knowledgev1.Edge
	proxies   []*knowledgev1.Node
	codeNodes []*knowledgev1.Node
}

// newCitedCodeSeed builds the chain for one thought. The proxy comes from
// mkCodeProxy (cited_code_staleness_test.go), which stamps foreign_graph /
// foreign_id / repo the way BuildCrossGraphProxy does — the keys are never
// hand-stamped here.
func newCitedCodeSeed(thoughtID string) *citedCodeSeed {
	return &citedCodeSeed{
		edges: []*knowledgev1.Edge{{
			FromId: thoughtID,
			ToId:   citedCodeProxyID,
			Type:   string(kgtypes.EdgeRelatesTo),
			Method: codeRefMethod,
		}},
		proxies: []*knowledgev1.Node{mkCodeProxy(citedCodeProxyID, citedCodeRepo, citedCodeNodeID)},
		codeNodes: []*knowledgev1.Node{{
			Id:        citedCodeNodeID,
			Type:      "function",
			UpdatedAt: citedCodeUpdatedAtNanos,
			Content:   "func Sym() {}",
		}},
	}
}

// answer dispatches on the same three request shapes citedCodeFake distinguishes
// (cited_code_staleness_test.go): a RETURN_MODE_EDGES query yields the code-ref
// edges, a request targeting the CODE graph yields the code nodes, and an ids[]
// request yields the proxy nodes. It returns the seed's CONTRIBUTION rather than a
// whole response, because this seed LAYERS onto reflectEquivFake (which owns the
// corpus) instead of being the whole wire the way citedCodeFake is.
//
// The one layering delta from that dispatch: the proxy arm fires only for a request
// that actually ASKS for a seeded proxy. citedCodeFake can answer every other ids[]
// request with proxies because it serves nothing else; here the same blanket arm
// would staple proxy nodes onto every corpus hydrate.
func (s *citedCodeSeed) answer(req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, bool) {
	q := req.GetQuery()
	if q == nil {
		return nil, false
	}
	if q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_EDGES {
		return &knowledgev1.ExecuteResponse{Edges: bandNarrow(s.edges, q)}, true
	}
	if req.GetTarget().GetGraph() == string(kgtypes.GraphCode) {
		return enginetest.ResponseWithNodes(s.codeNodes...), true
	}
	if slices.Contains(q.GetIds(), citedCodeProxyID) {
		return enginetest.ResponseWithNodes(s.proxies...), true
	}
	return nil, false
}

// citedCodeWireFake is reflectEquivFake with the cited-code chain layered on: the
// inner fake answers first (it owns the corpus), then the seed's contribution is
// appended. Appending rather than replacing is what keeps the edge arm additive —
// the code-ref edges join whatever adjacency edges the corpus already returned.
type citedCodeWireFake struct {
	*reflectEquivFake
	seed *citedCodeSeed
}

func (f *citedCodeWireFake) Execute(ctx context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	resp, err := f.reflectEquivFake.Execute(ctx, req)
	if err != nil {
		return resp, err
	}
	seeded, ok := f.seed.answer(req)
	if !ok {
		return resp, nil
	}
	if resp == nil {
		resp = &knowledgev1.ExecuteResponse{}
	}
	resp.Edges = append(resp.Edges, seeded.GetEdges()...)
	resp.Nodes = append(resp.Nodes, seeded.GetNodes()...)
	return resp, nil
}

// newCitedCodeFixture builds a FRESH fake + loop pair over the cited-code-seeded
// default corpus. Each caller gets its own instance so no run's metadata writeback
// can leak into another's.
func newCitedCodeFixture() (*citedCodeWireFake, *PropagationLoop) {
	base := newReflectEquivFake(defaultEquivSpec())
	// STAMP BEFORE SNAPSHOTTING. corpusNodesSlice clones the charges (cloneNode) and
	// cacheFromNodes merges those clones into the resident cache, so a stamp applied
	// after this line would reach the wire side only and diverge the two runs.
	for _, c := range base.charges {
		c.CreatedAt = citedCodeChargeNanos
	}
	gc := &citedCodeWireFake{reflectEquivFake: base, seed: newCitedCodeSeed(citedCodeThoughtID)}
	return gc, equivLoop(gc, cacheFromNodes(base.corpusNodesSlice()))
}

// citedCodeFacetItems returns the items of the named facet in a report, or nil when
// the facet is absent.
func citedCodeFacetItems(r BlindSpotReport, key string) []BlindSpotItem {
	for _, f := range r.Facets {
		if f.Key == key {
			return f.Items
		}
	}
	return nil
}

// TestPassReadsEquivalence_CitedCodeFacetFires is the ANTI-VACUITY GATE for the
// fixture itself. If the seed's Method is not exactly "code-ref", or the proxy is
// missing foreign_graph / repo / foreign_id, or the code-graph Target dispatch is
// wrong, this test goes red and nothing else in the suite would. Without it the
// memo-vs-fresh comparison below would compare two EMPTY maps and report success.
func TestPassReadsEquivalence_CitedCodeFacetFires(t *testing.T) {
	ctx := context.Background()
	gc, loop := newCitedCodeFixture()

	// THE MEMO, not nil. The cited-code chain now serves its relates-to edges from
	// the unified pivot read, and passing nil here would leave that path unexercised
	// by anything in this file — the fixture would still resolve, via the narrow read
	// it no longer takes in a real pass.
	got := buildCitedCodeUpdatedAt(ctx, gc, gc.thoughtIDs, newPassReads(loop))

	require.NotEmpty(t, got, "the seeded cited-code chain must resolve to a non-empty map")
	// The VALUE, not merely presence: buildCitedCodeUpdatedAt omits a thought whose
	// newest cited UpdatedAt is 0, so asserting the timestamp is what distinguishes a
	// fully resolved chain from a half-resolved one that folded in a zero.
	assert.Equal(t, citedCodeUpdatedAtNanos, got[citedCodeThoughtID],
		"the seeded thought resolves to the seeded code node's UpdatedAt")
	// The NEGATIVE half: a seed that leaked its code node onto every thought would
	// satisfy the positive assertion alone.
	assert.NotContains(t, got, citedCodeUncitedID,
		"a corpus thought with no code-ref edge is ABSENT from the map")
}

// TestPassReadsEquivalence_CitedCodeMemoEqualsFresh is a CHARACTERIZATION GUARD over
// today's behavior, landed BEFORE the read collapse so that change is made against a
// gate that already holds. It is green on landing and is NOT a red-first
// reproduction.
//
// The blind-spot report is the exact surface: buildCitedCodeUpdatedAt has exactly
// one non-test caller, computeBlindSpots (loop_detection.go), so nothing else in the
// reflect surface consumes citedCodeUpdatedAt. The whole-surface comparison stays
// covered by the pre-existing TestPassReadsEquivalence_MemoEqualsFresh, which this
// does not replace.
func TestPassReadsEquivalence_CitedCodeMemoEqualsFresh(t *testing.T) {
	ctx := context.Background()

	// DISTINCT instances: neither run's metadata writeback can reach the other.
	freshGC, freshLoop := newCitedCodeFixture()
	memoGC, memoLoop := newCitedCodeFixture()

	// src = the loop itself → every stage composes its own reads (today's behavior).
	fresh := citedCodeBlindSpots(t, ctx, freshGC, freshLoop, freshLoop)
	// src = the per-pass memo → reads are served once and shared.
	memo := citedCodeBlindSpots(t, ctx, memoGC, memoLoop, newPassReads(memoLoop))

	// THE NON-VACUITY PRECONDITION, asserted before the equality. The equality below
	// only constrains the cited-code path if that path REACHES the report at all, and
	// facetCodeChanged silently produces nothing when newestChargeTime reports
	// ok=false. Without this leg, two reports that both omit the facet entirely
	// compare equal and the gate passes while testing nothing — which is exactly the
	// state this fixture was in before the charges carried a CreatedAt.
	items := citedCodeFacetItems(fresh, facetCodeChanged)
	require.NotEmpty(t, items,
		"facetCodeChanged must be PRESENT in the report — the facet is the observable the "+
			"equality below compares, and it cannot fire while every fixture charge has CreatedAt==0")
	found := false
	for _, it := range items {
		if it.ThoughtID == citedCodeThoughtID {
			found = true
		}
	}
	require.True(t, found,
		"the seeded thought carries the code_changed facet — the cited-code resolution "+
			"reached the report rather than merely resolving in isolation")

	require.Equal(t, fresh, memo,
		"the blind-spot report is identical memoized vs re-reading, over a fixture whose "+
			"cited code actually resolves")
}

// citedCodeBlindSpots runs the blind-spot path the loop runs, with the node set
// sourced through src. The ids are SORTED before the call for the reason
// corpus_equivalence_test.go already gives: cache iteration order is random, and
// sorting removes ORDER as a variable while leaving any genuine SET divergence
// visible.
func citedCodeBlindSpots(t *testing.T, ctx context.Context, gc Caller, p *PropagationLoop, src CorpusSource) BlindSpotReport {
	t.Helper()

	clusters, err := DetectPersistedClusters(ctx, gc, src)
	require.NoError(t, err)

	nodeIDs, _, err := fetchAdjacency(ctx, gc, "all", nil, src)
	require.NoError(t, err)

	sortedIDs := append([]string(nil), nodeIDs...)
	sort.Strings(sortedIDs)

	return p.computeBlindSpots(ctx, sortedIDs, clusters, src)
}
