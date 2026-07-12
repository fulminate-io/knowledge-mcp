// SPDX-License-Identifier: Apache-2.0

// health.go — the upload-health state for the daemon's background transcript-upload
// loop. It classifies each batch Summary + error into an operator-visible snapshot so
// a persistent upload failure never ships nothing invisibly.
//
// The type lives in transcriptsync because it classifies transcriptsync.Summary and
// transcriptsync must not import tools or bootstrap (see ship.go). Owning the snapshot
// here makes it reachable by both the loop that WRITES it (bootstrap) and the status
// surface that READS it (tools) with no import cycle — the same sharing shape as the
// pipeline metrics snapshot.

package transcriptsync

import (
	"sync"
	"time"
)

// UploadHealth is an immutable value snapshot of the transcript-upload loop's health,
// classified from the per-tick batch Summary + error. It exposes TWO independent axes
// so a failure is visible on EVERY tick shape:
//
//   - SYSTEMIC streak: ConsecutiveFailures increments when a tick both errored AND
//     shipped nothing (err != nil && FilesUploaded == 0). This drives the loop's
//     log-level escalation. A shipped tick resets it to zero.
//   - DEGRADED per-file signal: FilesFailedLastTick (and the FilesFailedLifetime
//     running total) count sessions that failed on the last tick regardless of whether
//     the batch as a whole shipped. On a busy tick where other sessions ship, the
//     systemic streak resets but a stuck file still surfaces here — it is never masked.
//
// The two timestamps are deliberately SPLIT: LastTransportOK marks a completed pass /
// a reachable server (advanced even on a consent-off skip), while LastShip marks a tick
// that actually confirmed >= 1 session parquet. A consent-off tick advances
// LastTransportOK but never LastShip, so consent-off can never read as an upload
// success. LastError is set on ANY errored tick — including a partial-success tick —
// so the status can never read healthy with a hidden batch error.
type UploadHealth struct {
	LastTransportOK     time.Time
	LastShip            time.Time
	LastFailure         time.Time
	LastError           string
	ConsecutiveFailures int
	FilesFailedLastTick int

	TotalPasses          int64
	TotalFailures        int64
	FilesShippedLifetime int64
	FilesFailedLifetime  int64
}

// UploadHealthTracker is the mutex-guarded owner of the live UploadHealth. Record folds
// one tick's outcome into it and returns the post-update snapshot; Snapshot returns an
// independent value copy for a reader (the status surface).
type UploadHealthTracker struct {
	mu sync.Mutex
	h  UploadHealth
}

// NewUploadHealthTracker returns a tracker with zero-value health (no pass recorded
// yet — every timestamp is the zero time and every counter is zero).
func NewUploadHealthTracker() *UploadHealthTracker {
	return &UploadHealthTracker{}
}

// Record is the SINGLE classification source: it folds one upload tick's Summary + err
// into the health state and returns the post-update snapshot so the caller can read
// ConsecutiveFailures for its escalation decision without a second lock acquisition.
// now is injected for deterministic tests.
//
// Ordering is load-bearing:
//  1. Count per-file failures independent of the batch outcome (the degraded axis).
//  2. Record LastError/LastFailure on ANY errored tick, INCLUDING a partial success —
//     the status must never read healthy with a non-empty batch error hidden.
//  3. A consent-off skip is transport-reached, NOT a failure: advance LastTransportOK,
//     reset the streak, leave LastShip untouched.
//  4. A systemic failure (errored AND shipped nothing) advances the escalation streak;
//     LastTransportOK is deliberately NOT advanced (the transport may be down).
//  5. Otherwise (>= 1 shipped, or a clean zero-change pass) the transport is healthy:
//     advance LastTransportOK, reset the streak, and on an actual ship advance LastShip.
func (t *UploadHealthTracker) Record(summary Summary, err error, now time.Time) UploadHealth {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.h.TotalPasses++

	filesFailed := 0
	for _, f := range summary.Files {
		if f.Err != "" {
			filesFailed++
		}
	}
	t.h.FilesFailedLastTick = filesFailed
	t.h.FilesFailedLifetime += int64(filesFailed)

	if err != nil {
		t.h.LastError = err.Error()
		t.h.LastFailure = now
	}

	switch {
	case summary.Skipped != "":
		// Consent off (or otherwise gated): the transport was reached and this is not a
		// failure. Advance the transport clock and reset the streak; do not touch LastShip.
		t.h.LastTransportOK = now
		t.h.ConsecutiveFailures = 0
	case err != nil && summary.FilesUploaded == 0:
		// Systemic failure: the batch errored and nothing shipped. Advance the escalation
		// streak; leave LastTransportOK where it was (the server may be unreachable).
		t.h.ConsecutiveFailures++
		t.h.TotalFailures++
	default:
		// Healthy pass: >= 1 session shipped, or a clean zero-change tick.
		t.h.LastTransportOK = now
		t.h.ConsecutiveFailures = 0
		if summary.FilesUploaded > 0 {
			t.h.LastShip = now
			t.h.FilesShippedLifetime += int64(summary.FilesUploaded)
		}
	}

	return t.h
}

// Snapshot returns an independent value copy of the current health for a reader.
func (t *UploadHealthTracker) Snapshot() UploadHealth {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.h
}
