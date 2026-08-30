// SPDX-License-Identifier: Apache-2.0

// rebuild_segments_delta.go — the rebuild driver's DELTA arm: hand a watermark-scoped
// window to the partition machinery, report what it re-emitted, and check the
// published manifest did not grow.
//
// It sits beside the driver rather than inside it because its verification is a
// different shape from the full path's. A from-scratch run knows what the corpus
// should be and can measure the manifest against its own build count; a delta knows
// only what CHANGED, so the only thing it can honestly assert is that a re-emit left
// the cardinality where it found it — which needs a baseline captured before the call.

package tools

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/hnsw"
)

// runDeltaRebuild finalizes a watermark-scoped window through the partition
// machinery. It returns (outcome, emittedBucketCount, applicable, error);
// applicable=false means the serving engines hold no corpus to re-emit against and the
// CALLER must recover by re-scanning from zero — the outcome is empty and must not be
// reported.
//
// THE COUNT IS REPORTED BECAUSE ONLY THIS ARM KNOWS IT. It is the count the re-emit
// actually ran against — derived from the RESIDENT corpus, never from the window in
// hand — and the caller's retention trim is wrong under any other, because a window of
// at most DefaultMinSegmentDocs derives a single partition and collapses every masked
// id onto it. It is zero on every path that reports no completed re-emit.
//
// It NEVER reports a delta that could not be attempted as a completed run. The two
// negative answers are different: a deferred publish (Swapped=false, Applicable=true)
// is a normal outcome that holds the watermark and retries, while an inapplicable
// shape is a recovery the caller owns.
func runDeltaRebuild(
	ctx context.Context, shipper SegmentShipper, gt kgtypes.GraphType, name string, items []rebuildSegItem,
) (RebuildOutcome, int, bool, error) {
	// Applicable=true: an unresolvable representation is a recoverable
	// configuration fault, not a shape this delta can never handle — the caller
	// holds the watermark and retries once the config resolves.
	hnswDocs, bm25Docs, err := buildRebuildDeltaDocs(items)
	if err != nil {
		return RebuildOutcome{}, 0, true, fmt.Errorf("delta rebuild %s/%s: %w", gt, name, err)
	}

	// THE BASELINE IS CAPTURED BEFORE THE CALL, and that is the only mechanism that
	// makes "the live set must not have grown" implementable here. A delta run has no
	// corpus-size reading to measure the resident set against — its build count is a
	// partition count, not a corpus — so the comparison has to be against what this
	// same graph's engine held a moment ago.
	before := shipper.ResidentSegmentCount(gt, name, hnsw.New().Name())

	res, err := shipper.ReEmitRebuiltDelta(ctx, gt, name, hnswDocs, bm25Docs)
	if err != nil {
		return RebuildOutcome{}, 0, true, fmt.Errorf("delta re-emit failed: %w", err)
	}
	if !res.Applicable {
		return RebuildOutcome{}, 0, false, nil
	}

	out := RebuildOutcome{
		Ran: true, Scanned: len(items),
		Built:                deltaPartitionsTouched(items, res.DerivedBucketCount),
		Published:            res.Swapped,
		ResidentSegmentCount: derivedBucketCardinalityUnmeasured,
	}
	if res.Swapped {
		out.ResidentSegmentCount = readDeltaResidentCardinality(
			shipper, gt, name, before, res.DerivedBucketCount)
	}
	return out, res.DerivedBucketCount, true, nil
}

// deltaPartitionsTouched counts the DISTINCT partitions the scanned window owns under
// the count the re-emit ran at — the delta path's Built.
//
// BUILT MEANS PARTITIONS RE-EMITTED, NOT ITEMS SEALED, and the distinction is what
// made the live defect read as success. The old path sealed the window into segments
// of its own and reported how many it sealed, so a two-node delta that appended one
// thin unaligned segment reported "2 embedded nodes scanned, 1 hash buckets built" —
// a line an operator reasonably read as correct. Counting partitions instead makes the
// number describe what actually changed in the corpus.
func deltaPartitionsTouched(items []rebuildSegItem, bucketCount int) int {
	touched := make(map[int]struct{}, len(items))
	for _, it := range items {
		touched[searchengine.BucketOf(it.nodeID, bucketCount)] = struct{}{}
	}
	return len(touched)
}

// readDeltaResidentCardinality reads how many sealed HNSW segments the engine holds
// after a landed delta swap and reports it, WARNing when a delta CHANGED the count.
//
// A DELTA RE-EMITS PARTITIONS; IT DOES NOT ADD OR DROP THEM. At a stable partition
// count the engine must hold exactly what it held before: growth is the thin-append
// defect (a window sealed into a segment of its own and left beside every untouched
// bucket segment), and shrinkage means content stopped being in the live set.
//
// THE OPERANDS ARE THE SAME ENGINE READ TWICE, BEFORE AND AFTER, and that is exactly
// what makes it a real check rather than the identity the full-rebuild path had to
// avoid. It is not comparing a number to a restatement of itself; it is comparing the
// live set to its own prior state across an operation that is supposed to leave the
// cardinality alone.
//
// THE CHECK IS GATED ON A STABLE COUNT, and the gate is not a loophole — it is the
// difference between a defect and correct work. A delta whose re-emit realigns its
// touched partitions across a power-of-two boundary legitimately grows the set: a
// segment aligned to the old count spans two partitions of the new one, so closing over
// constituency consumes one segment and produces two. When the count the re-emit ran
// at differs from the one the baseline implies, there is no honest equality to assert,
// and the run reports UNMEASURED rather than flagging correct work as a fault. The same
// gate absorbs a raced embed drain, which can grow the set between the two readings for
// reasons this run did not cause.
//
// NEITHER READ CAN FAIL. Its predecessor read a server manifest and needed a
// baseline-unavailable path and a read-back-failure path; both operands are now one
// atomic snapshot load, so the only unmeasured outcome left is the realignment gate.
func readDeltaResidentCardinality(
	shipper SegmentShipper, gt kgtypes.GraphType, name string,
	before int, derivedBucketCount int,
) int {
	// The baseline implies a partition count: an aligned corpus carries one segment per
	// partition. A re-emit that ran at a different count realigned, so no equality holds.
	if derivedBucketCount != before {
		slog.Info("rebuild_segments: delta ran at a different partition count than the resident set implies — cardinality check not applicable (realignment, not a fault)",
			"graph_type", gt, "name", name,
			"resident_before", before, "derived_bucket_count", derivedBucketCount)
		return derivedBucketCardinalityUnmeasured
	}
	after := shipper.ResidentSegmentCount(gt, name, hnsw.New().Name())
	if after != before {
		slog.Warn("rebuild_segments: the DELTA run CHANGED the resident segment cardinality at a stable partition count — "+
			"a delta re-emits partitions in place, so a change means it appended or dropped one",
			"graph_type", gt, "name", name, "format", hnsw.New().Name(),
			"resident_before", before, "resident_after", after)
	}
	return after
}
