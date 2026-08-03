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

	cursors    []string
	watermarks []int64
}

func (f *fakeWatermarkScanner) PipelineScan(_ context.Context, req *knowledgev1.PipelineScanRequest) (*knowledgev1.PipelineScanResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cursors = append(f.cursors, req.GetAfterId())
	f.watermarks = append(f.watermarks, req.GetAfterStampedAtNanos())
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
