// SPDX-License-Identifier: Apache-2.0

// manager_reclaim_discharge.go makes an ABORTED merge reclaim RECOVERABLE. The abort
// itself is unchanged and stays where it is (manager_reclaim.go): a Put that fails
// removes NOT ONE constituent, because post-merge the consolidated blob is the only
// durable copy of their documents and a Remove after a failed Put is the whole corpus
// segment gone. What this file adds is what happens AFTERWARDS — the merge's
// supersession obligation is RETAINED instead of discarded, and discharged on a later
// consumer touch.
//
// WHY IT HAD TO BE RETAINED AT ALL, measured rather than argued: an un-reclaimed
// constituent is not swept by anything else. PruneCache's live set is force-loaded
// FROM the same L2 index it diffs against, so every stored id is live by construction
// and no stored blob can ever be classified an orphan (prune_cache.go's reap
// paragraph, measured by TestPruneCacheCannotReapAnUnreclaimedMergeConstituent). The
// merge that superseded these constituents will not run again — the entries it
// consumed are gone from the engine — so nothing but this record names them.
//
// IT IS THE remapPending SHAPE, DELIBERATELY. That seam already answers every
// structural question this one asks: a per-pool pending map under resMu, a bounded
// number of attempts, a drain driven from Manager.Search after the pool locks are
// released, and a loud terminal rather than a lane that re-arms forever on a cause a
// retry cannot clear.

package segmentdist

import (
	"errors"
	"fmt"
	"log/slog"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/bm25"
)

// reclaimMaxAttempts bounds the discharge's re-arm, and it bounds it for the same
// reason remapMaxAttempts does rather than by analogy to it: the one repair a
// discharge has IS the re-Put, so a cause that survives three of them is not a
// transient — re-driving the drain writes the same bytes through the same seam and
// fails the same way. A lane that can fire forever on one cause is hiding a defect
// rather than handling one, so the bound STOPS and ANNOUNCES.
//
// WHAT THE BOUND FORFEITS IS EXACTLY TODAY'S BEHAVIOUR, which is why stopping is
// admissible here: the constituents stay on disk for the life of the process, the
// state this seam exists to make recoverable. It is announced at ERROR naming the ids,
// where before it was neither announced nor recorded.
const reclaimMaxAttempts = 3

// reclaimAttempt is one merge supersession obligation this pool has not yet
// discharged: the consolidated blob that must land, and the constituent ids it
// supersedes.
//
// IT KEEPS THE WHOLE SegmentBlob RATHER THAN ITS BYTES. On a mapped payload the bytes
// ARE the mapping, and SegmentBlob carries the pin that keeps the entry backing that
// mapping reachable; retaining the bytes alone would drop the pin and leave a later
// re-Put reading unmapped memory the moment the entry's cleanup ran. The blob is heap-
// backed on today's merge path (the consolidated entry is freshly built, and
// remapMerged is what later turns it into a mapping), so the pin is carried for a
// property this code must not depend on holding by accident.
type reclaimAttempt struct {
	merged   searchengine.SegmentBlob
	removed  []searchengine.SegmentID
	failures int
}

// reclaimArm is the non-generic discharge-drain view of one format's pool, a SIBLING
// of remapArm rather than a widening of it — the fifth seam following the precedent
// remapArm's own doc records (coverageArm, completenessArm, residencyArm, remapArm).
// Its method set is what ITS consumer reads and nothing else: Manager.Search drains
// both formats' obligations and reads no other method here.
type reclaimArm interface {
	drainReclaimPending() error
}

var (
	_ reclaimArm = (*distManager[[]byte, struct{}])(nil)
	_ reclaimArm = (*distManager[bm25.Query, *bm25.CorpusStats])(nil)
)

// retainReclaimObligation records the supersession obligation an aborted reclaim could
// not discharge, so a later consumer touch can. It is called from reclaimMerged's
// abort branch ONLY, immediately after noteReclaimAbort — the record and the report are
// two halves of the same event and neither substitutes for the other: the record is
// what CONVERGES the state, the report is what tells this re-emit's caller the state
// exists right now.
//
// KEYED BY THE MERGED BLOB'S ID, so a merge that aborts twice for the same output
// replaces its own entry instead of accumulating one per attempt. Distinct merges hold
// distinct obligations, because their constituents are disjoint.
func (m *distManager[Q, S]) retainReclaimObligation(res searchengine.MergeResult) {
	m.resMu.Lock()
	a := m.reclaimPending[res.Merged.ID]
	a.merged = res.Merged
	a.removed = append([]searchengine.SegmentID(nil), res.Removed...)
	m.reclaimPending[res.Merged.ID] = a
	pending := len(m.reclaimPending)
	m.resMu.Unlock()

	slog.Warn("segmentdist: merge supersession obligation RETAINED for discharge on the next consumer touch",
		"graph", m.target.GetGraph(), "name", m.target.GetName(), "repo", m.target.GetRepo(),
		"format", m.format, "merged", res.Merged.ID, "constituents", len(res.Removed),
		"pending", pending)
}

// drainReclaimPending retries every retained supersession obligation for this pool. It
// is the NEXT-TOUCH convergence the house defaults to: called from Manager.Search after
// the pool read locks are released, beside the mapping-repair drain.
//
// THREE TERMINAL CONDITIONS, all reached here:
//  1. the discharge SUCCEEDS — the consolidated blob lands and its constituents are
//     reclaimed, which is the outcome the original reclaim owed;
//  2. NOTHING IS OWED — every constituent has already left L2 (a later re-emit
//     superseded them by content, or an operator wiped the cache), so there is no
//     removal left to make and the obligation is dropped. This is a legitimate
//     terminus, exactly as remapOnce treats a no-longer-resident segment;
//  3. the BOUND fires — reclaimMaxAttempts failed re-Puts, after which the obligation
//     is dropped and the permanent state is announced at ERROR.
//
// THE ORDERING INSIDE A DISCHARGE IS reclaimMerged's OWN, and it must stay that way:
// Put the consolidated blob FIRST, then remove the constituents, so a crash at any
// point leaves either {constituents present} or {merged present} on disk and never
// NEITHER. A discharge that removed first would defeat by hand the exact ordering the
// abort exists to protect.
//
// IT RETURNS ITS WRITE FAILURES RATHER THAN ABSORBING THEM, for the reason
// drainRemapPending states: each obligation is independent, so one bad write must not
// cost the others their discharge, but continuing is not absorbing — the failures are
// joined and returned so the caller decides what a failed discharge means at a level
// that can see the whole operation.
func (m *distManager[Q, S]) drainReclaimPending() error {
	m.resMu.Lock()
	if len(m.reclaimPending) == 0 {
		m.resMu.Unlock()
		return nil
	}
	ids := make([]searchengine.SegmentID, 0, len(m.reclaimPending))
	for id := range m.reclaimPending {
		ids = append(ids, id)
	}
	m.resMu.Unlock()

	var dischargeErrs []error
	for _, id := range ids {
		m.resMu.Lock()
		a, still := m.reclaimPending[id]
		m.resMu.Unlock()
		if !still {
			continue
		}

		owed := m.constituentsStillStored(a.removed)
		if len(owed) == 0 {
			m.dropReclaimPending(id)
			continue // (2) nothing left to reclaim — a terminus, not a failure
		}

		if err := m.dischargeReclaim(a, owed); err != nil {
			dischargeErrs = append(dischargeErrs, err)
			m.noteReclaimDischargeFailure(id, len(owed), err)
			continue
		}

		m.dropReclaimPending(id)
		slog.Info("segmentdist: aborted merge reclaim DISCHARGED on a later consumer touch",
			"graph", m.target.GetGraph(), "name", m.target.GetName(), "repo", m.target.GetRepo(),
			"format", m.format, "merged", a.merged.ID, "constituents", len(owed))
	}

	// errors.Join returns nil for an all-nil slice, so a clean drain reports nil
	// without a length check.
	return errors.Join(dischargeErrs...)
}

// constituentsStillStored narrows an obligation to the constituents L2 STILL holds.
//
// THE NARROWING IS WHAT KEEPS THE DISCHARGE HONEST rather than merely tidy: an
// obligation whose constituents are all gone is owed nothing, and re-Putting a
// superseded consolidated blob to satisfy it would ADD a stale segment to a corpus
// that had already converged.
func (m *distManager[Q, S]) constituentsStillStored(
	removed []searchengine.SegmentID,
) []searchengine.SegmentID {
	owed := make([]searchengine.SegmentID, 0, len(removed))
	for _, id := range removed {
		if _, stored := m.cache.sizeOf(id); stored {
			owed = append(owed, id)
		}
	}
	return owed
}

// dischargeReclaim runs the steps the aborted reclaim never reached, in reclaimMerged's
// own order: (a) persist the consolidated blob, (b) rewrite the strict-reload id set,
// (c) republish the merged payload as a mapping, then reclaim the constituents.
//
// IT UNLOADS THE CONSTITUENTS FROM THE ENGINE BEFORE IT UNLINKS THEM, which reclaimMerged
// does not need to do and this must. At merge time the constituents were already gone
// from the engine — the CAS swap replaced them — but between the abort and this discharge
// an ordinary load() re-imported the whole L2 index, and that index still held them. A
// removal that left them resident would leave the pool serving segments with no file
// behind them, which breaks the property persistResident establishes and disarms
// eviction for the WHOLE pool (evictResident's re-materializability gate is
// all-or-nothing). Unloading is safe on the same argument the removal is: the
// consolidated blob just persisted carries every live document those constituents held.
func (m *distManager[Q, S]) dischargeReclaim(a reclaimAttempt, owed []searchengine.SegmentID) error {
	if err := m.cache.Put(a.merged.ID, a.merged.Envelope, a.merged.Bytes); err != nil {
		return fmt.Errorf(
			"segmentdist: aborted merge reclaim could not be discharged (graph %s/%s repo %s format %s): "+
				"the consolidated blob %s still could not be persisted, so the %d constituent segment(s) "+
				"it supersedes stay in this client's L2 cache: %w",
			m.target.GetGraph(), m.target.GetName(), m.target.GetRepo(), m.format,
			a.merged.ID, len(owed), err)
	}
	res := searchengine.MergeResult{Merged: a.merged, Removed: owed}
	m.rewriteEvictedIDsForMerge(res)
	m.remapMerged(a.merged)
	m.engine.Unload(owed)
	for _, id := range owed {
		m.cache.Remove(id)
	}
	return nil
}

// noteReclaimDischargeFailure counts a failed discharge and, at the bound, drops the
// obligation with a loud terminal.
//
// THE DROP IS THE ANNOUNCED FORFEIT, not a silent one: what is lost is the reclaim of
// constituents that are still perfectly readable, so the corpus stays CORRECT and
// merely carries dead weight — the state this pool was in before the obligation was
// retained at all. What must never happen instead is a lane that keeps re-arming on a
// disk that is not coming back.
func (m *distManager[Q, S]) noteReclaimDischargeFailure(
	id searchengine.SegmentID, owed int, err error,
) {
	m.resMu.Lock()
	a, still := m.reclaimPending[id]
	if !still {
		m.resMu.Unlock()
		return
	}
	a.failures++
	terminal := a.failures >= reclaimMaxAttempts
	if terminal {
		delete(m.reclaimPending, id)
	} else {
		m.reclaimPending[id] = a
	}
	attempts := a.failures
	m.resMu.Unlock()

	if !terminal {
		return
	}
	slog.Error("segmentdist: aborted merge reclaim PERSISTENTLY undischargeable — its superseded constituents stay in this client's L2 cache for the life of this process",
		"graph", m.target.GetGraph(), "name", m.target.GetName(), "repo", m.target.GetRepo(),
		"format", m.format, "merged", id, "constituents", owed,
		"attempts", attempts, "err", err)
}

// dropReclaimPending removes a discharged obligation from the pending set.
func (m *distManager[Q, S]) dropReclaimPending(id searchengine.SegmentID) {
	m.resMu.Lock()
	delete(m.reclaimPending, id)
	m.resMu.Unlock()
}
