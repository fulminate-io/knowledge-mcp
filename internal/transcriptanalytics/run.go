// SPDX-License-Identifier: Apache-2.0

package transcriptanalytics

import "context"

// DetectorReport is the full deterministic detector output over the local cache — the
// aggregates + exemplars the BYOK synthesis stage and the MCP renderer consume. Every field
// is a re-authored detector family; there is no LLM inference at this layer.
type DetectorReport struct {
	DuplicateCommands    []DuplicateCommandRow `json:"duplicate_commands"`
	ToolLatency          []ToolLatencyRow      `json:"tool_latency"`
	ToolTimeTotals       []ToolTimeTotalRow    `json:"tool_time_totals"`
	AvgTokensPerSession  AvgTokensPerSession   `json:"avg_tokens_per_session"`
	TokensByTool         []TokenByDimensionRow `json:"tokens_by_tool"`
	TokensBySubagentType []TokenByDimensionRow `json:"tokens_by_subagent_type"`
	CacheEfficiency      CacheEfficiency       `json:"cache_efficiency"`
	SubagentWallTime     []SubagentWallTime    `json:"subagent_wall_time"`
	AgentChains          []AgentChainRow       `json:"agent_chains"`
	Waste                WasteSummary          `json:"waste"`
}

// RunDetectors loads the local parquet cache into the corpus aggregator ONCE, then
// materializes every deterministic detector family from the corpus accumulators. The load
// (the parallel file decode + fold) is the cost; the folds are cheap in-memory sorts, so no
// per-family concurrency is needed. An EMPTY cache yields an empty corpus and thus a
// zero-value report (every fold returns empty over empty accumulators) with a nil error.
func (s *Service) RunDetectors(ctx context.Context) (*DetectorReport, error) {
	c, err := s.loadCorpus(ctx)
	if err != nil {
		return nil, err
	}
	return &DetectorReport{
		DuplicateCommands:    c.duplicateCommands(),
		ToolLatency:          c.toolLatency(),
		ToolTimeTotals:       c.toolTimeTotals(),
		AvgTokensPerSession:  c.avgTokensPerSession(),
		TokensByTool:         tokenByDimension(c.tokensByTool),
		TokensBySubagentType: tokenByDimension(c.tokensBySubagent),
		CacheEfficiency:      c.cacheEfficiency(),
		SubagentWallTime:     c.subagentWallTime(),
		AgentChains:          c.agentChains(),
		Waste:                c.wasteSummary(),
	}, nil
}
