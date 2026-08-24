// SPDX-License-Identifier: Apache-2.0

// manager_coverage_probe.go — the Manager's READ-ONLY coverage surface: the
// standalone per-graph probes the status column, the heal decision and the
// degeneracy backstop each read a number from. Relocated verbatim from
// manager_owner.go.
//
// They are grouped because they answer ONE question from different sides — how
// much of this graph is actually covered — and because each is the STANDALONE
// wrapper kept for a caller that probes one graph in isolation; the shared-snapshot
// heal path uses the FromSnapshot forms in manager_snapshot.go instead. Every one
// of them is source-aware (OSS L2-resident vs cloud manifest), which is the detail
// a caller reading only the name will otherwise get wrong.

package segmentdist

import (
	"context"
	"fmt"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/hnsw"
)

// HasShippedSegments is the CHEAP zero-shipped-segments presence probe the
// auto-heal arm uses: ONE ShippedManifestSnapshot read for the graph, returning true
// when it holds at least one segment meta. The snapshot is login-gated (cloud →
// the GCS agent manifest/read; OSS not-logged-in → the L2-local source's set), so
// this probe follows whichever source the graph runs on. It does NOT Fetch any blob
// and does NOT touch the per-graph engines/maps, so it is safe to call on the embed
// drain edge without disturbing resident state — strictly the presence list.
//
// Standalone wrapper for callers that probe presence ALONE (no co-located doc-count
// probe to share a snapshot with). The shared-snapshot heal path uses
// ShippedManifestSnapshot + HasShippedFromSnapshot to collapse its reads. It probes
// the HNSW format; taking no format of its own is deliberate, so this wrapper's
// callers are unaffected by the snapshot API being format-parameterized.
func (m *Manager) HasShippedSegments(ctx context.Context, gt kgtypes.GraphType, name string) (bool, error) {
	snapshot, err := m.ShippedManifestSnapshot(ctx, gt, name, hnsw.New().Name())
	if err != nil {
		return false, err
	}
	return m.HasShippedFromSnapshot(snapshot), nil
}

// ShippedSegmentDocCount is the coverage-ratio probe's data source: it reports the
// "segment-covered docs" count for the graph's HNSW coverage. It returns:
//
//   - covered: the segment-covered HNSW doc count. On the cloud path it is the
//     summed HNSW meta.DocCount from the GCS manifest snapshot; on the OSS path it is
//     the L2 resident HNSW doc count. ONLY the HNSW dimension is counted: BM25 metas
//     index the SAME nodes, so counting both would double-count; HNSW is the
//     per-node vector coverage that mirrors the graph's binary_vector_count
//     denominator the coverage ratio compares against.
//   - anyUnknown: true when ANY summed HNSW meta has DocCount==0 (cloud path only).
//     A zero doc count means that segment predates the doc_count wire plumbing (an
//     old blob written before the field existed), so its real coverage is UNKNOWN.
//     The coverage probe treats anyUnknown as the conservative-unknown signal and
//     DISARMS the ratio trigger (falling back to the zero-only heal) — without this
//     guard a fleet mid-migration, whose every shipped meta still reports
//     doc_count=0, would read covered=0 on every graph and trigger a fleet-wide
//     rebuild storm. The OSS/L2 path never returns anyUnknown (the resident count is
//     always a real, known denominator).
//
// It is SOURCE-AWARE, mirroring the heal path's healNeedsRebuildLocal split:
//
//   - OSS / L2-authoritative (not logged in): there is no server/GCS manifest, and
//     the local source's List stamps DocCount=0 (so the snapshot would report
//     covered=0/anyUnknown=true and wrongly disarm). Instead the covered count is
//     the L2 RESIDENT HNSW doc count (LoadResidentDocCount) — the same L2 numerator
//     the OSS heal decision uses. anyUnknown is false: the resident count is a real,
//     known denominator, never the pre-doc_count sentinel.
//   - CLOUD (logged-in): the GCS manifest carries real per-digest doc_counts, so the
//     covered count is summed from the ShippedManifestSnapshot (the prior behavior).
//
// The OSS branch loads the read engine (idempotent, L2-only); the cloud branch does
// NOT touch the per-graph engines/maps (one manifest read, no blob fetch).
//
// Standalone wrapper preserved for the external coverage seam
// (tools.SegmentCoverageReader → manage(status)), which probes ONE graph's doc count
// in isolation. The shared-snapshot heal path uses ShippedDocCountFromSnapshot.
func (m *Manager) ShippedSegmentDocCount(
	ctx context.Context, gt kgtypes.GraphType, name string,
) (covered int, anyUnknown bool, err error) {
	if m.IsL2Authoritative(gt, name) {
		// OSS path: the L2 resident HNSW doc count is the covered denominator (the
		// local source's List stamps DocCount=0, so the manifest snapshot cannot
		// supply it). Known count → anyUnknown is always false.
		resident, err := m.LoadResidentDocCount(ctx, gt, name)
		if err != nil {
			return 0, false, err
		}
		return resident, false, nil
	}
	hnswFormat := hnsw.New().Name()
	snapshot, err := m.ShippedManifestSnapshot(ctx, gt, name, hnswFormat)
	if err != nil {
		return 0, false, err
	}
	covered, anyUnknown = m.ShippedDocCountFromSnapshot(snapshot, hnswFormat)
	return covered, anyUnknown, nil
}

// ResidentDocCount returns the LIVE in-memory HNSW engine resident doc count for
// one graph: the summed sealed-segment DocCount currently imported into the
// searchable set. It is the read-side coverage operand the degeneracy probe
// compares against the server's shipped doc count (the SAME operand
// recoverIfDegenerate uses internally) — distinct from ShippedSegmentDocCount,
// which reads the SERVER's shipped count. A graph that has never been searched or
// loaded returns 0 (the lazily-constructed engine's set is empty). It is a single
// atomic snapshot (SegmentedIndex.ResidentDocCount) with no RPC and no load — the
// caller decides whether to load() first (the reconcile probe does; the status
// column reads raw current resident).
func (m *Manager) ResidentDocCount(gt kgtypes.GraphType, name string) int {
	return m.managerFor(gt, name).engine.ResidentDocCount()
}

// IsL2Authoritative reports whether (gt, name) runs on the OSS-local L2-only source
// (the not-logged-in path) — reading the HNSW embed manager's l2Authoritative flag.
// The flag is uniform per graph across formats (both formats derive it from the same
// caller gate), so the HNSW manager's value is representative. The bootstrap heal
// path calls this to route the OSS degeneracy collapse: an L2-authoritative graph
// heals from resident-vs-embedded locally, with NO server presence probe.
func (m *Manager) IsL2Authoritative(gt kgtypes.GraphType, name string) bool {
	return m.managerFor(gt, name).l2Authoritative
}

// CoverageSuppressedSince reports when this graph's publish coverage gate became
// unsatisfiable — the EARLIEST non-zero suppression stamp across the graph's engines
// — and 0 when no engine is suppressed. A caller pairs it with the bootstrap heal
// breaker's LatchedSince to answer "since when has this graph been unable to heal",
// which is the two-owner span the manage(status) stuck band renders.
//
// HOW MANY ENGINES, read off the struct rather than off prose: the Manager holds
// exactly two per-graph distManager maps — managers (HNSW) and bm25Managers (BM25),
// manager_owner.go — each keyed by the bare graphKey with no engine discriminator, so
// a graph has two engines that can reach the publish coverage gate and each keeps its
// OWN skip streak. The EARLIEST is the honest answer: the graph has been unable to
// publish complete coverage since the first of its engines gave up.
//
// It reads the maps DIRECTLY rather than through managerFor/bm25ManagerFor, which
// lazily CONSTRUCT an engine (cache directory, segment source, possibly a transport
// build). Its caller asks about every graph in the account, so constructing on a read
// would build engines for graphs this client does not maintain. A graph with no
// constructed engine has never published and so has never been suppressed: 0.
//
// AND IT DOES NOT WAIT ON THE CONSTRUCTION GATE, which is a deliberate exemption
// rather than a missed site. It calls only coverageSuppressedSince(), a plain
// accessor — it never calls load(), so it cannot latch anything on a half-seeded
// engine. Its answer is identical either way: an engine still being seeded has
// never published, so it has never been suppressed, which is the same 0 this doc
// already documents for a graph with no engine at all. Waiting would put an
// account-wide read behind some other graph's corpus copy, which is precisely the
// coupling the off-the-lock seed placement exists to avoid.
func (m *Manager) CoverageSuppressedSince(gt kgtypes.GraphType, name string) int64 {
	k := graphKey{graphType: gt, graphName: name}
	m.mu.Lock()
	hnswGate, hasHNSW := m.managers[k]
	bm25Gate, hasBM25 := m.bm25Managers[k]
	m.mu.Unlock()

	earliest := int64(0)
	consider := func(at int64) {
		if at != 0 && (earliest == 0 || at < earliest) {
			earliest = at
		}
	}
	if hasHNSW {
		consider(hnswGate.dm.coverageSuppressedSince())
	}
	if hasBM25 {
		consider(bm25Gate.dm.coverageSuppressedSince())
	}
	return earliest
}

// LoadResidentDocCount loads the graph's HNSW engine (idempotent; L2-only on the OSS
// path) and returns the resident HNSW doc count — the L2 resident numerator the OSS
// degeneracy collapse compares against the embedded-node denominator. It is the
// load-first variant of ResidentDocCount (the raw accessor does NOT load), needed
// because the heal path must import the warm L2 set before reading the count.
//
// IT TAKES THE RESIDENCY READ LOCK ACROSS load()+READ, the same three lines
// VectorByID uses (manager_search.go), and for the same reason: the load and the
// count are SEPARATE STATEMENTS, so a concurrent evictResident landing between them
// empties the pool the load just materialized and the count reads ZERO. That zero is
// not a harmless one-off here — it is the numerator of the degeneracy collapse, so a
// read that lands in the window reports a graph with a full L2 pool as having no
// resident corpus at all.
func (m *Manager) LoadResidentDocCount(ctx context.Context, gt kgtypes.GraphType, name string) (int, error) {
	dm := m.managerFor(gt, name)
	dm.residencyMu.RLock()
	defer dm.residencyMu.RUnlock()
	if err := dm.load(ctx); err != nil {
		return 0, err
	}
	return dm.engine.ResidentDocCount(), nil
}

// LiveResidentDocCount returns the DISTINCT LIVE-SEARCHABLE HNSW doc count for one
// graph — how many documents a search could actually return, counted once each.
//
// It is the REPORTER half of the pair, mirroring ResidentDocCount above: no load
// and no RPC, so a caller on a latency-sensitive assembly path keeps its local-read
// contract. A graph whose engine has not loaded yet legitimately reads 0.
//
// It differs from ResidentDocCount in BOTH directions and that is the point:
// ResidentDocCount sums per-segment counts, so it over-reports an id resident in
// two segments, and it counts deleted-but-unpurged ids that no search will return.
func (m *Manager) LiveResidentDocCount(gt kgtypes.GraphType, name string) int {
	return m.managerFor(gt, name).engine.LiveResidentCount()
}

// LoadLiveResidentDocCount is the DECIDER half: it loads the graph's HNSW engine
// first (idempotent; L2-only on the OSS path) and then answers. A decider must not
// conclude "uncovered" from an engine that merely has not loaded, so the load error
// is RETURNED rather than swallowed — a caller that could not load declines instead
// of acting on an empty view.
//
// The first call per (graph, format) per process can pay a List+Fetch on a cold
// cloud L2; every later call is amortized to zero by the loaded flag.
//
// AN EVICTED POOL READS 0 AND IS NOT RESURRECTED. This is a BACKGROUND decider's
// probe, and the residency budget's whole point is that a background pass must not
// reload a pool it unloaded. The zero it returns therefore does NOT mean "this graph
// has no live documents" — it means nobody looked. A caller that must not conclude
// "uncovered" from a zero has to consult Manager.PoolEvicted FIRST and decline;
// healNeedsRebuildLocal (bootstrap client_segment_heal_need.go) is the caller that
// does.
func (m *Manager) LoadLiveResidentDocCount(ctx context.Context, gt kgtypes.GraphType, name string) (int, error) {
	dm := m.managerFor(gt, name)
	skipped, err := dm.loadIfResident(ctx)
	if err != nil {
		return 0, err
	}
	if skipped {
		return 0, nil
	}
	return dm.engine.LiveResidentCount(), nil
}

// UncoveredMembers reports, PER FORMAT, which of ids are not live-searchable in the
// graph's resident set — the ids a repair pass would have to re-ship.
//
// It loads BOTH engines first, for the decider reason above: answering from an
// unloaded engine would report the entire corpus missing and turn a no-op pass into
// a corpus-scale re-ship.
//
// The two formats are reported INDEPENDENTLY because they genuinely diverge — a
// document can be resident in one and absent from the other — and a caller shipping
// only the union would either miss a repair or re-ship a format that was fine.
//
// AN EVICTED POOL MAKES THIS AN ERROR, and the error IS the truthful output. Membership
// is not determinable for a pool this background probe declined to reload. The two
// alternatives are both fabrications: an empty missing-set asserts "nothing is missing"
// about a pool nobody looked at, and the full id set would make the repair re-ship the
// entire corpus — exactly what this method's own doc warns against above. Its caller
// (tools segment_repair) already turns a probe error into an aborted pass that ships
// nothing, and the repair decider declines an evicted graph before it reaches here, so
// in production this error is a backstop that does not fire.
func (m *Manager) UncoveredMembers(
	ctx context.Context, gt kgtypes.GraphType, name string, ids []searchengine.ExternalID,
) (missingHNSW, missingBM25 []searchengine.ExternalID, err error) {
	hdm := m.managerFor(gt, name)
	hnswSkipped, err := hdm.loadIfResident(ctx)
	if err != nil {
		return nil, nil, err
	}
	bdm := m.bm25ManagerFor(gt, name)
	bm25Skipped, err := bdm.loadIfResident(ctx)
	if err != nil {
		return nil, nil, err
	}
	if hnswSkipped || bm25Skipped {
		return nil, nil, fmt.Errorf(
			"segmentdist: membership is not determinable for %s/%s — its segment pool is evicted "+
				"(hnsw_evicted=%t bm25_evicted=%t); it re-materializes on the next consumer search",
			gt, name, hnswSkipped, bm25Skipped)
	}
	return hdm.engine.UncoveredFrom(ids), bdm.engine.UncoveredFrom(ids), nil
}
