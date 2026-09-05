// SPDX-License-Identifier: Apache-2.0

// sync_job_poll.go holds the client half of the ASYNCHRONOUS sync confirm: the
// job-status wire DTOs, the bounded poll loop pushGraph drives after confirm
// hands back a job id, its cadence, and the classifier that decides which
// failures are worth asking again. The errors the poll ends on are one family
// and live in sync_job_errors.go.
//
// Why the poll exists. The synchronous confirm held one HTTP request open for
// the whole download + decrypt + forward + ingest. Measured ingests ran 48 s,
// 418 s and 601 s behind an edge proxy that gives up at about 100 s, so a push
// the gateway COMPLETED with 200 was handed to the client as a 524 and reported
// as a failure — a successful ingest and a failed one were indistinguishable,
// and a retry re-uploaded work the gateway had already accepted. Confirm now
// answers 202 with a job id at once and the ingest runs behind it; this file is
// how the client finds out how that job ended.
//
// It lives beside intercept_sync_push.go rather than inside it because that
// file was itself split off intercept_sync.go at a 500-line cap, and the poll
// is a self-contained concern: one loop, one schedule, one error family.

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"net/http"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/auth"
)

// The three job states the gateway reports. Any other value is a wire the
// client does not understand and ends the poll loudly rather than being
// coerced into one of these.
const (
	syncJobStateInProgress = "in_progress"
	syncJobStateComplete   = "complete"
	syncJobStateFailed     = "failed"
)

// syncConfirmResponse is the 202 body of POST /v1/sync/confirm. Confirm no
// longer holds the connection through the ingest: it validates, hands the
// sealed object to an ingest job, and answers at once with the job's identity.
// The push is not finished when this lands — pollSyncJob decides that. It sits
// here rather than beside syncConfirmRequest because the job id is what the
// poll consumes, and the async half of the confirm wire is this file's subject.
type syncConfirmResponse struct {
	JobID string `json:"job_id"`
	State string `json:"state"`
}

// syncJobStatusRequest is the body of POST /v1/sync/job-status.
type syncJobStatusRequest struct {
	JobID string `json:"job_id"`
}

// syncJobStatusResponse is the 200 body of POST /v1/sync/job-status.
//
// Reason is populated on the failed state only and carries the gateway's own
// wording verbatim — it is the operator's only account of why an ingest the
// client can no longer see failed, so it is never re-worded or re-classified on
// the way out (see pushGraph's terminal-error routing).
//
// The fields are exactly the approved wire and no more. An earlier cut carried
// optional ingest counts on the reasoning that a gateway reporting them should
// have them rendered. The gateway's producing type has no such fields and never
// sets them, so the client branch reading them could only ever be reached by a
// test scripting a body the far side cannot produce — coverage of nothing.
// Counts would be a wire change and go to the owner as one before a field
// exists here to read them.
type syncJobStatusResponse struct {
	JobID      string `json:"job_id"`
	State      string `json:"state"`
	Reason     string `json:"reason,omitempty"`
	GraphType  string `json:"graph_type"`
	Name       string `json:"name"`
	StartedAt  string `json:"started_at"`
	FinishedAt string `json:"finished_at,omitempty"`
}

// syncJobPollSchedule bounds the poll: the cadence band between job-status
// calls, and how long to keep asking.
type syncJobPollSchedule struct {
	// base is the floor of the cadence band. 5 s spends 12 requests/minute
	// against the per-account API budget, whose floor is 200/minute and is
	// SHARED with presign, confirm and pull.
	base time.Duration
	// ceiling is the top of the cadence band. The wait is drawn uniformly
	// from [base, ceiling] on EVERY poll — this is a steady cadence with
	// jitter, not a backoff, because the owner's rule is that a poll keeps
	// asking every five to ten seconds until the job is terminal. Backing off
	// would make a long ingest's news progressively staler for no saving:
	// the request is tiny and the budget is nowhere near spent.
	ceiling time.Duration
	// deadline bounds the whole poll. Sized above BOTH known bounds on an
	// ingest: the longest one measured end to end ran 600.9 s, and the
	// gateway's own forward client gives up at 10 minutes, so a job that has
	// not finished in 15 minutes will not finish under a bound this client
	// can observe. Expiry is reported as its own error naming the job id, not
	// as a failure.
	deadline time.Duration
}

// syncJobPoll is the schedule pushGraph drives. It is a package var solely so
// tests can compress it — production never reassigns it, in the same spirit as
// the syncTransportBuilder seam next door.
var syncJobPoll = syncJobPollSchedule{
	base:     5 * time.Second,
	ceiling:  10 * time.Second,
	deadline: 15 * time.Minute,
}

// syncPollCadence draws the wait before each job-status call uniformly from
// [base, ceiling].
//
// It is a CADENCE, not a backoff, and the distinction is the owner's rule: the
// poll keeps asking on the same interval until the job is terminal, including
// after a transient failure, so nothing here grows with the attempt count. The
// jitter is not decoration — several clients pushing at once must not settle
// into a synchronized poll burst against a per-account budget they share.
type syncPollCadence struct {
	base    time.Duration
	ceiling time.Duration
	rng     *rand.Rand
}

func newSyncPollCadence(base, ceiling time.Duration) *syncPollCadence {
	if base <= 0 {
		base = 5 * time.Second
	}
	if ceiling < base {
		ceiling = base
	}
	return &syncPollCadence{
		base:    base,
		ceiling: ceiling,
		rng:     rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

// next returns the wait before the next poll, in [base, ceiling].
func (c *syncPollCadence) next() time.Duration {
	span := int64(c.ceiling - c.base)
	if span <= 0 {
		return c.base
	}
	return c.base + time.Duration(c.rng.Int63n(span+1))
}

// pollSyncJob asks POST /v1/sync/job-status for jobID until the job reaches a
// terminal state, the schedule's deadline expires, or the context is done. It
// returns nil when the job completed, and otherwise one of the errors in
// sync_job_errors.go or the transport's own error.
//
// It returns NO status body: everything a caller needs on a non-completion is
// already inside the error, which is where it has to be anyway for the message
// to be honest about what was observed.
//
// It rides transport.SyncControlJSON, which already carries the bearer, the
// one-shot 401 force-refresh retry, the account-id stamping, the client-version
// stamp and the version-refusal prove-and-retry — the status route is a POST
// precisely so it needs no new client transport.
//
// WHAT IS RETRIED, exactly, and nothing else: a transport failure (reset, EOF,
// DNS), the transport's own PER-REQUEST timeout, a 5xx from the edge or the
// gateway, and a body that does not parse. Each is logged at warn with the poll
// number and the running count and asked again on the same cadence. The ingest
// keeps running on the gateway whatever happens to this client's connection, so
// failing the push because one poll got a 502 would report a healthy ingest as
// broken — the confusion this whole change exists to remove.
//
// THE TWO TIMEOUTS ARE DIFFERENT EVENTS AND ARE NOT TOLD APART BY THE ERROR.
// The transport bounds each request at five minutes, inside this poll's
// fifteen-minute deadline; that bound expiring is one hung request and costs one
// retry. The CALLER'S context ending is somebody stopping the command, and it
// exits. Both surface as context.DeadlineExceeded in the error chain, which is
// why the caller's context is asked directly instead.
//
// WHAT EXITS AT ONCE, each with the remedy the operator actually needs:
//
//   - a terminal state (complete, or failed with the gateway's reason);
//   - 404 — the job is unknown, and asking again cannot make it known;
//   - 426 — the gateway refusing this client's version, which carries the
//     upgrade command; retrying would bury the one instruction that fixes it;
//   - 401 / 403 — an expired credential or a missing scope, including the
//     LOCAL failure every later poll takes once an account rejection has
//     latched and no request leaves the process at all;
//   - any other 4xx (400, 429, …) — a malformed request and a rate limit are
//     answers, not weather;
//   - an unrecognized state — the gateway answered and said something this
//     build cannot interpret, which is a wire disagreement rather than a
//     transient;
//   - the caller's context ending, and the overall deadline.
//
// The test that separates the two lists: could asking again change the answer?
// A dropped connection, yes. An expired credential, a refused version, a
// malformed request, a job that does not exist — no. Polling those for fifteen
// minutes and then reporting "the ingest is still running" describes something
// the client never observed and discards the remedy it was handed.
func pollSyncJob(
	ctx context.Context,
	transport *auth.Transport,
	jobID string,
	graph, name string,
) error {
	reqBody, err := json.Marshal(syncJobStatusRequest{JobID: jobID})
	if err != nil {
		return fmt.Errorf("marshal job-status request: %w", err)
	}

	start := time.Now()
	cadence := newSyncPollCadence(syncJobPoll.base, syncJobPoll.ceiling)
	// last carries the most recent SUCCESSFULLY PARSED status, so a deadline
	// expiry after a run of transient failures still reports the last thing the
	// gateway actually said rather than a zero value.
	var last syncJobStatusResponse
	// observed says whether ANY poll ever parsed a status. Without it the
	// deadline message would assert a state the client may never have seen.
	observed := false
	polls, transients := 0, 0

	for {
		polls++
		status, retry, err := readSyncJobStatus(ctx, transport, jobID, reqBody)
		switch {
		case err != nil && !retry:
			// Only the caller's own context makes this a cancellation, and it is
			// asked directly rather than inferred from the error.
			if ctxErr := ctx.Err(); ctxErr != nil {
				return canceledPoll(jobID, last, observed, time.Since(start), ctxErr)
			}
			return err
		case err != nil:
			transients++
			slog.Warn("sync push: job-status poll failed — retrying",
				"graph", graph, "name", name, "job_id", jobID,
				"poll", polls, "transient_failures", transients,
				"elapsed", time.Since(start).Round(time.Second), "error", err)
		default:
			last, observed = status, true
			if done, settled := settleSyncJobStatus(jobID, status); done {
				if settled == nil && transients > 0 {
					slog.Info("sync push: cloud ingest complete after transient poll failures",
						"graph", graph, "name", name, "job_id", jobID,
						"polls", polls, "transient_failures", transients)
				}
				return settled
			}
		}

		elapsed := time.Since(start)
		if elapsed >= syncJobPoll.deadline {
			return &syncJobDeadlineError{
				JobID:      jobID,
				Waited:     elapsed,
				Deadline:   syncJobPoll.deadline,
				LastState:  last.State,
				Observed:   observed,
				Transients: transients,
			}
		}

		wait := cadence.next()
		// A long ingest must be visibly alive rather than a silent client.
		if err == nil {
			slog.Info("sync push: cloud ingest in progress",
				"graph", graph, "name", name, "job_id", jobID,
				"elapsed", elapsed.Round(time.Second), "next_poll_in", wait.Round(time.Millisecond))
		}

		// Never sleep past the deadline: the wait is clipped so the deadline
		// error reports the bound it actually observed.
		if remaining := syncJobPoll.deadline - elapsed; wait > remaining {
			wait = remaining
		}
		if ctxEnded := sleepUntilNextPoll(ctx, wait); ctxEnded {
			// The wait is a select on ctx.Done so the loop leaves within
			// milliseconds rather than at the next five-to-ten-second tick —
			// and it leaves holding the job id, which is the whole point: the
			// ingest is certainly still running on the gateway.
			return canceledPoll(jobID, last, observed, time.Since(start), ctx.Err())
		}
	}
}

// canceledPoll builds the error for a wait the caller ended. It is one
// constructor rather than two literals because the two sites that reach it — a
// context error surfacing from the request, and the wait's own select — are the
// same event seen from either side of a round trip, and must not drift into
// telling an operator two different things.
func canceledPoll(jobID string, last syncJobStatusResponse, observed bool, waited time.Duration, cause error) error {
	return &syncJobCanceledError{
		JobID:     jobID,
		LastState: last.State,
		Observed:  observed,
		Waited:    waited,
		Cause:     cause,
	}
}

// settleSyncJobStatus decides what a parsed status means. done is false only
// for a job still in progress, which is the one answer that means "ask again";
// every other state ends the poll, with a nil error for a completed job and the
// matching terminal error otherwise.
func settleSyncJobStatus(jobID string, status syncJobStatusResponse) (done bool, err error) {
	switch status.State {
	case syncJobStateComplete:
		return true, nil
	case syncJobStateFailed:
		return true, &syncJobFailedError{JobID: jobID, Reason: status.Reason}
	case syncJobStateInProgress:
		return false, nil
	default:
		return true, &syncJobStateError{JobID: jobID, State: status.State}
	}
}

// sleepUntilNextPoll waits out the cadence and reports whether the caller's
// context ended instead of the timer firing.
//
// It is a select rather than a sleep, and that is the whole point: a command
// interrupted mid-poll leaves within milliseconds rather than at the next
// five-to-ten-second tick.
func sleepUntilNextPoll(ctx context.Context, wait time.Duration) (ctxEnded bool) {
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return true
	case <-timer.C:
		return false
	}
}

// readSyncJobStatus performs ONE job-status call and classifies its outcome.
// retry is true when the failure is transient and the caller should ask again.
//
// A BODY THAT DOES NOT PARSE IS TRANSIENT, deliberately: a truncated response or
// an HTML error page from an edge proxy is exactly the shape a gateway hiccup
// takes, and it says nothing about the ingest running behind it.
func readSyncJobStatus(
	ctx context.Context,
	transport *auth.Transport,
	jobID string,
	reqBody []byte,
) (status syncJobStatusResponse, retry bool, err error) {
	raw, err := transport.SyncControlJSON(ctx, "job-status", reqBody)
	if err != nil {
		retry, err = classifySyncPollFailure(ctx, jobID, err)
		return syncJobStatusResponse{}, retry, err
	}
	if err := json.Unmarshal(raw, &status); err != nil {
		return syncJobStatusResponse{}, true, fmt.Errorf("decode job-status response: %w", err)
	}
	return status, false, nil
}

// classifySyncPollFailure decides whether one failed job-status call is worth
// asking again, and returns the error the caller should surface if it is not.
//
// IT CLASSIFIES ON WHAT FAILED, not on the absence of a 404. Classifying by
// "anything but a 404 is transient" polled an expired credential, a missing
// scope, a malformed request, a rate limit and a refused client version for the
// whole fifteen-minute deadline and then reported the ingest as still running —
// a claim about something the client never observed, with the remedy it had been
// handed thrown away.
//
// The order below is the order the transport produces these in, and the first
// two matter: a version refusal is NOT a *SyncHTTPError (readHTTPError returns
// it first), and a latched account rejection fails LOCALLY inside the request
// builder with no round trip at all, so neither would be recognized by a status
// check.
func classifySyncPollFailure(ctx context.Context, jobID string, err error) (retry bool, out error) {
	// THE CALLER STOPPING IS DECIDED ON THE CALLER'S OWN CONTEXT, never by
	// sniffing a sentinel out of the transport's error.
	//
	// This line used to read errors.Is(err, context.DeadlineExceeded), and it is
	// worth keeping the note: the comment asserted the intent while the
	// predicate tested a PROXY for it, and that proxy has a second producer.
	// http.Client.Timeout reports exactly that sentinel, and the sync transport
	// sets a five-minute per-request bound INSIDE this poll's fifteen-minute
	// deadline — so one job-status call the gateway accepted and never answered
	// ended the whole poll and told the operator they had stopped the client.
	// Nobody had. A per-request timeout is a transient, which is what the ruling
	// says and where it now falls through to.
	if ctx.Err() != nil {
		return false, err
	}
	// The gateway refused this client over its version and told it how to
	// upgrade. Retrying buries the one instruction that resolves it.
	if _, ok := errors.AsType[*auth.VersionRefusalError](err); ok {
		return false, err
	}
	// The gateway rejected the selected account and the transport latched it, so
	// every later poll now fails here without a request leaving the process.
	// Asking again cannot change a decision made locally.
	if errors.Is(err, auth.ErrAccountSelectionRejected) {
		return false, err
	}

	if se, ok := errors.AsType[*auth.SyncHTTPError](err); ok {
		switch {
		case se.StatusCode == http.StatusNotFound:
			return false, &syncJobNotFoundError{JobID: jobID}
		case se.StatusCode >= 400 && se.StatusCode < 500:
			// 400, 401, 403, 429 and the rest: the gateway understood the
			// request and refused it. The transport's own error carries the
			// account remedy where it has one, and pushGraph's wrapPushErr adds
			// the login guidance for 401 and 403.
			return false, err
		}
		// 5xx: the gateway is unwell, not the request.
		return true, err
	}

	// A transport failure: reset, timeout, EOF, DNS. Ask again.
	return true, err
}
