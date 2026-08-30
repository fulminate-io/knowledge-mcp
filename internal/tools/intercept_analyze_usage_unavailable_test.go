// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/transcriptanalytics"
)

// nonEmptyReportForRecommend returns a report the intercept will render rather than
// short-circuit on: reportEmpty keys off SessionCount, so it must be non-zero.
func nonEmptyReportForRecommend() *transcriptanalytics.DetectorReport {
	return &transcriptanalytics.DetectorReport{
		AvgTokensPerSession: transcriptanalytics.AvgTokensPerSession{SessionCount: 2},
		DuplicateCommands: []transcriptanalytics.DuplicateCommandRow{
			{SessionID: "S1", ToolName: "Bash", RunCount: 2},
		},
	}
}

// TestInterceptAnalyzeUsage_RecommendNamesUnavailableReason asserts on the MARSHALED JSON
// rather than the Go value, because the defect it covers is a marshaling one: a nil slice
// renders as `"recommendations":null`, which carries no cause a caller can read.
func TestInterceptAnalyzeUsage_RecommendNamesUnavailableReason(t *testing.T) {
	t.Run("degraded synthesis names its cause", func(t *testing.T) {
		deps := analyzeUsageTestDeps{analyzer: fakeUsageAnalyzer{
			report: nonEmptyReportForRecommend(),
			recs: transcriptanalytics.SynthesisResult{
				Unavailable: "synthesis-failed: distinctive-oversize-b42e",
			},
		}}
		handled, body, isErr := callAnalyzeUsage(t, deps, `{"operation":"recommend"}`)
		require.True(t, handled)
		require.False(t, isErr)

		var payload map[string]json.RawMessage
		require.NoError(t, json.Unmarshal([]byte(body), &payload))
		require.Contains(t, payload, "detectors", "a degrade never costs the caller the deterministic report")

		raw, ok := payload["recommendations_unavailable_reason"]
		require.True(t, ok, "the reason key is present on a degrade")
		var reason string
		require.NoError(t, json.Unmarshal(raw, &reason))
		assert.NotEmpty(t, reason)
		assert.Contains(t, reason, "distinctive-oversize-b42e",
			"the analyzer's own reason reaches the wire rather than a generic substitute")
	})

	t.Run("successful synthesis omits the reason key", func(t *testing.T) {
		deps := analyzeUsageTestDeps{analyzer: fakeUsageAnalyzer{
			report: nonEmptyReportForRecommend(),
			recs: transcriptanalytics.SynthesisResult{
				Recommendations: []transcriptanalytics.Recommendation{
					{Title: "t", Category: "c", Impact: "high", Rationale: "r"},
				},
			},
		}}
		handled, body, isErr := callAnalyzeUsage(t, deps, `{"operation":"recommend"}`)
		require.True(t, handled)
		require.False(t, isErr)

		var payload map[string]json.RawMessage
		require.NoError(t, json.Unmarshal([]byte(body), &payload))
		assert.NotContains(t, payload, "recommendations_unavailable_reason",
			"omitempty keeps the key off a successful response; an implementation that always "+
				"emitted it would pass the degraded arm and fail here")
		require.Contains(t, payload, "recommendations")
		require.Contains(t, payload, "detectors")
	})

	t.Run("successful but empty never renders a bare null", func(t *testing.T) {
		// The analyzer returned success with a nil slice — the shape that produced
		// `recommendations: null` with no stated cause.
		deps := analyzeUsageTestDeps{analyzer: fakeUsageAnalyzer{
			report: nonEmptyReportForRecommend(),
			recs:   transcriptanalytics.SynthesisResult{Recommendations: nil},
		}}
		handled, body, isErr := callAnalyzeUsage(t, deps, `{"operation":"recommend"}`)
		require.True(t, handled)
		require.False(t, isErr)

		assert.NotContains(t, strings.ReplaceAll(body, " ", ""), `"recommendations":null`,
			"the key renders as an array, never as a bare null")

		var payload map[string]json.RawMessage
		require.NoError(t, json.Unmarshal([]byte(body), &payload))
		require.Contains(t, payload, "detectors")
		raw, ok := payload["recommendations"]
		require.True(t, ok, "the key is present, not deleted by an omitempty")

		var recs []transcriptanalytics.Recommendation
		require.NoError(t, json.Unmarshal(raw, &recs))
		assert.Empty(t, recs)
	})
}
