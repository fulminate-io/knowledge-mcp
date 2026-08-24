// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"log/slog"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/bm25"
)

// manifestCompletenessReadDeadline bounds the ONE source read this pass can make.
// The pass runs off the hot path, so the deadline is not about latency budget — it
// is about never letting a slow server convert a reconcile tick into a stall. On
// expiry the arm serves whatever L2 gave it, emits the WARN, and leaves the
// shortfall to the next tick.
const manifestCompletenessReadDeadline = 2 * time.Second

// completenessArm is the non-generic seam the per-format completeness pass
// iterates, for the same reason coverageArm exists: distManager is generic over
// [Q, S] and the two live instantiations carry different type arguments, so only an
// interface mentioning neither can hold both.
type completenessArm interface {
	load(context.Context) error
	// loadIfResident is load's DECLINING twin — see coverageArm
	// (manager_reconcile_arms.go). load stays declared beside it so a signature
	// drift on either is still a build failure at the assertions below.
	loadIfResident(context.Context) (bool, error)
	armFormat() string
	armL2Authoritative() bool
	armCachedIDs() []searchengine.SegmentID
	armResidentIDs() []searchengine.SegmentID
	armManifestIDs(context.Context) ([]searchengine.SegmentID, error)
	armImport(context.Context, []searchengine.SegmentID) error
}

// Compile-time satisfaction for BOTH live instantiations, so a signature drift is
// a build failure here rather than a silently dropped arm.
var (
	_ completenessArm = (*distManager[[]byte, struct{}])(nil)
	_ completenessArm = (*distManager[bm25.Query, *bm25.CorpusStats])(nil)
)

// armL2Authoritative exposes the OSS-local flag through the seam.
func (m *distManager[Q, S]) armL2Authoritative() bool { return m.l2Authoritative }

// armCachedIDs lists the L2-resident ids. Index read only — no disk re-read, no
// network — which is what makes the gate below affordable every tick.
func (m *distManager[Q, S]) armCachedIDs() []searchengine.SegmentID { return m.cache.Keys() }

// armResidentIDs lists the ids currently imported into the searchable engine.
func (m *distManager[Q, S]) armResidentIDs() []searchengine.SegmentID {
	return m.engine.ResidentSegmentIDs()
}

// armManifestIDs reads the published manifest for this engine's format. On the
// cloud path source.List(0) IS the manifest read (the GCS source Lists through the
// agent's manifest), so this introduces NO new wire surface — it is the same call
// ensureShippedSeeded already makes.
func (m *distManager[Q, S]) armManifestIDs(ctx context.Context) ([]searchengine.SegmentID, error) {
	metas, err := m.source.List(ctx, 0)
	if err != nil {
		return nil, err
	}
	ids := make([]searchengine.SegmentID, 0, len(metas))
	for _, meta := range metas {
		if !m.keepFormat(meta.Format) {
			continue
		}
		ids = append(ids, meta.ID)
	}
	return ids, nil
}

// armImport materializes the named ids into the engine through the EXISTING
// reload path — L2 hit where possible, sub-batched + byte-ceiling-halved Fetch for
// the misses (fetchMisses, which carries the OOM guard), then Import. Nothing new
// is built for the fetch; this is the named reuse target.
//
// tolerateMisses is true: an id the server cannot serve must not abort the repair
// of the ids it can. The shortfall that remains is re-detected next tick.
func (m *distManager[Q, S]) armImport(ctx context.Context, ids []searchengine.SegmentID) error {
	return m.reload(ctx, ids, true)
}

// ReconcileManifestCompleteness converges each of a graph's format arms toward its
// PUBLISHED manifest: L2 is a warm start, the manifest is the source of truth.
//
// THE DEFECT IT CLOSES. shipNew caches only blobs in the ship DIFF, so after a
// rebuild the L2 cache holds the buckets that rebuild ADDED — never the live set —
// while publishResident publishes the complete resident Export. load() is L2-FIRST
// and accepts ANY non-empty cache as final (loadResidentFromL2 → l2Loaded), with no
// comparison against the manifest. Quarantine the cache, rebuild, restart, and the
// engine pins itself to the added subset while the server holds everything. It was
// reproduced at 24.7% resident against a 100.0% cold-cache control on identical
// server state.
//
// IT DOES NOT RUN AT LOAD, and that is an architecture decision rather than a
// placement preference. load() is on the first-search critical path
// (manager_search.go:65/:68/:138) and a server read was DELIBERATELY removed from it
// (:59-64); the L2-primary ruling puts healing OFF the hot path. This method is
// called from the boot-delay one-shot and the periodic reconcile only.
//
// THE DIRECTION IS ONE-DIRECTIONAL — FETCH MISSING, NEVER REMOVE. The property is
// COMPLETENESS, not equality. A resident set LARGER than the manifest is correct:
// the un-reclaimed merge window is a live producer of legitimate supersets
// (manager_load.go:232-240), so trimming toward the manifest would delete valid
// segments.
//
// THE READ IS GATED ON A CHEAP LOCAL SIGNAL. The persisted per-format fingerprint
// (manifest_state.go) is compared against len(cache.Keys()) BEFORE any read, and
// source.List(0) is paid ONLY inside the mismatch branch. A healthy arm therefore
// keeps its ZERO-RPC property, the periodic loop gains no recurring network cost,
// and the no-read-above-the-floor invariant armCoverageVerdict maintains is
// untouched because the comparison is two local numbers.
//
// THE OSS l2AUTHORITATIVE ARM IS SKIPPED ENTIRELY. There is no cloud registry to
// converge toward; its source's Fetch is L2-only, so a manifest comparison there
// would compare the cache against itself.
//
// Best-effort per arm: a failure is logged and the next arm still runs. The
// returned error is the first arm error, for a caller that wants to log it; the
// caller never blocks on it.
func (m *Manager) ReconcileManifestCompleteness(
	ctx context.Context, gt kgtypes.GraphType, name string,
) error {
	arms := []completenessArm{m.managerFor(gt, name), m.bm25ManagerFor(gt, name)}
	var firstErr error
	for _, arm := range arms {
		if err := m.convergeArmToManifest(ctx, gt, name, arm); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// convergeArmToManifest runs the gate-then-converge sequence for ONE format arm.
func (m *Manager) convergeArmToManifest(
	ctx context.Context, gt kgtypes.GraphType, name string, arm completenessArm,
) error {
	format := arm.armFormat()
	if arm.armL2Authoritative() {
		return nil // OSS: no registry to converge toward.
	}

	// Warm the engine from L2 first (idempotent; zero network on the L2-first
	// primary path) so the resident set below reflects what the cache already holds
	// rather than an empty engine that would make every manifest id look missing.
	//
	// AN EVICTED POOL IS DECLINED HERE, before the local-signal gate, AND SKIPPING
	// COSTS NO CONVERGENCE. The gate compares cache.Keys() against the persisted
	// fingerprint — both L2-DISK facts, which eviction does not touch — so the
	// shortfall this pass repairs is a property of the disk cache, unchanged by the
	// pool leaving RAM and re-detected identically on the first tick after the pool
	// is next materialized.
	skipped, err := arm.loadIfResident(ctx)
	if err != nil {
		slog.Warn("segmentdist: completeness reconcile could not load the arm (skipping this tick)",
			"graph_type", gt, "name", name, "format", format, "err", err)
		return err
	}
	if skipped {
		return nil
	}

	// ---- THE LOCAL-SIGNAL GATE. Two local reads; no network above this line. ----
	cached := arm.armCachedIDs()
	fp, ok := m.loadManifestFingerprint(gt, name, format)
	if !ok {
		// No fingerprint yet (never published on this cache, or the record was wiped
		// with the cache). Nothing to compare against — the next completed publish
		// writes one. Doing nothing is correct; guessing is not.
		return nil
	}
	if !manifestShortfallSuspected(cached, fp) {
		return nil // healthy arm — zero RPC, which is the whole point of the gate.
	}

	// ---- INSIDE THE MISMATCH BRANCH: the only place this pass reads. ----
	readCtx, cancel := context.WithTimeout(ctx, manifestCompletenessReadDeadline)
	manifest, err := arm.armManifestIDs(readCtx)
	cancel()
	if err != nil {
		// DEGRADE, NEVER FAIL CLOSED. A failing or slow source leaves L2 serving
		// whatever it has. M is the PERSISTED count precisely because the read that
		// would have supplied the live one is what just failed — without the record
		// this line could not name a denominator at all.
		slog.Warn("segmentdist: manifest completeness read FAILED — serving the L2 set as-is, shortfall unrepaired this tick",
			"graph_type", gt, "name", name, "format", format,
			"resident", len(arm.armResidentIDs()), "cached", len(cached),
			"manifest_expected", fp.Count, "deadline", manifestCompletenessReadDeadline, "err", err)
		return err
	}

	missing := idsMissingFrom(manifest, arm.armResidentIDs())
	if len(missing) == 0 {
		// THE STALE-COUNT EDGE, AND IT SELF-HEALS WITH NO MACHINERY. A fingerprint
		// recorded before a legitimate prune describes a manifest larger than the one
		// that exists now, so the gate fires, this read runs ONCE, finds the resident
		// set already covers the live manifest, and rewrites the record. One wasted
		// List, then correct forever. Nothing tracks or expires the record beyond this.
		if serr := m.saveManifestFingerprint(gt, name, format, fingerprintOf(manifest)); serr != nil {
			slog.Warn("segmentdist: could not refresh the manifest fingerprint after a clean completeness read",
				"graph_type", gt, "name", name, "format", format, "err", serr)
		}
		return nil
	}

	return m.repairArmShortfall(ctx, gt, name, arm, manifest, missing, len(cached), fp.Count)
}

// repairArmShortfall announces a genuine shortfall, fetches the missing entries,
// and refreshes the fingerprint only if the repair actually landed. Split out of
// convergeArmToManifest so the GATE (which must be readable as "two local numbers,
// then maybe one read") is not buried under the repair it guards.
func (m *Manager) repairArmShortfall(
	ctx context.Context, gt kgtypes.GraphType, name string, arm completenessArm,
	manifest, missing []searchengine.SegmentID, cachedCount, fingerprintCount int,
) error {
	format := arm.armFormat()

	// A GENUINE SHORTFALL. This WARN is the ONLY detector on this failure mode —
	// the read-side coverage ratio short-circuits above the floor and never sees it
	// — so it carries both numbers and it RE-EMITS every tick until the shortfall is
	// gone, rather than announcing itself once and going quiet.
	slog.Warn("segmentdist: L2 cache is SHORT of the published manifest — fetching the missing segments",
		"graph_type", gt, "name", name, "format", format,
		"resident", len(arm.armResidentIDs()), "manifest", len(manifest), "missing", len(missing),
		"cached", cachedCount, "fingerprint_count", fingerprintCount)

	if err := arm.armImport(ctx, missing); err != nil {
		slog.Warn("segmentdist: manifest completeness fetch FAILED — serving the L2 set as-is, shortfall unrepaired this tick",
			"graph_type", gt, "name", name, "format", format,
			"missing", len(missing), "manifest", len(manifest), "err", err)
		return err
	}

	// Re-read the resident set: only a repair that actually landed may retire the
	// WARN, and only then is the fingerprint refreshed. An incomplete repair leaves
	// the record alone so the next tick re-detects and re-emits.
	stillMissing := idsMissingFrom(manifest, arm.armResidentIDs())
	if len(stillMissing) > 0 {
		slog.Warn("segmentdist: manifest completeness fetch landed only PART of the shortfall",
			"graph_type", gt, "name", name, "format", format,
			"still_missing", len(stillMissing), "manifest", len(manifest))
		return nil
	}

	slog.Info("segmentdist: L2 cache converged to the published manifest",
		"graph_type", gt, "name", name, "format", format,
		"resident", len(arm.armResidentIDs()), "manifest", len(manifest), "fetched", len(missing))
	if serr := m.saveManifestFingerprint(gt, name, format, fingerprintOf(manifest)); serr != nil {
		slog.Warn("segmentdist: could not refresh the manifest fingerprint after convergence",
			"graph_type", gt, "name", name, "format", format, "err", serr)
	}
	return nil
}

// idsMissingFrom returns the ids in want that are absent from have. The direction
// is the whole point and it is ONE-WAY: this asks what the resident set is missing,
// never what it holds in excess. A resident set larger than the manifest is correct
// (the un-reclaimed merge window), so nothing here may be read as a removal list.
func idsMissingFrom(want, have []searchengine.SegmentID) []searchengine.SegmentID {
	present := make(map[searchengine.SegmentID]struct{}, len(have))
	for _, id := range have {
		present[id] = struct{}{}
	}
	var missing []searchengine.SegmentID
	for _, id := range want {
		if _, ok := present[id]; !ok {
			missing = append(missing, id)
		}
	}
	return missing
}

// manifestShortfallSuspected is the cheap local gate: does the L2 cache look SHORT
// of the manifest the last completed publish recorded?
//
// The two arms are the two fields of the fingerprint, and the asymmetry is the
// ruled mismatch semantics:
//
//   - FEWER cached ids than the manifest recorded → a shortfall. This is the
//     incident's own shape (32 cached against 128 published) and it trips on the
//     FIRST tick.
//   - As many as recorded but a DIFFERENT set → also a shortfall. Equal counts with
//     unequal membership means some manifest members are absent and an equal number
//     of un-reclaimed orphans are present, which a count compare reads as healthy.
//   - MORE cached ids than recorded → the documented superset (an un-reclaimed merge
//     window). NO ACTION: the hash necessarily differs there too, so testing it
//     would fire the repair path on a correct cache every tick and destroy the
//     zero-RPC property. The count is checked FIRST for exactly that reason.
func manifestShortfallSuspected(cached []searchengine.SegmentID, fp manifestFingerprint) bool {
	if len(cached) < fp.Count {
		return true
	}
	if len(cached) == fp.Count {
		return fingerprintOf(cached).Hash != fp.Hash
	}
	return false
}
