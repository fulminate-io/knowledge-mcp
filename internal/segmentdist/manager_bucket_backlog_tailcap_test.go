// SPDX-License-Identifier: Apache-2.0

// manager_bucket_backlog_tailcap_test.go — the RESIDENT-GROWTH BOUND
// (GitHub issue #172, requirement R4).
//
// Both engines are built with the count-triggered merge disabled on purpose, so
// the resident segment set only falls when a re-emit runs. Between reconcile ticks
// a write lease seals one segment per partition it touches, and a reported host
// reached 16,761 resident segments that way. recordDirty now crosses a SECOND
// emergency valve beside the byte cap — the number of sealed tails the window has
// accumulated — and flags the graph for an earlier reconcile.
//
// EVERY ASSERTION HERE IS ABOUT THE NUDGE, NOT ABOUT A CALL COUNT. flagReconcileNudge
// is a set insert plus a coalescing non-blocking send, so "it was called once" is
// not observable and is not the contract: what a consumer sees is the graph
// appearing in TakeReconcileNudges, and the predicate stays true on every later
// recordDirty while the backlog is over the cap — exactly as the byte cap does.
//
// It runs on the backlog accounting alone: no engine, no segment source.

package segmentdist

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// tailIDs builds n distinct sealed-tail ids.
func tailIDs(prefix string, n int) []searchengine.SegmentID {
	out := make([]searchengine.SegmentID, 0, n)
	for i := range n {
		out = append(out, prefix+"-"+itoaTail(i))
	}
	return out
}

// itoaTail keeps this file free of an strconv import it would need for one call.
func itoaTail(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

// nudgedNames is the set of graph names TakeReconcileNudges reports, by graph type.
func nudgedNames(t *testing.T, m *Manager, gt kgtypes.GraphType) []string {
	t.Helper()
	var out []string
	for _, n := range m.TakeReconcileNudges() {
		if n.GraphType == gt {
			out = append(out, n.Name)
		}
	}
	return out
}

// TestSealedTailCapIsTheCountTheFanoutBudgetWasMeasuredAt is the agreement the
// cap rests on, and it is an agreement between two packages rather than inside one.
//
// The cap exists to hold the resident segment set at a count whose SEARCH LATENCY
// was measured; that measurement lives in searchengine, which this package imports,
// and it is published there as ResidentSegmentFanoutBudget. Without this row either
// constant could move alone: raising the cap would leave the engine's latency gate
// measuring a count production no longer permits, and lowering the budget would
// leave the cap holding a set nobody has measured the cost of.
func TestSealedTailCapIsTheCountTheFanoutBudgetWasMeasuredAt(t *testing.T) {
	t.Parallel()
	require.Equal(t, searchengine.ResidentSegmentFanoutBudget, pendingReEmitTailCap,
		"the sealed-tail cap must be the resident segment count the search fan-out budget was measured at; "+
			"moving either constant alone leaves a bound and its justification describing different numbers")
}

// TestSealedTailCapFlagsAnEarlierReconcile is R4's crossing predicate.
func TestSealedTailCapFlagsAnEarlierReconcile(t *testing.T) {
	gt := kgtypes.GraphCode

	t.Run("a_backlog_that_approaches_the_cap_without_crossing_flags_nothing", func(t *testing.T) {
		t.Parallel()
		m := closeOnCleanup(t, NewManager(t.TempDir(), 0))
		m.recordDirty(gt, "under", false, nil, tailIDs("under", pendingReEmitTailCap-1), m.nextWriteSeq())
		require.Empty(t, nudgedNames(t, m, gt),
			"one tail short of the cap must not flag; a predicate that fires here is not a cap")
	})

	t.Run("crossing_the_cap_flags_the_graph", func(t *testing.T) {
		t.Parallel()
		m := closeOnCleanup(t, NewManager(t.TempDir(), 0))
		m.recordDirty(gt, "over", false, nil, tailIDs("a", pendingReEmitTailCap-1), m.nextWriteSeq())
		require.Empty(t, nudgedNames(t, m, gt), "PRECONDITION: nothing is flagged before the crossing batch")

		m.recordDirty(gt, "over", false, nil, tailIDs("b", 1), m.nextWriteSeq())
		require.Equal(t, []string{"over"}, nudgedNames(t, m, gt),
			"the batch that takes the sealed-tail count to the cap must flag the graph for an earlier reconcile")
	})

	t.Run("the_crossing_is_per_format", func(t *testing.T) {
		t.Parallel()
		m := closeOnCleanup(t, NewManager(t.TempDir(), 0))
		// The vector backlog crosses; the field backlog of the SAME graph does not.
		m.recordDirty(gt, "perFormat", true, nil, tailIDs("bm25", 4), m.nextWriteSeq())
		require.Empty(t, nudgedNames(t, m, gt), "PRECONDITION: a small field backlog flags nothing")

		m.recordDirty(gt, "perFormat", false, nil, tailIDs("hnsw", pendingReEmitTailCap), m.nextWriteSeq())
		require.Equal(t, []string{"perFormat"}, nudgedNames(t, m, gt))

		// And the field backlog, still far under the cap, does not flag on its own.
		m.recordDirty(gt, "perFormat", true, nil, tailIDs("bm25b", 4), m.nextWriteSeq())
		require.Empty(t, nudgedNames(t, m, gt),
			"a format under the cap must not inherit the other format's crossing; the two engines seal and publish independently")
	})

	t.Run("the_crossing_is_per_graph", func(t *testing.T) {
		t.Parallel()
		m := closeOnCleanup(t, NewManager(t.TempDir(), 0))
		m.recordDirty(gt, "crossing", false, nil, tailIDs("x", pendingReEmitTailCap), m.nextWriteSeq())
		m.recordDirty(gt, "quiet", false, nil, tailIDs("y", 3), m.nextWriteSeq())
		require.Equal(t, []string{"crossing"}, nudgedNames(t, m, gt),
			"one graph's crossing must not flag another's backlog")
	})

	t.Run("a_drain_clears_the_tails_and_the_next_crossing_flags_again", func(t *testing.T) {
		t.Parallel()
		m := closeOnCleanup(t, NewManager(t.TempDir(), 0))
		m.recordDirty(gt, "recross", false, nil, tailIDs("first", pendingReEmitTailCap), m.nextWriteSeq())
		require.Equal(t, []string{"recross"}, nudgedNames(t, m, gt), "PRECONDITION: the first crossing flags")

		// The drain consumes exactly what it snapshotted, which is the whole backlog.
		snap, _ := m.snapshotDirty(gt, "recross")
		m.clearDirty(gt, "recross", snap, formatDirtyState{})
		after, _ := m.snapshotDirty(gt, "recross")
		require.Empty(t, after.tails, "PRECONDITION: the drain must have cleared the window's tails")

		m.recordDirty(gt, "recross", false, nil, tailIDs("second", pendingReEmitTailCap-1), m.nextWriteSeq())
		require.Empty(t, nudgedNames(t, m, gt), "the count restarts from the cleared backlog, so one short of the cap flags nothing")
		m.recordDirty(gt, "recross", false, nil, tailIDs("second-last", 1), m.nextWriteSeq())
		require.Equal(t, []string{"recross"}, nudgedNames(t, m, gt), "and the next crossing flags again")
	})

	// THE BYTE CAP IS THE SAME-RUN KNOWN POSITIVE. It is what proves the new
	// predicate was ADDED beside the existing one rather than replacing it: a
	// backlog that crosses the byte cap with a handful of tails must still flag.
	t.Run("known_positive_the_byte_cap_still_flags_on_its_own", func(t *testing.T) {
		t.Parallel()
		m := closeOnCleanup(t, NewManager(t.TempDir(), 0))
		big := searchengine.Document{
			ID:     "fat",
			Fields: map[string]string{"body": string(make([]byte, pendingReEmitByteCap+1))},
		}
		m.recordDirty(gt, "bytes", false, []searchengine.Document{big}, tailIDs("one", 1), m.nextWriteSeq())
		require.Equal(t, []string{"bytes"}, nudgedNames(t, m, gt),
			"the byte cap must keep firing; a tail cap that replaced it would silently retire the memory bound")
	})
}
