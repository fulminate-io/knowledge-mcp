// SPDX-License-Identifier: Apache-2.0

package llmproviders

import (
	"sync"
	"testing"
)

// TestChainHealth covers the active-index selection contract: a fresh chain is
// fully healthy so the highest-priority entry (index 0) is active; marking the
// active entry limited shifts selection to the next healthy index; limiting
// every entry yields -1 (chain exhausted); marking an entry healthy again
// returns selection to the highest-priority healthy index.
func TestChainHealth(t *testing.T) {
	h := NewChainHealth(3)
	if got := h.ActiveIndex(); got != 0 {
		t.Fatalf("fresh ActiveIndex = %d; want 0", got)
	}
	h.MarkLimited(0)
	if got := h.ActiveIndex(); got != 1 {
		t.Fatalf("after MarkLimited(0) ActiveIndex = %d; want 1", got)
	}
	h.MarkLimited(1)
	h.MarkLimited(2)
	if got := h.ActiveIndex(); got != -1 {
		t.Fatalf("all-limited ActiveIndex = %d; want -1", got)
	}
	h.MarkHealthy(0)
	if got := h.ActiveIndex(); got != 0 {
		t.Fatalf("after MarkHealthy(0) ActiveIndex = %d; want 0 (back to highest priority)", got)
	}
}

// TestChainHealth_LimitedIndices asserts the prober's snapshot view: only
// limited entries appear, in ascending index order.
func TestChainHealth_LimitedIndices(t *testing.T) {
	h := NewChainHealth(4)
	h.MarkLimited(2)
	h.MarkLimited(0)
	got := h.LimitedIndices()
	want := []int{0, 2}
	if len(got) != len(want) {
		t.Fatalf("LimitedIndices = %v; want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("LimitedIndices = %v; want %v", got, want)
		}
	}
}

// TestChainHealth_Concurrent exercises ActiveIndex against concurrent
// MarkLimited/MarkHealthy so the -race detector proves the RWMutex guards every
// access. It asserts no panic / data race, not a specific final index.
func TestChainHealth_Concurrent(t *testing.T) {
	h := NewChainHealth(8)
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(2)
		go func(idx int) { defer wg.Done(); h.MarkLimited(idx) }(i)
		go func(idx int) { defer wg.Done(); h.MarkHealthy(idx) }(i)
	}
	for range 64 {
		wg.Go(func() { _ = h.ActiveIndex(); _ = h.LimitedIndices() })
	}
	wg.Wait()
}
