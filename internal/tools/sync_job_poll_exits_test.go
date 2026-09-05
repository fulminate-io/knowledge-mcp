// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/auth"
	"github.com/fulminate-io/knowledge-mcp/internal/clientver"
	"github.com/fulminate-io/knowledge-mcp/internal/config"
)

// shortPollSchedule compresses the cadence so a test that must reach the
// deadline does it in milliseconds. Every test here that could otherwise poll
// for fifteen minutes uses it, which is also what makes a REGRESSION here fail
// as an assertion rather than as a suite timeout.
func shortPollSchedule() syncJobPollSchedule {
	return syncJobPollSchedule{
		base:     time.Millisecond,
		ceiling:  2 * time.Millisecond,
		deadline: 60 * time.Millisecond,
	}
}

// TestSyncPush_ClientErrorsExitOnTheFirstPoll is the correction to the retry
// rule. Classifying "anything that is not a 404" as transient polled an expired
// credential, a missing scope, a malformed request and a rate limit for the full
// deadline and then reported the ingest as still running — a claim about
// something the client never observed, with the remedy it had been handed thrown
// away.
//
// The test that separates the two classes: could asking again change the answer?
// A dropped connection, yes. Any of these, no.
func TestSyncPush_ClientErrorsExitOnTheFirstPoll(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		expect string
	}{
		{"400 bad request", http.StatusBadRequest, `{"error":"bad_request"}`, "HTTP 400"},
		{"401 unauthorized", http.StatusUnauthorized, `{"error":"unauthorized"}`, "authentication failed"},
		{"403 forbidden", http.StatusForbidden, `{"error":"forbidden"}`, "server rejected request"},
		{"429 rate limited", http.StatusTooManyRequests, `{"error":"rate_limited"}`, "HTTP 429"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withSyncJobPoll(t, shortPollSchedule())
			backend := newFakeSyncBackend(t)
			// Faults do not repeat, so a single entry proves the client stopped
			// after ONE call rather than being rescued by the script running out.
			backend.jobStatusFaults = []jobStatusFault{{status: tc.status, body: tc.body}}
			backend.jobStates = []syncJobStatusResponse{{State: syncJobStateComplete}}

			text, isErr := pushAgainst(t, backend)

			assert.True(t, isErr, "a refusal is not a successful push: %q", text)
			assert.Contains(t, text, tc.expect)
			assert.NotContains(t, text, "still running on the server",
				"a refusal must not be reported as a running ingest: %q", text)

			backend.mu.Lock()
			calls := backend.jobStatusCalls
			backend.mu.Unlock()
			assert.Equal(t, 1, calls, "the poll asked once and stopped")
		})
	}
}

// TestSyncPush_A401AfterAnAccountLatchExitsWithoutARoundTrip covers the second
// order of the same defect. When the gateway rejects the selected account, the
// transport LATCHES it, and every later poll then fails locally inside the
// request builder with no request leaving the process at all. That local failure
// is not an HTTP status, so a status-blind classifier called it transient and
// polled a decision the client had already made against itself.
func TestSyncPush_A401AfterAnAccountLatchExitsWithoutARoundTrip(t *testing.T) {
	withSyncJobPoll(t, shortPollSchedule())

	// A selection the gateway has already rejected. IDForRequest then refuses
	// BEFORE the round trip is issued, which is the shape under test.
	cfgPath := filepath.Join(t.TempDir(), "config")
	require.NoError(t, os.WriteFile(cfgPath, []byte("[default]\nprovider = \"anthropic\"\n"), 0o600))
	require.NoError(t, config.WriteSelectedAccountID(cfgPath, "acct-42"))
	sel := auth.NewAccountSelection(cfgPath, time.Second)
	sel.MarkInvalid("acct-42", "this account is not entitled to sync")

	backend := newFakeSyncBackend(t)
	tr := auth.NewSyncTransport(backend.srv.URL, auth.StaticTokenSource{
		AccessToken: "tok",
		Permissions: auth.PermissionSet{auth.PermMCPKnowledgeWrite: {}},
	}, auth.WithAccountSelection(sel))

	err := pollSyncJob(t.Context(), tr, "job-1", "knowledge", "default")

	require.Error(t, err)
	require.ErrorIs(t, err, auth.ErrAccountSelectionRejected,
		"the account remedy must survive to the caller: %v", err)
	assert.Contains(t, err.Error(), "knowledge accounts",
		"the remedy names the command that lists the accounts the user may pick")

	backend.mu.Lock()
	calls := backend.jobStatusCalls
	backend.mu.Unlock()
	assert.Equal(t, 0, calls, "a latched selection fails locally — nothing should reach the gateway")
}

// TestSyncPush_AVersionRefusalExitsCarryingTheUpgradeRemedy: a 426 is the
// gateway refusing this client outright and handing it the one command that
// fixes it. It is not even a *SyncHTTPError — readHTTPError returns the refusal
// first — so a classifier that only inspects HTTP statuses swallows it, polls
// for fifteen minutes, and reports a running ingest instead of the upgrade.
func TestSyncPush_AVersionRefusalExitsCarryingTheUpgradeRemedy(t *testing.T) {
	// The refusal latches process-wide, so it is cleared before and after.
	clientver.ClearRefusal()
	t.Cleanup(clientver.ClearRefusal)

	withSyncJobPoll(t, shortPollSchedule())
	backend := newFakeSyncBackend(t)
	refusal, err := json.Marshal(map[string]string{
		"minimum":         "v0.8.4",
		"client_version":  "v0.8.1",
		"platform":        "darwin-arm64",
		"upgrade_command": "knowledge install",
		"reason":          "below_minimum",
	})
	require.NoError(t, err)
	backend.jobStatusFaults = []jobStatusFault{{status: http.StatusUpgradeRequired, body: string(refusal)}}
	backend.jobStates = []syncJobStatusResponse{{State: syncJobStateComplete}}

	tr := auth.NewSyncTransport(backend.srv.URL, auth.StaticTokenSource{
		AccessToken: "tok",
		Permissions: auth.PermissionSet{auth.PermMCPKnowledgeWrite: {}},
	})
	pollErr := pollSyncJob(t.Context(), tr, "job-1", "knowledge", "default")

	require.Error(t, pollErr)
	refusalErr, ok := errors.AsType[*auth.VersionRefusalError](pollErr)
	require.True(t, ok, "the refusal type must reach the caller, not a generic transient: %T %v", pollErr, pollErr)
	assert.Equal(t, http.StatusUpgradeRequired, refusalErr.Status)
	assert.Contains(t, refusalErr.Error(), "knowledge install", "the upgrade remedy survives")
	assert.Contains(t, refusalErr.Error(), "v0.8.4", "the minimum the client must reach survives")

	backend.mu.Lock()
	calls := backend.jobStatusCalls
	backend.mu.Unlock()
	assert.Equal(t, 1, calls, "a refused client asked once and stopped")
}

// TestSyncPush_CancellationReturnsPromptlyAndNamesTheJob: canceling the command
// is the one case where the ingest is CERTAINLY still running on the gateway,
// and the client is holding the job id at the moment it stops watching. Before
// this it returned a bare "context canceled" and the id was lost.
func TestSyncPush_CancellationReturnsPromptlyAndNamesTheJob(t *testing.T) {
	// The PRODUCTION cadence, deliberately: promptness here means the wait is a
	// select on ctx.Done rather than a sleep, and compressing the schedule would
	// make the assertion pass for the wrong reason.
	backend := newFakeSyncBackend(t)
	backend.jobStates = []syncJobStatusResponse{{State: syncJobStateInProgress}}

	tr := auth.NewSyncTransport(backend.srv.URL, auth.StaticTokenSource{
		AccessToken: "tok",
		Permissions: auth.PermissionSet{auth.PermMCPKnowledgeWrite: {}},
	})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(300 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	err := pollSyncJob(ctx, tr, "job-1", "knowledge", "default")
	elapsed := time.Since(start)

	require.Error(t, err)
	assert.Less(t, elapsed, 3*time.Second,
		"the wait must leave on cancellation, not at the next 5-10s tick (took %s)", elapsed)
	require.ErrorIs(t, err, context.Canceled, "the cause is still inspectable")
	assert.Contains(t, err.Error(), "job-1", "the job id survives so the operator can poll it again")
	assert.Contains(t, err.Error(), "do NOT re-push")
	assert.Contains(t, err.Error(), "does NOT stop the ingest")

	var jobErr syncJobError
	assert.ErrorAs(t, err, &jobErr,
		"a cancellation must be surfaced verbatim, not re-wrapped by wrapPushErr")
}

// TestSyncPush_ConfirmStateIsReadNotJustDecoded: the state confirm reports is
// part of the wire and is validated. An unrecognized value errors before the
// poll rather than being ignored on the way to an answer the same
// misunderstanding would interpret.
func TestSyncPush_ConfirmStateIsReadNotJustDecoded(t *testing.T) {
	withSyncJobPoll(t, shortPollSchedule())
	backend := newFakeSyncBackend(t)
	backend.confirmState = "queued"

	text, isErr := pushAgainst(t, backend)

	assert.True(t, isErr, "an uninterpretable confirm state is not a successful push: %q", text)
	assert.Contains(t, text, "unrecognized job state")
	assert.Contains(t, text, `"queued"`)

	backend.mu.Lock()
	calls := backend.jobStatusCalls
	backend.mu.Unlock()
	assert.Equal(t, 0, calls, "nothing is polled on a confirm this client cannot interpret")
}

// TestSyncPush_ConfirmStateInProgressIsAccepted is the control for the case
// above: the state the wire actually specifies still drives a normal poll.
func TestSyncPush_ConfirmStateInProgressIsAccepted(t *testing.T) {
	withSyncJobPoll(t, shortPollSchedule())
	backend := newFakeSyncBackend(t)
	backend.confirmState = syncJobStateInProgress

	text, isErr := pushAgainst(t, backend)

	assert.False(t, isErr, "%q", text)
	assert.Contains(t, text, "pushed knowledge/default")
}

// pollLogCapture records what the poll logs, so a test can assert the transient
// COUNT rather than inferring it from a call tally.
type pollLogCapture struct {
	mu      sync.Mutex
	records []slog.Record
}

func (c *pollLogCapture) Enabled(context.Context, slog.Level) bool { return true }

func (c *pollLogCapture) Handle(_ context.Context, r slog.Record) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.records = append(c.records, r.Clone())
	return nil
}

func (c *pollLogCapture) WithAttrs([]slog.Attr) slog.Handler { return c }
func (c *pollLogCapture) WithGroup(string) slog.Handler      { return c }

// attrsOf returns the attributes of the first record with the given message.
func (c *pollLogCapture) attrsOf(msg string) (map[string]string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, r := range c.records {
		if r.Message != msg {
			continue
		}
		out := map[string]string{}
		r.Attrs(func(a slog.Attr) bool {
			out[a.Key] = a.Value.String()
			return true
		})
		return out, true
	}
	return nil, false
}

func capturePollLogs(t *testing.T) *pollLogCapture {
	t.Helper()
	c := &pollLogCapture{}
	prev := slog.Default()
	slog.SetDefault(slog.New(c))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return c
}

// TestSyncPush_APerRequestTimeoutIsRetriedNotReportedAsCancellation is the
// class that broke when the 4xx narrowing over-reached.
//
// The transport bounds every request at five minutes, INSIDE this poll's
// fifteen-minute deadline, and http.Client.Timeout reports
// context.DeadlineExceeded. Deciding "the caller stopped waiting" by sniffing
// that sentinel therefore made one hung job-status call end the whole poll and
// tell the operator they had stopped the client. Nobody had. A per-request
// timeout is one hung request, it costs one retry, and the ruling names
// timeouts in the retried set.
func TestSyncPush_APerRequestTimeoutIsRetriedNotReportedAsCancellation(t *testing.T) {
	logs := capturePollLogs(t)
	// The deadline must outlast the stall, or the test would measure the poll
	// deadline instead of the per-request one.
	withSyncJobPoll(t, syncJobPollSchedule{
		base:     time.Millisecond,
		ceiling:  2 * time.Millisecond,
		deadline: 20 * time.Second,
	})
	backend := newFakeSyncBackend(t)
	backend.jobStatusFaults = []jobStatusFault{{stall: 5 * time.Second}}
	backend.jobStates = []syncJobStatusResponse{
		{State: syncJobStateInProgress},
		{State: syncJobStateComplete},
	}

	tr := auth.NewSyncTransport(backend.srv.URL, auth.StaticTokenSource{
		AccessToken: "tok",
		Permissions: auth.PermissionSet{auth.PermMCPKnowledgeWrite: {}},
	})
	// The production per-request bound is five minutes; compressed here so the
	// same event happens in milliseconds. This is the ONLY thing the test
	// changes about the transport.
	tr.SetHTTPClientForTest(&http.Client{Timeout: 80 * time.Millisecond})

	err := pollSyncJob(t.Context(), tr, "job-1", "knowledge", "default")

	require.NoError(t, err,
		"a hung request is one retry, not the end of the poll: %v", err)

	backend.mu.Lock()
	calls := backend.jobStatusCalls
	backend.mu.Unlock()
	assert.Equal(t, 3, calls, "the timeout was retried, then in_progress, then complete")

	warn, ok := logs.attrsOf("sync push: job-status poll failed — retrying")
	require.True(t, ok, "a retried timeout is logged at warn like every other transient")
	assert.Equal(t, "1", warn["transient_failures"], "the transient count counted it")
}

// TestSyncPush_ACallerCancellationStillExitsAtOnce is the same-instrument
// control for the test above: the two events reach the client as the SAME
// context sentinel, so a fix that retried both would look identical from the
// timeout side alone.
func TestSyncPush_ACallerCancellationStillExitsAtOnce(t *testing.T) {
	withSyncJobPoll(t, syncJobPollSchedule{
		base:     time.Millisecond,
		ceiling:  2 * time.Millisecond,
		deadline: 20 * time.Second,
	})
	backend := newFakeSyncBackend(t)
	backend.jobStatusFaults = []jobStatusFault{{stall: 5 * time.Second}}
	backend.jobStates = []syncJobStatusResponse{{State: syncJobStateInProgress}}

	tr := auth.NewSyncTransport(backend.srv.URL, auth.StaticTokenSource{
		AccessToken: "tok",
		Permissions: auth.PermissionSet{auth.PermMCPKnowledgeWrite: {}},
	})
	tr.SetHTTPClientForTest(&http.Client{Timeout: 80 * time.Millisecond})

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	err := pollSyncJob(ctx, tr, "job-1", "knowledge", "default")

	require.Error(t, err, "a canceled command does not report a completed push")
	assert.Less(t, time.Since(start), 5*time.Second, "the cancellation was not swallowed as a transient")
	require.ErrorIs(t, err, context.Canceled)
	assert.Contains(t, err.Error(), "job-1")
	assert.Contains(t, err.Error(), "does NOT stop the ingest")
}
