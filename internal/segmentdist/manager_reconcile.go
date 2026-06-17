// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// ReconcileResidentDegenerate is the startup/periodic reconcile's detection probe:
// it reports whether a graph's LIVE in-memory engine is degenerate relative to the
// server's shipped corpus AFTER a cache-first load — i.e. a graph that lazy-load
// would heal is NOT flagged, and a genuinely-collapsed graph (server holds the full
// corpus, the live searchable pool stays near-empty even after loading) IS caught.
//
// It is recoverIfDegenerate's DETECTION half WITHOUT the self-loading action tail:
// recoverIfDegenerate resets the load floor and re-imports inline (the read-edge
// best-effort net), but the reconcile caller heals via the PROVEN RebuildSegments
// path instead of a bare load, so this method only DETECTS and reports — the heal
// decision and action belong to the caller.
//
// Flow (mirroring recoverIfDegenerate's AFTER-load ordering, which is why
// segmentPoolDegenerate — a server-vs-server compare with no load — is the wrong
// detector here):
//  1. load() cache-first so a graph whose first load would import the corpus is
//     made resident BEFORE the resident count is read (no false-positive on a cold
//     graph that simply had not loaded yet).
//  2. read the live resident doc count; resident >= residentBackstopFloor → healthy,
//     return false with zero further RPC (the healthy fast path pays one load + one
//     atomic count).
//  3. below the floor: read the shipped denominator via the SHARED
//     shippedDocCountForRatio (same disarm rules as the read-side backstop — a
//     pre-doc_count blob or a sub-floor corpus disarms).
//  4. degenerate iff resident < residentBackstopRatio * shipped.
//
// Best-effort: a load() or List error is returned to the caller, which logs and
// continues (never blocks boot). The probe never mutates ship/load state beyond the
// cache-first load() (it does NOT reset importedGen — that is recoverIfDegenerate's
// inline-heal mechanic, not the detector's job).
func (m *Manager) ReconcileResidentDegenerate(
	ctx context.Context, gt kgtypes.GraphType, name string,
) (degenerate bool, err error) {
	dm := m.managerFor(gt, name)

	// Cache-first load FIRST: a graph whose lazy load would heal must not be flagged.
	if err := dm.load(ctx); err != nil {
		return false, err
	}

	resident := dm.engine.ResidentDocCount()
	if resident >= residentBackstopFloor {
		// Healthy after load — the resident pool clears the floor. Zero further RPC.
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
