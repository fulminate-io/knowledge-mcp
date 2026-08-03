// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// sealOneSegmentEach force-seals one segment per document into the engine behind dm,
// bypassing the coalescing threshold the way a small write does. n segments in,
// n sealed segments out — unless a background merge consolidates them, which is
// exactly what this file asserts does not happen.
func sealOneSegmentEach[Q, S any](t *testing.T, dm *distManager[Q, S], n int) {
	t.Helper()
	for i := range n {
		require.NoError(t, dm.engine.Add(vecContentDocsSeed(1, i)))
		require.NoError(t, dm.engine.Flush())
	}
}

// TestSegmentEnginesDoNotAutoMerge is the catcher for the per-engine merge
// scoping: both segment engines this package constructs — the HNSW engine and the
// BM25 engine — must be built with the background merge triggers disarmed, so a
// segment layout this package maintains is never consolidated behind its back.
// (A third arm covered a separate deterministic rebuild engine, which no longer
// exists: the reset finalizes at the HNSW engine the first arm builds.)
//
// Each arm seals more than 32 segments, which is well past the engine's default
// count target of 16. With the default policy the count trigger returns the whole
// entry set and one tick collapses them into a single segment; with the triggers
// scoped off the segments survive. Missing EITHER construction site restores the
// default for that engine, so each arm fails independently.
//
// DELIBERATELY NOT PARALLEL. Shared resource: CPU scheduling, which this test
// reads as a signal. Both arms assert a NEGATIVE across a fixed wall-clock settle
// — the merge counter must still be zero after it elapses — and that shape is
// satisfied just as well by a merger that was never scheduled as by one that is
// correctly disarmed. Peers saturating the cores would therefore make this gate
// pass without testing anything, which is the one failure mode a catcher must not
// have.
func TestSegmentEnginesDoNotAutoMerge(t *testing.T) {
	const (
		segments = 33 // > 32, and far above the default SegmentCountTarget of 16
		// Well past several 50ms merge ticks: the merger also wakes on the write
		// signal every seal, so a live trigger fires long before this elapses.
		settle = 500 * time.Millisecond
	)
	gt := kgtypes.GraphCode

	mgr := NewManager(loginStateStub{loggedIn: false}, t.TempDir(), 0)

	t.Run("embed hnsw", func(t *testing.T) {
		dm := mgr.managerFor(gt, "mergescoping-embed")
		sealOneSegmentEach(t, dm, segments)
		time.Sleep(settle)
		require.Zero(t, dm.engine.MergeCount(), "the embed HNSW engine must not background-merge")
		require.Greater(t, dm.engine.Metrics().SegmentCount, 16,
			"the sealed segments must survive rather than collapse into one")
	})

	t.Run("bm25", func(t *testing.T) {
		dm := mgr.bm25ManagerFor(gt, "mergescoping-bm25")
		sealOneSegmentEach(t, dm, segments)
		time.Sleep(settle)
		require.Zero(t, dm.engine.MergeCount(), "the BM25 engine must not background-merge")
		require.Greater(t, dm.engine.Metrics().SegmentCount, 16,
			"the sealed segments must survive rather than collapse into one")
	})
}

// TestFactoryWiresReclaimHookDespiteDisarm asserts that disarming the automatic
// merge triggers did not also drop the merge-completion hook: both engines still
// carry one. Those are two independent construction decisions that a single Options
// literal expresses, and turning the trigger off must not disturb the second.
//
// A THIRD ASSERTION USED TO LIVE HERE — that the DETERMINISTIC rebuild engine carried
// NO hook, because it reclaimed through a different channel. There is no second HNSW
// engine now, and the hook is no longer conditional on a construction flag, so what
// remains at risk is only that it stays wired at all. That risk went UP, not down, when
// the two factories collapsed into one: the flag that used to select it is gone.
//
// It reads the state INSTALLED ON THE ENGINE rather than any record of what the
// factory meant to install, so deleting the OnMerge assignment fails it.
func TestFactoryWiresReclaimHookDespiteDisarm(t *testing.T) {
	t.Parallel()

	gt := kgtypes.GraphCode
	mgr := NewManager(loginStateStub{loggedIn: false}, t.TempDir(), 0)

	require.True(t, mgr.managerFor(gt, "hookwiring").engine.HasMergeHook(),
		"the HNSW engine must carry the reclaim hook")
	require.True(t, mgr.bm25ManagerFor(gt, "hookwiring").engine.HasMergeHook(),
		"the BM25 engine must carry the reclaim hook")
}

// TestMergeDisabledOptionsSurviveDefaults pins the seam the scoping rides on: the
// engine fills these fields only when unset, so the disarming values reach the
// policy rather than being replaced by the defaults. A dead ratio above 1.0 is
// unreachable because the ratio is dead documents over total, and the count target
// is never exceeded by a real segment set.
func TestMergeDisabledOptionsSurviveDefaults(t *testing.T) {
	t.Parallel()

	require.Greater(t, float64(searchengine.MergeDisabledDeadRatio), 1.0,
		"the dead-ratio value must be unreachable, not merely large")
	require.Greater(t, searchengine.MergeDisabledCountTarget, 1<<20,
		"the count target must be far above any real segment count")
}
