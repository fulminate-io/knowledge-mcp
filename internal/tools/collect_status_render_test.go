// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// collectRunRenderDeps embeds cloudStatusDeps (for the full ClientDeps +
// cloudStatusInfo surface), overrides LocalLiveness with an injectable fake, and
// implements collectRunReporter so manage(status) renders the collect-run section.
type collectRunRenderDeps struct {
	*cloudStatusDeps
	live LocalLiveness
	runs []CollectRunStatus
}

func (d *collectRunRenderDeps) LocalLiveness() LocalLiveness {
	if d.live != nil {
		return d.live
	}
	return d.cloudStatusDeps.LocalLiveness()
}

func (d *collectRunRenderDeps) CollectRunSnapshot() []CollectRunStatus { return d.runs }

// scriptedCollectRuns is one running + one completed + one failed target.
func scriptedCollectRuns() []CollectRunStatus {
	base := time.Unix(1_700_000_000, 0)
	return []CollectRunStatus{
		{Target: "code\x00/a", Label: "code /a", State: "running", StartedAt: time.Now().Add(-30 * time.Second)},
		{Target: "aws\x00acct", Label: "aws acct", State: "completed", StartedAt: base, FinishedAt: base.Add(12 * time.Second)},
		{Target: "web\x00https://x", Label: "web https://x", State: "failed", StartedAt: base, FinishedAt: base.Add(5 * time.Second), Err: "boom"},
	}
}

// TestHandleServerStatus_CollectRuns_Text: the logged-out local text path renders
// the running elapsed line + the completed + failed(error) lines.
func TestHandleServerStatus_CollectRuns_Text(t *testing.T) {
	deps := &collectRunRenderDeps{
		cloudStatusDeps: &cloudStatusDeps{loggedIn: false},
		live:            fakeLiveness{status: runningStatusMap()},
		runs:            scriptedCollectRuns(),
	}
	body := textBodyTools(handleServerStatus(opCtx(), deps, ""))
	assert.Contains(t, body, "Collect runs:")
	assert.Contains(t, body, "code /a: running")
	assert.Contains(t, body, "elapsed)")
	assert.Contains(t, body, "aws acct: completed")
	assert.Contains(t, body, "web https://x: failed")
	assert.Contains(t, body, "error: boom")
}

// TestHandleServerStatus_CollectRuns_JSON: the logged-out local json path carries a
// collect_runs key whose running/completed/failed entries are correctly shaped.
func TestHandleServerStatus_CollectRuns_JSON(t *testing.T) {
	deps := &collectRunRenderDeps{
		cloudStatusDeps: &cloudStatusDeps{loggedIn: false},
		live:            fakeLiveness{status: runningStatusMap()},
		runs:            scriptedCollectRuns(),
	}
	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(textBodyTools(handleServerStatus(opCtx(), deps, "json"))), &got))
	raw, ok := got["collect_runs"]
	require.True(t, ok, "collect_runs key present")
	entries, ok := raw.([]any)
	require.True(t, ok)
	require.Len(t, entries, 3)

	var sawRunning, sawCompleted, sawFailed bool
	for _, e := range entries {
		m := e.(map[string]any)
		switch m["state"] {
		case "running":
			sawRunning = true
			assert.Contains(t, m, "elapsed_seconds")
			assert.Equal(t, "code /a", m["label"])
		case "completed":
			sawCompleted = true
			assert.Contains(t, m, "duration_seconds")
		case "failed":
			sawFailed = true
			assert.Equal(t, "boom", m["error"])
		}
	}
	assert.True(t, sawRunning, "running entry present")
	assert.True(t, sawCompleted, "completed entry present")
	assert.True(t, sawFailed, "failed entry present")
}

// TestHandleCloudStatus_CollectRuns: collect runs are login-independent, so the
// logged-in cloud path renders them too (text section + json collect_runs key).
func TestHandleCloudStatus_CollectRuns(t *testing.T) {
	stats := &knowledgev1.GraphStats{NodeCount: 10, EdgeCount: 5, BinaryVectorCount: 2}
	deps := &collectRunRenderDeps{
		cloudStatusDeps: &cloudStatusDeps{gc: &modFake{stats: stats}, loggedIn: true, host: "https://dev.fulminate.io"},
		runs:            scriptedCollectRuns(),
	}

	body := textBodyTools(handleServerStatus(opCtx(), deps, ""))
	assert.Contains(t, body, "Backend: cloud (https://dev.fulminate.io)", "routed to the cloud status path")
	assert.Contains(t, body, "Collect runs:")
	assert.Contains(t, body, "aws acct: completed")

	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(textBodyTools(handleServerStatus(opCtx(), deps, "json"))), &got))
	assert.Equal(t, "cloud", got["backend"])
	assert.Contains(t, got, "collect_runs")
}

// TestHandleServerStatus_NoCollectRuns_Baseline: a deps WITHOUT the
// collectRunReporter interface produces output identical to the pre-change baseline
// — no collect section, no collect_runs key.
func TestHandleServerStatus_NoCollectRuns_Baseline(t *testing.T) {
	deps := &localNoHealthDeps{cloudStatusDeps: &cloudStatusDeps{loggedIn: false}, live: fakeLiveness{status: runningStatusMap()}}
	body := textBodyTools(handleServerStatus(opCtx(), deps, ""))
	assert.Contains(t, body, "Graph server: RUNNING")
	assert.NotContains(t, body, "Collect runs:")

	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(textBodyTools(handleServerStatus(opCtx(), deps, "json"))), &got))
	assert.NotContains(t, got, "collect_runs")
}

// TestHandleServerStatus_EmptyCollectRuns_NoSection: the interface is present but the
// snapshot is empty — the section still degrades to nothing (byte-identical baseline).
func TestHandleServerStatus_EmptyCollectRuns_NoSection(t *testing.T) {
	deps := &collectRunRenderDeps{
		cloudStatusDeps: &cloudStatusDeps{loggedIn: false},
		live:            fakeLiveness{status: runningStatusMap()},
		runs:            nil,
	}
	body := textBodyTools(handleServerStatus(opCtx(), deps, ""))
	assert.NotContains(t, body, "Collect runs:")

	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(textBodyTools(handleServerStatus(opCtx(), deps, "json"))), &got))
	assert.NotContains(t, got, "collect_runs")
}
