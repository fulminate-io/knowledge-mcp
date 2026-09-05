// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// wire_tombstone_test.go is the catcher for a regression this package had no
// test for, which is why a consolidation was able to introduce it while the
// whole suite stayed green: the bulk by-ids hydrate started asking the server
// for tombstoned rows, and deleted thoughts reached recall results.
//
// A GREEN SUITE WAS NOT EVIDENCE OF CORRECTNESS HERE. Nothing in this package
// asserted tombstone exclusion, so nothing could go red when it stopped
// happening. This test exists so the next change to the hydrate has to decide
// about deleted nodes on purpose.

// tombstoneGateFake models the ONE server behaviour under test: a by-ids read
// returns tombstoned rows only when the plan asks for them. The server's own
// executor states the rule as "ids that are missing or tombstoned (without
// IncludeTombstones) are skipped", and this fake is that sentence and nothing
// else.
//
// It records the flag it saw so a test can distinguish "the client asked for
// tombstones and the fake honored it" from "the fake never ran".
type tombstoneGateFake struct {
	live      *knowledgev1.Node
	tombstone *knowledgev1.Node

	calls        int
	sawIncludeTS bool
}

func (f *tombstoneGateFake) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	f.calls++
	q := req.GetQuery()
	f.sawIncludeTS = q.GetIncludeTombstones()
	want := map[string]bool{}
	for _, id := range q.GetIds() {
		want[id] = true
	}
	resp := &knowledgev1.ExecuteResponse{}
	if f.live != nil && want[f.live.Id] {
		resp.Nodes = append(resp.Nodes, f.live)
	}
	// THE GATE. The tombstoned row is served only under the flag.
	if f.tombstone != nil && want[f.tombstone.Id] && q.GetIncludeTombstones() {
		resp.Nodes = append(resp.Nodes, f.tombstone)
	}
	return resp, nil
}

// staticSearcher returns a fixed ranked hit list, standing in for the client
// segment engines so the recall path can be driven without one.
type staticSearcher struct{ ids []string }

func (s staticSearcher) Search(_ context.Context, _ kgtypes.GraphType, _, _ string, _ []byte, _ int) ([]searchengine.Hit, error) {
	hits := make([]searchengine.Hit, 0, len(s.ids))
	for i, id := range s.ids {
		hits = append(hits, searchengine.Hit{ID: id, Score: float64(len(s.ids) - i)})
	}
	return hits, nil
}

// TestFetchNodesByIDs_TombstonedNodeAbsentFromHydrateAndRecallCandidates asserts
// on BOTH surfaces, because either one alone is satisfiable by the wrong thing.
// The hydrate half can pass while some later change re-filters elsewhere; the
// recall half is what a caller actually receives, and it is the surface the
// regression reached.
//
// THE LIVE NODE IS CARRIED THROUGH BOTH HALVES UNDER FATAL GUARDS. A fake that
// returned nothing at all would satisfy every absence assertion here, so each
// half first REQUIRES the live node and stops the test if it is missing. The
// absence is then discriminating rather than merely different.
//
// The searcher ranks BOTH ids, so the deleted thought is in the candidate pool
// and is dropped by the hydrate gap at query.go's "tombstoned/deleted between
// rank and hydrate" branch. That branch is unreachable for tombstoned ids the
// moment the hydrate starts returning them, which is precisely the regression.
func TestFetchNodesByIDs_TombstonedNodeAbsentFromHydrateAndRecallCandidates(t *testing.T) {
	newFake := func() *tombstoneGateFake {
		return &tombstoneGateFake{
			live:      &knowledgev1.Node{Id: "live-1", Type: string(kgtypes.NodeThought), SymbolName: "a live thought"},
			tombstone: &knowledgev1.Node{Id: "deleted-1", Type: string(kgtypes.NodeThought), SymbolName: "a deleted thought"},
		}
	}

	t.Run("the bulk hydrate map", func(t *testing.T) {
		gc := newFake()

		got := fetchNodesByIDs(context.Background(), gc, []string{"live-1", "deleted-1"})

		require.Positive(t, gc.calls, "the hydrate must actually read; a zero here means this test proves nothing")
		require.Contains(t, got, "live-1", "KNOWN-POSITIVE: the live node must hydrate, or the absence below is vacuous")
		assert.False(t, gc.sawIncludeTS, "this package's hydrate must not ask the server for tombstoned rows")
		assert.NotContains(t, got, "deleted-1", "a deleted thought must not come back from the bulk hydrate")
		assert.Len(t, got, 1)
	})

	t.Run("the recall candidates a caller receives", func(t *testing.T) {
		gc := newFake()
		opts := RecallOptions{Searcher: staticSearcher{ids: []string{"live-1", "deleted-1"}}, Limit: 10}

		results, err := searchRecallCandidatesClient(context.Background(), gc, opts)
		require.NoError(t, err)

		ids := make([]string, 0, len(results))
		for _, r := range results {
			ids = append(ids, r.Node.Id)
		}
		require.Contains(t, ids, "live-1", "KNOWN-POSITIVE: the live thought must survive recall, or the absence below is vacuous")
		assert.Equal(t, []string{"live-1"}, ids, "recall must return the live thought and NOT the deleted one")
	})
}
