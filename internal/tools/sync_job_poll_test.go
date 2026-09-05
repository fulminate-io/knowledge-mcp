// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/auth"
)

// pushAgainst runs the push intercept against the given fake backend and
// returns the rendered result text plus the raw result, so each test below
// reads as "script the job, push, assert what the operator is told".
func pushAgainst(t *testing.T, backend *fakeSyncBackend) (string, bool) {
	t.Helper()
	withTransport(t, func() (*auth.Transport, error) {
		src := auth.StaticTokenSource{AccessToken: "tok", Permissions: auth.PermissionSet{auth.PermMCPKnowledgeWrite: {}}}
		return auth.NewSyncTransport(backend.srv.URL, src), nil
	})
	exp := &fakeExporter{bytesOut: []byte("KGV4 the local serialized graph bytes")}
	handled, out := InterceptSync(opCtx(), interceptTestDeps{gc: exp}, syncParams(t, map[string]any{"operation": "push"}))
	require.True(t, handled)
	return textOf(out), out.IsError
}

// withSyncJobPoll compresses the poll schedule for the duration of a test and
// restores the production one afterwards. It exists so a test that must observe
// SEVERAL polls, or the deadline, does not spend the production 2-second-and-up
// cadence doing it. Production never reassigns syncJobPoll.
func withSyncJobPoll(t *testing.T, s syncJobPollSchedule) {
	t.Helper()
	prev := syncJobPoll
	syncJobPoll = s
	t.Cleanup(func() { syncJobPoll = prev })
}

// TestSyncPush_PollsJobStatusUntilComplete is the core of the asynchronous
// confirm: confirm's 202 is NOT the end of the push. The client must take the
// job id, poll job-status until the job reports complete, and only then report
// success. Before this change pushGraph discarded the confirm body entirely and
// reported success the moment the 202 landed — which is precisely how a 418-second
// ingest that the gateway completed was reported to the operator as a failure,
// and how a still-running ingest would be reported as a success.
func TestSyncPush_PollsJobStatusUntilComplete(t *testing.T) {
	// Compressed: the production cadence is five to ten seconds, and this test
	// is about the SEQUENCE of polls rather than their spacing (which
	// TestSyncPollCadence_StaysInTheFiveToTenSecondBand pins directly).
	withSyncJobPoll(t, syncJobPollSchedule{
		base:     time.Millisecond,
		ceiling:  2 * time.Millisecond,
		deadline: 30 * time.Second,
	})
	backend := newFakeSyncBackend(t)
	backend.jobStates = []syncJobStatusResponse{
		{State: syncJobStateInProgress},
		{State: syncJobStateComplete, GraphType: "knowledge", Name: "default"},
	}

	text, isErr := pushAgainst(t, backend)

	assert.False(t, isErr, "a completed job is a successful push: %q", text)
	assert.Contains(t, text, "pushed knowledge/default")
	assert.Contains(t, text, "job=job-1", "the success line names the job the push rode")

	backend.mu.Lock()
	calls, askedAbout := backend.jobStatusCalls, backend.lastJobStatusID
	backend.mu.Unlock()
	assert.Equal(t, 2, calls, "polled until the job left in_progress")
	assert.Equal(t, "job-1", askedAbout, "the job id from the 202 reached the wire")
}

// TestSyncPush_JobFailed_SurfacesGatewayReasonVerbatim pins the one thing the
// failure path exists for. The gateway's reason is the operator's ONLY account
// of why an ingest they can no longer watch failed, so it must arrive
// unmodified — not re-worded, and not routed through wrapPushErr, whose 401/403
// arm would answer an ingest failure with advice about logging in again.
func TestSyncPush_JobFailed_SurfacesGatewayReasonVerbatim(t *testing.T) {
	const reason = "upstream knowledge-server rejected the bundle: vector width 256 does not match the graph's 512"
	backend := newFakeSyncBackend(t)
	backend.jobStates = []syncJobStatusResponse{
		{State: syncJobStateFailed, Reason: reason},
	}

	text, isErr := pushAgainst(t, backend)

	assert.True(t, isErr, "a failed job is a failed push: %q", text)
	assert.Contains(t, text, reason, "the gateway's reason survives verbatim")
	assert.Contains(t, text, "job-1", "the failure names the job")
	assert.NotContains(t, text, "knowledge login",
		"an ingest failure must not be dressed up as an auth failure")
}

// TestSyncPush_JobFailedWithoutReason_SaysSo covers the gateway reporting a
// failure it gave no reason for: the state is authoritative, and the honest
// output says the reason was absent rather than inventing one.
func TestSyncPush_JobFailedWithoutReason_SaysSo(t *testing.T) {
	backend := newFakeSyncBackend(t)
	backend.jobStates = []syncJobStatusResponse{{State: syncJobStateFailed}}

	text, isErr := pushAgainst(t, backend)

	assert.True(t, isErr, "a failed job is a failed push: %q", text)
	assert.Contains(t, text, "without a reason")
}

// TestSyncPush_JobStatusNotFound_NamedError covers the gateway's 404: an
// unknown job id, or one belonging to another account (the record is
// account-scoped, so the two are deliberately indistinguishable). The client
// must say the outcome is unknown — never report the push as landed.
func TestSyncPush_JobStatusNotFound_NamedError(t *testing.T) {
	backend := newFakeSyncBackend(t)
	backend.jobStatusNotFound = true

	text, isErr := pushAgainst(t, backend)

	assert.True(t, isErr, "an unobservable job is not a successful push: %q", text)
	assert.Contains(t, text, "not known to the server")
	assert.Contains(t, text, "job-1")
}

// TestSyncPush_PollDeadlineExpiry_NamesJobToPollAgain covers a job still
// running when the client's own poll deadline expires. That is NOT a failed
// ingest — the job keeps running on the gateway — so the message must hand back
// the job id and tell the operator not to re-push, since a re-push would
// re-upload and re-merge work the gateway already accepted.
func TestSyncPush_PollDeadlineExpiry_NamesJobToPollAgain(t *testing.T) {
	withSyncJobPoll(t, syncJobPollSchedule{
		base:     time.Millisecond,
		ceiling:  2 * time.Millisecond,
		deadline: 40 * time.Millisecond,
	})
	backend := newFakeSyncBackend(t)
	// One in_progress entry: the script's last entry repeats, so this job
	// never completes.
	backend.jobStates = []syncJobStatusResponse{{State: syncJobStateInProgress}}

	text, isErr := pushAgainst(t, backend)

	assert.True(t, isErr, "an unfinished job is not a successful push: %q", text)
	assert.Contains(t, text, `last observed in state "in_progress"`,
		"the deadline names the state the client actually saw, not an assumption")
	assert.Contains(t, text, "the ingest is still running on the server")
	assert.Contains(t, text, "do NOT re-push")
	assert.Contains(t, text, "job-1")

	backend.mu.Lock()
	calls := backend.jobStatusCalls
	backend.mu.Unlock()
	assert.Greater(t, calls, 1, "the client kept polling until its deadline")
}

// TestSyncPush_UnknownJobState_FailsLoud: a state outside the approved three is
// bad input and errors. Rounding it to in_progress would poll until the
// deadline; rounding it to complete would report a push that may not have
// landed. Neither is acceptable, so neither happens.
func TestSyncPush_UnknownJobState_FailsLoud(t *testing.T) {
	// Compressed like every sibling that can reach a wait. The arm returns on
	// the first poll while it is correct, so the schedule looks irrelevant — but
	// the regression this test exists to catch is coercing the unknown state to
	// in_progress, and at the production deadline that regression fails as a
	// 2m30s timeout panic that takes the package down, instead of as the
	// assertion below.
	withSyncJobPoll(t, syncJobPollSchedule{
		base:     time.Millisecond,
		ceiling:  2 * time.Millisecond,
		deadline: 40 * time.Millisecond,
	})
	backend := newFakeSyncBackend(t)
	backend.jobStates = []syncJobStatusResponse{{State: "queued"}}

	text, isErr := pushAgainst(t, backend)

	assert.True(t, isErr, "an uninterpretable state is not a successful push: %q", text)
	assert.Contains(t, text, "unrecognized state")
	assert.Contains(t, text, `"queued"`)
}

// TestSyncPush_ConfirmWithoutJobID_FailsLoud: a 202 with no job id leaves
// nothing to poll and no way to learn whether the ingest landed. Reporting
// success there would reintroduce exactly the blind spot this change removes.
func TestSyncPush_ConfirmWithoutJobID_FailsLoud(t *testing.T) {
	backend := newFakeSyncBackend(t)
	backend.confirmOmitJobID = true

	text, isErr := pushAgainst(t, backend)

	assert.True(t, isErr, "a push whose outcome cannot be observed is not a success: %q", text)
	assert.Contains(t, text, "no job id")

	backend.mu.Lock()
	calls := backend.jobStatusCalls
	backend.mu.Unlock()
	assert.Equal(t, 0, calls, "there is nothing to poll without a job id")
}

// TestSyncPollCadence_StaysInTheFiveToTenSecondBand pins the schedule itself.
// The owner's rule is a poll every five to ten seconds until the job is
// terminal, so the wait is drawn from that band on EVERY poll and never grows:
// forty draws must all land inside it, and they must not all be the same
// number, because the jitter is what keeps several clients from settling into a
// synchronized burst against a shared per-account budget.
func TestSyncPollCadence_StaysInTheFiveToTenSecondBand(t *testing.T) {
	c := newSyncPollCadence(5*time.Second, 10*time.Second)

	seen := map[time.Duration]bool{}
	for i := range 40 {
		got := c.next()
		assert.GreaterOrEqual(t, got, 5*time.Second, "poll %d: %s is below the band", i+1, got)
		assert.LessOrEqual(t, got, 10*time.Second, "poll %d: %s is above the band — the cadence must not grow", i+1, got)
		seen[got] = true
	}
	assert.Greater(t, len(seen), 1, "every wait was identical: the jitter is not being applied")
}

// TestSyncPollCadence_DegenerateInputsGetDefaults: a zero base or an inverted
// ceiling must not produce a hot loop.
func TestSyncPollCadence_DegenerateInputsGetDefaults(t *testing.T) {
	c := newSyncPollCadence(0, 0)
	assert.Positive(t, c.next(), "a zero-configured cadence still waits")

	inverted := newSyncPollCadence(10*time.Second, time.Second)
	assert.Equal(t, 10*time.Second, inverted.next(),
		"a ceiling below the base collapses to the base, never below it")
}

// TestSyncPush_TransientPollFailuresAreRetriedUntilComplete is the owner's
// ruling: the poll retries until the job is terminal, and transient issues get
// retried. The ingest keeps running on the gateway whatever happens to this
// client's connection, so a 502 from the edge, a dropped connection and an
// error page that is not JSON must each cost one retry rather than the whole
// push. Before this change any one of them ended the poll and reported a
// healthy ingest as a failure.
func TestSyncPush_TransientPollFailuresAreRetriedUntilComplete(t *testing.T) {
	withSyncJobPoll(t, syncJobPollSchedule{
		base:     time.Millisecond,
		ceiling:  2 * time.Millisecond,
		deadline: 30 * time.Second,
	})
	backend := newFakeSyncBackend(t)
	backend.jobStatusFaults = []jobStatusFault{
		{status: 502, body: "<html>502 Bad Gateway</html>"},
		{hangUp: true},
		{status: 200, body: "not json at all"},
	}
	backend.jobStates = []syncJobStatusResponse{
		{State: syncJobStateInProgress},
		{State: syncJobStateComplete},
	}

	text, isErr := pushAgainst(t, backend)

	assert.False(t, isErr, "transient poll failures must not fail a healthy ingest: %q", text)
	assert.Contains(t, text, "pushed knowledge/default")

	backend.mu.Lock()
	calls := backend.jobStatusCalls
	backend.mu.Unlock()
	assert.Equal(t, 5, calls, "three faults retried, then in_progress, then complete")
}

// TestSyncPush_TransientFailuresStillHitTheDeadline: retrying is bounded by the
// same overall deadline, so a gateway that never answers usefully is reported
// rather than polled forever.
//
// And the message must NOT claim the ingest is still running. Not one poll
// succeeded here, so the client never learned the job's state; asserting one
// would be describing something it never saw. "The client could not see" and
// "the ingest did not finish" are different diagnoses and the text says which
// this is.
func TestSyncPush_TransientFailuresStillHitTheDeadline(t *testing.T) {
	withSyncJobPoll(t, syncJobPollSchedule{
		base:     time.Millisecond,
		ceiling:  2 * time.Millisecond,
		deadline: 40 * time.Millisecond,
	})
	backend := newFakeSyncBackend(t)
	// A long run of faults, sized past what the deadline can consume at this
	// cadence. Faults do not repeat once exhausted, so the count is deliberate.
	for range 500 {
		backend.jobStatusFaults = append(backend.jobStatusFaults, jobStatusFault{status: 502, body: "gateway"})
	}

	text, isErr := pushAgainst(t, backend)

	assert.True(t, isErr, "a poll that never got an answer is not a successful push: %q", text)
	assert.Contains(t, text, "do NOT re-push")
	assert.Contains(t, text, "never successfully observed")
	assert.Contains(t, text, "never learned the job's state")
	assert.NotContains(t, text, "the ingest is still running on the server",
		"the client observed nothing, so it must not assert the job is running")
}

// TestSyncPush_A404IsNotRetried: the 404 is the one refusal that asking again
// cannot change, so it exits on the first call instead of burning the deadline.
func TestSyncPush_A404IsNotRetried(t *testing.T) {
	withSyncJobPoll(t, syncJobPollSchedule{
		base:     time.Millisecond,
		ceiling:  2 * time.Millisecond,
		deadline: 30 * time.Second,
	})
	backend := newFakeSyncBackend(t)
	backend.jobStatusNotFound = true

	text, isErr := pushAgainst(t, backend)

	assert.True(t, isErr, "%q", text)
	assert.Contains(t, text, "not known to the server")

	backend.mu.Lock()
	calls := backend.jobStatusCalls
	backend.mu.Unlock()
	assert.Equal(t, 1, calls, "the 404 ended the poll on the first call rather than being retried")
}
