// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// rebuild_segments_delta_test.go covers the driver's PATH SELECTION and the delta
// path's own reporting. The fixtures here script the SegmentShipper seam rather than
// a real engine: what is under test is which finalize the driver picks, what it
// reports about it, and how it verifies it — none of which needs real segments.

// deltaPriorWatermark is any non-zero watermark; its only job is to make a run
// window-scoped rather than corpus-complete.
const deltaPriorWatermark = int64(1_600_000_000_000_000_000)

// rescanningScanner serves one page per SCAN rather than per request: a request with
// an empty cursor starts a new drain and receives the page, and any later request
// receives the empty page that terminates it.
//
// THE DRIVER'S NOT-APPLICABLE FALLBACK ISSUES A SECOND FULL SCAN, and a fixture whose
// pages are consumed once would hand that second scan nothing — turning an untested
// fallback into a run that reports "found no work" and passing for the wrong reason.
type rescanningScanner struct {
	mu         sync.Mutex
	page       []*knowledgev1.PipelineScanItem
	horizon    int64
	watermarks []int64
}

func (s *rescanningScanner) PipelineScan(_ context.Context, req *knowledgev1.PipelineScanRequest) (*knowledgev1.PipelineScanResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.watermarks = append(s.watermarks, req.GetAfterStampedAtNanos())
	resp := &knowledgev1.PipelineScanResponse{ServedHorizonNanos: s.horizon}
	if req.GetAfterId() == "" {
		resp.Items = s.page
	}
	return resp, nil
}

func (s *rescanningScanner) Execute(context.Context, *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	return &knowledgev1.ExecuteResponse{}, nil
}

func newRescanningScanner(n int) *rescanningScanner {
	return &rescanningScanner{page: makeScanPage("d-", 0, n), horizon: 1_700_000_000_000_000_000}
}

// TestDeltaRunRoutesThroughBucketReEmit is the PATH-SELECTION gate: a run whose scan
// was scoped to a window must finalize through the partition re-emit, and a
// from-scratch run must stage its partitions and finalize exactly once.
//
// The two halves fail differently and both matter. A window-scoped run that STAGES its
// window lays it out as a whole layer and replaces the corpus with it. A from-scratch
// run that finalized more than once, or staged nothing, would publish something other
// than the layer it scanned.
func TestDeltaRunRoutesThroughBucketReEmit(t *testing.T) {
	ctx := context.Background()

	t.Run("a window-scoped run re-emits partitions and never stages a layer", func(t *testing.T) {
		shipper := &fakeRebuildShipper{}
		shipper.watermark = deltaPriorWatermark

		out, err := RebuildSegments(ctx, twoBucketScanner(), shipper, kgtypes.GraphCode, "delta-route", false)
		require.NoError(t, err)
		require.True(t, out.Ran)

		require.Equal(t, int64(1), shipper.deltaCalls.Load(), "the delta finalize runs exactly once")
		require.Zero(t, shipper.stageCalls.Load(), "a delta must not stage its window as a reset layer")
		require.Zero(t, shipper.finalizeCalls.Load(), "a delta must not run the from-scratch finalize")
		require.Equal(t, 2*searchengine.DefaultMinSegmentDocs, shipper.deltaHNSWDocs,
			"every scanned document reaches the re-emit UNGROUPED — grouping here would impose a count derived from the WINDOW")
		require.Equal(t, 2*searchengine.DefaultMinSegmentDocs, shipper.deltaBM25Docs, "and the field corpus gets the same window")
	})

	t.Run("a reset run stages its partitions and finalizes once", func(t *testing.T) {
		shipper := &fakeRebuildShipper{}
		shipper.watermark = deltaPriorWatermark

		out, err := RebuildSegments(ctx, twoBucketScanner(), shipper, kgtypes.GraphCode, "reset-route", true)
		require.NoError(t, err)
		require.True(t, out.Ran)

		require.Zero(t, shipper.deltaCalls.Load(), "a reset is a from-scratch run, never a delta — a reset exists to rebuild from truth")
		require.Positive(t, shipper.stageCalls.Load(), "it stages the partitions it scanned")
		require.Equal(t, int64(1), shipper.finalizeCalls.Load(), "and finalizes once")
		require.Equal(t, shipper.stageCalls.Load(), shipper.stagesAtFinalize,
			"the finalize must run AFTER every partition was staged — a finalize that ran early would publish a partial layer")
	})

	t.Run("an inapplicable delta re-scans from ZERO rather than publishing the window", func(t *testing.T) {
		logs := captureRebuildLogs(t)
		scanner := newRescanningScanner(searchengine.DefaultMinSegmentDocs)
		shipper := &fakeRebuildShipper{deltaNotApplicable: true}
		shipper.watermark = deltaPriorWatermark

		out, err := RebuildSegments(ctx, scanner, shipper, kgtypes.GraphCode, "fallback-route", false)
		require.NoError(t, err)
		require.True(t, out.Ran)

		require.Equal(t, int64(1), shipper.deltaCalls.Load(), "the delta was attempted")
		require.Positive(t, shipper.stageCalls.Load(), "and the fallback ran the from-scratch path, staging its partitions")
		require.Equal(t, int64(1), shipper.finalizeCalls.Load(), "which finalizes exactly once")

		// THE RE-SCAN IS THE CORPUS-WIPE GUARD. Driving the from-scratch path with the
		// delta-scoped items in hand would publish the WINDOW as the whole live set,
		// making dropped = the entire rest of the corpus — and the coverage ratio is
		// disarmed on exactly the near-empty resident set that makes a delta inapplicable,
		// so that publish would land and advance the watermark past what it just reaped.
		// Each scan drains until an empty page, so one scan is several requests; the
		// discriminator is that the watermark on the wire CHANGED to zero for a second
		// drain rather than the driver reusing the window it already held.
		require.GreaterOrEqual(t, len(scanner.watermarks), 3, "the fallback must issue its own scan")
		require.Equal(t, deltaPriorWatermark, scanner.watermarks[0], "the first scan is window-scoped")
		require.Zero(t, scanner.watermarks[len(scanner.watermarks)-1],
			"the fallback scan must reach the wire with a ZERO watermark")
		zeroRequests := 0
		for _, w := range scanner.watermarks {
			if w == 0 {
				zeroRequests++
			}
		}
		require.GreaterOrEqual(t, zeroRequests, 2, "the fallback drains a FULL-corpus scan of its own")
		require.NotEmpty(t, logs.linesContaining(slog.LevelWarn, "delta path not applicable"),
			"an operator must be able to see a graph that keeps missing its delta path")
	})
}

// TestDeltaRunReportsPartitionsBuilt pins what Built MEANS on the delta path.
//
// THE OLD READING IS WHY THE LIVE DEFECT LOOKED CORRECT. The staging path sealed the
// scanned window into segments of its own and reported how many it sealed, so a
// two-node delta that appended one thin unaligned segment reported "2 embedded nodes
// scanned, 1 hash buckets built" — a line an operator reasonably read as success.
//
// The discriminator below is the WINDOW-DERIVED count. A window of 8 documents derives
// ONE bucket on its own; the corpus it belongs to has four. Built must describe the
// CORPUS partitions the window touched, so a Built at or below the window-derived count
// means the driver is still reporting how it would have regrouped the window.
func TestDeltaRunReportsPartitionsBuilt(t *testing.T) {
	const window = 8
	const corpusPartitions = 4
	require.Less(t, searchengine.BucketCountFor(window), corpusPartitions,
		"the fixture needs a window whose own derived count is SMALLER than the corpus's, or the assertion cannot discriminate")

	shipper := &fakeRebuildShipper{deltaBucketCount: corpusPartitions}
	shipper.watermark = deltaPriorWatermark
	scanner := &fakeRebuildScanner{pages: [][]*knowledgev1.PipelineScanItem{makeScanPage("p-", 0, window)}}

	out, err := RebuildSegments(context.Background(), scanner, shipper, kgtypes.GraphCode, "built-repo", false)
	require.NoError(t, err)
	require.True(t, out.Ran)

	require.Equal(t, window, out.Scanned, "Scanned still counts documents")
	require.NotEqual(t, out.Scanned, out.Built, "Built must not be the scanned-item count")
	require.Greater(t, out.Built, searchengine.BucketCountFor(window),
		"Built must describe the CORPUS partitions the window touched, not how the window alone would regroup")
	require.LessOrEqual(t, out.Built, corpusPartitions, "and it cannot exceed the corpus's partition count")
}

// TestDeltaRunReadsBackManifestCardinality covers BOTH read-back halves.
//
// The full half is a WIDENING: a run reached without an explicit reset is still
// corpus-complete when its watermark is zero, and leaving that unmeasured was a blind
// spot. The delta half needs a different mechanism entirely — a delta has no
// corpus-size reading to measure against, so the only implementable assertion is
// equality against a baseline captured BEFORE the re-emit.
//
// THE COUNT GATE IS PART OF THE CONTRACT, not an escape hatch. A delta whose re-emit
// realigns its touched partitions across a power-of-two boundary legitimately grows the
// manifest, so when the count moves there is no honest equality to assert and the run
// must report UNMEASURED rather than flag correct work as a defect.
func TestDeltaRunReadsBackManifestCardinality(t *testing.T) {
	ctx := context.Background()

	t.Run("a zero-watermark full run IS read back", func(t *testing.T) {
		shipper := &fakeRebuildShipper{manifestConfigured: true, manifestCount: 2}
		out, err := RebuildSegments(ctx, twoBucketScanner(), shipper, kgtypes.GraphCode, "full-readback", false)
		require.NoError(t, err)
		require.Zero(t, shipper.deltaCalls.Load(), "a zero watermark is from-scratch, not a delta")
		require.Equal(t, 2, out.ResidentSegmentCount, "the read-back runs without an explicit reset")
	})

	t.Run("the delta captures its baseline BEFORE the re-emit", func(t *testing.T) {
		shipper := &fakeRebuildShipper{manifestSeq: []int{4, 4}, deltaBucketCount: 4}
		shipper.watermark = deltaPriorWatermark
		out, err := RebuildSegments(ctx, twoBucketScanner(), shipper, kgtypes.GraphCode, "baseline-repo", false)
		require.NoError(t, err)
		require.Equal(t, int64(1), shipper.deltaAtManifestRead,
			"exactly one manifest read must precede the re-emit — a baseline captured AFTER it compares the manifest to itself")
		require.Equal(t, int64(2), shipper.manifestReads.Load(), "and one more after it")
		require.Equal(t, 4, out.ResidentSegmentCount, "an unchanged manifest at a stable count is the clean reading")
	})

	t.Run("a GROWN manifest at a stable count is reported", func(t *testing.T) {
		logs := captureRebuildLogs(t)
		// before=4, after=5 at a partition count that did not move: the thin append.
		shipper := &fakeRebuildShipper{manifestSeq: []int{4, 5}, deltaBucketCount: 4}
		shipper.watermark = deltaPriorWatermark

		out, err := RebuildSegments(ctx, twoBucketScanner(), shipper, kgtypes.GraphCode, "grown-repo", false)
		require.NoError(t, err)
		require.Equal(t, 5, out.ResidentSegmentCount, "the reading is reported so a caller can compare it")
		require.NotEmpty(t, logs.linesContaining(slog.LevelWarn, "CHANGED the resident segment cardinality"),
			"a delta re-emits partitions in place, so a changed cardinality at a stable count is the defect")
	})

	t.Run("a MOVED count is unmeasured and never flagged as a fault", func(t *testing.T) {
		logs := captureRebuildLogs(t)
		// The manifest implies 2 partitions; the re-emit ran at 4. A segment aligned to
		// the old count spans two partitions of the new one, so closing over constituency
		// consumes one segment and publishes two — correct work that grows the manifest.
		shipper := &fakeRebuildShipper{manifestSeq: []int{2, 3}, deltaBucketCount: 4}
		shipper.watermark = deltaPriorWatermark

		out, err := RebuildSegments(ctx, twoBucketScanner(), shipper, kgtypes.GraphCode, "realign-repo", false)
		require.NoError(t, err)
		require.Equal(t, derivedBucketCardinalityUnmeasured, out.ResidentSegmentCount,
			"no honest equality holds across a realignment, so the run must report UNMEASURED")
		require.Equal(t, int64(1), shipper.manifestReads.Load(),
			"and it must not even pay the second read once the count is known to have moved")
		require.Empty(t, logs.linesContaining(slog.LevelWarn, "CHANGED the resident segment cardinality"),
			"a legitimate realignment must NEVER be reported as a defect")
	})

	t.Run("a failed baseline disables the check without failing the run", func(t *testing.T) {
		shipper := &fakeRebuildShipper{deltaBucketCount: 4}
		shipper.watermark = deltaPriorWatermark
		out, err := RebuildSegments(ctx, twoBucketScanner(), shipper, kgtypes.GraphCode, "noreadback-repo", false)
		require.NoError(t, err, "the corpus is published by then; a verification read must never fail a landed rebuild")
		require.True(t, out.Published)
		require.Equal(t, derivedBucketCardinalityUnmeasured, out.ResidentSegmentCount)
	})
}

// deltaTrimFixture derives the two axes a retention-trim assertion has to vary: a
// tombstoned id whose partition the delta's window DID touch, and tombstoned ids whose
// partitions it did not. Both are derived through searchengine.BucketOf at the count
// the re-emit reports, never hardcoded, so a fixture that drifts onto the wrong side of
// the predicate fails here rather than greening the assertion.
type deltaTrimFixture struct {
	windowIDs []string
	emitted   map[int]struct{}
	victim    searchengine.ExternalID
	survivors []searchengine.ExternalID
}

func newDeltaTrimFixture(t *testing.T, emittedBucketCount, windowSize int) deltaTrimFixture {
	t.Helper()

	f := deltaTrimFixture{emitted: map[int]struct{}{}}
	for i := range windowSize {
		id := fmt.Sprintf("dw-%08d", i)
		f.windowIDs = append(f.windowIDs, id)
		f.emitted[searchengine.BucketOf(id, emittedBucketCount)] = struct{}{}
	}

	var seen []int
	for i := 0; (f.victim == "" || len(f.survivors) < 2) && i < 4096; i++ {
		id := searchengine.ExternalID(fmt.Sprintf("dt-%d", i))
		part := searchengine.BucketOf(id, emittedBucketCount)
		if _, touched := f.emitted[part]; touched {
			if f.victim == "" {
				f.victim = id
			}
			continue
		}
		if len(f.survivors) >= 2 {
			continue
		}
		for _, p := range seen {
			if p == part {
				part = -1
				break
			}
		}
		if part < 0 {
			continue
		}
		seen = append(seen, part)
		f.survivors = append(f.survivors, id)
	}
	require.NotEmpty(t, f.victim, "fixture could not derive a tombstoned id inside a partition the window touched")
	require.Len(t, f.survivors, 2, "fixture could not derive two tombstoned ids outside every partition the window touched")

	return f
}

// TestDeltaRebuildKeepsTombstonesOutsideItsOwnPartitions drives the DRIVER, because the
// seam that was wrong is the call site rather than the predicate: retainTombstones is
// only as correct as the partition count its caller supplies, and a unit test on the
// helper alone cannot see an arm that hands it the count of its own WINDOW.
//
// A window of a handful of items derives ONE partition on its own
// (searchengine.BucketCountFor is 1 for any corpus up to DefaultMinSegmentDocs), under
// which every tombstoned id maps to partition 0 and the run's own items put partition 0
// in the emitted set — so the delta arm persisted an EMPTY record on any landed
// rebuild, dropping ids whose partitions it never re-emitted.
//
// BOTH AXES ARE VARIED IN ONE RUN. Asserting only that the outside ids survive is
// satisfied by a trim that never fires; asserting only that the inside id is dropped is
// satisfied by a trim that empties the record. The pair is what pins the predicate.
func TestDeltaRebuildKeepsTombstonesOutsideItsOwnPartitions(t *testing.T) {
	const emittedBucketCount = 128
	const window = 4

	require.Less(t, searchengine.BucketCountFor(window), emittedBucketCount,
		"FIXTURE PRECONDITION: the window's own derived count must be smaller than the count the re-emit ran at, "+
			"or this fixture cannot tell the two provenances apart")

	f := newDeltaTrimFixture(t, emittedBucketCount, window)
	t.Logf("re-emit ran at %d partitions: window touched %v, victim sits inside one of them, survivors outside",
		emittedBucketCount, f.emitted)

	shipper := &fakeRebuildShipper{deltaBucketCount: emittedBucketCount}
	shipper.watermark = deltaPriorWatermark
	shipper.tombstoned = append(append([]searchengine.ExternalID(nil), f.survivors...), f.victim)
	scanner := &fakeRebuildScanner{pages: [][]*knowledgev1.PipelineScanItem{bucketScanPage(f.windowIDs)}}

	out, err := RebuildSegments(context.Background(), scanner, shipper, kgtypes.GraphCode, "delta-trim", false)
	require.NoError(t, err)
	require.True(t, out.Ran)
	require.True(t, out.Published, "the trim runs only behind a landed publish, so an unpublished run proves nothing here")

	require.Positive(t, shipper.saves, "the durable record must have been rewritten, or the trim never ran at all")
	require.ElementsMatch(t, f.survivors, shipper.tombstoned,
		"the record must keep exactly the ids whose partitions this window never touched (%v) and drop the one it did (%s)",
		f.survivors, f.victim)
}
