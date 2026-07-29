// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// panicScanner panics on drain, exercising the wrapper's recover path: the async
// goroutine must turn a panicking pass into a completion error, still release the
// guard, and still drive onComplete (degrade, don't die).
type panicScanner struct{}

func (panicScanner) PipelineScan(_ context.Context, _ *knowledgev1.PipelineScanRequest) (*knowledgev1.PipelineScanResponse, error) {
	panic("boom")
}

// TestStartSimilarityPass_CoalesceNoSpawn: when the guard is already held, the
// wrapper returns started=false WITHOUT spawning and invokes NEITHER callback — so
// a coalesced trigger creates no orphan running event. Subsumes the deleted
// TestRunSimilarityPass_Coalesce at the new seam.
func TestStartSimilarityPass_CoalesceNoSpawn(t *testing.T) {
	p := newLeverLoop(&leverFakeCaller{}, &countingScanner{}, nil)

	// Hold the guard to simulate an in-flight reflection tick.
	release, ok := AcquireReflectionPass(ReflectionPassKey)
	require.True(t, ok)
	defer release()

	var onStartedCalled, onCompleteCalled bool
	started := p.StartSimilarityPass(0, 0, DensifyParams{}, func() { onStartedCalled = true },
		func(context.Context, SimilarityReport, error) { onCompleteCalled = true })
	assert.False(t, started, "a held guard must coalesce → started=false")
	assert.False(t, onStartedCalled, "onStarted must NOT fire on a coalesce (no orphan running event)")
	assert.False(t, onCompleteCalled, "onComplete must NOT fire on a coalesce")
}

// TestStartSimilarityPass_StartsAndReleases: on an uncontended acquire the wrapper
// returns started=true, invokes onStarted BEFORE returning, the goroutine invokes
// onComplete with the report, and the guard is released after completion (a fresh
// AcquireReflectionPass succeeds).
func TestStartSimilarityPass_StartsAndReleases(t *testing.T) {
	gc := &leverFakeCaller{thoughts: []*knowledgev1.Node{leverThought("t1", "c1")}}
	scanner := &fakeDrainScanner{pages: [][]*knowledgev1.PipelineScanItem{{item("t1", bitVec(0))}}}
	clusters := []ThoughtCluster{{ID: "c1", ThoughtIDs: []string{"t1"}, Size: 1}}
	p := newLeverLoop(gc, scanner, clusters)

	var onStartedBeforeReturn bool
	done := make(chan SimilarityReport, 1)

	started := p.StartSimilarityPass(0, 0, DensifyParams{}, func() { onStartedBeforeReturn = true },
		func(_ context.Context, rep SimilarityReport, _ error) { done <- rep })
	require.True(t, started, "an uncontended acquire must start the pass")
	assert.True(t, onStartedBeforeReturn, "onStarted must run synchronously before StartSimilarityPass returns")

	select {
	case rep := <-done:
		assert.InDelta(t, similarityLinkThresholdDefault, rep.LinkThreshold, 1e-9, "onComplete receives the report")
	case <-time.After(5 * time.Second):
		t.Fatal("onComplete was not invoked within the deadline")
	}

	// onComplete fires from inside the async goroutine BEFORE its deferred guard
	// release() runs, so re-acquiring immediately after <-done races the release
	// (assert-too-early flake). Stop() calls inFlight.Wait(), draining the goroutine
	// and its deferred release before we re-acquire. Same drain idiom as
	// TestStartSimilarityPass_StopDrains.
	p.Stop(5 * time.Second)

	// The guard is free after completion: a fresh acquire succeeds.
	release, ok := AcquireReflectionPass(ReflectionPassKey)
	require.True(t, ok, "the guard must be released after the async pass completes")
	release()
}

// TestStartSimilarityPass_StopDrains: Stop() waits for the async pass. A pass with a
// blocking onComplete is started; Stop(deadline) must block until the callback
// returns, proving the goroutine's inFlight bracket covers onComplete persistence.
func TestStartSimilarityPass_StopDrains(t *testing.T) {
	gc := &leverFakeCaller{thoughts: []*knowledgev1.Node{leverThought("t1", "c1")}}
	scanner := &fakeDrainScanner{pages: [][]*knowledgev1.PipelineScanItem{{item("t1", bitVec(0))}}}
	clusters := []ThoughtCluster{{ID: "c1", ThoughtIDs: []string{"t1"}, Size: 1}}
	p := newLeverLoop(gc, scanner, clusters)

	releaseComplete := make(chan struct{})
	var completeReturned bool
	var mu sync.Mutex

	started := p.StartSimilarityPass(0, 0, DensifyParams{}, nil,
		func(context.Context, SimilarityReport, error) {
			<-releaseComplete // block the callback until the test unblocks it
			mu.Lock()
			completeReturned = true
			mu.Unlock()
		})
	require.True(t, started)

	stopReturned := make(chan struct{})
	go func() {
		p.Stop(5 * time.Second)
		close(stopReturned)
	}()

	// Stop must NOT return while onComplete is still blocked.
	select {
	case <-stopReturned:
		t.Fatal("Stop returned before the blocking onComplete finished — inFlight bracket does not cover onComplete")
	case <-time.After(100 * time.Millisecond):
	}

	close(releaseComplete) // let onComplete finish
	select {
	case <-stopReturned:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not return after onComplete finished")
	}
	mu.Lock()
	assert.True(t, completeReturned, "onComplete must have returned before Stop drained")
	mu.Unlock()
}

// TestStartSimilarityPass_ReleasesOnError (FAILS-WHEN-ABSENT): the guard is released
// even when the pass FAILS — here a panicking scanner forces the worker to panic.
// The wrapper recovers the panic into a completion error, releases the guard via
// defer, and still drives onComplete; afterward a fresh AcquireReflectionPass
// succeeds, proving the guard is free on the failure path.
func TestStartSimilarityPass_ReleasesOnError(t *testing.T) {
	gc := &leverFakeCaller{thoughts: []*knowledgev1.Node{leverThought("t1", "c1")}}
	clusters := []ThoughtCluster{{ID: "c1", ThoughtIDs: []string{"t1"}, Size: 1}}
	p := newLeverLoop(gc, panicScanner{}, clusters)

	done := make(chan error, 1)
	started := p.StartSimilarityPass(0, 0, DensifyParams{}, nil,
		func(_ context.Context, _ SimilarityReport, err error) { done <- err })
	require.True(t, started)

	select {
	case err := <-done:
		require.Error(t, err, "a panicking pass must surface as a completion error, not a silent success")
	case <-time.After(5 * time.Second):
		t.Fatal("onComplete was not invoked after the pass panicked")
	}

	// onComplete fires from inside the async goroutine BEFORE its deferred guard
	// release() runs, so re-acquiring immediately after <-done races the release
	// (assert-too-early flake). Stop() calls inFlight.Wait(), draining the goroutine
	// and its deferred release before we re-acquire. Same drain idiom as
	// TestStartSimilarityPass_StopDrains.
	p.Stop(5 * time.Second)

	// The guard must be free despite the panic.
	release, ok := AcquireReflectionPass(ReflectionPassKey)
	require.True(t, ok, "the guard must be released even when the pass panics")
	release()
}
