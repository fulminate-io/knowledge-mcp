// SPDX-License-Identifier: Apache-2.0

// client_transcript_upload.go — the daemon's background transcript-upload loop.
//
// The `knowledge serve` daemon ships changed local CLI transcripts (~/.claude,
// ~/.codex) to the cloud on a fixed hourly cadence so /usage analytics stay fresh
// WITHOUT a manual `transcript-upload` invocation (the manual-only path let a large
// backlog accumulate un-shipped). It reuses the SAME upload engine the subcommand runs
// via cli.RunTranscriptUploadOnce — file-level incremental over the per-file size/mtime
// watermark, consent-gated, best-effort. The loop is spawned under c.wireCtx so
// drainOnShutdown cancels it, mirroring runSegmentReconcileLoop.

package bootstrap

import (
	"context"
	"log/slog"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/auth"
	"github.com/fulminate-io/knowledge-mcp/internal/cli"
	"github.com/fulminate-io/knowledge-mcp/internal/transcriptsync"
)

// transcriptUploadInterval is the hourly cadence of runTranscriptUploadLoop — a fixed
// default for v1 (not config-driven). One batch skips every byte-identical session
// (the size/mtime watermark short-circuits before parse), so the steady-state cost of a
// tick with no changed transcripts is a corpus stat walk + one consent probe.
const transcriptUploadInterval = 1 * time.Hour

// transcriptUploadBootDelay is the delay before the daemon's FIRST upload pass fires
// after boot. It runs OFF the bind / readiness critical path (spawned with `go` from
// wireRuntimesBackground) so a freshly-started daemon catches up on any backlog without
// waiting a full hour, yet the delay keeps it well clear of the MCP listener bind.
const transcriptUploadBootDelay = 1 * time.Minute

// transcriptUploadOnce is the upload-batch function the daemon's ticker invokes. It is a
// package var so a test can substitute a counting fake for the real
// cli.RunTranscriptUploadOnce (which needs the keychain + network); production leaves the
// default. Mirrors the buildSyncTransportFn overridable-seam idiom. It takes the
// transport factory the daemon injects (c.buildCloudSyncTransport) so the loop presents
// the shared cloud token source.
//
//nolint:gochecknoglobals // overridable upload seam for testability; mirrors buildSyncTransportFn.
var transcriptUploadOnce = cli.RunTranscriptUploadOnce

// transcriptUploadFailureEscalation is the consecutive-systemic-failure count at which
// the upload loop escalates its failure log from Warn to Error. A systemic failure is a
// tick that both errored AND shipped nothing; a single ship resets the streak. A session
// permanently over the raw/parquet cap never advances its watermark, so on a quiet
// steady-state tick it is the only change, errors, and ships nothing — it drives this
// streak and WILL escalate here. That is intended: a file that can never ship is a real
// persistent failure and must get loud. The message's files_failing field lets an
// operator tell a stuck/over-cap file apart from a total transport outage.
const transcriptUploadFailureEscalation = 3

// logTranscriptUploadOutcome runs one upload batch through the seam, records the outcome
// into the health tracker, and logs the result. It is the fail-loud degrade boundary: an
// error (consent unreachable, transport/keychain build, per-file failures) is logged and
// SWALLOWED — it never crashes the daemon or blocks the ticker, and the next tick
// retries. The tracker Record runs FIRST so every tick is counted regardless of the log
// branch taken. A batch error escalates from Warn to Error once the systemic-failure
// streak reaches transcriptUploadFailureEscalation, carrying both the streak length and
// the per-file failing count so the operator can distinguish the failure shapes. A
// consent-off skip logs at Info; a tick with no changed transcripts logs at Debug
// (steady-state quiet); a real ship logs the tallies at Info.
//
// CLOUD-AUTH GATE: transcript upload requires a cloud credential. A not-logged-in
// daemon (--no-auth, or one never `knowledge login`-ed and with no --auth-token)
// has none, so the tick short-circuits at the TOP — mirroring the segment
// capability-gate — BEFORE building a transport or probing consent: no transport
// is built, no consent probe fires, and the health tracker is left UNTOUCHED so
// LastTransportOK keeps meaning "the transport was actually exercised" and the
// consecutive-failure streak never advances on an unauthenticated tick. loggedIn
// is c.router.LoggedIn (machineAuth || live keychain login, re-checked each tick),
// so a mid-session login begins uploads with no restart and a machine-authed
// (--auth-token) daemon is treated as logged in. The gate short-circuits ONLY on
// !loggedIn — it never masks a genuine failure for an authenticated daemon, and
// consent-disabled semantics (a Skipped from transcriptsync.Run when the per-account
// flag is off) stay unchanged and are still reached when authenticated.
func logTranscriptUploadOutcome(
	ctx context.Context,
	tracker *transcriptsync.UploadHealthTracker,
	transportFn func() (*auth.Transport, error),
	loggedIn func(context.Context) bool,
) {
	if !loggedIn(ctx) {
		slog.Info("transcript upload: skipped — not authenticated (transcript upload requires cloud login)")
		return
	}
	summary, err := transcriptUploadOnce(ctx, transportFn)
	snap := tracker.Record(summary, err, time.Now())
	if err != nil {
		if snap.ConsecutiveFailures >= transcriptUploadFailureEscalation {
			slog.Error("transcript upload: persistent failure",
				"consecutive_failed_ticks", snap.ConsecutiveFailures,
				"files_failing", snap.FilesFailedLastTick,
				"error", err)
		} else {
			slog.Warn("transcript upload: batch failed (will retry next tick)",
				"consecutive_failed_ticks", snap.ConsecutiveFailures,
				"files_failing", snap.FilesFailedLastTick,
				"error", err)
		}
		return
	}
	if summary.Skipped != "" {
		slog.Info("transcript upload: skipped", "reason", summary.Skipped)
		return
	}
	if summary.FilesUploaded == 0 {
		slog.Debug("transcript upload: no changed transcripts", "files_scanned", summary.FilesScanned)
		return
	}
	slog.Info("transcript upload: shipped changed transcripts",
		"files_scanned", summary.FilesScanned,
		"files_uploaded", summary.FilesUploaded,
		"rows_shipped", summary.RowsShipped)
}

// maybeStartTranscriptUpload spawns the daemon's background transcript-upload
// loops UNLESS f.NoTranscriptUpload is set (which --headless does via
// applyHeadless) — an embedded/supervisor-managed daemon must ship nothing. When
// gated off it returns early leaving c.transcriptHealth nil; the
// TranscriptUploadHealth() accessor nil-guards, so the manage(status) surface
// renders the upload health as absent rather than a healthy zero snapshot.
//
// When enabled it ships changed local CLI transcripts (~/.claude, ~/.codex) to
// the cloud on a fixed hourly cadence so /usage analytics stay fresh WITHOUT a
// manual `transcript-upload` invocation (the manual-only path let a large backlog
// pile up un-shipped). Reuses the SAME upload engine the subcommand runs
// (cli.RunTranscriptUploadOnce → transcriptsync.Run) — file-level incremental via
// the per-file size/mtime watermark, consent-gated, best-effort. Spawned under
// ctx (c.wireCtx) so drainOnShutdown cancels both goroutines (no leak); a
// boot-delay one-shot catches a freshly started daemon up without waiting the
// full hour. Independent of the LLM pipeline (it must run even under
// --no-llm-pipeline / no summarizer configured). The loops present the daemon's
// SINGLE shared cloud token source (c.buildCloudSyncTransport) and are gated on
// live cloud-auth state (c.router.LoggedIn) so an unauthenticated daemon runs
// zero consent probes and logs zero transcript WARN/Error — a mid-session login
// begins uploads with no restart.
func (c *client) maybeStartTranscriptUpload(ctx context.Context, f Config) {
	if f.NoTranscriptUpload {
		slog.Debug("transcript upload skipped (headless)")
		return
	}
	c.transcriptHealth = transcriptsync.NewUploadHealthTracker()
	go bootDelayTranscriptUpload(ctx, c.transcriptHealth, c.buildCloudSyncTransport, c.router.LoggedIn)
	go runTranscriptUploadLoop(ctx, transcriptUploadInterval, c.transcriptHealth, c.buildCloudSyncTransport, c.router.LoggedIn)
}

// bootDelayTranscriptUpload runs ONE upload pass transcriptUploadBootDelay after boot,
// OFF the bind critical path, so a freshly started daemon catches up without waiting the
// full transcriptUploadInterval. Spawned with `go` from wireRuntimesBackground so the
// delay is awaited HERE, never on the wiring path. Exits promptly on ctx.Done (no leak).
func bootDelayTranscriptUpload(
	ctx context.Context,
	tracker *transcriptsync.UploadHealthTracker,
	transportFn func() (*auth.Transport, error),
	loggedIn func(context.Context) bool,
) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(transcriptUploadBootDelay):
		logTranscriptUploadOutcome(ctx, tracker, transportFn, loggedIn)
	}
}

// runTranscriptUploadLoop fires logTranscriptUploadOutcome on a fixed-interval ticker
// until ctx is canceled — the PERIODIC trigger that keeps cloud transcript analytics
// fresh. Mirrors runSegmentReconcileLoop's select{ctx.Done / ticker.C} shape; exits
// promptly on ctx.Done (no leak). ctx is c.wireCtx, which drainOnShutdown cancels on
// shutdown, so the loop is unwound. A per-batch error is logged inside the outcome
// helper and the loop continues to the next tick — an upload failure never crashes the
// daemon or blocks the HTTP listener.
func runTranscriptUploadLoop(
	ctx context.Context,
	interval time.Duration,
	tracker *transcriptsync.UploadHealthTracker,
	transportFn func() (*auth.Transport, error),
	loggedIn func(context.Context) bool,
) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			logTranscriptUploadOutcome(ctx, tracker, transportFn, loggedIn)
		}
	}
}
