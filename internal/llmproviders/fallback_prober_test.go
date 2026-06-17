// SPDX-License-Identifier: Apache-2.0

package llmproviders

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// TestHealthProber_RecoversLimitedEntry: entry0 is limited and its probe now
// returns nil (recovered). One prober run flips entry0 healthy so ActiveIndex
// shifts back 1 → 0. A still-erroring entry1 probe is never wired (entry1 stays
// healthy), and the healthy entry is never probed.
func TestHealthProber_RecoversLimitedEntry(t *testing.T) {
	health := NewChainHealth(2)
	health.MarkLimited(0)
	if health.ActiveIndex() != 1 {
		t.Fatalf("precondition ActiveIndex = %d; want 1", health.ActiveIndex())
	}

	var entry1Probed atomic.Int32
	probes := []func(context.Context) error{
		func(context.Context) error { return nil }, // entry0 recovered
		func(context.Context) error { entry1Probed.Add(1); return nil },
	}
	pr := newHealthProber(probes, health, 5*time.Millisecond)

	go pr.RunHealthProbeLoop(t.Context())

	waitFor(t, time.Second, func() bool { return health.ActiveIndex() == 0 })
	if entry1Probed.Load() != 0 {
		t.Errorf("entry1 (healthy) was probed %d times; want 0 — only limited entries are probed", entry1Probed.Load())
	}
}

// TestHealthProber_StillErroringStaysLimited: a probe that keeps erroring leaves
// its entry limited (ActiveIndex never returns to it).
func TestHealthProber_StillErroringStaysLimited(t *testing.T) {
	health := NewChainHealth(2)
	health.MarkLimited(0)
	probes := []func(context.Context) error{
		func(context.Context) error { return errors.New("still down") },
		func(context.Context) error { return nil },
	}
	pr := newHealthProber(probes, health, 5*time.Millisecond)

	go pr.RunHealthProbeLoop(t.Context())

	// Give the prober several ticks; entry0 must stay limited.
	time.Sleep(50 * time.Millisecond)
	if health.ActiveIndex() != 1 {
		t.Errorf("ActiveIndex = %d; want 1 (entry0 still limited)", health.ActiveIndex())
	}
}

// TestHealthProber_ExitsOnCancel: the loop returns when ctx is cancelled (no
// goroutine leak). Probe call-count for the healthy entry stays 0.
func TestHealthProber_ExitsOnCancel(t *testing.T) {
	health := NewChainHealth(2)
	var probed atomic.Int32
	probes := []func(context.Context) error{
		func(context.Context) error { probed.Add(1); return nil },
		func(context.Context) error { probed.Add(1); return nil },
	}
	pr := newHealthProber(probes, health, 5*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { pr.RunHealthProbeLoop(ctx); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("RunHealthProbeLoop did not exit on ctx cancel")
	}
	// No entry was limited, so no probe should have run.
	if probed.Load() != 0 {
		t.Errorf("healthy entries probed %d times; want 0", probed.Load())
	}
}

// waitFor polls cond until true or the timeout elapses.
func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("condition not met within timeout")
}
