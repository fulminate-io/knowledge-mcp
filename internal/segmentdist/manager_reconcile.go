// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// ReconcileResidentDegenerate is the startup/periodic reconcile's detection probe:
// it reports whether a graph's LIVE in-memory engine is degenerate relative to the
// server's shipped corpus AFTER a cache-first load + a cheap server re-import — i.e.
// a graph either heal step would restore is NOT flagged, and a genuinely-collapsed
// graph (server holds the full corpus, the live searchable pool stays near-empty
// even after both) IS caught, leaving the expensive RebuildSegments path to the
// caller.
//
// It runs TWO complementary heal steps before the degeneracy read, because each
// covers a case the other misses:
//   - load() (cache-first): the L2-FIRST primary path imports the warm L2 resident
//     set SERVER-INDEPENDENTLY, so a warm-but-not-yet-loaded graph is made resident
//     even when the server is slow/down — never collapsed to empty on a List
//     timeout. But load() short-circuits on the l2Loaded once-guard, so it heals
//     NOTHING for a graph a prior Search/load already (partially) loaded.
//   - recoverIfDegenerate (server re-import): resets the load floor
//     (importedGen.Store(0)) and re-imports from the SERVER (loadFromServer),
//     bypassing the l2Loaded guard — the fix for a PARTIAL-L2 graph (resident below
//     floor while the server holds the full corpus) that load() can no longer
//     touch. It is a no-op (ZERO RPC) when load() already cleared the floor.
//
// A bare load() was insufficient (a partial-L2 graph stayed flagged degenerate
// forever); a bare recoverIfDegenerate was ALSO insufficient (it skips the L2-first
// warm-disk import, collapsing a warm-but-unloaded graph to empty on a List
// timeout). Both, in order, cover both cases.
//
// Flow:
//  1. load() cache-first (L2-first warm import; no-op if already loaded).
//  2. recoverIfDegenerate: no-op when resident >= floor; else single-flighted
//     importedGen.Store(0) + loadFromServer re-import of the server corpus.
//  3. read the live resident doc count; resident >= residentBackstopFloor → healthy
//     (a heal step restored coverage), return false.
//  4. below the floor: read the shipped denominator via the SHARED
//     shippedDocCountForRatio (same disarm rules as the read-side backstop — a
//     pre-doc_count blob or a sub-floor corpus disarms).
//  5. degenerate iff resident < residentBackstopRatio * shipped.
//
// ACCEPTED TRADE — TWO List(0)s on the below-floor heal path: recoverIfDegenerate
// runs its own shippedDocCountForRatio List(0) (manager_backstop.go) when below
// floor, and this method then re-reads the shipped denominator with a SECOND List(0)
// at step 4. This is deliberate, not a regression: a HEALTHY graph short-circuits in
// BOTH recoverIfDegenerate's floor gate AND step 3 here, paying ZERO List — the
// extra List is confined to the rare actually-degenerate case. ReconcileResidentDegenerate
// is OFF the bind path (fired only by the ~30s boot-delay one-shot and the periodic
// reconcile loop), so a second List on a cold heal never touches first-search
// readiness. Threading recoverIfDegenerate's already-computed shipped count back to
// the caller to save the second List was rejected: its early-exit paths never
// compute one, so plumbing a sentinel through that hot read-side net to shave one
// List off a non-bind-path heal is not worth the coupling.
//
// Best-effort: a load(), recoverIfDegenerate, or List error is returned to the
// caller, which logs and continues (never blocks boot).
func (m *Manager) ReconcileResidentDegenerate(
	ctx context.Context, gt kgtypes.GraphType, name string,
) (degenerate bool, err error) {
	dm := m.managerFor(gt, name)

	// Cache-first load FIRST: the L2-first primary path imports the warm L2 resident
	// set SERVER-INDEPENDENTLY (loadResidentFromL2), so a warm-but-not-yet-loaded
	// graph is made resident even when the server List times out — never collapsed
	// to empty. A graph whose lazy load would heal is thus not flagged.
	if err := dm.load(ctx); err != nil {
		return false, err
	}
	// THEN the cheap server re-import: heal a partial-L2 graph whose prior load
	// already latched l2Loaded (so the load() above short-circuits and re-imports
	// nothing) — recoverIfDegenerate resets the load floor and re-imports from the
	// server, bypassing the once-guard. It is a no-op (zero RPC) when the load above
	// already cleared the floor, so a warm/healthy graph pays nothing extra here.
	if err := dm.recoverIfDegenerate(ctx); err != nil {
		return false, err
	}

	resident := dm.engine.ResidentDocCount()
	if resident >= residentBackstopFloor {
		// Healthy after the re-import — the resident pool clears the floor.
		return false, nil
	}

	shipped, disarm, err := dm.shippedDocCountForRatio(ctx)
	if err != nil {
		return false, err
	}
	if disarm {
		// Pre-doc_count blob (unknown denominator) or a sub-floor corpus — never
		// flag, mirroring the read-side backstop's disarms so the reconcile never
		// storms a migrating fleet or churns a tiny graph.
		return false, nil
	}

	return float64(resident) < residentBackstopRatio*float64(shipped), nil
}
