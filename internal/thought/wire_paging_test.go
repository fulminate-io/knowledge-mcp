// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// pagingFakeCaller is an Execute-only Caller that serves a type-browse drain by
// slicing a backing node slice on GetLimit()/GetOffset(), mirroring the server's
// applyNodePage (s[offset:] then cap at limit; offset>=len → nil → clean
// termination). It counts Execute calls so a test can assert the page count, and
// optionally front-inserts a node after page 1 to exercise the CreatedAt-desc
// concurrent-writer race the seen-set dedup defends against.
type pagingFakeCaller struct {
	nodes       []*knowledgev1.Node
	execCalls   atomic.Int64
	frontInsert *knowledgev1.Node // unshifted after the first page when non-nil
}

func (c *pagingFakeCaller) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	n := c.execCalls.Add(1)
	// Simulate a concurrent CreatedAt-desc front-insert AFTER page 1 was served
	// (at the start of the SECOND Execute, before page 2 is sliced): unshift one
	// node at index 0, shifting every subsequent offset window back by one so the
	// last row of page 1 re-appears as the first row of page 2.
	if c.frontInsert != nil && n == 2 {
		c.nodes = append([]*knowledgev1.Node{c.frontInsert}, c.nodes...)
	}
	q := req.GetQuery()
	if q == nil {
		return &knowledgev1.ExecuteResponse{}, nil
	}
	offset := int(q.GetOffset())
	limit := int(q.GetLimit())
	if offset >= len(c.nodes) {
		return &knowledgev1.ExecuteResponse{}, nil // offset past end → empty page.
	}
	end := offset + limit
	if limit <= 0 || end > len(c.nodes) {
		end = len(c.nodes)
	}
	return &knowledgev1.ExecuteResponse{Nodes: c.nodes[offset:end]}, nil
}

func makeThoughtNodes(n int) []*knowledgev1.Node {
	nodes := make([]*knowledgev1.Node, n)
	for i := range nodes {
		// Zero-padded IDs keep a deterministic order for assertions.
		nodes[i] = &knowledgev1.Node{Id: thoughtID(i), Type: string(kgtypes.NodeThought)}
	}
	return nodes
}

func thoughtID(i int) string {
	const digits = "0123456789"
	return "t" + string([]byte{digits[(i/100)%10], digits[(i/10)%10], digits[i%10]})
}

func uniqueIDs(t *testing.T, nodes []*knowledgev1.Node) {
	t.Helper()
	seen := map[string]int{}
	for _, n := range nodes {
		seen[n.Id]++
	}
	for id, count := range seen {
		require.Equalf(t, 1, count, "node.Id %s appeared %d times — drain must not duplicate", id, count)
	}
}

func TestDrainThoughtBrowse(t *testing.T) {
	ctx := context.Background()
	tt := string(kgtypes.NodeThought)

	// (1) 35 nodes, pageSize 10 → 4 pages (10+10+10+5), all returned, no dupes.
	t.Run("multi_page_no_dupes_no_drops", func(t *testing.T) {
		fc := &pagingFakeCaller{nodes: makeThoughtNodes(35)}
		got, err := drainThoughtBrowse(ctx, fc, tt, 10)
		require.NoError(t, err)
		require.Len(t, got, 35, "all 35 nodes drained")
		uniqueIDs(t, got)
		// Order preserved.
		for i := range got {
			assert.Equal(t, thoughtID(i), got[i].Id)
		}
		// 4 pages: the last (5<10) is the short terminating page — no extra empty read.
		assert.Equal(t, int64(4), fc.execCalls.Load(), "ceil(35/10)=4 page reads")
	})

	// (2) Exact multiple: 30 nodes, pageSize 10 → 3 full pages + 1 empty terminator.
	t.Run("exact_multiple_terminates_cleanly", func(t *testing.T) {
		fc := &pagingFakeCaller{nodes: makeThoughtNodes(30)}
		got, err := drainThoughtBrowse(ctx, fc, tt, 10)
		require.NoError(t, err)
		require.Len(t, got, 30)
		uniqueIDs(t, got)
		// 3 full pages (each len==pageSize, so the loop continues) + 1 empty page.
		assert.Equal(t, int64(4), fc.execCalls.Load(), "3 full pages + 1 terminating empty page")
	})

	// (3) Single short page: 4 nodes, pageSize 10 → 1 page.
	t.Run("single_short_page", func(t *testing.T) {
		fc := &pagingFakeCaller{nodes: makeThoughtNodes(4)}
		got, err := drainThoughtBrowse(ctx, fc, tt, 10)
		require.NoError(t, err)
		require.Len(t, got, 4)
		assert.Equal(t, int64(1), fc.execCalls.Load())
	})

	// (4) Empty corpus: 0 nodes → empty result, one RPC, no error.
	t.Run("empty_corpus", func(t *testing.T) {
		fc := &pagingFakeCaller{nodes: nil}
		got, err := drainThoughtBrowse(ctx, fc, tt, 10)
		require.NoError(t, err)
		assert.Empty(t, got)
		assert.Equal(t, int64(1), fc.execCalls.Load())
	})

	// (5) SEEN-SET DEDUP under a mid-drain front-insert: a concurrent writer
	// unshifts a node at index 0 after page 1 is served, shifting page 2's window
	// back one so the last row of page 1 (t009) re-appears as the first row of
	// page 2. The seen-set must DROP that re-emitted boundary row so the returned
	// slice has zero duplicate node.Id. The unshifted node lands behind the cursor
	// (index 0, already passed) so it is not served — the 20 original rows each
	// appear exactly once. WITHOUT the seen-set, t009 would appear twice (21 rows,
	// a duplicate) — this case is the fails-when-absent guard for the dedup.
	t.Run("front_insert_seen_set_dedup", func(t *testing.T) {
		fc := &pagingFakeCaller{
			nodes:       makeThoughtNodes(20),
			frontInsert: &knowledgev1.Node{Id: "t-inserted", Type: string(kgtypes.NodeThought)},
		}
		got, err := drainThoughtBrowse(ctx, fc, tt, 10)
		require.NoError(t, err)
		// Zero duplicates — the re-emitted boundary row is dropped by the seen-set.
		uniqueIDs(t, got)
		// Exact count: the 20 original rows, each once (the re-emitted t009 is not
		// double-counted; the behind-cursor inserted node is never served).
		require.Len(t, got, 20, "boundary row deduped, not double-counted")
	})
}
