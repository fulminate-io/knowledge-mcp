// SPDX-License-Identifier: Apache-2.0

// manager_rebuild_finalize.go — the reset path's PER-FORMAT finalize and the
// ship/un-ship legs it owns. Split from manager_rebuild_entry.go at the finalizer's
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

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// finalizeResetLayer is ONE FORMAT's reset finalize: it builds that format's staged
// partitions into a complete layer ASIDE, ships it, gates it, swaps it in whole, and
// publishes the result. It returns the superseded ids and whether the swap LANDED.
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
//	SHIP          — before the swap, so "every resident blob is already shipped" holds
//	                at EVERY instant. Swapping first would leave a window in which the
//	                engine serves blobs the server has not been told about; an embed
//	                drain publishing there names unshipped ids and takes a 409 or
//	                non-subset SKIP. That is a nil-error deferral rather than a wipe,
//	                but it is avoidable outright by ordering.
//	GATE          — against the PROSPECTIVE layer, because after the swap it is too
//	                late: the degenerate layer would already be serving reads.
//	SWAP          — one CAS, retiring the whole prior layer.
//	PUBLISH       — the new resident Export, reconciled against shippedIDs (ROLE A) so
//	                dropped IS the old layer and superseded-pruned goes non-zero.
//
// THE SHIP STILL APPLIES THE shippedIDs DIFF. Force-uploading the built set would be
// simpler and would break the property that a re-run over an unchanged corpus is a
// content-hash no-op — the second reset must ship nothing and report nothing pruned.
//
// A REFUSAL IS NOT A TRUE NO-OP, and the caller owns the unwind because the caller
// owns the ship. Two consequences of having shipped before gating, with different
// dispositions. RECLAIM is traced and accepted: the uploaded blob is an orphan that
// PruneCache reaps locally, and server-side the next successful publish's refcount GC
// reaps it because no manifest ever referenced it. BOOKKEEPING is the consequential
// one and is unwound here: shipNew stamps both shippedIDs and locallyShipped, and a
// retained stamp makes the ship diff SUPPRESS that blob on every later attempt — so if
// the server GCs the orphan before an identical rebuild happens to re-mint it, the
// publish names ids the server no longer holds and takes a nil-error skip, repeating
// until a restart re-seeds shippedIDs from List(0). The un-stamp is exact: the built
// ids are known precisely, so this is a bounded removal and not a heuristic sweep.
func finalizeResetLayer[Q, S any](
	ctx context.Context, gt kgtypes.GraphType, name string,
	bm *distManager[Q, S], work []searchengine.BucketWork,
) (superseded []searchengine.SegmentID, swapped bool, err error) {
	if len(work) == 0 {
		// No staged partitions for this format: the caller is not resetting it. Fall back
		// to the pre-existing tail behavior — force-seal whatever the engine buffered and
		// publish it the ordinary way — so a finalize driven for other reasons still
		// behaves as it did.
		if ferr := bm.engine.Flush(); ferr != nil {
			return nil, false, ferr
		}
		before := bm.completedSwapCount()
		dropped, perr := bm.shipAndPublish(ctx, bm.shippedIDs)
		if perr != nil {
			return nil, false, perr
		}
		return dropped, bm.completedSwapCount() > before, nil
	}

	if err := bm.ensureShippedSeeded(ctx); err != nil {
		return nil, false, err
	}

	built, err := bm.engine.BuildLayer(work)
	if err != nil {
		return nil, false, err
	}
	if built.Len() == 0 {
		return nil, false, nil
	}
	blobs := built.Blobs()

	shippedNow, err := shipBuiltLayer(ctx, bm, blobs)
	if err != nil {
		return nil, false, err
	}

	ok, reason, err := bm.prospectiveLayerOK(ctx, blobs)
	if err != nil {
		unshipBuiltLayer(bm, shippedNow)
		return nil, false, err
	}
	if !ok {
		unshipBuiltLayer(bm, shippedNow)
		slog.Warn("segmentdist: reset SKIPPED the layer swap (degenerate built layer — the prior layer keeps serving)",
			"graph_type", gt, "name", name, "format", bm.format, "built", len(blobs), "reason", reason)
		return nil, false, nil
	}

	if _, _, err := bm.engine.ReplaceLayer(built); err != nil {
		unshipBuiltLayer(bm, shippedNow)
		return nil, false, err
	}

	before := bm.completedSwapCount()
	superseded, err = bm.publishResident(ctx, bm.engine.Export(), bm.shippedIDs)
	if err != nil {
		return nil, false, err
	}
	return superseded, bm.completedSwapCount() > before, nil
}

// shipBuiltLayer uploads the built layer's blobs through the ordinary ship-new leg,
// applying the shippedIDs diff so a content-hash-unchanged rebuild uploads nothing. It
// returns the ids this call actually stamped, which is exactly what a refusal has to
// un-stamp.
func shipBuiltLayer[Q, S any](
	ctx context.Context, bm *distManager[Q, S], blobs []searchengine.SegmentBlob,
) ([]searchengine.SegmentID, error) {
	bm.shipMu.Lock()
	var diff []*knowledgev1.SegmentBlobProto
	diffBlobs := make(map[string]searchengine.SegmentBlob)
	stamped := make([]searchengine.SegmentID, 0, len(blobs))
	for _, b := range blobs {
		if _, sent := bm.shippedIDs[b.ID]; sent {
			continue
		}
		diff = append(diff, blobToProto(b))
		diffBlobs[b.ID] = b
		stamped = append(stamped, b.ID)
	}
	bm.shipMu.Unlock()

	slog.Info("segmentdist: ship diff resolved",
		"graph", bm.target.GetGraph(), "name", bm.target.GetName(), "repo", bm.target.GetRepo(),
		"format", bm.format, "resident", len(blobs), "shipped", len(diff),
		"skipped_as_present", len(blobs)-len(diff))

	if err := bm.shipNew(ctx, diff, diffBlobs); err != nil {
		return nil, err
	}
	return stamped, nil
}

// unshipBuiltLayer removes exactly the ids a refused swap's ship stamped, under the
// same lock the stamping took. Without it those ids stay in shippedIDs forever and the
// diff suppresses them on every later attempt, so an identical rebuild would publish
// names the server may no longer hold and take nil-error skips until a restart.
func unshipBuiltLayer[Q, S any](
	bm *distManager[Q, S], ids []searchengine.SegmentID,
) {
	if len(ids) == 0 {
		return
	}
	bm.shipMu.Lock()
	defer bm.shipMu.Unlock()
	for _, id := range ids {
		delete(bm.shippedIDs, id)
		delete(bm.locallyShipped, id)
	}
}
