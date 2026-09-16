// SPDX-License-Identifier: Apache-2.0

// manage_status_coverage_resident_real_test.go — the per-format resident SEGMENT
// count read by the REAL tools reader from a REAL segment Manager.
//
// THE DOUBLED PART IS NAMED SO IT IS NOT MISTAKEN FOR THE SUBJECT. The row's seam is
// SegmentCoverageReader, six methods wide, and only ONE of them is under test here;
// the other five are the existing stub's, because the production adapter that
// supplies them lives in the composition root and cannot be imported from here
// (bootstrap imports tools). ResidentSegmentCounts is NOT stubbed: it is the
// production *segmentdist.Manager's own method, over a Manager that really sealed
// segments through the real write entry points. The adapter's pass-through between
// the two is pinned by a compile-time proof beside the adapter itself, and its
// behaviour by a bootstrap-side row with both sides real.
//
// The precedent for a tools test importing the segment package is
// intercept_search_offline_test.go; nothing in tools' production code imports it, so
// there is no cycle.

package tools

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/bm25"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/hnsw"
	"github.com/fulminate-io/knowledge-mcp/internal/segmentdist"
)

// realManagerSegReader answers the resident-segment count from a REAL Manager and
// everything else from the package's existing stub.
type realManagerSegReader struct {
	*coverageSegReader
	mgr *segmentdist.Manager
}

func (r realManagerSegReader) ResidentSegmentReadings(
	gt kgtypes.GraphType, name string,
) (counts, peaks, destinations map[string]int) {
	return r.mgr.ResidentSegmentReadings(gt, name)
}

// realManagerStatusSegReader answers the SHIPPED and LIVE doc counts from the real
// Manager too, which the rows below need and the row above must not have: the
// production assembly reads both through this same seam immediately before the
// resident-segment readings, the live one used to CONSTRUCT the root's vector arm
// for those readings to find, and the two of them are the pair whose destination
// semantics have to agree.
type realManagerStatusSegReader struct{ realManagerSegReader }

func (r realManagerStatusSegReader) LiveResidentDocCount(gt kgtypes.GraphType, name string) int {
	return r.mgr.LiveResidentDocCount(gt, name)
}

func (r realManagerStatusSegReader) LiveResidentDocCountFor(
	ctx context.Context, gt kgtypes.GraphType, name string,
) int {
	return r.mgr.LiveResidentDocCountFor(ctx, gt, name)
}

// realManagerPairSegReader answers the OTHER half of the cell's doc-count pair from
// the real Manager as well, which is what makes the destination semantics of the two
// halves comparable at all: ShippedSegmentDocCount resolves the bound destination
// inside the Manager, so a stubbed one cannot show whether the live reading beside it
// resolves the same one.
type realManagerPairSegReader struct{ realManagerStatusSegReader }

func (r realManagerPairSegReader) ShippedSegmentDocCount(
	ctx context.Context, gt kgtypes.GraphType, name string,
) (int, error) {
	return r.mgr.ShippedSegmentDocCount(ctx, gt, name)
}

// TestCoverageRows_ResidentSegmentsFromARealManager drives collectCoverageRows — the
// production reader, unchanged — against a Manager that really holds segments.
func TestCoverageRows_ResidentSegmentsFromARealManager(t *testing.T) {
	ctx := context.Background()
	mgr := segmentdist.NewManager(t.TempDir(), 0)
	t.Cleanup(mgr.Close)

	// branchFixture's base row for code/agent is the row this asserts on, so the
	// Manager is written under the SAME instance key the coverage walk enumerates.
	gt, name := kgtypes.GraphCode, "agent"
	docs := []searchengine.Document{{
		ID:     "real-manager-0",
		Vector: make([]byte, 32),
		Fields: map[string]string{searchengine.FieldContent: "alpha beta"},
	}}
	require.NoError(t, mgr.AddAndMarkDirty(ctx, gt, name, docs))
	require.NoError(t, mgr.AddAndMarkDirtyFields(ctx, gt, name, docs))
	want := mgr.ResidentSegmentCounts(gt, name)
	require.Positive(t, want[bm25.New().Name()],
		"PRECONDITION: the Manager must actually hold segments, or every assertion below compares two absences")

	seg := realManagerSegReader{
		coverageSegReader: &coverageSegReader{
			coveredByKey: map[string]int{"code/agent": 1},
			liveByKey:    map[string]int{"code/agent": 1},
		},
		mgr: mgr,
	}
	rows, err := collectCoverageRows(ctx, &coverageDeps{gc: branchFixture(), segCov: seg})
	require.NoError(t, err)

	byGraph := make(map[string]CoverageRow, len(rows))
	for _, r := range rows {
		byGraph[r.Graph] = r
	}
	row, ok := byGraph["code/agent"]
	require.True(t, ok)
	assert.Equal(t, want, row.ResidentSegments,
		"the row must carry what the real Manager holds, keyed by format")
	assert.Equal(t, mgr.ResidentSegmentPeaks(gt, name), row.ResidentSegmentsPeak,
		"and the seal-path high-water the real Manager holds, on the same terms")
	assert.Contains(t, formatCoverageRow(row), "· segments ",
		"and the rendered cell must name them, because that cell is what the release smoke reads")
}

// TestCoverageRows_ResidentSegmentsFromADestinationChild is the wiring every
// shipped client actually runs: a write BINDS a destination, so the engine that
// serves this graph belongs to the {local} child and the root Manager the reader is
// handed holds no engine for it at all.
//
// IT IS THE RC's OWN CELL. A keyless client sealed its segments into that child and
// the field read null; a client with one embedded node read `{"hnswv3": 0}`,
// because the live-count probe in the same statement group had just built an empty
// vector arm on the root for this reader to find. Both readings are asserted here.
func TestCoverageRows_ResidentSegmentsFromADestinationChild(t *testing.T) {
	bound := graphclient.WithDestination(context.Background(), graphclient.Destination{Storage: "local"})
	mgr := segmentdist.NewManager(t.TempDir(), 0)
	t.Cleanup(mgr.Close)

	gt, name := kgtypes.GraphCode, "agent"
	// NO VECTOR ON THE DOCUMENT: the keyless arm, which has no embed credential and
	// therefore no vector engine anywhere — so a reported hnswv3 key could only be an
	// arm the status read built for itself.
	docs := []searchengine.Document{{
		ID:     "destination-child-0",
		Fields: map[string]string{searchengine.FieldContent: "alpha beta"},
	}}
	require.NoError(t, mgr.AddAndMarkDirtyFields(bound, gt, name, docs))
	want := mgr.ResidentSegmentCounts(gt, name)
	require.Positive(t, want[bm25.New().Name()],
		"PRECONDITION: the destination child must hold sealed segments, or every assertion below compares two absences")

	seg := realManagerStatusSegReader{realManagerSegReader{
		coverageSegReader: &coverageSegReader{
			coveredByKey: map[string]int{"code/agent": 1},
			liveByKey:    map[string]int{"code/agent": 1},
		},
		mgr: mgr,
	}}
	// THE COLLECT CALL CARRIES NO DESTINATION, exactly as manage(status) does: the
	// reader observes the destinations that exist rather than resolving one.
	rows, err := collectCoverageRows(context.Background(), &coverageDeps{gc: branchFixture(), segCov: seg})
	require.NoError(t, err)

	byGraph := make(map[string]CoverageRow, len(rows))
	for _, r := range rows {
		byGraph[r.Graph] = r
	}
	row, ok := byGraph["code/agent"]
	require.True(t, ok)
	assert.Equal(t, want, row.ResidentSegments,
		"the row must carry what the SERVING child holds; a reading taken off the root alone is null on every keyless client")
	assert.NotContains(t, row.ResidentSegments, hnsw.New().Name(),
		"and no vector engine exists on any destination, so a key for one could only be an arm the status read constructed")
	assert.Equal(t, map[string]int{bm25.New().Name(): 1}, row.ResidentSegmentDestinations,
		"one destination contributed it, which is why the cell below names no destination count")
	assert.Contains(t, formatCoverageRow(row), "· segments "+bm25.New().Name()+" ",
		"and the rendered cell names the format beside the count, because that cell is what the release smoke reads")
}

// TestCoverageRows_ShippedAndLiveComeFromTheSameDestination is the pair beside those
// segment terms, on the wiring every user call runs: a bound destination.
//
// THE CELL DOCUMENTS `live 0 while shipped is N` AS A POOL COLLAPSE. shipped resolves
// the destination inside the Manager and answers off the child; a root-scoped live
// reading beside it therefore fired that alarm permanently on every
// destination-bound client, which is every client, while the segment terms in the
// same cell reported the child correctly. This row drives both readers real, through
// a bound ctx, and asserts the alarm does NOT fire on a healthy child.
func TestCoverageRows_ShippedAndLiveComeFromTheSameDestination(t *testing.T) {
	bound := graphclient.WithDestination(context.Background(), graphclient.Destination{Storage: "local"})
	mgr := segmentdist.NewManager(t.TempDir(), 0)
	t.Cleanup(mgr.Close)

	gt, name := kgtypes.GraphCode, "agent"
	// THE KEYED SHAPE: a vector, so the cell takes its vector branch — the branch
	// whose two readers disagreed about which destination they were reading.
	docs := []searchengine.Document{{
		ID:     "same-destination-0",
		Vector: make([]byte, 32),
		Fields: map[string]string{searchengine.FieldContent: "alpha beta"},
	}}
	require.NoError(t, mgr.AddAndMarkDirty(bound, gt, name, docs))
	wantLive := mgr.LiveResidentDocCountFor(bound, gt, name)
	require.Positive(t, wantLive,
		"PRECONDITION: the bound child's vector engine must hold live documents, or 'no collapse' is vacuous")

	seg := realManagerPairSegReader{realManagerStatusSegReader{realManagerSegReader{
		coverageSegReader: &coverageSegReader{},
		mgr:               mgr,
	}}}
	// THE COLLECT CARRIES THE BOUND ctx, as a served manage(status) does: every user
	// call binds a destination before it dispatches.
	rows, err := collectCoverageRows(bound, &coverageDeps{gc: branchFixture(), segCov: seg})
	require.NoError(t, err)

	byGraph := make(map[string]CoverageRow, len(rows))
	for _, r := range rows {
		byGraph[r.Graph] = r
	}
	row, ok := byGraph["code/agent"]
	require.True(t, ok)

	assert.Equal(t, wantLive, row.LiveResident,
		"the live figure must come off the engine that serves this destination — the one its shipped figure came off")
	assert.Positive(t, row.SegCovered,
		"CONTROL: shipped is non-zero on the same row, so 'live 0' below would be the collapse signal rather than an empty graph")
	assert.NotContains(t, formatCoverageRow(row), "· live 0 ",
		"and the live-pool-collapse signal must NOT fire on a healthy bound child: that alarm is what a root-scoped "+
			"live reading beside a destination-resolved shipped reading produces on every ordinary client")
}
