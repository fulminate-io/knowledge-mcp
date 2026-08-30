// SPDX-License-Identifier: Apache-2.0

// segment_repair.go — the coverage-repair arm: scan the full eligible corpus,
// diff it against what is actually live-searchable, and ship ONLY the difference.
//
// It exists because a node can be embedded on the server and absent from the
// searchable corpus with nothing left to notice: the ship that would have carried
// it was best-effort and swallowed, or it sat in the in-memory backlog when the
// process went away. Neither event leaves the graph degenerate, so the existing
// auto-heal — which owns the collapsed-pool case below the coverage band — declines,
// correctly. This arm owns the band above it.
//
// THE REPAIR IS NOT A REBUILD. It ships through AddAndMarkDirty /
// AddAndMarkDirtyFields, the ordinary embed-writeback producer, and never touches
// the rebuild's stage/finalize path. That is enforced by the SegmentRepairShipper
// type, which does not carry those methods, rather than by convention.

package tools

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/pipeline"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// repairMissingSampleCap bounds how many missing ids either diagnostic line carries
// — the shipped attribution record and the unshipped-remainder line alike. Both are
// records an operator reads, not dumps: a repair that finds thousands of uncovered
// nodes must not put thousands of ids in one log statement.
const repairMissingSampleCap = 20

// RepairOutcome is what one repair pass reports back.
type RepairOutcome struct {
	// Ran reports whether a real pass happened. It is FALSE, WITH A NIL ERROR, when
	// the single-flight coalesced this call into a pass already running for the same
	// graph. A caller deciding whether to record post-pass calibration must read Ran,
	// NOT err: treating the nil-error coalesce as a completed pass records an
	// un-repaired gap as settled residue and disarms the arm for the process
	// lifetime.
	Ran bool
	// ScannedEligible is the TRUE rebuild-eligible corpus size this pass measured —
	// the diagnostic the INFO line carries and an operator reads. It is NOT the
	// calibration operand: calibration is the caller's post-pass re-read, which is
	// what makes convergence robust to the two operands disagreeing.
	ScannedEligible int
	// MissingHNSW / MissingBM25 are the per-format uncovered counts the diff found.
	MissingHNSW, MissingBM25 int
	// ShippedHNSW / ShippedBM25 are the per-format document counts actually handed to
	// the producer. They can trail the Missing counts: an id with no vector builds no
	// HNSW document, and an id with no indexable fields builds no BM25 one.
	ShippedHNSW, ShippedBM25 int
	// ServedHorizonNanos is the safe horizon the server served THIS scan up to. The
	// backstop's scan is unwatermarked, so its horizon is the one honest reading of
	// "current" this graph gets without paying a second read — and it is what seeds
	// the delta merge's horizon for a graph the backstop actually SCANS. A graph the
	// backstop DECLINES at its band gate never reaches this field; its horizon comes
	// from the declined-graph seed instead.
	ServedHorizonNanos int64
}

// SegmentRepairShipper is the repair arm's producer seam, and its NARROWNESS IS THE
// STORM GUARD. It omits every rebuild method — no RebuildSegments, no
// FinalizeRebuild, no StageRebuildPartition, no ReEmitRebuiltDelta — so the repair
// structurally cannot trigger a manifest swap, cannot be refused by the swap-time
// coverage gate (reachable only through the rebuild finalize), and cannot
// re-introduce the full-rebuild storm. Not because a comment forbids it: because the
// type does not carry the methods.
//
// *segmentdist.Manager satisfies this directly; no adapter is needed.
type SegmentRepairShipper interface {
	// UncoveredMembers reports, per format, which of ids are not live-searchable.
	// It TAKES ctx and RETURNS AN ERROR because it must LOAD the engine before
	// answering: an unloaded engine reports every id missing, which would make the
	// repair ship the entire corpus. A load error aborts the pass.
	UncoveredMembers(ctx context.Context, gt kgtypes.GraphType, name string, ids []searchengine.ExternalID) (missingHNSW, missingBM25 []searchengine.ExternalID, err error)

	// AddAndMarkDirty adds HNSW documents and marks their partitions dirty — the
	// same producer call the embed writeback makes.
	AddAndMarkDirty(ctx context.Context, gt kgtypes.GraphType, name string, docs []searchengine.Document) error

	// AddAndMarkDirtyFields is the BM25 counterpart.
	AddAndMarkDirtyFields(ctx context.Context, gt kgtypes.GraphType, name string, docs []searchengine.Document) error

	// ReEmitDirtyBuckets ships the partitions the adds marked, so the repair lands
	// within the pass that found the gap rather than on the next drain tick.
	ReEmitDirtyBuckets(ctx context.Context, gt kgtypes.GraphType, name string) error
}

// segmentRepairInFlight is the per-graph single-flight guard, mirroring
// rebuildSegmentsInFlight's shape (mutex plus set, claimed at entry, released by
// defer). It is needed because the boot-delay reconcile runs in its own goroutine
// while the periodic ticker loop can already be inside a pass.
var (
	segmentRepairInFlightMu sync.Mutex
	segmentRepairInFlight   = map[string]struct{}{}
)

// RepairUncoveredSegments scans the full eligible corpus for one graph, diffs it
// against live-searchable membership, and ships only the ids that are missing.
//
// A PASS THAT FINDS NOTHING MISSING IS A SUCCESS, NOT A NO-OP: it returns Ran=true
// with zero Missing counts and a real ScannedEligible. That distinction is what lets
// the caller calibrate on a converged graph instead of re-firing forever.
//
// A ship error is RETURNED rather than swallowed the way the embed writeback
// swallows its own: the writeback is best-effort because embed liveness wins, but a
// repair that failed must not be reported as one — this arm IS the thing that
// notices swallowed drops.
func RepairUncoveredSegments(
	ctx context.Context, scanner PipelineScanner, shipper SegmentRepairShipper, gt kgtypes.GraphType, name string,
) (RepairOutcome, error) {
	if scanner == nil || shipper == nil {
		return RepairOutcome{}, fmt.Errorf("segment_repair: pipeline not wired — the client is running in degraded mode (no segment engine)")
	}

	// Single-flight per (graphType, name): claim, or return the coalesce
	// (Ran=false, nil error). Keyed on the threaded gt so a custom graph and a code
	// graph of the same name never collide.
	key := string(gt) + "/" + name
	segmentRepairInFlightMu.Lock()
	if _, busy := segmentRepairInFlight[key]; busy {
		segmentRepairInFlightMu.Unlock()
		return RepairOutcome{}, nil
	}
	segmentRepairInFlight[key] = struct{}{}
	segmentRepairInFlightMu.Unlock()
	defer func() {
		segmentRepairInFlightMu.Lock()
		delete(segmentRepairInFlight, key)
		segmentRepairInFlightMu.Unlock()
	}()

	// WATERMARK ZERO — the full corpus, deliberately. A change-scoped scan pages
	// straight past the nodes this arm exists to find: a node whose ship was dropped
	// is BY DEFINITION unchanged since the last landed rebuild, so every
	// watermark-scoped page skips it and the repair converges on nothing.
	items, _, horizon, err := scanRebuildSegmentsAs(ctx, graphclient.OpSegmentRepair, scanner, gt, name, 0, 0)
	if err != nil {
		return RepairOutcome{}, fmt.Errorf("segment_repair scan failed: %w", err)
	}

	out := RepairOutcome{Ran: true, ScannedEligible: len(items), ServedHorizonNanos: horizon}

	ids := make([]searchengine.ExternalID, 0, len(items))
	for _, it := range items {
		ids = append(ids, it.nodeID)
	}

	missingHNSW, missingBM25, err := shipper.UncoveredMembers(ctx, gt, name, ids)
	if err != nil {
		return RepairOutcome{}, fmt.Errorf("segment_repair membership probe failed: %w", err)
	}
	out.MissingHNSW, out.MissingBM25 = len(missingHNSW), len(missingBM25)

	hnswDocs, fieldDocs, err := buildRepairDocuments(items, missingHNSW, missingBM25)
	if err != nil {
		return RepairOutcome{}, fmt.Errorf("segment_repair %s/%s: %w", gt, name, err)
	}

	shipped := false
	if len(hnswDocs) > 0 {
		if err := shipper.AddAndMarkDirty(ctx, gt, name, hnswDocs); err != nil {
			return RepairOutcome{}, fmt.Errorf("segment_repair HNSW ship failed: %w", err)
		}
		out.ShippedHNSW = len(hnswDocs)
		shipped = true
	}
	if len(fieldDocs) > 0 {
		if err := shipper.AddAndMarkDirtyFields(ctx, gt, name, fieldDocs); err != nil {
			return RepairOutcome{}, fmt.Errorf("segment_repair BM25 ship failed: %w", err)
		}
		out.ShippedBM25 = len(fieldDocs)
		shipped = true
	}
	if !shipped {
		// THE ONLY LINE THAT EVER NAMES THESE IDS. An id reported missing that builds
		// no document is missing again on every future pass, and the attribution line
		// below runs only when something shipped — so without this, the one case an
		// operator has to diagnose BY ID is the one case that logs none. DEBUG rather
		// than INFO because a pass with nothing to ship is not an incident; the sample
		// is there for when someone goes looking. Guarded on a non-zero missing count
		// so a converged graph — the other reader of this branch — stays quiet.
		if out.MissingHNSW+out.MissingBM25 > 0 {
			slog.Debug("segment_repair: uncovered ids remain but nothing was buildable to ship",
				"graph_type", gt, "name", name,
				"scanned_eligible", out.ScannedEligible,
				"missing_hnsw", out.MissingHNSW, "missing_bm25", out.MissingBM25,
				"missing_sample", sampleIDs(missingHNSW, missingBM25))
		}
		return out, nil
	}

	if err := shipper.ReEmitDirtyBuckets(ctx, gt, name); err != nil {
		return RepairOutcome{}, fmt.Errorf("segment_repair re-emit failed: %w", err)
	}

	// THIS LINE IS THE AFTER-THE-FACT ATTRIBUTION RECORD for a ship the process lost
	// without a trace — a node dropped from the in-memory backlog by a crash is
	// exactly a node this pass finds embedded-but-uncovered.
	slog.Info("segment_repair: shipped the uncovered difference",
		"graph_type", gt, "name", name,
		"scanned_eligible", out.ScannedEligible,
		"missing_hnsw", out.MissingHNSW, "missing_bm25", out.MissingBM25,
		"shipped_hnsw", out.ShippedHNSW, "shipped_bm25", out.ShippedBM25,
		"missing_sample", sampleIDs(missingHNSW, missingBM25))
	return out, nil
}

// buildRepairDocuments assembles the per-format documents for the MISSING ids only,
// through the SAME builders the embed writeback uses — so a repaired document is
// byte-identical to a freshly-embedded one and the two paths cannot drift.
//
// Serial on purpose, a deliberate departure from the rebuild driver's NumCPU pool:
// that pool builds the WHOLE corpus, this builds only the gap.
// AN UNRESOLVABLE REPRESENTATION ABORTS THE REPAIR RATHER THAN TAGGING, for the
// same reason the rebuild driver refuses: these documents carry vectors ALREADY
// STORED, so a defaulted tag would re-seal a float32 corpus as ubinary and rank
// its bit patterns by Hamming distance with nothing reporting a problem.
func buildRepairDocuments(
	items []rebuildSegItem, missingHNSW, missingBM25 []searchengine.ExternalID,
) (hnswDocs, fieldDocs []searchengine.Document, err error) {
	dtype, err := resolvedEmbedDtype()
	if err != nil {
		return nil, nil, fmt.Errorf(
			"segment repair: cannot determine the representation of the stored vectors being re-sealed: %w", err)
	}

	hnswWanted := idSet(missingHNSW)
	bm25Wanted := idSet(missingBM25)

	vectors := make(map[string][]byte, len(hnswWanted))
	hnswIDs := make([]string, 0, len(hnswWanted))
	bm25Docs := make([]pipeline.SegmentDoc, 0, len(bm25Wanted))
	for _, it := range items {
		if _, want := hnswWanted[it.nodeID]; want {
			vectors[it.nodeID] = it.vector
			hnswIDs = append(hnswIDs, it.nodeID)
		}
		if _, want := bm25Wanted[it.nodeID]; want {
			bm25Docs = append(bm25Docs, pipeline.SegmentDoc{NodeID: it.nodeID, Fields: it.bm25Fields})
		}
	}
	// Same resolved [embedder] representation the ship path and the rebuild
	// driver tag with, so a repaired document is byte-identical to a
	// freshly-embedded one in its dtype as well as its bytes.
	return pipeline.BuildHNSWDocuments(vectors, hnswIDs, dtype), pipeline.BuildBM25Documents(bm25Docs), nil
}

// idSet indexes ids for O(1) membership during the per-item build walk.
func idSet(ids []searchengine.ExternalID) map[string]struct{} {
	s := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		s[id] = struct{}{}
	}
	return s
}

// sampleIDs returns at most repairMissingSampleCap ids drawn from the two missing
// sets, HNSW first, deduped. It is a diagnostic sample, not the set.
func sampleIDs(a, b []searchengine.ExternalID) []string {
	seen := make(map[string]struct{}, repairMissingSampleCap)
	out := make([]string, 0, repairMissingSampleCap)
	for _, group := range [][]searchengine.ExternalID{a, b} {
		for _, id := range group {
			if len(out) >= repairMissingSampleCap {
				return out
			}
			if _, dup := seen[id]; dup {
				continue
			}
			seen[id] = struct{}{}
			out = append(out, id)
		}
	}
	return out
}
