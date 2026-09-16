// SPDX-License-Identifier: Apache-2.0

// manager_resident_destination_test.go — the per-format resident SEGMENT readings
// over the {storage,account} DESTINATION CHILDREN, which is where every serving
// engine actually lives.
//
// WHY THIS FILE EXISTS. The readings the status column renders were taken off the
// ROOT Manager's own arm maps, and a user call never seals into those: it binds a
// destination and lands on a child (storage.go bindChild), so a keyless client
// sealed 42 bm25v2 segments into the {local} child while the field read null — at
// one segment, at forty-two, and after a drain. Installing one binary vector then
// flipped the cell to an EMPTY root hnswv3 arm that the status read had just
// constructed for itself, which is the same defect from the other side: a reading
// of a pool nobody writes to, and a pool created by the act of reading.
//
// THE ROWS BELOW ARE THEREFORE ABOUT WHICH ENGINE IS OBSERVED, not about the
// bound. They write through the production entry points onto real children and
// read back through the exported status surface, and the last of them watches a
// real consolidation move the number the operator sees.

package segmentdist

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
)

// residentDestAccount is the account a header-bound cloud destination names. One
// value for every case that needs one, named here rather than passed in.
const residentDestAccount = "22222222-2222-4222-8222-222222222222"

// localDestination and cloudDestination are the two destinations these rows bind:
// the {local} child every logged-out write lands on, and the account-bound cloud
// child an inbound Knowledge-Account-Id selects.
func localDestination(t *testing.T) context.Context {
	t.Helper()
	return graphclient.WithDestination(t.Context(), graphclient.Destination{Storage: "local"})
}

func cloudDestination(t *testing.T) context.Context {
	t.Helper()
	return graphclient.WithDestination(t.Context(),
		graphclient.Destination{Storage: "cloud", AccountID: residentDestAccount})
}

// rootArms reports whether this Manager's OWN arm maps hold an entry for one
// graph. It is the observable that says whether a READ constructed an engine
// here: the per-format accessors construct lazily, so a status read that reaches
// one leaves a pool behind and then reports it.
func rootArms(m *Manager, gt kgtypes.GraphType, name string) (hnswArm, bm25Arm bool) {
	k := graphKey{graphType: gt, graphName: name}
	m.mu.Lock()
	defer m.mu.Unlock()
	_, hnswArm = m.managers[k]
	_, bm25Arm = m.bm25Managers[k]
	return hnswArm, bm25Arm
}

// TestResidentSegmentReadings_KeylessLocalChildIsReported is the KEYLESS arm: no
// credential, therefore no vectors and no vector engine, and every sealed segment
// in the {local} child's bm25v2 pool.
func TestResidentSegmentReadings_KeylessLocalChildIsReported(t *testing.T) {
	t.Parallel()
	const name = "dest-keyless"
	root := closeOnCleanup(t, NewManager(t.TempDir(), 0))
	ctx := localDestination(t)

	for i := range 3 {
		require.NoError(t, root.AddAndMarkDirtyFields(ctx, boundGraphType, name, boundBatchDocuments(i)))
	}

	// THE EXPECTATION IS DERIVED FROM THE CHILD ITSELF, never a literal: how many
	// segments a batch seals is a property of the partition count the corpus
	// derives, and a pinned number would red at every unrelated change to it.
	child := root.ForDestination(ctx)
	want := child.bm25ManagerFor(boundGraphType, name).residentSegmentCount()
	require.Positive(t, want,
		"PRECONDITION: the {local} child must actually hold sealed segments, or every assertion below compares two absences")
	hnswArm, bm25Arm := rootArms(root, boundGraphType, name)
	require.False(t, hnswArm, "PRECONDITION: the ROOT holds no vector engine for this graph — the writes went to the child")
	require.False(t, bm25Arm, "PRECONDITION: and no field engine either, which is exactly why a root-only reader reports nothing")

	counts := root.ResidentSegmentCounts(boundGraphType, name)
	require.NotNil(t, counts,
		"the status reader must report the engine that actually SERVES; a keyless client's segments live in the "+
			"{local} destination child, and a reading taken off the root alone is null however many it holds")
	assert.Equal(t, want, counts[bm25.New().Name()], "and the number must be the child's own count")
	assert.NotContains(t, counts, hnsw.New().Name(),
		"no vector engine was constructed anywhere for this graph, and an absent engine is not an empty one")
	assert.Equal(t, map[string]int{bm25.New().Name(): 1}, root.ResidentSegmentDestinations(boundGraphType, name),
		"one destination contributed the reading, which is what keeps the text render from naming a count it did not aggregate")
}

// TestResidentSegmentReadings_KeyedChildReportsBothFormats is the KEYED arm, and
// the anti-construction row: the status path reads the live resident doc count
// before it reads these, and that read used to CREATE the root's vector arm —
// which the next line then reported as an engine holding zero.
func TestResidentSegmentReadings_KeyedChildReportsBothFormats(t *testing.T) {
	t.Parallel()
	const name = "dest-keyed"
	root := closeOnCleanup(t, NewManager(t.TempDir(), 0))
	ctx := cloudDestination(t)

	docs := boundBatchDocuments(0)
	require.NoError(t, root.AddAndMarkDirty(ctx, boundGraphType, name, docs))
	require.NoError(t, root.AddAndMarkDirtyFields(ctx, boundGraphType, name, docs))

	child := root.ForDestination(ctx)
	wantHNSW := child.managerFor(boundGraphType, name).residentSegmentCount()
	wantBM25 := child.bm25ManagerFor(boundGraphType, name).residentSegmentCount()
	require.Positive(t, wantHNSW, "PRECONDITION: the account-bound child's vector engine holds segments")
	require.Positive(t, wantBM25, "PRECONDITION: and so does its field engine, or the per-format keys below are untested")

	// THE STATUS PATH'S OWN CALLS, in the order manage(status) makes them:
	// collectSegProbes reads the live resident doc count in the same statement
	// group that reads the per-format counts, and reads it FIRST.
	// It is ASSERTED rather than discarded: its destination semantics are the ones
	// the cell renders beside these counts.
	wantLive := child.managerFor(boundGraphType, name).engine.LiveResidentCount()
	require.Positive(t, wantLive, "PRECONDITION: the child's vector engine holds live documents")
	assert.Equal(t, wantLive, root.LiveResidentDocCountFor(ctx, boundGraphType, name),
		"the cell's live reading must resolve the SAME destination its shipped reading does; a root-scoped one beside "+
			"a destination-resolved shipped figure renders `shipped N · live 0`, which is the collapse signal itself")
	assert.Zero(t, root.LiveResidentDocCount(boundGraphType, name),
		"while the root-scoped reader — kept for the code-search gate that still reads it — answers 0 here, and the "+
			"assertions below say it built nothing to answer with")

	counts := root.ResidentSegmentCounts(boundGraphType, name)

	assert.Equal(t, map[string]int{hnsw.New().Name(): wantHNSW, bm25.New().Name(): wantBM25}, counts,
		"both of the child's engines are reported, keyed by format: the budget is per format per graph")
	hnswArm, bm25Arm := rootArms(root, boundGraphType, name)
	assert.False(t, hnswArm,
		"a status READ must construct nothing: the root's vector arm was empty before it and must be absent after it, "+
			"or the read materializes a pool for an instance that does not exist and then reports it as an engine holding zero")
	assert.False(t, bm25Arm, "and the same for the field arm")
}

// TestResidentSegmentReadings_TwoDestinationsReportTheMaximum is the multi-
// destination row: one daemon holds a child per bound account, and the fan-out
// budget is per ENGINE.
func TestResidentSegmentReadings_TwoDestinationsReportTheMaximum(t *testing.T) {
	t.Parallel()
	const name = "dest-two"
	root := closeOnCleanup(t, NewManager(t.TempDir(), 0))
	format := bm25.New().Name()

	local, cloud := localDestination(t), cloudDestination(t)
	require.NoError(t, root.AddAndMarkDirtyFields(local, boundGraphType, name, boundBatchDocuments(0)))
	for i := range 4 {
		require.NoError(t, root.AddAndMarkDirtyFields(cloud, boundGraphType, name, boundBatchDocuments(i)))
	}

	smallArm := root.ForDestination(local).bm25ManagerFor(boundGraphType, name)
	largeArm := root.ForDestination(cloud).bm25ManagerFor(boundGraphType, name)
	require.Greater(t, largeArm.residentSegmentCount(), smallArm.residentSegmentCount(),
		"PRECONDITION: the two destinations must hold DIFFERENT counts, or a maximum and a minimum agree and this row proves neither")

	assert.Equal(t, largeArm.residentSegmentCount(), root.ResidentSegmentCounts(boundGraphType, name)[format],
		"per format the reading is the MAXIMUM across destinations, never the sum: a search of one graph on one "+
			"destination fans out over THAT engine's resident segments, so the maximum is the number the budget bounds")
	assert.Equal(t, max(largeArm.residentSegmentPeak(), smallArm.residentSegmentPeak()),
		root.ResidentSegmentPeaks(boundGraphType, name)[format],
		"and the high-water is aggregated the same way, or the excursion the current count cannot show is lost at the seam")
	assert.Equal(t, 2, root.ResidentSegmentDestinations(boundGraphType, name)[format],
		"two destinations contributed, which the text render names so an aggregate is never read as one engine")
}

// TestResidentSegmentReadings_FollowABoundedChild is the resident-growth bound's
// OBSERVABLE made live: the bound acts inside a write on the child's engine, and
// this is the row that says an operator can watch it act.
func TestResidentSegmentReadings_FollowABoundedChild(t *testing.T) {
	t.Parallel()
	const name = "dest-bound"
	root := closeOnCleanup(t, NewManager(t.TempDir(), 0))
	ctx := localDestination(t)
	format := bm25.New().Name()
	budget := searchengine.ResidentSegmentFanoutBudget

	highest, previous := 0, -1
	fell := false
	for i := range boundBatches {
		require.NoError(t, root.AddAndMarkDirtyFields(ctx, boundGraphType, name, boundBatchDocuments(i)))
		counts := root.ResidentSegmentCounts(boundGraphType, name)
		require.Contains(t, counts, format,
			"batch %d: the serving child's engine must be readable at EVERY reading, not only once it is interesting", i)
		n := counts[format]
		require.LessOrEqual(t, n, budget,
			"batch %d: the status reader observed %d resident segments against a budget of %d", i, n, budget)
		if previous >= 0 && n < previous {
			fell = true
		}
		highest = max(highest, n)
		previous = n
	}

	require.Greater(t, highest, budget/2,
		"ANTI-VACUITY: the fixture must have grown the child's resident set far enough for the bound to act (highest %d of %d)",
		highest, budget)
	require.True(t, fell,
		"a consolidation inside the destination child must be VISIBLE through the status reader: that observable is the "+
			"whole reason the per-format count is on the wire, and it is what the release smoke reads")
}
