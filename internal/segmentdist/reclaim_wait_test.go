// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

const (
	// reclaimSettleDeadline bounds the completion wait. Generous, so a real HNSW or
	// BM25 merge of a large segment completes even under -race instrumentation;
	// reaching it is a failure, not a tax, so its length costs a passing run nothing.
	reclaimSettleDeadline = 30 * time.Second
	// reclaimSettlePoll is the interval between evaluations of the conjunction.
	reclaimSettlePoll = 10 * time.Millisecond
)

// waitReclaimSettled waits until the engine owes no reclaim: no merge is in flight,
// no merge has published whose hook has not returned, and none is about to start.
//
// WHAT IT REPLACED AND WHY. It stands where waitMergeQuiesce and
// waitMergeQuiesceWindow stood. Those returned once the engine's MERGE COUNTER had
// not moved for a wall-clock window, and that counter increments at the CAS publish
// — BEFORE the OnMerge hook that writes the consolidated blob to L2 and removes the
// segments it superseded. So a stable counter meant "nothing new was published
// recently", never "the reclaim finished": the window could elapse entirely inside
// one hook, and the test then inspected a corpus with a live segment that had no L2
// file and constituents that had not been removed. That is not a window too short
// to widen — it is the wrong event, and every widening of it bought time rather
// than truth.
//
// THE OBSERVABLE IS A CONJUNCTION, and each half is load-bearing.
//
//   - PUBLISHED == SETTLED says every merge that has published has also finished the
//     work that publish set in motion (searchengine's SettledMergeCount).
//   - NOT MERGE-ELIGIBLE says the merger would pick nothing on its next tick
//     (searchengine's MergeEligible), so no NEW merge is about to start.
//
// Either half alone returns too early. The counters are equal in every lull between
// two merges of a chain, because the merger goes back to its select for at least one
// tick before the next publish. Eligibility alone goes false at the publish CAS,
// which is one step further into the very window this wait exists to close.
//
// THE PRECONDITION: no test-side write to the engine may run concurrently with this
// wait. Every caller satisfies it today by doing its writes first. The conjunction
// is not a lock — an Add or a Delete landing during the wait can make an ineligible
// corpus eligible again the moment after the read.
func waitReclaimSettled[Q, S any](t invariantT, e *searchengine.SegmentedIndex[Q, S]) {
	t.Helper()
	waitReclaimSettledUntil(t, reclaimSettleDeadline, e.MergeEligible, e.MergeCount, e.SettledMergeCount)
}

// waitReclaimSettledUntil is waitReclaimSettled over caller-supplied readers and a
// caller-chosen deadline. The readers are a seam: they let a test drive the loop
// through an interleaving a real engine would only produce under load.
//
// THE READ ORDER IS THE CONTRACT, NOT AN IMPLEMENTATION DETAIL. Read ELIGIBILITY
// first, then PUBLISHED, then SETTLED. The three reads are not atomic with one
// another, and the obvious spelling — counters first, eligibility last — can still
// return while a reclaim is owed: the counters read equal at their pre-publish
// values, a merge publishes, and the eligibility read then sees the corpus that
// merge already consolidated. Reading eligibility FIRST closes that: a merge owed
// at the settled read either published before the published read, in which case the
// counters differ, or published after it, in which case its constituents were still
// unmerged at the eligibility read and eligibility was true.
//
// THE DEADLINE ARM IS Errorf, NOT Fatalf, and that is deliberate. One caller runs
// this inside a goroutine (the property test's per-graph waits), where FailNow is
// invalid; Errorf is goroutine-safe, marks the test failed, and lets the failure
// reach the test goroutine at the next join. What it must never be is a bare
// return: a wait that gives up quietly is the defect this helper replaced.
func waitReclaimSettledUntil(
	t invariantT, within time.Duration,
	eligible func() bool, published, settled func() uint64,
) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		// ELIGIBILITY, then PUBLISHED, then SETTLED. Read in any other order this
		// loop can return with a reclaim owed; see the doc above.
		if !eligible() {
			pub := published()
			stl := settled()
			if pub == stl {
				return
			}
		}
		time.Sleep(reclaimSettlePoll)
	}
	t.Errorf("reclaim never settled within %s: merge-eligible=%v published=%d settled=%d — "+
		"either a merge hook is still running, or the engine keeps finding new merge targets",
		within, eligible(), published(), settled())
}

// TestReclaimSettleWaitFailsOnItsDeadline pins the arm the deleted helpers got
// wrong: an unsatisfiable wait must report, and must name the deadline it spent.
func TestReclaimSettleWaitFailsOnItsDeadline(t *testing.T) {
	t.Parallel()

	rec := &recorderT{}
	// One merge published, its hook never returns: the conjunction can never hold.
	waitReclaimSettledUntil(rec, 60*time.Millisecond,
		func() bool { return false },
		func() uint64 { return 2 },
		func() uint64 { return 1 })

	require.True(t, rec.failed, "an exhausted deadline must FAIL the test, not return as if satisfied")
	require.Len(t, rec.msgs, 1)
	require.Contains(t, rec.msgs[0], "60ms", "the failure must name the deadline it spent")
	require.Contains(t, rec.msgs[0], "published=2", "and the counters that were still apart")
	require.Contains(t, rec.msgs[0], "settled=1")
}

// TestReclaimSettleWaitDeadlineReachesTheTestGoroutine pins the property test's
// seam: its waits run inside wg.Go, where require's FailNow is invalid, so the
// deadline arm has to be a report the TEST goroutine still sees after the join.
func TestReclaimSettleWaitDeadlineReachesTheTestGoroutine(t *testing.T) {
	t.Parallel()

	rec := &recorderT{}
	var wg sync.WaitGroup
	wg.Go(func() {
		waitReclaimSettledUntil(rec, 60*time.Millisecond,
			func() bool { return false },
			func() uint64 { return 1 },
			func() uint64 { return 0 })
	})
	wg.Wait()

	require.True(t, rec.failed, "a deadline reached inside a wait goroutine must still fail the test after the join")
	require.Contains(t, rec.msgs[0], "60ms")
}

// TestReclaimSettleWaitReadsEligibilityBeforeTheCounters is the ONE test that
// distinguishes the safe read order from the obvious one. Every other test in this
// package passes under either.
//
// THE SCRIPT IS THE INTERLEAVING. The eligibility reader publishes a merge as a
// side effect of being read — modeling a merger that publishes in the instant after
// the wait asked whether anything was eligible — and the hook for that merge never
// returns, so settled stays one behind forever.
//
//   - Reading eligibility FIRST: the publish happens during that read, so the
//     published counter read next already carries it, published != settled, and the
//     wait correctly refuses to return. It burns its deadline and reports.
//   - Reading eligibility LAST: published and settled are read at their pre-publish
//     values and compare equal, the eligibility read then reports false because the
//     merge it just triggered consolidated the corpus, and the wait returns with a
//     reclaim owed. No failure is reported, which is what this assertion catches.
func TestReclaimSettleWaitReadsEligibilityBeforeTheCounters(t *testing.T) {
	t.Parallel()

	var published, settled uint64 = 1, 1
	eligible := func() bool {
		// The merger publishes HERE, in the instant after eligibility was read, and
		// its hook does not return.
		published = 2
		return false
	}

	rec := &recorderT{}
	waitReclaimSettledUntil(rec, 80*time.Millisecond, eligible,
		func() uint64 { return published },
		func() uint64 { return settled })

	require.True(t, rec.failed,
		"the wait returned while a published merge still owed its reclaim: the counters must be read "+
			"AFTER the eligibility check, so a publish landing in that instant is visible in the published counter")
}

// TestReclaimSettleWaitReturnsOnAnEngineThatNeverMerged pins the trivial state:
// nothing published, nothing settled, nothing eligible, so the first evaluation of
// the conjunction holds and the wait returns rather than watching a clock.
func TestReclaimSettleWaitReturnsOnAnEngineThatNeverMerged(t *testing.T) {
	t.Parallel()

	dm, _ := buildHNSWReclaimManager(t, kgtypes.GraphCode, "never-merged", t.TempDir(), 1<<30)
	defer dm.engine.Close()

	rec := &recorderT{}
	waitReclaimSettled(rec, dm.engine)
	require.False(t, rec.failed, "a corpus that never merged owes no reclaim: %v", rec.msgs)
	require.Equal(t, uint64(0), dm.engine.MergeCount())
}

// TestReclaimSettleWaitIsNotOneShot pins that the wait is a predicate, not a latch:
// a second merge occasion after a satisfied wait is waited out just as the first was.
func TestReclaimSettleWaitIsNotOneShot(t *testing.T) {
	t.Parallel()

	dm, ic := buildHNSWReclaimManagerWithHookDelay(
		t, kgtypes.GraphCode, "not-one-shot", t.TempDir(), 4, 150*time.Millisecond)
	defer dm.engine.Close()

	docs := vecContentDocs(10)
	for _, d := range docs[:5] {
		require.NoError(t, dm.engine.Add([]searchengine.Document{d}))
	}
	waitForMerge(t, dm.engine.MergeCount, "the first count-driven merge must fire")
	waitReclaimSettled(t, dm.engine)
	first := dm.engine.MergeCount()
	require.Equal(t, first, dm.engine.SettledMergeCount(), "the first wait must leave nothing owed")
	firstRemoved := len(ic.removedSet())
	require.NotZero(t, firstRemoved, "the first merge reclaimed its constituents")

	for _, d := range docs[5:] {
		require.NoError(t, dm.engine.Add([]searchengine.Document{d}))
	}
	waitReclaimSettled(t, dm.engine)
	require.Greater(t, dm.engine.MergeCount(), first, "a second merge occasion must have fired")
	require.Equal(t, dm.engine.MergeCount(), dm.engine.SettledMergeCount(), "and the second wait must leave nothing owed either")
	require.Greater(t, len(ic.removedSet()), firstRemoved, "with its own reclaim finished by the time the wait returned")
}

// TestReclaimSettleWaitAfterEngineClose pins the shutdown transition, and it has
// TWO halves because the conjunction answers them differently.
//
// A CLOSED ENGINE THAT SETTLED OWES NOTHING. Close JOINS the merger, and every
// post-publish path through doMerge settles before it returns, so the counters are
// equal and the consolidated corpus is not eligible: the wait returns on its first
// evaluation rather than watching a clock.
//
// A CLOSED ENGINE WITH AN ELIGIBLE CORPUS FAILS LOUDLY, and that is the correct
// answer rather than a limitation. The merger is gone, so nothing will ever
// consolidate that corpus and no reclaim will ever run on it. A wait that returned
// here would be reporting "the reclaim is finished" about a reclaim that will never
// start, which is the silent-satisfaction failure this whole helper replaced. The
// deadline arm names merge-eligible=true, so the report says which half refused.
func TestReclaimSettleWaitAfterEngineClose(t *testing.T) {
	t.Parallel()

	t.Run("a settled corpus returns at once", func(t *testing.T) {
		t.Parallel()

		dm, _ := buildHNSWReclaimManagerWithHookDelay(
			t, kgtypes.GraphCode, "closed-settled", t.TempDir(), 4, 150*time.Millisecond)
		for _, d := range vecContentDocs(5) {
			require.NoError(t, dm.engine.Add([]searchengine.Document{d}))
		}
		waitForMerge(t, dm.engine.MergeCount, "count-driven merge must fire")
		waitReclaimSettled(t, dm.engine)
		dm.engine.Close()

		rec := &recorderT{}
		start := time.Now()
		waitReclaimSettledUntil(rec, 5*time.Second,
			dm.engine.MergeEligible, dm.engine.MergeCount, dm.engine.SettledMergeCount)
		require.False(t, rec.failed, "a joined merger with nothing owed must satisfy the wait: %v", rec.msgs)
		require.Less(t, time.Since(start), time.Second, "and satisfy it at once, not after a poll cycle")
	})

	t.Run("a corpus no merger will ever consume fails loudly", func(t *testing.T) {
		t.Parallel()

		dm, _ := buildHNSWReclaimManager(t, kgtypes.GraphCode, "closed-eligible", t.TempDir(), 4)
		// Join the merger FIRST, then build a corpus past the count target. Nothing
		// will ever merge it, so no reclaim will ever be owed OR performed — the
		// state a wait must refuse rather than bless.
		dm.engine.Close()
		for _, d := range vecContentDocs(5) {
			require.NoError(t, dm.engine.Add([]searchengine.Document{d}))
		}
		require.True(t, dm.engine.MergeEligible(),
			"fixture: the corpus must be past the count target, or this asserts nothing")
		require.Equal(t, uint64(0), dm.engine.MergeCount(), "fixture: and no merge may have fired")

		rec := &recorderT{}
		waitReclaimSettledUntil(rec, 200*time.Millisecond,
			dm.engine.MergeEligible, dm.engine.MergeCount, dm.engine.SettledMergeCount)
		require.True(t, rec.failed,
			"a merge that can never run is not a merge that finished: the wait must report, not return")
		require.Contains(t, rec.msgs[0], "merge-eligible=true", "and must name the half that refused")
	})
}
