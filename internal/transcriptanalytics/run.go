// SPDX-License-Identifier: Apache-2.0

package transcriptanalytics

import (
	"context"
	"time"
)

// CorpusProvenance states the basis every number in the report was computed over: which
// lanes the loader read, how many records survived the intake filter, and the record window
// they span. It exists because the analyzer reads the RETAINED parquet cache under
// cacheRoot, which is append-only, while a reader comparing against the CLI's own transcript
// directory is looking at a rolling window — two different corpora, and without a stated
// basis the difference reads as a counting bug.
type CorpusProvenance struct {
	Scope     string `json:"scope"`
	Selector  string `json:"selector"`
	LaneCount int64  `json:"lane_count"`
	// LanesWithResultBytes counts lanes carrying at least one measured tool-result size.
	// Compare it against LaneCount: EQUAL means every lane has been re-shipped since the
	// result-size columns landed; ZERO means none has, and every residency figure in the
	// report is an artifact of a missing column rather than a measurement. A lane cached
	// before those columns existed zero-fills them, and a zero is indistinguishable from a
	// genuinely empty result without this count — which is why it is a field a caller can
	// branch on rather than a caveat in prose.
	LanesWithResultBytes int64  `json:"lanes_with_result_bytes"`
	RecordCount          int64  `json:"record_count"`
	SessionCount         int64  `json:"session_count"`
	AgentCount           int64  `json:"agent_count"`
	FirstRecordTS        string `json:"first_record_ts"`
	LastRecordTS         string `json:"last_record_ts"`
	CacheRoot            string `json:"cache_root"`
}

// FamilyTruncation discloses the bounded detector families: how many rows each returned
// against how many it found. Truncated is true when EITHER family returned fewer rows than
// it measured, so a reader can tell a short family from a bounded one without comparing the
// pairs itself. It adopts the repo's `truncated` disclosure key deliberately.
type FamilyTruncation struct {
	Truncated                 bool  `json:"truncated"`
	SubagentWallTimeReturned  int64 `json:"subagent_wall_time_returned"`
	SubagentWallTimeTotal     int64 `json:"subagent_wall_time_total"`
	DuplicateCommandsReturned int64 `json:"duplicate_commands_returned"`
	DuplicateCommandsTotal    int64 `json:"duplicate_commands_total"`
}

// DetectorReport is the full deterministic detector output over the local cache — the
// aggregates + exemplars the BYOK synthesis stage and the MCP renderer consume. Every field
// is a re-authored detector family; there is no LLM inference at this layer.
type DetectorReport struct {
	DuplicateCommands     []DuplicateCommandRow `json:"duplicate_commands"`
	ToolLatency           []ToolLatencyRow      `json:"tool_latency"`
	ToolTimeTotals        []ToolTimeTotalRow    `json:"tool_time_totals"`
	AvgTokensPerSession   AvgTokensPerSession   `json:"avg_tokens_per_session"`
	TokensBySubagentType  []TokenByDimensionRow `json:"tokens_by_subagent_type"`
	ResultResidencyByTool []ResultResidencyRow  `json:"result_residency_by_tool"`
	CacheEfficiency       CacheEfficiency       `json:"cache_efficiency"`
	SubagentWallTime      []SubagentWallTime    `json:"subagent_wall_time"`
	AgentChains           []AgentChainRow       `json:"agent_chains"`
	Waste                 WasteSummary          `json:"waste"`
	Corpus                CorpusProvenance      `json:"corpus"`
	Truncation            FamilyTruncation      `json:"truncation"`
	// LaneDetail is present ONLY when the corpus was narrowed to a single lane. It is a
	// POINTER with omitempty so a wider scope omits the key outright, rather than emitting a
	// zero-valued object a reader could mistake for a measured lane of all zeros.
	LaneDetail *LaneDetail `json:"lane_detail,omitempty"`
}

// provenance renders the corpus's disclosed basis. The two timestamps render as RFC3339 in
// the record's own offset, and as the EMPTY STRING when the corpus folded no row at all — an
// empty corpus must stay distinguishable from one that genuinely begins at the zero instant.
func (c *corpus) provenance(cacheRoot string, f Filters) CorpusProvenance {
	p := CorpusProvenance{
		Scope:                string(f.resolved()),
		Selector:             f.Selector(),
		LaneCount:            c.laneCount,
		LanesWithResultBytes: c.lanesWithResultBytes,
		RecordCount:          c.recordCount,
		SessionCount:         int64(len(c.sessions)),
		AgentCount:           int64(len(c.subagents)),
		CacheRoot:            cacheRoot,
	}
	if !c.minTS.IsZero() {
		p.FirstRecordTS = c.minTS.Format(time.RFC3339)
		p.LastRecordTS = c.maxTS.Format(time.RFC3339)
	}
	return p
}

// RunDetectors loads the local parquet cache into the corpus aggregator ONCE, then
// materializes every deterministic detector family from the corpus accumulators. The load
// (the parallel file decode + fold) is the cost; the folds are cheap in-memory sorts, so no
// per-family concurrency is needed. An EMPTY cache yields an empty corpus and thus a
// zero-value report (every fold returns empty over empty accumulators) with a nil error.
//
// The filters are VALIDATED before anything is read: an unusable scope-and-selector
// combination is an error here rather than a plausible report about the wrong population.
func (s *Service) RunDetectors(ctx context.Context, base Filters) (*DetectorReport, error) {
	if err := base.Validate(); err != nil {
		return nil, err
	}
	c, err := s.loadCorpus(ctx, base)
	if err != nil {
		return nil, err
	}
	dupes, dupeTotal := c.duplicateCommands()
	subagents, subagentTotal := c.subagentWallTime()
	var lane *LaneDetail
	if base.resolved() == ScopeSingle {
		lane = c.laneDetail(base.AgentID)
	}
	return &DetectorReport{
		DuplicateCommands:     dupes,
		ToolLatency:           c.toolLatency(),
		ToolTimeTotals:        c.toolTimeTotals(),
		AvgTokensPerSession:   c.avgTokensPerSession(),
		TokensBySubagentType:  tokenByDimension(c.tokensBySubagent),
		ResultResidencyByTool: c.resultResidencyByTool(),
		CacheEfficiency:       c.cacheEfficiency(),
		SubagentWallTime:      subagents,
		AgentChains:           c.agentChains(),
		Waste:                 c.wasteSummary(),
		Corpus:                c.provenance(s.cacheRoot, base),
		Truncation: FamilyTruncation{
			Truncated:                 int64(len(subagents)) < subagentTotal || int64(len(dupes)) < dupeTotal,
			SubagentWallTimeReturned:  int64(len(subagents)),
			SubagentWallTimeTotal:     subagentTotal,
			DuplicateCommandsReturned: int64(len(dupes)),
			DuplicateCommandsTotal:    dupeTotal,
		},
		LaneDetail: lane,
	}, nil
}
