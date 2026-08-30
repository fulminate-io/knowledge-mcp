// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"fmt"
	"log/slog"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// reclaimMerged is the engine merge-completion handler: when a background merge
// consolidates several segments into one, it reclaims the superseded
// constituents' L2 disk files from THIS engine's LIVE cache. It is registered as
// the engine's Options.OnMerge at construction (managers/bm25Managers only — the
// deterministic rebuild engines get nil OnMerge and reclaim via the existing
// FinalizeRebuild→InvalidateLocal path instead).
//
// CRASH-SAFE ORDERING (the load-bearing logic): the merged blob is Put FIRST,
// then the constituents are Removed. doMerge does NOT persist the merged blob —
// the only on-disk copies are the old constituents — so Putting the merged blob
// before Removing the constituents guarantees a crash at any point leaves either
// {constituents present} OR {merged present} on disk, never NEITHER. The reverse
// order would open a window where a crash between Remove and Put loses the docs
// entirely (a fresh false-prune). An empty Merged.ID means doMerge could not
// encode the consolidated blob; in that case we skip the whole reclaim — a Remove
// without a durable Put is exactly the false-prune we are guarding against.
//
// Beyond the cache it touches bookkeeping on two disjoint paths. On the SUCCESS path
// it touches evictedIDs, and only while this pool is EVICTED (see step (b) below); on
// the ABORT path it touches lastReclaimAbort (the report) and reclaimPending (the
// retained obligation) and nothing else. It touches no resident-set bookkeeping at
// all: the merged-away constituents
// are removed from L2 by step (c) here, and nothing else records that they ever
// existed. The two diff-suppression sets a merge used to have to reconcile against
// are gone with the upload they suppressed.
//
// LOCKING: the cache legs need none of their own — diskSegmentCache.Put/Remove are
// internally mutex-guarded — but the evictedIDs rewrite takes resMu, which guards
// that field alongside resident. resMu is the ONLY lock this handler acquires, and
// evictResident takes residencyMu before resMu, so no lock cycle exists.
func (m *distManager[Q, S]) reclaimMerged(res searchengine.MergeResult) {
	if res.Merged.ID == "" {
		return // no durable merged blob to anchor the reclaim — do not Remove
	}
	// (a) Persist the consolidated blob FIRST (the crash-safe anchor). Reuses the
	// writeNewBlobsToL2 Put idiom (manager_prune.go).
	//
	// A FAILED PUT ABORTS THE WHOLE RECLAIM, before step (d) removes a single
	// constituent. The Put-before-Remove ordering exists precisely so that a crash
	// between them leaves the constituents intact; a Put that FAILS and is then
	// followed by the Removes anyway defeats that ordering by hand — the merged blob
	// would be absent from disk and its constituents deleted, which is the whole
	// corpus segment gone. Returning here leaves the pre-merge constituents on disk
	// and the engine serving the merged payload from memory.
	//
	// THE ABORT IS RECOVERABLE, NOT PERMANENT, and that is what retainReclaimObligation
	// below is for. Nothing else reaps those constituents — the merge that superseded
	// them cannot run again, and PruneCache's live set is force-loaded from the same L2
	// index it diffs, so a stored blob is never an orphan — so the obligation is kept
	// and discharged on a later consumer touch (manager_reclaim_discharge.go). What is
	// NOT weakened is this branch's own promise: this call removes nothing.
	if err := m.cache.Put(res.Merged.ID, res.Merged.Envelope, res.Merged.Bytes); err != nil {
		slog.Error("segmentdist: merge reclaim ABORTED — the consolidated blob could not be persisted, so its constituents were NOT removed",
			"graph", m.target.GetGraph(), "name", m.target.GetName(), "repo", m.target.GetRepo(),
			"format", m.format, "merged", res.Merged.ID, "constituents", len(res.Removed), "err", err)
		// AND RECORDED, so a driver that owes its caller a durability verdict can read
		// what the log line says. Recording is not reporting: this handler still
		// returns nothing to the engine and every existing driver still sees exactly
		// what it saw before — only a driver that ASKS (replaceBucketAndPublish's
		// surfaceAbortedReclaim) is changed by it.
		m.noteReclaimAbort(res, err)
		// AND RETAINED, which is the other half and a different record: the report
		// above tells this re-emit's caller the state exists, this makes the state
		// converge.
		m.retainReclaimObligation(res)
		return
	}
	// (b) If this pool is EVICTED, rewrite the strict-reload id set before the
	// constituents go away. evictedIDs names the PRE-MERGE constituents that
	// evictResident unloaded, and step (c) is about to delete them from L2 — so a
	// merge that was already in flight when the pool was evicted, completing now,
	// would make the strict re-materialization hard-error on a graph whose data is
	// perfectly intact. Dropping res.Removed and adding res.Merged.ID (already
	// durable from step (a)) keeps the set pointing at what is actually on disk.
	//
	// It PRESERVES strict completeness rather than weakening it: only ids this merge
	// actually superseded are rewritten, so a genuinely lost blob still errors. The
	// test-and-rewrite is one step under resMu, which is what stops it racing
	// markMaterialized's clear of the same pair.
	m.rewriteEvictedIDsForMerge(res)
	// (c) Republish the merged payload as a MAPPING of the file just written.
	// Without this the merge's output stays heap-resident for the life of the
	// process: merges publish through newEntry, which the import path's cleanup
	// never sees, and because a merge REPLACES its constituents a progressively
	// merged corpus would climb back to whole-corpus heap residency — the exact
	// cost this seam exists to remove.
	m.remapMerged(res.Merged)
	// (d) Then reclaim the superseded constituents from the LIVE cache. Reuses the
	// InvalidateLocal Remove-loop idiom (manager_owner.go) but targets the live
	// embed cache, not the deterministic rebuild cache.
	for _, id := range res.Removed {
		m.cache.Remove(id)
	}
}

// rewriteEvictedIDsForMerge is reclaimMerged's step (b): while — and only while —
// the evicted latch is set, replace the merge's superseded constituents in
// evictedIDs with the consolidated blob's id. A pool that is NOT evicted has no
// strict-reload set to maintain and is left untouched.
//
// It is one of exactly TWO writers of evictedIDs and it does not do the other's
// job: it REWRITES the set while the pool stays evicted, whereas markMaterialized
// DROPS the set because the pool became resident. Neither clears the latch on the
// other's behalf.
func (m *distManager[Q, S]) rewriteEvictedIDsForMerge(res searchengine.MergeResult) {
	m.resMu.Lock()
	defer m.resMu.Unlock()
	if !m.evicted.Load() || len(m.evictedIDs) == 0 {
		return
	}
	superseded := make(map[searchengine.SegmentID]struct{}, len(res.Removed))
	for _, id := range res.Removed {
		superseded[id] = struct{}{}
	}
	kept := make([]searchengine.SegmentID, 0, len(m.evictedIDs)+1)
	dropped := false
	haveMerged := false
	for _, id := range m.evictedIDs {
		if _, gone := superseded[id]; gone {
			dropped = true
			continue
		}
		if id == res.Merged.ID {
			haveMerged = true
		}
		kept = append(kept, id)
	}
	if !dropped {
		return // this merge superseded nothing in the unloaded set
	}
	if !haveMerged {
		kept = append(kept, res.Merged.ID)
	}
	m.evictedIDs = kept
}

// reclaimAbortRecord is the LAST merge reclaim this pool aborted, plus a monotonic
// sequence number.
//
// THE SEQUENCE IS WHAT MAKES THE RECORD READABLE, and without it the record is
// unusable rather than merely coarse. A reader wants to know whether an abort landed
// during ITS OWN re-emit; a bare record answers only "an abort happened at some point
// in this process's life", so a delete would inherit an arbitrarily old abort left
// behind by a background merge and report a stale condition as its own.
//
// ONE RECORD RATHER THAN A LIST is the other half. A pool whose disk is failing
// aborts every merge, and a list would grow for as long as that lasts with nothing
// draining it. The seq delta still tells a reader that aborts happened in its window;
// what it gives up is naming more than the most recent one, which is a diagnostic
// detail the ERROR log already carries per occurrence.
type reclaimAbortRecord struct {
	seq          uint64
	merged       searchengine.SegmentID
	constituents int
	err          error
}

// noteReclaimAbort records an aborted reclaim. It is called from reclaimMerged's
// abort branch ONLY, so a bumped sequence always means constituents were left on
// disk.
func (m *distManager[Q, S]) noteReclaimAbort(res searchengine.MergeResult, err error) {
	m.resMu.Lock()
	defer m.resMu.Unlock()
	m.lastReclaimAbort = reclaimAbortRecord{
		seq:          m.lastReclaimAbort.seq + 1,
		merged:       res.Merged.ID,
		constituents: len(res.Removed),
		err:          err,
	}
}

// reclaimAbortMark reads the record as one value, for a caller that will compare it
// against a later read.
//
// THE WHOLE RECORD, NOT THE SEQUENCE ALONE, because the sequence and the details it
// belongs to must be read under one lock acquisition — a caller that took the count
// and the details separately could pair one abort's sequence with another's message.
func (m *distManager[Q, S]) reclaimAbortMark() reclaimAbortRecord {
	m.resMu.Lock()
	defer m.resMu.Unlock()
	return m.lastReclaimAbort
}

// abortedReclaimSince reports the merge reclaim this pool aborted after mark was
// taken, as an error, or nil when none was.
//
// IT NAMES WHAT STAYS ON DISK, because that is the entire consequence and it is not
// inferable from the disk error alone. A failed re-emit write and an aborted reclaim
// both surface as a Put failure, and they leave OPPOSITE states: a failed write
// leaves the new blob absent, while an aborted reclaim leaves the OLD constituents
// present beside a new blob that did land. The remedies differ accordingly, so the
// two must not read alike at the caller.
//
// THE UNDERLYING DISK ERROR IS WRAPPED, NOT RESTATED, so errors.Is still resolves it
// for a caller that wants the cause by identity.
//
// IT REPORTS A STATE THAT NOW CONVERGES, and the qualifier is worth keeping for exactly
// that reason rather than in spite of it: the constituents named here are on disk AT
// THIS INSTANT and a load between now and the pool's next search imports them, so the
// caller still owes its user the warning. What has changed since this text was written
// is the horizon — the obligation is retained and discharged on the next consumer touch
// (manager_reclaim_discharge.go), so the state is bounded by that touch rather than by
// the life of the process.
func (m *distManager[Q, S]) abortedReclaimSince(mark reclaimAbortRecord) error {
	cur := m.reclaimAbortMark()
	if cur.seq == mark.seq {
		return nil
	}
	return fmt.Errorf(
		"segmentdist: a merge reclaim ABORTED during this re-emit (graph %s/%s repo %s format %s): "+
			"the consolidated blob %s could not be persisted, so the %d constituent segment(s) it "+
			"supersedes were NOT removed and stay in this client's L2 cache beside the blob this "+
			"re-emit wrote — they are declined on that blob's supersession record rather than "+
			"imported, so they cost disk until the retained obligation discharges: %w",
		m.target.GetGraph(), m.target.GetName(), m.target.GetRepo(), m.format,
		cur.merged, cur.constituents, cur.err)
}
