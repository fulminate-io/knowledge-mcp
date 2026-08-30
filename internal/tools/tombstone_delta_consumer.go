// SPDX-License-Identifier: Apache-2.0

// tombstone_delta_consumer.go — the RUNNING-DAEMON consumer of BOTH halves of the
// server-side segment delta. It lands the window's DELETES on the local pool and
// MERGES the window's live items into the local segments, which is what makes
// another machine's updates reach this one: the cloud is the delta channel, and a
// watermark-keyed window of it is pulled and merged incrementally rather than
// re-derived by a full rebuild.
//
// The delete half came first and was for a while the only half. Before it the feed
// had exactly one reader and SetGraphTombstones exactly one writer, both inside the
// manually-invoked rebuild driver, so the only route from a server-side delete to
// this client's segment pool ran through a command a human issued. The live half was
// read by the same scan and discarded; consuming it is what turns this from a
// delete-only feed into the currency path.

package tools

import (
	"context"
	"errors"
	"fmt"

	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// ErrRetentionFloorUnreadable is what the delta arm returns instead of pulling a
// window it cannot report a retention floor for.
//
// ITS DOC STATES THE CONSEQUENCE, because the condition alone reads like a minor
// bookkeeping failure and the consequence is permanent data loss. A pass sent with a
// zero floor is classified by the server as a full rebuild rather than a delta, and
// a full rebuild carries NO erase rows at all. The client then merges the live
// corpus, learns no deletion, and advances its horizon past the window — so every
// deletion inside that window becomes unlearnable for that graph, forever. The
// alternative to declining is not a slower pass; it is silent permanent deletion
// loss.
var ErrRetentionFloorUnreadable = errors.New(
	"retention floor unreadable: a delta pass sent with a zero floor is served as a full rebuild and carries no erases")

// SegmentDelta is one merge pass's result. Horizon is the server-served timestamp
// the pass read up to; the caller carries it forward so the NEXT pass reads only
// what changed after it.
//
// RetentionFloorNanos and ScanFromNanos ARE THE VALUES THIS PASS SENT, assigned from
// the very operands handed to the scan rather than re-read afterwards. A caller that
// recomputed them would print a second, later reading of the same inputs — which is
// the instrument defect that made this pass's behaviour unreadable for two
// investigations, when the only value logged was an operand that did not scope the
// scan at all.
type SegmentDelta struct {
	// Learned is how many ids this pass saw deleted for the FIRST time — the ones it
	// removed from their routed buckets. Ids the persisted record already carried are
	// merged again (harmless) but are not re-deleted, so a steady state costs nothing.
	Learned int
	// Carried is the size of the MERGED tombstone set handed to the engines:
	// everything the record already held plus everything this delta reported.
	Carried int
	// Merged is how many LIVE items this window carried into the local segments.
	Merged int
	// Horizon is the server-served scan horizon. Zero means the scan reported none,
	// in which case the caller must keep its previous value rather than resetting.
	Horizon int64
	// RetentionFloorNanos is the retention floor this pass PUT ON THE WIRE in
	// after_stamped_at_nanos — the minimum across the consumers holding a position.
	RetentionFloorNanos int64
	// ScanFromNanos is the scan bound this pass PUT ON THE WIRE in
	// scan_from_stamped_at_nanos — this consumer's own horizon.
	ScanFromNanos int64
}

// MergeSegmentDelta reads one graph's BOUNDED segment delta and lands BOTH halves in
// the local pool: it merges the reported deletes into the persisted record, hands the
// MERGED set to the engines, removes the newly-learned ids from their routed buckets,
// and adds the window's LIVE items to the local segments through the ordinary
// embed-writeback producer.
//
// THE RECORD WRITE ITSELF IS mergeTombstonesIntoRecord's, and its contract — merge
// rather than replace, and never advance the persisted watermark — lives there with
// its rationale. What belongs to THIS consumer is the delta read: its progress is the
// returned Horizon, which the caller commits only after the drain that made the merge
// durable.
//
// sinceNanos scopes the read. Passing the caller's horizon is what keeps this a
// DELTA: a consumer that re-read the corpus every tick would be worse than the gap it
// closes. The caller is responsible for never passing a zero it has not earned — a
// zero-watermark scan is the full vectored corpus, and merging one would be the full
// rebuild this path exists to replace.
//
// NO MEMBERSHIP FILTER ON THE LIVE HALF, deliberately. It is tempting to ask
// UncoveredMembers first and merge only what is missing — that is what the repair arm
// does, and it is WRONG here: a co-worker's UPDATE to an existing node produces an id
// that IS already live-resident, carrying a NEW vector, and a presence filter would
// drop exactly the updates this path exists to deliver. The engine handles the
// replace: the add resolves each incoming id to its current resident copy before the
// seal and retires that copy after, so the newer document wins. The accepted cost is
// that this machine's own recent writes ride back through the window and re-dirty
// their partitions, bounded by the window and by the drain's own consolidation.
//
// Best-effort by shape: a scan failure returns the error for the caller to log and
// move on, and a bucket-delete failure never discards the merge that already
// persisted. Nothing here fails a reconcile pass.
func MergeSegmentDelta(
	ctx context.Context,
	scanner PipelineScanner, shipper SegmentShipper, merger SegmentRepairShipper, deleter SegmentDeleter,
	gt kgtypes.GraphType, name string, sinceNanos int64,
) (SegmentDelta, error) {
	if scanner == nil || shipper == nil {
		return SegmentDelta{}, fmt.Errorf("segment delta: pipeline not wired — the client is running in degraded mode (no segment engine)")
	}

	// ONE scan carries both halves. This is the only server axis that carries the
	// delete flag, and reading both through the existing seam keeps a single
	// definition of what "changed since" means.
	// THE TWO WATERMARKS ARE SENT SEPARATELY, and which one carries which meaning is
	// the whole correctness of this call. after_stamped_at_nanos carries the FLOOR
	// across the consumers that hold a position, for the reason
	// rebuild_segments_driver.go states at its own call: the ahead consumer must not
	// raise the server's retention watermark past what the lagging one has read, and
	// it is what the server's erasure-completeness refusal is measured against.
	// scan_from_stamped_at_nanos carries THIS consumer's own position, which is what
	// the scan reads from. The window therefore no longer widens down to the floor:
	// a graph whose rebuild watermark is pinned stops re-serving rows this consumer
	// has already merged, which is what lets a repeated pass converge.
	floor := retentionFloorFor(shipper, gt, name, sinceNanos)
	// THE PASS DECLINES RATHER THAN SENDING A ZERO. `<= 0` and not `== 0`: a zero from
	// ANY cause is a floor this client cannot vouch for across both of its consumers,
	// and the caller reaches this code only with a resolved horizon, so a legitimate
	// zero does not arrive here.
	if floor <= 0 {
		return SegmentDelta{}, fmt.Errorf("segment delta %s/%s: %w", gt, name, ErrRetentionFloorUnreadable)
	}
	items, tombstoned, horizon, err := scanRebuildSegmentsAs(
		ctx, graphclient.OpSegmentDeltaMerge, scanner, gt, name, floor, sinceNanos)
	if err != nil {
		return SegmentDelta{}, fmt.Errorf("segment delta scan failed: %w", err)
	}

	out := SegmentDelta{Horizon: horizon, RetentionFloorNanos: floor, ScanFromNanos: sinceNanos}
	if len(tombstoned) > 0 {
		if terr := landDeltaTombstones(ctx, shipper, deleter, gt, name, tombstoned, &out); terr != nil {
			return out, terr
		}
	}

	// The live half runs AFTER the delete half so an id this window reports deleted is
	// already stamped before its partition is rebuilt below.
	if len(items) > 0 {
		if merr := mergeDeltaItems(ctx, merger, gt, name, items, &out); merr != nil {
			return out, merr
		}
	}
	return out, nil
}

// landDeltaTombstones is the DELETE half, unchanged in contract from when it was the
// whole function: stamp this window's own reported deletes, merge them into the
// persisted record, and remove the newly-learned ids from their routed buckets.
func landDeltaTombstones(
	ctx context.Context, shipper SegmentShipper, deleter SegmentDeleter,
	gt kgtypes.GraphType, name string, tombstoned []string, out *SegmentDelta,
) error {
	// Stamp THIS WINDOW's own reported deletes — the scan's slice, never the merged
	// set. Stamping the merge would re-date deletes this window did not report and
	// suppress writes that legitimately followed them. This is also what closes the
	// re-delete case: an already-known id is still in the scan's results even though
	// `fresh` is empty for it. It runs BEFORE the merge below, which is what writes the
	// live set, so no interleaving can observe an id as tombstoned-without-a-stamp.
	shipper.NoteDeletedIDs(gt, name, tombstoned)

	fresh, carried, merr := mergeTombstonesIntoRecord(shipper, gt, name, tombstoned)
	if merr != nil {
		return merr
	}
	out.Learned, out.Carried = len(fresh), len(carried)
	if len(fresh) == 0 || deleter == nil {
		// Everything in this window was already known, so the buckets already lost these
		// documents on the pass that first learned them; re-deleting would rebuild
		// partitions for no change.
		return nil
	}
	// Remove the newly-learned ids from their routed buckets. The caller's re-emit
	// then ships partitions that no longer carry them, which is what makes the delete
	// reach the durable blob rather than only the in-memory live set.
	if derr := deleter.DeleteFromBuckets(ctx, gt, name, fresh); derr != nil {
		return fmt.Errorf("segment delta: bucket delete failed (the merged set is already persisted and seeded): %w", derr)
	}
	return nil
}

// mergeDeltaItems is the LIVE half: it builds documents for EVERY item in the window
// and adds them through the same narrow producer the repair arm uses, so a merged
// document is byte-identical to a freshly-embedded one and the two paths cannot
// drift. The producer's narrowness is the storm guard — it carries no rebuild method,
// so a merge structurally cannot become a manifest swap.
//
// The adds only mark partitions dirty; the caller's drain is what ships them, which
// is why the horizon commit belongs to the caller and not here.
func mergeDeltaItems(
	ctx context.Context, merger SegmentRepairShipper,
	gt kgtypes.GraphType, name string, items []rebuildSegItem, out *SegmentDelta,
) error {
	if merger == nil {
		return nil // degraded client — nothing to merge into.
	}
	ids := allIDsOf(items)
	hnswDocs, fieldDocs, err := buildRepairDocuments(items, ids, ids)
	if err != nil {
		return fmt.Errorf("segment delta merge %s/%s: %w", gt, name, err)
	}
	if len(hnswDocs) > 0 {
		if err := merger.AddAndMarkDirty(ctx, gt, name, hnswDocs); err != nil {
			return fmt.Errorf("segment delta HNSW merge failed: %w", err)
		}
	}
	if len(fieldDocs) > 0 {
		if err := merger.AddAndMarkDirtyFields(ctx, gt, name, fieldDocs); err != nil {
			return fmt.Errorf("segment delta BM25 merge failed: %w", err)
		}
	}
	out.Merged = len(items)
	return nil
}

// allIDsOf is the merge's wanted-set: EVERY item in the window, because a delta
// window is by construction the set of things that changed and all of them are wanted.
func allIDsOf(items []rebuildSegItem) []searchengine.ExternalID {
	ids := make([]searchengine.ExternalID, 0, len(items))
	for _, it := range items {
		ids = append(ids, it.nodeID)
	}
	return ids
}
