// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/llm"
	"github.com/fulminate-io/knowledge-mcp/internal/llmproviders"
)

// TestCircuit_SummaryFailureDoesNotBlockEmbed is the INVERSE of the old
// cross-axis coupling guard: it proves a per-axis breaker split. A SUMMARY-only
// error storm latches the SUMMARY breaker paused while the EMBED breaker stays
// running — a failing summarizer must NOT stall healthy embeddings. RED if the
// coupling returns (a summary storm pausing the embed axis).
func TestCircuit_SummaryFailureDoesNotBlockEmbed(t *testing.T) {
	ctx := context.Background()
	wc := newFakeWireClient()
	// Use the NON-deterministic terminal sentinel (auth/quota): this drives the
	// class-agnostic zero-success WINDOW on the summary axis, so the errors must
	// NOT be a deterministic class (which would fast-trip at 2 before the 3-call
	// window closes). The embedder NEVER errors — the embed axis stays healthy.
	fs := &fakeSummarizer{err: errTerminalNonDeterministic}
	fe := &fakeEmbedder{vectors: map[string][]byte{}}
	// Threshold 3 so a short summary-only run trips deterministically.
	p := New(Config{CircuitBreakerThreshold: 3}, wc, fs.call, fe.call)

	// Three SUMMARY error rounds (zero summary successes) -> the summary breaker
	// trips. The embed axis is never driven and must stay open.
	for range 3 {
		runSummaryWorkerBatch(ctx, p, []SummaryWork{summaryWork("s", `{"name":"s"}`)})
	}

	sumSt := p.summaryCircuit.status()
	if !sumSt.Paused {
		t.Fatalf("summary breaker did not trip after a full 3-call summary error round")
	}
	if p.embedCircuit.status().Paused {
		t.Fatalf("embed breaker tripped on a SUMMARY-only storm — the per-axis coupling regressed")
	}

	// The embed axis is independent: an embed worker's waitResumed must return
	// immediately (embed work flows while the summary axis is latched paused).
	embedFlows := make(chan struct{})
	go func() { p.embedCircuit.waitResumed(ctx); close(embedFlows) }()
	select {
	case <-embedFlows:
	case <-time.After(time.Second):
		t.Fatalf("embed axis waitResumed blocked while only the summary axis was paused")
	}

	// A summary worker, by contrast, IS parked until the summary axis resumes.
	summaryParked := make(chan struct{})
	go func() { p.summaryCircuit.waitResumed(ctx); close(summaryParked) }()
	select {
	case <-summaryParked:
		t.Fatalf("summary axis waitResumed returned while the summary axis was paused")
	case <-time.After(30 * time.Millisecond):
	}

	p.summaryCircuit.resume()
	select {
	case <-summaryParked:
	case <-time.After(time.Second):
		t.Fatalf("summary waitResumed did not return after resume")
	}
}

// TestCircuit_IsolatedSummaryFailureAmidSummarySuccessNeverTrips drives the real
// summary wiring with interleaved summary FAILURES and summary SUCCESSES on the
// same (summary) axis: each summary success zeroes that axis's OWN counter, so
// the interleaved summary failures can never reach the threshold. Per-axis: the
// reset comes from a success on the SAME axis (not a cross-axis embed success) —
// the embed axis is independent and not exercised here.
func TestCircuit_IsolatedSummaryFailureAmidSummarySuccessNeverTrips(t *testing.T) {
	ctx := context.Background()
	wc := newFakeWireClient()
	// Non-deterministic terminal errors (http_401/ClassAuthQuota): two consecutive
	// failures must NOT fast-trip, so a deterministic class is wrong here. The errs
	// sequence is [fail, fail, success] repeated, so every third summary call
	// succeeds and resets THIS axis's counter before it climbs to the threshold (3).
	const rounds = 20
	errs := make([]error, 0, rounds*3)
	for range rounds {
		errs = append(errs, errTerminalNonDeterministic, errTerminalNonDeterministic, nil)
	}
	fs := &fakeSummarizer{errs: errs, results: map[string]llmproviders.SummarizeResult{}}
	fe := &fakeEmbedder{vectors: map[string][]byte{}}
	p := New(Config{CircuitBreakerThreshold: 3}, wc, fs.call, fe.call)

	for i := range rounds {
		// Two summary failures...
		runSummaryWorkerBatch(ctx, p, []SummaryWork{summaryWork("s", `{"name":"s"}`)})
		runSummaryWorkerBatch(ctx, p, []SummaryWork{summaryWork("s", `{"name":"s"}`)})
		// ...then one summary SUCCESS resets THIS axis's counter.
		runSummaryWorkerBatch(ctx, p, []SummaryWork{summaryWork("s", `{"name":"s"}`)})
		if p.summaryCircuit.status().Paused {
			t.Fatalf("summary breaker tripped at iteration %d despite an intervening summary success", i)
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
			c.recordErr(ClassOther)
			c.recordErr(ClassOther) // trips at threshold 2
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

// TestCircuit_DeterministicParseStormFastTripsBoundedCalls drives the REAL
// summary worker path with a summarizer that always returns a parse-class
// terminal error and the DEFAULT thresholds (window 20, fast-trip 2). The
// load-bearing assertion: the breaker latches paused after exactly
// DefaultDeterministicFastTripThreshold (2) batches AND fs.calls.load() == 2 —
// NOT a full 20-call billed round. RED if a full round of billed calls happened
// (a class-agnostic 20-window would let fs.calls reach 20 before tripping).
func TestCircuit_DeterministicParseStormFastTripsBoundedCalls(t *testing.T) {
	ctx := context.Background()
	wc := newFakeWireClient()
	fs := &fakeSummarizer{err: &llm.LLMError{Reason: "parse_summaries_json"}}
	fe := &fakeEmbedder{vectors: map[string][]byte{}}
	p := New(Config{}, wc, fs.call, fe.call) // default thresholds

	batches := driveUntilPausedSummary(ctx, p, "s")
	if !p.summaryCircuit.status().Paused {
		t.Fatalf("breaker did not fast-trip on a parse storm within the bound")
	}
	if batches != DefaultDeterministicFastTripThreshold {
		t.Fatalf("fast-trip after %d batches, want %d", batches, DefaultDeterministicFastTripThreshold)
	}
	if got := fs.calls.load(); got != DefaultDeterministicFastTripThreshold {
		t.Fatalf("summarizer billed calls = %d, want %d (a full round of billed calls happened)", got, DefaultDeterministicFastTripThreshold)
	}
}

// TestCircuit_DeterministicEmbedParseStormFastTripsBoundedCalls is the embed-axis
// mirror: handleEmbedderError is the sibling recordErr site, so a parse-class
// embed storm fast-trips identically with fe.calls.load() bounded to the
// threshold (not a full 20-call round).
func TestCircuit_DeterministicEmbedParseStormFastTripsBoundedCalls(t *testing.T) {
	ctx := context.Background()
	wc := newFakeWireClient()
	fs := &fakeSummarizer{}
	fe := &fakeEmbedder{err: &llm.LLMError{Reason: "parse_summaries_json"}}
	p := New(Config{}, wc, fs.call, fe.call)

	batches := driveUntilPausedEmbed(ctx, p, "e")
	if !p.embedCircuit.status().Paused {
		t.Fatalf("breaker did not fast-trip on an embed parse storm within the bound")
	}
	if batches != DefaultDeterministicFastTripThreshold {
		t.Fatalf("fast-trip after %d embed batches, want %d", batches, DefaultDeterministicFastTripThreshold)
	}
	if got := fe.calls.load(); got != DefaultDeterministicFastTripThreshold {
		t.Fatalf("embedder billed calls = %d, want %d (a full round of billed calls happened)", got, DefaultDeterministicFastTripThreshold)
	}
}

// TestCircuit_DeterministicTruncationStormFastTripsBoundedCalls drives the real
// summary worker path with a truncation-class terminal error (the orchestrator
// ruling that truncation is deterministic-terminal). The truncation storm is the
// operator-actionable burn this ticket stops: it fast-trips after the threshold
// with fs.calls bounded to it, not a full 20-call round.
func TestCircuit_DeterministicTruncationStormFastTripsBoundedCalls(t *testing.T) {
	ctx := context.Background()
	wc := newFakeWireClient()
	fs := &fakeSummarizer{err: &llm.LLMError{Reason: "response_truncated"}}
	fe := &fakeEmbedder{vectors: map[string][]byte{}}
	p := New(Config{}, wc, fs.call, fe.call)

	batches := driveUntilPausedSummary(ctx, p, "s")
	if !p.summaryCircuit.status().Paused {
		t.Fatalf("breaker did not fast-trip on a truncation storm within the bound")
	}
	if batches != DefaultDeterministicFastTripThreshold {
		t.Fatalf("fast-trip after %d batches, want %d", batches, DefaultDeterministicFastTripThreshold)
	}
	if got := fs.calls.load(); got != DefaultDeterministicFastTripThreshold {
		t.Fatalf("summarizer billed calls = %d, want %d on a truncation storm", got, DefaultDeterministicFastTripThreshold)
	}
}

// TestCircuit_TransientInterleaveDoesNotFastTrip drives the real summary worker
// path alternating a parse (deterministic) error and a TRANSIENT error so the
// deterministic streak keeps resetting — the breaker must NOT fast-trip across
// several batches. The interleaver is a transient *llm.LLMError whose Reason
// ("http_429") classifies to a NON-deterministic class (ClassAuthQuota); it must
// NOT be errTerminal, whose "http_400" Reason maps to ClassInvalidRequest (a
// deterministic class that would wrongly exercise the same-class streak path).
func TestCircuit_TransientInterleaveDoesNotFastTrip(t *testing.T) {
	ctx := context.Background()
	wc := newFakeWireClient()
	transientInterleaver := &llm.LLMError{Transient: true, Reason: "http_429"}
	fs := &fakeSummarizer{errs: []error{
		&llm.LLMError{Reason: "parse_summaries_json"},
		transientInterleaver,
		&llm.LLMError{Reason: "parse_summaries_json"},
		transientInterleaver,
		&llm.LLMError{Reason: "parse_summaries_json"},
		transientInterleaver,
	}}
	fe := &fakeEmbedder{vectors: map[string][]byte{}}
	p := New(Config{}, wc, fs.call, fe.call)

	for range 6 {
		runSummaryWorkerBatch(ctx, p, []SummaryWork{summaryWork("s", `{"name":"s"}`)})
		if p.summaryCircuit.status().Paused {
			t.Fatalf("breaker fast-tripped despite a transient interleave resetting the deterministic streak")
		}
	}
}

// driveUntilPausedSummary runs summary worker batches until the breaker latches
// paused (or a safety bound is hit) and returns the number of batches driven. It
// stops the moment the breaker pauses so no batch parks in waitResumed.
func driveUntilPausedSummary(ctx context.Context, p *Pipeline, id string) int {
	const safety = DefaultCircuitBreakerThreshold + 5
	for i := 1; i <= safety; i++ {
		runSummaryWorkerBatch(ctx, p, []SummaryWork{summaryWork(id, `{"name":"`+id+`"}`)})
		if p.summaryCircuit.status().Paused {
			return i
		}
	}
	return safety
}

// driveUntilPausedEmbed is the embed-axis sibling of driveUntilPausedSummary.
func driveUntilPausedEmbed(ctx context.Context, p *Pipeline, id string) int {
	const safety = DefaultCircuitBreakerThreshold + 5
	for i := 1; i <= safety; i++ {
		runEmbedWorkerBatch(ctx, p, []EmbedWork{embedWork(id, "embed me round")})
		if p.embedCircuit.status().Paused {
			return i
		}
	}
	return safety
}

// oldGenericReason is the FIXED pause string the breaker stamped before the
// dominant-class change. The three fails-when-absent tests below assert the
// reason now NAMES the real dominant error class — and explicitly that it is NOT
// this old generic string, so each test goes RED if the dominant-class reason
// regresses to the fixed text.
const oldGenericReason = "full error round (quota/auth or repeated timeouts)"

// TestCircuit_TimeoutRoundNamesTimeoutReason drives the real worker wiring with a
// summarizer that always returns a timeout/transport error (http_500). After a
// full zero-success round the pause reason must NAME the timeout/transport class
// with counts — not the old generic string. A NON-deterministic class is used on
// purpose: it climbs the full zero-success WINDOW (a deterministic class would
// fast-trip at 2 before the 3-call window closes — that path is covered by the
// deterministic fast-trip tests). RED if the generic reason returns.
func TestCircuit_TimeoutRoundNamesTimeoutReason(t *testing.T) {
	ctx := context.Background()
	wc := newFakeWireClient()
	fs := &fakeSummarizer{err: &llm.LLMError{Reason: "http_500"}}
	fe := &fakeEmbedder{vectors: map[string][]byte{}}
	p := New(Config{CircuitBreakerThreshold: 3}, wc, fs.call, fe.call)

	for range 3 {
		runSummaryWorkerBatch(ctx, p, []SummaryWork{summaryWork("s", `{"name":"s"}`)})
	}

	st := p.summaryCircuit.status()
	if !st.Paused {
		t.Fatalf("breaker did not trip after a full 3-call timeout error round")
	}
	if st.DominantClass != ClassTimeoutTransport {
		t.Fatalf("DominantClass = %v, want ClassTimeoutTransport", st.DominantClass)
	}
	if !strings.Contains(st.Reason, ClassTimeoutTransport.Label()) {
		t.Fatalf("reason %q does not name the timeout/transport class label %q", st.Reason, ClassTimeoutTransport.Label())
	}
	if !strings.Contains(st.Reason, "3/3") {
		t.Fatalf("reason %q does not carry the 3/3 count", st.Reason)
	}
	if strings.Contains(st.Reason, oldGenericReason) {
		t.Fatalf("reason regressed to the old generic string: %q", st.Reason)
	}
}

// TestCircuit_MixedRoundNamesDominantClass pins a concrete per-call ordering so
// that AT the trip (the 3rd errored call latches at threshold 3) the window
// holds timeout/transport=2, auth/quota=1 — timeout/transport is the strict
// majority at the trip moment. The dominant-class selection, count, and breakdown
// are all asserted. Both classes are NON-deterministic on purpose so the window
// closes at 3 (a same-class deterministic pair would fast-trip at 2, exercising a
// different path covered by the deterministic fast-trip tests). RED if dominant
// selection is wrong.
func TestCircuit_MixedRoundNamesDominantClass(t *testing.T) {
	ctx := context.Background()
	wc := newFakeWireClient()
	// errs sequenced so the window at the 3rd (latching) call is transport=2,
	// auth=1. All terminal (Transient unset) so none takes the backoff path.
	fs := &fakeSummarizer{errs: []error{
		&llm.LLMError{Reason: "http_500"},
		&llm.LLMError{Reason: "http_500"},
		&llm.LLMError{Reason: "http_401"},
	}}
	fe := &fakeEmbedder{vectors: map[string][]byte{}}
	p := New(Config{CircuitBreakerThreshold: 3}, wc, fs.call, fe.call)

	for range 3 {
		runSummaryWorkerBatch(ctx, p, []SummaryWork{summaryWork("s", `{"name":"s"}`)})
	}

	st := p.summaryCircuit.status()
	if !st.Paused {
		t.Fatalf("breaker did not trip after the 3-call mixed error round")
	}
	if st.DominantClass != ClassTimeoutTransport {
		t.Fatalf("DominantClass = %v, want ClassTimeoutTransport (transport=2 strict majority at trip)", st.DominantClass)
	}
	if st.DominantCount != 2 {
		t.Fatalf("DominantCount = %d, want 2", st.DominantCount)
	}
	if st.Breakdown == "" {
		t.Fatalf("Breakdown empty, want a per-class tally for the mixed round")
	}
	if !strings.Contains(st.Breakdown, ClassTimeoutTransport.shortLabel()) ||
		!strings.Contains(st.Breakdown, ClassAuthQuota.shortLabel()) {
		t.Fatalf("Breakdown %q does not name both timeout/transport and auth/quota", st.Breakdown)
	}
	if !strings.Contains(st.Reason, ClassTimeoutTransport.Label()) {
		t.Fatalf("reason %q does not name the dominant timeout/transport class", st.Reason)
	}
	if strings.Contains(st.Reason, oldGenericReason) {
		t.Fatalf("reason regressed to the old generic string: %q", st.Reason)
	}
}

// TestCircuit_AuthRoundNamesAuthReason drives a summarizer returning an HTTP-401
// (auth/quota) error every call. http_401 is terminal (only 429/5xx are
// transient), so it exercises the terminal path without the backoff delay while
// still classifying as ClassAuthQuota. The pause reason must name the auth/quota
// class and the structured DominantClass must be ClassAuthQuota. RED if auth
// still maps to the generic string.
func TestCircuit_AuthRoundNamesAuthReason(t *testing.T) {
	ctx := context.Background()
	wc := newFakeWireClient()
	fs := &fakeSummarizer{err: &llm.LLMError{Reason: "http_401"}}
	fe := &fakeEmbedder{vectors: map[string][]byte{}}
	p := New(Config{CircuitBreakerThreshold: 3}, wc, fs.call, fe.call)

	for range 3 {
		runSummaryWorkerBatch(ctx, p, []SummaryWork{summaryWork("s", `{"name":"s"}`)})
	}

	st := p.summaryCircuit.status()
	if !st.Paused {
		t.Fatalf("breaker did not trip after a full 3-call auth error round")
	}
	if st.DominantClass != ClassAuthQuota {
		t.Fatalf("DominantClass = %v, want ClassAuthQuota", st.DominantClass)
	}
	if !strings.Contains(st.Reason, ClassAuthQuota.Label()) {
		t.Fatalf("reason %q does not name the auth/quota class", st.Reason)
	}
	if strings.Contains(st.Reason, oldGenericReason) {
		t.Fatalf("reason regressed to the old generic string: %q", st.Reason)
	}
}
