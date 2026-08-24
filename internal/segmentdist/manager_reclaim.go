// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"log/slog"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/bm25"
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
// Beyond the cache it touches exactly ONE piece of bookkeeping — evictedIDs, and
// only while this pool is EVICTED (see step (b) below). It still touches no
// shippedIDs/locallyShipped/resident state: merged-away constituents that were
// shipped are reconciled server-side separately — the embed path's next
// shipAndPublish republishes the post-merge RESIDENT Export() as this writer's
// manifest (which no longer contains the merged-away ids), so the server
// reference-count-GCs them; the deterministic ROLE-A rebuild reconciles via its
// reconcilePrune leg.
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
	// shipNew Put idiom (manager_prune.go).
	m.cache.Put(res.Merged.ID, res.Merged.Bytes)
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

// remapMaxAttempts bounds the drain's re-arm on a cause a retry cannot clear on
// its own. It is the CONJUNCTION of the two in-tree precedents this terminus
// extends: coverageSkipMaxStreak, which STOPS re-arming a cause that "only
// re-reads the SAME sub-ratio resident and re-skips", and
// incompletePublishWarnStreak, which ESCALATES a per-cycle transient log into a
// loud persistent-degradation log. A mapping failure that survives one re-Put
// will not clear by re-driving the same drain, so the re-arm must stop AND an
// operator must see it.
const remapMaxAttempts = 3

// Causes a remap can fail for. They ride markRemapPending as a STRUCTURED
// ATTRIBUTE rather than as three near-duplicate messages, which is both better
// logging and what makes the single-Warn-site count deterministic.
const (
	remapCauseMapFailed = "mapping the cached blob failed"
	remapCauseNotCached = "the blob is not in the L2 cache"
	remapCauseRepublish = "republishing the mapping failed"
)

// remapAttempt is one pending republication.
//
// It CARRIES THE MERGED BYTES so the bound's one additive repair — a re-Put — is
// possible without re-deriving them from a corpus that may have moved on.
type remapAttempt struct {
	bytes    []byte
	failures int
	// rePut records that the bound already spent its single additive repair, so
	// the next failed drain is terminal rather than another re-Put.
	rePut bool
}

// remapArm is the non-generic remap-drain view of one format's pool, following
// the precedent this package already documents three times — coverageArm
// (manager_reconcile_arms.go), completenessArm (manager_completeness.go) and
// residencyArm (manager_residency.go). A FOURTH seam rather than a widening of
// any sibling is the house-documented move: each file's method set is the union
// of what ITS consumers read, and the remap drain is a different consumer with
// different needs. armFormat is reused verbatim, exactly as residencyArm reuses it.
type remapArm interface {
	armFormat() string
	drainRemapPending()
}

var (
	_ remapArm = (*distManager[[]byte, struct{}])(nil)
	_ remapArm = (*distManager[bm25.Query, *bm25.CorpusStats])(nil)
)

// remapOnce attempts the mapping swap once. It returns an empty cause on
// success, and on failure the cause plus any underlying error.
//
// A SEGMENT THAT IS NO LONGER RESIDENT IS A SUCCESS HERE, not a failure:
// RemapResident already returns nil and releases the mapping when the segment was
// superseded, evicted or pruned before the remap reached it. There is no longer a
// degraded entry to repair, so the drain drops the id — a legitimate terminus
// rather than a silent drop.
func (m *distManager[Q, S]) remapOnce(id searchengine.SegmentID) (string, error) {
	data, release, ok, err := m.cache.GetMapped(id)
	switch {
	case err != nil:
		return remapCauseMapFailed, err
	case !ok:
		return remapCauseNotCached, nil
	}
	// RemapResident TAKES the blob: it either hands the release to the new
	// entry's cleanup or calls it itself, including when it declines because the
	// segment is no longer resident. Releasing here as well would be a second
	// owner for one mapping.
	if err := m.engine.RemapResident(id, searchengine.SegmentBlob{ID: id, Bytes: data, Release: release}); err != nil {
		return remapCauseRepublish, err
	}
	return "", nil
}

// remapMerged swaps the resident merged entry's payload for a mapping of the
// cached file.
//
// A FAILURE IS RECORDED AS PENDING, NEVER LOGGED AND FORGOTTEN. The earlier shape
// warned and returned on each of the three arms below, which left a degraded
// state published and unremembered — an unapproved fallback, and one whose damage
// is invisible at every gate precisely because results stay correct. Each arm now
// marks the id pending so a later consumer touch can repair it.
func (m *distManager[Q, S]) remapMerged(merged searchengine.SegmentBlob) {
	cause, err := m.remapOnce(merged.ID)
	switch cause {
	case "":
		m.clearRemapPending(merged.ID)
	case remapCauseMapFailed:
		m.markRemapPending(merged.ID, merged.Bytes, cause, err)
	case remapCauseNotCached:
		m.markRemapPending(merged.ID, merged.Bytes, cause, err)
	default:
		m.markRemapPending(merged.ID, merged.Bytes, cause, err)
	}
}

// markRemapPending records a segment whose mapping republication failed, so a
// later drain can retry it.
//
// THIS IS THE FILE'S ONLY slog.Warn SITE, deliberately. The cause rides as a
// structured attribute rather than as three near-duplicate messages.
func (m *distManager[Q, S]) markRemapPending(id searchengine.SegmentID, blob []byte, cause string, err error) {
	m.resMu.Lock()
	a := m.remapPending[id]
	if a.bytes == nil {
		a.bytes = blob
	}
	m.remapPending[id] = a
	pending := len(m.remapPending)
	m.resMu.Unlock()

	slog.Warn("segmentdist: segment mapping not republished — retained for repair on the next consumer touch",
		"graph", m.target.GetGraph(), "name", m.target.GetName(), "repo", m.target.GetRepo(),
		"format", m.format, "segment", id, "cause", cause, "err", err, "pending", pending)
}

// clearRemapPending drops a segment from the pending set after a successful
// republication.
func (m *distManager[Q, S]) clearRemapPending(id searchengine.SegmentID) {
	m.resMu.Lock()
	delete(m.remapPending, id)
	m.resMu.Unlock()
}

// drainRemapPending retries every pending republication for this pool. It is the
// NEXT-TOUCH convergence the house defaults to: called from Manager.Search after
// the pool read locks are released, beside the residency budget pass.
//
// FOUR TERMINAL CONDITIONS, all reached here:
//  1. the remap SUCCEEDS — the primary path is restored and the id is dropped;
//  2. the segment is NO LONGER RESIDENT — RemapResident declines and releases, so
//     there is no degraded entry left to repair and the id is dropped;
//  3. a TRANSIENT cause clears, which resolves into (1);
//  4. the BOUND fires — one additive re-Put, then terminal.
//
// THE BOUND IS STRICTLY NON-DESTRUCTIVE, and that is the whole design. The only
// repair it may attempt is a re-Put of the merged bytes, which can only ADD a
// durable copy and never remove one. It MUST NOT remove the blob from L2 (post-
// merge that blob is the ONLY durable copy of its constituents' documents), MUST
// NOT clear l2Loaded (the heal it would be reaching for is not reachable from
// distManager.load at all), MUST NOT leave a hole in L2 (evictResident's
// re-materializability gate is all-or-nothing, so one absent id disarms eviction
// for the WHOLE pool), and MUST NOT call onCoverageSuppressed (that would corrupt
// the publish gate's own re-arm state). The failure being repaired is a lost
// memory property on a CORRECT, COMPLETE corpus, so any repair that risks a
// document or disarms a control is strictly worse than the bug.
//
// CONVERGENCE, stated honestly: the SYSTEM converges — to a bounded, announced,
// correct-but-heap-resident state — rather than looping forever or silently
// forfeiting the property.
func (m *distManager[Q, S]) drainRemapPending() {
	m.resMu.Lock()
	if len(m.remapPending) == 0 {
		m.resMu.Unlock()
		return
	}
	ids := make([]searchengine.SegmentID, 0, len(m.remapPending))
	for id := range m.remapPending {
		ids = append(ids, id)
	}
	m.resMu.Unlock()

	for _, id := range ids {
		cause, err := m.remapOnce(id)
		if cause == "" {
			m.clearRemapPending(id)
			slog.Info("segmentdist: segment mapping republished on a later consumer touch",
				"graph", m.target.GetGraph(), "name", m.target.GetName(), "repo", m.target.GetRepo(),
				"format", m.format, "segment", id)
			continue
		}

		m.resMu.Lock()
		a, still := m.remapPending[id]
		if !still {
			m.resMu.Unlock()
			continue
		}
		a.failures++
		switch {
		case a.rePut:
			// TERMINAL. The single additive repair was already spent and the
			// cause survived it, so this pool cannot repair itself. The CORRECT
			// heap-backed payload stays published; only the memory property is
			// forfeited, and it is announced rather than absorbed.
			delete(m.remapPending, id)
			m.resMu.Unlock()
			slog.Error("segmentdist: segment mapping PERSISTENTLY unrepublished — the segment stays correct but heap-resident for the life of this process",
				"graph", m.target.GetGraph(), "name", m.target.GetName(), "repo", m.target.GetRepo(),
				"format", m.format, "segment", id, "cause", cause, "err", err,
				"attempts", a.failures)
		case a.failures >= remapMaxAttempts:
			// THE ONE ADDITIVE REPAIR: re-Put the merged bytes, covering a torn
			// or evicted L2 copy. Additive only — it cannot remove a copy.
			a.rePut = true
			m.remapPending[id] = a
			blob := a.bytes
			m.resMu.Unlock()
			if blob != nil {
				m.cache.Put(id, blob)
			}
		default:
			m.remapPending[id] = a
			m.resMu.Unlock()
		}
	}
}
