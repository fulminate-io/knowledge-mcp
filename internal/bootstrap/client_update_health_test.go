// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

// levelCapture records the level of every log record emitted through it, so the
// Warn-to-Error escalation is observed rather than assumed.
type levelCapture struct {
	mu      sync.Mutex
	levels  []slog.Level
	lastMsg string
}

func (h *levelCapture) Enabled(context.Context, slog.Level) bool { return true }

func (h *levelCapture) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	h.levels = append(h.levels, r.Level)
	h.lastMsg = r.Message
	h.mu.Unlock()
	return nil
}

func (h *levelCapture) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *levelCapture) WithGroup(string) slog.Handler      { return h }

func (h *levelCapture) counts() (warns, errs int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, l := range h.levels {
		switch l {
		case slog.LevelWarn:
			warns++
		case slog.LevelError:
			errs++
		}
	}
	return warns, errs
}

// TestUpdateCheck_PersistentFailureEscalatesAndRecords drives consecutive
// failing ticks and asserts the streak advances, the log escalates from Warn to
// Error at the threshold, and a single success resets the streak.
func TestUpdateCheck_PersistentFailureEscalatesAndRecords(t *testing.T) {
	cap := &levelCapture{}
	prevLogger := slog.Default()
	slog.SetDefault(slog.New(cap))
	t.Cleanup(func() { slog.SetDefault(prevLogger) })

	withVersionAndConfig(t, "v1.0.0", nil)

	var failing bool
	prevResolve, prevInstall, prevBrew, prevOwner := updateCheckResolveFn, updateCheckInstallFn, brewListFn, serverOwnerFn
	updateCheckResolveFn = func(context.Context) (*releaseResponse, error) {
		if failing {
			return nil, errors.New("release endpoint unreachable")
		}
		return &releaseResponse{TagName: "v1.0.0"}, nil
	}
	updateCheckInstallFn = func(context.Context, *releaseResponse) error { return nil }
	brewListFn = func() bool { return false }
	serverOwnerFn = func() string { return "" }
	t.Cleanup(func() {
		updateCheckResolveFn, updateCheckInstallFn, brewListFn, serverOwnerFn = prevResolve, prevInstall, prevBrew, prevOwner
	})

	c := &client{updateHealth: newUpdateHealthTracker()}

	failing = true
	for i := 1; i <= updateCheckFailureEscalation; i++ {
		c.logUpdateCheckOutcome(t.Context(), Config{})
		snap, _ := c.UpdateCheckHealth()
		if snap.ConsecutiveFailures != i {
			t.Fatalf("after %d failing tick(s) the streak is %d, want %d", i, snap.ConsecutiveFailures, i)
		}
		if snap.LastError == "" {
			t.Errorf("tick %d recorded no error", i)
		}
		if snap.LastFailure.IsZero() {
			t.Errorf("tick %d recorded no failure time", i)
		}
	}

	warns, errs := cap.counts()
	// The first ticks below the threshold warn; the threshold tick escalates.
	if warns != updateCheckFailureEscalation-1 {
		t.Errorf("saw %d Warn record(s) before the threshold, want %d", warns, updateCheckFailureEscalation-1)
	}
	if errs != 1 {
		t.Errorf("saw %d Error record(s) at the threshold, want exactly 1 — a persistent updater failure must get loud rather than warning forever", errs)
	}

	// A single success resets the streak.
	failing = false
	c.logUpdateCheckOutcome(t.Context(), Config{})
	snap, _ := c.UpdateCheckHealth()
	if snap.ConsecutiveFailures != 0 {
		t.Errorf("a successful tick left the streak at %d, want 0", snap.ConsecutiveFailures)
	}
	if snap.LastError != "" {
		t.Errorf("a successful tick left the last error %q standing", snap.LastError)
	}
	if snap.LatestKnown != "v1.0.0" {
		t.Errorf("latest known release = %q, want the resolved tag", snap.LatestKnown)
	}
}

// TestUpdateHealthTracker_RefusalIsNotAFailure pins the distinction the
// snapshot exists to carry: a guard that refuses is a correct outcome, so it
// must not advance the failure streak and escalate a healthy daemon's log to
// Error for doing exactly the right thing.
func TestUpdateHealthTracker_RefusalIsNotAFailure(t *testing.T) {
	tr := newUpdateHealthTracker()
	now := time.Now()

	tr.Record(updateCheckOutcome{Err: errors.New("boom")}, now)
	if got := tr.Snapshot().ConsecutiveFailures; got != 1 {
		t.Fatalf("a failure must advance the streak; got %d", got)
	}
	snap := tr.Record(updateCheckOutcome{Skipped: skipBrewManaged}, now)
	if snap.ConsecutiveFailures != 0 {
		t.Errorf("a refusal advanced the failure streak to %d; brew owning the install is a correct outcome, not a failure", snap.ConsecutiveFailures)
	}
	if snap.NoActionReason != string(skipBrewManaged) {
		t.Errorf("no-action reason = %q, want %q", snap.NoActionReason, skipBrewManaged)
	}

	// The reason describes the LAST tick, not a high-water mark: a stale reason
	// left standing after the condition cleared would misreport why the updater
	// is idle.
	snap = tr.Record(updateCheckOutcome{Installed: true, Tag: "v2.0.0"}, now)
	if snap.NoActionReason != "" {
		t.Errorf("a tick that ACTED left the stale no-action reason %q standing", snap.NoActionReason)
	}
	if snap.InstalledVersion != "v2.0.0" || snap.LastInstall.IsZero() {
		t.Errorf("an install was not recorded: %+v", snap)
	}
	if !strings.Contains(snap.LatestKnown, "v2.0.0") {
		t.Errorf("latest known = %q, want v2.0.0", snap.LatestKnown)
	}
}
