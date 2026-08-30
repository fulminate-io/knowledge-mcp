// SPDX-License-Identifier: Apache-2.0

// manager_bucket_delete_seal.go closes the IMPORT WINDOW a delete used to leave open.
//
// THE WINDOW. A delete kills the ids in the partitions that hold them and re-emits
// those partitions, so the removal survives in the blobs this client ships. What it did
// NOT do is stop a blob written BEFORE the delete from putting the document back: every
// Import starts its members LIVE unless the engine is handed a tombstone set, so a cold
// load, a stranded pre-merge constituent, or any pre-delete copy still on disk re-adds
// the removed node. That was measured, not inferred — one ordinary read after an aborted
// merge reclaim resolves the deleted id again.
//
// THE SEAL IS THE GRAPH'S OWN TOMBSTONE RECORD, and it is durable because that record
// already is. rebuild_state.go persists a per-graph {watermark, tombstoned} file under
// the L2 cache root and writes it atomically; SetGraphTombstones hands the set to the
// engines, which seed it into every segment they import. Both halves are this package's,
// so sealing needed NO new persistence surface — it needed the delete path to write the
// record it was already reading elsewhere.
//
// THE SET'S LIFECYCLE, stated here because an unbounded in-memory set is a leak rather
// than a seal:
//
//   - DURABILITY. The record survives a restart, and is hydrated back into the engines'
//     seed on the first Import of a fresh process (Manager.graphTombstones). Wiping the
//     L2 cache removes it, which is the correct coupling: the blobs it masks go with it.
//   - GROWTH BOUND. Membership is the rule rebuildStateRecord states — the ids learned
//     deleted whose partitions have not yet been re-emitted without them — and the set has
//     THREE REMOVERS, so it drains rather than accumulating. retainTombstones drops an id
//     once a rebuild RUN re-emits the partition routing it. trimReEmittedTombstones drops
//     one once a DRAIN publishes that partition. UntombstoneWrittenIDs drops one whose
//     document has been written again. The first two are separate paths shrinking one
//     persisted set — a rebuild run and a deferred drain are different producers of the
//     same discharge — and the delete's own contribution is therefore bounded by the
//     deletes whose partitions neither has re-emitted since.
//   - WHAT EACH TRIM KEYS ON, stated here because this is where a reader looks for it and
//     because taking the old wording at face value is what let a live defect sit unseen.
//     The rebuild driver's trim keys on the partition count the run's re-emit ACTUALLY RAN
//     AT, supplied by its caller rather than derived from the items in hand; deriving it
//     from a delta WINDOW collapses every masked id onto one always-emitted partition and
//     persists an empty record. The drain-side trim keys on the partitions the drains
//     actually PUBLISHED — per format, each under that format's own count — never on the
//     partitions a drain merely scheduled.
//   - THE RETENTION TRIM IS WHERE THIS SEAL ENDS, and what remains there is narrower than
//     it looks. The trim drops an id because "no durable blob holds the node and no import
//     can bring it back", which is true of a partition that was re-emitted — and not
//     obviously true when the re-emit's merge reclaim aborted, because the pre-delete
//     constituent is still on disk carrying the document. That case is already closed in
//     the blob rather than pending: a consolidated blob carries a durable supersession
//     record naming both what it superseded and the whole cohort published alongside it
//     (searchengine/supersession.go), and declineSuperseded drops those constituents at
//     Import rather than importing them (searchengine/distribution.go) — which is what
//     manager_reclaim.go's own aborted-reclaim error text tells the caller. The residual
//     that genuinely survives is narrower: the record is honored only when the superseding
//     blob AND every id in its cohort are present, so a reclaim whose blob write aborted
//     and whose following persistResident also failed leaves constituents that nothing
//     declines.
package segmentdist

import (
	"fmt"
	"log/slog"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// sealDeletedIDs folds the ids of a delete into the graph's durable tombstone record and
// re-seeds the engines from the MERGED set.
//
// IT MERGES, IT NEVER REPLACES, and that is the one rule this function must not get
// wrong. SetGraphTombstones REPLACES a graph's set and an empty set DELETES the entry, so
// handing it this delete's own window would erase everything earlier passes accumulated
// and re-open every window those ids were holding closed.
//
// IT PERSISTS BEFORE IT SEEDS. A crash between the two leaves a record that already knows
// the ids are dead, which is the safe direction; the reverse leaves a process seeding
// from a set its own disk does not carry.
//
// IT NEVER MOVES THE WATERMARK — that value is the rebuild's durability contract and may
// advance only when a publish LANDED. The watermark read here is written straight back.
//
// IT DOES NOT STAMP THE DELETE (NoteDeletedIDs), and the omission is deliberate. A stamp
// SUPPRESSES a queued write that began before it; the delete path has just purged the
// backlog for these very ids, so there is no such write to suppress, and any write queued
// AFTER this point is a genuine re-creation that must be reported — which an unstamped id
// is, since a missing stamp reads as sequence zero (TombstonedPendingWriteIDs). Stamping
// here could only suppress a legitimate re-creation.
func (m *Manager) sealDeletedIDs(
	gt kgtypes.GraphType, name string, ids []searchengine.ExternalID,
) error {
	watermark, retained, err := m.LoadRebuildState(gt, name)
	if err != nil {
		// Merging into a set we could not read would DROP the ids it holds, which is
		// the erasure this exists to avoid, so the seal declines rather than guessing.
		return fmt.Errorf(
			"segmentdist: delete could not seal its import window for %s/%s — the tombstone record is "+
				"unreadable, so a blob shipped before this delete will resurrect its documents on the "+
				"next import: %w", gt, name, err)
	}
	carried := unionExternalIDs(retained, ids)
	if err := m.SaveRebuildState(gt, name, watermark, carried); err != nil {
		return fmt.Errorf(
			"segmentdist: delete could not persist its tombstone record for %s/%s — this process masks "+
				"the deleted ids at Import, but a restart will not: %w", gt, name, err)
	}
	// The MERGED set, never the caller's window and never the ids new to it.
	m.SetGraphTombstones(gt, name, carried)
	return nil
}

// trimReEmittedTombstones is sealDeletedIDs' exact inverse: it removes from the durable
// tombstone record the candidate ids whose partitions a drain has just re-emitted
// without them. The two live in one file for the reason the union and the trim live
// together on the rebuild driver's side — both decide membership of one persisted set,
// and separating them hides that the rule they enforce is the same rule.
//
// THE PREDICATE IS PER ID AND PER FORMAT, AND THE COUNTS ARE NOT INTERCHANGEABLE. An id
// leaves only when the partition it routes to under the HNSW count was published by the
// HNSW drain AND the partition it routes to under the BM25 count was published by the
// BM25 drain. It is NOT "the same partition number appears in both published sets": the
// two pools derive their counts independently, so a raw partition int from one is not
// comparable to one from the other, and each format's question has to be asked under
// that format's own count.
//
// PUBLISHED, NEVER SCHEDULED. A drain that harvested nothing for a partition records no
// entry for it, so an id in that partition stays masked — the conservative direction,
// and the one that keeps a degenerate or unloaded pool from emptying the record.
//
// IT NEVER MOVES THE WATERMARK, on the same terms sealDeletedIDs states: that value is
// the rebuild's durability contract, so the watermark read here is written straight back.
//
// ITS ERROR IS RETURNED RATHER THAN LOGGED. A record that failed to shrink costs a
// redundant re-emit next tick and nothing else, but a caller told the mask shrank when it
// did not would stop offering those partitions with the ids still masked.
func (m *Manager) trimReEmittedTombstones(
	gt kgtypes.GraphType, name string, candidates []searchengine.ExternalID,
	hnswPublished map[int]bool, hnswCount int,
	bm25Published map[int]bool, bm25Count int,
) error {
	if len(candidates) == 0 {
		return nil
	}
	discharged := make(map[searchengine.ExternalID]struct{}, len(candidates))
	for _, id := range candidates {
		if hnswPublished[searchengine.BucketOf(id, hnswCount)] &&
			bm25Published[searchengine.BucketOf(id, bm25Count)] {
			discharged[id] = struct{}{}
		}
	}
	if len(discharged) == 0 {
		return nil
	}

	watermark, retained, err := m.LoadRebuildState(gt, name)
	if err != nil {
		// Rewriting a set we could not read would DROP the ids it holds, which is the
		// erasure the seal exists to prevent, so the trim declines rather than guessing.
		return fmt.Errorf(
			"segmentdist: the deferred re-emit could not read the tombstone record for %s/%s to discharge "+
				"the partitions it just rebuilt, so the ids stay masked and are re-offered next tick: %w",
			gt, name, err)
	}
	// Survivor order is preserved so the persisted record stays stable across ticks that
	// discharge nothing.
	kept := make([]searchengine.ExternalID, 0, len(retained))
	for _, id := range retained {
		if _, gone := discharged[id]; gone {
			continue
		}
		kept = append(kept, id)
	}
	if err := m.SaveRebuildState(gt, name, watermark, kept); err != nil {
		return fmt.Errorf(
			"segmentdist: the deferred re-emit rebuilt its partitions for %s/%s but could not persist the "+
				"reduced tombstone record, so those ids stay masked: %w", gt, name, err)
	}
	// The in-memory seed follows disk, never leads it.
	m.SetGraphTombstones(gt, name, kept)
	// THE DISCHARGE IS THE OTHER HALF OF THE CONVERGENCE SIGNAL. The selector announces
	// what it served; this announces what actually left the record, which is the number
	// that has to keep falling. A drain that serves partitions tick after tick while
	// discharging nothing is the lane-fires-forever shape, and only these two lines read
	// together make it visible.
	slog.Info("segmentdist: deferred re-emit discharged masked ids",
		"graph_type", gt, "name", name, "discharged", len(discharged), "still_masked", len(kept))
	return nil
}

// unionExternalIDs merges retained with added, dropping duplicates and preserving the
// retained order so the persisted record is stable across deletes that add nothing new.
// It is unionTombstones' in-package twin, kept here rather than shared because the two
// modules exchange no hand-written packages.
func unionExternalIDs(retained, added []searchengine.ExternalID) []searchengine.ExternalID {
	seen := make(map[searchengine.ExternalID]struct{}, len(retained)+len(added))
	out := make([]searchengine.ExternalID, 0, len(retained)+len(added))
	add := func(id searchengine.ExternalID) {
		if _, dup := seen[id]; dup {
			return
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	for _, id := range retained {
		add(id)
	}
	for _, id := range added {
		add(id)
	}
	return out
}
