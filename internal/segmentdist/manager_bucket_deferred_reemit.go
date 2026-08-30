// SPDX-License-Identifier: Apache-2.0

// manager_bucket_deferred_reemit.go — the DEFERRED RE-EMIT LEDGER READER: which of the
// partitions the durable tombstone mask still owes a re-emit this tick should serve.
//
// It sits beside manager_bucket_backlog_state.go rather than inside it because the two
// read different sets. That file owns the WRITE backlog's own accounting — documents
// queued by writes, waiting for a drain. This one owns a set nothing queued: the ids the
// durable tombstone record names, whose partitions have not yet been re-emitted without
// them. The record is the ledger; no second durable structure exists or is needed, which
// is also why a restart loses nothing here.

package segmentdist

import (
	"log/slog"
	"sort"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

const (
	// deferredReEmitPartitionBudget bounds how many partitions one drain may take on
	// deferred-delete account.
	//
	// WHAT IT BOUNDS is the DIRTY set the drain SEEDS — the partitions the selected ids
	// route to — not the CLOSED set replaceBucketGroups ends up rebuilding.
	// closeOverConstituency grows the dirty map in place, and manager_bucket_partition.go
	// states the bound that growth reaches: nothing on a stable count, and across a
	// power-of-two crossing at most double the dirty set when every segment is one count
	// behind, "which is the common case in steady traffic rather than a guarantee this
	// code enforces". So 8 seeded is 8 rebuilt in the steady state and up to about 16
	// across a crossing.
	//
	// WHY 8, AND WHAT THE MEASUREMENT ACTUALLY COVERS. BenchmarkCrossSegmentSerialAcross
	// measured K=8 at 478ms against a single 1024-node build's 459ms, while K=32 cost
	// 1327ms against an ideal 918ms — so the per-partition cost is still near-flat at 8
	// and has left the plateau by 32. That 478ms is the ALIGNED case, eight independent
	// 1024-node builds, so it is a FLOOR for a real drain rather than its cost: real
	// partitions carry whatever their survivors carry, and the closure may widen the set
	// across a crossing. Measured on an Apple M4 Max at NumCPU=16 — a machine-anchored
	// default, not a universal constant.
	//
	// WHAT IT MEANS TO AN OPERATOR. At a five-minute segment reconcile interval, the ~113
	// partitions a 240-id delete dirties drain at 8 per tick over about 15 ticks, so
	// roughly 75 minutes — AS AN UPPER BOUND, NEVER AS A SCHEDULE. The background merger
	// reclaims some of these partitions on its own before the drain reaches them, under
	// its dead-ratio trigger (DeletesPctAllowed, default 0.33), so convergence EARLIER
	// than this figure is the expected behaviour and not a fault. Throughout the window
	// the deleted documents are invisible to every reader — the mask and the killed live
	// bits both exclude them — and what remains outstanding is blob size alone.
	deferredReEmitPartitionBudget = 8
)

// deferredReEmitIDs selects the masked ids a drain should serve this tick: every id
// belonging to the lowest-numbered deferredReEmitPartitionBudget partitions the mask
// spans, or nil when this graph cannot be served safely.
//
// THE CORPUS-LOADED GATE COMES FIRST AND IS THE WHOLE CORRECTNESS ARGUMENT, not
// defensive padding. Partition numbers are meaningless unless the count they were
// derived under describes a corpus that is actually resident. On an engine holding
// nothing, BucketCountFor(~0) derives ONE partition and BucketOf collapses every masked
// id onto it — a single partition that fits inside any budget, so the whole mask would
// be offered as one partition's worth of work and trimmed on one publish. That state is
// ORDINARY rather than exotic: a fresh process hydrates the entire mask from the durable
// record on its first read (Manager.graphTombstones), while the pools stay unloaded
// until something reads them.
//
// The guard is the one ReEmitRebuiltDelta already applies against exactly this
// arithmetic: both pools must clear residentBackstopFloor, read through
// ResidentDocCount. Below the floor nothing is lost by declining — the graph's
// partitions converge on the next ordinary write, whose drain rebuilds them and drops
// the dead members, or on a full rebuild.
//
// A DECLINE IS ANNOUNCED, NEVER SILENT, and the mask is read BEFORE the gate so the
// announcement can say how much work is outstanding. The decline is not always
// transient: a graph whose FIELD corpus never clears the floor — one whose documents
// carry none of the field format's indexed vocabulary, say — is declined on every tick
// forever, and its blob size and mask grow the whole time with nothing in the logs. An
// operator has to be able to see that, so the decline WARNs with both resident counts,
// the floor they failed, and the number of ids still masked. A graph with an empty mask
// returns before either the gate or its log line, so a converged graph stays quiet.
//
// THE BUDGET IS EXACT ON HNSW AND APPROXIMATE ON BM25. The two pools derive their counts
// independently, so the same ids can span a different number of BM25 partitions than
// HNSW ones. The count here is the HNSW pool's — the expensive format, carrying the bulk
// of a delete's re-emit cost, whose per-partition rebuild measured a p50 of 332ms
// against BM25's 104ms — so the bound holds exactly where it matters and loosely where
// it does not.
//
// ASCENDING PARTITION ORDER CANNOT STARVE a partition: one the drain publishes leaves
// the mask at the trim and is never offered again, so the lowest-numbered set advances.
func (m *Manager) deferredReEmitIDs(gt kgtypes.GraphType, name string) []searchengine.ExternalID {
	// THE MASK IS READ FIRST so a decline below can report what it declined. A graph
	// with nothing outstanding returns here, before the gate and before any log line.
	masked := m.graphTombstones(gt, name)
	if len(masked) == 0 {
		return nil
	}

	hnswDM := m.managerFor(gt, name)
	bm25DM := m.bm25ManagerFor(gt, name)
	hnswResident, bm25Resident := hnswDM.engine.ResidentDocCount(), bm25DM.engine.ResidentDocCount()
	if hnswResident < residentBackstopFloor || bm25Resident < residentBackstopFloor {
		slog.Warn("segmentdist: deferred re-emit DECLINED — the serving engines hold no corpus to derive partitions against (the masked ids stay outstanding)",
			"graph_type", gt, "name", name, "hnsw_resident", hnswResident, "bm25_resident", bm25Resident,
			"floor", residentBackstopFloor, "masked", len(masked))
		return nil
	}

	// count-provenance: corpus-derived — the resident set IS the corpus here, and the
	// floor gate above is what makes that true rather than assumed; a count taken from an
	// unloaded pool would be corpus-derived in shape and degenerate in fact.
	//
	// THE ADMITTED PROVENANCE CHECK DOES NOT FIRE ON THIS SITE, and the declaration is
	// documentation rather than a gate's demand — it is not redundant and must not be
	// deleted as such. That check matches a bare len(identifier) argument; this argument
	// is a corpus-count expression, one of the shapes the check itself names as
	// conforming without a declaration.
	bucketCount := searchengine.BucketCountFor(hnswDM.engine.DistinctResidentDocCount())

	byPartition := make(map[int][]searchengine.ExternalID, len(masked))
	for _, id := range masked {
		b := searchengine.BucketOf(id, bucketCount)
		byPartition[b] = append(byPartition[b], id)
	}

	partitions := make([]int, 0, len(byPartition))
	for b := range byPartition {
		partitions = append(partitions, b)
	}
	sort.Ints(partitions)
	if len(partitions) > deferredReEmitPartitionBudget {
		partitions = partitions[:deferredReEmitPartitionBudget]
	}

	// WHOLE PARTITIONS, NEVER A PARTIAL ONE. An id left behind in a partition the drain
	// re-emits would be trimmed by nothing — the trim keys on published partitions — and
	// would be re-offered on every tick forever.
	out := make([]searchengine.ExternalID, 0, len(masked))
	for _, b := range partitions {
		out = append(out, byPartition[b]...)
	}
	// THE SERVING SIDE IS ANNOUNCED TOO, because convergence is otherwise unobservable:
	// partitions_outstanding falling tick over tick is the only external signal that the
	// mask is draining rather than standing still, and it is what distinguishes a bounded
	// backstop from a lane firing forever on the same cause.
	slog.Info("segmentdist: deferred re-emit serving masked partitions",
		"graph_type", gt, "name", name, "partitions_served", len(partitions),
		"partitions_outstanding", len(byPartition), "ids_served", len(out), "masked", len(masked),
		"budget", deferredReEmitPartitionBudget)
	return out
}
