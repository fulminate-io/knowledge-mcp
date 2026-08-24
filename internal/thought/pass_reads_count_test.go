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
	"github.com/fulminate-io/knowledge-mcp/internal/paging"
)

// pass_reads_count_test.go pins the per-pass read COUNTS by equality, and pins the
// residual leg that keeps the memo exact rather than merely cheap. The counting
// recorder WRAPS reflectEquivFake (corpus_equivalence_test.go) rather than replacing
// it, so the fixture stays the real reflect surface and the numbers below describe a
// real pass rather than a stub.

// TestPassReads_RecorderCountsUnclassifiedReads is the KNOWN-POSITIVE CONTROL for the
// default bucket. The zero this counter reports in the pass gates is only meaningful
// if the counter can go non-zero at all — and before the default arm existed it could
// not, so a read over an unclassified type set was invisible to every equality leg.
func TestPassReads_RecorderCountsUnclassifiedReads(t *testing.T) {
	ctx := context.Background()
	base := newReflectEquivFake(defaultEquivSpec())
	gc := &countingWireFake{reflectEquivFake: base}

	require.Equal(t, 0, gc.otherReads(), "no unclassified read has happened yet")

	// A type set no bucket classifies. EdgeSupports and EdgeInformedBy are both
	// outside unifiedPivotEdgeTypes and outside every narrow bucket.
	_, err := fetchEdgesForNodeSet(ctx, gc, base.thoughtIDs,
		[]kgtypes.EdgeType{kgtypes.EdgeSupports, kgtypes.EdgeInformedBy})
	require.NoError(t, err)

	assert.Equal(t, 1, gc.otherReads(),
		"an unclassified edge read must land in the default bucket — otherwise it is "+
			"invisible to every equality leg that asserts a zero")
}

// TestPassReads_RecorderSeesRelatesToReads is the KNOWN-POSITIVE CONTROL for the
// relates-to counter: two un-memoized cited-code resolutions over the same fixture
// must record TWO reads. Without it a later zero on this counter would be
// indistinguishable from a counter that never fires at all.
func TestPassReads_RecorderSeesRelatesToReads(t *testing.T) {
	ctx := context.Background()
	base := newReflectEquivFake(defaultEquivSpec())
	gc := &countingWireFake{reflectEquivFake: base}

	// THE nil SOURCE IS THE POINT, and it must stay nil. This is the control that
	// proves the counter can observe relates-to reads at all, so it has to be the
	// UN-MEMOIZED path: handed the per-pass memo instead, the second call would be
	// served from the unified pivot read, the counter would read 1, and the failure
	// would look like a broken recorder rather than a disarmed control.
	ResolveCitedCodeNodes(ctx, gc, base.thoughtIDs, nil)
	ResolveCitedCodeNodes(ctx, gc, base.thoughtIDs, nil)

	assert.Equal(t, 2, gc.relatesToReads(),
		"two un-memoized cited-code resolutions are two relates-to wire reads")
}

// edgeTypeRecorder records the requested edge-type filter of every
// RETURN_MODE_EDGES request that reaches the wire, so a test can assert on WHETHER A
// WIRE CALL HAPPENED rather than on what came back. That distinction is the whole
// point for memoTypedEdges: a memo serving its full 7-type set also returns a
// non-empty slice, so a non-empty return proves nothing about which path ran.
// It also records the PIVOT COUNT of each such request (pivotCounts), which is what
// lets a test assert the page SEQUENCE a drain produced rather than just how many
// calls it made — a drain splitting the same ids into the wrong-sized pages issues
// the same number of calls for some inputs.
type edgeTypeRecorder struct {
	*reflectEquivFake

	mu          sync.Mutex
	edgeCalls   [][]string
	pivotCounts []int
}

func (f *edgeTypeRecorder) Execute(ctx context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	if q := req.GetQuery(); q != nil && q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_EDGES {
		f.mu.Lock()
		f.edgeCalls = append(f.edgeCalls, append([]string(nil), q.GetSelection().GetEdgeTypes()...))
		f.pivotCounts = append(f.pivotCounts, len(q.GetIds()))
		f.mu.Unlock()
	}
	return f.reflectEquivFake.Execute(ctx, req)
}

func (f *edgeTypeRecorder) takeEdgeCalls() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	calls := f.edgeCalls
	f.edgeCalls = nil
	return calls
}

func (f *edgeTypeRecorder) takePivotCounts() []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	counts := f.pivotCounts
	f.pivotCounts = nil
	return counts
}

// TestMemoTypedEdges_FallsThroughOnUncarriedType proves the type-superset check
// ACTS rather than merely existing. Every consumer this ticket wires asks only for
// types the unified read carries, so a missing superset check would pass every other
// gate in the package while serving a FUTURE caller a silently short answer — the
// unified read holds no EdgeSupports edge, so a memo that answered the request would
// report zero of them and look like a clean empty result.
//
// The CARRIED leg is the known-positive control: without it, "a wire call happened"
// would be equally true of a memo that never serves anything at all.
func TestMemoTypedEdges_FallsThroughOnUncarriedType(t *testing.T) {
	ctx := context.Background()
	base := newReflectEquivFake(defaultEquivSpec())
	gc := &edgeTypeRecorder{reflectEquivFake: base}
	pr := newPassReads(equivLoop(gc, cacheFromNodes(base.corpusNodesSlice())))

	// Populate the unified memo so a served request is genuinely possible.
	_, err := memoPivotEdges(ctx, gc, base.thoughtIDs, pr)
	require.NoError(t, err)
	require.Len(t, gc.takeEdgeCalls(), paging.EdgeBandCount,
		"the unified read is ONE logical read, issued as exactly EdgeBandCount banded wire calls")

	// CARRIED type → served from the memo, NO wire call.
	_, err = memoTypedEdges(ctx, gc, base.thoughtIDs, []kgtypes.EdgeType{kgtypes.EdgeKGContains}, pr)
	require.NoError(t, err)
	assert.Empty(t, gc.takeEdgeCalls(),
		"a type the unified read CARRIES is served from the memo — no wire call")

	// UNCARRIED type → falls through to the narrow read.
	_, err = memoTypedEdges(ctx, gc, base.thoughtIDs, []kgtypes.EdgeType{kgtypes.EdgeSupports}, pr)
	require.NoError(t, err)
	calls := gc.takeEdgeCalls()
	require.Len(t, calls, 1,
		"a type OUTSIDE unifiedPivotEdgeTypes must reach the wire, not be served short from the memo")
	assert.Equal(t, []string{string(kgtypes.EdgeSupports)}, calls[0],
		"the fall-through issues the exact narrow read it replaces")
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
// THE THREE ONES BECAME ZEROS, AND HERE IS THE CALL SITE THAT MOVED THEM, named as
// the rule above requires. The adjacency read (wire_adjacency.go), the
// session-membership read (deriveSessionSiblings via memoKGContainsEdges) and the
// thought-pivot charge read (fetchChargesUncached) pivoted on the IDENTICAL thought
// id set and differed only by edge-type filter. They are now served from ONE unified
// read over unifiedPivotEdgeTypes (memoPivotEdges), so they no longer exist as
// separate wire calls. unifiedPivotEdgeReads is where that ONE LOGICAL read is
// counted; the three zeros are the evidence the collapse landed, NOT a relaxation.
// The equality form is deliberately intact — an inequality here would stop catching
// a consumer that quietly re-acquired its own read.
//
// THE COUNTERS COUNT EXECUTE ROUND-TRIPS, NOT LOGICAL READS, and since the unified
// read became a BANDED sweep one logical read is exactly paging.EdgeBandCount
// Executes. That equality is a property of the shipped code rather than of this
// fixture's size: EdgeBandBoundaries always returns n-1 boundaries (duplicating when
// the id list is shorter rather than emitting fewer) and the drain walks every band
// including empty ones. Asserting the CONSTANT rather than a literal is deliberate —
// a literal here and a changed constant in the package drift apart silently. Exact
// equality also doubles as a guard that no band saturated and split.
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

	assert.Equal(t, paging.EdgeBandCount, gc.unifiedReads(),
		"ONE unified full-corpus edge sweep per pass, measurable as exactly EdgeBandCount banded Executes and no more")
	assert.Equal(t, 0, adjacency, "the separate adjacency edge read is GONE — folded into the unified read")
	assert.Equal(t, 0, kgContains, "the separate EdgeKGContains read is GONE — folded into the unified read")
	assert.Equal(t, 0, chargedByThought, "the separate thought-pivot charge read is GONE — folded into the unified read")
	assert.Equal(t, 0, gc.relatesToReads(), "the separate cited-code relates-to read is GONE — folded into the unified read")
	assert.Equal(t, 0, gc.otherReads(),
		"ZERO edge reads over an unclassified type set — the default bucket is what keeps "+
			"the zeros above from being satisfied by a read no counter can see")
	assert.Equal(t, 0, hydrates, "ZERO corpus-node hydrates on a warm pass — every one is resident")
	assert.Equal(t, 0, chargeBrowses, "ZERO charge type-browses — ChargeSnapshot is forwarded through the memo")

	// KNOWN-POSITIVE CONTROL for the recorder itself: the tension universe's own
	// charge-pivot read is a DIFFERENT read that still happens, so the zeros above
	// are zeros the recorder could have seen non-zero.
	assert.Equal(t, paging.EdgeBandCount, chargedByCharge,
		"the tension universe's own charge read still runs — banded now, and the recorder tells it "+
			"from the per-thought one by the BAND rather than the pivot type. Proves the recorder "+
			"observes EdgeChargedBy reads at all, so the counts above are not vacuous")
}

// TestPassReads_OnePivotEdgeReadPerPass IS THE COLLAPSE GATE: a real pass issues ONE
// unified pivot-edge read, ZERO of the four narrow reads it replaced, and STILL
// resolves cited code.
//
// THE FIVE ZEROS ARE ONLY MEANINGFUL BECAUSE OF THE TWO LEGS AROUND THEM. Five zeros
// on their own are equally satisfied by a pass that stopped doing the work — which is
// exactly what must not ship. So:
//   - chargedByCharge==1 is the KNOWN-POSITIVE CONTROL: the tension universe's
//     charge-pivot read is a DIFFERENT pivot, outside this collapse, and still runs.
//     It proves the recorder observes edge reads at all, so the zeros are zeros it
//     could have seen non-zero.
//   - the cited-code assertion is the WORK-STILL-HAPPENED leg: the pass must still
//     resolve the seeded thought's code node, so citedCodeRelatesToReads==0 means
//     "served from the unified read" rather than "no longer looked".
//
// TestPassReads_RecorderSeesRelatesToReads is the third leg, proving that particular
// counter can be non-zero at all.
func TestPassReads_OnePivotEdgeReadPerPass(t *testing.T) {
	base := newReflectEquivFake(defaultEquivSpec())
	for _, c := range base.charges {
		c.CreatedAt = citedCodeChargeNanos // so facetCodeChanged can fire (see the seed).
	}
	gc := &countingWireFake{
		reflectEquivFake: base,
		citedCode:        newCitedCodeSeed(citedCodeThoughtID),
	}
	loop := equivLoop(gc, cacheFromNodes(base.corpusNodesSlice()))

	_, err := loop.runPass(context.Background(), false)
	require.NoError(t, err)

	adjacency, kgContains, chargedByThought, chargedByCharge, _, _ := gc.counts()

	assert.Equal(t, paging.EdgeBandCount, gc.unifiedReads(),
		"ONE unified full-corpus edge sweep for the whole pass, issued as exactly EdgeBandCount banded Executes")
	assert.Equal(t, 0, adjacency, "no separate adjacency read")
	assert.Equal(t, 0, kgContains, "no separate session-membership read")
	assert.Equal(t, 0, chargedByThought, "no separate thought-pivot charge read")
	assert.Equal(t, 0, gc.relatesToReads(), "no separate cited-code relates-to read")
	assert.Equal(t, 0, gc.otherReads(),
		"no edge read over an unclassified type set — closes the gap where a read with a "+
			"type set no bucket matches would leave every zero above green")

	assert.Equal(t, paging.EdgeBandCount, chargedByCharge,
		"the tension universe's own charge read still runs — a DIFFERENT read, outside this "+
			"collapse, so the five zeros above are not vacuous")

	// WORK STILL HAPPENED: the cited code is still resolved, from the unified read.
	cited := buildCitedCodeUpdatedAt(context.Background(), gc, base.thoughtIDs, newPassReads(loop))
	assert.Equal(t, citedCodeUpdatedAtNanos, cited[citedCodeThoughtID],
		"cited code is STILL resolved — the zero above means served-from-the-memo, "+
			"not stopped-looking")
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

	// The counter READ HERE IS unifiedReads(), not the adjacency bucket: scope="all"
	// now issues the 7-type unified read, which classify buckets as unified. The
	// adjacency bucket counts the old 5-type shape and is legitimately 0 throughout,
	// so watching it would make this control silently compare 0 against 0.
	//
	// NO MEMO: the loop as src, so each call composes its own adjacency.
	_, _, err := fetchAdjacency(ctx, gc, "all", nil, loop)
	require.NoError(t, err)
	_, _, err = fetchAdjacency(ctx, gc, "all", nil, loop)
	require.NoError(t, err)

	unmemoized := gc.unifiedReads()
	require.Equal(t, 2*paging.EdgeBandCount, unmemoized,
		"two un-memoized adjacency calls are two banded sweeps, EdgeBandCount Executes each")

	// WITH THE MEMO: two calls, ONE further read.
	pr := newPassReads(loop)
	_, _, err = fetchAdjacency(ctx, gc, "all", nil, pr)
	require.NoError(t, err)
	_, _, err = fetchAdjacency(ctx, gc, "all", nil, pr)
	require.NoError(t, err)

	memoized := gc.unifiedReads()
	assert.Equal(t, unmemoized+paging.EdgeBandCount, memoized,
		"two memoized adjacency calls add exactly ONE logical read — EdgeBandCount Executes, not one; "+
			"the +EdgeBandCount reads as relative but is a LOGICAL-read count expressed in Execute units")
}
