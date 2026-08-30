// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// TestSearchNudgesMergeWithCoolOff pins the search-driven delta nudge: it fires for
// exactly the graph searched, at most once per cool-off window, and not at all from
// the two seams that are deliberately excluded.
//
// Every assertion re-establishes its own state because TakeReconcileNudges DRAINS.
func TestSearchNudgesMergeWithCoolOff(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	mgr := closeOnCleanup(t, NewManager(t.TempDir(), 0))

	const (
		alpha = "alpha"
		beta  = "beta"
		gamma = "gamma"
	)
	alphaKey := graphKey{graphType: kgtypes.GraphCode, graphName: alpha}

	// A search's own result is irrelevant here — the nudge is recorded before the
	// engines are even loaded, deliberately, because a search against a cold engine
	// is precisely a moment when a delta pull is worth asking for.
	search := func(name string, k int) {
		t.Helper()
		_, _ = mgr.Search(ctx, kgtypes.GraphCode, name, "probe", nil, k)
	}

	// 1 + 2 — the basic wiring and the per-graph scoping. A nudge that flagged every
	// graph would turn one search into a whole-corpus pull.
	search(alpha, 10)
	drained := mgr.TakeReconcileNudges()
	require.Equal(t, []NudgedGraph{{GraphType: kgtypes.GraphCode, Name: alpha}}, drained,
		"a search nudges exactly the graph it searched")
	require.NotContains(t, drained, NudgedGraph{GraphType: kgtypes.GraphCode, Name: beta},
		"an unsearched graph must never be dragged into a nudged pass")

	// 3 — the cool-off. This is the catcher for a cool-off that was written but never
	// consulted: without it every search of a hot graph wakes the reconcile loop.
	search(alpha, 10)
	require.Empty(t, mgr.TakeReconcileNudges(),
		"a second search inside the cool-off window must not re-nudge")

	// 4 — the cool-off EXPIRES. The catcher for a window that never elapses, which
	// would silence the nudge for the process lifetime after one search. Back-dating
	// the in-package stamp is what drives the boundary; no clock seam is needed.
	mgr.nudges.mu.Lock()
	mgr.nudges.lastNudge[alphaKey] = time.Now().Add(-mergeNudgeCoolOff - time.Second)
	mgr.nudges.mu.Unlock()

	search(alpha, 10)
	require.Equal(t, []NudgedGraph{{GraphType: kgtypes.GraphCode, Name: alpha}}, mgr.TakeReconcileNudges(),
		"past the cool-off window the same graph nudges again")

	// 5 — the two excluded seams, driven against a graph never searched so a nudge
	// would certainly be recorded if either fired.
	//
	// VectorByID is the propagation-loop catcher: its non-tool caller is the
	// background propagation loop, and nudging there would ask for a delta pull on
	// every boot with no user interaction at all — the exact defeat of the design.
	_, _, _ = mgr.VectorByID(ctx, kgtypes.GraphCode, gamma, "some-id")
	require.Empty(t, mgr.TakeReconcileNudges(), "a by-id read is not a user search")

	// k<=0 returns before doing any work, so it is not a user search either.
	search(gamma, 0)
	require.Empty(t, mgr.TakeReconcileNudges(), "a k<=0 call is not a user search")
}
