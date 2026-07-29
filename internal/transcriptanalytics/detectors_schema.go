// SPDX-License-Identifier: Apache-2.0

package transcriptanalytics

import "time"

// This file holds the detector baseline constants + the parameterized Filters. The SQL
// column constants, the idle-guard SQL expression, and the where()-builder were removed
// with the former DuckDB engine; the pure-Go engine reads the transcripts.Row fields
// directly and applies the baseline via Filters.keep (corpus.go) + the trustworthy /
// durationBucket primitives.

// syntheticModel is the sentinel the parser leaves on rows it could not resolve to a real
// model. It is ALWAYS excluded so the marker never pollutes a total.
const syntheticModel = "<synthetic>"

// stopReasonMaxTokens is the turn-termination reason for an output that hit the model token
// ceiling — a truncation that typically forces a rerun, so the analyzer counts it as waste.
const stopReasonMaxTokens = "max_tokens"

// idleGuardCeilingMs is the last-resort idle guard for tool-execution time: a span longer
// than 2h is assumed to straddle a paused/resumed session and is EXCLUDED from the
// trustworthy-time metrics (via trustworthy(), corpus.go) — it is NOT a cap on legitimate
// long runs.
//
// LOCKSTEP: transcriptsync.rollupIdleGuardCeilingMs is a hand-mirror of this value (the
// upload-time rollup applies the identical idle guard client-side). The two consts MUST
// move together — bump one, bump the other.
const idleGuardCeilingMs = 2 * 60 * 60 * 1000 // 7,200,000 ms (2h)

// duplicateCommandsLimit bounds the duplicate-command family to the top-N groups by
// (de-idled) wasted time, so the payload the synthesis + renderer consume stays bounded.
const duplicateCommandsLimit = 100

// Filters are the parameterized base filters the analyzer applies uniformly at corpus
// intake (Filters.keep, corpus.go). A zero value means "no filter on this dimension". The
// synthetic-model marker AND genuine is_meta==true rows are ALWAYS excluded (the analyzer's
// baseline); a MISSING/false is_meta is KEPT (the CEO is_meta fix — the OPPOSITE of the old
// duckdb NOT-NULL exclusion). is_sidechain is NEVER filtered in the baseline — doing so
// would zero out the subagent detectors, which scope themselves via agent_id <> "" (only
// sidechain rows carry an agent_id).
type Filters struct {
	Since   time.Time // record_ts >= Since (zero = unbounded)
	Until   time.Time // record_ts <  Until  (zero = unbounded)
	Model   string    // model = Model
	Tool    string    // tool_name = Tool
	Project string    // project = Project
}
