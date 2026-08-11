// SPDX-License-Identifier: Apache-2.0

package graphclient

import (
	"context"
	"log/slog"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/hivemonitor"
)

// resolveTranscript is the transcript-resolution seam: the package-level
// indirection through which the per-session harness cache reaches
// hivemonitor.ResolveTranscript. It exists because that function's own seams
// (execRunner, homeDir) are unexported and unreachable from this
// package, so a test cannot otherwise drive the cache or the retry bound.
//
//nolint:gochecknoglobals // overridable resolution seam for testability; mirrors the hivemonitor exec/home seams.
var resolveTranscript = hivemonitor.ResolveTranscript

// defaultHarnessResolveRetry is how long a session waits after an unresolved
// resolution attempt before trying again. A session whose agent has not written
// its first turn yet has no transcript on disk, so resolution legitimately fails
// for a while and must be retried — but not on every request, because an
// unresolved attempt walks the transcript stores and can shell out.
const defaultHarnessResolveRetry = 5 * time.Second

// harnessSessionID returns the session's HARNESS session-id — the identity the
// cloud keys a hive member on, read from the agent's on-disk transcript — or ""
// when the transcript has not resolved yet.
//
// It resolves ONCE per session and caches the result: hivemonitor.ResolveTranscript
// is not cheap (the claude arm reads one project directory; the codex arm shells
// `lsof` and, on a miss, walks the rollout directory reading the first line of
// every file), so resolving per outbound RPC would put a directory walk and a
// process spawn on the hot path of every tool call. A resolved session pays
// exactly one resolution for its whole life; an unresolved one pays at most one
// attempt per h.harnessRetry. A peer that binds no claude transcript falls
// through to the codex chain, so an unresolved attempt costs both arms — which
// is precisely what the retry throttle bounds.
//
// LOCKING: harnessMu is held across the WHOLE body, INCLUDING the
// resolveTranscript call. That is deliberate rather than an oversight of the
// usual "never hold a lock across I/O" rule: the mutex is PER-SESSION and has no
// other holder, and the within-session single-in-flight invariant (see
// httpSession) means at most one further caller can be waiting on it — so a
// blocked second caller waits out exactly one resolution instead of launching a
// duplicate ps/lsof. Releasing around the call would let two concurrent requests
// in the same session both miss the cache and both spawn.
//
// A resolve error is logged and treated as unresolved, never as fatal — the same
// posture ResolveTranscript's other callers take.
func (h *HTTPServer) harnessSessionID(ctx context.Context, s *httpSession) string {
	s.harnessMu.Lock()
	defer s.harnessMu.Unlock()

	if s.harnessID != "" {
		return s.harnessID
	}

	now := time.Now()
	if now.Sub(s.harnessAttempt) < h.harnessRetry {
		return ""
	}
	s.harnessAttempt = now

	handle, err := resolveTranscript(ctx, hivemonitor.SessionSnapshot{
		ID:   s.id,
		Cwd:  s.cwd,
		PID:  s.pid,
		Comm: s.comm,
	})
	if err != nil {
		slog.Warn("knowledge serve: harness transcript resolve error", "session", s.id, "error", err)
		return ""
	}
	s.harnessID = handle.HarnessSessionID
	return s.harnessID
}
