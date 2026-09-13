// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// worker_summary_split_test.go — the oversize split, under summaryGroupOnce's
// existing contract: a size refusal becomes SMALLER calls, the documents that
// fit are summarized, a document that cannot fit alone is marked terminally, and
// the lease still costs ONE writeback.
//
// EVERY FIXTURE SIZE IS THE TEST'S OWN. The per-document character counts, the
// provider budget and the expected call sequence are stated here, never read
// back from the code that produced them; the marker assertions compute the size
// they expect from the fixture text.

// sizedSummaryFixture builds n SummaryWork items whose composed text is exactly
// chars long each, plus one item of oversizeChars when that is positive. The
// oversize item is FIRST, so a split that simply dropped the head of the batch
// would be visible.
func sizedSummaryFixture(n, chars, oversizeChars int) []SummaryWork {
	work := make([]SummaryWork, 0, n+1)
	if oversizeChars > 0 {
		work = append(work, summaryWork("pkg/huge.go", strings.Repeat("H", oversizeChars)))
	}
	for i := range n {
		id := fmt.Sprintf("pkg/small.go:S%d", i)
		work = append(work, summaryWork(id, strings.Repeat("s", chars)))
	}
	return work
}

// splitWrites partitions the fake wire client's recorded batches into the
// summary writeback items and the failure-marker items, the same split
// TestSummaryLease_StrideFailureLosesOnlyThatStride uses.
func splitWrites(wc *fakeWireClient) (summaries, markers []updateBatchItem) {
	for _, batch := range wc.recordedWrites {
		if len(batch) > 0 && batch[0].Summary != nil {
			summaries = append(summaries, batch...)
			continue
		}
		markers = append(markers, batch...)
	}
	return summaries, markers
}

// TestSummarySplit_OversizeBatchSplitsUntilItFits is T1: the stride's first call
// is refused for size and the smaller calls succeed, so EVERY document is
// summarized and the lease still issues ONE writeback.
//
// The call sequence is the measurement the two candidate shapes differ on, and
// it is a literal this test states: 20 documents of 100 characters against a
// 1,000-character budget pack into two calls of ten, so the sequence is
// 20 (refused), 10, 10 — three calls. Blind halving of the same batch would be
// 20, 10, 10 here too, but would be 1 + 2*ceil(log2(20)) = 11 calls the moment
// one document were oversize on its own, which the sibling rows exercise.
func TestSummarySplit_OversizeBatchSplitsUntilItFits(t *testing.T) {
	ctx := context.Background()
	cfg := Config{}
	work := sizedSummaryFixture(20, 100, 0)
	wc := newFakeWireClient()
	ss := &strideSummarizer{charBudget: 1000}
	p := New(cfg, wc, ss.call, nil)

	drainSummaryThroughDispatcher(ctx, p, work, len(work))

	sizes := ss.callSizes()
	t.Logf("call_sizes=%v writebacks=%d write_items=%d", sizes, execCallCount(wc), wc.totalWriteItems())
	assert.Equal(t, []int{20, 10, 10}, sizes,
		"the refused call must be re-issued as sub-batches packed against the limit the provider reported")

	summaries, markers := splitWrites(wc)
	assert.Len(t, summaries, 20, "every document fits in some sub-batch, so every one must be summarized")
	assert.Empty(t, markers, "no document is oversize on its own, so nothing may be marked failed")
	assert.Equal(t, 1, execCallCount(wc),
		"the split must merge upward into the lease's SINGLE writeback — each writeback is one acquisition of the graph's advisory write mutex")
}

// TestSummarySplit_MultipleSplitLevels is T2, and it is the owner's ruling
// verbatim: "we may need multiple splits". Two arms, because the split reaches a
// second level two different ways:
//
//   - PACKED, THEN STILL TOO LARGE: the provider charges scaffolding on top of
//     the content, so a sub-batch packed to exactly the limit is refused again.
//     A one-level implementation marks that sub-batch terminal; the recursion
//     must halve it instead.
//   - NO LIMIT REPORTED: the refusal carried no number, so the split halves
//     blindly and needs two levels to get under the budget.
func TestSummarySplit_MultipleSplitLevels(t *testing.T) {
	t.Run("packed_then_still_too_large", func(t *testing.T) {
		ctx := context.Background()
		work := sizedSummaryFixture(20, 100, 0)
		wc := newFakeWireClient()
		// 1,000-character budget with 200 characters of scaffolding: a pack of ten
		// (1,000 content) is refused, and the halves of five (500 + 200) fit.
		ss := &strideSummarizer{charBudget: 1000, overheadPerCall: 200}
		p := New(Config{}, wc, ss.call, nil)

		drainSummaryThroughDispatcher(ctx, p, work, len(work))

		sizes := ss.callSizes()
		t.Logf("call_sizes=%v", sizes)
		require.Greater(t, len(sizes), 3,
			"a second split level must have happened: %v is one level only", sizes)
		assert.Contains(t, sizes, 5, "the second level must produce sub-batches of five")

		summaries, markers := splitWrites(wc)
		assert.Len(t, summaries, 20, "every document fits at SOME split level, so every one must be summarized")
		assert.Empty(t, markers,
			"a sub-batch that is still too large must be split again, never marked terminal — the owner's ruling is multiple splits")
		assert.Equal(t, 1, execCallCount(wc), "however many levels the split took, the lease writes back once")
	})

	t.Run("no_limit_reported_halves_blindly", func(t *testing.T) {
		ctx := context.Background()
		work := sizedSummaryFixture(20, 100, 0)
		wc := newFakeWireClient()
		ss := &strideSummarizer{charBudget: 550, hideLimit: true}
		p := New(Config{}, wc, ss.call, nil)

		drainSummaryThroughDispatcher(ctx, p, work, len(work))

		sizes := ss.callSizes()
		t.Logf("call_sizes=%v", sizes)
		// 20 refused, 10 refused, 5 fits: the halving reaches depth two on both sides.
		assert.Equal(t, []int{20, 10, 5, 5, 10, 5, 5}, sizes,
			"with no reported limit the split must halve and recurse until a half fits")

		summaries, markers := splitWrites(wc)
		assert.Len(t, summaries, 20, "every document must still be summarized when the limit is unknown")
		assert.Empty(t, markers, "nothing is oversize alone, so nothing may be marked failed")
	})
}

// TestSummarySplit_CancelledContextAbandonsTheSplit is T3's observable: the split
// inherits the stride loop's ONLY early exit. A context cancelled between
// sub-batches abandons the rest and marks NOTHING — the documents stay
// summary-eligible for the next run, which is what the stride loop does one level
// up, and stamping a durable failure marker on a shutdown would strand them.
func TestSummarySplit_CancelledContextAbandonsTheSplit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	work := sizedSummaryFixture(20, 100, 0)
	wc := newFakeWireClient()
	ss := &strideSummarizer{charBudget: 1000}
	// Cancel as soon as the refusal has been served, so the split's loop sees a
	// dead context before it issues its first sub-batch.
	ss.onCall = func(call int) {
		if call == 1 {
			cancel()
		}
	}
	p := New(Config{}, wc, ss.call, nil)

	drainSummaryThroughDispatcher(ctx, p, work, len(work))

	sizes := ss.callSizes()
	t.Logf("call_sizes=%v writebacks=%d", sizes, execCallCount(wc))
	assert.Equal(t, []int{20}, sizes, "a cancelled split must issue no sub-batch")

	summaries, markers := splitWrites(wc)
	assert.Empty(t, summaries, "a cancelled split writes nothing back")
	assert.Empty(t, markers,
		"a cancelled split must mark NOTHING — an abandoned document is still summary-eligible, and a durable marker would strand it")
}

// TestSummarySplit_TwoOversizeDocumentsDoNotPauseTheAxis is T4: the settlement-3
// guard. An oversize condition the split RESOLVES is not an errored call for the
// breaker at all, so it never reaches recordErr — and it must not, because
// ClassInvalidRequest is deterministic-terminal and the deterministic fast-trip
// threshold is TWO. Two oversize documents in one stride, recorded, would latch
// the whole summary axis into a human-only pause: the ticket's latch wearing a
// different hat.
func TestSummarySplit_TwoOversizeDocumentsDoNotPauseTheAxis(t *testing.T) {
	ctx := context.Background()
	work := append(
		sizedSummaryFixture(0, 0, 5000),
		sizedSummaryFixture(18, 100, 5000)...,
	)
	require.Len(t, work, 20, "fixture: two oversize documents and eighteen ordinary ones")

	wc := newFakeWireClient()
	// The scaffolding overhead is what makes this fixture reach a SECOND refusal
	// with no success between the two: the packed remainder of eighteen fits the
	// reported limit on content but not once the provider's own overhead is
	// counted, so it is refused and halved. Two refusals with nothing between
	// them is exactly the deterministic streak the fast-trip threshold of two
	// latches on — without the overhead a single-stride fixture can never show
	// this, because its one refusal is followed by a success that resets it.
	ss := &strideSummarizer{charBudget: 2000, overheadPerCall: 300}
	p := New(Config{}, wc, ss.call, nil)

	drainSummaryThroughDispatcher(ctx, p, work, len(work))

	st := p.summaryCircuit.status()
	t.Logf("call_sizes=%v paused=%v reason=%q", ss.callSizes(), st.Paused, st.Reason)
	require.Greater(t, len(ss.callSizes()), 2,
		"control: the fixture must have produced two refusals, or the streak this row guards is never reached: %v", ss.callSizes())
	assert.False(t, st.Paused,
		"the summary axis must still be RUNNING: reason=%q. Two recorded ClassInvalidRequest failures reach the deterministic fast-trip threshold, so a resolved oversize must not be recorded at all", st.Reason)

	summaries, markers := splitWrites(wc)
	assert.Len(t, markers, 2, "both oversize documents must be marked")
	assert.Len(t, summaries, 18, "every ordinary document must still be summarized")
}

// TestSummarySplit_AllOversizeLeaseKeepsTheAxisRunning is T4b — the stronger
// reading, and the one T4 cannot reach. A lease of TWO strides whose every
// document is oversize contains no successful call anywhere, so nothing resets
// the deterministic streak. An implementation that recorded the resolved oversize
// ONCE PER LOGICAL BATCH — settlement 3's literal words — would record twice and
// pause the axis, and a single-stride fixture would never show it.
func TestSummarySplit_AllOversizeLeaseKeepsTheAxisRunning(t *testing.T) {
	ctx := context.Background()
	cfg := Config{}
	stride := cfg.SummaryBatchSizeOrDefault()
	work := sizedSummaryFixture(2*stride, 5000, 0)

	wc := newFakeWireClient()
	ss := &strideSummarizer{charBudget: 2000}
	p := New(cfg, wc, ss.call, nil)

	drainSummaryThroughDispatcher(ctx, p, work, len(work))

	st := p.summaryCircuit.status()
	t.Logf("strides=2 call_sizes=%v paused=%v reason=%q", ss.callSizes(), st.Paused, st.Reason)
	assert.False(t, st.Paused,
		"a lease with NO successful call anywhere must still leave the axis running: reason=%q", st.Reason)

	_, markers := splitWrites(wc)
	assert.Len(t, markers, 2*stride,
		"every document of both strides must carry its terminal marker")
	assert.Equal(t, []int{stride, stride}, ss.callSizes(),
		"each stride is refused once and every document is known-oversize from its own size, so no sub-batch may be attempted")
}

// TestSummarySplit_FailureCounterCountsOnlyTerminalMarks is T5: the split's
// attempts must not feed summaryFail. The status count an operator reads IS this
// counter, so k attempts inflating it by k is exactly the "stuck batch" reading
// requirement 6 exists to prevent.
func TestSummarySplit_FailureCounterCountsOnlyTerminalMarks(t *testing.T) {
	ctx := context.Background()
	work := append(
		sizedSummaryFixture(0, 0, 5000),
		sizedSummaryFixture(19, 100, 0)...,
	)
	require.Len(t, work, 20, "fixture: one oversize document and nineteen ordinary ones")

	wc := newFakeWireClient()
	ss := &strideSummarizer{charBudget: 2000}
	p := New(Config{}, wc, ss.call, nil)

	drainSummaryThroughDispatcher(ctx, p, work, len(work))

	m := p.Metrics()
	t.Logf("call_sizes=%v summary_failed=%d summary_succeeded=%d", ss.callSizes(), m.SummaryFailed, m.SummarySucceeded)
	assert.Equal(t, int64(1), m.SummaryFailed,
		"exactly ONE document failed terminally; a higher count means the split's own attempts were counted")
	assert.Equal(t, int64(19), m.SummarySucceeded,
		"the other nineteen must be counted as succeeded")
}

// TestSummarySplit_LoneOversizeDocumentIsTerminal is T6 and requirement 2: one
// document over the limit plus nineteen ordinary ones ends with the nineteen
// summarized in the lease's single writeback, EXACTLY ONE terminal marker, and
// NO second attempt at the oversize document — its size alone answers the
// question, so re-sending it would bill a round trip to be told again.
func TestSummarySplit_LoneOversizeDocumentIsTerminal(t *testing.T) {
	ctx := context.Background()
	work := append(
		sizedSummaryFixture(0, 0, 5000),
		sizedSummaryFixture(19, 100, 0)...,
	)
	wc := newFakeWireClient()
	ss := &strideSummarizer{charBudget: 2000}
	p := New(Config{}, wc, ss.call, nil)

	drainSummaryThroughDispatcher(ctx, p, work, len(work))

	sizes := ss.callSizes()
	t.Logf("call_sizes=%v", sizes)
	assert.Equal(t, []int{20, 19}, sizes,
		"the refused call is followed by ONE call carrying the nineteen that fit; the oversize document is never re-sent")

	// The oversize document appears in the FIRST call and nowhere after it.
	for i, chunks := range ss.sentChunks() {
		if i == 0 {
			continue
		}
		for _, c := range chunks {
			assert.NotEqual(t, "pkg/huge.go", c.ID,
				"call %d re-sent the oversize document; its own size already answered the question", i+1)
		}
	}

	summaries, markers := splitWrites(wc)
	assert.Len(t, summaries, 19, "every ordinary document must be summarized")
	require.Len(t, markers, 1, "exactly one terminal marker")
	assert.Equal(t, "pkg/huge.go", markers[0].ID, "the marker must be on the oversize document")
	assert.Equal(t, 2, execCallCount(wc), "one writeback for the lease plus one marker write")
}

// TestSummarySplit_NoTruncationAtAnySplitLevel is T7 and the AGENTS.md
// invariant: "Content sent to LLM summarizers is never truncated". A split that
// trimmed or elided text to make a sub-batch fit would pass every other row in
// this file — the batch would fit and every document would come back
// summarized — while silently summarizing a prefix of each document.
func TestSummarySplit_NoTruncationAtAnySplitLevel(t *testing.T) {
	ctx := context.Background()
	work := append(
		sizedSummaryFixture(0, 0, 5000),
		sizedSummaryFixture(19, 100, 0)...,
	)
	wantText := make(map[string]string, len(work))
	for _, w := range work {
		wantText[w.NodeID] = w.SummarizeText
	}

	wc := newFakeWireClient()
	ss := &strideSummarizer{charBudget: 2000, overheadPerCall: 200}
	p := New(Config{}, wc, ss.call, nil)

	drainSummaryThroughDispatcher(ctx, p, work, len(work))

	sent := ss.sentChunks()
	require.Greater(t, len(sent), 1, "control: the split must actually have happened, or nothing is being checked")
	seen := 0
	for i, chunks := range sent {
		for _, c := range chunks {
			want, ok := wantText[c.ID]
			require.True(t, ok, "call %d carried an unknown chunk id %q", i+1, c.ID)
			assert.Len(t, c.Content, len(want),
				"call %d sent %d characters for %s; the fixture's own text is %d — content sent to a summarizer is never truncated",
				i+1, len(c.Content), c.ID, len(want))
			assert.Equal(t, want, c.Content, "call %d altered the text of %s", i+1, c.ID)
			seen++
		}
	}
	t.Logf("chunks_checked=%d across %d calls", seen, len(sent))
}

// TestSummarySplit_TicketShapeEndToEnd is requirement 7's client half at the
// SHAPE THE TICKET DESCRIBES, through the real dispatcher and the real worker
// with a fake summarizer refusing over the provider limit the incident hit.
//
// THE FIXTURE MIRRORS THE MEASURED BATCH rather than a convenient one: one
// document far over the limit, three between 134k and 231k, sixteen small, and a
// refusal at 1,048,576 characters. That shape is what makes the call sequence
// interesting — the nineteen that remain fit in ONE call, so the whole incident
// costs the refusal plus one call plus one marker write, where blind halving of
// the same batch would have cost eleven calls.
//
// THE SECOND PASS is the idempotence half: run the same lease again and the
// oversize document must still never be re-sent in a sub-batch and must still be
// marked exactly once per pass. On the host the collect skip keeps it out of the
// queue entirely — that half is the server's, on its own rails — but the worker
// must not be relying on that to avoid re-attempting it.
func TestSummarySplit_TicketShapeEndToEnd(t *testing.T) {
	ctx := context.Background()
	const limit = 1048576

	work := []SummaryWork{summaryWork("openapi.yaml", strings.Repeat("Y", 1500000))}
	for i, size := range []int{230211, 134553, 134102, 19237, 17494, 9511, 8078, 3853, 2218, 1434, 1102, 959, 752, 746, 700, 524, 379, 335, 269} {
		work = append(work, summaryWork(fmt.Sprintf("openapi.yaml:c%d", i), strings.Repeat("s", size)))
	}
	require.Len(t, work, 20, "fixture: the measured batch was twenty documents")

	wantSummarized := make([]string, 0, len(work)-1)
	for _, w := range work[1:] {
		wantSummarized = append(wantSummarized, w.NodeID)
	}

	wc := newFakeWireClient()
	ss := &strideSummarizer{charBudget: limit}
	p := New(Config{}, wc, ss.call, nil)

	for pass := 1; pass <= 2; pass++ {
		before := len(ss.callSizes())
		drainSummaryThroughDispatcher(ctx, p, work, len(work))
		sizes := ss.callSizes()[before:]
		t.Logf("pass=%d call_sizes=%v", pass, sizes)
		assert.Equal(t, []int{20, 19}, sizes,
			"pass %d: the refusal must be followed by ONE call carrying the nineteen that fit, and the oversize document must never be re-sent", pass)
	}

	summaries, markers := splitWrites(wc)
	gotSummarized := map[string]int{}
	for _, it := range summaries {
		gotSummarized[it.ID]++
	}
	for _, id := range wantSummarized {
		assert.Equal(t, 2, gotSummarized[id], "%s must be summarized once per pass", id)
	}
	assert.Len(t, gotSummarized, len(wantSummarized),
		"only the nineteen that fit may be summarized; the oversize document must not appear in a writeback")

	require.Len(t, markers, 2, "one terminal marker per pass, on the one document that cannot fit")
	for _, m := range markers {
		assert.Equal(t, "openapi.yaml", m.ID, "the marker must be on the oversize document")
		assert.Contains(t, m.Metadata[kgtypes.MetaKeySummaryFailureTerminal], fmt.Sprint(limit),
			"the terminal marker must name the provider limit it was refused against")
		assert.Contains(t, m.Metadata[kgtypes.MetaKeySummaryFailureTerminal], "1500000",
			"the terminal marker must name the document's own composed size")
	}

	m := p.Metrics()
	t.Logf("summary_failed=%d summary_succeeded=%d", m.SummaryFailed, m.SummarySucceeded)
	assert.Equal(t, int64(2), m.SummaryFailed, "one terminal failure per pass, never one per split attempt")
	assert.Equal(t, int64(2*len(wantSummarized)), m.SummarySucceeded, "nineteen successes per pass")
	assert.False(t, p.summaryCircuit.status().Paused,
		"the summary axis must still be running after two refused batches: reason=%q", p.summaryCircuit.status().Reason)
}
