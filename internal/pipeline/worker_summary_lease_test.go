// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/llm"
	"github.com/fulminate-io/knowledge-mcp/internal/llmproviders"
)

// worker_summary_lease_test.go is the summary-axis twin of
// worker_embed_lease_test.go: one lease is N summarizer calls and ONE writeback.
//
// As on the embed axis, the assertion is the count of Execute calls rather than
// the count of items written, because each Execute is one acquisition of the
// graph's per-graph advisory write mutex — the quantity the convoy is made of.

// strideSummarizer records the size of every summarizer call and can fail a
// chosen one.
//
// charBudget makes it a SIZE-REFUSING provider, which is what the oversize split
// is tested against: when set, a call whose summed chunk content (plus
// overheadPerCall) exceeds the budget is refused with the typed oversize error
// carrying the budget as its limit — the shape codex-cli produces. overheadPerCall
// models the prompt scaffolding a real provider counts on top of the content, so a
// sub-batch packed to exactly the limit can still be refused and the split has to
// make progress a second way.
//
// onCall runs after each call is recorded, before its result is decided, so a
// test can perturb the world mid-split (cancel a context, for instance).
type strideSummarizer struct {
	mu              sync.Mutex
	sizes           []int
	chunkSets       [][]llmproviders.BatchChunk
	failCall        int // 1-based index of the call that errors; 0 = none
	calls           int
	charBudget      int
	overheadPerCall int
	// hideLimit refuses over the budget WITHOUT reporting it, which is the arm
	// every transport but codex-cli would take: the condition is detected, the
	// number is not supplied. Zero means unknown on the wire, so a consumer that
	// rendered or packed against the zero is caught here.
	hideLimit bool
	onCall    func(call int)
}

func (s *strideSummarizer) call(_ context.Context, chunks []llmproviders.BatchChunk) (map[string]llmproviders.SummarizeResult, error) {
	s.mu.Lock()
	s.calls++
	n := s.calls
	s.sizes = append(s.sizes, len(chunks))
	s.chunkSets = append(s.chunkSets, append([]llmproviders.BatchChunk(nil), chunks...))
	budget, overhead, hook, hide := s.charBudget, s.overheadPerCall, s.onCall, s.hideLimit
	s.mu.Unlock()
	if hook != nil {
		hook(n)
	}
	if n == s.failCall {
		return nil, errTerminalNonDeterministic
	}
	if budget > 0 {
		total := overhead
		for _, c := range chunks {
			total += len(c.Content)
		}
		if total > budget {
			reported := budget
			if hide {
				reported = 0
			}
			return nil, &llm.LLMError{
				Transient:     false,
				Reason:        llm.ReasonInputTooLarge,
				InputChars:    total,
				MaxInputChars: reported,
				Cause:         fmt.Errorf("fake provider: %d characters over the %d limit", total, budget),
			}
		}
	}
	out := make(map[string]llmproviders.SummarizeResult, len(chunks))
	for _, c := range chunks {
		out[c.ID] = llmproviders.SummarizeResult{Summary: "summary of " + c.ID, Keywords: "kw"}
	}
	return out, nil
}

func (s *strideSummarizer) callSizes() []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]int(nil), s.sizes...)
}

// sentChunks returns every chunk of every call, in call order. The no-truncation
// row compares each one's content against the fixture's own text.
func (s *strideSummarizer) sentChunks() [][]llmproviders.BatchChunk {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([][]llmproviders.BatchChunk(nil), s.chunkSets...)
}

// summaryLeaseFixture builds n SummaryWork items for ONE (graphType, graphName,
// backend) triple, each carrying server-composed text so none is skipped.
func summaryLeaseFixture(n int) []SummaryWork {
	work := make([]SummaryWork, 0, n)
	for i := range n {
		id := fmt.Sprintf("pkg/lease.go:Sum%d", i)
		work = append(work, summaryWork(id, `{"name":"`+id+`"}`))
	}
	return work
}

// drainSummaryThroughDispatcher pushes work through the REAL dispatcher at the
// given batch size and runs each emitted batch through the REAL worker entry
// point, exactly as Pipeline.Start wires them.
func drainSummaryThroughDispatcher(ctx context.Context, p *Pipeline, work []SummaryWork, batchSize int) {
	in := make(chan SummaryWork, len(work))
	out := make(chan []SummaryWork, len(work)/max(batchSize, 1)+2)
	go runSummaryDispatcher(ctx, in, out, batchSize)
	for _, w := range work {
		in <- w
	}
	close(in)
	for batch := range out {
		runSummaryWorkerBatch(ctx, p, batch)
	}
}

// TestSummaryLease_OneWritebackPerLease asserts a summary lease costs
// ceil(items/lease) writeback transactions while still being spent as
// ceil(lease/cap) summarizer calls.
//
// The provider cap is a PROMPT bound on this axis — the summarizer puts every
// chunk of one call into a single prompt — so the per-call size assertion is not
// a style check: a lease handed over in one call would build a prompt an order
// of magnitude larger than the batch size was chosen for.
func TestSummaryLease_OneWritebackPerLease(t *testing.T) {
	ctx := context.Background()
	cfg := Config{}
	lease := cfg.SummaryLeaseSizeOrDefault()
	cap := cfg.SummaryBatchSizeOrDefault()
	require.Positive(t, lease)

	work := summaryLeaseFixture(lease)
	wc := newFakeWireClient()
	ss := &strideSummarizer{}
	p := New(cfg, wc, ss.call, nil)

	drainSummaryThroughDispatcher(ctx, p, work, lease)

	sizes := ss.callSizes()
	wantCalls := (lease + cap - 1) / cap
	t.Logf("lease=%d provider_cap=%d call_sizes=%v writebacks=%d", lease, cap, sizes, execCallCount(wc))
	require.Len(t, sizes, wantCalls, "one lease must be spent as ceil(%d/%d) summarizer calls", lease, cap)
	for i, n := range sizes {
		require.LessOrEqual(t, n, cap, "summarizer call %d carried %d chunks, over the prompt cap of %d", i+1, n, cap)
	}
	require.Equal(t, 1, execCallCount(wc),
		"those %d summarizer calls must share ONE writeback — each writeback is one acquisition of the graph's advisory write mutex", wantCalls)
	require.Equal(t, lease, wc.totalWriteItems(),
		"every fixture item must still be written; a lower count means the lease dropped work rather than batching it")
}

// TestSummaryLease_StrideFailureLosesOnlyThatStride pins the stride-failure
// policy on the summary axis: a failed stride contributes nothing and the loop
// CONTINUES, so the lease size never becomes a failure-amplification factor.
func TestSummaryLease_StrideFailureLosesOnlyThatStride(t *testing.T) {
	ctx := context.Background()
	cfg := Config{}
	lease := cfg.SummaryLeaseSizeOrDefault()
	cap := cfg.SummaryBatchSizeOrDefault()
	const failingCall = 4

	work := summaryLeaseFixture(lease)
	wc := newFakeWireClient()
	ss := &strideSummarizer{failCall: failingCall}
	p := New(cfg, wc, ss.call, nil)

	drainSummaryThroughDispatcher(ctx, p, work, lease)

	require.Len(t, ss.callSizes(), (lease+cap-1)/cap,
		"the loop must CONTINUE past the failed stride — a short call list means the lease aborted")

	failedIDs := map[string]bool{}
	for _, w := range work[(failingCall-1)*cap : failingCall*cap] {
		failedIDs[w.NodeID] = true
	}

	var summaryWrites, markerWrites []updateBatchItem
	for _, batch := range wc.recordedWrites {
		if len(batch) > 0 && batch[0].Summary != nil {
			summaryWrites = append(summaryWrites, batch...)
			continue
		}
		markerWrites = append(markerWrites, batch...)
	}
	t.Logf("lease=%d failed_stride=%d summary_items=%d marker_items=%d writebacks=%d",
		lease, failingCall, len(summaryWrites), len(markerWrites), execCallCount(wc))

	// Compared against the FIXTURE-derived count, never against the other set's
	// length — two sets that lost the same members stay equal.
	require.Len(t, summaryWrites, lease-cap, "the writeback must carry every succeeded stride and only those")
	for _, it := range summaryWrites {
		require.False(t, failedIDs[it.ID], "id %s came from the failed stride and must not be in the writeback", it.ID)
	}
	// The known positive on the other side: the failed stride was not swallowed —
	// the unchanged error path stamped its terminal markers.
	require.Len(t, markerWrites, cap, "the failed stride's ids must still receive their terminal markers")
	for _, it := range markerWrites {
		require.True(t, failedIDs[it.ID], "marker written for %s, which did not come from the failed stride", it.ID)
	}
}
