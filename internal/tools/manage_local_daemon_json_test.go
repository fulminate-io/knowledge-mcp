// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/pipeline"
)

// fullDaemonDeps is a fully-wired local daemon: healthy local liveness, a
// pipeline (pipelineMetricser), a doctor (doctorChecker), and a statsRPC
// GraphCaller for cloud stats + coverage. Its login state is toggleable so the
// SAME fixture drives both the logged-in cloud path and the logged-out local
// path, proving the local-daemon fields are present on BOTH.
type fullDaemonDeps struct {
	*cloudStatusDeps
	live    LocalLiveness
	metrics pipeline.Metrics
	checks  []DoctorCheck
}

func (d *fullDaemonDeps) LocalLiveness() LocalLiveness                 { return d.live }
func (d *fullDaemonDeps) PipelineMetrics() (pipeline.Metrics, bool)    { return d.metrics, true }
func (d *fullDaemonDeps) DoctorChecks(_ context.Context) []DoctorCheck { return d.checks }

func newFullDaemonDeps(loggedIn bool) *fullDaemonDeps {
	fake := &coverageFake{statsByKey: map[string]*knowledgev1.GraphStats{
		"knowledge": {NodeCount: 42, NonProxyNodeCount: 10, SummarizedCount: 5, BinaryVectorCount: 5},
	}}
	return &fullDaemonDeps{
		cloudStatusDeps: &cloudStatusDeps{gc: fake, loggedIn: loggedIn, host: "https://dev.fulminate.io"},
		live:            fakeLiveness{status: runningStatusMap()},
		metrics:         pipeline.Metrics{SummaryQueued: 3, EmbedQueued: 2},
		checks:          []DoctorCheck{{Name: "config", Status: "pass", Detail: "ok"}},
	}
}

// assertLocalDaemonFields checks the always-present local-daemon contract: the
// header facts (pid, graph_path), pipeline_enabled + the runtime counters, the
// coverage[] table, the doctor[] block, and the calling request's account
// routing are ALL present.
//
// THE FOUR ACCOUNT KEYS JOIN THE LIST RATHER THAN GETTING A SECOND HELPER: a
// consumer reads them by name off the same body, so "present on both branches"
// is the same claim for them as for pid and doctor[], and asserting it somewhere
// else is how one branch ends up carrying a field the other does not.
func assertLocalDaemonFields(t *testing.T, got map[string]any) {
	t.Helper()
	// account/account_source ride the SAME always-emitted contract as the
	// harness-session keys, on every json arm including the one with no local
	// daemon at all: both describe the calling REQUEST's resolution, and a web
	// page reads an absent account_source as a daemon that predates per-request
	// accounts.
	for _, k := range []string{
		"pid", "graph_path", "pipeline_enabled", "summary_queued", "embed_queued", "coverage", "doctor",
		// The harness-session resolution for THIS call. Present on every path:
		// an absent key and an unresolved session are different facts.
		"session", "session_source", "session_reason",
		// The account resolution for THIS call, on the same terms, with the
		// reason a header-carrying request on a logged-out daemon needs and the
		// machine-wide selection beside it — requirement 6 names the effective
		// account AND the global default, because only the pair tells a bound
		// caller whether its binding took.
		"account", "account_source", "account_reason", "account_global",
	} {
		assert.Contains(t, got, k, "local-daemon field %q must be present", k)
	}
	assert.Equal(t, true, got["pipeline_enabled"])
	require.IsType(t, []any{}, got["doctor"])
	assert.NotEmpty(t, got["doctor"].([]any), "doctor[] must be non-empty")
	require.IsType(t, []any{}, got["coverage"])
	assert.NotEmpty(t, got["coverage"].([]any), "coverage[] must be non-empty")

	// AND EVERY COVERAGE ROW CARRIES THE PER-FORMAT RESIDENT SEGMENT COUNT. It is
	// the eleventh pinned key and the only observable outside the process that says
	// whether the resident-growth bound is holding, so a row that stopped emitting
	// it would take the release smoke's assertion with it silently. Asserted HERE,
	// in the helper both arms call, so the logged-in and logged-out paths cannot
	// drift apart on it.
	for i, raw := range got["coverage"].([]any) {
		row, isObj := raw.(map[string]any)
		require.True(t, isObj, "coverage[%d] must be a JSON object", i)
		assert.Contains(t, row, "resident_segments",
			"coverage[%d] must carry the per-format resident segment count", i)
		assert.Contains(t, row, "resident_segments_peak",
			"coverage[%d] must carry its seal-path high-water too: the current count is always the count AFTER "+
				"the bound acted, so a row shipping only that one hides every excursion", i)
	}
	// THE VALUE ITSELF IS ASSERTED WHERE A PROBE ACTUALLY RUNS — this fixture's rows
	// are unmanaged, so their segment probe is declined by design and the honest
	// value here is the absent one. See TestCoverageRows_ResidentSegmentsPerFormat.
}

// TestHandleServerStatus_AlwaysEmitsLocalDaemonFields is the T2-2 criterion (CEO:
// always show local-daemon fields). A logged-IN daemon's status JSON still
// carries the local-daemon fields (doctor[], coverage[], pid, graph_path,
// pipeline_enabled, summary_*/embed_*) — layered under the cloud graph totals —
// and a logged-OUT run keeps the SAME fields (regression guard).
func TestHandleServerStatus_AlwaysEmitsLocalDaemonFields(t *testing.T) {
	t.Run("logged in — cloud path still carries local-daemon fields", func(t *testing.T) {
		deps := newFullDaemonDeps(true)
		var got map[string]any
		require.NoError(t, json.Unmarshal([]byte(textBodyTools(handleServerStatus(opCtx(), deps, "json"))), &got))

		// Cloud identity + CLOUD graph totals layered on top.
		assert.Equal(t, "cloud", got["backend"])
		assert.Equal(t, "https://dev.fulminate.io", got["host"])
		assert.EqualValues(t, 42, got["nodes"], "cloud graph totals win over local counts")

		assertLocalDaemonFields(t, got)
	})

	t.Run("logged out — local path carries the same fields", func(t *testing.T) {
		deps := newFullDaemonDeps(false)
		var got map[string]any
		require.NoError(t, json.Unmarshal([]byte(textBodyTools(handleServerStatus(opCtx(), deps, "json"))), &got))

		assert.Equal(t, "running", got["status"])
		assert.NotContains(t, got, "backend", "logged-out local path omits the backend routing key")
		assertLocalDaemonFields(t, got)
	})
}
