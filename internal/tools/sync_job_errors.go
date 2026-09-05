// SPDX-License-Identifier: Apache-2.0

// sync_job_errors.go holds the terminal errors the sync-push job poll can end
// on, split from sync_job_poll.go for the repo's file-length cap.
//
// They are one family and are read as one: each is a state the poll can leave
// in, and each carries the whole sentence an operator needs — the job id, what
// is known about the job, and what to do next. The marker interface below is
// what keeps that wording intact on the way out.

package tools

import (
	"fmt"
	"time"
)

// syncJobError marks the poll's OWN terminal errors: the ones whose message is
// already operator-facing and complete. pushGraph surfaces these verbatim
// instead of routing them through wrapPushErr, whose 401/403 login guidance
// would overwrite a gateway-supplied failure reason with advice about
// credentials that were never the problem.
type syncJobError interface {
	error
	syncJobError()
}

// syncJobFailedError is the gateway reporting state "failed". Reason is the
// gateway's verbatim wording.
type syncJobFailedError struct {
	JobID  string
	Reason string
}

func (e *syncJobFailedError) syncJobError() {}

func (e *syncJobFailedError) Error() string {
	reason := e.Reason
	if reason == "" {
		// The state is authoritative even when the reason field is not set;
		// saying so is honest, and inventing a cause would not be.
		reason = "the gateway reported the job failed without a reason"
	}
	return fmt.Sprintf("cloud ingest job %s failed: %s", e.JobID, reason)
}

// syncJobDeadlineError is the poll running out its own deadline with the job
// still in progress. This is NOT a statement that the ingest failed: the job
// keeps running on the gateway, which is exactly why the message hands back the
// job id to poll again rather than suggesting a re-push.
type syncJobDeadlineError struct {
	JobID    string
	Waited   time.Duration
	Deadline time.Duration
	// LastState is the last state a poll actually PARSED, and Observed says
	// whether any poll ever did. The pair exists because the two cases warrant
	// different sentences: a deadline reached while watching a running job may
	// honestly say the ingest is still running, and a deadline reached without
	// a single successful poll may not — the client never learned anything
	// about that job and must not assert a state it never saw.
	LastState string
	Observed  bool
	// Transients counts the poll failures that were retried on the way here.
	// A deadline reached after a run of them is a different diagnosis from one
	// reached with a healthy connection — the first says the client could not
	// see, the second says the ingest did not finish — and an operator should
	// not have to guess which they are looking at.
	Transients int
}

func (e *syncJobDeadlineError) syncJobError() {}

func (e *syncJobDeadlineError) Error() string {
	waited := e.Waited.Round(time.Second)
	if !e.Observed {
		return fmt.Sprintf(
			"cloud ingest job %s was never successfully observed within %s (client poll deadline %s): "+
				"all %d poll(s) failed, so this client never learned the job's state and cannot say "+
				"whether the ingest finished; do NOT re-push, poll job id %s again",
			e.JobID, waited, e.Deadline, e.Transients, e.JobID)
	}
	msg := fmt.Sprintf(
		"cloud ingest job %s was last observed in state %q after %s (client poll deadline %s) — "+
			"the ingest is still running on the server; do NOT re-push, poll job id %s again",
		e.JobID, e.LastState, waited, e.Deadline, e.JobID)
	if e.Transients > 0 {
		msg += fmt.Sprintf(" (%d poll(s) failed and were retried along the way, so the job's "+
			"last reported state may be older than the deadline)", e.Transients)
	}
	return msg
}

// syncJobCanceledError is the caller's context ending the wait: the command was
// interrupted, or an enclosing deadline fired.
//
// It is a first-class error rather than a bare ctx.Err() for the same reason the
// deadline is: the ingest keeps running on the gateway, the client is holding
// the job id at the moment it stops watching, and "context canceled" tells an
// operator none of that. Canceling the client does not cancel the ingest, and a
// re-push would re-upload and re-merge work the gateway already accepted.
//
// The cause is wrapped, so errors.Is(err, context.Canceled) still holds for any
// caller that checks.
type syncJobCanceledError struct {
	JobID     string
	LastState string
	Observed  bool
	Waited    time.Duration
	Cause     error
}

func (e *syncJobCanceledError) syncJobError() {}

func (e *syncJobCanceledError) Unwrap() error { return e.Cause }

func (e *syncJobCanceledError) Error() string {
	observed := "this client never observed its state"
	if e.Observed {
		observed = fmt.Sprintf("its last observed state was %q", e.LastState)
	}
	return fmt.Sprintf(
		"the wait for cloud ingest job %s ended after %s (%v) and %s — "+
			"stopping the client does NOT stop the ingest, which is still running on the server; "+
			"do NOT re-push, poll job id %s again",
		e.JobID, e.Waited.Round(time.Millisecond), e.Cause, observed, e.JobID)
}

// syncJobNotFoundError is a 404 from job-status: the job id is unknown to the
// gateway, or belongs to another account. The gateway deliberately does not
// distinguish the two (an account-scoped record is not an existence oracle), so
// neither does this message.
type syncJobNotFoundError struct {
	JobID string
}

func (e *syncJobNotFoundError) syncJobError() {}

func (e *syncJobNotFoundError) Error() string {
	return fmt.Sprintf(
		"cloud ingest job %s is not known to the server (unknown job, or a job belonging to another account) — "+
			"the ingest's outcome cannot be determined from this client",
		e.JobID)
}

// syncJobStateError is a state string outside the approved three. Bad input
// errors: an unrecognized state is never rounded to in_progress (which would
// poll forever) nor to complete (which would report a push that may not have
// landed).
type syncJobStateError struct {
	JobID string
	State string
}

func (e *syncJobStateError) syncJobError() {}

func (e *syncJobStateError) Error() string {
	return fmt.Sprintf(
		"cloud ingest job %s reported an unrecognized state %q (expected %s, %s or %s) — "+
			"this client cannot interpret the server's answer",
		e.JobID, e.State, syncJobStateInProgress, syncJobStateComplete, syncJobStateFailed)
}
