// SPDX-License-Identifier: Apache-2.0

package transcriptanalytics

import (
	"context"
	"runtime"

	"golang.org/x/sync/errgroup"
)

// DetectorReport is the full deterministic detector output over the local cache — the
// aggregates + exemplars the BYOK synthesis stage and the MCP renderer consume. Every
// field is a re-authored detector family; there is no LLM inference at this layer.
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

// RunDetectors runs every deterministic detector family CONCURRENTLY over the shared
// DuckDB pool (database/sql gives safe concurrent reads; DuckDB parallelizes within a
// query) and assembles the results into one DetectorReport. An EMPTY cache
// short-circuits to a zero-value report and a nil error via the cachePaths() zero-path
// guard — read_parquet is never handed an empty set. Each family writes its own local,
// and the report is assembled AFTER errgroup.Wait() establishes happens-before, so the
// concurrent fan-out is race-free.
func (s *Service) RunDetectors(ctx context.Context) (*DetectorReport, error) {
	paths, err := s.cachePaths()
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return &DetectorReport{}, nil
	}

	f := Filters{}
	var (
		dup    []DuplicateCommandRow
		lat    []ToolLatencyRow
		toolTT []ToolTimeTotalRow
		avg    AvgTokensPerSession
		tokTby []TokenByDimensionRow
		tokSub []TokenByDimensionRow
		cache  CacheEfficiency
		subs   []SubagentWallTime
		chains []AgentChainRow
		waste  WasteSummary
	)

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(runtime.NumCPU())
	g.Go(func() (e error) { dup, e = s.duplicateCommandsFrom(gctx, paths, f); return })
	g.Go(func() (e error) { lat, e = s.toolLatencyFrom(gctx, paths, f); return })
	g.Go(func() (e error) { toolTT, e = s.toolTimeTotalFrom(gctx, paths, f); return })
	g.Go(func() (e error) { avg, e = s.avgTokensPerSessionFrom(gctx, paths, f); return })
	g.Go(func() (e error) { tokTby, e = s.tokenByDimensionFrom(gctx, paths, colToolName, f); return })
	g.Go(func() (e error) { tokSub, e = s.tokenByDimensionFrom(gctx, paths, colSubagentType, f); return })
	g.Go(func() (e error) { cache, e = s.cacheEfficiencyFrom(gctx, paths, f); return })
	g.Go(func() (e error) { subs, e = s.subagentWallTimeFrom(gctx, paths, f); return })
	g.Go(func() (e error) { chains, e = s.agentChainFrom(gctx, paths, f); return })
	g.Go(func() (e error) { waste, e = s.wasteSummaryFrom(gctx, paths, f); return })
	if err := g.Wait(); err != nil {
		return nil, err
	}

	return &DetectorReport{
		DuplicateCommands:    dup,
		ToolLatency:          lat,
		ToolTimeTotals:       toolTT,
		AvgTokensPerSession:  avg,
		TokensByTool:         tokTby,
		TokensBySubagentType: tokSub,
		CacheEfficiency:      cache,
		SubagentWallTime:     subs,
		AgentChains:          chains,
		Waste:                waste,
	}, nil
}
