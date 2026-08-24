// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
	"github.com/fulminate-io/knowledge-mcp/internal/tools"
)

// deltaTombstoneScanner serves ONE page of tombstone-only scan items and then an
// empty page so the id-cursor drain terminates. Tombstone-only is the shape that
// matters here: the delta a delete-only collect produces carries erase instructions
// and no payload at all.
type deltaTombstoneScanner struct {
	mu      sync.Mutex
	ids     []string
	horizon int64
	served  bool
}

func (s *deltaTombstoneScanner) PipelineScan(_ context.Context, req *knowledgev1.PipelineScanRequest) (*knowledgev1.PipelineScanResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	resp := &knowledgev1.PipelineScanResponse{ServedHorizonNanos: s.horizon}
	if !s.served && req.GetAfterId() == "" {
		s.served = true
		for _, id := range s.ids {
			resp.Items = append(resp.Items, &knowledgev1.PipelineScanItem{NodeId: id, Tombstoned: true})
		}
	}
	return resp, nil
}

func (s *deltaTombstoneScanner) Execute(context.Context, *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	return &knowledgev1.ExecuteResponse{}, nil
}

// TestDeltaTombstoneMergesWithAccumulatedSet asserts a PREVIOUSLY-KNOWN tombstone
// is still seeded after a delta-fed one lands.
//
// WHAT IT CATCHES, and nothing else on this step can. SetGraphTombstones REPLACES
// the graph's set, and an EMPTY set DELETES the entry outright. A delta-scoped
// consumer naturally holds only the ids in the current window, so handing that
// straight over ERASES everything learned earlier and re-opens the
// import-resurrection window the tombstone seeding exists to close. The propagation
// gate stays perfectly GREEN against that implementation — it only asks whether the
// NEWLY deleted id lost its rank slot, which a replace-shaped consumer does
// correctly. Only the first victim below can tell the two apart.
//
// IT ASSERTS AT THE LOAD PATH, not on the tombstone accessor. The set exists to seed
// IMPORTS; a consumer could hold both ids and still fail to seed, so the observable
// is a cold engine importing a blob that PREDATES both deletes and neither victim
// coming back from it.
func TestDeltaTombstoneMergesWithAccumulatedSet(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	gt, name := kgtypes.GraphCode, "mergeRepo"
	const corpusN = 200
	// A watermark a prior landed rebuild left behind. The consumer must not move it:
	// only a landed publish may, and this pass publishes nothing.
	const priorWatermark = int64(1_600_000_000_000_000_000)
	const deltaHorizon = int64(1_700_000_000_123_456_789)

	dir := t.TempDir()
	_, gc := newSegmentHarness(t)

	// The blob on the server and in L2 predates BOTH deletes and contains every
	// victim — the precondition the whole test rests on, since nothing rewrites it.
	producer := closeOnCleanup(t, NewManager(loginStateStub{loggedIn: true}, dir, 0, withSegmentSource(gc)))
	docs := prefixIDs(hnswVecDocs(corpusN), "merge-")
	require.NoError(t, producer.AddAndMarkDirty(ctx, gt, name, docs))
	require.NoError(t, producer.ReEmitDirtyBuckets(ctx, gt, name))

	known, fed, survivor := docs[0], docs[1], docs[2]

	// The accumulated state an earlier pass left: the durable record carries `known`
	// and the engines are seeded from it.
	consumer := closeOnCleanup(t, NewManager(loginStateStub{loggedIn: true}, dir, 0, withSegmentSource(gc)))
	require.NoError(t, consumer.SaveRebuildState(gt, name, priorWatermark, []searchengine.ExternalID{known.ID}))
	consumer.SetGraphTombstones(gt, name, []searchengine.ExternalID{known.ID})

	// The delta reports ONLY the second victim — exactly the window a delete-only
	// collect produces, and exactly what a replace-shaped consumer would hand over.
	scanner := &deltaTombstoneScanner{ids: []string{fed.ID}, horizon: deltaHorizon}
	out, err := tools.MergeSegmentDelta(ctx, scanner, toolsShipperAdapter{consumer}, consumer, consumer, gt, name, priorWatermark)
	require.NoError(t, err)
	require.Equal(t, 1, out.Learned, "exactly one id in this window was new")
	require.Equal(t, 2, out.Carried, "the set handed to the engines is the MERGE, not the delta")
	require.Equal(t, deltaHorizon, out.Horizon, "the consumer reports the served horizon so the caller can scope the next read")

	// The DURABLE record holds both, and the rebuild's watermark is untouched.
	w, ids, err := consumer.LoadRebuildState(gt, name)
	require.NoError(t, err)
	require.Equal(t, priorWatermark, w,
		"the delta consumer must NEVER advance the rebuild watermark — that value may move only when a publish LANDED, and this pass published nothing")
	require.ElementsMatch(t, []searchengine.ExternalID{known.ID, fed.ID}, ids,
		"the persisted set is the union; writing only the delta would lose the accumulated ids across a restart")

	// THE LOAD PATH. A cold import of the pre-delete blob must seed BOTH dead.
	dm := consumer.managerFor(gt, name)
	require.NoError(t, dm.load(ctx))
	require.Positive(t, dm.engine.ResidentDocCount(),
		"PRECONDITION: the consumer must actually have imported the pre-delete blob, or the assertions below prove nothing")

	require.False(t, searchReturnsID(dm, known),
		"THE DISCRIMINATOR: the PREVIOUSLY-KNOWN tombstone must still be seeded after a delta-fed one lands — a replace-shaped consumer loses exactly this id and passes every other gate on this step")
	require.False(t, searchReturnsID(dm, fed),
		"the delta-fed tombstone must be seeded too, or the consumer learned nothing")
	require.True(t, searchReturnsID(dm, survivor),
		"seeding kills exactly the tombstoned ids and nothing else — without this leg an implementation that seeded the whole corpus dead would pass")
}
