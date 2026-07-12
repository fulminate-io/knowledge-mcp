// SPDX-License-Identifier: Apache-2.0

package transcriptanalytics

import (
	"fmt"
	"strings"
	"time"
)

// =============================================================================
// Transcript parquet columns. These physical names are authored by the transcript
// parser's parquet writer and read HERE by name via read_parquet. This re-authored
// list is the analyzer's half of the column contract (the query engine has no Go row
// DTO). Re-authored, NOT shared with the parser package.
// =============================================================================
const (
	colSessionID           = "session_id"
	colRecordTS            = "record_ts"
	colModel               = "model"
	colInputTokens         = "input_tokens"
	colOutputTokens        = "output_tokens"
	colCacheReadTokens     = "cache_read_tokens"
	colCacheCreationTokens = "cache_creation_tokens"
	colToolName            = "tool_name"
	colDurationMs          = "duration_ms"
	colSubagentType        = "subagent_type"
	colIsSidechain         = "is_sidechain"
	colAgentID             = "agent_id"
	colToolInputHash       = "tool_input_hash"
	colToolInputPreview    = "tool_input_preview"
	colCacheCreation1h     = "cache_creation_1h_tokens"
	colCacheCreation5m     = "cache_creation_5m_tokens"
	colStopReason          = "stop_reason"
	colIsAPIError          = "is_api_error"
	colIsMeta              = "is_meta"
	colInterrupted         = "interrupted"
)

// syntheticModel is the sentinel the parser leaves on rows it could not resolve to a
// real model. It is ALWAYS excluded so the marker never pollutes a total.
const syntheticModel = "<synthetic>"

// stopReasonMaxTokens is the turn-termination reason for an output that hit the model
// token ceiling — a truncation that typically forces a rerun, so the analyzer counts
// it as waste.
const stopReasonMaxTokens = "max_tokens"

// idleGuardCeilingMs is the last-resort idle guard for tool-execution time: a span
// longer than 2h is assumed to straddle a paused/resumed session and is EXCLUDED from
// the trustworthy-time metrics — it is NOT a cap on legitimate long runs.
//
// LOCKSTEP: transcriptsync.rollupIdleGuardCeilingMs is a hand-mirror of this value (the
// upload-time rollup applies the identical idle guard client-side). The two consts MUST
// move together — bump one, bump the other.
const idleGuardCeilingMs = 2 * 60 * 60 * 1000 // 7,200,000 ms (2h)

// duplicateCommandsLimit bounds the duplicate-command family to the top-N groups by
// (de-idled) wasted time, so the payload the synthesis + renderer consume stays bounded.
const duplicateCommandsLimit = 100

// Per-query resource bounds already live in service.go (queryThreads/queryMemoryLimit).

// idleGuardExpr returns the SQL boolean predicate that keeps only trustworthy
// tool-execution durations: NOT interrupted AND a duration in (0, ceiling]. It is a
// STRUCTURAL discriminator — an interrupted or >ceiling row is DROPPED, never clipped
// — so a genuine long run keeps its full value. The COALESCE guards mirror the
// union_by_name NULL contract so a pre-enrichment file lacking the column reads NULL
// (excluded), not a crash.
func idleGuardExpr() string {
	return fmt.Sprintf("NOT COALESCE(%s, false) AND COALESCE(%s, 0) BETWEEN 1 AND %d",
		colInterrupted, colDurationMs, idleGuardCeilingMs)
}

// Filters are the parameterized base filters every detector applies uniformly. Every
// value is bound as a SQL `?` parameter; a zero value means "no filter on this
// dimension". The synthetic-model marker AND is_meta rows are ALWAYS excluded (the
// analyzer's baseline): is_meta is a distinct boolean not covered by the synthetic
// sentinel, and the ticket requires dropping both from every total. is_sidechain is
// NEVER filtered in the baseline — doing so would zero out the subagent detectors,
// which scope themselves via agent_id <> ” (only sidechain rows carry an agent_id).
type Filters struct {
	Since   time.Time // record_ts >= Since (zero = unbounded)
	Until   time.Time // record_ts <  Until  (zero = unbounded)
	Model   string    // model = ?
	Tool    string    // tool_name = ?
	Project string    // project = ?
}

// where builds the shared WHERE clause and its bound args. The synthetic marker and
// is_meta rows are excluded unconditionally so no total (daemon or cloud) counts them.
func (f Filters) where() (string, []any) {
	clauses := []string{colModel + " <> ?", "NOT " + colIsMeta}
	args := []any{syntheticModel}

	if !f.Since.IsZero() {
		clauses = append(clauses, "CAST("+colRecordTS+" AS TIMESTAMP) >= ?")
		args = append(args, f.Since)
	}
	if !f.Until.IsZero() {
		clauses = append(clauses, "CAST("+colRecordTS+" AS TIMESTAMP) < ?")
		args = append(args, f.Until)
	}
	if f.Model != "" {
		clauses = append(clauses, colModel+" = ?")
		args = append(args, f.Model)
	}
	if f.Tool != "" {
		clauses = append(clauses, colToolName+" = ?")
		args = append(args, f.Tool)
	}
	if f.Project != "" {
		clauses = append(clauses, "project = ?")
		args = append(args, f.Project)
	}
	return "WHERE " + strings.Join(clauses, " AND "), args
}
