// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"testing"
	"time"
)

// TestRunSummaryDispatcher_BatchesAndEmitsPartial feeds 25 items at
// batchSize=10 and asserts the dispatcher emits batches of [10, 10, 5].
// Confirms full-batch emission AND partial-batch flush on input close.
func TestRunSummaryDispatcher_BatchesAndEmitsPartial(t *testing.T) {
	ctx := context.Background()
	in := make(chan SummaryWork, 25)
	batchOut := make(chan []SummaryWork, 5)

	for i := range 25 {
		in <- SummaryWork{NodeID: "id-" + string(rune('a'+i))}
	}
	close(in)

	go runSummaryDispatcher(ctx, in, batchOut, 10)

	var sizes []int
	for batch := range batchOut {
		sizes = append(sizes, len(batch))
	}

	if len(sizes) != 3 {
		t.Fatalf("got %d batches, want 3 (sizes=%v)", len(sizes), sizes)
	}
	if sizes[0] != 10 || sizes[1] != 10 || sizes[2] != 5 {
		t.Errorf("batch sizes = %v, want [10 10 5]", sizes)
	}
}

// TestRunEmbedDispatcher_BatchesAndEmitsPartial mirrors the summary test
// for the embed-side dispatcher.
func TestRunEmbedDispatcher_BatchesAndEmitsPartial(t *testing.T) {
	ctx := context.Background()
	in := make(chan EmbedWork, 25)
	batchOut := make(chan []EmbedWork, 5)

	for i := range 25 {
		in <- EmbedWork{NodeID: "id-" + string(rune('a'+i))}
	}
	close(in)

	go runEmbedDispatcher(ctx, in, batchOut, 10)

	var sizes []int
	for batch := range batchOut {
		sizes = append(sizes, len(batch))
	}

	if len(sizes) != 3 {
		t.Fatalf("got %d batches, want 3 (sizes=%v)", len(sizes), sizes)
	}
	if sizes[0] != 10 || sizes[1] != 10 || sizes[2] != 5 {
		t.Errorf("batch sizes = %v, want [10 10 5]", sizes)
	}
}

// TestRunSummaryDispatcher_ContextCancelDrains feeds 5 items, cancels
// ctx, and asserts the dispatcher emits the partial batch before exiting.
func TestRunSummaryDispatcher_ContextCancelDrains(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	in := make(chan SummaryWork, 5)
	batchOut := make(chan []SummaryWork, 2)

	for range 5 {
		in <- SummaryWork{NodeID: "id"}
	}
	go runSummaryDispatcher(ctx, in, batchOut, 10)

	// Give the dispatcher time to read the items into the batch.
	time.Sleep(20 * time.Millisecond)
	cancel()

	var seen int
	for batch := range batchOut {
		seen += len(batch)
	}
	if seen != 5 {
		t.Errorf("dispatcher emitted %d items after cancel, want 5", seen)
	}
}

// TestRunSummaryDispatcher_PartialFlushOnTimeout asserts a sub-batchSize
// burst flushes within partialFlushInterval (250ms) without needing close
// or context cancel. Regression test for the live-server bug where the
// embed dispatcher held 99 items waiting for the 100th that never came.
func TestRunSummaryDispatcher_PartialFlushOnTimeout(t *testing.T) {
	ctx := t.Context()
	in := make(chan SummaryWork, 5)
	batchOut := make(chan []SummaryWork, 2)

	for range 3 {
		in <- SummaryWork{NodeID: "id"}
	}
	go runSummaryDispatcher(ctx, in, batchOut, 100)

	select {
	case batch := <-batchOut:
		if len(batch) != 3 {
			t.Errorf("partial flush emitted %d items, want 3", len(batch))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("dispatcher never emitted partial batch within 2s — partial-flush timer broken")
	}
}

// TestRunEmbedDispatcher_PartialFlushOnTimeout mirrors the summary test.
func TestRunEmbedDispatcher_PartialFlushOnTimeout(t *testing.T) {
	ctx := t.Context()
	in := make(chan EmbedWork, 5)
	batchOut := make(chan []EmbedWork, 2)

	for range 3 {
		in <- EmbedWork{NodeID: "id"}
	}
	go runEmbedDispatcher(ctx, in, batchOut, 100)

	select {
	case batch := <-batchOut:
		if len(batch) != 3 {
			t.Errorf("partial flush emitted %d items, want 3", len(batch))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("dispatcher never emitted partial batch within 2s — partial-flush timer broken")
	}
}
