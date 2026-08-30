// SPDX-License-Identifier: Apache-2.0

// manager_rebuild_finalize.go — the reset path's PER-FORMAT finalize and the
// cache-write leg it owns. Split from manager_rebuild_entry.go at the finalizer's
// per-format seam: that file keeps the staging entry point and the single
// FinalizeRebuild that sequences both formats, while the sequence each format runs
// lives here.
//
// ONE BODY SERVES BOTH FORMATS. It is generic over distManager[Q, S] for the same
// reason replaceBucketAndPublish is: the two live instantiations carry different type
// arguments, and a second per-format copy of a sequence whose ordering IS its safety
// property is exactly where the two would drift.

package segmentdist

import (
	"context"
	"log/slog"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// finalizeResetLayer is ONE FORMAT's reset finalize: it builds that format's staged
// partitions into a complete layer ASIDE, writes it to L2, gates it, swaps it in
// whole, and records what the swap superseded. It returns the superseded ids and
// whether the swap LANDED.
//
// BOTH FORMATS RUN THIS SAME BODY. Before the collapse the vector corpus finalized
// through a second staging engine while the field corpus had none, and that asymmetry
// is the whole reason the two drifted — the field corpus kept publishing the union of
// every layer it had held. One body over one engine per format removes the asymmetry
// rather than documenting it.
//
// THE ORDER IS THE SAFETY PROPERTY, and each step earns its place:
//
//	BUILD ASIDE   — nothing resident is read, so the layer is from-scratch by
//	                construction and the serving engine is untouched if anything fails.
//	CACHE-WRITE   — before the swap, so "every resident blob is already durable" holds
//	                at EVERY instant. Swapping first would leave a window in which the
//	                engine serves blobs that exist only in memory, and a crash inside
//	                that window loses them outright.
//	GATE          — against the PROSPECTIVE layer, because after the swap it is too
//	                late: the degenerate layer would already be serving reads.
//	SWAP          — one CAS, retiring the whole prior layer.
//	SUPERSEDED    — the ids the prior layer held and the new one does not, read from
//	                the engine's own Export either side of the swap.
//
// THE CACHE-WRITE STILL APPLIES A DIFF. Force-writing the built set would be simpler
// and would break the property that a re-run over an unchanged corpus is a
// content-hash no-op — the second reset must write nothing and report nothing
// superseded.
//
// A REFUSAL LEAVES BLOBS ON DISK that no layer references, and they STAY there. This
// used to call them orphans reaped by PruneCache; they are not reaped, because the
// cache presence the diff keys on is exactly what makes them live to the prune — its
// live set is force-loaded from the pool's L2 index (see the reap paragraph at the top
// of prune_cache.go). There is still no bookkeeping to unwind here.
func finalizeResetLayer[Q, S any](
	ctx context.Context, gt kgtypes.GraphType, name string,
	bm *distManager[Q, S], work []searchengine.BucketWork,
) (superseded []searchengine.SegmentID, swapped bool, err error) {
	if len(work) == 0 {
		// No staged partitions for this format: the caller is not resetting it. Fall back
		// to the pre-existing tail behavior — force-seal whatever the engine buffered and
		// make it durable — so a finalize driven for other reasons still behaves as it
		// did.
		//
		// SWAPPED REPORTS NEW DURABILITY HERE, not a layer swap, because no layer swap
		// happens on this branch: nothing is replaced, a buffered tail is sealed and
		// written. A caller asking "did this finalize change anything" is answered by
		// whether any blob was newly written, which is the directly observable local
		// analog of the manifest swap this used to report.
		if ferr := bm.engine.Flush(); ferr != nil {
			return nil, false, ferr
		}
		wrote, perr := bm.persistResident()
		if perr != nil {
			return nil, false, perr
		}
		return nil, wrote > 0, nil
	}

	built, err := bm.engine.BuildLayer(work)
	if err != nil {
		return nil, false, err
	}
	if built.Len() == 0 {
		return nil, false, nil
	}
	blobs := built.Blobs()

	if err := writeBuiltLayerToL2(bm, blobs); err != nil {
		return nil, false, err
	}

	ok, reason := bm.prospectiveLayerOK(blobs)
	if !ok {
		slog.Warn("segmentdist: reset SKIPPED the layer swap (degenerate built layer — the prior layer keeps serving)",
			"graph_type", gt, "name", name, "format", bm.format, "built", len(blobs), "reason", reason)
		return nil, false, nil
	}

	// The prior layer's ids, read BEFORE the swap retires it. This is the whole
	// operand for the superseded set: no bookkeeping is kept, so the engine's own
	// before/after Export is the authority on what the swap dropped.
	priorIDs := exportedIDs(bm.engine.Export())

	published, _, err := bm.engine.ReplaceLayer(built)
	if err != nil {
		return nil, false, err
	}

	// ABSORB WHAT A CONCURRENT PUBLISHER LANDED INSIDE THE BUILD WINDOW, before the
	// superseded set is computed below.
	//
	// AN ABSORB ERROR IS RETURNED RATHER THAN WARNED PAST. The layer swap has landed and
	// the corpus is correct-but-duplicated; a swallowed failure would be a lane hiding
	// its own failure. The caller treats a finalize error as fatal for the run and does
	// not advance the watermark, which is the right disposition for this state.
	if _, aErr := absorbBuildWindowSurvivors(bm, published); aErr != nil {
		return nil, false, aErr
	}

	// SUPERSEDED = prior − new. Its consumer is FinalizeRebuild → InvalidateLocal,
	// which evicts the superseded .seg files from L2; losing it orphans them on a
	// cache whose LRU never fires.
	//
	// THE POST-SWAP EXPORT IS READ AFTER THE ABSORB, DELIBERATELY. The absorb retires
	// prior-layer segments — the build-window survivors themselves, which ARE in
	// priorIDs because priorIDs was read after they landed. Reading newIDs before the
	// absorb would leave those ids out of the superseded set, and their .seg files would
	// never be evicted by InvalidateLocal.
	newIDs := exportedIDs(bm.engine.Export())
	for id := range priorIDs {
		if _, live := newIDs[id]; !live {
			superseded = append(superseded, id)
		}
	}
	// The swap is the swap: ReplaceLayer returned nil, so it landed. There is no
	// separate publish that could skip with a nil error, which is the only reason a
	// completion counter ever existed.
	return superseded, true, nil
}

// writeBuiltLayerToL2 writes the built layer's blobs into the L2 cache, applying a
// cache-presence diff so a content-hash-unchanged rebuild writes nothing.
func writeBuiltLayerToL2[Q, S any](
	bm *distManager[Q, S], blobs []searchengine.SegmentBlob,
) error {
	var diff []searchengine.SegmentBlob
	for _, b := range blobs {
		if _, present := bm.cache.sizeOf(b.ID); present {
			continue
		}
		diff = append(diff, b)
	}

	slog.Info("segmentdist: L2 write diff resolved",
		"graph", bm.target.GetGraph(), "name", bm.target.GetName(), "repo", bm.target.GetRepo(),
		"format", bm.format, "resident", len(blobs), "written", len(diff),
		"skipped_as_present", len(blobs)-len(diff))

	return bm.writeNewBlobsToL2(diff)
}
