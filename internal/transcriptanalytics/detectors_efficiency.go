// SPDX-License-Identifier: Apache-2.0

package transcriptanalytics

import (
	"context"
	"database/sql"
	"fmt"
)

// This file holds the TOKEN-HOTSPOT, CACHE-EFFICIENCY, and WASTE detectors — where
// tokens concentrate, how well the prompt cache is reused, and where spend is thrown
// away (errors, interrupts, max-token truncations).

// AvgTokensPerSession is the mean per-session token spend across the window (averaged
// over per-session SUMs, so a long and a short session weigh equally).
type AvgTokensPerSession struct {
	AvgInputTokens  float64 `json:"avg_input_tokens"`
	AvgOutputTokens float64 `json:"avg_output_tokens"`
	AvgTotalTokens  float64 `json:"avg_total_tokens"`
	SessionCount    int64   `json:"session_count"`
}

// TokenByDimensionRow is one dimension value's token spend — the hotspot ranking of
// where input/output tokens accumulate (by tool or by subagent type).
type TokenByDimensionRow struct {
	Key          string `json:"key"`
	InputTokens  int64  `json:"input_tokens"`
	OutputTokens int64  `json:"output_tokens"`
}

// CacheEfficiency is the prompt-cache reuse ratio plus the ephemeral cache-creation
// split (1h vs 5m). CacheReadRatio = cache_read_tokens / input_tokens (0 when there is
// no input), a proxy for how much prompt context was served from cache.
type CacheEfficiency struct {
	CacheReadTokens       int64   `json:"cache_read_tokens"`
	InputTokens           int64   `json:"input_tokens"`
	CacheReadRatio        float64 `json:"cache_read_ratio"`
	CacheCreation1hTokens int64   `json:"cache_creation_1h_tokens"`
	CacheCreation5mTokens int64   `json:"cache_creation_5m_tokens"`
}

// WasteSummary counts spend that produced no durable progress: API errors, user
// interrupts, and max-token truncations (a truncated turn typically forces a rerun),
// plus the ephemeral cache-creation totals and the token/time cost of the truncations.
type WasteSummary struct {
	CacheCreation1hTokens    int64 `json:"cache_creation_1h_tokens"`
	CacheCreation5mTokens    int64 `json:"cache_creation_5m_tokens"`
	APIErrorCount            int64 `json:"api_error_count"`
	InterruptedCount         int64 `json:"interrupted_count"`
	MaxTokensTruncationCount int64 `json:"max_tokens_truncation_count"`
	MaxTokensOutputTokens    int64 `json:"max_tokens_output_tokens"`
	MaxTokensDurationMs      int64 `json:"max_tokens_duration_ms"`
}

// avgTokensPerSessionFrom averages the per-session token SUMs (each session weighed
// equally) — AVG over a subquery of per-session sums, not a naive row average.
func (s *Service) avgTokensPerSessionFrom(ctx context.Context, paths []string, f Filters) (AvgTokensPerSession, error) {
	where, args := f.where()
	query := fmt.Sprintf(
		`SELECT
			COALESCE(AVG(in_sum), 0),
			COALESCE(AVG(out_sum), 0),
			COALESCE(AVG(in_sum + out_sum), 0),
			COUNT(*)
		FROM (
			SELECT %s,
				COALESCE(SUM(%s), 0) AS in_sum,
				COALESCE(SUM(%s), 0) AS out_sum
			FROM %s %s
			GROUP BY %s
		) per_session`,
		colSessionID, colInputTokens, colOutputTokens,
		fromClause(paths), where, colSessionID,
	)

	var out AvgTokensPerSession
	err := s.queryRow(ctx, query, args, func(row *sql.Row) error {
		return row.Scan(&out.AvgInputTokens, &out.AvgOutputTokens, &out.AvgTotalTokens, &out.SessionCount)
	})
	if err != nil {
		return AvgTokensPerSession{}, err
	}
	return out, nil
}

// tokenByDimensionFrom sums input/output tokens grouped by an allowlisted column
// (col is a pinned column constant, never user input), ordered by total tokens desc.
// The key is COALESCE'd to ” so a NULL dimension value scans into the empty-key group.
func (s *Service) tokenByDimensionFrom(ctx context.Context, paths []string, col string, f Filters) ([]TokenByDimensionRow, error) {
	where, args := f.where()
	query := fmt.Sprintf(
		`SELECT
			COALESCE(CAST(%s AS VARCHAR), '') AS key,
			CAST(COALESCE(SUM(%s), 0) AS BIGINT) AS in_tokens,
			CAST(COALESCE(SUM(%s), 0) AS BIGINT) AS out_tokens
		FROM %s %s
		GROUP BY key
		ORDER BY (in_tokens + out_tokens) DESC, key ASC`,
		col, colInputTokens, colOutputTokens,
		fromClause(paths), where,
	)

	var out []TokenByDimensionRow
	err := s.queryRows(ctx, query, args, func(rows *sql.Rows) error {
		var r TokenByDimensionRow
		if err := rows.Scan(&r.Key, &r.InputTokens, &r.OutputTokens); err != nil {
			return err
		}
		out = append(out, r)
		return nil
	})
	return out, err
}

// cacheEfficiencyFrom sums the cache-read/input totals + the 1h/5m ephemeral split and
// derives the read ratio in Go (guarding divide-by-zero).
func (s *Service) cacheEfficiencyFrom(ctx context.Context, paths []string, f Filters) (CacheEfficiency, error) {
	where, args := f.where()
	query := fmt.Sprintf(
		`SELECT
			CAST(COALESCE(SUM(%s), 0) AS BIGINT),
			CAST(COALESCE(SUM(%s), 0) AS BIGINT),
			CAST(COALESCE(SUM(%s), 0) AS BIGINT),
			CAST(COALESCE(SUM(%s), 0) AS BIGINT)
		FROM %s %s`,
		colCacheReadTokens, colInputTokens, colCacheCreation1h, colCacheCreation5m,
		fromClause(paths), where,
	)

	var out CacheEfficiency
	err := s.queryRow(ctx, query, args, func(row *sql.Row) error {
		return row.Scan(&out.CacheReadTokens, &out.InputTokens,
			&out.CacheCreation1hTokens, &out.CacheCreation5mTokens)
	})
	if err != nil {
		return CacheEfficiency{}, err
	}
	if out.InputTokens > 0 {
		out.CacheReadRatio = float64(out.CacheReadTokens) / float64(out.InputTokens)
	}
	return out, nil
}

// wasteSummaryFrom counts the waste signals + the max-token truncation cost. The
// max_tokens count and its output-token / duration cost are the truncation-rerun
// signal; api-error + interrupted are the discard signals; the 1h/5m sums surface
// ephemeral cache spend.
func (s *Service) wasteSummaryFrom(ctx context.Context, paths []string, f Filters) (WasteSummary, error) {
	where, args := f.where()
	// stopReasonMaxTokens is a fixed constant, not user input, so it is inlined as a
	// quoted literal — the only bound `?` params are the where() filters, keeping the
	// positional binding unambiguous.
	maxTok := quoteLiteral(stopReasonMaxTokens)
	query := fmt.Sprintf(
		`SELECT
			CAST(COALESCE(SUM(%s), 0) AS BIGINT),
			CAST(COALESCE(SUM(%s), 0) AS BIGINT),
			COUNT(*) FILTER (WHERE %s),
			COUNT(*) FILTER (WHERE %s),
			COUNT(*) FILTER (WHERE %s = %s),
			CAST(COALESCE(SUM(CASE WHEN %s = %s THEN %s ELSE 0 END), 0) AS BIGINT),
			CAST(COALESCE(SUM(CASE WHEN %s = %s THEN %s ELSE 0 END), 0) AS BIGINT)
		FROM %s %s`,
		colCacheCreation1h, colCacheCreation5m,
		colIsAPIError, colInterrupted,
		colStopReason, maxTok,
		colStopReason, maxTok, colOutputTokens,
		colStopReason, maxTok, colDurationMs,
		fromClause(paths), where,
	)

	var out WasteSummary
	err := s.queryRow(ctx, query, args, func(row *sql.Row) error {
		return row.Scan(&out.CacheCreation1hTokens, &out.CacheCreation5mTokens,
			&out.APIErrorCount, &out.InterruptedCount, &out.MaxTokensTruncationCount,
			&out.MaxTokensOutputTokens, &out.MaxTokensDurationMs)
	})
	if err != nil {
		return WasteSummary{}, err
	}
	return out, nil
}
