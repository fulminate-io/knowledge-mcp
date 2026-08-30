// SPDX-License-Identifier: Apache-2.0

package transcriptanalytics

import "sort"

// This file holds the TOKEN-HOTSPOT, CACHE-EFFICIENCY, and WASTE detectors — where tokens
// concentrate, how well the prompt cache is reused, and where spend is thrown away (errors,
// interrupts, max-token truncations) — as pure-Go folds over the loaded *corpus.

// AvgTokensPerSession is the mean per-session token spend across the window (averaged over
// per-session SUMs, so a long and a short session weigh equally).
type AvgTokensPerSession struct {
	AvgInputTokens  float64 `json:"avg_input_tokens"`
	AvgOutputTokens float64 `json:"avg_output_tokens"`
	AvgTotalTokens  float64 `json:"avg_total_tokens"`
	SessionCount    int64   `json:"session_count"`
}

// TokenByDimensionRow is one dimension value's token spend — the hotspot ranking of where
// input/output tokens accumulate (by tool or by subagent type).
type TokenByDimensionRow struct {
	Key          string `json:"key"`
	InputTokens  int64  `json:"input_tokens"`
	OutputTokens int64  `json:"output_tokens"`
}

// CacheEfficiency is the prompt-cache reuse ratio plus the ephemeral cache-creation split
// (1h vs 5m). CacheReadRatio = cache_read_tokens / input_tokens (0 when there is no input).
type CacheEfficiency struct {
	CacheReadTokens       int64   `json:"cache_read_tokens"`
	InputTokens           int64   `json:"input_tokens"`
	CacheReadRatio        float64 `json:"cache_read_ratio"`
	CacheCreation1hTokens int64   `json:"cache_creation_1h_tokens"`
	CacheCreation5mTokens int64   `json:"cache_creation_5m_tokens"`
}

// WasteSummary counts spend that produced no durable progress: API errors, user interrupts,
// and max-token truncations (a truncated turn typically forces a rerun), plus the ephemeral
// cache-creation totals and the token/time cost of the truncations.
type WasteSummary struct {
	CacheCreation1hTokens    int64 `json:"cache_creation_1h_tokens"`
	CacheCreation5mTokens    int64 `json:"cache_creation_5m_tokens"`
	APIErrorCount            int64 `json:"api_error_count"`
	InterruptedCount         int64 `json:"interrupted_count"`
	MaxTokensTruncationCount int64 `json:"max_tokens_truncation_count"`
	MaxTokensOutputTokens    int64 `json:"max_tokens_output_tokens"`
	MaxTokensDurationMs      int64 `json:"max_tokens_duration_ms"`
}

// avgTokensPerSession averages the per-session token SUMs (each session weighed equally) —
// mirroring the agent's QueryAvgTokensPerSession (rollup_query.go:157): AVG over the
// per-session sums, SessionCount = the number of sessions.
func (c *corpus) avgTokensPerSession() AvgTokensPerSession {
	n := int64(len(c.sessions))
	if n == 0 {
		return AvgTokensPerSession{}
	}
	var inSum, outSum int64
	for _, s := range c.sessions {
		inSum += s.inSum
		outSum += s.outSum
	}
	fn := float64(n)
	return AvgTokensPerSession{
		AvgInputTokens:  float64(inSum) / fn,
		AvgOutputTokens: float64(outSum) / fn,
		AvgTotalTokens:  float64(inSum+outSum) / fn,
		SessionCount:    n,
	}
}

// tokenByDimension sums input/output tokens per dimension key and ranks them — mirroring the
// agent's QueryBreakdown ordering (rollup_query.go:202): ordered by total tokens (in+out)
// desc, then key asc. The empty-key group carries every row whose dimension value is empty
// (COALESCE to ""). Used by the by-subagent-type detector family.
func tokenByDimension(m map[string]*tokenAcc) []TokenByDimensionRow {
	out := make([]TokenByDimensionRow, 0, len(m))
	for key, acc := range m {
		out = append(out, TokenByDimensionRow{Key: key, InputTokens: acc.inSum, OutputTokens: acc.outSum})
	}
	sort.Slice(out, func(i, j int) bool {
		ti, tj := out[i].InputTokens+out[i].OutputTokens, out[j].InputTokens+out[j].OutputTokens
		if ti != tj {
			return ti > tj
		}
		return out[i].Key < out[j].Key
	})
	return out
}

// ResultResidencyRow is one tool's share of the context its RESULTS occupied. It replaced a
// per-tool token breakdown that was structurally zero: the parser splits a turn into a
// zero-tool token row plus zero-token tool_use rows, so summing tokens by tool name put
// every token under the empty key and every named tool at zero. What a tool actually costs
// is not the tokens billed on its own row — there are none — but the context its result
// occupies for the rest of the lane.
//
// RESIDENT TOKENS IS A DERIVED UPPER BOUND, NOT A MEASUREMENT. It assumes a result stays
// resident for every model call that follows it in the lane, which context compaction
// falsifies. Its inputs are estimates too: result tokens come from a bytes-per-token ratio
// and a flat per-image cost, neither read from any usage record. SpilledResults is reported
// beside them so a reader can see how much of a tool's figure rests on a size RECOVERED
// from a spill notice rather than one directly observed.
type ResultResidencyRow struct {
	ToolName       string  `json:"tool_name"`
	Calls          int64   `json:"calls"`
	ResultBytes    int64   `json:"result_bytes"`
	ResultTokens   int64   `json:"result_tokens"`
	ResidentTokens int64   `json:"resident_tokens"`
	PctBilledInput float64 `json:"pct_billed_input"`
	SpilledResults int64   `json:"spilled_results"`
}

// resultResidencyByTool ranks tools by the context their results occupied — resident desc,
// then tool asc, the same weight-desc-then-key-asc discipline tokenByDimension uses.
//
// PctBilledInput is measured against the corpus's TOTAL billed input, which is the sum of
// cache reads, both ephemeral cache-creation splits and fresh input — the four quantities a
// caller is actually charged for.
//
// A tool whose results measured NOTHING is omitted rather than emitted as a row of zeros.
// Until a lane has been re-shipped, its result-size columns zero-fill, so an emit-always
// fold would fill every report with a row per tool carrying no information — and a reader
// could not tell those from a tool that genuinely returned nothing. The count of lanes
// actually carrying measurements is disclosed as CorpusProvenance.LanesWithResultBytes,
// which is where that question belongs.
func (c *corpus) resultResidencyByTool() []ResultResidencyRow {
	billed := float64(c.cacheRead + c.cc1h + c.cc5m + c.inputTokens)
	out := make([]ResultResidencyRow, 0, len(c.residency))
	for tool, acc := range c.residency {
		if acc.resultBytes == 0 && acc.resultTokens == 0 {
			continue
		}
		row := ResultResidencyRow{
			ToolName: tool, Calls: acc.calls,
			ResultBytes: acc.resultBytes, ResultTokens: acc.resultTokens,
			ResidentTokens: acc.residentTokens, SpilledResults: acc.spilledResults,
		}
		if billed > 0 {
			row.PctBilledInput = float64(acc.residentTokens) / billed * 100
		}
		out = append(out, row)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ResidentTokens != out[j].ResidentTokens {
			return out[i].ResidentTokens > out[j].ResidentTokens
		}
		return out[i].ToolName < out[j].ToolName
	})
	return out
}

// cacheEfficiency sums the cache-read/input totals + the 1h/5m ephemeral split and derives
// the read ratio (guarding divide-by-zero) — mirroring cacheEfficiencyPG (rollup_insights.go:125).
func (c *corpus) cacheEfficiency() CacheEfficiency {
	out := CacheEfficiency{
		CacheReadTokens:       c.cacheRead,
		InputTokens:           c.inputTokens,
		CacheCreation1hTokens: c.cc1h,
		CacheCreation5mTokens: c.cc5m,
	}
	if out.InputTokens > 0 {
		out.CacheReadRatio = float64(out.CacheReadTokens) / float64(out.InputTokens)
	}
	return out
}

// wasteSummary reports the waste signals + the max-token truncation cost — mirroring the
// agent's wasteInsightsPG (rollup_insights.go:185): the 1h/5m ephemeral sums, api-error +
// interrupted counts, and the max_tokens truncation count / output-token / RAW-duration
// cost (the truncation duration is RAW, NOT idle-guarded).
func (c *corpus) wasteSummary() WasteSummary {
	return WasteSummary{
		CacheCreation1hTokens:    c.cc1h,
		CacheCreation5mTokens:    c.cc5m,
		APIErrorCount:            c.apiErrorCount,
		InterruptedCount:         c.interruptedCount,
		MaxTokensTruncationCount: c.maxTokCount,
		MaxTokensOutputTokens:    c.maxTokOutput,
		MaxTokensDurationMs:      c.maxTokDurationRaw,
	}
}
