// SPDX-License-Identifier: Apache-2.0

package config

import (
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestActive_PanicsBeforeInit asserts Active() panics with the expected
// message when nothing has populated active. Uses SetForTest(nil) +
// recover() to keep the test deterministic regardless of whether other
// tests left active populated.
func TestActive_PanicsBeforeInit(t *testing.T) {
	cleanup := SetForTest(nil)
	defer cleanup()

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("Active() did not panic")
		}
		msg, ok := r.(string)
		if !ok {
			t.Fatalf("panic value not string: %T %v", r, r)
		}
		if !strings.Contains(msg, "config.LoadOrAutoDetect() not called") {
			t.Errorf("panic message = %q; want \"config.LoadOrAutoDetect() not called\"", msg)
		}
	}()

	_ = Active()
}

func TestSetForTest_RoundTrip(t *testing.T) {
	a := &Config{Default: Section{Provider: ProviderAnthropic, Model: "claude-haiku-5"}}

	// Snapshot current state via SetForTest(nil) so we can assert
	// post-cleanup behavior independent of other tests.
	resetPrior := SetForTest(nil)
	defer resetPrior()

	cleanup := SetForTest(a)
	if got := Active(); got != a {
		t.Errorf("Active() = %p; want %p", got, a)
	}
	cleanup()

	// After cleanup, active should be back to the prior value (nil
	// from resetPrior); Active() should panic.
	defer func() { _ = recover() }()
	_ = Active()
	t.Error("Active() did not panic after cleanup restored nil")
}

func TestSetForTest_NestingLIFO(t *testing.T) {
	a := &Config{Default: Section{Provider: ProviderAnthropic, Model: "a"}}
	b := &Config{Default: Section{Provider: ProviderOpenAI, Model: "b"}}

	resetPrior := SetForTest(nil)
	defer resetPrior()

	outerCleanup := SetForTest(a)
	if got := Active(); got != a {
		t.Errorf("after outer SetForTest: Active() = %p; want %p", got, a)
	}

	innerCleanup := SetForTest(b)
	if got := Active(); got != b {
		t.Errorf("after inner SetForTest: Active() = %p; want %p", got, b)
	}

	// LIFO: inner cleanup restores to a.
	innerCleanup()
	if got := Active(); got != a {
		t.Errorf("after inner cleanup: Active() = %p; want %p", got, a)
	}

	outerCleanup()
	// Now back to nil; Active() panics.
	defer func() { _ = recover() }()
	_ = Active()
	t.Error("Active() did not panic after outer cleanup")
}

// TestActive_ConcurrentRead exercises 100 goroutines reading via Active()
// while a writer goroutine swaps configs via setActive. The RWMutex
// guarantees no torn reads; the test passes when run with -race.
func TestActive_ConcurrentRead(t *testing.T) {
	a := &Config{Default: Section{Provider: ProviderAnthropic, Model: "a"}}
	b := &Config{Default: Section{Provider: ProviderOpenAI, Model: "b"}}

	cleanup := SetForTest(a)
	defer cleanup()

	const readers = 100
	const duration = 50 * time.Millisecond
	stop := make(chan struct{})

	var reads atomic.Int64
	var wg sync.WaitGroup
	for range readers {
		wg.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
					cfg := Active()
					if cfg == nil {
						t.Errorf("Active() returned nil")
						return
					}
					// Touch a field — under -race this catches torn
					// reads of the *Config pointer (treating it as
					// the unit of atomicity, which the lock provides).
					_ = cfg.Default.Provider
					reads.Add(1)
				}
			}
		})
	}

	// Writer goroutine swaps repeatedly via setActive (the package-
	// internal writer). SetForTest would also work but stacks
	// closures; setActive is the lighter loop.
	wg.Go(func() {
		toggle := false
		for {
			select {
			case <-stop:
				return
			default:
				if toggle {
					setActive(a)
				} else {
					setActive(b)
				}
				toggle = !toggle
			}
		}
	})

	time.Sleep(duration)
	close(stop)
	wg.Wait()

	if reads.Load() == 0 {
		t.Errorf("no reads completed (got %d)", reads.Load())
	}
}
