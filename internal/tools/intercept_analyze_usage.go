// SPDX-License-Identifier: Apache-2.0

// intercept_analyze_usage.go — client-side intercept for the `analyze_usage` MCP
// tool. The analyzer (the pure-Go transcriptanalytics engine over the local
// transcript parquet cache) lives in the client process, so the call is handled
// in-process here and never falls through to the server. The op-dispatch +
// render shape follows the worker intercept's, from before the worker system
// was removed.
//
// Test seam: UsageAnalyzerAPI is declared in this file (not deps.go) so the intercept
// test can satisfy it with a small fake; *transcriptanalytics.Service satisfies it
// structurally, so production wiring needs no adapter.

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/transcriptanalytics"
)

// UsageAnalyzerAPI is the narrow surface InterceptAnalyzeUsage calls on the client-side
// analyzer. *transcriptanalytics.Service satisfies it structurally; tests inject a fake.
type UsageAnalyzerAPI interface {
	RunDetectors(ctx context.Context, base transcriptanalytics.Filters) (*transcriptanalytics.DetectorReport, error)
	Recommend(ctx context.Context, base transcriptanalytics.Filters) (*transcriptanalytics.DetectorReport, transcriptanalytics.SynthesisResult, error)
}

// coldCacheHint is shown when the analyzer is unavailable OR the local cache has no
// analyzable data yet. The transcript cache is populated on upload and unchanged
// sessions are skipped, so an established install needs one --seed pass to backfill. It
// also states the cache's retention, because the same reader who has just been told the
// cache is EMPTY is the one most likely to later compare its counts against the live
// transcript directory and read the difference as a bug.
const coldCacheHint = "analyze_usage: no local transcript analytics data yet. " +
	"The local cache is populated on transcript upload, and unchanged sessions are skipped — " +
	"run the transcript upload once with --seed to backfill the cache from your existing sessions, then re-run analyze_usage. " +
	"Once populated, that cache RETAINS a session after the CLI has removed the session's own transcript file, so its counts " +
	"legitimately exceed what is currently on disk; each report's corpus block states the exact basis it was computed over."

// analyzeUsageRecommendation is the render envelope for the recommend operation: the
// deterministic detector report plus any LLM-synthesized recommendations.
//
// Recommendations carries NO omitempty deliberately. A degraded synthesis leaves the slice
// nil, and omitempty would delete the key outright — trading a null for a missing key, so a
// caller could not tell "no recommendations" from "this response has no recommendations
// field". The key always renders, as an array, and RecommendationsUnavailable says why it
// is empty. That field DOES carry omitempty, because it is genuinely absent on success.
type analyzeUsageRecommendation struct {
	Detectors                  *transcriptanalytics.DetectorReport  `json:"detectors"`
	Recommendations            []transcriptanalytics.Recommendation `json:"recommendations"`
	RecommendationsUnavailable string                               `json:"recommendations_unavailable_reason,omitempty"`
}

// InterceptAnalyzeUsage handles the analyze_usage tool entirely client-side. Returns
// (true, result) when the call was handled; (false, zero) when it is not this tool.
// A nil analyzer or an empty cache both render the cold-cache --seed hint.
func InterceptAnalyzeUsage(ctx context.Context, deps ClientDeps, params kgtools.CallToolParams) (bool, kgtools.ToolResult) {
	if params.Name != "analyze_usage" {
		return false, kgtools.ToolResult{}
	}
	if err := rejectUndeclaredParams("analyze_usage", "", AnalyzeUsageToolDef().InputSchema.Properties, params.Arguments); err != nil {
		return true, errorResult(err.Error())
	}
	var a analyzeUsageArgs
	if err := json.Unmarshal(params.Arguments, &a); err != nil {
		return true, errorResult("analyze_usage: invalid arguments: " + decodeArgsError(params.Arguments, err))
	}
	analyzer := deps.UsageAnalyzer()
	if analyzer == nil {
		return true, textResult(coldCacheHint)
	}

	base, err := a.filters()
	if err != nil {
		return true, errorResult(err.Error())
	}

	switch a.Operation {
	case "run-detectors", "":
		report, err := analyzer.RunDetectors(ctx, base)
		if err != nil {
			return true, errorResult("analyze_usage:run-detectors: " + err.Error())
		}
		if reportEmpty(report) {
			return true, textResult(coldCacheHint)
		}
		return true, jsonResult(report)
	case "recommend":
		report, recs, err := analyzer.Recommend(ctx, base)
		if err != nil {
			return true, errorResult("analyze_usage:recommend: " + err.Error())
		}
		if reportEmpty(report) {
			return true, textResult(coldCacheHint)
		}
		return true, jsonResult(analyzeUsageRecommendation{
			Detectors:                  report,
			Recommendations:            renderedRecommendations(recs),
			RecommendationsUnavailable: recs.Unavailable,
		})
	default:
		return true, unknownOperationResult("analyze_usage", a.Operation,
			[]string{"run-detectors", "recommend"})
	}
}

// filters builds the analyzer's population selector from the parsed arguments and validates
// it, so both operation arms narrow by one implementation of the rules rather than a copy
// each.
//
// A malformed timestamp is an ERROR naming the field and the expected format. Parsing it to
// a zero time instead would leave that bound unset, silently widening the population to
// unbounded and returning a plausible report about records the caller never asked for.
func (a analyzeUsageArgs) filters() (transcriptanalytics.Filters, error) {
	f := transcriptanalytics.Filters{
		Scope:     transcriptanalytics.Scope(a.Scope),
		SessionID: a.Session,
		AgentID:   a.Agent,
	}
	var err error
	if f.Since, err = parseAnalyzeUsageTime("since", a.Since); err != nil {
		return transcriptanalytics.Filters{}, err
	}
	if f.Until, err = parseAnalyzeUsageTime("until", a.Until); err != nil {
		return transcriptanalytics.Filters{}, err
	}
	if err := f.Validate(); err != nil {
		return transcriptanalytics.Filters{}, err
	}
	return f, nil
}

// parseAnalyzeUsageTime parses one RFC3339 bound. An EMPTY value is the unset bound and
// yields the zero time with no error; a NON-empty unparseable one is an error.
func parseAnalyzeUsageTime(field, value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}
	ts, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("analyze_usage: %s %q is not an RFC3339 timestamp (want e.g. 2026-08-29T00:00:00Z): %w", field, value, err)
	}
	return ts, nil
}

// renderedRecommendations returns a slice that marshals to a JSON ARRAY rather than null.
// The normalization lives here, at the render boundary, because that is where the defect
// is: a nil slice anywhere upstream — a degraded synthesis, or a model that returned
// nothing — marshals to `recommendations: null`, and a caller cannot read a cause out of a
// null. The reason for an empty array is carried separately.
func renderedRecommendations(recs transcriptanalytics.SynthesisResult) []transcriptanalytics.Recommendation {
	if recs.Recommendations == nil {
		return []transcriptanalytics.Recommendation{}
	}
	return recs.Recommendations
}

// reportEmpty reports whether the detector report carries no analyzable data — a nil
// report or zero sessions (an empty cache, or a corpus of only excluded meta/synthetic
// rows). Zero sessions is the reliable "nothing to analyze → suggest --seed" signal.
func reportEmpty(r *transcriptanalytics.DetectorReport) bool {
	return r == nil || r.AvgTokensPerSession.SessionCount == 0
}
