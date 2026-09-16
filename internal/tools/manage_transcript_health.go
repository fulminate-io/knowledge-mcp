// SPDX-License-Identifier: Apache-2.0

// manage_transcript_health.go — the transcript-upload health block manage(status)
// renders, in both formats, plus the optional-interface read it degrades
// through. Lifted out of manage.go, which is at the package's file-size budget,
// so the dispatch table there can grow an operation without the file's own
// length becoming what blocks it. Nothing but the location changed.

package tools

import (
	"fmt"
	"strings"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/transcriptsync"
)

// transcriptUploadHealth reads the background transcript-upload loop's health snapshot.
// Returns (snapshot, true) only when deps satisfy transcriptUploadHealther AND the
// tracker was wired (a running daemon that reached the loop-spawn stage); (zero, false)
// otherwise — the render sites emit nothing in that case, the SAME degrade contract as
// the pipeline overlay.
func transcriptUploadHealth(deps ClientDeps) (transcriptsync.UploadHealth, bool) {
	th, ok := deps.(transcriptUploadHealther)
	if !ok {
		return transcriptsync.UploadHealth{}, false
	}
	return th.TranscriptUploadHealth()
}

// transcriptHealthTS formats a health timestamp as RFC3339 (UTC), or "never" for the
// zero time.
func transcriptHealthTS(ts time.Time) string {
	if ts.IsZero() {
		return "never"
	}
	return ts.UTC().Format(time.RFC3339)
}

// renderTranscriptHealthText renders the operator-facing transcript-upload health block.
// It keeps the TWO failure axes SEPARATELY visible: a "degraded" line whenever one or
// more files failed to ship on the last tick (regardless of whether the batch as a whole
// shipped), and a "systemic" line for the consecutive-failed-tick streak that drives the
// loop's log escalation. The last error is ALWAYS shown when non-empty — the status can
// never read healthy with a hidden batch error. Consent-off reads as an advanced
// transport clock with the ship clock left untouched, never as an upload success. A
// persistently over-cap session (its watermark never advances, so it re-fails every
// tick) is exactly what the files-failed counters make durably visible here.
func renderTranscriptHealthText(h transcriptsync.UploadHealth) string {
	var b strings.Builder
	b.WriteString("\n\nTranscript upload:\n")
	fmt.Fprintf(&b, "  Last transport OK: %s\n", transcriptHealthTS(h.LastTransportOK))
	fmt.Fprintf(&b, "  Last ship: %s\n", transcriptHealthTS(h.LastShip))
	fmt.Fprintf(&b, "  Lifetime: %d pass(es), %d failure(s); %d file(s) shipped, %d file(s) failed",
		h.TotalPasses, h.TotalFailures, h.FilesShippedLifetime, h.FilesFailedLifetime)
	if h.FilesFailedLastTick > 0 {
		fmt.Fprintf(&b, "\n  degraded: %d file(s) failing to ship this tick; last error: %s",
			h.FilesFailedLastTick, h.LastError)
	}
	if h.ConsecutiveFailures > 0 {
		fmt.Fprintf(&b, "\n  systemic: %d consecutive failed tick(s) (last failure: %s)",
			h.ConsecutiveFailures, transcriptHealthTS(h.LastFailure))
	}
	// Keep the error visible even when no per-file signal carried it (e.g. a consent-fetch
	// or transport error that populated no per-file entries).
	if h.LastError != "" && h.FilesFailedLastTick == 0 {
		fmt.Fprintf(&b, "\n  last error: %s", h.LastError)
	}
	return b.String()
}

// addTranscriptHealthJSON merges the transcript-upload health fields into the status map
// so format:json carries them too. Timestamps are RFC3339 (or "never" for the zero
// time); the last error is the empty string when there is none.
func addTranscriptHealthJSON(m map[string]any, h transcriptsync.UploadHealth) {
	m["transcript_last_transport_ok"] = transcriptHealthTS(h.LastTransportOK)
	m["transcript_last_ship"] = transcriptHealthTS(h.LastShip)
	m["transcript_last_failure"] = transcriptHealthTS(h.LastFailure)
	m["transcript_last_error"] = h.LastError
	m["transcript_consecutive_failures"] = h.ConsecutiveFailures
	m["transcript_files_failed_last_tick"] = h.FilesFailedLastTick
	m["transcript_files_failed_lifetime"] = h.FilesFailedLifetime
	m["transcript_files_shipped_lifetime"] = h.FilesShippedLifetime
	m["transcript_total_passes"] = h.TotalPasses
	m["transcript_total_failures"] = h.TotalFailures
}
