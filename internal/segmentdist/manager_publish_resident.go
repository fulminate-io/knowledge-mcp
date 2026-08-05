// SPDX-License-Identifier: Apache-2.0

// manager_publish_resident.go — the REGISTRY-MODEL publish path: ship the new
// content-hash blobs, then publish this writer's RESIDENT live set as its manifest
// so the server refcount-GCs whatever dropped out of it. Relocated verbatim from
// manager_prune.go, which keeps the superseded diff-prune mechanism (ship /
// shipNew / reconcilePrune) that the in-package machinery tests still drive.
//
// The seam is the MODEL, not the line count: everything here decides what the LIVE
// SET IS, while what stays behind decides which stale ids to DELETE.

package segmentdist

import (
	"context"
	"errors"
	"log/slog"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// shipAndPublish is the embed/tail ship path's REGISTRY-MODEL replacement for the
// diff-prune reconcile: it ships the new-content-hash blobs (the same
// ship-new leg as ship()), then PUBLISHES this writer's current RESIDENT live set
// as its manifest so the server reference-count-GCs whatever dropped out of the
// live set. Unlike reconcilePrune — which deletes the diff of a caller-supplied
// `against` set — the published manifest is the AUTHORITATIVE live set, and the
// server reaps a blob only when NO writer's manifest references it (multi-writer
// safe by construction).
//
// THE MANIFEST IS THIS ENGINE'S RESIDENT SET, FULL STOP. It used to be a UNION with a
// sibling engine's digests, because the HNSW rebuild wrote a second engine while both
// keyed one "hnsw" manifest — so an embed publish that named only its own set reaped the
// blobs the rebuild was still responsible for. There is one engine per format now, so
// there is no sibling to reference and no union to get wrong.
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
//   - ROLE A (reset rebuild, against = m.shippedIDs): the rebuild's resident Export IS
//     the complete new corpus, so shippedIDs − liveSet is the old corpus it superseded
//     — returned so FinalizeRebuild reports it per format and feeds the HNSW half to
//     InvalidateLocal for local L2 eviction.
//
// RETURN: the ids that dropped out (per the role's set). The empty-set / coverage
// gate inside publishResident protects against a degenerate publish.
func (m *distManager[Q, S]) shipAndPublish(
	ctx context.Context, reconcileAgainst map[searchengine.SegmentID]struct{},
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

	// SHIPPED VERSUS SKIPPED-AS-PRESENT, and this line is the one an operator needed
	// and did not have. The ship diff SUPPRESSES every blob whose content hash the
	// server already holds, so a rebuild that emits 128 buckets can legitimately
	// upload 32 — and because only shipped blobs are written to the L2 cache, the
	// difference is precisely what leaves the local cache short of the published set.
	// Without both numbers on one line the upload count reads as the whole story: the
	// 78-second rebuild that truncated a served corpus to a quarter emitted no ship
	// line at all, and the only detector was a human doing arithmetic after a restart.
	slog.Info("segmentdist: ship diff resolved",
		"graph", m.target.GetGraph(), "name", m.target.GetName(), "repo", m.target.GetRepo(),
		"format", m.format, "resident", len(all), "shipped", len(diff),
		"skipped_as_present", len(all)-len(diff))

	// Ship-new FIRST so the consolidated blobs land before the publish reaps their
	// merged-away predecessors (never a server gap).
	if err := m.shipNew(ctx, diff, diffBlobs); err != nil {
		return nil, err
	}

	return m.publishResident(ctx, all, reconcileAgainst)
}

// publishResident publishes this writer's resident live set (the ids in
// `all` = m.engine.Export()) as this writer's manifest for (graphKey, writerID,
// format), then reconciles the local bookkeeping. It returns the ids in
// reconcileAgainst that dropped out of the published set (the caller's ROLE choice —
// locallyShipped for the embed path, shippedIDs for the reset rebuild). Separated from
// shipAndPublish so the merge/reclaim paths can re-publish without re-running the
// ship-new diff.
func (m *distManager[Q, S]) publishResident(
	ctx context.Context, all []searchengine.SegmentBlob,
	reconcileAgainst map[searchengine.SegmentID]struct{},
) ([]searchengine.SegmentID, error) {
	liveSet := make(map[searchengine.SegmentID]struct{}, len(all))
	// manifestDigests carries the per-digest doc_count to the wire (the GCS manifest
	// stores it as the coverage-read denominator; the RPC path drops it). manifestIDs
	// is the id-only view the coverage gate + dropped-reconcile below still use.
	manifestDigests := make([]segmentDigest, 0, len(all))
	manifestIDs := make([]searchengine.SegmentID, 0, len(all))
	for _, b := range all {
		if _, dup := liveSet[b.ID]; dup {
			continue
		}
		liveSet[b.ID] = struct{}{}
		manifestDigests = append(manifestDigests, segmentDigest{ID: b.ID, DocCount: b.DocCount})
		manifestIDs = append(manifestIDs, b.ID)
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
	ok, reason, err := m.publishCoverageOK(ctx, liveSet, m.engine.ResidentDocCount())
	if err != nil {
		// The ship already stamped shippedIDs, but the coverage-read List failed
		// before PublishManifest — set the retry bit so a later sub-threshold tick
		// re-attempts the publish (hasUnshippedExport is now false).
		m.setPublishPending()
		return nil, err
	}
	if !ok {
		// Coverage/subset gate skipped the publish (degenerate/incomplete live set).
		// The ship landed but no manifest was published — mark the coverage skip so the
		// publish is re-attempted once the live set heals. Unlike the transient causes
		// (List error / 409 / transport) which retry indefinitely, this cause cannot
		// self-clear by retrying (a re-attempt reads the SAME sub-ratio resident), so
		// markCoverageSkip BOUNDS the re-arm: after coverageSkipMaxStreak consecutive
		// skips at a non-rising resident it stops re-arming (terminal WARN) until the
		// resident actually rises — breaking the self-sustaining publish-retry read loop.
		m.markCoverageSkip()
		// The identity trio leads every skip WARN — a skip logged without the
		// manager's target cannot be attributed to a graph.
		slog.Warn("segmentdist: publish SKIPPED (degenerate/incomplete live set — manifest+blobs left intact)",
			"graph", m.target.GetGraph(), "name", m.target.GetName(), "repo", m.target.GetRepo(),
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
			// 409: the agent HEAD-verify reported a referenced blob genuinely absent
			// server-side. The ship stamped the ids but the manifest did not land —
			// markIncompletePublish UN-STAMPS the missing ids so the next ship diff
			// re-uploads them (without this the diff skips them forever and the 409
			// wedges permanently), arms the retry bit, and escalates to a loud WARN if
			// the re-upload is not converging.
			m.markIncompletePublish(incomplete.Missing)
			return nil, nil
		}
		// Transport error on PublishManifest — the ship landed but the publish did
		// not; set the retry bit so a later tick re-attempts it.
		m.setPublishPending()
		return nil, err
	}

	// THE SWAP LANDED — say so, with its cardinality. A publish is skipped with a NIL
	// ERROR on both the coverage gate and the agent 409, and each of those paths logs
	// a WARN; before this line the SUCCESS path logged nothing, so an operator reading
	// the daemon log could not distinguish "published 128" from "published" at all,
	// let alone from a skip. The manifest count is the number a truncation claim is
	// ultimately argued against.
	slog.Info("segmentdist: manifest swap COMPLETED",
		"graph", m.target.GetGraph(), "name", m.target.GetName(), "repo", m.target.GetRepo(),
		"format", m.format, "published", len(manifestIDs), "resident", len(all))

	// The manifest just swapped — it IS the new shipped denominator.
	m.invalidateCoverageMemo()

	// Reconcile bookkeeping: the ids in reconcileAgainst (the caller's ROLE set)
	// that dropped out of the published live set are now reaped server-side (their
	// refcount went to zero). The embed path passes locallyShipped (so a fresh
	// process never drops the prior corpus it merely re-imported — the
	// restart-tail guard); the rebuild path passes shippedIDs (the old corpus it
	// superseded). The dropped ids are removed from BOTH bookkeeping views to keep
	// them consistent.
	m.shipMu.Lock()
	// PublishManifest succeeded — clear the retry bit under the reconcile lock we
	// already hold (no new acquisition), and reset the coverage-skip bound so a future
	// degenerate publish re-arms fresh (streak from zero, lastSkipResident zeroed).
	m.publishPending = false
	m.coverageSkipStreak = 0
	m.lastSkipResident = 0
	// The swap landed, so any prior agent-409 incomplete streak converged — reset it
	// so a future 409 re-arms fresh and escalates only on a NEW persistent episode.
	m.incompletePublishStreak = 0
	// The swap LANDED. This is the only site that increments, so a caller comparing
	// the counter across a call learns whether a manifest swap actually happened —
	// which the nil error it also gets on every skip cannot tell it.
	m.completedSwaps++
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

	// THE SINGLE WRITER of the manifest fingerprint. This is the one place a swap
	// completes, so it is the one place that can honestly say "the published set for
	// this (graph, format) is now exactly these ids". The off-hot-path completeness
	// reconcile compares len(cache.Keys()) against this record to decide whether to
	// pay a source read at all — and it is the L2 cache, NOT this manifest, that
	// routinely ends up short: the ship diff above skips every content-hash-unchanged
	// blob, so those never reach cache.Put even though they are published here.
	//
	// Fired OUTSIDE shipMu (a file write must not extend the ship lock) and
	// best-effort: a failed record leaves the detector blind for this graph until the
	// next publish, which is strictly better than failing a landed publish over it.
	if m.onManifestPublished != nil {
		m.onManifestPublished(manifestIDs)
	}
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
//	    count for this format (read through the publish-path memo,
//	    shippedDocCountForRatioCached), via the SAME shippedDocCountForRatio +
//	    residentBackstopFloor/residentBackstopRatio policy the read-side
//	    recoverIfDegenerate uses (conservative-unknown on a pre-doc_count blob,
//	    sub-floor tiny-graph disarm). A resident set far below the shipped corpus is
//	    a degenerate/partial load — skip rather than reap the corpus it has not yet
//	    re-imported. A disarmed ratio (tiny graph / untrustworthy denominator) is
//	    treated as SAFE: a small graph legitimately publishes its whole tiny set.
//	(3) SUBSET-COMPLETENESS: the live set must be a subset of List(0) for this
//	    format (liveSetSubsetOfList0). A live set referencing ids the server lacks
//	    is an incomplete/suspect view — skip.
//
// prospectiveLayerOK evaluates the SAME degeneracy policy against a layer that has
// been BUILT but is not yet resident: the built ids are the live set and their summed
// DocCount is the resident count. It returns publishCoverageOK's own (ok, reason, err)
// triple unchanged.
//
// WHY THE GATE MOVES EARLIER FOR A BUILD-ASIDE SWAP. At publish time the gate is a
// manifest guard, and that is sufficient while a degenerate rebuild lands in a second
// engine — the serving engine keeps the good corpus and a refused publish leaves both
// the prior manifest and every blob intact. Once a rebuild REPLACES the serving set in
// place, the swap is the destructive act and publish time is too late: reads would be
// served from the degenerate layer until a restart reloaded it, so the manifest would
// be protected and the corpus would not. Calling the gate here refuses the layer
// BEFORE it can serve anything.
//
// A REFUSAL LEAVES THE RESIDENT SET UNTOUCHED, but it is NOT a costless no-op for the
// caller: by the time this is consulted the built blobs have been shipped, and
// unwinding that ship is the caller's obligation (it owns the ship, so it owns the
// unwind). This function's contract stops at the verdict.
func (m *distManager[Q, S]) prospectiveLayerOK(
	ctx context.Context, built []searchengine.SegmentBlob,
) (bool, string, error) {
	liveSet := make(map[searchengine.SegmentID]struct{}, len(built))
	resident := 0
	for _, b := range built {
		if _, dup := liveSet[b.ID]; dup {
			continue
		}
		liveSet[b.ID] = struct{}{}
		resident += b.DocCount
	}
	return m.publishCoverageOK(ctx, liveSet, resident)
}

// THE RESIDENT COUNT IS THE CALLER'S TO SUPPLY, and that parameter is what lets one
// policy serve two moments. Read inline off the engine, this gate could only ever
// judge a layer that is ALREADY resident — which is safe while a degenerate rebuild
// lands in a second engine and the serving engine keeps the good corpus (the skip
// path's own reasoning above). It is not safe once a rebuild replaces the serving set
// in place: by publish time the swap has happened and reads are already being served
// from the degenerate layer. Taking the count as a parameter lets the SAME policy be
// evaluated against a PROSPECTIVE layer before it becomes resident.
//
// Every publish-path caller passes m.engine.ResidentDocCount() and is unchanged in
// behavior; the swap-path caller passes the built layer's summed doc count. There is
// deliberately ONE policy object rather than a swap-time copy — the publish gate and
// the read-side backstop are already documented as sharing one expression, and two
// copies of a degeneracy policy drift.
func (m *distManager[Q, S]) publishCoverageOK(
	ctx context.Context, liveSet map[searchengine.SegmentID]struct{}, resident int,
) (bool, string, error) {
	if len(liveSet) == 0 {
		return false, "empty live set", nil
	}

	// Coverage-ratio floor — the read-side backstop policy verbatim, read through a
	// short-TTL memo: a cached denominator may only CONFIRM a skip (see
	// shippedDocCountForRatioCached for why a memo-derived pass is re-derived first).
	shipped, disarm, cached, err := m.shippedDocCountForRatioCached(ctx)
	if err != nil {
		return false, "", err
	}
	// disarm == true means the denominator is untrustworthy (pre-doc_count blob) or
	// the corpus is below the floor (tiny graph): the ratio is not meaningful, so the
	// coverage check does not block — a tiny/legacy graph legitimately publishes its
	// whole set. A non-disarmed below-ratio resident set is the degenerate case.
	if belowCoverageRatio(resident, shipped, disarm) {
		return false, "resident doc count below coverage ratio of shipped corpus", nil
	}
	if cached {
		m.invalidateCoverageMemo()
		shipped, disarm, _, err = m.shippedDocCountForRatioCached(ctx)
		if err != nil {
			return false, "", err
		}
		if belowCoverageRatio(resident, shipped, disarm) {
			return false, "resident doc count below coverage ratio of shipped corpus", nil
		}
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
