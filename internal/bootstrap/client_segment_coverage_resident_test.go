// SPDX-License-Identifier: Apache-2.0

// client_segment_coverage_resident_test.go — the per-format resident SEGMENT count
// crossing from the segment package into the tools carrier, with BOTH SIDES REAL.
//
// WHY THIS ROW EXISTS. The tools layer reads this count through an OPTIONAL seam it
// resolves by a RUNTIME type assertion on an unexported interface, so nothing about
// the adapter's shape is checked by the compiler at the consumer. A drift there does
// not fail: it makes the assertion miss, and every coverage row then reports no
// resident segment count forever — which is also the row's honest rendering for a
// reader that cannot look, so the permanently-taken decline reads exactly like a
// working one.
//
// TWO THINGS CLOSE THAT, AND THIS FILE IS THE SECOND. The first is the compile-time
// proof beside the adapter (client_segment_coverage.go), which turns a shape drift
// into a build error. This is the behavioral half: a REAL Manager that really sealed
// segments, reached through the PRODUCTION accessor c.SegmentCoverage(), resolved
// through the SAME interface shape the tools seam asserts for, answering the counts
// the Manager itself holds.

package bootstrap

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/bm25"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/hnsw"
	"github.com/fulminate-io/knowledge-mcp/internal/segmentdist"
)

// residentSegmentCountsSeam is the SHAPE the tools layer's optional seam asserts
// for, written out here rather than imported because tools keeps it unexported. It
// is the same shape the compile-time proof beside the adapter pins, so the two
// cannot drift apart without one of them failing.
type residentSegmentCountsSeam interface {
	ResidentSegmentReadings(gt kgtypes.GraphType, name string) (counts, peaks, destinations map[string]int)
}

// liveResidentDestinationSeam is the tools layer's OTHER runtime-asserted shape on
// this adapter: the live resident doc count resolved for the BOUND DESTINATION,
// which the status cell reads so its live figure comes off the engine its shipped
// figure came off. A miss there does not fail either — it declines to the
// root-scoped reading, which renders a plausible number — so the shape is written
// out and asserted here like its neighbor.
type liveResidentDestinationSeam interface {
	LiveResidentDocCountFor(ctx context.Context, gt kgtypes.GraphType, name string) int
}

func TestSegmentCoverageAdapterCarriesResidentSegmentCounts(t *testing.T) {
	ctx := context.Background()
	gt, name := kgtypes.GraphCode, "adapter-resident"

	mgr := segmentdist.NewManager(t.TempDir(), 0)
	t.Cleanup(mgr.Close)
	c := &client{segmentMgr: mgr}

	docs := []searchengine.Document{{
		ID:     "adapter-resident-0",
		Vector: make([]byte, 32),
		Fields: map[string]string{searchengine.FieldContent: "alpha beta"},
	}}
	// BOTH ENGINES, through the real write entry points: the count is per format, so
	// a fixture exercising one arm would leave the other's key untested.
	require.NoError(t, mgr.AddAndMarkDirty(ctx, gt, name, docs))
	require.NoError(t, mgr.AddAndMarkDirtyFields(ctx, gt, name, docs))

	reader := c.SegmentCoverage()
	require.NotNil(t, reader, "PRECONDITION: a wired segment manager must produce a coverage reader")

	seam, resolved := reader.(residentSegmentCountsSeam)
	require.True(t, resolved,
		"the tools layer resolves this count by a runtime type assertion on exactly this shape; "+
			"an adapter that does not satisfy it makes every coverage row report no count forever")

	got, peaks, destinations := seam.ResidentSegmentReadings(gt, name)
	require.Equal(t, mgr.ResidentSegmentCounts(gt, name), got,
		"the adapter is a pass-through: what it answers must be what the Manager holds")
	require.Positive(t, got[bm25.New().Name()], "the field engine sealed a segment for this graph")
	require.Positive(t, got[hnsw.New().Name()], "and so did the vector engine")

	require.Equal(t, mgr.ResidentSegmentPeaks(gt, name), peaks,
		"and the seal-path high-water crosses the same seam IN THE SAME CALL: it is the reading the current count "+
			"cannot give, so an adapter carrying only one of the pair hides every excursion from the operator and the smoke")

	require.Equal(t, mgr.ResidentSegmentDestinations(gt, name), destinations,
		"and so does the count of destinations the pair was folded from, without which the cell states a maximum "+
			"over several engines as a reading of one")
}

// TestSegmentCoverageAdapterReportsADestinationChild is the wiring the RC
// confirmation actually runs on, with both sides real: a write BINDS a destination,
// so the engine that serves is the {local} child's and the adapter is handed the
// ROOT. A reader of the root's own arm maps reports nothing here however many
// segments the client holds — the defect this ticket corrects — and the vector
// reader beside it must not paper over that by constructing an empty arm.
func TestSegmentCoverageAdapterReportsADestinationChild(t *testing.T) {
	gt, name := kgtypes.GraphCode, "adapter-resident-child"
	ctx := graphclient.WithDestination(context.Background(), graphclient.Destination{Storage: "local"})

	mgr := segmentdist.NewManager(t.TempDir(), 0)
	t.Cleanup(mgr.Close)
	c := &client{segmentMgr: mgr}

	// THE KEYLESS SHAPE: content and no vector, which is what a client with no embed
	// credential writes, and the arm the resident bound was named for.
	docs := []searchengine.Document{{
		ID:     "adapter-resident-child-0",
		Fields: map[string]string{searchengine.FieldContent: "alpha beta"},
	}}
	require.NoError(t, mgr.AddAndMarkDirtyFields(ctx, gt, name, docs))

	reader := c.SegmentCoverage()
	require.NotNil(t, reader, "PRECONDITION: a wired segment manager must produce a coverage reader")
	seam, resolved := reader.(residentSegmentCountsSeam)
	require.True(t, resolved, "PRECONDITION: the adapter must satisfy the shape the tools layer asserts for")

	// The status assembly reads the live resident doc count FIRST, in the same
	// statement group — so it is read here, in that order, before the counts, and
	// through the DESTINATION-RESOLVED reader the cell uses.
	live, resolvedLive := reader.(liveResidentDestinationSeam)
	require.True(t, resolvedLive,
		"the tools layer resolves the live reading by a runtime assertion on this shape too; an adapter that misses "+
			"it declines to the root-scoped reading, which renders a number rather than an error")
	require.Zero(t, live.LiveResidentDocCountFor(ctx, gt, name),
		"no vector engine exists for this graph on this destination, so the live count is zero")

	got, _, destinations := seam.ResidentSegmentReadings(gt, name)
	require.Positive(t, got[bm25.New().Name()],
		"the segments the client sealed live on the {local} destination child, and the status reader must see them")
	require.NotContains(t, got, hnsw.New().Name(),
		"and the live-count read above must have CONSTRUCTED no vector arm to be reported here as an engine holding zero")
	require.Equal(t, map[string]int{bm25.New().Name(): 1}, destinations,
		"one destination held the engine, which is why the rendered cell names no destination count")
}
