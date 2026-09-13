// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// worker_summary_split_marker_test.go — the MARKER half of the oversize split:
// which keys a terminal mark writes, how many RPCs it costs, and what a later
// success clears. Split from worker_summary_split_test.go, which holds the split
// mechanism and its fixtures, because this package's files are capped at 500
// lines; the fixtures both files use live there.

// TestSummarySplit_TerminalMarkWritesBothKeysInOneCall is T8 and the marker
// half of decision-recorded design: the terminal mark carries
// summary_failure_reason exactly as today PLUS the terminal key naming the
// reason, the size and the limit, in ONE mutate(update_batch).
//
// The expected size is computed by THIS TEST from its own fixture, never read
// back from the producer.
func TestSummarySplit_TerminalMarkWritesBothKeysInOneCall(t *testing.T) {
	ctx := context.Background()
	const oversizeChars = 5000
	const budget = 2000
	work := append(
		sizedSummaryFixture(0, 0, oversizeChars),
		sizedSummaryFixture(19, 100, 0)...,
	)
	wc := newFakeWireClient()
	ss := &strideSummarizer{charBudget: budget}
	p := New(Config{}, wc, ss.call, nil)

	drainSummaryThroughDispatcher(ctx, p, work, len(work))

	_, markers := splitWrites(wc)
	require.Len(t, markers, 1, "exactly one terminal marker, written in one call")
	item := markers[0]

	reason := item.Metadata[kgtypes.MetaKeySummaryFailureReason]
	terminal := item.Metadata[kgtypes.MetaKeySummaryFailureTerminal]
	t.Logf("reason=%q terminal=%q", reason, terminal)

	assert.NotEmpty(t, reason,
		"summary_failure_reason must still be written: four gap-scan sites and the status count read THAT key, and writing only the terminal key would re-queue the node on every scan tick")
	assert.NotEmpty(t, terminal,
		"the terminal key must be written, or the collect self-heal wipes the marker and the node re-latches")

	// All three facts, on the key an operator reads by id.
	assert.Contains(t, terminal, "input_too_large", "the terminal marker must name the reason")
	assert.Contains(t, terminal, fmt.Sprint(oversizeChars), "the terminal marker must name the document's composed size")
	assert.Contains(t, terminal, fmt.Sprint(budget), "the terminal marker must name the provider limit")

	// The marker write is ONE RPC for the whole id set, as it is today.
	markerCalls := 0
	for _, batch := range wc.recordedWrites {
		if len(batch) > 0 && batch[0].Summary == nil {
			markerCalls++
		}
	}
	assert.Equal(t, 1, markerCalls, "the terminal mark must stay ONE update_batch, with the second key on the same item")
}

// TestSummarySplit_ManyOversizeDocumentsMarkInOneCall is the per-batch RPC
// criterion for the split: a stride that isolates k oversize documents marks
// them in ONE call, not one per split leaf.
func TestSummarySplit_ManyOversizeDocumentsMarkInOneCall(t *testing.T) {
	ctx := context.Background()
	work := sizedSummaryFixture(20, 5000, 0)
	wc := newFakeWireClient()
	ss := &strideSummarizer{charBudget: 2000}
	p := New(Config{}, wc, ss.call, nil)

	drainSummaryThroughDispatcher(ctx, p, work, len(work))

	markerCalls := 0
	marked := 0
	for _, batch := range wc.recordedWrites {
		if len(batch) > 0 && batch[0].Summary == nil {
			markerCalls++
			marked += len(batch)
		}
	}
	t.Logf("call_sizes=%v marker_calls=%d marked=%d", ss.callSizes(), markerCalls, marked)
	assert.Equal(t, 20, marked, "every oversize document must be marked")
	assert.Equal(t, 1, markerCalls, "twenty isolated oversize documents must cost ONE marker write, not twenty")
}

// TestSummarySplit_SuccessfulSummaryClearsBothKeys is contract 6's cell: a node
// that carried the terminal key and later summarizes successfully must have BOTH
// keys cleared, or the terminal marker outlives the condition it recorded and
// the collect keeps skipping a row that is no longer failed.
func TestSummarySplit_SuccessfulSummaryClearsBothKeys(t *testing.T) {
	ctx := context.Background()
	wc := newFakeWireClient()
	ss := &strideSummarizer{}
	p := New(Config{}, wc, ss.call, nil)

	drainSummaryThroughDispatcher(ctx, p, sizedSummaryFixture(3, 100, 0), 3)

	summaries, _ := splitWrites(wc)
	require.Len(t, summaries, 3, "control: the fixture must have been summarized")
	for _, item := range summaries {
		reason, hasReason := item.Metadata[kgtypes.MetaKeySummaryFailureReason]
		terminal, hasTerminal := item.Metadata[kgtypes.MetaKeySummaryFailureTerminal]
		assert.True(t, hasReason, "%s: the success writeback must clear summary_failure_reason", item.ID)
		assert.Empty(t, reason, "%s: summary_failure_reason must be cleared to empty", item.ID)
		assert.True(t, hasTerminal,
			"%s: the success writeback must clear the terminal key too, or a node that becomes summarizable keeps a marker for a condition that no longer holds", item.ID)
		assert.Empty(t, terminal, "%s: the terminal key must be cleared to empty", item.ID)
	}
}
