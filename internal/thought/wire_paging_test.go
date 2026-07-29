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

// pagingFakeCaller is an Execute-only Caller that serves a type-browse drain the
// way the real backends do under an id-KEYSET cursor: it returns the nodes whose
// Id sorts strictly after the plan's after_id, ascending, capped at limit — so a
// page past the end comes back empty and terminates the drain cleanly. It records
// every page's plan (cursor value + presence, offset, skip_total) so a test can
// assert the exact cursor sequence, and optionally front-inserts a node after
// page 1 to exercise the concurrent-writer case.
//
// The backing slice is kept sorted by Id, which makeThoughtNodes already produces.
type pagingFakeCaller struct {
	nodes       []*knowledgev1.Node
	execCalls   atomic.Int64
	frontInsert *knowledgev1.Node // unshifted after the first page when non-nil
	skipTotals  []bool            // per-page q.GetSkipTotal() captured across the drain
	cursors     []string          // per-page after_id VALUE
	cursorSet   []bool            // per-page after_id PRESENCE (nil pointer = false)
	offsets     []int32           // per-page offset — must be 0 on every keyset page
}

func (c *pagingFakeCaller) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	n := c.execCalls.Add(1)
	q := req.GetQuery()
	if q == nil {
		return &knowledgev1.ExecuteResponse{}, nil
	}
	c.skipTotals = append(c.skipTotals, q.GetSkipTotal())
	c.cursors = append(c.cursors, q.GetAfterId())
	c.cursorSet = append(c.cursorSet, q.AfterId != nil)
	c.offsets = append(c.offsets, q.GetOffset())

	// Simulate a concurrent writer inserting a node BEHIND the cursor after page 1
	// was served (at the start of the SECOND Execute). Under offset paging this
	// shifted every later window and re-emitted a page-boundary row; under a keyset
	// cursor it cannot, because the cursor names a row rather than a position.
	if c.frontInsert != nil && n == 2 {
		c.nodes = append([]*knowledgev1.Node{c.frontInsert}, c.nodes...)
	}

	limit := int(q.GetLimit())
	var page []*knowledgev1.Node
	for _, node := range c.nodes {
		if node.GetId() <= q.GetAfterId() {
			continue // at or before the cursor.
		}
		if limit > 0 && len(page) >= limit {
			break
		}
		page = append(page, node)
	}
	return &knowledgev1.ExecuteResponse{Nodes: page}, nil
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

// TestDrainThoughtBrowse_KeysetCursor pins the drain's cursor protocol, whose
// load-bearing detail is that page 1 SETS after_id to the empty string rather
// than omitting it. Presence is what selects the keyset browse: an omitted field
// means "not a keyset browse", and the backend then pages page 1 in its default
// order (CreatedAt-descending on the local store), so the cursor taken from that
// page skips every lower id and the drain terminates early having silently
// returned a fraction of the corpus.
func TestDrainThoughtBrowse_KeysetCursor(t *testing.T) {
	ctx := context.Background()
	fc := &pagingFakeCaller{nodes: makeThoughtNodes(25)}

	got, err := drainThoughtBrowse(ctx, fc, string(kgtypes.NodeThought), 10)
	require.NoError(t, err)
	require.Len(t, got, 25, "every node drained")
	uniqueIDs(t, got)
	for i := range got {
		assert.Equal(t, thoughtID(i), got[i].Id, "the keyset drain returns ids ascending")
	}

	require.Equal(t, int64(3), fc.execCalls.Load(), "ceil(25/10)=3 pages")
	require.Len(t, fc.cursors, 3)

	// Page 1 carries a SET but EMPTY cursor; pages 2 and 3 carry the PRIOR page's
	// LAST id.
	for i, set := range fc.cursorSet {
		assert.Truef(t, set, "page %d must SET after_id — presence is what selects the keyset browse", i+1)
	}
	assert.Empty(t, fc.cursors[0], "page 1 starts from the beginning")
	assert.Equal(t, thoughtID(9), fc.cursors[1], "page 2 resumes after page 1's last id")
	assert.Equal(t, thoughtID(19), fc.cursors[2], "page 3 resumes after page 2's last id")

	// No page may set Offset — the server rejects a plan carrying both cursors.
	for i, off := range fc.offsets {
		assert.Zerof(t, off, "page %d must not set Offset alongside a keyset cursor", i+1)
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

	// (6) skip_total: EVERY page the drain marshals must carry skip_total=true so
	// the single-layer executor drops the per-page paginating COUNT. 25 nodes over
	// pageSize 10 → 3 pages, each captured plan asserts GetSkipTotal()==true.
	t.Run("every_page_carries_skip_total", func(t *testing.T) {
		fc := &pagingFakeCaller{nodes: makeThoughtNodes(25)}
		got, err := drainThoughtBrowse(ctx, fc, tt, 10)
		require.NoError(t, err)
		require.Len(t, got, 25)
		require.Equal(t, int64(3), fc.execCalls.Load(), "ceil(25/10)=3 page reads")
		require.Len(t, fc.skipTotals, 3, "one captured plan per page")
		for i, st := range fc.skipTotals {
			assert.Truef(t, st, "drain page %d must carry skip_total=true", i)
		}
	})

	// (5) MID-DRAIN INSERT BEHIND THE CURSOR: a concurrent writer adds a node that
	// sorts before the cursor after page 1 is served. Under the old OFFSET paging
	// this shifted page 2's window back one and re-emitted page 1's last row, and
	// the seen-set existed to drop that duplicate. An id-keyset cursor names a ROW
	// rather than a position, so the re-emission cannot happen at all: the inserted
	// node is behind the cursor and is simply never served, and the 20 original
	// rows each appear exactly once. The assertions are unchanged — what changed is
	// that they now hold structurally rather than by deduplication.
	t.Run("insert_behind_cursor_is_not_served", func(t *testing.T) {
		fc := &pagingFakeCaller{
			nodes:       makeThoughtNodes(20),
			frontInsert: &knowledgev1.Node{Id: "t-inserted", Type: string(kgtypes.NodeThought)},
		}
		got, err := drainThoughtBrowse(ctx, fc, tt, 10)
		require.NoError(t, err)
		uniqueIDs(t, got)
		require.Len(t, got, 20,
			"the 20 original rows, each exactly once — the behind-cursor insert is never served and no boundary row is re-emitted")
	})
}
