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
// machinery. It returns (outcome, applicable, error); applicable=false means the
// serving engines hold no corpus to re-emit against and the CALLER must recover by
// re-scanning from zero — the outcome is empty and must not be reported.
//
// It NEVER reports a delta that could not be attempted as a completed run. The two
// negative answers are different: a deferred publish (Swapped=false, Applicable=true)
// is a normal outcome that holds the watermark and retries, while an inapplicable
// shape is a recovery the caller owns.
func runDeltaRebuild(
	ctx context.Context, shipper SegmentShipper, gt kgtypes.GraphType, name string, items []rebuildSegItem,
) (RebuildOutcome, bool, error) {
	hnswDocs, bm25Docs := buildRebuildDeltaDocs(items)

	// THE BASELINE IS CAPTURED BEFORE THE CALL, and that is the only mechanism that
	// makes "the manifest must not have grown" implementable here. A delta run has no
	// corpus-size reading to measure the published cardinality against — its build
	// count is a partition count, not a corpus — so the comparison has to be against
	// what this same graph's manifest held a moment ago. A failed reading disables the
	// check rather than guessing.
	before, beforeErr := shipper.PublishedManifestCount(ctx, gt, name, hnsw.New().Name())

	res, err := shipper.ReEmitRebuiltDelta(ctx, gt, name, hnswDocs, bm25Docs)
	if err != nil {
		return RebuildOutcome{}, true, fmt.Errorf("delta re-emit failed: %w", err)
	}
	if !res.Applicable {
		return RebuildOutcome{}, false, nil
	}

	out := RebuildOutcome{
		Ran: true, Scanned: len(items),
		Built:             deltaPartitionsTouched(items, res.DerivedBucketCount),
		Published:         res.Swapped,
		PublishedManifest: manifestCardinalityUnmeasured,
	}
	if res.Swapped {
		out.PublishedManifest = readBackDeltaManifestCardinality(
			ctx, shipper, gt, name, before, beforeErr, res.DerivedBucketCount)
	}
	return out, true, nil
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

// readBackDeltaManifestCardinality reads the published HNSW manifest back after a
// landed delta swap and reports its entry count, WARNing when a delta CHANGED it.
//
// A DELTA RE-EMITS PARTITIONS; IT DOES NOT ADD OR DROP THEM. At a stable partition
// count the manifest must hold exactly what it held before: growth is the thin-append
// defect (a window sealed into a segment of its own and published beside every
// untouched bucket blob), and shrinkage means content stopped being referenced.
//
// THE CHECK IS GATED ON A STABLE COUNT, and the gate is not a loophole — it is the
// difference between a defect and correct work. A delta whose re-emit realigns its
// touched partitions across a power-of-two boundary legitimately grows the manifest: a
// segment aligned to the old count spans two partitions of the new one, so closing over
// constituency consumes one segment and publishes two. When the count the re-emit ran
// at differs from the one the baseline implies, there is no honest equality to assert,
// and the run reports UNMEASURED rather than flagging correct work as a fault. The same
// gate absorbs a raced embed drain, which can grow the manifest between the two
// readings for reasons this run did not cause.
//
// A FAILED READ-BACK IS NOT A FAILED REBUILD. The corpus is published by the time this
// runs; the failure is logged and the cardinality reported unmeasured.
func readBackDeltaManifestCardinality(
	ctx context.Context, shipper SegmentShipper, gt kgtypes.GraphType, name string,
	before int, beforeErr error, derivedBucketCount int,
) int {
	if beforeErr != nil {
		slog.Debug("rebuild_segments: delta manifest baseline unavailable — cardinality check disabled for this run (rebuild unaffected)",
			"graph_type", gt, "name", name, "err", beforeErr)
		return manifestCardinalityUnmeasured
	}
	// The baseline implies a partition count: an aligned corpus carries one segment per
	// partition. A re-emit that ran at a different count realigned, so no equality holds.
	if derivedBucketCount != before {
		slog.Info("rebuild_segments: delta ran at a different partition count than the published manifest implies — cardinality check not applicable (realignment, not a fault)",
			"graph_type", gt, "name", name,
			"manifest_before", before, "derived_bucket_count", derivedBucketCount)
		return manifestCardinalityUnmeasured
	}
	after, err := shipper.PublishedManifestCount(ctx, gt, name, hnsw.New().Name())
	if err != nil {
		slog.Debug("rebuild_segments: delta manifest cardinality read-back unavailable (rebuild unaffected)",
			"graph_type", gt, "name", name, "err", err)
		return manifestCardinalityUnmeasured
	}
	if after != before {
		slog.Warn("rebuild_segments: the DELTA run CHANGED the published manifest cardinality at a stable partition count — "+
			"a delta re-emits partitions in place, so a change means it appended or dropped one",
			"graph_type", gt, "name", name, "format", hnsw.New().Name(),
			"manifest_before", before, "manifest_after", after)
	}
	return after
}
