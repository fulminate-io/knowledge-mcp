// SPDX-License-Identifier: Apache-2.0

// manager_coverage_probe.go — the Manager's READ-ONLY coverage surface: the
// standalone per-graph probes the status column, the heal decision and the
// degeneracy backstop each read a number from. Relocated verbatim from
// manager_owner.go.
//
// They are grouped because they answer ONE question from different sides — how
// much of this graph is actually covered. Every one of them reads a LOCAL operand:
// the L2-resident set and the engine's own resident counts. There is no second,
// remote authority to be source-aware about any more.

package segmentdist

import (
	"context"
	"fmt"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/bm25"
)

// ShippedSegmentDocCount is the coverage-ratio probe's data source: the
// segment-covered HNSW doc count for one graph, which is the L2 RESIDENT HNSW doc
// count (LoadResidentDocCount).
//
// ONLY THE HNSW DIMENSION IS COUNTED. BM25 metas index the SAME nodes, so counting
// both would double-count; HNSW is the per-node vector coverage that mirrors the
// binary_vector_count denominator the coverage ratio compares against.
//
// IT RETURNS NO CONSERVATIVE-UNKNOWN FLAG, AND THAT SIGNAL IS GONE RATHER THAN
// ALWAYS-FALSE. The second return meant "at least one segment predates the doc_count
// wire plumbing, so its real coverage is unknowable", and it disarmed the ratio so a
// fleet mid-migration would not read covered=0 on every healthy graph and storm a
// fleet-wide rebuild. It could only ever be true while the count was summed from
// manifest doc counts. That reading is deleted, the count now comes from the engine's
// own resident tally, and no engine reports the pre-doc_count sentinel — so the flag
// was permanently false. A permanently-false bool is the
// stub-returning-hardcoded-values shape, and every caller branching on it had dead
// code behind that branch, so the return is removed rather than pinned to false.
//
// THE NAME IS NOW A LEGACY ONE. Nothing is "shipped" anywhere; this counts what the
// local engine holds. It is kept because it is the tools-side SegmentCoverageReader
// seam's method name and renaming it is a wider sweep than this step owns.
func (m *Manager) ShippedSegmentDocCount(
	ctx context.Context, gt kgtypes.GraphType, name string,
) (covered int, err error) {
	return m.LoadResidentDocCount(ctx, gt, name)
}

// CachedSegmentCount reports how many segments one graph's per-format L2 cache
// HOLDS ON DISK. It is the PRESENCE operand: "has this graph ever produced a corpus
// for this format", answered from the store rather than from the engine.
//
// IT IS DELIBERATELY NOT A RESIDENT COUNT. A resident count reads 0 for an EVICTED
// pool — the residency budget unloaded it while every byte stayed on disk — so using
// one as a presence signal reports a graph with a complete corpus as never having
// shipped. This reads the cache's in-memory index only: no disk read, no load, no
// materialization of an evicted pool, and recency-neutral so asking the question does
// not perturb the LRU ordering the budget sorts on.
func (m *Manager) CachedSegmentCount(gt kgtypes.GraphType, name, format string) int {
	if format == bm25.New().Name() {
		return len(m.bm25ManagerFor(gt, name).cache.Keys())
	}
	return len(m.managerFor(gt, name).cache.Keys())
}

// ResidentDocCount returns the LIVE in-memory HNSW engine resident doc count for
// one graph: the summed sealed-segment DocCount currently imported into the
// searchable set. It is the read-side coverage operand the degeneracy probe
// compares against the server's shipped doc count (the SAME operand
// the reconcile probe reads) — distinct from ShippedSegmentDocCount,
// which reads the SERVER's shipped count. A graph that has never been searched or
// loaded returns 0 (the lazily-constructed engine's set is empty). It is a single
// atomic snapshot (SegmentedIndex.ResidentDocCount) with no RPC and no load — the
// caller decides whether to load() first (the reconcile probe does; the status
// column reads raw current resident).
func (m *Manager) ResidentDocCount(gt kgtypes.GraphType, name string) int {
	return m.managerFor(gt, name).engine.ResidentDocCount()
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

// LoadSegmentDocCounts answers BOTH resident counts for one graph — the SUMMING
// shipped count and the DISTINCT live one — from a single observation of one
// engine. It is the pair manage(status) renders as "shipped N · live M".
//
// IT EXISTS BECAUSE THE PAIR IS ONE MEASUREMENT, NOT TWO. Calling
// LoadResidentDocCount and LoadLiveResidentDocCount in sequence takes two snapshots
// of the same engine at two instants, and a ship, merge or reclaim landing between
// them moves one operand and not the other. Their DIFFERENCE is the duplication
// signal, so a skew of one between the two reads is indistinguishable from one
// genuinely duplicated document — and a skew in the other direction produces a
// negative difference, which is not a negative duplication but a disagreement
// between two readers of one engine. This is the same single-snapshot rule
// tools.GraphCoverageCounts applies to the vector count and the failure count, for
// the same reason: consumers of the pair have zero tolerance.
//
// IT IS THE DECIDER FORM. loadIfResident does NOT resurrect an evicted pool, so an
// evicted graph reports skipped=true rather than a fabricated pair of zeros; a
// caller must decline on skipped rather than read the zeros as a measurement. The
// residency read lock spans the load AND both counts, so an eviction can no more
// land between the two reads than it can between a search's load and its query.
func (m *Manager) LoadSegmentDocCounts(
	ctx context.Context, gt kgtypes.GraphType, name string,
) (shipped, live int, skipped bool, err error) {
	dm := m.managerFor(gt, name)
	dm.residencyMu.RLock()
	defer dm.residencyMu.RUnlock()
	evicted, err := dm.loadIfResident(ctx)
	if err != nil {
		return 0, 0, false, err
	}
	if evicted {
		return 0, 0, true, nil
	}
	return dm.engine.ResidentDocCount(), dm.engine.LiveResidentCount(), false, nil
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
