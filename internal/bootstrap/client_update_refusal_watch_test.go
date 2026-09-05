// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"os/exec"
	"sync"
	"testing"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/clientver"
)

// withFrozenClock installs a controllable clock for the watcher's interval
// comparisons and returns a func that advances it.
func withFrozenClock(t *testing.T) func(time.Duration) {
	t.Helper()
	var mu sync.Mutex
	now := time.Now()
	prev := updateCheckNow
	updateCheckNow = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return now
	}
	t.Cleanup(func() { updateCheckNow = prev })
	return func(d time.Duration) {
		mu.Lock()
		now = now.Add(d)
		mu.Unlock()
	}
}

// TestUpdateCheck_LatchedRefusalTriggersImmediateCheck drives the refusal
// watcher.
func TestUpdateCheck_LatchedRefusalTriggersImmediateCheck(t *testing.T) {
	// SEVEN ROWS: the six reasons the cloud currently emits, PLUS one
	// deliberately out-of-vocabulary string. THE LAST ROW IS THE LOAD-BEARING
	// ONE. The property under test is "does not branch on Reason", and an
	// enumeration of KNOWN members cannot establish it — a switch listing all
	// six passes every one of those rows and is exactly the implementation the
	// property forbids. Only a reason the implementation cannot have been
	// written against separates a switch from a struct read.
	reasons := []string{
		clientver.ReasonBelowMinimum,
		clientver.ReasonHeaderAbsent,
		clientver.ReasonUnparseable,
		clientver.ReasonDevStampNotAllowlisted,
		clientver.ReasonUnverified,
		clientver.ReasonUnprovable,
		"a_reason_this_binary_was_never_built_against",
	}

	for _, reason := range reasons {
		t.Run("reason "+reason+" drives a check", func(t *testing.T) {
			s := &updateSeams{}
			installUpdateSeams(t, s, "v1.0.0", nil, false)
			withVersionAndConfig(t, "v1.0.0", nil)
			advance := withFrozenClock(t)
			_ = advance
			clientver.ClearRefusal()
			t.Cleanup(clientver.ClearRefusal)

			c := &client{updateHealth: newUpdateHealthTracker()}
			state := &refusalWatchState{}

			clientver.LatchRefusal(clientver.Refusal{
				Reason: reason, Minimum: "v2.0.0", ClientVersion: "v1.0.0", At: time.Now(),
			})
			// No ticker wait: the observation is driven directly, which is what
			// "immediately rather than waiting for the next tick" means.
			c.checkOnRefusal(t.Context(), Config{}, state)

			if got := s.resolves.Load(); got != 1 {
				t.Fatalf("reason %q produced %d check(s), want exactly 1 — every reason must drive a check, whatever the string is", reason, got)
			}

			// The origin is recorded VERBATIM, including a reason this binary
			// was never built against.
			snap, ok := c.UpdateCheckHealth()
			if !ok {
				t.Fatalf("the health snapshot must be readable")
			}
			if snap.TriggerReason != reason {
				t.Errorf("trigger reason = %q, want the cloud's own %q verbatim", snap.TriggerReason, reason)
			}
			if snap.TriggerMinimum != "v2.0.0" || snap.TriggerClientVersion != "v1.0.0" {
				t.Errorf("the origin lost the cloud's demand: %+v", snap)
			}
		})
	}

	// THE CONVERGENCE CONTROL, and its clock advance is load-bearing rather
	// than incidental. Two mechanisms suppress a repeat: the per-refusal
	// de-duplication, which suppresses forever, and the minimum interval, which
	// suppresses only until it elapses. Asserted WITHOUT advancing the clock,
	// this row would pass against the plausible-but-wrong implementation that
	// carries only the interval — and that implementation re-checks every
	// fifteen minutes forever on a permanently-refused client. Advancing PAST
	// the interval leaves the de-duplication as the only thing that can still
	// suppress.
	t.Run("the SAME refusal produces no second check even past the minimum interval", func(t *testing.T) {
		s := &updateSeams{}
		installUpdateSeams(t, s, "v1.0.0", nil, false)
		withVersionAndConfig(t, "v1.0.0", nil)
		advance := withFrozenClock(t)
		clientver.ClearRefusal()
		t.Cleanup(clientver.ClearRefusal)

		c := &client{updateHealth: newUpdateHealthTracker()}
		state := &refusalWatchState{}
		at := time.Now()
		clientver.LatchRefusal(clientver.Refusal{Reason: clientver.ReasonBelowMinimum, At: at})

		c.checkOnRefusal(t.Context(), Config{}, state)
		if got := s.resolves.Load(); got != 1 {
			t.Fatalf("the first observation produced %d check(s), want 1", got)
		}

		advance(updateTriggeredCheckMinInterval + time.Minute)
		for range 5 {
			c.checkOnRefusal(t.Context(), Config{}, state)
		}
		if got := s.resolves.Load(); got != 1 {
			t.Errorf("an UNCHANGED refusal produced %d checks past the minimum interval, want 1 — a permanently-refused client would otherwise re-check forever", got)
		}

		// KNOWN-POSITIVE, same run: a NEW refusal (strictly newer At) DOES
		// trigger, so the silence above is the de-duplication working rather
		// than a watcher that never fires again.
		clientver.LatchRefusal(clientver.Refusal{Reason: clientver.ReasonBelowMinimum, At: at.Add(time.Hour)})
		c.checkOnRefusal(t.Context(), Config{}, state)
		if got := s.resolves.Load(); got != 2 {
			t.Errorf("a NEW refusal produced no check (total %d); the de-duplication has become a permanent mute", got)
		}
	})

	t.Run("N concurrent triggers produce exactly ONE check", func(t *testing.T) {
		s := &updateSeams{}
		installUpdateSeams(t, s, "v1.0.0", nil, false)
		withVersionAndConfig(t, "v1.0.0", nil)
		withFrozenClock(t)
		clientver.ClearRefusal()
		t.Cleanup(clientver.ClearRefusal)

		c := &client{updateHealth: newUpdateHealthTracker()}
		state := &refusalWatchState{}
		clientver.LatchRefusal(clientver.Refusal{Reason: clientver.ReasonUnverified, At: time.Now()})

		var wg sync.WaitGroup
		for range 16 {
			wg.Go(func() {
				c.checkOnRefusal(t.Context(), Config{}, state)
			})
		}
		wg.Wait()
		if got := s.resolves.Load(); got != 1 {
			t.Errorf("%d concurrent triggers produced %d checks, want exactly 1 — a refusal is hit by EVERY routed cloud operation, so an uncoalesced trigger turns one policy change into a burst", 16, got)
		}
	})

	t.Run("a trigger under a refusing guard performs no install", func(t *testing.T) {
		for _, tc := range []struct {
			name      string
			version   string
			brew      bool
			container bool
			disabled  bool
		}{
			{name: "dev-stamped", version: "dev-v0.8.1-312-gabc"},
			{name: "brew-managed", version: "v1.0.0", brew: true},
			{name: "container-managed", version: "v1.0.0", container: true},
			{name: "disabled", version: "v1.0.0", disabled: true},
		} {
			t.Run(tc.name, func(t *testing.T) {
				s := &updateSeams{}
				installUpdateSeams(t, s, "v9.9.9", nil, tc.brew)
				var auto *bool
				if tc.disabled {
					auto = new(false)
				}
				withVersionAndConfig(t, tc.version, auto)
				if tc.container {
					t.Setenv(envManagedInstall, "container")
				}
				withFrozenClock(t)
				clientver.ClearRefusal()
				t.Cleanup(clientver.ClearRefusal)

				c := &client{updateHealth: newUpdateHealthTracker()}
				state := &refusalWatchState{}
				clientver.LatchRefusal(clientver.Refusal{Reason: clientver.ReasonBelowMinimum, At: time.Now()})
				c.checkOnRefusal(t.Context(), Config{}, state)

				if got := s.installs.Load(); got != 0 {
					t.Errorf("a cloud refusal drove %d install(s) past the %s guard; a refusal is a reason to LOOK sooner, never authority to bypass a guard", got, tc.name)
				}
			})
		}
	})

	t.Run("a successful self-update clears the latch BEFORE the restart handoff", func(t *testing.T) {
		s := &updateSeams{}
		installUpdateSeams(t, s, "v2.0.0", nil, false)
		withVersionAndConfig(t, "v1.0.0", nil)
		withFrozenClock(t)
		clientver.ClearRefusal()
		t.Cleanup(clientver.ClearRefusal)

		// Observe the ORDER: the handoff records whether the latch was already
		// cleared when it ran. Clearing AFTER would leave a window in which the
		// still-old process re-triggers on its own stale refusal.
		var latchedAtHandoff bool
		prevExec, prevPath := daemonExecCommand, installedClientPath
		daemonExecCommand = func(string, ...string) *exec.Cmd {
			_, latchedAtHandoff = clientver.CurrentRefusal()
			return exec.Command("true")
		}
		installedClientPath = func() (string, error) { return "/nonexistent/knowledge", nil }
		t.Cleanup(func() { daemonExecCommand, installedClientPath = prevExec, prevPath })

		c := &client{updateHealth: newUpdateHealthTracker()}
		state := &refusalWatchState{}
		clientver.LatchRefusal(clientver.Refusal{Reason: clientver.ReasonBelowMinimum, At: time.Now()})
		c.checkOnRefusal(t.Context(), Config{}, state)

		if got := s.installs.Load(); got != 1 {
			t.Fatalf("the permitted case installed %d time(s), want 1; the ordering assertion below would prove nothing", got)
		}
		if latchedAtHandoff {
			t.Errorf("the refusal was STILL latched when the restart handoff ran; it must be cleared BEFORE the handoff, or the old process re-triggers on its own stale refusal in the window before the restart lands")
		}
		if _, still := clientver.CurrentRefusal(); still {
			t.Errorf("a successful self-update left the refusal latched")
		}
	})

	t.Run("no refusal produces no check", func(t *testing.T) {
		// The base control: the watcher must not fire on an empty latch, or
		// every row above would be consistent with one that always fires.
		s := &updateSeams{}
		installUpdateSeams(t, s, "v1.0.0", nil, false)
		withVersionAndConfig(t, "v1.0.0", nil)
		withFrozenClock(t)
		clientver.ClearRefusal()

		c := &client{updateHealth: newUpdateHealthTracker()}
		c.checkOnRefusal(t.Context(), Config{}, &refusalWatchState{})
		if got := s.resolves.Load(); got != 0 {
			t.Errorf("the watcher fired %d time(s) with no refusal latched", got)
		}
	})
}
