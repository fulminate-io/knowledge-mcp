// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"testing"
	"time"
)

// TestErrBackoff_GrowsAndResets verifies the window grows on consecutive
// fails and clears on ok.
func TestErrBackoff_GrowsAndResets(t *testing.T) {
	b := newErrBackoff(10*time.Millisecond, 1*time.Second)

	// Steady state: wait returns immediately.
	start := time.Now()
	b.wait(context.Background())
	if d := time.Since(start); d > 5*time.Millisecond {
		t.Fatalf("steady-state wait should be ~instant, took %v", d)
	}

	// First fail opens a window roughly base-sized (±20% jitter).
	d1 := b.fail()
	if d1 < 8*time.Millisecond || d1 > 13*time.Millisecond {
		t.Fatalf("first fail delay = %v, want ~10ms ±20%%", d1)
	}
	// Second fail ~doubles.
	d2 := b.fail()
	if d2 < 16*time.Millisecond || d2 > 25*time.Millisecond {
		t.Fatalf("second fail delay = %v, want ~20ms ±20%%", d2)
	}

	// ok clears the window: next wait is instant.
	b.ok()
	start = time.Now()
	b.wait(context.Background())
	if d := time.Since(start); d > 5*time.Millisecond {
		t.Fatalf("post-ok wait should be ~instant, took %v", d)
	}
}

// TestErrBackoff_Caps verifies the exponential growth saturates at maxDelay
// and never overflows on a long failure streak.
func TestErrBackoff_Caps(t *testing.T) {
	b := newErrBackoff(1*time.Second, 5*time.Second)
	var last time.Duration
	for range 100 {
		last = b.fail()
	}
	// 100 consecutive fails: 1s << 99 would overflow int64 ns without the
	// shift cap. Must stay at/under cap (+20% jitter).
	if last > 6*time.Second || last <= 0 {
		t.Fatalf("capped delay = %v, want <= ~5s and positive", last)
	}
}

// TestErrBackoff_WaitHonorsContext verifies wait returns promptly on ctx
// cancel rather than blocking the full window.
func TestErrBackoff_WaitHonorsContext(t *testing.T) {
	b := newErrBackoff(10*time.Second, 30*time.Second)
	b.fail() // open a long window

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already canceled
	start := time.Now()
	b.wait(ctx)
	if d := time.Since(start); d > 100*time.Millisecond {
		t.Fatalf("wait should return promptly on canceled ctx, took %v", d)
	}
}
