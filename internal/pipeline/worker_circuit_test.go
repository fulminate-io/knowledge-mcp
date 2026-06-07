// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"sync"
	"testing"
	"time"
)

// TestCircuit_FullErrorRoundTripsBothAxes drives the REAL worker wiring: a
// summarizer and embedder that always error. After tripThreshold errored calls
// across either axis with zero successes, the breaker latches paused and BOTH
// a summary worker's and an embed worker's next wait blocks until resume.
func TestCircuit_FullErrorRoundTripsBothAxes(t *testing.T) {
	ctx := context.Background()
	wc := newFakeWireClient()
	fs := &fakeSummarizer{err: errTerminal}
	fe := &fakeEmbedder{err: errTerminal}
	// Threshold 3 so a short run trips deterministically.
	p := New(Config{CircuitBreakerThreshold: 3}, wc, fs.call, fe.call)

	// Two summary error rounds + one embed error round = 3 errored calls,
	// zero successes -> trips. Mix axes to prove the counter is shared.
	runSummaryWorkerBatch(ctx, p, []SummaryWork{summaryWork("s1", `{"name":"s1"}`)})
	runEmbedWorkerBatch(ctx, p, []EmbedWork{embedWork("e1", "embed me one")})
	if p.circuit.status().Paused {
		t.Fatalf("breaker tripped at 2 errored calls, threshold is 3")
	}
	runSummaryWorkerBatch(ctx, p, []SummaryWork{summaryWork("s2", `{"name":"s2"}`)})

	st := p.circuit.status()
	if !st.Paused {
		t.Fatalf("breaker did not trip after a full 3-call error round")
	}
	if st.Reason != autoTripReason {
		t.Fatalf("trip reason = %q, want %q", st.Reason, autoTripReason)
	}

	// Both axes' wait sites now block until resume. Park a goroutine in each
	// axis' waitResumed and confirm neither returns while paused.
	summaryDone := make(chan struct{})
	embedDone := make(chan struct{})
	go func() { p.circuit.waitResumed(ctx); close(summaryDone) }()
	go func() { p.circuit.waitResumed(ctx); close(embedDone) }()

	select {
	case <-summaryDone:
		t.Fatalf("summary axis waitResumed returned while paused")
	case <-embedDone:
		t.Fatalf("embed axis waitResumed returned while paused")
	case <-time.After(30 * time.Millisecond):
	}

	p.circuit.resume()
	for _, ch := range []chan struct{}{summaryDone, embedDone} {
		select {
		case <-ch:
		case <-time.After(time.Second):
			t.Fatalf("waitResumed did not return after resume")
		}
	}
}

// TestCircuit_IsolatedFailureAmidSuccessNeverTrips drives the real wiring with
// a summarizer that errors on every call but an embedder that succeeds: each
// embed success zeroes the shared counter, so the interleaved summary failures
// can never reach the threshold. Mirrors the production reality that a single
// healthy axis keeps the breaker open.
func TestCircuit_IsolatedFailureAmidSuccessNeverTrips(t *testing.T) {
	ctx := context.Background()
	wc := newFakeWireClient()
	fs := &fakeSummarizer{err: errTerminal}
	// Embedder succeeds: returns a vector for the one id it is asked about.
	fe := &fakeEmbedder{vectors: map[string][]byte{}}
	p := New(Config{CircuitBreakerThreshold: 3}, wc, fs.call, fe.call)

	for i := range 20 {
		// Two summary failures...
		runSummaryWorkerBatch(ctx, p, []SummaryWork{summaryWork("s", `{"name":"s"}`)})
		runSummaryWorkerBatch(ctx, p, []SummaryWork{summaryWork("s", `{"name":"s"}`)})
		// ...then one embed success resets the shared counter.
		runEmbedWorkerBatch(ctx, p, []EmbedWork{embedWork("e", "embed me round")})
		if p.circuit.status().Paused {
			t.Fatalf("breaker tripped at iteration %d despite an intervening embed success", i)
		}
	}
}

// TestCircuit_ShutdownUnblocksParkedWorker proves Pipeline.Stop won't hang on
// the worker WaitGroup: a worker parked in waitResumed on a paused breaker
// returns promptly when its ctx is canceled.
func TestCircuit_ShutdownUnblocksParkedWorker(t *testing.T) {
	c := newCircuitBreaker(1)
	c.pause("manual pause for shutdown test")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		c.waitResumed(ctx)
		close(done)
	}()

	// Confirm it is genuinely parked.
	select {
	case <-done:
		t.Fatalf("waitResumed returned while paused and ctx live")
	case <-time.After(20 * time.Millisecond):
	}

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatalf("waitResumed did not unblock on ctx cancel — Stop would hang on workerWG")
	}
}

// TestCircuit_ConcurrentWaitThenBackoff exercises the real two-gate sequence:
// N goroutines each do waitResumed(ctx) then backoff.wait(ctx) while another
// goroutine concurrently trips, resumes, and opens/closes the backoff window.
// Asserts every goroutine eventually proceeds (no deadlock) under -race.
func TestCircuit_ConcurrentWaitThenBackoff(t *testing.T) {
	c := newCircuitBreaker(2)
	b := newErrBackoff(time.Millisecond, 5*time.Millisecond)
	ctx := t.Context()

	const workers = 12
	var proceeded sync.WaitGroup
	proceeded.Add(workers)
	for range workers {
		go func() {
			defer proceeded.Done()
			for range 30 {
				c.waitResumed(ctx)
				b.wait(ctx)
			}
		}()
	}

	// Churn the gates: trip + resume the breaker, open + close the backoff
	// window, concurrently with the workers.
	churnDone := make(chan struct{})
	go func() {
		defer close(churnDone)
		for range 200 {
			c.record(false)
			c.record(false) // trips at threshold 2
			b.fail()
			c.resume()
			b.ok()
			time.Sleep(time.Millisecond)
		}
	}()

	// Wait for all workers with a generous deadline; fail on deadlock.
	allDone := make(chan struct{})
	go func() { proceeded.Wait(); close(allDone) }()
	<-churnDone
	c.resume() // ensure no lingering latch
	select {
	case <-allDone:
	case <-time.After(5 * time.Second):
		t.Fatalf("workers deadlocked on the waitResumed->backoff sequence")
	}
}
