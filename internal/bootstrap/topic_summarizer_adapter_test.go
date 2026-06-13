// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/llmproviders"
	clientthought "github.com/fulminate-io/knowledge-mcp/internal/thought"
)

// fakeSummarizer records every SummarizeBatch invocation and echoes a
// deterministic summary per chunk ID so the adapter's per-cluster mapping and
// input-order reassembly can be asserted. It is concurrency-safe: SummarizeTopics
// runs batches in parallel goroutines, so the call recording is mutex-guarded
// (and exercised under -race).
//
// failClusterIDs marks the first ClusterID of any batch that should fail: a batch
// whose first chunk ID is in the set returns an error and no summaries, modeling a
// per-batch failure for the partial-failure isolation test.
type fakeSummarizer struct {
	mu             sync.Mutex
	calls          int
	batches        [][]llmproviders.BatchChunk
	failClusterIDs map[string]bool
}

func (f *fakeSummarizer) SummarizeBatch(_ context.Context, chunks []llmproviders.BatchChunk) (map[string]llmproviders.SummarizeResult, error) {
	f.mu.Lock()
	f.calls++
	f.batches = append(f.batches, chunks)
	f.mu.Unlock()

	if len(chunks) > 0 && f.failClusterIDs[chunks[0].ID] {
		return nil, fmt.Errorf("batch starting at %s failed", chunks[0].ID)
	}
	out := make(map[string]llmproviders.SummarizeResult, len(chunks))
	for _, c := range chunks {
		out[c.ID] = llmproviders.SummarizeResult{Summary: "summary-of-" + c.ID}
	}
	return out, nil
}

func (f *fakeSummarizer) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// TestTopicSummarizerAdapter asserts that an input list at or under
// topicSummarizeBatchSize maps to a single batch and returns one TopicSummary per
// cluster_id (carrying the one-line Summary) in input order; an empty input list
// issues ZERO LLM calls.
func TestTopicSummarizerAdapter(t *testing.T) {
	fake := &fakeSummarizer{}
	adapter := topicSummarizerAdapter{sum: fake}

	inputs := []clientthought.TopicInput{
		{ClusterID: "c1", Content: "alpha beta"},
		{ClusterID: "c2", Content: "gamma delta"},
		{ClusterID: "c3", Content: "epsilon"},
	}
	got, err := adapter.SummarizeTopics(context.Background(), inputs)
	if err != nil {
		t.Fatalf("SummarizeTopics error: %v", err)
	}

	// 3 inputs <= batch size → exactly one batch.
	if c := fake.callCount(); c != 1 {
		t.Fatalf("SummarizeBatch calls = %d, want 1 (inputs <= batch size → one batch)", c)
	}
	if len(fake.batches[0]) != 3 {
		t.Fatalf("first batch chunks = %d, want 3", len(fake.batches[0]))
	}
	if len(got) != 3 {
		t.Fatalf("TopicSummaries = %d, want 3 (one per cluster_id)", len(got))
	}
	for i, ts := range got {
		// Order preserved from inputs.
		if ts.ClusterID != inputs[i].ClusterID {
			t.Fatalf("TopicSummary[%d].ClusterID = %q, want %q", i, ts.ClusterID, inputs[i].ClusterID)
		}
		if want := "summary-of-" + ts.ClusterID; ts.Summary != want {
			t.Fatalf("TopicSummary[%q].Summary = %q, want %q", ts.ClusterID, ts.Summary, want)
		}
	}

	// Empty input list → ZERO LLM calls.
	fake2 := &fakeSummarizer{}
	adapter2 := topicSummarizerAdapter{sum: fake2}
	out, err := adapter2.SummarizeTopics(context.Background(), nil)
	if err != nil {
		t.Fatalf("empty SummarizeTopics error: %v", err)
	}
	if c := fake2.callCount(); c != 0 {
		t.Fatalf("empty input issued %d LLM calls, want 0", c)
	}
	if len(out) != 0 {
		t.Fatalf("empty input returned %d summaries, want 0", len(out))
	}
}

// TestTopicSummarizerAdapter_Chunking feeds more inputs than
// topicSummarizeBatchSize and asserts the inputs are split into multiple
// SummarizeBatch calls AND that the returned summaries are reassembled in input
// order with the correct per-cluster summary.
func TestTopicSummarizerAdapter_Chunking(t *testing.T) {
	fake := &fakeSummarizer{}
	adapter := topicSummarizerAdapter{sum: fake}

	const n = topicSummarizeBatchSize*2 + 5 // 45 with batch 20 → 3 batches
	inputs := make([]clientthought.TopicInput, n)
	for i := range inputs {
		inputs[i] = clientthought.TopicInput{ClusterID: fmt.Sprintf("c%02d", i), Content: fmt.Sprintf("content %d", i)}
	}

	got, err := adapter.SummarizeTopics(context.Background(), inputs)
	if err != nil {
		t.Fatalf("SummarizeTopics error: %v", err)
	}

	wantBatches := (n + topicSummarizeBatchSize - 1) / topicSummarizeBatchSize
	if c := fake.callCount(); c != wantBatches {
		t.Fatalf("SummarizeBatch calls = %d, want %d (chunked by batch size)", c, wantBatches)
	}
	if len(got) != n {
		t.Fatalf("TopicSummaries = %d, want %d", len(got), n)
	}
	for i, ts := range got {
		if ts.ClusterID != inputs[i].ClusterID {
			t.Fatalf("TopicSummary[%d].ClusterID = %q, want %q (input order)", i, ts.ClusterID, inputs[i].ClusterID)
		}
		if want := "summary-of-" + ts.ClusterID; ts.Summary != want {
			t.Fatalf("TopicSummary[%q].Summary = %q, want %q", ts.ClusterID, ts.Summary, want)
		}
	}
}

// TestTopicSummarizerAdapter_PartialBatchFailure makes exactly one batch fail and
// asserts SummarizeTopics returns a non-nil aggregate error while the surviving
// batches' summaries are present (in input order) and the failed batch's topics
// carry an empty Summary.
func TestTopicSummarizerAdapter_PartialBatchFailure(t *testing.T) {
	const n = topicSummarizeBatchSize*2 + 5 // 45 → 3 batches of 20/20/5
	inputs := make([]clientthought.TopicInput, n)
	for i := range inputs {
		inputs[i] = clientthought.TopicInput{ClusterID: fmt.Sprintf("c%02d", i), Content: fmt.Sprintf("content %d", i)}
	}

	// The second batch starts at index topicSummarizeBatchSize; fail it.
	failHeadIdx := topicSummarizeBatchSize
	failHead := inputs[failHeadIdx].ClusterID
	fake := &fakeSummarizer{failClusterIDs: map[string]bool{failHead: true}}
	adapter := topicSummarizerAdapter{sum: fake}

	got, err := adapter.SummarizeTopics(context.Background(), inputs)
	if err == nil {
		t.Fatalf("expected non-nil aggregate error from a failed batch")
	}
	if !strings.Contains(err.Error(), failHead) {
		t.Fatalf("aggregate error %q should name the failed batch head %q", err.Error(), failHead)
	}
	if len(got) != n {
		t.Fatalf("TopicSummaries = %d, want %d (survivors still returned in order)", len(got), n)
	}

	// The failed batch is [topicSummarizeBatchSize, 2*topicSummarizeBatchSize): its
	// topics carry empty Summary; every other topic carries its summary. All in order.
	for i, ts := range got {
		if ts.ClusterID != inputs[i].ClusterID {
			t.Fatalf("TopicSummary[%d].ClusterID = %q, want %q (input order)", i, ts.ClusterID, inputs[i].ClusterID)
		}
		failed := i >= topicSummarizeBatchSize && i < 2*topicSummarizeBatchSize
		if failed {
			if ts.Summary != "" {
				t.Fatalf("failed-batch TopicSummary[%d].Summary = %q, want empty", i, ts.Summary)
			}
		} else {
			if want := "summary-of-" + ts.ClusterID; ts.Summary != want {
				t.Fatalf("survivor TopicSummary[%d].Summary = %q, want %q", i, ts.Summary, want)
			}
		}
	}
}
