// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"sync"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/llmproviders"
)

// fakeSummarizer captures calls and returns canned results / errors.
// Mirrors the pre-Phase-4 fake shape; the function signature now matches
// SummarizerFunc (no db arg — the worker doesn't carry one).
type fakeSummarizer struct {
	mu      sync.Mutex
	calls   asyncCalls
	results map[string]llmproviders.SummarizeResult
	err     error
	chunks  []llmproviders.BatchChunk
}

func (f *fakeSummarizer) call(_ context.Context, chunks []llmproviders.BatchChunk) (map[string]llmproviders.SummarizeResult, error) {
	f.calls.inc()
	f.mu.Lock()
	f.chunks = append([]llmproviders.BatchChunk{}, chunks...)
	f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	return f.results, nil
}

// fakeEmbedder captures items and returns canned per-id vectors / errors.
type fakeEmbedder struct {
	mu       sync.Mutex
	calls    asyncCalls
	vectors  map[string][]byte
	err      error
	received []EmbedItem
}

func (f *fakeEmbedder) call(_ context.Context, items []EmbedItem) (map[string][]byte, error) {
	f.calls.inc()
	f.mu.Lock()
	f.received = append([]EmbedItem{}, items...)
	f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	return f.vectors, nil
}

// summaryWork builds a SummaryWork carrying server-composed text. Post-FUL-305
// the worker reads SummarizeText off the work item directly (no node re-fetch),
// so tests set the envelope text here rather than seeding a fake node.
func summaryWork(id, text string) SummaryWork {
	return SummaryWork{GraphType: kgtypes.GraphCode, GraphName: "myrepo", NodeID: id, SummarizeText: text}
}

// embedWork builds an EmbedWork carrying server-composed embed text.
func embedWork(id, text string) EmbedWork {
	return EmbedWork{GraphType: kgtypes.GraphCode, GraphName: "myrepo", NodeID: id, EmbedText: text}
}

// TestSummaryWorker_TerminalErrorWritesMarker asserts terminal *LLMError
// surfaces as a summary_failure_reason via one mutate(update_batch) RPC.
func TestSummaryWorker_TerminalErrorWritesMarker(t *testing.T) {
	ctx := context.Background()
	wc := newFakeWireClient()
	fs := &fakeSummarizer{err: errTerminal}
	p := New(Config{}, wc, fs.call, nil)

	batch := []SummaryWork{
		summaryWork("f1", `{"name":"f1"}`),
		summaryWork("f2", `{"name":"f2"}`),
	}
	runSummaryWorkerBatch(ctx, p, batch)

	if got := fs.calls.load(); got != 1 {
		t.Errorf("summarizer calls = %d, want 1", got)
	}
	if got := wc.mutateCallCount(); got != 1 {
		t.Errorf("mutate(update_batch) calls = %d, want 1", got)
	}
	if got := wc.totalWriteItems(); got != 2 {
		t.Errorf("total items written = %d, want 2", got)
	}
	m := p.Metrics()
	if m.SummaryFailed != 2 {
		t.Errorf("SummaryFailed = %d, want 2", m.SummaryFailed)
	}
}

// TestSummaryWorker_TransientErrorNoMarker asserts transient errors do NOT
// write any mutate batch (the next collector tick retries).
func TestSummaryWorker_TransientErrorNoMarker(t *testing.T) {
	ctx := context.Background()
	wc := newFakeWireClient()
	fs := &fakeSummarizer{err: errTransient}
	p := New(Config{}, wc, fs.call, nil)

	batch := []SummaryWork{summaryWork("f1", `{"name":"f1"}`)}
	runSummaryWorkerBatch(ctx, p, batch)

	if got := wc.mutateCallCount(); got != 0 {
		t.Errorf("transient path issued mutate calls = %d, want 0", got)
	}
	m := p.Metrics()
	if m.SummaryFailed != 1 {
		t.Errorf("SummaryFailed = %d, want 1 (transient counts as failed)", m.SummaryFailed)
	}
}

// TestSummaryWorker_SuccessWritesBatch confirms the success path issues
// exactly 1 mutate(update_batch) RPC carrying every successful summary.
// Load-bearing per-batch RPC budget criterion.
func TestSummaryWorker_SuccessWritesBatch(t *testing.T) {
	ctx := context.Background()
	wc := newFakeWireClient()
	fs := &fakeSummarizer{
		results: map[string]llmproviders.SummarizeResult{
			"f1": {Summary: "summary-1", Keywords: "kw1"},
			"f2": {Summary: "summary-2", Keywords: "kw2"},
			"f3": {Summary: "summary-3", Keywords: "kw3"},
		},
	}
	p := New(Config{}, wc, fs.call, nil)

	batch := []SummaryWork{
		summaryWork("f1", `{"name":"f1"}`),
		summaryWork("f2", `{"name":"f2"}`),
		summaryWork("f3", `{"name":"f3"}`),
	}
	runSummaryWorkerBatch(ctx, p, batch)

	if got := wc.mutateCallCount(); got != 1 {
		t.Errorf("mutate(update_batch) calls = %d, want 1 (one RPC per group)", got)
	}
	sizes := wc.recordedBatchSizes()
	if len(sizes) != 1 || sizes[0] != 3 {
		t.Errorf("captured batch sizes = %v, want [3]", sizes)
	}
	m := p.Metrics()
	if m.SummarySucceeded != 3 {
		t.Errorf("SummarySucceeded = %d, want 3", m.SummarySucceeded)
	}
}

// TestSummaryWorker_BuildsChunkFromServerText pins the FUL-305 collapse: the
// worker builds one BatchChunk per work item from the SERVER-COMPOSED
// SummarizeText (chunk.Content == the work item's SummarizeText, chunk.ID ==
// NodeID), and an item with EMPTY SummarizeText is skipped defensively. The
// old client-side already-summarized race-guard + node re-fetch are gone (the
// server's ShouldSummarize gate excludes summarized nodes before they ever
// reach the worker).
func TestSummaryWorker_BuildsChunkFromServerText(t *testing.T) {
	ctx := context.Background()
	wc := newFakeWireClient()
	fs := &fakeSummarizer{
		results: map[string]llmproviders.SummarizeResult{
			"f1": {Summary: "s-1", Keywords: "k-1"},
			"f2": {Summary: "s-2", Keywords: "k-2"},
		},
	}
	p := New(Config{}, wc, fs.call, nil)

	batch := []SummaryWork{
		summaryWork("f1", `{"language":"go","type":"file","name":"f1","children":"x: y"}`),
		summaryWork("f2", `{"name":"f2"}`),
		summaryWork("f3", ""), // empty server text — skipped defensively
	}
	runSummaryWorkerBatch(ctx, p, batch)

	fs.mu.Lock()
	chunks := append([]llmproviders.BatchChunk{}, fs.chunks...)
	fs.mu.Unlock()
	if len(chunks) != 2 {
		t.Fatalf("summarizer received %d chunks, want 2 (f3 skipped for empty text)", len(chunks))
	}
	byID := map[string]string{}
	for _, c := range chunks {
		byID[c.ID] = c.Content
	}
	if byID["f1"] != `{"language":"go","type":"file","name":"f1","children":"x: y"}` {
		t.Errorf("chunk f1 Content = %q, want the server-composed envelope verbatim", byID["f1"])
	}
	if byID["f2"] != `{"name":"f2"}` {
		t.Errorf("chunk f2 Content = %q, want the server-composed envelope verbatim", byID["f2"])
	}
	if _, ok := byID["f3"]; ok {
		t.Errorf("f3 (empty SummarizeText) must NOT produce a chunk")
	}
}

// TestSummaryWorker_PerBatchRPCBudget verifies the load-bearing criterion:
// 32-item batch produces exactly 1 mutate(update_batch) RPC, NOT 32. This
// is the same invariant the server-side update_batch handler enforces;
// the client-side worker mirrors it. If this fails, the writeback path
// has silently regressed from per-batch to per-item RPCs.
func TestSummaryWorker_PerBatchRPCBudget(t *testing.T) {
	ctx := context.Background()
	wc := newFakeWireClient()
	results := make(map[string]llmproviders.SummarizeResult, 32)
	var batch []SummaryWork
	for i := range 32 {
		id := "n" + string(rune('a'+i%26)) + string(rune('0'+i/26))
		results[id] = llmproviders.SummarizeResult{Summary: "s-" + id, Keywords: "k-" + id}
		batch = append(batch, summaryWork(id, `{"name":"`+id+`"}`))
	}
	fs := &fakeSummarizer{results: results}
	p := New(Config{}, wc, fs.call, nil)

	runSummaryWorkerBatch(ctx, p, batch)

	if got := wc.mutateCallCount(); got != 1 {
		t.Errorf("mutate(update_batch) calls = %d, want 1 for 32-item batch", got)
	}
	if got := wc.totalWriteItems(); got != 32 {
		t.Errorf("total items written = %d, want 32", got)
	}
}

// TestSummaryWorker_PanicRecovery verifies a summarizer panic is recovered
// and the worker goroutine keeps going.
func TestSummaryWorker_PanicRecovery(t *testing.T) {
	ctx := context.Background()
	wc := newFakeWireClient()
	panicSum := func(_ context.Context, _ []llmproviders.BatchChunk) (map[string]llmproviders.SummarizeResult, error) {
		panic("synthetic panic")
	}
	p := New(Config{}, wc, panicSum, nil)

	batch := []SummaryWork{summaryWork("f1", `{"name":"f1"}`)}
	// Should not panic.
	runSummaryWorkerBatch(ctx, p, batch)
}

// TestEmbedWorker_UsesServerEmbedText pins the FUL-305 collapse: the worker
// feeds the embedder the SERVER-COMPOSED EmbedText off each work item verbatim
// (no node re-fetch, no client-side kgtypes EmbedText composition).
func TestEmbedWorker_UsesServerEmbedText(t *testing.T) {
	ctx := context.Background()
	wc := newFakeWireClient()
	fe := &fakeEmbedder{
		vectors: map[string][]byte{
			"f1": make([]byte, 32),
			"d1": make([]byte, 32),
		},
	}
	p := New(Config{}, wc, nil, fe.call)

	batch := []EmbedWork{
		embedWork("f1", "file summary text"),
		embedWork("d1", "d1-name\ndecision body\ndecision summary"),
	}
	runEmbedWorkerBatch(ctx, p, batch)

	if got := fe.calls.load(); got != 1 {
		t.Errorf("embedder calls = %d, want 1", got)
	}
	if len(fe.received) != 2 {
		t.Fatalf("embedder received %d items, want 2", len(fe.received))
	}
	texts := map[string]string{}
	for _, it := range fe.received {
		texts[it.ID] = it.Text
	}
	if texts["f1"] != "file summary text" {
		t.Errorf("EmbedText[f1] = %q, want the server-composed text verbatim", texts["f1"])
	}
	if texts["d1"] != "d1-name\ndecision body\ndecision summary" {
		t.Errorf("EmbedText[d1] = %q, want the server-composed text verbatim", texts["d1"])
	}
}

// TestEmbedWorker_TerminalErrorWritesMarker asserts terminal embedder
// errors write embed_failure_reason via one mutate(update_batch) RPC.
func TestEmbedWorker_TerminalErrorWritesMarker(t *testing.T) {
	ctx := context.Background()
	wc := newFakeWireClient()
	fe := &fakeEmbedder{err: errTerminal}
	p := New(Config{}, wc, nil, fe.call)

	batch := []EmbedWork{
		embedWork("f1", "summary f1"),
		embedWork("f2", "summary f2"),
	}
	runEmbedWorkerBatch(ctx, p, batch)

	if got := wc.mutateCallCount(); got != 1 {
		t.Errorf("mutate(update_batch) calls = %d, want 1 for terminal-marker batch", got)
	}
}

// TestEmbedWorker_TransientNoMarker confirms transient embed errors do NOT
// trigger any write.
func TestEmbedWorker_TransientNoMarker(t *testing.T) {
	ctx := context.Background()
	wc := newFakeWireClient()
	fe := &fakeEmbedder{err: errTransient}
	p := New(Config{}, wc, nil, fe.call)

	batch := []EmbedWork{embedWork("f1", "summary f1")}
	runEmbedWorkerBatch(ctx, p, batch)

	if got := wc.mutateCallCount(); got != 0 {
		t.Errorf("transient embed path issued mutate calls = %d, want 0", got)
	}
}

// TestEmbedWorker_PerBatchRPCBudget mirrors the summary perf test for embed.
// 32-item batch → exactly 1 mutate(update_batch) RPC.
func TestEmbedWorker_PerBatchRPCBudget(t *testing.T) {
	ctx := context.Background()
	wc := newFakeWireClient()
	vectors := make(map[string][]byte, 32)
	var batch []EmbedWork
	for i := range 32 {
		id := "e" + string(rune('a'+i%26)) + string(rune('0'+i/26))
		vectors[id] = make([]byte, 32)
		batch = append(batch, embedWork(id, "s-"+id))
	}
	fe := &fakeEmbedder{vectors: vectors}
	p := New(Config{}, wc, nil, fe.call)

	runEmbedWorkerBatch(ctx, p, batch)

	if got := wc.mutateCallCount(); got != 1 {
		t.Errorf("mutate(update_batch) calls = %d, want 1 for 32-item embed batch", got)
	}
	if got := wc.totalWriteItems(); got != 32 {
		t.Errorf("total items written = %d, want 32", got)
	}
}

// TestEmbedWorker_PanicRecovery verifies an embedder panic is recovered.
func TestEmbedWorker_PanicRecovery(t *testing.T) {
	ctx := context.Background()
	wc := newFakeWireClient()
	panicEmb := func(_ context.Context, _ []EmbedItem) (map[string][]byte, error) {
		panic("synthetic embed panic")
	}
	p := New(Config{}, wc, nil, panicEmb)
	batch := []EmbedWork{embedWork("f1", "s")}
	runEmbedWorkerBatch(ctx, p, batch)
}

// TestEmbedWorker_EmptyServerTextStampsMarker is the T3 regression test: the
// server EMITS empty-embed items (it does not drop them), and the client's
// RETAINED markStuckEmbedItems path must stamp the durable
// MetaKeyEmbedFailureReason marker on a whitespace-only server-composed
// EmbedText (and NOT send it to the embedder), while a sibling item with real
// text IS embedded. This pins the durable circuit-breaker the original plan
// regressed by deleting markStuckEmbedItems.
func TestEmbedWorker_EmptyServerTextStampsMarker(t *testing.T) {
	ctx := context.Background()
	wc := newFakeWireClient()
	fe := &fakeEmbedder{vectors: map[string][]byte{"good": make([]byte, 32)}}
	p := New(Config{}, wc, nil, fe.call)

	batch := []EmbedWork{
		embedWork("good", "real embed text"),
		embedWork("empty", "   \n  "), // whitespace-only server-composed text
	}
	runEmbedWorkerBatch(ctx, p, batch)

	// Only the good item reached the embedder.
	if len(fe.received) != 1 || fe.received[0].ID != "good" {
		t.Fatalf("embedder received %v, want exactly [good]", fe.received)
	}

	// markStuckEmbedItems fired: a writeBatchUpdates carrying the durable
	// MetaKeyEmbedFailureReason marker for the empty item. (The good item's
	// vector write is a SEPARATE mutate; we scan all captured writes for the
	// marker on the empty item.)
	foundMarker := false
	for _, batchItems := range wc.recordedWrites {
		for _, it := range batchItems {
			if it.ID == "empty" {
				if reason, ok := it.Metadata[kgtypes.MetaKeyEmbedFailureReason]; ok && reason != "" {
					foundMarker = true
				}
			}
			if it.ID == "good" {
				if reason, ok := it.Metadata[kgtypes.MetaKeyEmbedFailureReason]; ok && reason != "" {
					t.Errorf("good item must NOT carry a non-empty failure marker, got %q", reason)
				}
			}
		}
	}
	if !foundMarker {
		t.Errorf("expected markStuckEmbedItems to stamp MetaKeyEmbedFailureReason on the empty-text item")
	}

	// The embedFail metric incremented for the stuck item.
	if m := p.Metrics(); m.EmbedFailed < 1 {
		t.Errorf("EmbedFailed = %d, want >= 1 (stuck item increments embedFail)", m.EmbedFailed)
	}
}
