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
// (COALESCE to ""). Shared by the by-tool and by-subagent-type detector families.
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
