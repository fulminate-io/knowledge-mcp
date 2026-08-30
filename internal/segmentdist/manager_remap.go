// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"errors"
	"fmt"
	"log/slog"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/bm25"
)

// manager_remap.go carries the MAPPING REPUBLICATION half of a completed merge:
// swapping a resident merged entry's payload for a mapping of its stored file,
// remembering the swaps that failed, and draining them on a later consumer touch
// under a bound. The reclaim itself — persisting the merged blob and removing the
// constituents it superseded — is in manager_reclaim.go.

// remapMaxAttempts bounds the drain's re-arm on a cause a retry cannot clear on its
// own, and it does TWO things at that bound: it STOPS re-arming, and it escalates to
// a loud persistent-degradation log so an operator sees it.
//
// BOTH HALVES ARE ARGUED FROM THE MECHANISM, NOT FROM PRECEDENT. This constant used
// to justify itself as the conjunction of two publish-path streak constants; both
// went with the publish, and a rationale resting entirely on deleted
// siblings is a rationale a reader cannot check. The standing argument is simpler and
// local: a mapping failure that survives the one additive repair (the re-Put at
// remapPending's terminal branch) is not a transient — re-driving the same drain
// re-reads the same blob through the same seam and fails the same way — so retrying
// past that point cannot converge. A lane that can fire forever on one cause is
// hiding a defect rather than handling one, which is why the bound stops AND
// announces instead of quietly continuing.
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
// It CARRIES THE MERGED BLOB so the bound's one additive repair — a re-Put — is
// possible without re-deriving it from a corpus that may have moved on.
//
// IT RETAINS THE WHOLE SegmentBlob RATHER THAN ITS BYTES, and that is a
// use-after-unmap fix rather than a tidier field. The merged payload is now a
// MAPPING owned by the resident entry, and holding a byte slice does not make
// that entry reachable — the cleanup that unmaps is keyed on the ENTRY's
// reachability, so a retained raw slice could be read after its mapping was
// released. SegmentBlob's keepAlive pins the entry, which pins the mapping. A raw
// slice would compile, pass every existing test, and fault only when the cleanup
// happened to run. reclaimAttempt already retains a blob for the same reason;
// this matches it rather than inventing a second shape.
//
// THE RETENTION COST, stated rather than hidden: a pending remap pins one merged
// mapping — address space plus the unlinked scratch file's blocks — until the
// drain resolves, bounded by remapMaxAttempts and the next consumer touch. That
// is the same order as the heap retention it replaces and strictly cheaper,
// because page-cache pages are evictable where heap bytes are not.
type remapAttempt struct {
	blob     searchengine.SegmentBlob
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
	drainRemapPending() error
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
	// THE MAPPED FILE IS SPLIT INTO TWO ZERO-COPY SUBSLICES of the same mapping,
	// so the blob satisfies SegmentBlob's invariant — Bytes is the payload alone —
	// without copying either half off the mapping.
	envelope, payload, err := searchengine.SplitStoredBlob(data)
	if err != nil {
		// The blob never reaches RemapResident, so nothing else will ever own this
		// mapping — release it here rather than stranding it.
		if release != nil {
			release()
		}
		return remapCauseRepublish, err
	}
	if err := m.engine.RemapResident(id, searchengine.SegmentBlob{
		ID: id, Bytes: payload, Envelope: envelope, Release: release,
	}); err != nil {
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
		m.markRemapPending(merged, cause, err)
	case remapCauseNotCached:
		m.markRemapPending(merged, cause, err)
	default:
		m.markRemapPending(merged, cause, err)
	}
}

// markRemapPending records a segment whose mapping republication failed, so a
// later drain can retry it.
//
// THIS IS THE FILE'S ONLY slog.Warn SITE, deliberately. The cause rides as a
// structured attribute rather than as three near-duplicate messages.
func (m *distManager[Q, S]) markRemapPending(blob searchengine.SegmentBlob, cause string, err error) {
	id := blob.ID
	m.resMu.Lock()
	a := m.remapPending[id]
	if a.blob.Bytes == nil {
		a.blob = blob
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
// IT RETURNS ITS WRITE FAILURES RATHER THAN ABSORBING THEM, and the two halves of
// that sentence are separate decisions. Each per-segment repair CONTINUES past a
// failure, because the repairs are independent and aborting the loop would forfeit
// every other segment's repair over one segment's bad write. But continuation is not
// absorption: the failures are collected with errors.Join and RETURNED, so the caller
// decides what a failed repair means at a level that can see the whole operation. A
// Put error is never logged-and-walked-past at the write site.
func (m *distManager[Q, S]) drainRemapPending() error {
	m.resMu.Lock()
	if len(m.remapPending) == 0 {
		m.resMu.Unlock()
		return nil
	}
	ids := make([]searchengine.SegmentID, 0, len(m.remapPending))
	for id := range m.remapPending {
		ids = append(ids, id)
	}
	m.resMu.Unlock()

	// repairErrs collects per-segment write failures so one bad blob does not cost
	// the others their repair. It is JOINED and returned, never dropped.
	var repairErrs []error

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
			blob := a.blob
			m.resMu.Unlock()
			if blob.Bytes != nil {
				// WHY A FAILURE HERE DOES NOT STOP THE LOOP: the repair is ADDITIVE and
				// per-segment. It cannot make things worse — the payload is already
				// correct and served from the heap, and only the memory property is at
				// stake — so one segment's bad write must not cost every other pending
				// segment its repair. That is an argument for CONTINUING, and it is not
				// an argument for discarding the error: the failure is collected and
				// returned, because a silently-failed re-Put leaves this pool waiting on
				// a repair that already gave up.
				if perr := m.cache.Put(id, blob.Envelope, blob.Bytes); perr != nil {
					repairErrs = append(repairErrs, fmt.Errorf(
						"segmentdist: segment mapping repair could not re-persist blob %s (graph %s/%s repo %s format %s) — "+
							"the segment stays correct but heap-resident: %w",
						id, m.target.GetGraph(), m.target.GetName(), m.target.GetRepo(), m.format, perr))
				}
			}
		default:
			m.remapPending[id] = a
			m.resMu.Unlock()
		}
	}

	// errors.Join returns nil for an all-nil slice, so a clean drain reports nil
	// without a length check.
	return errors.Join(repairErrs...)
}
