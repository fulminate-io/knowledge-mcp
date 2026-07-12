// SPDX-License-Identifier: Apache-2.0

// intercept_analyze_usage.go — client-side intercept for the `analyze_usage` MCP
// tool. The analyzer (embedded DuckDB over the local transcript parquet cache) lives
// in the client process, so the call is handled in-process here and never falls
// through to the server. Mirrors the worker intercept's op-dispatch + render shape.
//
// Test seam: UsageAnalyzerAPI is declared in this file (not deps.go) so the intercept
// test can satisfy it with a small fake; *transcriptanalytics.Service satisfies it
// structurally, so production wiring needs no adapter.

package tools

import (
	"context"
	"encoding/json"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/transcriptanalytics"
)

// UsageAnalyzerAPI is the narrow surface InterceptAnalyzeUsage calls on the client-side
// analyzer. *transcriptanalytics.Service satisfies it structurally; tests inject a fake.
type UsageAnalyzerAPI interface {
	RunDetectors(ctx context.Context) (*transcriptanalytics.DetectorReport, error)
	Recommend(ctx context.Context) (*transcriptanalytics.DetectorReport, transcriptanalytics.SynthesisResult, error)
}

// coldCacheHint is shown when the analyzer is unavailable OR the local cache has no
// analyzable data yet. The transcript cache is populated on upload and unchanged
// sessions are skipped, so an established install needs one --seed pass to backfill.
const coldCacheHint = "analyze_usage: no local transcript analytics data yet. " +
	"The local cache is populated on transcript upload, and unchanged sessions are skipped — " +
	"run the transcript upload once with --seed to backfill the cache from your existing sessions, then re-run analyze_usage."

// analyzeUsageRecommendation is the render envelope for the recommend operation: the
// deterministic detector report plus any LLM-synthesized recommendations.
type analyzeUsageRecommendation struct {
	Detectors       *transcriptanalytics.DetectorReport  `json:"detectors"`
	Recommendations []transcriptanalytics.Recommendation `json:"recommendations"`
}

// InterceptAnalyzeUsage handles the analyze_usage tool entirely client-side. Returns
// (true, result) when the call was handled; (false, zero) when it is not this tool.
// A nil analyzer or an empty cache both render the cold-cache --seed hint.
func InterceptAnalyzeUsage(deps ClientDeps, params kgtools.CallToolParams) (bool, kgtools.ToolResult) {
	if params.Name != "analyze_usage" {
		return false, kgtools.ToolResult{}
	}
	var a analyzeUsageArgs
	if err := json.Unmarshal(params.Arguments, &a); err != nil {
		return true, errorResult("analyze_usage: invalid arguments: " + err.Error())
	}
	analyzer := deps.UsageAnalyzer()
	if analyzer == nil {
		return true, textResult(coldCacheHint)
	}
	ctx := context.Background()

	switch a.Operation {
	case "run-detectors", "":
		report, err := analyzer.RunDetectors(ctx)
		if err != nil {
			return true, errorResult("analyze_usage:run-detectors: " + err.Error())
		}
		if reportEmpty(report) {
			return true, textResult(coldCacheHint)
		}
		return true, jsonResult(report)
	case "recommend":
		report, recs, err := analyzer.Recommend(ctx)
		if err != nil {
			return true, errorResult("analyze_usage:recommend: " + err.Error())
		}
		if reportEmpty(report) {
			return true, textResult(coldCacheHint)
		}
		return true, jsonResult(analyzeUsageRecommendation{Detectors: report, Recommendations: recs.Recommendations})
	default:
		return true, errorResult("analyze_usage: unknown operation " + a.Operation + " — valid operations: run-detectors, recommend")
	}
}

// reportEmpty reports whether the detector report carries no analyzable data — a nil
// report or zero sessions (an empty cache, or a corpus of only excluded meta/synthetic
// rows). Zero sessions is the reliable "nothing to analyze → suggest --seed" signal.
func reportEmpty(r *transcriptanalytics.DetectorReport) bool {
	return r == nil || r.AvgTokensPerSession.SessionCount == 0
}
