// SPDX-License-Identifier: Apache-2.0

package pipeline

// stop_registration_race_test.go — Stop must be safe against a registration that
// arrives while it is shutting down.
//
// THE DEFECT. stopSequence cancels every collector from a SNAPSHOT of
// collectorCancels, replaces the map, releases the lock, and only then waits on
// collectorWG. A RegisterGraph that takes the same mutex after that release
// stores its cancel func in the fresh map — which nothing will read again — and
// enlists its goroutine on the WaitGroup Stop is about to wait on. Stop then waits
// for a goroutine it has no way to cancel, and the waiter goroutine waitWithCtx
// spawned stays blocked on wg.Wait for the life of the process.
//
// IT REACHED THE REPOSITORY'S PUSH GATE. The full-module `go test ./...` refused a
// branch on this, through an unrelated test that merely happened to admit a graph
// while stopping a pipeline: `Pipeline.Stop did not finish within 1m0s: pipeline:
// collectors did not drain: context deadline exceeded`. It reproduced at roughly
// one run in eight, which is exactly the frequency that makes a race read as an
// environment problem.
//
// THIS ROW IS DETERMINISTIC AND OWES NO PRODUCTION SEAM. The window is opened by
// the test's own wire client: the first collector's BM25 drain blocks inside
// CorpusDelta on a channel the test releases, so that collector is still enlisted
// on collectorWG no matter how promptly it was cancelled. The observable for "step
// 1 has run" is production state the test reads under the real mutex — the cleared
// collectorCancels map — rather than a hook, so nothing here exists only for the
// test.

import (
	"context"
	"errors"
	"testing"
	"time"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/workingset"
)

// blockingDeltaClient is a WireClient whose CorpusDelta parks until the test
// releases it, and announces that it has parked. It ignores ctx DELIBERATELY:
// the point is to hold a collector enlisted on collectorWG across its own
// cancellation, which is the state the race needs and which a ctx-honoring stub
// could not produce.
type blockingDeltaClient struct {
	*fakeWireClient
	entered chan struct{}
	release chan struct{}
}

func (c *blockingDeltaClient) CorpusDelta(
	_ context.Context, _ *knowledgev1.CorpusDeltaRequest,
) (*knowledgev1.CorpusDeltaResponse, error) {
	select {
	case c.entered <- struct{}{}:
	default: // the test only waits for the first entry
	}
	<-c.release
	return &knowledgev1.CorpusDeltaResponse{}, nil
}

// stopRaceBound is this row's Stop budget. It is SHORT on purpose: the claim is
// that Stop finishes, and a generous bound would let a Stop that finishes only
// because the race did not happen pass as one that cannot hang.
const stopRaceBound = 5 * time.Second

// TestStopDeclinesARegistrationThatArrivesAfterIt drives the window directly.
func TestStopDeclinesARegistrationThatArrivesAfterIt(t *testing.T) {
	const firstGraph, lateGraph = "raceRepoFirst", "raceRepoLate"
	wire := &blockingDeltaClient{
		fakeWireClient: newFakeWireClient(),
		entered:        make(chan struct{}, 1),
		release:        make(chan struct{}),
	}
	p := New(Config{Tick: time.Hour, CloudTick: time.Hour, IdleTickMax: time.Hour}, wire, nil, nil)
	// The BM25 arm needs a ship manager to be enabled at all; it is what puts the
	// first collector inside CorpusDelta.
	p.AttachSegmentManager(&fakeShipManager{})
	ws := workingset.New()
	if !ws.Admit(kgtypes.GraphCode, firstGraph, "test") {
		t.Fatal("fixture check: the working set must accept the first member")
	}
	p.AttachWorkingSet(ws)
	ctx := context.Background()
	p.refreshOnce(ctx)

	// THE FIRST COLLECTOR IS PARKED INSIDE THE WIRE CALL, so it stays enlisted on
	// collectorWG through its own cancel. Without this the WaitGroup can reach zero
	// before the injection below and the row would pass for the wrong reason.
	select {
	case <-wire.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the first collector's BM25 drain never reached CorpusDelta, so the shutdown window this " +
			"row needs was never opened")
	}

	stopCtx, cancelStop := context.WithTimeout(context.Background(), stopRaceBound)
	defer cancelStop()
	stopErr := make(chan error, 1)
	go func() { stopErr <- p.Stop(stopCtx) }()

	// STEP 1 HAS RUN when the cancel map is empty — production state, read under
	// the production mutex. It is the same edge RegisterGraph races against.
	awaitCollectorsCleared(t, p)

	// THE INJECTION: a registration arriving inside the window. Its return value is
	// asserted by TestRegisterGraphAfterStopRefuses; here the claim is only that it
	// ENLISTED NOTHING, and that half is written so this row compiles against the
	// tree that still HAS the defect — which is what let its red be an assertion a
	// reviewer can read rather than a build failure.
	//nolint:errcheck // the error arm is its own row, and this one must compile pre-fix
	p.RegisterGraph(ctx, kgtypes.GraphCode, lateGraph)
	if n := collectorCount(p); n != 0 {
		t.Errorf("a registration arriving after Stop's cancel-and-clear enlisted %d collector(s); it must "+
			"enlist none — its cancel func would go into a map Stop has already replaced, and its goroutine "+
			"onto the WaitGroup Stop is already waiting on", n)
	}

	// Release the parked collector so the ONLY thing that could still hold Stop is
	// the injected registration.
	close(wire.release)

	select {
	case err := <-stopErr:
		if err != nil {
			t.Fatalf("Stop returned %v — a registration that arrived during shutdown was enlisted, and Stop "+
				"is now waiting for a goroutine whose cancel func it threw away", err)
		}
	case <-time.After(stopRaceBound + 5*time.Second):
		t.Fatal("Stop never returned at all, not even with its own ctx error")
	}
}

// TestRegisterGraphAfterStopRefuses is the error arm: the decline is LOUD.
//
// A silent no-op would be the fallback this project forbids — the caller would
// believe it had registered a collector that does not exist, and the graph would
// go undrained with nothing saying so.
func TestRegisterGraphAfterStopRefuses(t *testing.T) {
	p := New(Config{Tick: time.Hour}, newFakeWireClient(), nil, nil)
	ctx := context.Background()
	if err := p.Stop(ctx); err != nil {
		t.Fatalf("Stop on an idle pipeline returned %v, want nil", err)
	}

	err := p.RegisterGraph(ctx, kgtypes.GraphCode, "afterStop")
	if err == nil {
		t.Fatal("RegisterGraph after Stop returned nil — a registration the pipeline will never service must " +
			"say so; a silent no-op leaves the caller believing a graph is being drained when it is not")
	}
	if !errors.Is(err, ErrPipelineStopped) {
		t.Errorf("RegisterGraph after Stop = %v, want an error matching ErrPipelineStopped so a caller can "+
			"branch on the condition rather than on a message", err)
	}
	if n := collectorCount(p); n != 0 {
		t.Errorf("the refused registration still enlisted %d collector(s), want 0", n)
	}

	// THE SAME-RUN KNOWN POSITIVE: before a Stop, the identical call registers. A
	// refusal that fired for every call would satisfy the assertions above.
	q := New(Config{Tick: time.Hour}, newFakeWireClient(), nil, nil)
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), stopRaceBound)
		defer cancel()
		_ = q.Stop(stopCtx)
	})
	if err := q.RegisterGraph(ctx, kgtypes.GraphCode, "beforeStop"); err != nil {
		t.Fatalf("KNOWN POSITIVE FAILED: RegisterGraph on a live pipeline returned %v, want nil — the "+
			"refusals above prove nothing if every registration is refused", err)
	}
	if n := collectorCount(q); n != 1 {
		t.Errorf("a live pipeline registered %d collector(s) for one graph, want 1", n)
	}
}

// collectorCount reads the live collector registry under the production mutex.
func collectorCount(p *Pipeline) int {
	p.collectorMu.Lock()
	defer p.collectorMu.Unlock()
	return len(p.collectorCancels)
}

// awaitCollectorsCleared blocks until the collector registry is empty, which is
// the observable edge for "stopSequence's step 1 has cancelled and cleared".
func awaitCollectorsCleared(t *testing.T, p *Pipeline) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if collectorCount(p) == 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("Stop never cleared the collector registry, so the shutdown window this row targets never opened")
}
