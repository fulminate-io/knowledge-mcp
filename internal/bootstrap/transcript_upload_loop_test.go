// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/auth"
	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
	"github.com/fulminate-io/knowledge-mcp/internal/transcriptsync"
)

// swapTranscriptUploadOnce overrides the upload seam with fn for the duration of the
// test, restoring the real cli.RunTranscriptUploadOnce on cleanup. Keeps the daemon
// loop tests off the keychain + network. fn now takes the injected transport factory
// the daemon threads through.
func swapTranscriptUploadOnce(t *testing.T, fn func(context.Context, func() (*auth.Transport, error)) (transcriptsync.Summary, error)) {
	t.Helper()
	prev := transcriptUploadOnce
	transcriptUploadOnce = fn
	t.Cleanup(func() { transcriptUploadOnce = prev })
}

// stubTransportFn is a no-op transport factory for the loop tests — the swapped
// upload seam never dials, so the transport value is irrelevant.
func stubTransportFn() (*auth.Transport, error) { return nil, nil }

// alwaysLoggedIn / neverLoggedIn are the auth-gate predicates the loop tests pass:
// existing tests assert uploads proceed (logged in), the gate test asserts the
// unauthenticated short-circuit.
func alwaysLoggedIn(context.Context) bool { return true }
func neverLoggedIn(context.Context) bool  { return false }

// TestMaybeStartTranscriptUpload_GatedByNoTranscriptUpload is the "transcript
// loops verifiably not started under headless" guarantee. With NoTranscriptUpload
// set (as --headless does), maybeStartTranscriptUpload returns early: no goroutine
// is spawned, c.transcriptHealth stays nil, and the upload seam is never invoked.
// With it clear, the loops spawn and c.transcriptHealth is wired. The seam counter
// proves no upload fires inside the observation window in EITHER case (the boot
// delay is 1m and the interval 1h — far outside the test window), so the
// nil-vs-wired transcriptHealth is the spawn-decision observable.
func TestMaybeStartTranscriptUpload_GatedByNoTranscriptUpload(t *testing.T) {
	var calls atomic.Int64
	swapTranscriptUploadOnce(t, func(_ context.Context, _ func() (*auth.Transport, error)) (transcriptsync.Summary, error) {
		calls.Add(1)
		return transcriptsync.Summary{}, nil
	})

	// A machine-auth router so the (never-invoked-in-window) LoggedIn method value
	// resolves — mirrors the daemon's c.router.LoggedIn seam.
	newClient := func() *client {
		return &client{router: graphclient.NewRouterWithMachineAuth(nil, "", nil, nil, true)}
	}

	t.Run("headless_skips_spawn", func(t *testing.T) {
		c := newClient()
		c.maybeStartTranscriptUpload(t.Context(), Config{NoTranscriptUpload: true})
		if c.transcriptHealth != nil {
			t.Fatal("transcriptHealth must stay nil under NoTranscriptUpload — the loops were spawned when they should not have been")
		}
	})

	t.Run("normal_spawns", func(t *testing.T) {
		c := newClient()
		// t.Context() is canceled when this subtest ends, unwinding the two spawned
		// goroutines (bootDelay 1m / interval 1h keep them parked on their timers).
		c.maybeStartTranscriptUpload(t.Context(), Config{NoTranscriptUpload: false})
		if c.transcriptHealth == nil {
			t.Fatal("transcriptHealth must be wired when NoTranscriptUpload is false — the loops did not spawn")
		}
	})

	if got := calls.Load(); got != 0 {
		t.Fatalf("upload seam invoked %d times in-window; expected 0 (boot delay 1m / interval 1h are outside the window)", got)
	}
}

// TestRunTranscriptUploadLoop_InvokesEngineOnInterval pins the core contract: the
// daemon's ticker calls the upload engine (the seam) once per interval. With a 5ms
// interval the loop should fire several times inside the observation window; the fake
// signals each invocation on a channel so the test asserts real repeated ticks rather
// than sleeping for a fixed count.
func TestRunTranscriptUploadLoop_InvokesEngineOnInterval(t *testing.T) {
	var calls atomic.Int64
	fired := make(chan struct{}, 16)
	swapTranscriptUploadOnce(t, func(_ context.Context, _ func() (*auth.Transport, error)) (transcriptsync.Summary, error) {
		calls.Add(1)
		select {
		case fired <- struct{}{}:
		default:
		}
		return transcriptsync.Summary{FilesScanned: 3}, nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	const interval = 5 * time.Millisecond
	go runTranscriptUploadLoop(ctx, interval, transcriptsync.NewUploadHealthTracker(), stubTransportFn, alwaysLoggedIn)

	// Require at least three distinct ticks — proof the loop keeps firing on the
	// interval, not a single one-shot invocation.
	for range 3 {
		select {
		case <-fired:
		case <-time.After(2 * time.Second):
			t.Fatalf("ticker did not invoke the upload engine %d times within the deadline (got %d)", 3, calls.Load())
		}
	}
	cancel()
}

// TestRunTranscriptUploadLoop_ExitsOnCtxCancel proves the loop honors the ctx it is
// handed (c.wireCtx in production): once the wiring ctx is canceled — the shutdown edge
// drainOnShutdown drives — the goroutine returns rather than leaking. RED if the loop
// were spawned under a non-cancellable ctx (the C4 leak shape), where the cancel is
// ignored and this deadlines.
func TestRunTranscriptUploadLoop_ExitsOnCtxCancel(t *testing.T) {
	swapTranscriptUploadOnce(t, func(_ context.Context, _ func() (*auth.Transport, error)) (transcriptsync.Summary, error) {
		return transcriptsync.Summary{}, nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		runTranscriptUploadLoop(ctx, 5*time.Millisecond, transcriptsync.NewUploadHealthTracker(), stubTransportFn, alwaysLoggedIn)
		close(done)
	}()

	// Let the loop enter its select, then cancel (the shutdown edge).
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case <-done:
		// Correct: the loop observed the ctx cancel and returned — no leak.
	case <-time.After(2 * time.Second):
		t.Fatal("runTranscriptUploadLoop did not stop on ctx cancel — a loop spawned under a non-cancellable ctx would hang exactly like this")
	}
}

// TestLogTranscriptUploadOutcome_SwallowsError is the fail-loud-degrade guard: a batch
// error is logged and swallowed, never propagated — logTranscriptUploadOutcome returns
// normally so the ticker continues to the next tick instead of crashing the daemon.
func TestLogTranscriptUploadOutcome_SwallowsError(t *testing.T) {
	swapTranscriptUploadOnce(t, func(_ context.Context, _ func() (*auth.Transport, error)) (transcriptsync.Summary, error) {
		return transcriptsync.Summary{}, context.DeadlineExceeded
	})
	// No panic, no propagation — the helper absorbs the error.
	logTranscriptUploadOutcome(context.Background(), transcriptsync.NewUploadHealthTracker(), stubTransportFn, alwaysLoggedIn)
}

// TestLogTranscriptUploadOutcome_TracksStreakAndResets proves the health tracker is
// Record-ed on every tick regardless of the log branch: a systemic-failing tick (error +
// nothing shipped) advances ConsecutiveFailures, and a shipped tick resets it.
func TestLogTranscriptUploadOutcome_TracksStreakAndResets(t *testing.T) {
	var shouldFail atomic.Bool
	shouldFail.Store(true)
	swapTranscriptUploadOnce(t, func(_ context.Context, _ func() (*auth.Transport, error)) (transcriptsync.Summary, error) {
		if shouldFail.Load() {
			return transcriptsync.Summary{FilesUploaded: 0}, context.DeadlineExceeded
		}
		return transcriptsync.Summary{FilesUploaded: 1, RowsShipped: 5}, nil
	})

	tracker := transcriptsync.NewUploadHealthTracker()
	logTranscriptUploadOutcome(context.Background(), tracker, stubTransportFn, alwaysLoggedIn)
	logTranscriptUploadOutcome(context.Background(), tracker, stubTransportFn, alwaysLoggedIn)
	if snap := tracker.Snapshot(); snap.ConsecutiveFailures != 2 {
		t.Fatalf("expected ConsecutiveFailures=2 after two failing ticks, got %d", snap.ConsecutiveFailures)
	}

	shouldFail.Store(false)
	logTranscriptUploadOutcome(context.Background(), tracker, stubTransportFn, alwaysLoggedIn)
	if snap := tracker.Snapshot(); snap.ConsecutiveFailures != 0 {
		t.Fatalf("expected a shipped tick to reset the streak, got %d", snap.ConsecutiveFailures)
	}
}

// TestLogTranscriptUploadOutcome_EscalatesAtThreshold drives the lone-over-cap quiet-tick
// steady state — a batch that errors, ships nothing, and reports one per-file failure
// each tick — and asserts the outcome helper escalates from Warn to Error once the
// systemic streak reaches transcriptUploadFailureEscalation. The log level is captured
// via a buffer slog handler; the snapshot carries LastError + FilesFailedLastTick==1.
func TestLogTranscriptUploadOutcome_EscalatesAtThreshold(t *testing.T) {
	swapTranscriptUploadOnce(t, func(_ context.Context, _ func() (*auth.Transport, error)) (transcriptsync.Summary, error) {
		return transcriptsync.Summary{
			FilesUploaded: 0,
			Files:         []transcriptsync.FileSummary{{Session: "stuck", Err: "over cap"}},
		}, errors.New("session parquet exceeds cap")
	})

	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	tracker := transcriptsync.NewUploadHealthTracker()
	// Below the threshold: Warn, not Error.
	for i := 1; i < transcriptUploadFailureEscalation; i++ {
		logTranscriptUploadOutcome(context.Background(), tracker, stubTransportFn, alwaysLoggedIn)
	}
	if strings.Contains(buf.String(), "level=ERROR") {
		t.Fatalf("escalated to ERROR before reaching the threshold:\n%s", buf.String())
	}
	buf.Reset()

	// The tick that reaches the threshold escalates to Error.
	logTranscriptUploadOutcome(context.Background(), tracker, stubTransportFn, alwaysLoggedIn)
	out := buf.String()
	if !strings.Contains(out, "level=ERROR") {
		t.Fatalf("expected ERROR escalation at the threshold, got:\n%s", out)
	}
	if !strings.Contains(out, "persistent failure") {
		t.Fatalf("expected the persistent-failure message, got:\n%s", out)
	}

	snap := tracker.Snapshot()
	if snap.ConsecutiveFailures != transcriptUploadFailureEscalation {
		t.Fatalf("expected ConsecutiveFailures=%d, got %d", transcriptUploadFailureEscalation, snap.ConsecutiveFailures)
	}
	if snap.FilesFailedLastTick != 1 {
		t.Fatalf("expected FilesFailedLastTick=1, got %d", snap.FilesFailedLastTick)
	}
	if snap.LastError == "" {
		t.Fatal("expected LastError to be set on the failing steady state")
	}
}

// TestLogTranscriptUploadOutcome_PartialSuccessDoesNotEscalate proves a busy tick that
// ships >0 sessions but also has a per-file failure is NOT a systemic failure: the streak
// stays 0 (no escalation) even though the degraded axis recorded the failing file.
func TestLogTranscriptUploadOutcome_PartialSuccessDoesNotEscalate(t *testing.T) {
	swapTranscriptUploadOnce(t, func(_ context.Context, _ func() (*auth.Transport, error)) (transcriptsync.Summary, error) {
		return transcriptsync.Summary{
			FilesUploaded: 2,
			Files: []transcriptsync.FileSummary{
				{Session: "ok"},
				{Session: "stuck", Err: "over cap"},
			},
		}, errors.New("one file failed")
	})

	tracker := transcriptsync.NewUploadHealthTracker()
	for range 5 {
		logTranscriptUploadOutcome(context.Background(), tracker, stubTransportFn, alwaysLoggedIn)
	}
	snap := tracker.Snapshot()
	if snap.ConsecutiveFailures != 0 {
		t.Fatalf("a partial-success tick must not advance the systemic streak, got %d", snap.ConsecutiveFailures)
	}
	if snap.FilesFailedLastTick != 1 {
		t.Fatalf("the degraded axis should still count the failing file, got %d", snap.FilesFailedLastTick)
	}
	if snap.LastError == "" {
		t.Fatal("LastError must be set even on a partial-success tick")
	}
}

// TestLogTranscriptUploadOutcome_NotAuthenticatedShortCircuits verifies the T3
// cloud-auth gate: with a loggedIn predicate returning false the tick short-circuits
// at the top — the upload seam (transcriptUploadOnce) is NEVER invoked (no transport
// built, no consent probe fired), the failure streak stays 0 across repeated ticks
// (an unauthenticated tick is not a failure), and NOTHING is logged at WARN/Error. A
// NoAuth/never-logged-in daemon must run zero consent probes and emit zero transcript
// WARN/Error noise.
func TestLogTranscriptUploadOutcome_NotAuthenticatedShortCircuits(t *testing.T) {
	var invoked atomic.Int64
	swapTranscriptUploadOnce(t, func(_ context.Context, _ func() (*auth.Transport, error)) (transcriptsync.Summary, error) {
		invoked.Add(1)
		return transcriptsync.Summary{}, nil
	})

	// Capture logs at Debug so any WARN/Error would be visible.
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	// A transport factory that MUST NOT be reached (the gate short-circuits before it).
	failTransportFn := func() (*auth.Transport, error) {
		t.Fatal("transport factory must not be built on the unauthenticated skip")
		return nil, nil
	}

	tracker := transcriptsync.NewUploadHealthTracker()
	for range 3 {
		logTranscriptUploadOutcome(context.Background(), tracker, failTransportFn, neverLoggedIn)
	}

	if got := invoked.Load(); got != 0 {
		t.Fatalf("upload seam must never be invoked when not authenticated, got %d invocations", got)
	}
	if snap := tracker.Snapshot(); snap.ConsecutiveFailures != 0 {
		t.Fatalf("unauthenticated ticks must not advance the failure streak, got %d", snap.ConsecutiveFailures)
	}
	out := buf.String()
	if strings.Contains(out, "level=WARN") || strings.Contains(out, "level=ERROR") {
		t.Fatalf("unauthenticated ticks must emit no WARN/Error, got:\n%s", out)
	}
}
