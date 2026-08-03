// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// TestRebuildOutcomeCarriesPerFormatRetirement is the REPORTING half of the
// per-format retirement decision. Its sibling in segmentdist asserts the finalize
// RESULT carries both sets; this asserts the driver carries them through to the
// surface an operator actually reads.
//
// THE DEFECT IT CLOSES IS A MISREADING, NOT A DATA LOSS. The completion line used to
// emit one `pruned` count taken from the HNSW set alone. A live cloud run hit exactly
// the asymmetric case — the vector corpus had already been converged by the
// tombstone-delta consumer, so the line read pruned=0 while all eight bm25 blobs
// retired — and an operator reading it concluded the reset had retired nothing. One
// number cannot describe two corpora that carry separate manifests and retire
// independently.
//
// IT IS SCOPED HERE AND NOT TO segmentdist BECAUSE THE LINE EXISTS HERE. It is emitted
// by finishRebuild in this package and segmentdist holds zero references to it, so a
// criterion running in the engine scope could not observe this half at all.
//
// THE LINE IS READ OFF THE DRIVER'S OWN EMIT through the real default logger, never
// from a string the test assembled. A test that formatted its own expectation would
// pass against a driver that had stopped logging altogether.
func TestRebuildOutcomeCarriesPerFormatRetirement(t *testing.T) {
	ctx := context.Background()

	// THE ASYMMETRIC FIXTURE: the vector corpus retires NOTHING (already converged) and
	// the field corpus retires two blobs. This is the shape that reads as zero under a
	// single collapsed count, so a driver that had not been split would report the
	// retirement as absent rather than as bm25's.
	bm25Retired := []searchengine.SegmentID{"bm25-seg-old-1", "bm25-seg-old-2"}
	shipper := &fakeRebuildShipper{pruned: nil, bm25Pruned: bm25Retired}

	logs := captureRebuildLogs(t)
	out, err := RebuildSegments(ctx, twoBucketScanner(), shipper, kgtypes.GraphCode, "per-format-report", true)
	require.NoError(t, err)
	require.True(t, out.Ran)
	require.True(t, out.Published, "the swap must land, or the completion line below describes a run that did nothing")

	// (1) THE OUTCOME CARRIES BOTH SETS SEPARATELY. Empty-on-one-side is the whole
	// point: a collapsed field could not represent it.
	require.Empty(t, out.HNSWPruned,
		"the vector corpus retired nothing in this fixture — asserting it is what makes the bm25 leg meaningful")
	require.Equal(t, bm25Retired, out.BM25Pruned,
		"the outcome must expose the bm25 retired set — a collapsed count reports this run as having retired nothing")

	// (2) INVALIDATION STAYS HNSW-ONLY. Reporting a set is not evicting it: local bm25
	// orphans are PruneCache's job, and feeding the field set here would evict blobs the
	// finalize is not responsible for.
	require.Len(t, shipper.invalidate, 1, "InvalidateLocal fires once")
	require.Empty(t, shipper.invalidate[0],
		"InvalidateLocal is fed the HNSW set ONLY — it must not have been handed the bm25 retirement")

	// (3) THE OPERATOR LINE REPORTS PER FORMAT. Both keys must be present with their own
	// values; a line carrying one `pruned` number is what this fails against.
	lines := logs.linesContaining(slog.LevelInfo, "rebuild_segments: run complete")
	require.Len(t, lines, 1, "the run emits exactly one completion line")
	line := lines[0]
	require.Contains(t, line, "hnsw_pruned=0",
		"the completion line must report the vector retirement under its OWN key: %s", line)
	require.Contains(t, line, "bm25_pruned=2",
		"the completion line must report the field retirement under its OWN key — this is the number that read as absent: %s", line)
	require.False(t, strings.Contains(line, " pruned="),
		"no single collapsed `pruned` count may survive alongside the per-format pair: %s", line)
}
