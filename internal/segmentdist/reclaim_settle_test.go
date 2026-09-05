// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// settleHookDelay is how long the reclaim hook stalls in the tests below before it
// starts reclaiming. It is chosen against two bounds, and both matter.
//
// WELL ABOVE the 120ms stability window the deleted waitMergeQuiesce used, so a
// wait that watches only the PUBLISH counter returns while the hook is still
// stalled — that is the failure these tests exist to catch, and at this delay it
// is deterministic rather than load-dependent.
//
// WELL BELOW the completion wait's own 30s deadline, so a wait that correctly
// waits for the hook returns normally. A delay near the deadline would turn a
// PASSING test into a deadline failure, which is a different observation.
const settleHookDelay = 600 * time.Millisecond

// TestReclaimWaitHoldsUntilTheHookHasFinished is the committed regression test for
// the publish-to-reclaim gap: the merge counter moves at the CAS publish
// (searchengine/merge.go), and the reclaim hook that writes the consolidated blob
// to L2 runs afterwards, so between them a live segment has no L2 file and clause
// 1 of assertLiveSetBackedByL2 is false.
//
// IT IS DETERMINISTIC, UNLIKE THE FLAKES THAT MOTIVATED IT. The gap is opened by
// an injected hook delay rather than by CPU contention, so the red does not depend
// on machine load and this test cannot become the flake it guards against.
//
// NOTHING WARMS L2 BETWEEN THE WAIT AND THE ASSERTION, and that is load-bearing
// rather than incidental. warmExported Puts every currently-sealed segment,
// including the merged one, so a warmExported call in that position backs the
// segment the hook has not written yet and turns this test into a false green. The
// sibling tests DO call it there, so the next reader will want to copy that shape:
// do not. This test's live set is exactly the merged segment, and the hook is its
// only route into L2.
func TestReclaimWaitHoldsUntilTheHookHasFinished(t *testing.T) {
	t.Parallel()

	dm, ic := buildHNSWReclaimManagerWithHookDelay(
		t, kgtypes.GraphCode, "settle-hook-delay", t.TempDir(), 4, settleHookDelay)
	defer dm.engine.Close()

	// FIVE single-doc segments against a count target of FOUR. The fifth Add is the
	// first moment the set exceeds the target, and a count-driven merge consolidates
	// every entry, so exactly one merge fires and the live set afterwards is exactly
	// one segment: the merged one. That is what makes clause 1 a single-id question.
	for _, d := range vecContentDocs(5) {
		require.NoError(t, dm.engine.Add([]searchengine.Document{d}))
	}

	waitForMerge(t, dm.engine.MergeCount, "count-driven merge must fire")
	waitReclaimSettled(t, dm.engine)

	// Clause 1 AT THE WAIT'S RETURN. Errorf, not Fatalf, so the known-positive below
	// still runs and a red carries both messages.
	assertLiveSetBackedByL2(t, dm, ic.removedSet(), nil, nil)

	// KNOWN-POSITIVE: the reclaim really ran, so a green is not the green of a merge
	// that never happened.
	require.NotEmpty(t, ic.removedSet(),
		"the merge must have reclaimed its superseded constituents by the time the wait returned")
}

// TestReclaimSettleWaitReturnsWhenTheHookAborts pins the arm that would hang a
// completion wait forever if the settle counter were wired to the reclaim's SUCCESS
// path: a reclaim whose Put fails aborts before removing anything, records the
// abort and retains the obligation, and returns. It is still a hook that RETURNED,
// so the merge is settled and the wait must be satisfied by it.
//
// WHY THE COUNTER IS ENGINE-SIDE AND THIS TEST IS END-TO-END. The settle counter
// lives in doMerge, not in reclaimMerged, so it moves on a real background merge
// and not on the sixteen test call sites that invoke the reclaim directly. A
// direct-invocation test therefore cannot observe it at all; only a genuine merge
// can, which is why this drives the failure through the engine.
func TestReclaimSettleWaitReturnsWhenTheHookAborts(t *testing.T) {
	t.Parallel()

	dm, ic := buildHNSWReclaimManager(t, kgtypes.GraphCode, "settle-abort", t.TempDir(), 4)
	defer dm.engine.Close()

	// Every Put fails from the first call, so the reclaim hook aborts on its
	// merged-blob write before it removes a single constituent.
	ic.failPut = true

	for _, d := range vecContentDocs(5) {
		require.NoError(t, dm.engine.Add([]searchengine.Document{d}))
	}
	waitForMerge(t, dm.engine.MergeCount, "count-driven merge must fire")

	rec := &recorderT{}
	waitReclaimSettledUntil(rec, 5*time.Second,
		dm.engine.MergeEligible, dm.engine.MergeCount, dm.engine.SettledMergeCount)
	require.False(t, rec.failed,
		"an ABORTED reclaim is a finished reclaim: the wait must return rather than spend its deadline: %v", rec.msgs)

	// THE DISCRIMINATING ASSERTIONS: the abort really happened (nothing was
	// removed, which is the crash-safe promise), and the merge really settled.
	require.Empty(t, ic.removedSet(),
		"a failed Put must abort the reclaim before any constituent is removed")
	require.Equal(t, dm.engine.MergeCount(), dm.engine.SettledMergeCount(),
		"and the engine must count the aborted hook's return as settled")
	require.GreaterOrEqual(t, dm.engine.MergeCount(), uint64(1), "fixture: a merge must have published")
}
