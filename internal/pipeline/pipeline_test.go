// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"testing"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/llmproviders"
)

// TestPipeline_StopWithFullChannel exercises the 7-step shutdown ordering.
// Stop cancels the collector context first (Step 1), waits collectors
// (Step 2), THEN closes channels (Step 3), THEN waits dispatchers (Step 4),
// THEN closes batch channels (Step 5), THEN waits workers (Step 6).
//
// Asserts Stop returns nil within the 5s deadline.
func TestPipeline_StopWithFullChannel(t *testing.T) {
	cfg := Config{
		SummaryChannelSize: 1,
		SummaryBatchSize:   1,
		SummaryWorkers:     1,
		EmbedChannelSize:   1,
		EmbedBatchSize:     1,
		EmbedWorkers:       1,
		Tick:               10 * time.Millisecond,
	}
	noopSum := func(_ context.Context, _ []llmproviders.BatchChunk) (map[string]llmproviders.SummarizeResult, error) {
		return nil, nil
	}
	noopEmb := func(_ context.Context, _ []EmbedItem) (map[string][]byte, error) {
		return nil, nil
	}
	wc := newFakeWireClient()
	p := New(cfg, wc, noopSum, noopEmb)

	if err := p.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Register a real collector via the public API. Its run loop ticks
	// every 10ms; with the fake returning no scan items it pushes nothing.
	p.RegisterGraph(context.Background(), kgtypes.GraphCode, "synthetic")

	// Let the collector tick at least once.
	time.Sleep(20 * time.Millisecond)

	stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	start := time.Now()
	if err := p.Stop(stopCtx); err != nil {
		t.Errorf("Stop returned %v after %v (expected nil before 5s deadline)", err, time.Since(start))
	}
	elapsed := time.Since(start)
	if elapsed > 4*time.Second {
		t.Errorf("Stop took %v — expected near-instant after collector cancel released ticker", elapsed)
	}
}

// TestRegisterGraph_NonEligibleGraphIdles locks the Option-B
// behavior: the client carries NO graph-type eligibility gate, so
// RegisterGraph spawns collectors for EVERY loaded graph — including a
// non-eligible one (web/logs/pdf/linkage). The non-eligible graph is
// idle-cheap: its collectors poll pipeline_scan (proving they actually
// spawned, not gated away), the server short-circuit returns empty items,
// and so NO summary/embed work is produced and NO writeback (mutate) RPC
// ever fires. The fakeWireClient's default PipelineScan response (empty
// items, dirty_gen=0) IS the server short-circuit for a non-eligible
// graph type — exactly what NodeIDsBySummaryGap/ByEmbedGap return for it.
//
// This is the converted home of the old client-side eligibility assertion
// that lived in collector/web/integration_single_test.go (which called
// GraphWebRaw.Summarizable()/Embeddable() — now zero such client calls).
func TestRegisterGraph_NonEligibleGraphIdles(t *testing.T) {
	cfg := Config{
		SummaryChannelSize: 4,
		SummaryBatchSize:   1,
		SummaryWorkers:     1,
		EmbedChannelSize:   4,
		EmbedBatchSize:     1,
		EmbedWorkers:       1,
		Tick:               5 * time.Millisecond,
	}
	noopSum := func(_ context.Context, _ []llmproviders.BatchChunk) (map[string]llmproviders.SummarizeResult, error) {
		return nil, nil
	}
	noopEmb := func(_ context.Context, _ []EmbedItem) (map[string][]byte, error) {
		return nil, nil
	}
	wc := newFakeWireClient()
	p := New(cfg, wc, noopSum, noopEmb)
	if err := p.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Register a NON-ELIGIBLE graph type. Previously the eligibility gate
	// would have made this a no-op (no collector spawned at all). Now it
	// spawns collectors that poll-and-idle.
	p.RegisterGraph(context.Background(), kgtypes.GraphWebRaw, "test-source")

	// Let both collector loops tick several times.
	time.Sleep(40 * time.Millisecond)

	stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := p.Stop(stopCtx); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	wc.mu.Lock()
	scans := wc.calls["pipeline_scan"]
	mutates := wc.calls["mutate"]
	wc.mu.Unlock()

	// Collectors actually spawned and polled (NOT gated away): the summary +
	// embed loops each issued at least one pipeline_scan.
	if scans == 0 {
		t.Errorf("expected the non-eligible graph's collectors to spawn and poll pipeline_scan, got 0 scans (gate not removed?)")
	}
	// Idle-cheap: the server short-circuit returns empty, so NO summary/embed
	// work was produced and NO writeback RPC ever fired.
	if mutates != 0 {
		t.Errorf("non-eligible graph produced %d writeback (mutate) RPCs; expected 0 (no work should flow)", mutates)
	}
	m := p.Metrics()
	if m.SummaryQueued != 0 || m.EmbedQueued != 0 {
		t.Errorf("non-eligible graph queued work: summary=%d embed=%d; expected 0/0 (server short-circuit yields empty)", m.SummaryQueued, m.EmbedQueued)
	}
}

// TestEnqueueIDs_PushesBothAxesRegardlessOfGraphType locks the
// Option-B EnqueueIDs behavior: with the eligibility gate removed, every id
// is pushed onto BOTH the summary and embed channels regardless of the
// graph's eligibility. The off-axis work is discarded server-side. Previously
// a non-eligible graph would early-return (zero pushes); now both channels
// receive the id. Workers are NOT started here so the pushed work stays
// queued for the depth assertion.
func TestEnqueueIDs_PushesBothAxesRegardlessOfGraphType(t *testing.T) {
	cfg := Config{
		SummaryChannelSize: 8,
		EmbedChannelSize:   8,
	}
	noopSum := func(_ context.Context, _ []llmproviders.BatchChunk) (map[string]llmproviders.SummarizeResult, error) {
		return nil, nil
	}
	noopEmb := func(_ context.Context, _ []EmbedItem) (map[string][]byte, error) {
		return nil, nil
	}
	p := New(cfg, newFakeWireClient(), noopSum, noopEmb)

	// A non-eligible graph type — this previously early-returned with zero
	// pushes. Note: Start is NOT called, so no dispatcher drains the channels;
	// the pushed work stays queued for the Metrics() depth read.
	p.EnqueueIDs(kgtypes.GraphWebRaw, "test-source", []string{"n1", "n2"})

	m := p.Metrics()
	if m.SummaryQueued != 2 {
		t.Errorf("EnqueueIDs must push every id onto the summary channel regardless of eligibility; got SummaryQueued=%d, want 2", m.SummaryQueued)
	}
	if m.EmbedQueued != 2 {
		t.Errorf("EnqueueIDs must push every id onto the embed channel regardless of eligibility; got EmbedQueued=%d, want 2", m.EmbedQueued)
	}
}

// TestPipeline_StopIdempotent confirms double-Stop does not panic.
func TestPipeline_StopIdempotent(t *testing.T) {
	noopSum := func(_ context.Context, _ []llmproviders.BatchChunk) (map[string]llmproviders.SummarizeResult, error) {
		return nil, nil
	}
	noopEmb := func(_ context.Context, _ []EmbedItem) (map[string][]byte, error) {
		return nil, nil
	}
	wc := newFakeWireClient()
	p := New(Config{}, wc, noopSum, noopEmb)
	if err := p.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	ctx := context.Background()
	if err := p.Stop(ctx); err != nil {
		t.Errorf("first Stop returned %v, want nil", err)
	}
	if err := p.Stop(ctx); err != nil {
		t.Errorf("second Stop returned %v, want nil (idempotent via stopOnce)", err)
	}
}

// TestPipeline_PipelineStatusPerAxis verifies the per-axis status aggregation: a
// SUMMARY-only auto-trip makes PipelineStatus() report aggregate Paused=true with
// the summary sub-state paused and the embed sub-state RUNNING (independent), and
// an all-clear pipeline reports Paused=false on both sub-states.
func TestPipeline_PipelineStatusPerAxis(t *testing.T) {
	ctx := context.Background()
	wc := newFakeWireClient()
	// Distinct providers so the summary trip does NOT cross-trip embed (the
	// anthropic+voyage shape) — isolates the per-axis status aggregation from
	// escalation.
	fs := &fakeSummarizer{err: errTerminalNonDeterministic}
	fe := &fakeEmbedder{vectors: map[string][]byte{}}
	p := New(Config{CircuitBreakerThreshold: 3, SummaryProvider: "anthropic", EmbedProvider: "voyage"},
		wc, fs.call, fe.call)

	// All-clear: both sub-states running.
	if st := p.PipelineStatus(); st.Paused || st.Summary.Paused || st.Embed.Paused {
		t.Fatalf("fresh pipeline reports paused: %+v", st)
	}

	// Drive a summary-only auth/quota storm to auto-trip the summary axis.
	for range 3 {
		runSummaryWorkerBatch(ctx, p, []SummaryWork{summaryWork("s", `{"name":"s"}`)})
	}

	st := p.PipelineStatus()
	if !st.Paused {
		t.Fatalf("aggregate Paused = false after a summary-only auto-trip, want true")
	}
	if !st.Summary.Paused {
		t.Fatalf("summary sub-state not paused after a summary-only auto-trip")
	}
	if st.Embed.Paused {
		t.Fatalf("embed sub-state paused after a SUMMARY-only trip (distinct providers must not cross-trip)")
	}
	// The aggregate carries the summary axis's dominant class (auth/quota) — the
	// representative paused axis is summary.
	if st.DominantClass != ClassAuthQuota || st.Summary.DominantClass != ClassAuthQuota {
		t.Fatalf("dominant class not carried per axis: aggregate=%v summary=%v", st.DominantClass, st.Summary.DominantClass)
	}
}
