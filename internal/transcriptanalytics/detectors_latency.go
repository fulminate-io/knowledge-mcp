// SPDX-License-Identifier: Apache-2.0

package transcriptanalytics

import (
	"context"
	"database/sql"
	"fmt"
)

// This file holds the per-tool LATENCY-DISTRIBUTION detector (p50/p90/p99) and the
// per-tool TIME-TOTAL detector — the "where does wall-time go" pair. Both reuse the
// idle guard so a genuine long run keeps its full weight while an idle-straddling row
// is excluded.

// ToolLatencyRow is one tool's trustworthy-execution-time distribution: the call
// count plus p50/p90/p99 (milliseconds), over the idle-guarded rows.
type ToolLatencyRow struct {
	ToolName string `json:"tool_name"`
	Count    int64  `json:"count"`
	P50      int64  `json:"p50"`
	P90      int64  `json:"p90"`
	P99      int64  `json:"p99"`
}

// ToolTimeTotalRow is one tool's TOTAL trustworthy wall-time — the direct
// "which tools consume the most time" ranking the synthesis stage reasons over.
type ToolTimeTotalRow struct {
	ToolName        string `json:"tool_name"`
	TotalDurationMs int64  `json:"total_duration_ms"`
	CallCount       int64  `json:"call_count"`
}

// toolLatencyFrom computes the per-tool p50/p90/p99 latency distribution over an
// explicit set of local parquet paths. quantile_cont returns DOUBLE, so each
// percentile is CAST to BIGINT for the int64 Scan. The idle guard excludes interrupted
// and >2h-ceiling rows STRUCTURALLY (never a magnitude cap) before the percentile is
// taken; tool_name <> ” drops token rows. Ordered by p90 desc.
func (s *Service) toolLatencyFrom(ctx context.Context, paths []string, f Filters) ([]ToolLatencyRow, error) {
	where, args := f.where()
	query := fmt.Sprintf(
		`SELECT
			%s,
			COUNT(*) AS call_count,
			CAST(quantile_cont(%s, 0.5) AS BIGINT) AS p50,
			CAST(quantile_cont(%s, 0.9) AS BIGINT) AS p90,
			CAST(quantile_cont(%s, 0.99) AS BIGINT) AS p99
		FROM %s %s AND %s <> '' AND (%s)
		GROUP BY %s
		ORDER BY p90 DESC, %s ASC`,
		colToolName,
		colDurationMs, colDurationMs, colDurationMs,
		fromClause(paths), where, colToolName, idleGuardExpr(),
		colToolName,
		colToolName,
	)

	var out []ToolLatencyRow
	err := s.queryRows(ctx, query, args, func(rows *sql.Rows) error {
		var r ToolLatencyRow
		if err := rows.Scan(&r.ToolName, &r.Count, &r.P50, &r.P90, &r.P99); err != nil {
			return err
		}
		out = append(out, r)
		return nil
	})
	return out, err
}

// toolTimeTotalFrom sums the trustworthy per-tool wall-time (de-idled via the CASE-WHEN
// idle guard) grouped by tool, ordered by total time desc. tool_name <> ” drops token
// rows. This is the direct per-tool time TOTAL, the counterpart to the latency
// distribution.
func (s *Service) toolTimeTotalFrom(ctx context.Context, paths []string, f Filters) ([]ToolTimeTotalRow, error) {
	where, args := f.where()
	query := fmt.Sprintf(
		`SELECT
			%s,
			CAST(COALESCE(SUM(CASE WHEN %s THEN %s ELSE 0 END), 0) AS BIGINT) AS total_ms,
			COUNT(*) AS call_count
		FROM %s %s AND %s <> ''
		GROUP BY %s
		ORDER BY total_ms DESC, %s ASC`,
		colToolName,
		idleGuardExpr(), colDurationMs,
		fromClause(paths), where, colToolName,
		colToolName,
		colToolName,
	)

	var out []ToolTimeTotalRow
	err := s.queryRows(ctx, query, args, func(rows *sql.Rows) error {
		var r ToolTimeTotalRow
		if err := rows.Scan(&r.ToolName, &r.TotalDurationMs, &r.CallCount); err != nil {
			return err
		}
		out = append(out, r)
		return nil
	})
	return out, err
}
