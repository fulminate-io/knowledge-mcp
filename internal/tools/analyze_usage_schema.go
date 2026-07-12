// SPDX-License-Identifier: Apache-2.0

package tools

import "github.com/fulminate-io/knowledge-mcp/internal/kgtools"

// AnalyzeUsageToolDef returns the analyze_usage tool definition. Modeled on
// WorkerToolDef (op-dispatched: one tool with an `operation` enum rather than
// several top-level tools), so a new analysis operation is a schema edit + a
// dispatch case.
//
// Operations:
//
//   - run-detectors: return the deterministic agent-flow metrics computed over the
//     local transcript cache (duplicate commands, per-tool latency + total time,
//     token hotspots, cache efficiency, subagent wall-time, agent-chain
//     over-orchestration, and waste including max-token truncations). Zero inference.
//   - recommend: run the detectors AND, when a local LLM is configured, synthesize
//     ranked, actionable recommendations from the report. Degrades to detector-only
//     output when no LLM is configured.
//
// The analysis runs entirely on the local machine over a parquet cache the transcript
// upload writes; no transcript data leaves the device. This definition is client-side;
// the intercept (intercept_analyze_usage.go) handles the call in-process.
func AnalyzeUsageToolDef() kgtools.MCPTool {
	return kgtools.MCPTool{
		Name: "analyze_usage",
		Description: "Analyze your own AI coding-assistant usage transcripts for agent-flow optimization, " +
			"entirely on your machine (no transcript data leaves your device). " +
			"run-detectors: return deterministic metrics — redundantly-rerun commands, per-tool latency and total wall-time, " +
			"per-session and per-subagent token hotspots, prompt-cache efficiency, subagent wall-time, " +
			"agent-chain over-orchestration, and waste (API errors, interrupts, max-token truncations). " +
			"recommend: run the detectors AND synthesize ranked, actionable recommendations using your locally-configured LLM " +
			"(degrades to detector-only output when no LLM is configured). " +
			"COLD-CACHE NOTE: the local cache is populated on transcript upload, and unchanged sessions are skipped — so on an " +
			"established install run the transcript upload once with --seed to backfill the cache before analyzing.",
		InputSchema: kgtools.InputSchema{
			Type: "object",
			Properties: map[string]kgtools.Property{
				"operation": {
					Type:        "string",
					Description: "Operation to perform: run-detectors (deterministic metrics only) or recommend (detectors + LLM-synthesized recommendations)",
					Enum:        []string{"run-detectors", "recommend"},
				},
				"format": {Type: "string", Description: "Output format: 'json' (default)."},
			},
			Required: []string{"operation"},
		},
	}
}

// analyzeUsageArgs holds parsed arguments for the analyze_usage tool.
type analyzeUsageArgs struct {
	Operation string `json:"operation"`
	Format    string `json:"format"`
}
