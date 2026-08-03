// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// TestPerGraphMergeAndRepairState pins FOUR properties for BOTH new records,
// because each is a separate way a durable record can silently stop working:
// round-trip across a restart, per-graph scoping, absent-safety, and inertness on
// a Manager with no cache root.
func TestPerGraphMergeAndRepairState(t *testing.T) {
	t.Parallel()

	const horizon = int64(1_700_000_000_123_456_789)
	converged := RepairState{Residue: 7, Converged: true, Scanned: true, VerifiedAtNanos: horizon}

	t.Run("saved values survive a reload", func(t *testing.T) {
		dir := t.TempDir()
		mgr := NewManager(loginStateStub{}, dir, 0)

		require.NoError(t, mgr.SaveMergeWatermark(kgtypes.GraphCode, "repo", horizon))
		require.NoError(t, mgr.SaveRepairState(kgtypes.GraphCode, "repo", converged))

		// A SECOND Manager over the same cache root is the daemon restart: these
		// records exist precisely so a restart does not re-earn work the previous
		// process paid for, so they must read what the first wrote.
		restarted := NewManager(loginStateStub{}, dir, 0)

		gotHorizon, err := restarted.LoadMergeWatermark(kgtypes.GraphCode, "repo")
		require.NoError(t, err)
		require.Equal(t, horizon, gotHorizon)

		gotState, err := restarted.LoadRepairState(kgtypes.GraphCode, "repo")
		require.NoError(t, err)
		require.Equal(t, converged, gotState)

		// A BOOL THAT ONLY EVER MOVES ONE WAY is the classic persisted-flag bug, and
		// both bits on this record are read as trust bits — so write false over a
		// previous true and require the false to survive the reload.
		unsettled := RepairState{Residue: 3, Converged: false, Scanned: false, VerifiedAtNanos: horizon + 1}
		require.NoError(t, restarted.SaveRepairState(kgtypes.GraphCode, "repo", unsettled))

		reread := NewManager(loginStateStub{}, dir, 0)
		gotState, err = reread.LoadRepairState(kgtypes.GraphCode, "repo")
		require.NoError(t, err)
		require.Equal(t, unsettled, gotState)
		require.False(t, gotState.Converged, "a trust bit that cannot be cleared would mask every later un-settled pass")
		require.False(t, gotState.Scanned, "the scanned bit must clear too — the coverage column ANDs it with converged")
	})

	t.Run("records are per graph", func(t *testing.T) {
		dir := t.TempDir()
		mgr := NewManager(loginStateStub{}, dir, 0)

		require.NoError(t, mgr.SaveMergeWatermark(kgtypes.GraphCode, "alpha", horizon))
		require.NoError(t, mgr.SaveRepairState(kgtypes.GraphCode, "alpha", converged))

		gotHorizon, err := mgr.LoadMergeWatermark(kgtypes.GraphCode, "beta")
		require.NoError(t, err)
		require.Zero(t, gotHorizon, "one graph's horizon must never scope another graph's delta window")

		gotState, err := mgr.LoadRepairState(kgtypes.GraphCode, "beta")
		require.NoError(t, err)
		require.Equal(t, RepairState{}, gotState, "one graph's converged bit must never gate another graph's backstop")
	})

	t.Run("absent records read as zero with a nil error", func(t *testing.T) {
		mgr := NewManager(loginStateStub{}, t.TempDir(), 0)

		// This is the contract every caller's degradation depends on: no record means
		// no horizon (pull nothing) and no verification (eligible for one scan),
		// neither of which is a failure.
		gotHorizon, err := mgr.LoadMergeWatermark(kgtypes.GraphCode, "neverMerged")
		require.NoError(t, err)
		require.Zero(t, gotHorizon)

		gotState, err := mgr.LoadRepairState(kgtypes.GraphCode, "neverScanned")
		require.NoError(t, err)
		require.Equal(t, RepairState{}, gotState)
	})

	t.Run("inert without a cache dir", func(t *testing.T) {
		// The bootstrap tests build cacheless Managers; a Save that errored or a Load
		// that failed there would break them, so both must degrade silently.
		mgr := NewManager(loginStateStub{}, "", 0)

		require.NoError(t, mgr.SaveMergeWatermark(kgtypes.GraphCode, "repo", horizon))
		require.NoError(t, mgr.SaveRepairState(kgtypes.GraphCode, "repo", converged))

		gotHorizon, err := mgr.LoadMergeWatermark(kgtypes.GraphCode, "repo")
		require.NoError(t, err)
		require.Zero(t, gotHorizon)

		gotState, err := mgr.LoadRepairState(kgtypes.GraphCode, "repo")
		require.NoError(t, err)
		require.Equal(t, RepairState{}, gotState)
	})
}

// TestDropGraphCacheRemovesMergeAndRepairRecords turns an inheritance into a
// measured fact: both new records live under the rebuildstate format directory
// DropGraphCache already enumerates, so dropping a graph sweeps them for free.
//
// The two graphs are a PREFIX PAIR deliberately — the drop's graph-directory match
// is exact, and a sibling like code/repo@branch beside code/repo must keep its
// records.
func TestDropGraphCacheRemovesMergeAndRepairRecords(t *testing.T) {
	t.Parallel()

	const (
		droppedHorizon = int64(1_700_000_000_000_000_001)
		siblingHorizon = int64(1_700_000_000_000_000_002)
	)
	siblingState := RepairState{Residue: 11, Converged: true, Scanned: true, VerifiedAtNanos: siblingHorizon}

	dir := t.TempDir()
	mgr := NewManager(loginStateStub{}, dir, 0)

	require.NoError(t, mgr.SaveMergeWatermark(kgtypes.GraphCode, "repo", droppedHorizon))
	require.NoError(t, mgr.SaveRepairState(kgtypes.GraphCode, "repo",
		RepairState{Residue: 5, Converged: true, Scanned: true, VerifiedAtNanos: droppedHorizon}))
	require.NoError(t, mgr.SaveMergeWatermark(kgtypes.GraphCode, "repo@branch", siblingHorizon))
	require.NoError(t, mgr.SaveRepairState(kgtypes.GraphCode, "repo@branch", siblingState))

	report, err := mgr.DropGraphCache(kgtypes.GraphCode, "repo")
	require.NoError(t, err)
	require.Contains(t, report.Formats, rebuildStateFormat,
		"the records live under the reserved rebuildstate format dir, so that dir must be among the swept formats")

	// A fresh Manager, so the answers come off disk rather than out of the hot map.
	after := NewManager(loginStateStub{}, dir, 0)

	gotHorizon, err := after.LoadMergeWatermark(kgtypes.GraphCode, "repo")
	require.NoError(t, err)
	require.Zero(t, gotHorizon, "the dropped graph's merge horizon went with its cache")

	gotState, err := after.LoadRepairState(kgtypes.GraphCode, "repo")
	require.NoError(t, err)
	require.Equal(t, RepairState{}, gotState, "the dropped graph's backstop record went with its cache")

	gotHorizon, err = after.LoadMergeWatermark(kgtypes.GraphCode, "repo@branch")
	require.NoError(t, err)
	require.Equal(t, siblingHorizon, gotHorizon, "a prefix-sibling graph must survive a drop of the base graph")

	gotState, err = after.LoadRepairState(kgtypes.GraphCode, "repo@branch")
	require.NoError(t, err)
	require.Equal(t, siblingState, gotState, "a prefix-sibling graph must survive a drop of the base graph")
}
