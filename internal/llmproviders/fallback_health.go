// SPDX-License-Identifier: Apache-2.0

package llmproviders

import (
	"sync"
	"time"
)

// chainHealth is the thread-safe per-entry health state shared between the
// composite selection summarizer (fallback_summarizer.go) and the background
// health-prober (fallback_prober.go). One instance per summarizer chain.
//
// Concurrency: multiple summary workers call ActiveIndex / MarkLimited
// concurrently while the prober calls LimitedIndices / MarkHealthy on its own
// goroutine, so every access takes the RWMutex (the in-tree concurrent-shared-
// state pattern — cf. config.activeMu, llm.registryMu). Reads (ActiveIndex,
// LimitedIndices) take the read lock; mutations (MarkLimited, MarkHealthy) take
// the write lock.
//
// Selection contract: the ACTIVE entry is the lowest-index healthy entry, so
// once a recovered entry is marked healthy again selection shifts back to the
// highest-priority one automatically. All entries start healthy.
type chainHealth struct {
	mu      sync.RWMutex
	entries []healthEntry
}

// healthEntry is one chain entry's health. lastProbe records when the prober
// last checked a limited entry (used by the prober for diagnostics / pacing);
// it is unset for an entry that has never been limited.
type healthEntry struct {
	healthy   bool
	lastProbe time.Time
}

// NewChainHealth returns a chainHealth for an n-entry chain with every entry
// healthy.
func NewChainHealth(n int) *chainHealth {
	entries := make([]healthEntry, n)
	for i := range entries {
		entries[i].healthy = true
	}
	return &chainHealth{entries: entries}
}

// ActiveIndex returns the lowest-index healthy entry — the highest-priority
// summarizer currently usable — or -1 when every entry is limited (chain
// exhausted).
func (h *chainHealth) ActiveIndex() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for i, e := range h.entries {
		if e.healthy {
			return i
		}
	}
	return -1
}

// MarkLimited flags entry i unhealthy and stamps its lastProbe baseline so the
// prober knows it is awaiting recovery. Out-of-range indices are ignored.
func (h *chainHealth) MarkLimited(i int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if i < 0 || i >= len(h.entries) {
		return
	}
	h.entries[i].healthy = false
	h.entries[i].lastProbe = time.Now()
}

// MarkHealthy flags entry i healthy again — called by the prober on a
// successful recovery probe so selection shifts back to the highest-priority
// recovered entry. Out-of-range indices are ignored.
func (h *chainHealth) MarkHealthy(i int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if i < 0 || i >= len(h.entries) {
		return
	}
	h.entries[i].healthy = true
}

// LimitedIndices returns the ascending indices of currently-limited entries —
// the set the prober re-checks each interval. A fresh snapshot slice (safe for
// the caller to retain without holding the lock).
func (h *chainHealth) LimitedIndices() []int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	var out []int
	for i, e := range h.entries {
		if !e.healthy {
			out = append(out, i)
		}
	}
	return out
}

// Len returns the number of entries in the chain.
func (h *chainHealth) Len() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.entries)
}
