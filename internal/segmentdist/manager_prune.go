// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"errors"
	"log/slog"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// ship exports every current segment, diffs against shippedIDs (the
// server-seeded diff-suppression set), and ships ONLY the new content-hash blobs,
// byte-packed into successive ≤64 MiB ShipRequests (one Ship RPC per sub-batch).
// An empty diff is a NO-OP for the ship leg: zero RPC, zero generation, zero
// bytes. On each response it warms the L2 cache, marks each id shipped (in BOTH
// shippedIDs and locallyShipped), and advances last-seen generation.
//
// SUPERSEDED ON THE PRODUCTION PATH: the registry-model cutover moved
// every production ship caller — the embed path (AddAndShip/AddAndShipFields/
// Flush) AND the deterministic rebuild (FlushDeterministic) — onto shipAndPublish,
// which ships the new blobs then PUBLISHES the resident live set as the writer's
// manifest (the server refcount-GCs what dropped out). ship()+reconcilePrune are
// retained as the diff-prune mechanism that the segmentdist unit tests exercise
// directly (they push segments and assert the diff/reconcile semantics); they are
// no longer reached by any production caller. The publishResident path is the
// production reclaim authority.
//
// reconcile-on-ship: after the ship-new leg, ship() also PRUNES the stale
// segments via reconcilePrune. The PRUNE ROLE is chosen by the CALLER via the
// `against` argument:
//
//   - ROLE B (against = locallyShipped): prune only this-process merged-away ids.
//     A fresh process's locallyShipped is empty, so it can NEVER prune the prior
//     corpus — the segment-ship restart false-prune fix.
//   - ROLE A (against = shippedIDs): the authoritative replace-prune — the
//     Export() is the complete corpus, so shippedIDs − Export() is exactly the old
//     corpus it superseded.
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

// shipNew ships the new-content-hash blobs, byte-packed into successive
// ≤64 MiB ShipRequests (one Ship RPC per sub-batch so no request body crosses
// the cloud cap), warms the L2 cache from each stamped response, marks each id
// shipped, and advances shippedGen ONCE to the max stamped generation across the
// whole ship (TRACKING ONLY — NOT the load floor). An empty diff is a NO-OP:
// zero RPC, zero generation, zero bytes. A small diff packs into a single
// sub-batch — exactly one Ship RPC, the prior behavior.
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

	// Byte-pack the diff into successive ≤64 MiB ShipRequests so no single request
	// body crosses the cloud cap (the Cloudflare-fronted endpoint 413s oversize
	// bodies). Each sub-batch is a separate Ship RPC; the per-response bookkeeping
	// (stamp shippedIDs/locallyShipped, warm the L2 cache, track the max stamped
	// gen) runs per sub-batch, and advanceGen fires ONCE after the loop with the
	// highest stamped generation across the whole ship (current semantics). shipMu
	// is held only around each response's bookkeeping, NEVER across the Ship RPC.
	var maxGen uint64
	for _, sub := range BatchSegmentBlobs(diff, kgwire.MaxCloudRequestBytes) {
		stamped, err := m.source.Ship(ctx, sub)
		if err != nil {
			return err
		}

		m.shipMu.Lock()
		for _, meta := range stamped {
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
	}

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

// shipAndPublish is the embed/tail ship path's REGISTRY-MODEL replacement for the
// diff-prune reconcile: it ships the new-content-hash blobs (the same
// ship-new leg as ship()), then PUBLISHES this writer's current RESIDENT live set
// as its manifest so the server reference-count-GCs whatever dropped out of the
// live set. Unlike reconcilePrune — which deletes the diff of a caller-supplied
// `against` set — the published manifest is the AUTHORITATIVE live set, and the
// server reaps a blob only when NO writer's manifest references it (multi-writer
// safe by construction).
//
// extraReferenced carries sibling-engine resident digests (id + doc_count) that
// share this format + graphKey and must stay referenced by THIS manifest (the HNSW
// embed∪deterministic union — both engines key one "hnsw" manifest, so the embed
// publish must include the deterministic engine's resident digests or it would reap
// them). It is nil for formats with a single engine (BM25).
//
// CRITICAL: the published set is the RESIDENT m.engine.Export(),
// NOT a force-reloaded set. A force-reload (List(0)+load+Export) re-imports
// merged-away constituents via publishImport, so they would resurface in the
// manifest and never be reaped — breaking the bounded-server-set property. The
// resident Export already omits merged-away constituents (the merge CAS removed
// them), so the manifest omits them → their refcount drops to zero → GC reaps.
//
// reconcileAgainst encodes the caller's ROLE exactly as reconcilePrune's `against`
// set does — it selects WHICH bookkeeping set is diffed against the published live
// set to compute the dropped (superseded) ids returned + dropped from local
// bookkeeping:
//
//   - ROLE B (embed/tail, against = m.locallyShipped): a fresh process's
//     locallyShipped is empty, so it can only ever report its own merged-away tail
//     — never the prior corpus it re-imported. The restart-tail guard.
//   - ROLE A (deterministic rebuild, against = m.shippedIDs): the rebuild's
//     resident Export IS the complete new corpus, so shippedIDs − liveSet is the
//     old corpus it superseded — returned so FlushDeterministic feeds it to
//     InvalidateLocal for local L2 eviction.
//
// RETURN: the ids that dropped out (per the role's set). The empty-set / coverage
// gate inside publishResident protects against a degenerate publish.
func (m *distManager[Q, S]) shipAndPublish(
	ctx context.Context, extraReferenced []segmentDigest,
	reconcileAgainst map[searchengine.SegmentID]struct{},
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

	// Ship-new FIRST so the consolidated blobs land before the publish reaps their
	// merged-away predecessors (never a server gap).
	if err := m.shipNew(ctx, diff, diffBlobs); err != nil {
		return nil, err
	}

	return m.publishResident(ctx, all, extraReferenced, reconcileAgainst)
}

// publishResident publishes the union of this writer's resident live set (the
// ids in `all` = m.engine.Export()) and extraReferenced as this writer's manifest
// for (graphKey, writerID, format), then reconciles the local bookkeeping. It
// returns the ids in reconcileAgainst that dropped out of the published set (the
// caller's ROLE choice — locallyShipped for the embed path, shippedIDs for the
// deterministic rebuild). Separated from shipAndPublish so the merge/reclaim paths
// can re-publish without re-running the ship-new diff.
func (m *distManager[Q, S]) publishResident(
	ctx context.Context, all []searchengine.SegmentBlob, extraReferenced []segmentDigest,
	reconcileAgainst map[searchengine.SegmentID]struct{},
) ([]searchengine.SegmentID, error) {
	liveSet := make(map[searchengine.SegmentID]struct{}, len(all)+len(extraReferenced))
	// manifestDigests carries the per-digest doc_count to the wire (the GCS manifest
	// stores it as the coverage-read denominator; the RPC path drops it). manifestIDs
	// is the id-only view the coverage gate + dropped-reconcile below still use.
	manifestDigests := make([]segmentDigest, 0, len(all)+len(extraReferenced))
	manifestIDs := make([]searchengine.SegmentID, 0, len(all)+len(extraReferenced))
	for _, b := range all {
		if _, dup := liveSet[b.ID]; dup {
			continue
		}
		liveSet[b.ID] = struct{}{}
		manifestDigests = append(manifestDigests, segmentDigest{ID: b.ID, DocCount: b.DocCount})
		manifestIDs = append(manifestIDs, b.ID)
	}
	for _, d := range extraReferenced {
		if _, dup := liveSet[d.ID]; dup {
			continue
		}
		liveSet[d.ID] = struct{}{}
		manifestDigests = append(manifestDigests, d)
		manifestIDs = append(manifestIDs, d.ID)
	}

	// SAFETY GATE (empty/degenerate-publish corpus-wipe guard): a publish swaps this
	// writer's manifest and drives a refcount-GC, so a DEGENERATE live set (empty
	// Export, a partial/incomplete load, a not-yet-loaded fresh process) must NEVER
	// reach PublishManifest or it would wipe the prior corpus. Two checks gate it:
	//
	//   (1) NON-EMPTY + COVERAGE-RATIO FLOOR: an empty manifest, or a resident set
	//       far below the server's shipped doc count for this format, is rejected.
	//       This reuses the read-side coverage backstop policy verbatim
	//       (publishCoverageOK → shippedDocCountForRatio + residentBackstopFloor/
	//       Ratio with the conservative-unknown + tiny-graph disarm) so the publish
	//       path and the read-side recoverIfDegenerate share one coverage policy.
	//   (2) SUBSET-COMPLETENESS: the live set must be a subset of the server's
	//       List(0) for this format. A live set holding ids the server lacks signals
	//       an incomplete/suspect view — skip rather than publish against it.
	//
	// On a skip the prior manifest + ALL blobs survive (the swap never runs), so a
	// degenerate publish is a no-op, not a corpus wipe. Skips are logged (best-effort
	// like the read-side backstop) and return nil — the embed ship treats a skipped
	// publish as "nothing to reconcile this pass", self-healing on a later pass once
	// the engine is fully loaded.
	ok, reason, err := m.publishCoverageOK(ctx, liveSet)
	if err != nil {
		return nil, err
	}
	if !ok {
		slog.Warn("segmentdist: publish SKIPPED (degenerate/incomplete live set — manifest+blobs left intact)",
			"format", m.format, "live", len(manifestIDs), "reason", reason)
		return nil, nil
	}

	if _, err := m.source.PublishManifest(m.format, manifestDigests); err != nil {
		// A server-side completeness failure (the GCS agent 409'd because a
		// referenced blob is not yet present) is NOT a hard error: treat it as a
		// logged SKIP — the prior manifest stays intact and no bookkeeping reconcile
		// runs, matching the degenerate-publish skip semantics above. The unshipped
		// blob heals on a later pass once its PUT succeeds and it re-enters the
		// resident→published set.
		if incomplete, ok := errors.AsType[*manifestIncompleteError](err); ok {
			slog.Warn("segmentdist: publish SKIPPED (agent reported missing blob(s) — manifest+blobs left intact)",
				"format", m.format, "missing", incomplete.Missing)
			return nil, nil
		}
		return nil, err
	}

	// Reconcile bookkeeping: the ids in reconcileAgainst (the caller's ROLE set)
	// that dropped out of the published live set are now reaped server-side (their
	// refcount went to zero). The embed path passes locallyShipped (so a fresh
	// process never drops the prior corpus it merely re-imported — the
	// restart-tail guard); the rebuild path passes shippedIDs (the old corpus it
	// superseded). The dropped ids are removed from BOTH bookkeeping views to keep
	// them consistent.
	m.shipMu.Lock()
	var dropped []searchengine.SegmentID
	for id := range reconcileAgainst {
		if _, live := liveSet[id]; !live {
			dropped = append(dropped, id)
		}
	}
	for _, id := range dropped {
		delete(m.shippedIDs, id)
		delete(m.locallyShipped, id)
	}
	m.shipMu.Unlock()
	return dropped, nil
}

// publishCoverageOK is the publish-path safety gate. It returns
// (true, "") when liveSet is safe to publish as this writer's manifest, or
// (false, reason) when the publish must be SKIPPED to avoid wiping the corpus.
// The checks, in order:
//
//	(1) NON-EMPTY: an empty live set (∅ ⊆ anything is a vacuous subset) would pass
//	    the subset gate yet drive a full refcount-GC — the exact corpus wipe. An
//	    empty manifest is always rejected.
//	(2) COVERAGE-RATIO FLOOR: the resident doc count vs the server's shipped doc
//	    count for this format, via the SAME shippedDocCountForRatio +
//	    residentBackstopFloor/residentBackstopRatio policy the read-side
//	    recoverIfDegenerate uses (conservative-unknown on a pre-doc_count blob,
//	    sub-floor tiny-graph disarm). A resident set far below the shipped corpus is
//	    a degenerate/partial load — skip rather than reap the corpus it has not yet
//	    re-imported. A disarmed ratio (tiny graph / untrustworthy denominator) is
//	    treated as SAFE: a small graph legitimately publishes its whole tiny set.
//	(3) SUBSET-COMPLETENESS: the live set must be a subset of List(0) for this
//	    format (liveSetSubsetOfList0). A live set referencing ids the server lacks
//	    is an incomplete/suspect view — skip.
func (m *distManager[Q, S]) publishCoverageOK(
	ctx context.Context, liveSet map[searchengine.SegmentID]struct{},
) (bool, string, error) {
	if len(liveSet) == 0 {
		return false, "empty live set", nil
	}

	// Coverage-ratio floor — reuse the read-side backstop policy verbatim.
	resident := m.engine.ResidentDocCount()
	shipped, disarm, err := m.shippedDocCountForRatio(ctx)
	if err != nil {
		return false, "", err
	}
	// disarm == true means the denominator is untrustworthy (pre-doc_count blob) or
	// the corpus is below the floor (tiny graph): the ratio is not meaningful, so the
	// coverage check does not block — a tiny/legacy graph legitimately publishes its
	// whole set. A non-disarmed below-ratio resident set is the degenerate case.
	if !disarm && float64(resident) < residentBackstopRatio*float64(shipped) {
		return false, "resident doc count below coverage ratio of shipped corpus", nil
	}

	// Subset-completeness against List(0). SKIPPED when the source verifies
	// completeness server-side (the GCS agent HEAD-verifies + 409s on missing): there
	// List(0) IS the published manifest, so a resident set that legitimately includes
	// newly-shipped-but-not-yet-published blobs is NEVER a subset and would deadlock
	// the first/every add-publish. The agent's manifest/publish HEAD-verify (surfaced
	// as a manifestIncompleteError → logged skip in publishResident) is the
	// completeness authority on that path instead.
	if m.source.verifiesCompletenessServerSide() {
		return true, "", nil
	}
	subset, err := m.liveSetSubsetOfList0(ctx, liveSet)
	if err != nil {
		return false, "", err
	}
	if !subset {
		return false, "live set not a subset of List(0) — incomplete view", nil
	}
	return true, "", nil
}
