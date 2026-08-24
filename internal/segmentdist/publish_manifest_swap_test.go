// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/bm25"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/hnsw"
	"github.com/fulminate-io/knowledge-mcp/internal/tools"
)

// publish_manifest_swap_test.go gates the manifest swap COMPLETING: a rebuild over
// a graph that already holds a prior layer must publish, retire what it superseded,
// and leave a durable watermark behind.
//
// IT DRIVES THE REAL DRIVER. tools.RebuildSegments is the production rebuild flow —
// scan, group, add, seal, finalize, advance — and *Manager satisfies its
// SegmentShipper seam, so the test wires the two together over the in-package fake
// segment source. The import direction is legal (tools does not import segmentdist)
// and it is the only way to gate the watermark leg and the per-format resident leg
// in ONE run: the tools package cannot reach a non-disarmed source (its only
// in-package Manager option is the production factory, whose not-logged-in branch is
// the L2-local source that disarms the ratio unconditionally), and this package
// cannot reach the watermark advance without the driver.

// swapCorpusN is the TRUE corpus the rebuild rebuilds — a fixture CONSTANT, so every
// expectation below is derived from what the fixture put in rather than from
// anything the engine reported back.
const swapCorpusN = 2048

// swapPriorLayers is how many FULL prior corpus layers the fixture publishes into
// the manifest before the rebuild runs. Two layers is the incident's shape: the
// graph had accumulated a second copy of every document because no swap had ever
// retired the first.
const swapPriorLayers = 2

// swapServedHorizon is the horizon the fake server serves the scan. It is
// deliberately a value no clock would produce, so the watermark assertion catches a
// driver that stamped a reading of its own instead of the served horizon.
const swapServedHorizon = int64(1_700_000_000_123_456_789)

// swapWriterID is the single writer every engine and every seeded layer in this
// fixture publishes under — the manifest is per (target, writer, format), so a
// seeded layer under a different writer would not be in the denominator the
// rebuild's publish is gated on.
const swapWriterID = "00000000000000a1"

// The two format tags the per-format legs iterate, taken from the Formats
// themselves so a rename cannot leave the fixture asserting against a stale literal.
var (
	hnswFormatName = hnsw.New().Name()
	bm25FormatName = bm25.New().Name()
)

// swapScanner serves ONE page of n live items and then an empty page, echoing
// swapServedHorizon on every response. It records the watermarks it was asked for so
// a delta-scoped second run can be distinguished from a full re-scan.
type swapScanner struct {
	mu         sync.Mutex
	page       []*knowledgev1.PipelineScanItem
	served     bool
	watermarks []int64
}

func newSwapScanner(n int) *swapScanner {
	page := make([]*knowledgev1.PipelineScanItem, 0, n)
	for i := range n {
		id := fmt.Sprintf("swap-%08d", i)
		vec := make([]byte, 32)
		vec[0] = byte(i)
		vec[1] = byte(i >> 8)
		page = append(page, &knowledgev1.PipelineScanItem{
			NodeId:       id,
			GraphName:    "swap-repo",
			BinaryVector: vec,
			Bm25Fields:   &knowledgev1.Bm25Fields{SymbolName: id},
		})
	}
	return &swapScanner{page: page}
}

func (s *swapScanner) PipelineScan(_ context.Context, req *knowledgev1.PipelineScanRequest) (*knowledgev1.PipelineScanResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.watermarks = append(s.watermarks, req.GetAfterStampedAtNanos())
	resp := &knowledgev1.PipelineScanResponse{ServedHorizonNanos: swapServedHorizon}
	if !s.served {
		s.served = true
		resp.Items = s.page
	}
	return resp, nil
}

func (s *swapScanner) Execute(context.Context, *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	return &knowledgev1.ExecuteResponse{}, nil
}

// swapDocs builds n HNSW documents and n BM25 documents over the SAME ids, with the
// vectors and the field text both salted by gen. Two calls with different gens
// therefore describe the same corpus while building DIFFERENT blobs — which is what
// makes a prior layer a layer (a second set of content hashes covering documents the
// live set already covers) rather than a duplicate the content-addressed store
// would collapse.
func swapDocs(n, gen int) (hnswDocs, bm25Docs []searchengine.Document) {
	hnswDocs = make([]searchengine.Document, 0, n)
	bm25Docs = make([]searchengine.Document, 0, n)
	for i := range n {
		id := fmt.Sprintf("swap-%08d", i)
		vec := make([]byte, 32)
		vec[0] = byte(i)
		vec[1] = byte(i >> 8)
		vec[2] = byte(gen)
		hnswDocs = append(hnswDocs, searchengine.Document{ID: id, Vector: vec})
		bm25Docs = append(bm25Docs, searchengine.Document{
			ID:     id,
			Fields: map[string]string{searchengine.FieldSymbolName: fmt.Sprintf("%s gen%d", id, gen)},
		})
	}
	return hnswDocs, bm25Docs
}

// seedPriorLayer ships one layer's blobs WITHOUT publishing and returns the ids it
// minted per format. Shipping without publishing is the point: the layer has to land
// in the blob store so the seeder can then hand it to publishPriorManifest, which is
// what puts it in the denominator. A throwaway Manager per layer keeps each layer's
// Export to exactly its own blobs.
func seedPriorLayer(
	t *testing.T, svc *sharedServerFake, gt kgtypes.GraphType, name string, n, gen int,
) (hnswIDs, bm25IDs []searchengine.SegmentID) {
	t.Helper()
	ctx := context.Background()

	view := svc.viewFor(graphSelector(gt, name), swapWriterID)
	mgr := closeOnCleanup(t, NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0, withSegmentSource(view)))
	hnswDocs, bm25Docs := swapDocs(n, gen)

	hnswDM := mgr.managerFor(gt, name)
	require.NoError(t, hnswDM.engine.Add(hnswDocs))
	require.NoError(t, hnswDM.engine.Flush())
	_, err := hnswDM.ship(ctx, nil)
	require.NoError(t, err)

	bmDM := mgr.bm25ManagerFor(gt, name)
	require.NoError(t, bmDM.engine.Add(bm25Docs))
	require.NoError(t, bmDM.engine.Flush())
	_, err = bmDM.ship(ctx, nil)
	require.NoError(t, err)

	for _, b := range hnswDM.engine.Export() {
		hnswIDs = append(hnswIDs, b.ID)
	}
	for _, b := range bmDM.engine.Export() {
		bm25IDs = append(bm25IDs, b.ID)
	}
	require.NotEmpty(t, hnswIDs, "a seeded layer must mint at least one hnsw blob")
	require.NotEmpty(t, bm25IDs, "a seeded layer must mint at least one bm25 blob")
	return hnswIDs, bm25IDs
}

// manifestScopedCloudView puts a fake view in the LIVE CLOUD SHAPE, which is TWO
// coupled settings rather than one. List reads the published manifest
// (gcsSegmentSource.List), and completeness is verified SERVER-side, which is what
// makes publishCoverageOK skip its client-side subset check. The two must move
// together: on a manifest-scoped List a freshly shipped blob is NEVER yet in the
// manifest, so keeping the client subset check would refuse the FIRST publish of any
// new content forever — the deadlock publishCoverageOK already documents at its
// verifiesCompletenessServerSide branch. Setting only listFromManifest models a
// source that exists nowhere in production.
func manifestScopedCloudView(v *fakeSegmentSource) {
	v.listFromManifest = true
	v.verifies = true
}

// publishPriorManifest installs BOTH formats' manifests in ONE server mutation.
// Publishing them one format at a time does NOT work: the fake's refcount-GC runs on
// every publish and reaps every blob no manifest yet references, so the first
// format's publish would reap the second format's freshly seeded layer before
// anything referenced it. The GC still runs once at the end, so a blob outside both
// manifests is still reaped — the model is unchanged, only the ordering hazard is
// removed.
func publishPriorManifest(
	svc *sharedServerFake, target *knowledgev1.GraphSelector, writerID string, byFormat map[string][]searchengine.SegmentID,
) {
	svc.mu.Lock()
	k := svc.key(target)
	if svc.manifests[k] == nil {
		svc.manifests[k] = map[string]map[string]bool{}
	}
	for format, ids := range byFormat {
		set := map[string]bool{}
		for _, id := range ids {
			set[id] = true
		}
		svc.manifests[k][writerID+"\x00"+format] = set
	}
	referenced := map[string]bool{}
	for _, s := range svc.manifests[k] {
		for id := range s {
			referenced[id] = true
		}
	}
	kept := svc.byKey[k][:0]
	for _, b := range svc.byKey[k] {
		if referenced[b.GetId()] {
			kept = append(kept, b)
		}
	}
	svc.byKey[k] = kept
	svc.mu.Unlock()
}

// manifestDocCount sums the DocCount of every digest in the fixture writer's
// manifest for one format — the shipped denominator the publish gate divides
// against, read straight off the server model rather than off any engine.
func manifestDocCount(svc *sharedServerFake, target *knowledgev1.GraphSelector, format string) int {
	total := 0
	for _, m := range svc.manifestMetas(target, swapWriterID) {
		if m.GetFormat() == format {
			total += int(m.GetDocCount())
		}
	}
	return total
}

// residentCounts is one format arm's post-swap reader measurement. BOTH numbers are
// needed and neither alone is sufficient:
//
//   - Summed is ResidentDocCount, the sum of per-segment DocCount. It is the number
//     the incident reported (resident 47,431; bm25 +86,954) and the ONLY one that can
//     see a layer: a document resident in two segments counts twice here.
//   - Distinct is DistinctResidentDocCount, the route map's size. It is blind to
//     layering by construction — two layers over the same ids read exactly one corpus
//     — so a leg asserting distinct ALONE passes with the prior layer fully intact.
//     It is here to catch the opposite failure: a retire that dropped documents.
//
// Summed == Distinct == the true corpus is the only state that is one corpus, whole.
type residentCounts struct {
	Summed   int
	Distinct int
}

// residentAfterReload reports what a READER sees per format after the swap: a FRESH
// Manager over the same server and a cold L2 cache, loaded from the published
// manifest. Reading a fresh reader rather than the writer's own engines is
// deliberate — the writer's resident set is what it just built, while this is the
// corpus the swap actually made durable.
func residentAfterReload(
	t *testing.T, svc *sharedServerFake, gt kgtypes.GraphType, name string,
) (hnswResident, bm25Resident residentCounts) {
	t.Helper()
	ctx := context.Background()

	view := svc.viewFor(graphSelector(gt, name), swapWriterID)
	manifestScopedCloudView(view)
	reader := closeOnCleanup(t, NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0, withSegmentSource(view)))

	hnswDM := reader.managerFor(gt, name)
	require.NoError(t, hnswDM.load(ctx))
	bmDM := reader.bm25ManagerFor(gt, name)
	require.NoError(t, bmDM.load(ctx))
	return residentCounts{
			Summed:   hnswDM.engine.ResidentDocCount(),
			Distinct: hnswDM.engine.DistinctResidentDocCount(),
		}, residentCounts{
			Summed:   bmDM.engine.ResidentDocCount(),
			Distinct: bmDM.engine.DistinctResidentDocCount(),
		}
}

// TestRebuildReplacesPriorLayerAndAdvancesWatermark asserts a rebuild over a graph
// that ALREADY HOLDS A LAYER leaves BOTH formats at exactly ONE corpus and leaves a
// non-zero watermark behind.
//
// PER-FORMAT, NOT AGGREGATE. hnsw and bm25 carry SEPARATE manifests, so a fix that
// lands on one and not the other passes any aggregate assertion — which is exactly
// the bm25-gained-a-layer / hnsw-gained-nothing asymmetry this criterion exists to
// settle. Every leg below is taken per format.
//
// THE FIXTURE REACHES THE REFUSAL CONDITION. The prior manifest holds two full
// corpus layers plus one extra segment, all with real per-digest doc counts and a
// total far above the tiny-graph floor, so the ratio is armed and a correct
// one-corpus live set is BELOW it. That is the deadlock: the publish that would
// retire the inflated digests is gated on a ratio computed over them. The ratio
// constant is not touched — the fixture is built to reach the gate, not to clear it.
//
// THE WATERMARK IS THE DURABILITY LEG. It is the only signal that a manifest swap
// FINALIZED; a rebuild whose publish was skipped returns a nil error and looks
// identical from the outside. Equality with the served horizon (rather than "it
// moved") is what catches a driver stamping a clock reading of its own.
func TestRebuildReplacesPriorLayerAndAdvancesWatermark(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	gt, name := kgtypes.GraphCode, "swap-repo"
	target := graphSelector(gt, name)
	svc := newSharedServerFake()

	// --- the prior state: a published manifest at roughly twice the true corpus ----
	var priorHNSW, priorBM25 []searchengine.SegmentID
	for gen := 1; gen <= swapPriorLayers; gen++ {
		h, b := seedPriorLayer(t, svc, gt, name, swapCorpusN, gen)
		priorHNSW = append(priorHNSW, h...)
		priorBM25 = append(priorBM25, b...)
	}
	publishPriorManifest(svc, target, swapWriterID, map[string][]searchengine.SegmentID{
		hnswFormatName: priorHNSW,
		bm25FormatName: priorBM25,
	})

	const wantPrior = swapPriorLayers * swapCorpusN
	for _, format := range []string{hnswFormatName, bm25FormatName} {
		got := manifestDocCount(svc, target, format)
		require.Equal(t, wantPrior, got,
			"%s: the fixture must START from an inflated NON-DISARMED manifest, or the refusal condition is never reached", format)
		require.GreaterOrEqual(t, got, residentBackstopFloor,
			"%s: a manifest below the floor DISARMS the ratio, and a disarmed fixture passes without exercising the gate", format)
	}

	// --- the rebuild -------------------------------------------------------------
	view := svc.viewFor(target, swapWriterID)
	manifestScopedCloudView(view)
	mgr := closeOnCleanup(t, NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0, withSegmentSource(view)))
	scanner := newSwapScanner(swapCorpusN)

	out, err := tools.RebuildSegments(ctx, scanner, toolsShipperAdapter{mgr}, gt, name, false)
	require.NoError(t, err)
	require.True(t, out.Ran, "the rebuild must actually run")
	require.Equal(t, swapCorpusN, out.Scanned)
	require.Equal(t, searchengine.BucketCountFor(swapCorpusN), out.Built,
		"every partition of the true corpus is emitted")
	require.True(t, out.Published,
		"the swap must be reported as PUBLISHED — every leg below reads state the swap produced, so a run that only SHIPPED would make them assertions about the fixture instead of about the swap")

	// --- the durability leg ------------------------------------------------------
	watermark, _, err := mgr.LoadRebuildState(gt, name)
	require.NoError(t, err)
	assert.Equal(t, swapServedHorizon, watermark,
		"the watermark must equal the SERVER-served horizon: it is the only evidence a manifest swap FINALIZED, and it has never moved off zero on a graph holding a prior layer")

	// --- the per-format retire legs ----------------------------------------------
	for _, format := range []string{hnswFormatName, bm25FormatName} {
		assert.Equal(t, swapCorpusN, manifestDocCount(svc, target, format),
			"%s: a completed swap must RESET the shipped reference to the set it just published — a manifest still summing the prior layer keeps the ratio armed against the next rebuild and the deadlock never exits", format)
	}

	hnswResident, bm25Resident := residentAfterReload(t, svc, gt, name)
	for _, arm := range []struct {
		format string
		counts residentCounts
	}{{hnswFormatName, hnswResident}, {bm25FormatName, bm25Resident}} {
		assert.Equal(t, swapCorpusN, arm.counts.Summed,
			"%s: a reader loading the published manifest must SUM to exactly ONE corpus — a higher sum is the prior layer still resident, and it is the only one of the two counts that can see it", arm.format)
		assert.Equal(t, swapCorpusN, arm.counts.Distinct,
			"%s: the retire must not have cost documents — distinct is blind to layering, so it is here for the opposite failure", arm.format)
	}

	// KNOWN-NEGATIVE CONTROL, and it carries two jobs at once.
	//
	// Every leg above asserts a swap LANDED, and each of them would pass just as
	// happily against a publish path that had stopped consulting the coverage gate at
	// all. This arm re-runs the identical fixture with the manifest pushed ONE SEGMENT
	// past twice the true corpus, which is the far side of the knife edge: the ratio is
	// resident < 0.5*shipped, so at exactly two layers a correct one-corpus live set
	// reads 2048 < 2048 and passes by exactly ZERO margin, while one extra segment tips
	// it into refusal. So it proves the legs above bite (a driver that could not swap
	// leaves the watermark at zero and the manifest inflated — observed here), and it
	// is the non-weakening guard: nothing in this step may make a below-ratio publish
	// land.
	//
	// A MANIFEST THIS INFLATED DOES NOT EXIT THROUGH THIS PATH, deliberately. The gate
	// cannot tell "my live set is a complete re-derivation" from "my engine loaded half
	// the corpus", and refusing is the behavior that kept the incident's damage local.
	t.Run("a manifest inflated past the ratio still refuses, and the watermark holds", func(t *testing.T) {
		ctlName := "swap-repo-overinflated"
		ctlTarget := graphSelector(gt, ctlName)
		ctlSvc := newSharedServerFake()

		var ctlHNSW, ctlBM25 []searchengine.SegmentID
		for gen := 1; gen <= swapPriorLayers; gen++ {
			h, b := seedPriorLayer(t, ctlSvc, gt, ctlName, swapCorpusN, gen)
			ctlHNSW = append(ctlHNSW, h...)
			ctlBM25 = append(ctlBM25, b...)
		}
		// The one extra segment past 2x that tips the ratio into refusal.
		h, b := seedPriorLayer(t, ctlSvc, gt, ctlName, searchengine.DefaultMinSegmentDocs, swapPriorLayers+1)
		ctlHNSW = append(ctlHNSW, h...)
		ctlBM25 = append(ctlBM25, b...)
		publishPriorManifest(ctlSvc, ctlTarget, swapWriterID, map[string][]searchengine.SegmentID{
			hnswFormatName: ctlHNSW,
			bm25FormatName: ctlBM25,
		})

		const wantInflated = swapPriorLayers*swapCorpusN + searchengine.DefaultMinSegmentDocs
		for _, format := range []string{hnswFormatName, bm25FormatName} {
			require.Equal(t, wantInflated, manifestDocCount(ctlSvc, ctlTarget, format),
				"%s: the control must start PAST twice the true corpus or it is not on the refusing side of the ratio", format)
		}

		ctlView := ctlSvc.viewFor(ctlTarget, swapWriterID)
		manifestScopedCloudView(ctlView)
		ctlMgr := closeOnCleanup(t, NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0, withSegmentSource(ctlView)))

		ctlOut, err := tools.RebuildSegments(
			context.Background(), newSwapScanner(swapCorpusN), toolsShipperAdapter{ctlMgr}, gt, ctlName, false)
		require.NoError(t, err, "a refused publish is not an error — that is exactly why the error cannot be the completion signal")
		require.True(t, ctlOut.Ran)
		require.False(t, ctlOut.Published, "the refusal must reach the caller as NOT published")

		watermark, _, err := ctlMgr.LoadRebuildState(gt, ctlName)
		require.NoError(t, err)
		assert.Zero(t, watermark,
			"a refused publish made nothing durable, so the watermark must NOT advance — and its advancing above is therefore a real signal, not a constant")
		for _, format := range []string{hnswFormatName, bm25FormatName} {
			assert.Equal(t, wantInflated, manifestDocCount(ctlSvc, ctlTarget, format),
				"%s: a refused publish leaves the prior manifest and its blobs INTACT — a skip is a no-op, never a corpus wipe", format)
		}
	})
}
