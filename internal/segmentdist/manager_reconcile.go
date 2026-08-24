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
// It is the ANY-ARM wrapper over ReconcileResidentDegenerateByFormat
// (manager_reconcile_arms.go), which evaluates EVERY format arm of the graph
// INDEPENDENTLY, each against its OWN format's shipped denominator. This method ORs
// the per-arm verdicts: true iff ANY arm is degenerate. One format's read pool can
// collapse while the other is intact — they have separate engines, separate segment
// sources and separate L2 cache roots — so a single-arm probe would report a healthy
// graph while half its search surface was empty.
//
// Its (ctx, gt, name) (bool, error) signature is unchanged: every caller that wants
// the plain "does this graph need attention" answer keeps working untouched. A
// caller that needs to know WHICH arm collapsed calls the per-format probe directly.
//
// ERROR ISOLATION: an arm that could not be measured contributes Degenerate false,
// never an error, so a partially-failed probe degrades to "the arms I could read
// look healthy" rather than a spurious rebuild. An error surfaces here only when
// EVERY arm failed. Each arm's own error is preserved in its ArmVerdict.Err.
//
// EVICTION ISOLATION, and it is riding on the OR below rather than on a branch of
// its own — say so here so a future reader who inverts the OR sees what depends on
// it. An arm whose segment pool the residency budget unloaded reports
// {Evicted:true, Degenerate:false}, exactly like the unmeasurable arm above, so it
// contributes false and can never drive a rebuild through this wrapper. That is the
// correct disposition: an evicted pool re-materializes on its next consumer search,
// and rebuilding it from the server instead would undo the eviction at the highest
// possible cost. A caller that needs to DISTINGUISH evicted from healthy — rather
// than merely decline both — must read ArmVerdict.Evicted off the per-format probe;
// this wrapper deliberately collapses the two.
//
// PER-ARM FLOW (see armCoverageVerdict for the authoritative sequence): cache-first
// load, entry floor gate, one shipped-count read, cheap server re-import, then the
// verdict with the floor RE-APPLIED before the ratio. Each arm emits one slog.Debug
// carrying its format, so the whole reconcile decision stays inspectable in the
// daemon log without re-deriving it.
//
// TWO List(0)s ON THE BELOW-FLOOR PATH: NO LONGER ACCEPTED. This method previously
// documented the two-List cost as deliberate — recoverIfDegenerate ran its own
// shipped-count List and the verdict then re-read the denominator with a second one
// — on two premises: that the extra List was confined to the rare actually-degenerate
// case, and that threading the count back would push a sentinel through a hot
// read-side net. BOTH have expired:
//
//   - An arm whose shipped manifest is absent or below the floor stays below the
//     floor permanently, so its probe is not rare — it is the recurring per-tick
//     state for every graph that has never shipped that format.
//   - The read-side net is gone: recoverIfDegenerate has ONE production caller (this
//     probe's arm sequence) plus its restart-backstop tests; Search no longer calls
//     it.
//
// So the arm sequence reads the denominator ONCE and passes it into
// recoverIfDegenerateWithShipped. Reusing one snapshot for both the recovery
// decision and the verdict REMOVES a staleness window rather than adding one: the
// probe only reads the shipped corpus, and the second List it replaces could observe
// a different corpus than the recovery decision used.
//
// Best-effort: the returned error is logged by the caller, which continues (never
// blocks boot).
func (m *Manager) ReconcileResidentDegenerate(
	ctx context.Context, gt kgtypes.GraphType, name string,
) (degenerate bool, err error) {
	verdicts, err := m.ReconcileResidentDegenerateByFormat(ctx, gt, name)
	if err != nil {
		return false, err
	}
	for _, v := range verdicts {
		if v.Degenerate {
			return true, nil
		}
	}
	return false, nil
}
