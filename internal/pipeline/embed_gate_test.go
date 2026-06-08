// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"testing"
	"time"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/llmproviders"
)

// TestPipeline_NilEmbedder_EmbedAxisGatedOff is the regression test for the
// nil-embedder infinite loop. With NO embedder configured (the no-Voyage-key
// path: BuildEmbedder → nil → adaptEmbedder(nil) → nil EmbedderFunc), the
// ENTIRE embed axis must stay off: no embed dispatcher, no embed worker pool,
// and crucially NO per-collector runEmbedLoop. Without the gate, a collector
// re-discovers embed-eligible nodes forever, the recovered nil-func panic
// stamps no marker so the nodes stay eligible, embedCh fills, and summarization
// starves (CPU pins).
//
// The server is seeded with embed-eligible gaps (embedScanResp carries items
// with server-composed EmbedText). A running embed loop WOULD scan the embed
// axis and push them; the gate means it never starts.
//
// Asserts:
//
//	(a) NO panic (the nil EmbedderFunc is never invoked — Start/run gate it off);
//	(b) the embed axis does NO work: zero embed-axis pipeline_scan RPCs, while the
//	    summary axis DID scan (the collector + summary path keep flowing), and
//	    Pipeline.Stop returns cleanly within a short timeout (no wedge on a full
//	    embedCh, no hang on an un-started embed goroutine);
//	(c) NO embed-failure marker is stamped on any node (we leave them embed-
//	    ELIGIBLE for a later keyed run — we did NOT mark them failed).
//
// RED-BEFORE: against the un-gated code the embed loop runs, so the embed axis
// is scanned (scanCountForAxis("embed") > 0 → assertion (b) fails) and the
// nil-func embedder is reached and nil-panic-loops.
func TestPipeline_NilEmbedder_EmbedAxisGatedOff(t *testing.T) {
	cfg := Config{
		SummaryChannelSize: 4,
		SummaryBatchSize:   1,
		SummaryWorkers:     1,
		EmbedChannelSize:   2, // small: a running embed loop would fill it fast
		EmbedBatchSize:     1,
		EmbedWorkers:       2,
		Tick:               5 * time.Millisecond,
	}
	noopSum := func(_ context.Context, _ []llmproviders.BatchChunk) (map[string]llmproviders.SummarizeResult, error) {
		return nil, nil
	}
	wc := newFakeWireClient()
	// Embed-eligible gaps: an embed-axis scan would return these items with
	// server-composed EmbedText, so a running embed loop would push real
	// EmbedWork toward the (nil) embedder. The summary axis returns the default
	// empty response.
	wc.seedEmbedScan(
		&knowledgev1.PipelineScanItem{NodeId: "n1", GraphName: "myrepo", EmbedText: "embed me 1"},
		&knowledgev1.PipelineScanItem{NodeId: "n2", GraphName: "myrepo", EmbedText: "embed me 2"},
	)

	// embedder is nil — the no-Voyage-key path.
	p := New(cfg, wc, noopSum, nil)
	if err := p.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Register a graph and let the collector loops tick many times. A running
	// embed loop would scan the embed axis repeatedly within this window.
	p.RegisterGraph(context.Background(), kgtypes.GraphCode, "myrepo")
	time.Sleep(60 * time.Millisecond)

	// (b) Stop returns cleanly within a short timeout — no wedge, no hang on an
	// un-started embed goroutine.
	stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := p.Stop(stopCtx); err != nil {
		t.Fatalf("Stop did not return cleanly (wedge / un-started-goroutine hang?): %v", err)
	}

	// (b) The embed axis was NEVER scanned (runEmbedLoop never started) while the
	// summary axis DID flow.
	if got := wc.scanCountForAxis("embed"); got != 0 {
		t.Errorf("embed axis scanned %d times; want 0 (runEmbedLoop must not start with no embedder)", got)
	}
	if got := wc.scanCountForAxis("summary"); got == 0 {
		t.Errorf("summary axis scanned 0 times; want > 0 (the summary/collector path must keep flowing)")
	}

	// (b) Nothing wedged in the embed channel.
	if m := p.Metrics(); m.EmbedQueued != 0 {
		t.Errorf("EmbedQueued = %d; want 0 (no embed work should ever be pushed)", m.EmbedQueued)
	}

	// (c) NO embed-failure marker was stamped on any node — they stay embed-
	// ELIGIBLE for a future keyed run. No mutate writeback fired at all.
	for _, batchItems := range wc.recordedWrites {
		for _, it := range batchItems {
			if reason, ok := it.Metadata[kgtypes.MetaKeyEmbedFailureReason]; ok && reason != "" {
				t.Errorf("node %q got embed-failure marker %q; want NONE (no-embedder must leave nodes eligible, not failed)", it.ID, reason)
			}
		}
	}
	if m := p.Metrics(); m.EmbedFailed != 0 {
		t.Errorf("EmbedFailed = %d; want 0 (no node should be marked embed-failed for the no-embedder case)", m.EmbedFailed)
	}
}

// TestEmbedWorker_PanicStampsTerminalMarker is the regression test for the
// general panic-loop class behind the nil-embedder instance: a recovered panic
// in runEmbedWorkerBatch that wrote NO durable marker leaves the batch's nodes
// embed-eligible, so the collector re-discovers + re-enqueues them and the SAME
// batch panics again forever. The recover path now routes every node of the
// panicked batch to the terminal-marker path (markPanickedEmbedBatch →
// markEmbedItemsWithReason) so the eligibility loop terminates.
//
// Distinct from the nil-embedder case: here an embedder IS present (non-nil) but
// PROCESSING panics — the deterministic-code-bug case that must terminate, not
// stay eligible.
//
// Asserts:
//
//	(a) the panic is recovered (no crash);
//	(b) every node in the batch gets MetaKeyEmbedFailureReason stamped (proving
//	    no re-discovery/loop), via the writeBatchUpdates spy on the fake client;
//	(c) in-flight is released (the release defer runs on panic).
//
// RED-BEFORE: against the old recover handler (log-only) no marker is written,
// so assertion (b) fails.
func TestEmbedWorker_PanicStampsTerminalMarker(t *testing.T) {
	ctx := context.Background()
	wc := newFakeWireClient()
	panicEmb := func(_ context.Context, _ []EmbedItem) (map[string][]byte, error) {
		panic("synthetic embed panic")
	}
	p := New(Config{}, wc, nil, panicEmb)

	// A buffered release channel + the worker's release defer: on panic the
	// release defer (registered after the recover defer → runs first, LIFO) must
	// still free in-flight.
	release := make(chan string, 4)
	batch := []EmbedWork{
		{GraphType: kgtypes.GraphCode, GraphName: "myrepo", NodeID: "p1", EmbedText: "real text 1", Release: release},
		{GraphType: kgtypes.GraphCode, GraphName: "myrepo", NodeID: "p2", EmbedText: "real text 2", Release: release},
	}

	// (a) recovered — no crash.
	runEmbedWorkerBatch(ctx, p, batch)

	// (c) in-flight released for every node (the release defer ran on panic).
	released := map[string]bool{}
	for {
		select {
		case id := <-release:
			released[id] = true
			continue
		default:
		}
		break
	}
	for _, w := range batch {
		if !released[w.NodeID] {
			t.Errorf("node %q was not released after the recovered panic (in-flight would wedge)", w.NodeID)
		}
	}

	// (b) every node got a terminal embed-failure marker stamped.
	marked := map[string]string{}
	for _, batchItems := range wc.recordedWrites {
		for _, it := range batchItems {
			if reason, ok := it.Metadata[kgtypes.MetaKeyEmbedFailureReason]; ok && reason != "" {
				marked[it.ID] = reason
			}
		}
	}
	for _, w := range batch {
		if marked[w.NodeID] == "" {
			t.Errorf("node %q got NO terminal marker after a recovered panic — the eligibility loop would recur", w.NodeID)
		}
	}
	if m := p.Metrics(); m.EmbedFailed < int64(len(batch)) {
		t.Errorf("EmbedFailed = %d; want >= %d (every panicked node counts as a terminal failure)", m.EmbedFailed, len(batch))
	}
}

// TestPipeline_NilSummarizer_SummaryAxisGatedOff is the symmetric regression
// test for the nil-summarizer loop (the bug-report case: a nil summarizer wired
// alongside a non-nil embedder). With NO summarizer configured the ENTIRE
// summary axis must stay off: no summary dispatcher, no summary worker pool, and
// NO per-collector runSummaryLoop. Without the gate the collector re-discovers
// summary-eligible nodes forever and runSummaryWorker nil-panic-loops.
//
// The embed axis IS enabled here (non-nil embedder) — proving the gates are
// per-axis: summary off, embed on. The server is seeded with summary-eligible
// gaps on the summary axis only.
//
// Asserts:
//
//	(a) NO panic (the nil SummarizerFunc is never invoked);
//	(b) the summary axis does NO work: zero summary-axis pipeline_scan RPCs, while
//	    the embed axis DID scan (the embed/collector path keeps flowing), and Stop
//	    returns cleanly within a short timeout;
//	(c) NO summary-failure marker is stamped on any node (nodes stay summary-
//	    ELIGIBLE for a later configured run — NOT marked failed).
//
// RED-BEFORE: against the un-gated code the summary loop runs (summary-axis
// scans > 0 → assertion (b) fails) and the nil-func summarizer nil-panic-loops.
func TestPipeline_NilSummarizer_SummaryAxisGatedOff(t *testing.T) {
	cfg := Config{
		SummaryChannelSize: 2, // small: a running summary loop would fill it fast
		SummaryBatchSize:   1,
		SummaryWorkers:     2,
		EmbedChannelSize:   4,
		EmbedBatchSize:     1,
		EmbedWorkers:       1,
		Tick:               5 * time.Millisecond,
	}
	noopEmb := func(_ context.Context, _ []EmbedItem) (map[string][]byte, error) {
		return nil, nil
	}
	wc := newFakeWireClient()
	// Summary-eligible gaps: a summary-axis scan would return these items with
	// server-composed SummarizeText, so a running summary loop would push real
	// SummaryWork toward the (nil) summarizer.
	wc.seedSummaryScan(
		&knowledgev1.PipelineScanItem{NodeId: "s1", GraphName: "myrepo", SummarizeText: `{"name":"s1"}`},
		&knowledgev1.PipelineScanItem{NodeId: "s2", GraphName: "myrepo", SummarizeText: `{"name":"s2"}`},
	)

	// summarizer is nil; embedder is non-nil — the mixed case the per-axis gate
	// must handle (summary off, embed on).
	p := New(cfg, wc, nil, noopEmb)
	if err := p.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	p.RegisterGraph(context.Background(), kgtypes.GraphCode, "myrepo")
	time.Sleep(60 * time.Millisecond)

	// (b) Stop returns cleanly within a short timeout.
	stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := p.Stop(stopCtx); err != nil {
		t.Fatalf("Stop did not return cleanly (wedge / un-started-goroutine hang?): %v", err)
	}

	// (b) The summary axis was NEVER scanned (runSummaryLoop never started) while
	// the embed axis DID flow.
	if got := wc.scanCountForAxis("summary"); got != 0 {
		t.Errorf("summary axis scanned %d times; want 0 (runSummaryLoop must not start with no summarizer)", got)
	}
	if got := wc.scanCountForAxis("embed"); got == 0 {
		t.Errorf("embed axis scanned 0 times; want > 0 (the embed/collector path must keep flowing)")
	}
	if m := p.Metrics(); m.SummaryQueued != 0 {
		t.Errorf("SummaryQueued = %d; want 0 (no summary work should ever be pushed)", m.SummaryQueued)
	}

	// (c) NO summary-failure marker was stamped — nodes stay summary-ELIGIBLE.
	for _, batchItems := range wc.recordedWrites {
		for _, it := range batchItems {
			if reason, ok := it.Metadata[kgtypes.MetaKeySummaryFailureReason]; ok && reason != "" {
				t.Errorf("node %q got summary-failure marker %q; want NONE (no-summarizer must leave nodes eligible)", it.ID, reason)
			}
		}
	}
	if m := p.Metrics(); m.SummaryFailed != 0 {
		t.Errorf("SummaryFailed = %d; want 0 (no node should be marked summary-failed for the no-summarizer case)", m.SummaryFailed)
	}
}

// TestSummaryWorker_PanicStampsTerminalMarker is the symmetric regression test
// for the summary-axis panic-loop class: a recovered panic in
// runSummaryWorkerBatch that wrote NO durable marker leaves the batch's nodes
// summary-eligible, so the collector re-discovers + re-enqueues them and the
// SAME batch panics again forever. The recover path now routes every node of the
// panicked batch to the terminal-marker path (markPanickedSummaryBatch →
// markSummaryItemsWithReason).
//
// Distinct from the nil-summarizer case: here a summarizer IS present (non-nil)
// but PROCESSING panics — the deterministic-code-bug case that must terminate.
//
// Asserts: (a) recovered, no crash; (b) MetaKeySummaryFailureReason stamped on
// every node (no loop); (c) in-flight released.
//
// RED-BEFORE: against the old recover handler (log-only) no marker is written,
// so assertion (b) fails.
func TestSummaryWorker_PanicStampsTerminalMarker(t *testing.T) {
	ctx := context.Background()
	wc := newFakeWireClient()
	panicSum := func(_ context.Context, _ []llmproviders.BatchChunk) (map[string]llmproviders.SummarizeResult, error) {
		panic("synthetic summary panic")
	}
	p := New(Config{}, wc, panicSum, nil)

	release := make(chan string, 4)
	batch := []SummaryWork{
		{GraphType: kgtypes.GraphCode, GraphName: "myrepo", NodeID: "q1", SummarizeText: `{"name":"q1"}`, Release: release},
		{GraphType: kgtypes.GraphCode, GraphName: "myrepo", NodeID: "q2", SummarizeText: `{"name":"q2"}`, Release: release},
	}

	// (a) recovered — no crash.
	runSummaryWorkerBatch(ctx, p, batch)

	// (c) in-flight released for every node.
	released := map[string]bool{}
	for {
		select {
		case id := <-release:
			released[id] = true
			continue
		default:
		}
		break
	}
	for _, w := range batch {
		if !released[w.NodeID] {
			t.Errorf("node %q was not released after the recovered panic (in-flight would wedge)", w.NodeID)
		}
	}

	// (b) every node got a terminal summary-failure marker stamped.
	marked := map[string]string{}
	for _, batchItems := range wc.recordedWrites {
		for _, it := range batchItems {
			if reason, ok := it.Metadata[kgtypes.MetaKeySummaryFailureReason]; ok && reason != "" {
				marked[it.ID] = reason
			}
		}
	}
	for _, w := range batch {
		if marked[w.NodeID] == "" {
			t.Errorf("node %q got NO terminal marker after a recovered panic — the eligibility loop would recur", w.NodeID)
		}
	}
	if m := p.Metrics(); m.SummaryFailed < int64(len(batch)) {
		t.Errorf("SummaryFailed = %d; want >= %d (every panicked node counts as a terminal failure)", m.SummaryFailed, len(batch))
	}
}
