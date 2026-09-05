// SPDX-License-Identifier: Apache-2.0

package tools

// raw_graph_drop_workingset_test.go gates the drop lifecycle's local half: a
// graph the user has DROPPED must stop being a member of this client's working
// set, or every background loop keeps draining, reconciling and covering a graph
// that no longer exists — forever, because nothing else ages a member out.

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/workingset"
)

// workingSetDropDeps satisfies THREE seams, and each one is load-bearing.
//
//	1+2. InWorkingSet and RemoveFromWorkingSet, over a REAL workingset.Set. A deps
//	     implementing neither answers "not wired" — false and no-op — and every
//	     assertion below would pass against an implementation that does nothing.
//	     That is the recorder-that-cannot-discriminate trap, so the membership is
//	     real and is asserted present BEFORE the drop.
//	3.   SegmentCacheDropper, so a leg can VARY it. dropGraphAck returns early
//	     when no segment engine is wired, so a removal placed beside the cache
//	     teardown is skipped entirely on that path while the client has still
//	     taken the server-side drop. The nil-dropper leg is what sees that.
//
// removals counts as well as delegating, so a leg can assert removals == 1 rather
// than only asserting absence — absence alone is also what a member that was
// never admitted looks like.
type workingSetDropDeps struct {
	interceptTestDeps
	set      *workingset.Set
	dropper  SegmentCacheDropper
	removals int
}

func (d *workingSetDropDeps) SegmentCacheDropper() SegmentCacheDropper { return d.dropper }

func (d *workingSetDropDeps) InWorkingSet(gt kgtypes.GraphType, name string) bool {
	return d.set.Has(gt, name)
}

func (d *workingSetDropDeps) RemoveFromWorkingSet(gt kgtypes.GraphType, name string) bool {
	d.removals++
	return d.set.Remove(gt, name)
}

// newWorkingSetDropDeps builds the fixture with (pdf, stopford) admitted.
func newWorkingSetDropDeps(t *testing.T, dropper SegmentCacheDropper) *workingSetDropDeps {
	t.Helper()
	set := workingset.New()
	require.True(t, set.Admit(kgtypes.GraphPDFRaw, "stopford", "test"),
		"the fixture must actually admit the graph, or the post-drop absence proves nothing")
	deps := &workingSetDropDeps{
		interceptTestDeps: interceptTestDeps{gc: &fakeGraphCaller{}},
		set:               set,
		dropper:           dropper,
	}
	require.True(t, deps.InWorkingSet(kgtypes.GraphPDFRaw, "stopford"),
		"the graph must be a member BEFORE the drop")
	return deps
}

// TestDropGraph_RemovesWorkingSetMembership is the drop-lifecycle property pair.
//
// WHAT IT REJECTS THAT COMPILES: no removal at all; a removal wired into
// handleClientDropGraph ahead of the dry-run return (the dry_run leg reds); and a
// removal placed after dropGraphAck's nil-dropper early return (the
// dropper_nil leg reds while the wired one stays green).
func TestDropGraph_RemovesWorkingSetMembership(t *testing.T) {
	const args = `{"operation":"drop_graph","graph":"pdf","name":"stopford"}`

	t.Run("drop", func(t *testing.T) {
		deps := newWorkingSetDropDeps(t, &fakeCacheDropper{})
		handled, res := InterceptManage(opCtx(), deps,
			kgtools.CallToolParams{Name: "manage", Arguments: json.RawMessage(args)})
		require.True(t, handled)
		require.False(t, res.IsError, "drop_graph: %s", toolResultText(res))

		assert.False(t, deps.InWorkingSet(kgtypes.GraphPDFRaw, "stopford"),
			"a dropped raw graph is still in the working set — the pipeline will keep draining a graph that is gone")
		assert.Equal(t, 1, deps.removals, "the drop must attempt exactly one removal")
	})

	t.Run("dropper_nil", func(t *testing.T) {
		// THE DISCRIMINATING LEG. With no segment engine wired, dropGraphAck
		// returns before it ever resolves the cache target — so a removal placed
		// beside the cache teardown never runs, while this client has still taken
		// the server-side drop and must still stop wanting the graph.
		deps := newWorkingSetDropDeps(t, nil)
		handled, res := InterceptManage(opCtx(), deps,
			kgtools.CallToolParams{Name: "manage", Arguments: json.RawMessage(args)})
		require.True(t, handled)
		require.False(t, res.IsError, "drop_graph: %s", toolResultText(res))
		// The degraded path really was taken, so this leg is not a second copy of
		// the one above under a different name.
		require.Contains(t, toolResultText(res), "segment engine not wired",
			"this leg must exercise the no-segment-engine path")

		assert.False(t, deps.InWorkingSet(kgtypes.GraphPDFRaw, "stopford"),
			"nil SegmentCacheDropper: the drop completed server-side but the working set still lists the graph")
		assert.Equal(t, 1, deps.removals, "the removal must run on the degraded path too")
	})

	t.Run("dry_run", func(t *testing.T) {
		// SCOPE FENCE: a preview must change nothing. This leg PASSES ALREADY
		// against the unfixed tree — it guards a property the change must not
		// break, rather than one the change introduces.
		deps := newWorkingSetDropDeps(t, &fakeCacheDropper{})
		handled, res := InterceptManage(opCtx(), deps, kgtools.CallToolParams{
			Name:      "manage",
			Arguments: json.RawMessage(`{"operation":"drop_graph","graph":"pdf","name":"stopford","dry_run":true}`),
		})
		require.True(t, handled)
		require.False(t, res.IsError)
		require.Contains(t, toolResultText(res), "DRY RUN", "this leg must exercise the preview path")

		assert.Zero(t, deps.removals, "dry_run must attempt NO removal")
		assert.True(t, deps.InWorkingSet(kgtypes.GraphPDFRaw, "stopford"),
			"dry_run removed the graph from the working set — a preview must change nothing")
	})
}
