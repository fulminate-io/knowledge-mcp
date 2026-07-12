// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/transcriptanalytics"
)

// fakeUsageAnalyzer is a UsageAnalyzerAPI stub returning canned reports.
type fakeUsageAnalyzer struct {
	report *transcriptanalytics.DetectorReport
	recs   transcriptanalytics.SynthesisResult
}

func (f fakeUsageAnalyzer) RunDetectors(context.Context) (*transcriptanalytics.DetectorReport, error) {
	return f.report, nil
}

func (f fakeUsageAnalyzer) Recommend(context.Context) (*transcriptanalytics.DetectorReport, transcriptanalytics.SynthesisResult, error) {
	return f.report, f.recs, nil
}

// analyzeUsageTestDeps is a minimal ClientDeps carrying only a UsageAnalyzer; the
// other accessors are unused by InterceptAnalyzeUsage. Embeds workerTestDeps to inherit
// every other ClientDeps method as a nil stub.
type analyzeUsageTestDeps struct {
	workerTestDeps
	analyzer UsageAnalyzerAPI
}

func (d analyzeUsageTestDeps) UsageAnalyzer() UsageAnalyzerAPI { return d.analyzer }

func callAnalyzeUsage(t *testing.T, deps ClientDeps, argsJSON string) (handled bool, body string, isErr bool) {
	t.Helper()
	h, res := InterceptAnalyzeUsage(deps, kgtools.CallToolParams{
		Name:      "analyze_usage",
		Arguments: json.RawMessage(argsJSON),
	})
	if !h {
		return false, "", false
	}
	require.NotEmpty(t, res.Content, "intercept handled but returned no content")
	return true, res.Content[0].Text, res.IsError
}

// TestInterceptAnalyzeUsage_NameFiltering pins that the intercept ignores other tools.
func TestInterceptAnalyzeUsage_NameFiltering(t *testing.T) {
	deps := analyzeUsageTestDeps{}
	for _, name := range []string{"worker", "search", "query", ""} {
		handled, _ := InterceptAnalyzeUsage(deps, kgtools.CallToolParams{Name: name, Arguments: json.RawMessage(`{}`)})
		assert.False(t, handled, "tool %q must not be handled by InterceptAnalyzeUsage", name)
	}
}

// TestInterceptAnalyzeUsage_RunDetectors renders the detector report over a populated
// (non-empty) analyzer.
func TestInterceptAnalyzeUsage_RunDetectors(t *testing.T) {
	report := &transcriptanalytics.DetectorReport{
		DuplicateCommands:   []transcriptanalytics.DuplicateCommandRow{{SessionID: "S1", ToolName: "Bash", RunCount: 2, WastedDurationMs: 2000}},
		AvgTokensPerSession: transcriptanalytics.AvgTokensPerSession{SessionCount: 1, AvgInputTokens: 2000},
	}
	deps := analyzeUsageTestDeps{analyzer: fakeUsageAnalyzer{report: report}}

	handled, body, isErr := callAnalyzeUsage(t, deps, `{"operation":"run-detectors"}`)
	require.True(t, handled, "run-detectors must be handled client-side")
	require.False(t, isErr, "expected non-error result, got: %s", body)
	assert.Contains(t, body, "duplicate_commands", "renders the detector report JSON")
	assert.Contains(t, body, "Bash")
}

// TestInterceptAnalyzeUsage_Recommend renders detectors + recommendations.
func TestInterceptAnalyzeUsage_Recommend(t *testing.T) {
	report := &transcriptanalytics.DetectorReport{
		AvgTokensPerSession: transcriptanalytics.AvgTokensPerSession{SessionCount: 1},
	}
	recs := transcriptanalytics.SynthesisResult{Recommendations: []transcriptanalytics.Recommendation{
		{Title: "Stop rerunning Bash", Category: "duplicate_work", Impact: "high", Rationale: "wasted 2s"},
	}}
	deps := analyzeUsageTestDeps{analyzer: fakeUsageAnalyzer{report: report, recs: recs}}

	handled, body, isErr := callAnalyzeUsage(t, deps, `{"operation":"recommend"}`)
	require.True(t, handled)
	require.False(t, isErr, body)
	assert.Contains(t, body, "recommendations")
	assert.Contains(t, body, "Stop rerunning Bash")
}

// TestInterceptAnalyzeUsage_NilAnalyzerColdCacheHint proves the cold-cache --seed hint
// fires both when the analyzer is nil and when the cache resolves to zero data.
func TestInterceptAnalyzeUsage_NilAnalyzerColdCacheHint(t *testing.T) {
	// Nil analyzer.
	deps := analyzeUsageTestDeps{analyzer: nil}
	handled, body, isErr := callAnalyzeUsage(t, deps, `{"operation":"run-detectors"}`)
	require.True(t, handled)
	require.False(t, isErr)
	assert.Contains(t, body, "--seed", "nil analyzer must render the --seed cold-cache hint")

	// Empty report (zero sessions) — cold cache.
	emptyDeps := analyzeUsageTestDeps{analyzer: fakeUsageAnalyzer{report: &transcriptanalytics.DetectorReport{}}}
	handled, body, isErr = callAnalyzeUsage(t, emptyDeps, `{"operation":"run-detectors"}`)
	require.True(t, handled)
	require.False(t, isErr)
	assert.Contains(t, body, "--seed", "empty cache must render the --seed hint")
}

// TestAnalyzeUsageToolDef_Registered proves the tool is in the client-owned catalog
// with the run-detectors|recommend operation enum.
func TestAnalyzeUsageToolDef_Registered(t *testing.T) {
	var def *kgtools.MCPTool
	for _, tool := range AllToolSchemas() {
		if tool.Name == "analyze_usage" {
			t := tool
			def = &t
			break
		}
	}
	require.NotNil(t, def, "analyze_usage must be registered in AllToolSchemas")
	op, ok := def.InputSchema.Properties["operation"]
	require.True(t, ok, "the operation param is present")
	assert.ElementsMatch(t, []string{"run-detectors", "recommend"}, op.Enum)
	assert.Contains(t, strings.ToLower(def.Description), "--seed", "the tool doc carries the cold-cache --seed hint")
}
