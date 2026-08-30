package searchengine

import (
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

// close_join_test.go gates SegmentedIndex.Close's shutdown contract: when Close
// RETURNS, the background merger has exited, so nothing it was running can still be
// running.
//
// THE DEFECT THIS EXISTS FOR. Close used to only SIGNAL — it closed e.stop and
// returned — while its doc claimed it "stops the background merge goroutine". A
// merge already past its checks ran on afterwards, and a completed merge fires
// OnMerge, which in production is segmentdist's reclaimMerged: a cache.Put that
// WRITES A FILE. An owner that closed the engine and then removed its cache
// directory could have a blob written underneath it. In the daemon that is a stray
// content-addressed blob the next boot rebuilds; in a test rooted at t.TempDir() it
// is a write into a directory being removed, which is how it surfaced —
// "TempDir RemoveAll cleanup: directory not empty" on TestReclaimConcurrency.
//
// WHY IT OBSERVES A HOOK STILL RUNNING RATHER THAN A HOOK FIRING LATE. doMerge also
// gained stop checks that ABANDON a merge once Close is signaled, so a merge held
// BEFORE those checks simply never publishes and never fires the hook at all — a
// test built that way would pass with the join removed, because the abandon alone
// suppresses the callback. It would gate the pair, not the join. This test instead
// lets the merge get PAST every check and INTO the hook, then asks whether Close
// can return while that hook is still executing. Only the join can prevent that,
// so only the join can turn it green.
//
// IT IS DETERMINISTIC IN BOTH DIRECTIONS. The hook is provably running when Close is
// called (it signals, then lingers a fixed interval). Unjoined, Close returns during
// that linger and the completion flag is still false — RED every run. Joined, Close
// cannot return until the goroutine exits, which cannot happen until the hook
// returns — GREEN every run. Neither arm depends on scheduler luck.

// TestCloseJoinsTheMergerBeforeReturning pins that Close does not return while the
// background merger is still inside an OnMerge callback.
//
// THE ASSERTION IS ABOUT THE CALLBACK, NOT ABOUT A CHANNEL, deliberately. Checking
// that a done channel is closed would test the mechanism this change happens to
// use; checking that the owner's hook cannot still be running tests the PROPERTY the
// owner depends on when it tears down the directory that hook writes into.
func TestCloseJoinsTheMergerBeforeReturning(t *testing.T) {
	const hookLinger = 250 * time.Millisecond

	var (
		hookDone atomic.Bool
		fired    atomic.Int64
	)
	hookEntered := make(chan struct{})
	var enteredOnce atomic.Bool

	e := New[mockQuery, mockStats](mockFormat{}, Options{
		MinSegmentDocs: 1,
		// Arm the count trigger so the BACKGROUND merger fires on its own — that
		// goroutine's lifetime is the subject.
		SegmentCountTarget: 1,
		DeletesPctAllowed:  0.01,
		OnMerge: func(MergeResult) {
			fired.Add(1)
			if enteredOnce.CompareAndSwap(false, true) {
				close(hookEntered)
				// Linger INSIDE the hook, so Close is called while the merge
				// goroutine is provably still executing owner code — exactly the
				// window in which the production hook is writing a file.
				time.Sleep(hookLinger)
			}
			hookDone.Store(true)
		},
	})

	// Seal several segments so the count trigger fires and the merge has constituents
	// to consolidate (OnMerge only fires when something was superseded).
	for i := range 6 {
		if err := e.Add([]Document{doc(fmt.Sprintf("close-join-%02d", i), "alpha beta")}); err != nil {
			t.Fatalf("Add: %v", err)
		}
		if err := e.Flush(); err != nil {
			t.Fatalf("Flush: %v", err)
		}
	}

	// PRECONDITION / KNOWN-POSITIVE: the hook is genuinely running. Without this the
	// test could call Close before any merge reached the callback and then assert a
	// completion flag that was never going to be set by anyone.
	select {
	case <-hookEntered:
	case <-time.After(15 * time.Second):
		t.Fatal("KNOWN-POSITIVE FAILED: OnMerge never ran, so this test never entered the window it exists to observe and proves nothing about Close")
	}

	e.Close()

	// THE GATE. Close has returned; the merge goroutine must have exited, and it
	// cannot exit while still inside the hook.
	if !hookDone.Load() {
		t.Fatal("Close returned while an OnMerge callback was STILL RUNNING on the merge goroutine — Close signaled instead of joining, so an owner that removes its cache directory after Close can have a blob written underneath it")
	}
	if fired.Load() == 0 {
		t.Fatal("KNOWN-POSITIVE FAILED: no OnMerge ever fired")
	}
}
