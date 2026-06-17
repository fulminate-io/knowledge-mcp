// SPDX-License-Identifier: Apache-2.0

package llmproviders

import (
	"context"
	"sync"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/llm"
)

// fakeSummarizer is a test Summarizer that returns a fixed result or error and
// counts how many times SummarizeBatch was called.
type fakeSummarizer struct {
	mu      sync.Mutex
	result  map[string]SummarizeResult
	err     error
	callCnt int
}

func (f *fakeSummarizer) SummarizeBatch(_ context.Context, _ []BatchChunk) (map[string]SummarizeResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.callCnt++
	if f.err != nil {
		return nil, f.err
	}
	return f.result, nil
}

func (f *fakeSummarizer) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.callCnt
}

// advanceOnTransient is a test advance predicate mirroring the production
// !IsDeterministicTerminal contract: a transient *llm.LLMError advances.
func advanceOnTransient(err error) bool { return llm.IsTransient(err) }

// TestFallbackSummarizer_AdvancesOnFailure: entry0 returns a transient (advance)
// error, entry1 succeeds. SummarizeBatch returns entry1's results, the active
// index shifts to 1, and entry0 is marked limited.
func TestFallbackSummarizer_AdvancesOnFailure(t *testing.T) {
	entry0 := &fakeSummarizer{err: &llm.LLMError{Transient: true, Reason: "http_429"}}
	entry1 := &fakeSummarizer{result: map[string]SummarizeResult{"n1": {Summary: "ok"}}}
	health := NewChainHealth(2)
	fs := newFallbackSummarizer([]Summarizer{entry0, entry1}, health, advanceOnTransient)

	res, err := fs.SummarizeBatch(context.Background(), []BatchChunk{{ID: "n1", Content: "x"}})
	if err != nil {
		t.Fatalf("SummarizeBatch: %v", err)
	}
	if res["n1"].Summary != "ok" {
		t.Errorf("result = %+v; want entry1's summary", res)
	}
	if health.ActiveIndex() != 1 {
		t.Errorf("ActiveIndex = %d; want 1 (failover)", health.ActiveIndex())
	}
}

// TestFallbackSummarizer_Sticky: after entry0 is limited, a SECOND batch invokes
// entry1 directly — entry0's call count stays at 1 (the wrapper reads health,
// it does NOT re-probe the limited primary per batch).
func TestFallbackSummarizer_Sticky(t *testing.T) {
	entry0 := &fakeSummarizer{err: &llm.LLMError{Transient: true, Reason: "http_429"}}
	entry1 := &fakeSummarizer{result: map[string]SummarizeResult{"n1": {Summary: "ok"}}}
	health := NewChainHealth(2)
	fs := newFallbackSummarizer([]Summarizer{entry0, entry1}, health, advanceOnTransient)

	if _, err := fs.SummarizeBatch(context.Background(), []BatchChunk{{ID: "n1"}}); err != nil {
		t.Fatalf("first batch: %v", err)
	}
	if _, err := fs.SummarizeBatch(context.Background(), []BatchChunk{{ID: "n1"}}); err != nil {
		t.Fatalf("second batch: %v", err)
	}
	if got := entry0.calls(); got != 1 {
		t.Errorf("entry0 call count = %d; want 1 (sticky, no per-batch re-probe)", got)
	}
	if got := entry1.calls(); got != 2 {
		t.Errorf("entry1 call count = %d; want 2 (both batches go direct)", got)
	}
}

// TestFallbackSummarizer_DeterministicTerminalNoAdvance: entry0 returns an error
// the predicate says NOT to advance on. SummarizeBatch returns it WITHOUT
// calling entry1 and WITHOUT marking entry0 limited — the node is failed
// directly by the unchanged handleSummarizerError downstream.
func TestFallbackSummarizer_DeterministicTerminalNoAdvance(t *testing.T) {
	entry0 := &fakeSummarizer{err: &llm.LLMError{Transient: false, Reason: "parse_summaries_json"}}
	entry1 := &fakeSummarizer{result: map[string]SummarizeResult{"n1": {Summary: "ok"}}}
	health := NewChainHealth(2)
	fs := newFallbackSummarizer([]Summarizer{entry0, entry1}, health, advanceOnTransient)

	_, err := fs.SummarizeBatch(context.Background(), []BatchChunk{{ID: "n1"}})
	if err == nil {
		t.Fatal("want the deterministic-terminal error returned, got nil")
	}
	if entry1.calls() != 0 {
		t.Errorf("entry1 call count = %d; want 0 (no advance on deterministic-terminal)", entry1.calls())
	}
	if health.ActiveIndex() != 0 {
		t.Errorf("ActiveIndex = %d; want 0 (entry0 NOT limited)", health.ActiveIndex())
	}
}

// TestFallbackSummarizer_ChainExhausted: every entry returns an advance error.
// SummarizeBatch returns the LAST error (so handleSummarizerError marks the
// node), and every entry is left limited (ActiveIndex == -1).
func TestFallbackSummarizer_ChainExhausted(t *testing.T) {
	lastErr := &llm.LLMError{Transient: true, Reason: "http_503"}
	entry0 := &fakeSummarizer{err: &llm.LLMError{Transient: true, Reason: "http_429"}}
	entry1 := &fakeSummarizer{err: lastErr}
	health := NewChainHealth(2)
	fs := newFallbackSummarizer([]Summarizer{entry0, entry1}, health, advanceOnTransient)

	_, err := fs.SummarizeBatch(context.Background(), []BatchChunk{{ID: "n1"}})
	if err == nil {
		t.Fatal("want the last error on chain exhaustion, got nil")
	}
	le, ok := err.(*llm.LLMError)
	if !ok || le.Reason != "http_503" {
		t.Errorf("err = %v; want the LAST entry's http_503 error", err)
	}
	if health.ActiveIndex() != -1 {
		t.Errorf("ActiveIndex = %d; want -1 (all limited)", health.ActiveIndex())
	}
}
