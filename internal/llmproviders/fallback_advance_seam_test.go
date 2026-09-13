// SPDX-License-Identifier: Apache-2.0

// Package llmproviders_test holds the ONE test in this directory that runs both
// real sides of the fallback chain's advance seam.
//
// WHY IT IS AN EXTERNAL TEST PACKAGE. The chain takes its advance predicate by
// injection because llmproviders must not import pipeline (pipeline imports
// llmproviders, so the reverse is a cycle). Every in-package test therefore
// passes a stand-in predicate — which is a double sitting on the far side of the
// seam under test, and two independently-maintained halves of one decision can
// drift without any of those tests noticing. An external test package CAN import
// both, so this file wires the chain's real walk to the real
// pipeline.ShouldAdvanceFallback, exactly as bootstrap does in production.
package llmproviders_test

import (
	"context"
	"sync"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/llm"
	"github.com/fulminate-io/knowledge-mcp/internal/llmproviders"
	"github.com/fulminate-io/knowledge-mcp/internal/pipeline"
)

// seamSummarizer is a chain entry that returns a fixed result or error and
// counts its calls. It is the only double in this file, and it stands in for a
// PROVIDER — not for either side of the seam under test.
type seamSummarizer struct {
	mu      sync.Mutex
	result  map[string]llmproviders.SummarizeResult
	err     error
	callCnt int
}

func (s *seamSummarizer) SummarizeBatch(_ context.Context, _ []llmproviders.BatchChunk) (map[string]llmproviders.SummarizeResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.callCnt++
	if s.err != nil {
		return nil, s.err
	}
	return s.result, nil
}

func (s *seamSummarizer) calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.callCnt
}

// TestFallbackChain_RealAdvancePredicate_OversizeDoesNotAdvance is requirement
// 3's seam cell: a two-entry chain whose FIRST entry refuses an oversize input
// must return that error without calling the second entry and without marking
// the first limited. Advancing would bill a second provider to refuse the same
// bytes, and marking the first limited would divert every LATER batch — batches
// that are a normal size and that entry can serve — onto the fallback.
//
// Both sides are real: llmproviders' own selection walk, and the pipeline's own
// ShouldAdvanceFallback over its own classifier.
func TestFallbackChain_RealAdvancePredicate_OversizeDoesNotAdvance(t *testing.T) {
	oversize := &llm.LLMError{
		Transient:     false,
		Reason:        llm.ReasonInputTooLarge,
		InputChars:    1608836,
		MaxInputChars: 1048576,
	}
	entry0 := &seamSummarizer{err: oversize}
	entry1 := &seamSummarizer{result: map[string]llmproviders.SummarizeResult{"n1": {Summary: "ok"}}}
	health := llmproviders.NewChainHealth(2)
	chain := llmproviders.NewSelectionSummarizer([]llmproviders.Summarizer{entry0, entry1}, health, pipeline.ShouldAdvanceFallback)

	_, err := chain.SummarizeBatch(context.Background(), []llmproviders.BatchChunk{{ID: "n1"}})
	if err == nil {
		t.Fatal("SummarizeBatch err = nil; want the oversize error returned to the worker")
	}
	if got := entry1.calls(); got != 0 {
		t.Errorf("second entry calls = %d; want 0 — no provider can accept an input this size, so the chain must not advance", got)
	}
	if got := health.ActiveIndex(); got != 0 {
		t.Errorf("ActiveIndex = %d; want 0 — the first entry must NOT be marked limited by a request-shaped failure, or every later normal-sized batch is diverted too", got)
	}
	if got := entry0.calls(); got != 1 {
		t.Errorf("first entry calls = %d; want 1", got)
	}
}

// TestFallbackChain_RealAdvancePredicate_QuotaStillAdvances is the control for
// the test above, through the same real predicate: a quota wall IS
// non-deterministic, so the chain advances, the second entry serves the batch
// and the first is marked limited. Without it a predicate that returned false
// for everything would satisfy every assertion above while disabling the whole
// fallback feature.
func TestFallbackChain_RealAdvancePredicate_QuotaStillAdvances(t *testing.T) {
	entry0 := &seamSummarizer{err: &llm.LLMError{Transient: true, Reason: "http_429"}}
	entry1 := &seamSummarizer{result: map[string]llmproviders.SummarizeResult{"n1": {Summary: "ok"}}}
	health := llmproviders.NewChainHealth(2)
	chain := llmproviders.NewSelectionSummarizer([]llmproviders.Summarizer{entry0, entry1}, health, pipeline.ShouldAdvanceFallback)

	got, err := chain.SummarizeBatch(context.Background(), []llmproviders.BatchChunk{{ID: "n1"}})
	if err != nil {
		t.Fatalf("control: SummarizeBatch err = %v; want nil (the second entry serves the batch)", err)
	}
	if _, ok := got["n1"]; !ok {
		t.Errorf("control: results = %v; want the second entry's result for n1", got)
	}
	if calls := entry1.calls(); calls != 1 {
		t.Errorf("control: second entry calls = %d; want 1 — a quota wall must advance", calls)
	}
	if idx := health.ActiveIndex(); idx != 1 {
		t.Errorf("control: ActiveIndex = %d; want 1 — the quota-walled entry must be marked limited", idx)
	}
}
