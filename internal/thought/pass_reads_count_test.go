// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// pass_reads_count_test.go pins the per-pass read COUNTS by equality, and pins the
// residual leg that keeps the memo exact rather than merely cheap. The counting
// recorder WRAPS reflectEquivFake (corpus_equivalence_test.go) rather than replacing
// it, so the fixture stays the real reflect surface and the numbers below describe a
// real pass rather than a stub.

// corpusNodesSlice returns thoughts + charges + sessions — exactly the three types
// corpusNodeTypes covers (loop_corpus.go) — so cacheFromNodes yields a cache with the
// same TYPE COVERAGE the production delta drain gives. Seeding thoughts alone leaves
// ChargeSnapshot warm-but-empty (it reports cold only for a WHOLLY empty cache),
// which silently disables the tension path these counts are meant to observe AND
// pushes the charge and session hydrates onto their residual wire reads.
func (f *reflectEquivFake) corpusNodesSlice() []*knowledgev1.Node {
	out := f.thoughtNodesSlice()
	for _, c := range f.charges {
		out = append(out, cloneNode(c))
	}
	for _, s := range f.sessions {
		out = append(out, cloneNode(s))
	}
	return out
}

// countingWireFake classifies every request BEFORE delegating to the embedded
// reflectEquivFake, so each read the pass issues lands in exactly one counter. The
// counters are deliberately narrow: an unrecognized read (the EdgeEvidencedBy walk,
// the tension edge read, the topic-doc browse, the by-id watermark reads) is counted
// nowhere rather than smeared into a neighboring bucket.
//
// extra serves ids the fixture does not hold — the non-corpus stand-in the residual
// gate hydrates.
type countingWireFake struct {
	*reflectEquivFake
	extra map[string]*knowledgev1.Node

	mu                    sync.Mutex
	adjacencyEdgeReads    int
	kgContainsEdgeReads   int
	chargedByThoughtReads int
	chargedByChargeReads  int
	corpusNodeHydrates    int
	chargeTypeBrowses     int
	hydrateIDs            [][]string
}

func (f *countingWireFake) Execute(ctx context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	if q := req.GetQuery(); q != nil {
		f.classify(q)
	}
	resp, err := f.reflectEquivFake.Execute(ctx, req)
	if err != nil {
		return resp, err
	}
	q := req.GetQuery()
	if q == nil || q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_EDGES {
		return resp, nil
	}
	for _, id := range q.GetIds() {
		if n, ok := f.extra[id]; ok {
			resp.Nodes = append(resp.Nodes, n)
		}
	}
	return resp, nil
}

// classify buckets one query plan. Edge reads are keyed by the requested edge-type
// set; the EdgeChargedBy read is split by PIVOT — thought ids (the per-thought charge
// map) versus charge ids (the tension universe's own read, wire_tensions.go) — so the
// two are never mistaken for duplicates of each other.
func (f *countingWireFake) classify(q *knowledgev1.QueryPlan) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ids := q.GetIds()
	if q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_EDGES {
		switch types := q.GetSelection().GetEdgeTypes(); {
		case sameEdgeSet(types, adjacencyEdgeTypes):
			f.adjacencyEdgeReads++
		case sameEdgeSet(types, []kgtypes.EdgeType{kgtypes.EdgeKGContains}):
			f.kgContainsEdgeReads++
		case sameEdgeSet(types, []kgtypes.EdgeType{kgtypes.EdgeChargedBy}):
			if f.allOfType(ids, kgtypes.NodeCharge) {
				f.chargedByChargeReads++
			} else {
				f.chargedByThoughtReads++
			}
		}
		return
	}
	if len(ids) == 0 {
		if kgtypes.NodeType(q.GetSelection().GetNodeType()) == kgtypes.NodeCharge {
			f.chargeTypeBrowses++
		}
		return
	}
	f.hydrateIDs = append(f.hydrateIDs, append([]string(nil), ids...))
	if f.allCorpusTyped(ids) {
		f.corpusNodeHydrates++
	}
}

// allOfType reports whether every id names a node of the given corpus type in the
// fixture. An empty id list is false so it can never select a branch vacuously.
func (f *countingWireFake) allOfType(ids []string, want kgtypes.NodeType) bool {
	if len(ids) == 0 {
		return false
	}
	for _, id := range ids {
		if f.typeOfID(id) != want {
			return false
		}
	}
	return true
}

// allCorpusTyped reports whether every id names a thought, charge or session — the
// three types the resident cache covers.
func (f *countingWireFake) allCorpusTyped(ids []string) bool {
	if len(ids) == 0 {
		return false
	}
	for _, id := range ids {
		switch f.typeOfID(id) {
		case kgtypes.NodeThought, kgtypes.NodeCharge, kgtypes.NodeThoughtSession:
		default:
			return false
		}
	}
	return true
}

func (f *countingWireFake) typeOfID(id string) kgtypes.NodeType {
	if n := f.reflectEquivFake.nodeByID(id); n != nil {
		return kgtypes.NodeType(n.GetType())
	}
	if n, ok := f.extra[id]; ok {
		return kgtypes.NodeType(n.GetType())
	}
	return ""
}

// sameEdgeSet compares a request's edge-type strings against an expected set,
// order-independently.
func sameEdgeSet(got []string, want []kgtypes.EdgeType) bool {
	if len(got) != len(want) {
		return false
	}
	wantSet := make(map[string]bool, len(want))
	for _, et := range want {
		wantSet[string(et)] = true
	}
	for _, g := range got {
		if !wantSet[g] {
			return false
		}
	}
	return true
}

func (f *countingWireFake) counts() (adjacency, kgContains, chargedByThought, chargedByCharge, hydrates, chargeBrowses int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.adjacencyEdgeReads, f.kgContainsEdgeReads, f.chargedByThoughtReads,
		f.chargedByChargeReads, f.corpusNodeHydrates, f.chargeTypeBrowses
}

// TestPassReads_ResidualHydrateCoversUncachedIDs is the T2-1 gate on the residual
// leg — the leg that makes memoCorpusNodes EXACT rather than merely cheap. A memo
// that simply DROPPED ids the cache cannot cover would pass every other gate in this
// package (the equivalence fixture holds no finding or research node, so its residual
// is empty by construction), while in production it would make every charged FINDING
// silently vanish from the tension universe: charge parents are legitimately findings
// and research nodes (tensionClaimTypes, wire_tensions.go).
//
// BOTH halves are required. Presence alone would pass a memo that ignored the cache
// and hydrated everything; the call-shape assertion alone would pass a memo that
// hydrated nothing.
func TestPassReads_ResidualHydrateCoversUncachedIDs(t *testing.T) {
	ctx := context.Background()

	const thoughtID, findingID = "t-resident", "f-uncached"
	thought := &knowledgev1.Node{Id: thoughtID, Type: string(kgtypes.NodeThought)}
	finding := &knowledgev1.Node{Id: findingID, Type: string(kgtypes.NodeFinding)}

	gc := &countingWireFake{
		reflectEquivFake: newReflectEquivFake(equivCorpusSpec{}),
		extra:            map[string]*knowledgev1.Node{findingID: finding},
	}
	// The cache holds ONLY the thought, so the finding is the whole residual.
	loop := equivLoop(gc, cacheFromNodes([]*knowledgev1.Node{thought}))

	got := memoCorpusNodes(ctx, gc, []string{thoughtID, findingID}, newPassReads(loop))

	require.Contains(t, got, thoughtID, "the resident thought is served from the cache")
	require.Contains(t, got, findingID,
		"the NON-CORPUS id must still be hydrated — dropping it is how a charged finding "+
			"silently vanishes from the tension universe")

	gc.mu.Lock()
	hydrates := append([][]string(nil), gc.hydrateIDs...)
	gc.mu.Unlock()
	require.Len(t, hydrates, 1, "exactly ONE wire hydrate: the residual, not the whole id set")
	assert.Equal(t, []string{findingID}, hydrates[0],
		"the hydrate carries ONLY the residual id — the cached thought never reaches the wire")
}

// TestPassReads_OneReadPerKindPerPass pins the per-pass read counts BY EQUALITY over
// a REAL runPass against a warm three-type cache.
//
// WHAT EACH NUMBER CATCHES. The three ==1 legs catch a consumer that was not
// re-pointed at the memo. corpusNodeHydrates==0 is the BROAD catcher: it fires for
// any hydrate site left on the wire (the tension-parent hydrate included) and for the
// general case of passReads failing to forward ChargeSnapshot, since unforwarded
// charges fall to the residual wire read. chargeTypeBrowses==0 is NARROWER and
// catches exactly one thing nothing else does: charges served internally while
// fetchTensionChargeSet's ChargeCorpusSource type-assert still fails, which drains the
// ENTIRE charge type-browse with every hydrate count staying clean.
//
// IF AN OBSERVED COUNT DIFFERS, DO NOT RELAX THE ASSERTION — find the un-memoized
// call site. The only legitimate reason to change a number is a read this ticket
// deliberately does not memoize, and such a change must name that call site here.
//
// FIXTURE SCOPE: this corpus's charge parents are all thoughts, so hydrates==0 is a
// claim about THIS fixture. In production a finding/research charge parent is
// hydrated by the residual leg, by design — which the residual gate above pins.
func TestPassReads_OneReadPerKindPerPass(t *testing.T) {
	spec := defaultEquivSpec()
	base := newReflectEquivFake(spec)
	gc := &countingWireFake{reflectEquivFake: base}
	loop := equivLoop(gc, cacheFromNodes(base.corpusNodesSlice()))

	// The real pass body. gc is not a reflectProbe, so the quiet-tick gate never
	// skips; with no corpusScanner wired refreshCorpusCache is a no-op over the
	// pre-seeded cache.
	_, err := loop.runPass(context.Background(), false)
	require.NoError(t, err)

	adjacency, kgContains, chargedByThought, chargedByCharge, hydrates, chargeBrowses := gc.counts()

	assert.Equal(t, 1, adjacency, "ONE full-corpus adjacency edge read per pass")
	assert.Equal(t, 1, kgContains, "ONE session-membership EdgeKGContains read per pass")
	assert.Equal(t, 1, chargedByThought, "ONE thought-pivot charge map per pass")
	assert.Equal(t, 0, hydrates, "ZERO corpus-node hydrates on a warm pass — every one is resident")
	assert.Equal(t, 0, chargeBrowses, "ZERO charge type-browses — ChargeSnapshot is forwarded through the memo")

	// KNOWN-POSITIVE CONTROL for the recorder itself: the tension universe's own
	// charge-pivot read is a DIFFERENT read that still happens, so the zeros above
	// are zeros the recorder could have seen non-zero.
	assert.Equal(t, 1, chargedByCharge,
		"the tension universe's charge-pivot read still runs — proves the recorder observes "+
			"EdgeChargedBy reads at all, so the counts above are not vacuous")
}

// TestPassReads_NoMemoRereadsControl is the known-positive control for the RECORDER:
// the same fixture, the same read, issued twice WITHOUT a memo, must record TWO
// reads; issued twice WITH one, exactly one more. A recorder that could not tell
// those apart is a recorder whose zeros mean nothing.
func TestPassReads_NoMemoRereadsControl(t *testing.T) {
	ctx := context.Background()
	spec := defaultEquivSpec()
	base := newReflectEquivFake(spec)
	gc := &countingWireFake{reflectEquivFake: base}
	loop := equivLoop(gc, cacheFromNodes(base.corpusNodesSlice()))

	// NO MEMO: the loop as src, so each call composes its own adjacency.
	_, _, err := fetchAdjacency(ctx, gc, "all", nil, loop)
	require.NoError(t, err)
	_, _, err = fetchAdjacency(ctx, gc, "all", nil, loop)
	require.NoError(t, err)

	unmemoized, _, _, _, _, _ := gc.counts()
	require.Equal(t, 2, unmemoized, "two un-memoized adjacency calls are two wire reads")

	// WITH THE MEMO: two calls, ONE further read.
	pr := newPassReads(loop)
	_, _, err = fetchAdjacency(ctx, gc, "all", nil, pr)
	require.NoError(t, err)
	_, _, err = fetchAdjacency(ctx, gc, "all", nil, pr)
	require.NoError(t, err)

	memoized, _, _, _, _, _ := gc.counts()
	assert.Equal(t, unmemoized+1, memoized,
		"two memoized adjacency calls add exactly ONE wire read")
}
