// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// rebuild_segments_watermark_test.go covers the change-scoping tail of the
// segment_rebuild driver: the watermark reaches the wire, the served horizon comes
// back, and a tombstoned item is split out of the build stream instead of being
// handed to the document builders.
//
// It carries its own scanner fake rather than extending the driver fixtures,
// because it needs to script two fields those fixtures do not model.

// fakeWatermarkScanner returns scripted pages and records what each request
// carried, so the test can assert the watermark was forwarded and the cursor
// advanced correctly.
type fakeWatermarkScanner struct {
	mu sync.Mutex

	pages    [][]*knowledgev1.PipelineScanItem
	horizon  int64
	pageIter int

	// failAfterPage, when positive, makes the scan FAIL once that many pages have
	// been served — a drain that started and could not finish. It is the only way to
	// build a genuinely TRUNCATED drain: the scan terminates normally on an empty
	// page, so every other shape is a complete one.
	failAfterPage int

	cursors    []string
	watermarks []int64
}

// errScanTruncated is what a drain that could not finish reports.
var errScanTruncated = errors.New("pipeline scan failed mid-drain")

func (f *fakeWatermarkScanner) PipelineScan(_ context.Context, req *knowledgev1.PipelineScanRequest) (*knowledgev1.PipelineScanResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cursors = append(f.cursors, req.GetAfterId())
	f.watermarks = append(f.watermarks, req.GetAfterStampedAtNanos())
	if f.failAfterPage > 0 && f.pageIter >= f.failAfterPage {
		return nil, errScanTruncated
	}
	resp := &knowledgev1.PipelineScanResponse{ServedHorizonNanos: f.horizon}
	if f.pageIter < len(f.pages) {
		resp.Items = f.pages[f.pageIter]
		f.pageIter++
	}
	return resp, nil
}

func (f *fakeWatermarkScanner) Execute(context.Context, *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	return &knowledgev1.ExecuteResponse{}, nil
}

func liveScanItem(id string) *knowledgev1.PipelineScanItem {
	vec := make([]byte, 32)
	vec[0] = id[len(id)-1]
	return &knowledgev1.PipelineScanItem{
		NodeId:       id,
		GraphName:    "myrepo",
		BinaryVector: vec,
		Bm25Fields:   &knowledgev1.Bm25Fields{SymbolName: id},
	}
}

// TestScanRebuildSegments_SplitsTombstonesAndReturnsHorizon pins all three parts
// of the change-scoping tail at once.
//
// The tombstone is scripted LAST in its page deliberately. The cursor must advance
// past it — taken from the raw page rather than from the surviving live items — or
// a page whose tail is a delete re-reads itself forever.
func TestScanRebuildSegments_SplitsTombstonesAndReturnsHorizon(t *testing.T) {
	const horizon = int64(1_700_000_000_123_456_789)
	const watermark = int64(1_699_999_999_000_000_000)

	dead := &knowledgev1.PipelineScanItem{NodeId: "n-0003", GraphName: "myrepo", Tombstoned: true}
	scanner := &fakeWatermarkScanner{
		horizon: horizon,
		pages: [][]*knowledgev1.PipelineScanItem{
			{liveScanItem("n-0001"), liveScanItem("n-0002"), dead},
		},
	}

	items, tombstoned, served, err := scanRebuildSegments(
		context.Background(), scanner, kgtypes.GraphCode, "myrepo", watermark)
	require.NoError(t, err)

	ids := make([]string, 0, len(items))
	for _, it := range items {
		ids = append(ids, it.nodeID)
		assert.NotEmpty(t, it.vector, "a live item must keep its vector")
	}
	assert.Equal(t, []string{"n-0001", "n-0002"}, ids,
		"the tombstone must NOT reach the build stream — it has no vector, and a zero-vector document would land in the HNSW segment")
	assert.Equal(t, []string{"n-0003"}, tombstoned, "the tombstoned id must be reported separately")
	assert.Equal(t, horizon, served, "the served horizon must be returned for the caller's watermark advance")

	assert.Equal(t, watermark, scanner.watermarks[0], "the watermark must reach the wire")
	require.GreaterOrEqual(t, len(scanner.cursors), 2, "the drain must page until an empty page")
	assert.Equal(t, "n-0003", scanner.cursors[1],
		"the cursor must advance past a TOMBSTONED tail item; taking it from the surviving live items would re-read that page forever")
}

// TestScanRebuildSegments_ZeroWatermarkIsFullCorpus pins the back-compat default
// the driver relies on: no watermark on the wire means the server serves
// everything, exactly as before this axis gained change scoping.
func TestScanRebuildSegments_ZeroWatermarkIsFullCorpus(t *testing.T) {
	scanner := &fakeWatermarkScanner{
		pages: [][]*knowledgev1.PipelineScanItem{{liveScanItem("n-0001")}},
	}

	items, tombstoned, served, err := scanRebuildSegments(
		context.Background(), scanner, kgtypes.GraphCode, "myrepo", 0)
	require.NoError(t, err)

	assert.Len(t, items, 1)
	assert.Empty(t, tombstoned, "a full-corpus scan emits no tombstones")
	assert.Zero(t, served, "a server that echoes no horizon leaves the caller's watermark unmoved")
	assert.Zero(t, scanner.watermarks[0], "the driver must send a ZERO watermark today")
}

// watermarkCorpusN is the fixture corpus every driver-level case below rebuilds.
// It is a fixture CONSTANT, not a count read back from anything the driver
// produced, so an expectation derived from it cannot agree with a broken driver.
const watermarkCorpusN = 2048

// newWatermarkFixture builds a scanner holding one page of watermarkCorpusN live
// items plus the supplied tombstoned ids, echoing horizon.
func newWatermarkFixture(horizon int64, tombstonedIDs ...string) *fakeWatermarkScanner {
	page := makeScanPage("n-", 0, watermarkCorpusN)
	for _, id := range tombstonedIDs {
		page = append(page, &knowledgev1.PipelineScanItem{NodeId: id, GraphName: "myrepo", Tombstoned: true})
	}
	return &fakeWatermarkScanner{horizon: horizon, pages: [][]*knowledgev1.PipelineScanItem{page}}
}

// TestWatermarkAdvancesOnlyAfterSuccessfulPublish drives the three outcomes a
// finalize can have and pins the watermark against each.
//
// THE CATCHER IS THE SKIP LEG. A publish that the coverage gate refuses returns a
// NIL ERROR — every skip does — so a driver that reads the error as the completion
// signal treats the skip as a success and advances past a window it never
// published. Nothing was made durable in that window, and an advanced watermark
// means it is never scanned again: a permanent hole. A happy-path-only test passes
// with that bug present, which is why the skip and error legs are here.
//
// The success leg asserts EQUALITY with the served horizon rather than "it moved
// forward". A driver that stamped its own clock would also move forward, and only
// equality catches it — the horizon constant below is deliberately nothing a clock
// would produce.
func TestWatermarkAdvancesOnlyAfterSuccessfulPublish(t *testing.T) {
	const servedHorizon = int64(1_700_000_000_123_456_789)
	// The watermark a prior landed rebuild left behind. The skip and error legs must
	// leave exactly this value in place.
	const priorWatermark = int64(1_600_000_000_000_000_000)

	t.Run("a landed swap advances to exactly the served horizon", func(t *testing.T) {
		scanner := newWatermarkFixture(servedHorizon, "n-deleted")
		shipper := &fakeRebuildShipper{}

		out, err := RebuildSegments(
			context.Background(), scanner, shipper, kgtypes.GraphCode, "advance-repo", false)
		require.NoError(t, err)
		require.True(t, out.Ran)
		require.Equal(t, watermarkCorpusN, out.Scanned, "the tombstone is not a scanned document")
		require.Positive(t, out.Built)
		require.True(t, out.Published, "a landed swap must be reported as PUBLISHED, not merely shipped")

		assert.Equal(t, servedHorizon, shipper.savedWatermark(),
			"the watermark must equal the SERVER-served horizon; any other value means the driver substituted a reading of its own")
		assert.Equal(t, 1, shipper.saveCount(), "one landed publish persists the record exactly once")

		// The ids the engines must seed dead at Import are handed over before any
		// build, so a load racing this rebuild cannot resurrect the removed node.
		require.NotEmpty(t, shipper.seeded)
		assert.Equal(t, []searchengine.ExternalID{"n-deleted"}, shipper.seeded[0])
		// This run re-emitted every partition, so no shipped blob still carries the id
		// and it leaves the record.
		assert.Empty(t, shipper.tombstoned,
			"an id whose partition was rebuilt without it can no longer be resurrected, so it is dropped")
	})

	t.Run("a coverage-gate skip holds the watermark", func(t *testing.T) {
		scanner := newWatermarkFixture(servedHorizon)
		// The skip is scripted on BOTH finalizes — a nil error and no manifest swap —
		// because a prior watermark scopes this run to a window, which finalizes through
		// the delta path. Scripting only the full path's skip would leave the delta
		// reporting a landed swap and the subject of this test unexercised.
		shipper := &fakeRebuildShipper{noSwap: true, deltaNoSwap: true}
		shipper.watermark = priorWatermark

		out, err := RebuildSegments(
			context.Background(), scanner, shipper, kgtypes.GraphCode, "skip-repo", false)
		require.NoError(t, err, "a skipped publish is not an error — that is exactly why the error cannot be the signal")
		require.True(t, out.Ran)
		require.False(t, out.Published, "a skipped publish must be reported as NOT published; the nil error says nothing")

		assert.Equal(t, priorWatermark, shipper.savedWatermark(),
			"a skipped publish made nothing durable; advancing past that window would never re-scan it")
		assert.Zero(t, shipper.saveCount(), "no record write at all on a skip")
	})

	t.Run("a ship error holds the watermark", func(t *testing.T) {
		scanner := newWatermarkFixture(servedHorizon)
		// Scripted on both finalizes, for the same reason the skip above is: a prior
		// watermark routes this run through the delta path.
		shipper := &fakeRebuildShipper{
			finalizeErr: errors.New("ship transport failed"),
			deltaErr:    errors.New("ship transport failed"),
		}
		shipper.watermark = priorWatermark

		_, err := RebuildSegments(
			context.Background(), scanner, shipper, kgtypes.GraphCode, "error-repo", false)
		require.Error(t, err)

		assert.Equal(t, priorWatermark, shipper.savedWatermark(), "a failed ship advances nothing")
		assert.Zero(t, shipper.saveCount())
	})
}

// TestZeroWatermarkRebuildsFullCorpus pins the two ways a rebuild is asked for
// everything: a daemon with no record yet, and an operator who asks for a
// from-scratch run over one that has a record. Both must put a ZERO on the wire
// and emit every partition — the escape hatch is worthless if a stored watermark
// can still narrow it.
func TestZeroWatermarkRebuildsFullCorpus(t *testing.T) {
	// The partition count comes from the production function over the fixture size,
	// which is what the driver groups by — never from a count read back off the run.
	fullBuckets := searchengine.BucketCountFor(watermarkCorpusN)
	require.Greater(t, fullBuckets, 1, "the fixture must span more than one partition or 'every bucket' proves nothing")

	t.Run("no persisted record scans from zero", func(t *testing.T) {
		scanner := newWatermarkFixture(0)
		shipper := &fakeRebuildShipper{} // no record: watermark zero

		out, err := RebuildSegments(
			context.Background(), scanner, shipper, kgtypes.GraphCode, "fresh-repo", false)
		require.NoError(t, err)
		require.True(t, out.Ran)

		require.NotEmpty(t, scanner.watermarks)
		assert.Zero(t, scanner.watermarks[0], "a daemon with no record must ask the server for the whole corpus")
		assert.Equal(t, watermarkCorpusN, out.Scanned)
		assert.Equal(t, fullBuckets, out.Built, "every partition is emitted, not just the ones a delta would touch")
	})

	t.Run("reset ignores a stored watermark", func(t *testing.T) {
		scanner := newWatermarkFixture(0)
		shipper := &fakeRebuildShipper{}
		shipper.watermark = 1_600_000_000_000_000_000
		shipper.tombstoned = []searchengine.ExternalID{"n-stale"}

		out, err := RebuildSegments(
			context.Background(), scanner, shipper, kgtypes.GraphCode, "reset-repo", true)
		require.NoError(t, err)
		require.True(t, out.Ran)

		require.NotEmpty(t, scanner.watermarks)
		assert.Zero(t, scanner.watermarks[0], "reset must put a ZERO on the wire even with a record on disk")
		assert.Equal(t, watermarkCorpusN, out.Scanned)
		assert.Equal(t, fullBuckets, out.Built)
		require.NotEmpty(t, shipper.seeded)
		assert.Empty(t, shipper.seeded[0], "reset drops the retained ids along with the watermark")
	})
}

// TestFullReScanPutsAZeroOnTheWire pins WHICH RUNS SCAN THE WHOLE CORPUS, on the one
// observable that still carries the answer: the watermark the driver puts on the wire.
//
// IT REPLACES TestFinalizeCarriesCorpusCompleteProvenance, and the replacement is a
// re-point rather than a deletion. That test asserted a corpus-complete BOOL the driver
// threaded to FinalizeRebuild, which the ship side used to decide whether to compare the
// built layer against the PRIOR MANIFEST's summed doc count. There is no prior manifest
// and no such comparison — FinalizeRebuild's signature carries no such parameter any
// more (deps_segments.go: FinalizeRebuild(ctx, gt, name) (RebuildFinalizeResult, error))
// — so the claim had no consumer left to inform. It was also silently unfalsifiable: the
// fake's recorder field survived as a legal, never-written slice, so the KNOWN-NEGATIVE
// half ("a delta run makes no claim") asserted that an always-empty slice was empty and
// could not have failed for any input.
//
// THE DECISION THE CLAIM REPORTED ON IS STILL REAL, which is why this is not a drop: a
// run either asks the server for the whole corpus or for a bounded window, and getting
// that wrong is a silent full-corpus read or a silently partial rebuild.
//
// THE THREE CASES ARE THE THREE WAYS THE DRIVER DECIDES. A reset scans from zero by
// operator instruction; a graph with no record scans from zero because it has no window;
// and a watermark-scoped run whose delta path cannot apply RE-SCANS from zero, which
// makes the items in hand the corpus even though the run did not start that way. The
// last is the one a reader would guess wrong, it is the one an implementation that
// decided once at entry would get wrong too, and it is the only one of the three that
// TestZeroWatermarkRebuildsFullCorpus does not already cover.
func TestFullReScanPutsAZeroOnTheWire(t *testing.T) {
	const priorWatermark = int64(1_600_000_000_000_000_000)

	// The fallback case scans TWICE — the watermark-scoped window, then the full
	// re-scan from zero — so its scanner has to serve the corpus page a second time.
	// The nil page between them is what terminates the first scan's paging.
	rescannable := func() *fakeWatermarkScanner {
		page := makeScanPage("n-", 0, watermarkCorpusN)
		return &fakeWatermarkScanner{pages: [][]*knowledgev1.PipelineScanItem{page, nil, page}}
	}

	cases := []struct {
		name    string
		reset   bool
		scanner func() *fakeWatermarkScanner
		prepare func(s *fakeRebuildShipper)
		// scoped marks the run that STARTS at the stored watermark and only then falls
		// back, which is what separates it from the two that scan from zero at entry.
		scoped bool
	}{
		{name: "an operator reset", reset: true,
			scanner: func() *fakeWatermarkScanner { return newWatermarkFixture(0) },
			prepare: func(s *fakeRebuildShipper) { s.watermark = priorWatermark }},
		{name: "a graph with no record", reset: false,
			scanner: func() *fakeWatermarkScanner { return newWatermarkFixture(0) },
			prepare: func(*fakeRebuildShipper) {}},
		{name: "a delta that fell back to a full re-scan", reset: false, scoped: true,
			scanner: rescannable,
			prepare: func(s *fakeRebuildShipper) {
				s.watermark = priorWatermark
				s.deltaNotApplicable = true
			}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			scanner := tc.scanner()
			shipper := &fakeRebuildShipper{}
			tc.prepare(shipper)

			out, err := RebuildSegments(
				context.Background(), scanner, shipper, kgtypes.GraphCode, "provenance-repo", tc.reset)
			require.NoError(t, err)
			require.True(t, out.Ran)

			require.NotEmpty(t, scanner.watermarks, "the run must have asked the server for something")
			last := scanner.watermarks[len(scanner.watermarks)-1]
			assert.Zero(t, last,
				"this run's scan covered the whole corpus, so the LAST thing it put on the wire is a "+
					"zero watermark — a non-zero here means it built a full layer out of a bounded window")

			if tc.scoped {
				// The fallback case must be shown to have STARTED scoped, or it is
				// indistinguishable from the two cases that scan from zero at entry —
				// and an implementation deciding once at entry would pass it anyway.
				assert.Equal(t, priorWatermark, scanner.watermarks[0],
					"the run must have opened its scan at the stored watermark before falling back")
			} else {
				assert.Zero(t, scanner.watermarks[0],
					"this run scans from zero at ENTRY, not after a fallback")
			}
		})
	}

	// THE KNOWN-NEGATIVE: a run that takes the delta path asks for a BOUNDED window and
	// never re-scans. Without it every assertion above would be satisfied by a driver
	// that hard-coded a zero on the wire, and the watermark would carry no information.
	t.Run("a delta run asks for a bounded window and never re-scans", func(t *testing.T) {
		scanner := newWatermarkFixture(0)
		shipper := &fakeRebuildShipper{}
		shipper.watermark = priorWatermark

		out, err := RebuildSegments(
			context.Background(), scanner, shipper, kgtypes.GraphCode, "delta-provenance-repo", false)
		require.NoError(t, err)
		require.True(t, out.Ran)
		require.Positive(t, shipper.deltaCalls.Load(), "this run must take the DELTA path or the case is not what it says")
		require.NotEmpty(t, scanner.watermarks)
		for i, w := range scanner.watermarks {
			assert.Equal(t, priorWatermark, w,
				"scan %d asked from the stored watermark: a delta run re-emits against what the engines "+
					"already hold and must never widen to the whole corpus", i)
		}
	})
}
