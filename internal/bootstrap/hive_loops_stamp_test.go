// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"

	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
	"github.com/fulminate-io/knowledge-mcp/internal/hivemonitor"
	"github.com/fulminate-io/knowledge-mcp/internal/llmproviders"
)

// opRecordingHiveCaller records the operation on the ctx — and the request —
// each half of the HiveCaller seam is called with.
type opRecordingHiveCaller struct {
	hiveOp    graphclient.Operation
	hiveOK    bool
	hiveReq   *knowledgev1.HiveRequest
	executeOp graphclient.Operation
	executeOK bool
}

func (c *opRecordingHiveCaller) Hive(
	ctx context.Context, req *knowledgev1.HiveRequest,
) (*knowledgev1.HiveResponse, error) {
	c.hiveOp, c.hiveOK = graphclient.OperationFromContext(ctx)
	c.hiveReq = req
	return &knowledgev1.HiveResponse{}, nil
}

func (c *opRecordingHiveCaller) Execute(
	ctx context.Context, _ *knowledgev1.ExecuteRequest,
) (*knowledgev1.ExecuteResponse, error) {
	c.executeOp, c.executeOK = graphclient.OperationFromContext(ctx)
	return &knowledgev1.ExecuteResponse{}, nil
}

// TestHiveCallerStampsOperation is the bootstrap package's half of the
// query-origin completeness gate. The hive daemons (lease monitor + machine-down
// reaper) tick on their own detached contexts with no originating tool call, so
// the stamping wrapper is the only thing keeping their RPCs out of the
// client.unstamped bucket — and both halves of the seam matter: the reaper's
// sweep issues its role-gate and stale-member scans through Execute and its
// evictions through Hive.
func TestHiveCallerStampsOperation(t *testing.T) {
	t.Parallel()

	t.Run("the graph read half is stamped", func(t *testing.T) {
		t.Parallel()

		inner := &opRecordingHiveCaller{}
		_, err := hiveCallerStampingOperation{inner: inner}.Execute(
			context.Background(), &knowledgev1.ExecuteRequest{})
		require.NoError(t, err)

		require.True(t, inner.executeOK, "the hive_member read reached the wire with NO operation on ctx")
		assert.Equal(t, graphclient.OpHiveMonitor, inner.executeOp)
	})

	t.Run("the hive op half is stamped", func(t *testing.T) {
		t.Parallel()

		inner := &opRecordingHiveCaller{}
		_, err := hiveCallerStampingOperation{inner: inner}.Hive(
			context.Background(), &knowledgev1.HiveRequest{})
		require.NoError(t, err)

		require.True(t, inner.hiveOK, "the renew/evict call reached the wire with NO operation on ctx")
		assert.Equal(t, graphclient.OpHiveMonitor, inner.hiveOp)
	})
}

// stampJudge is a fixed-verdict llmproviders.Supervisor: the supervisor reaches
// a Hive RPC only on a high-confidence terminal verdict, so the verdict is
// scripted rather than judged. Mirrors hivemonitor's fakeJudge
// (supervisor_handler_test.go:15), which is package-private there.
type stampJudge struct{ verdict llmproviders.Verdict }

func (j stampJudge) Judge(context.Context, string) (llmproviders.Verdict, error) {
	return j.verdict, nil
}

// supervisorTranscript writes a one-record claude transcript and returns a
// handle for it, so FormatTranscript renders text instead of taking its
// unknown-format error path — which resumes and issues no RPC at all, the one
// way this test could pass without exercising anything.
func supervisorTranscript(t *testing.T, harnessID string) hivemonitor.TranscriptHandle {
	t.Helper()
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	line := `{"type":"assistant","message":{"role":"assistant","stop_reason":"end_turn","content":[{"type":"text","text":"done"}]}}` + "\n"
	require.NoError(t, os.WriteFile(path, []byte(line), 0o600))
	return hivemonitor.TranscriptHandle{Path: path, HarnessSessionID: harnessID, Format: hivemonitor.FormatClaude}
}

// TestSupervisorHiveCallerStampsOperation is the supervisor's half of the
// query-origin completeness gate. The Tier-2 supervisor can act on a dead
// worker's behalf — ack-on-behalf and evict — from the monitor's escalation
// callback, on a detached context with no originating tool call, so like the
// heartbeats and sweeps around it those RPCs reach the cloud unattributed
// unless they travel through the stamping caller.
func TestSupervisorHiveCallerStampsOperation(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		verdict llmproviders.Verdict
		wantOp  knowledgev1.HiveOp
	}{
		{"ack-on-behalf", llmproviders.Verdict{State: "done", Confidence: 0.9, Result: "shipped"}, knowledgev1.HiveOp_HIVE_OP_ACK},
		{"evict", llmproviders.Verdict{State: "stuck", Confidence: 0.95, Reason: "looping"}, knowledgev1.HiveOp_HIVE_OP_EVICT},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// The recorder sits UNDER the stamping wrapper, so the call is recorded
			// whether or not it was stamped — the assertion that discriminates is
			// the operation on ctx, not whether an RPC happened.
			inner := &opRecordingHiveCaller{}
			l := &hiveLoops{
				hive: hiveCallerStampingOperation{inner: inner},
				ban:  hivemonitor.NewBanSet(),
			}
			// ResumeRenew is map-and-mutex state only, so an unstarted Monitor is a
			// valid resume seam; the terminal verdicts under test never call it.
			resume := hivemonitor.NewMonitor(nil, nil, nil, nil, nil, hivemonitor.DefaultMonitorConfig())

			l.supervisorHandler(resume, stampJudge{verdict: tc.verdict}).Handle(
				context.Background(),
				hivemonitor.Claim{MsgID: "m1", Hive: "hive1"},
				"mcp-sess",
				hivemonitor.StateIdle,
				supervisorTranscript(t, "harness-abc"),
			)

			// POSITIVE CONTROL first: prove the supervisor action reached the wire.
			// A handler that issued NO RPC would leave the stamp assertion below
			// unexercised rather than failing it.
			require.NotNil(t, inner.hiveReq, "the supervisor issued NO Hive RPC — the stamp assertion would be vacuous")
			require.Equal(t, tc.wantOp, inner.hiveReq.GetOp())

			require.True(t, inner.hiveOK, "the supervisor RPC reached the wire with NO operation on ctx")
			assert.Equal(t, graphclient.OpHiveMonitor, inner.hiveOp)
		})
	}
}
