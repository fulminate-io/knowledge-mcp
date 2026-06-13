// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// ship exports every current segment, diffs against shippedIDs (the
// server-seeded diff-suppression set), and ships ONLY the new content-hash blobs
// in one batched Ship. An empty diff is a NO-OP for the ship leg: zero RPC, zero
// generation, zero bytes. On the response it warms the L2 cache, marks each id
// shipped (in BOTH shippedIDs and locallyShipped), and advances last-seen
// generation.
//
// reconcile-on-ship: after the ship-new leg, ship() also PRUNES the stale
// segments via reconcilePrune. The PRUNE ROLE is chosen by the CALLER via the
// `against` argument — ship() is the single shared method, and which set it
// prunes against is a property of the call:
//
//   - embed/tail path (AddAndShip/AddAndShipFields/Flush) passes locallyShipped
//     (ROLE B): prune only this-process merged-away ids. A fresh process's
//     locallyShipped is empty, so it can NEVER prune the prior corpus — the
//     segment-ship restart false-prune fix.
//   - deterministic rebuild (FlushDeterministic) passes shippedIDs (ROLE A): the
//     authoritative replace-prune — its Export() is the complete rebuilt corpus,
//     so shippedIDs − Export() is exactly the old corpus it superseded.
//
// Ship-new runs FIRST so the consolidated/rebuilt blobs land before their
// predecessors are pruned (never a server gap), then Prune removes the stale ids.
// Both legs are independently zero-RPC when there is nothing to do.
//
// ROLE-B post-load corpus-merge leak (ACCEPTED + DOCUMENTED, not silent): a
// Search/VectorByID load() of the shared embed engine pulls the server corpus
// into the SAME engine instance the embed path ships from, so the in-process
// merger can consolidate corpus segments whose ids are NOT in locallyShipped.
// Those merged-away corpus ids are then NOT pruned by the embed (ROLE-B) ship —
// a BOUNDED leak: it is reclaimed by the next ROLE-A rebuild OR coverage heal
// (lever 2), and across restart cycles the stale set is bounded by the HEAL
// CADENCE, not a single rebuild event. (The alternative — a loaded-then-merged
// provenance set — needs new searchengine merge-callback plumbing rather than
// free bookkeeping, and the leak requires >16×~1024 loaded docs to fire and
// self-corrects via lever 2, so it is accepted here.)
//
// RETURN: the set of superseded segment ids reconcilePrune dropped server-side.
// The deterministic rebuild path propagates these up to
// Manager.FlushDeterministic → the driver → InvalidateLocal so the local L2
// .seg files for the superseded ids are evicted (they would otherwise orphan
// until LRU, which never fires on an unbounded cache). Every other (embed/
// migration) caller discards the slice — the server-side prune is the behavior
// they already had.
func (m *distManager[Q, S]) ship(
	ctx context.Context, against map[searchengine.SegmentID]struct{},
) ([]searchengine.SegmentID, error) {
	if err := m.ensureShippedSeeded(ctx); err != nil {
		return nil, err
	}

	all := m.engine.Export()

	m.shipMu.Lock()
	var diff []*knowledgev1.SegmentBlobProto
	diffBlobs := make(map[string]searchengine.SegmentBlob)
	for _, b := range all {
		if _, sent := m.shippedIDs[b.ID]; sent {
			continue
		}
		diff = append(diff, blobToProto(b))
		diffBlobs[b.ID] = b
	}
	m.shipMu.Unlock()

	// Ship-new FIRST (skips the RPC when the diff is empty but does NOT return —
	// the prune leg below must still reconcile a merge whose consolidated blob was
	// shipped on an earlier pass).
	if err := m.shipNew(ctx, diff, diffBlobs); err != nil {
		return nil, err
	}

	// reconcile-on-ship prune: drop the stale ids in `against` the engine no
	// longer Exports, returning the pruned id set up the stack.
	return m.reconcilePrune(all, against)
}

// shipNew ships the new-content-hash blobs in one batched Ship, warms the L2
// cache from the stamped response, marks each id shipped, and advances shippedGen
// (TRACKING ONLY — NOT the load floor). An empty diff is a NO-OP: zero RPC, zero
// generation, zero bytes.
//
// CRITICAL: shipNew advances shippedGen, NEVER importedGen. The old single shared
// cursor let this advance poison the load floor — on a cold process the embed
// ship stamped the fresh tail at the server's monotonic generation N and pushed
// the shared cursor to N before any search, so the first lazy load()'s List(N)
// returned an empty delta and the N stored blobs were never imported (search
// served a ~2-doc tail). With the cursors decoupled, load() still Lists from
// importedGen==0 on a cold process and imports the full corpus. Re-listing this
// process's own shipped tail is now safe: Import is idempotent by segment id
// (searchengine publishImport drops an already-resident segment), so the tail is
// never double-imported.
func (m *distManager[Q, S]) shipNew(
	ctx context.Context,
	diff []*knowledgev1.SegmentBlobProto,
	diffBlobs map[string]searchengine.SegmentBlob,
) error {
	if len(diff) == 0 {
		return nil
	}

	resp, err := m.source.caller.Ship(ctx, &knowledgev1.ShipRequest{
		Target: m.target,
		Blobs:  diff,
	})
	if err != nil {
		return err
	}

	m.shipMu.Lock()
	var maxGen uint64
	for _, meta := range resp.GetStamped() {
		m.shippedIDs[meta.GetId()] = struct{}{}
		// locallyShipped records ONLY ids this process actually shipped — the
		// ROLE-B (embed) prune-eligible set. Populated regardless of the caller's
		// prune role (a ROLE-A rebuild ship keeps it current too, harmless).
		m.locallyShipped[meta.GetId()] = struct{}{}
		if b, ok := diffBlobs[meta.GetId()]; ok {
			m.cache.Put(meta.GetId(), b.Bytes)
		}
		if meta.GetGeneration() > maxGen {
			maxGen = meta.GetGeneration()
		}
	}
	m.shipMu.Unlock()

	m.advanceGen(&m.shippedGen, maxGen)
	return nil
}

// reconcilePrune is the INVERSE of the ship-new diff: it computes the set of ids
// in the caller-supplied `against` set that the engine no longer Exports — the
// segments a merge or a corpus replacement made stale — Prunes them on the server
// so the server segment set stays BOUNDED, then drops them from BOTH shippedIDs
// and locallyShipped (keeping the two views consistent). Empty pruneSet → ZERO
// Prune RPC (mirrors the empty-diff zero-Ship fast path). Runs under the
// already-held shipMu lifecycle — no new lock; the O(|against|) walk is cheap.
//
// The `against` set encodes the caller's PRUNE ROLE — the two roles must not be
// collapsed:
//
//   - ROLE A (replace-prune, against = shippedIDs): authoritative full-corpus
//     replacement. Used by the deterministic rebuild (FlushDeterministic), whose
//     Export() IS the complete rebuilt corpus, so shippedIDs − Export() is exactly
//     the old corpus the rebuild superseded. The rebuild error-gates BEFORE this
//     ship, so a partial Export never prunes. Sound even on a fresh process: the
//     authority is the complete Export, not what this process previously shipped.
//
//   - ROLE B (merge-reconcile guard, against = locallyShipped): used by the
//     embed/tail path, whose Export() is only THIS process's sealed tail.
//     locallyShipped holds only ids this process shipped, seeded empty, so a fresh
//     process can only ever prune its own merged-away tail segments — never the
//     prior server corpus it did not ship. This is the segment-ship restart
//     false-prune fix: a never-loaded producer no longer prunes the whole corpus.
//
// RETURN: the pruneSet it computed + dropped (nil on the empty fast path). ship()
// surfaces this up the stack so the deterministic rebuild path can invalidate the
// superseded ids from the local L2 cache.
func (m *distManager[Q, S]) reconcilePrune(
	all []searchengine.SegmentBlob, against map[searchengine.SegmentID]struct{},
) ([]searchengine.SegmentID, error) {
	exportedIDs := make(map[searchengine.SegmentID]struct{}, len(all))
	for _, b := range all {
		exportedIDs[b.ID] = struct{}{}
	}

	m.shipMu.Lock()
	var pruneSet []searchengine.SegmentID
	for id := range against {
		if _, live := exportedIDs[id]; !live {
			pruneSet = append(pruneSet, id)
		}
	}
	m.shipMu.Unlock()

	if len(pruneSet) == 0 {
		return nil, nil
	}

	if _, err := m.source.Prune(pruneSet); err != nil {
		return nil, err
	}

	m.shipMu.Lock()
	for _, id := range pruneSet {
		delete(m.shippedIDs, id)
		delete(m.locallyShipped, id)
	}
	m.shipMu.Unlock()
	return pruneSet, nil
}
