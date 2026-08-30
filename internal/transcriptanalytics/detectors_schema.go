// SPDX-License-Identifier: Apache-2.0

package transcriptanalytics

import (
	"fmt"
	"strings"
	"time"
)

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

// subagentIdleGapMs is the inter-event gap at or above which a subagent lane is considered
// idle rather than working, so a lane resumed after a pause reports the work and not the
// wait. A gap below it is counted as active time; a gap at or above it contributes nothing.
//
// It is NOT a cap on a long single operation: a genuine 12-minute build inside one gap is
// correctly excluded from active time, because the transcript records no event while it
// runs and the analyzer cannot tell that gap from a lane sitting idle. Active time is
// therefore a LOWER bound on work, and span_ms remains beside it as the upper one.
//
// LOCKSTEP: transcriptsync.rollupSubagentIdleGapMs is a hand-mirror of this value (the
// upload-time rollup runs the same active reduction client-side). The two consts MUST move
// together — bump one, bump the other — and a gate asserts they are numerically equal.
const subagentIdleGapMs = 10 * 60 * 1000 // 600,000 ms (10m)

// duplicateCommandsLimit bounds the duplicate-command family to the top-N groups by
// (de-idled) wasted time, so the payload the synthesis + renderer consume stays bounded.
const duplicateCommandsLimit = 100

// bytesPerResultToken converts a tool result's text bytes to tokens. It is an ESTIMATE —
// the measured bytes-per-token ratio over this corpus — not a count read from any usage
// record, because a tool result is never billed on its own and no per-result token count is
// stored anywhere. Every figure derived from it inherits that status.
const bytesPerResultToken = 3.6

// imageResultTokens is the token cost charged for one image block in a tool result. It is
// an ESTIMATE, not read from a usage record; image token cost varies with dimensions, which
// the transcript does not carry.
const imageResultTokens = 1500

// laneDetailTopWaits bounds the single-lane drill-down's longest-wait list. A lane can hold
// tens of thousands of tool calls and the question the list answers — "what did this lane
// wait on" — is answered by the top few.
const laneDetailTopWaits = 20

// minSamplesForP999 is the number of trustworthy latency samples a tool needs before its
// p99.9 has enough resolution to bound anything. Below it the duplicate-command detector
// reports the waste verdict as undetermined rather than guessing.
//
// WHAT THE FLOOR COSTS, MEASURED over the real corpus 2026-08-29: only 22 of the 124 tools
// carrying a latency histogram clear 1,000 trustworthy samples (18%), and 13 of the 100
// duplicate groups the detector reports are for tools below the floor.
//
// The pointed case is AskUserQuestion, which has 736 samples and therefore lands
// PERMANENTLY on undetermined — while carrying the corpus's six longest admitted spans
// (75.3, 75.3, 65.0, 55.9, 54.5 and 49.6 minutes of human answer latency). The bound will
// never flag it, and that is the honest answer rather than a gap: histPercentile takes the
// bucket where the cumulative count first reaches ceil(q*total), so at q=0.999 with total
// below 1,000 the target IS total — the last bucket — and the bound degenerates to the
// tool's own maximum, which no row can exceed. Lowering the floor would not surface
// AskUserQuestion; it would return a vacuous bound that blesses every row instead of
// admitting it cannot tell. Human-wait time worth measuring needs its own detector keyed on
// the tool's semantics, not a smaller threshold here.
const minSamplesForP999 = 1000

// subagentWallTimeLimit bounds the subagent family to the top-N agents by active time,
// mirroring duplicateCommandsLimit. This family emits one row per agent_id with no natural
// cardinality ceiling, which is how a report reached 881,002 bytes with this family alone
// carrying 91.8% of it — past the synthesis prompt's token limit, and spent out of the
// caller's context on every invocation. The rows it drops are disclosed as a count, never
// silently: FamilyTruncation states returned against total.
const subagentWallTimeLimit = 100

// Scope names the population a report is computed over. Every detector runs identically
// under every scope — the scope narrows ROWS at intake, it does not fork the folds — so
// numbers stay comparable across scopes by construction.
type Scope string

const (
	// ScopeAll is the whole cached corpus: the default, and the only behaviour that
	// existed before the selector.
	ScopeAll Scope = "all"
	// ScopeSessionTree is a main session PLUS every subagent lane it spawned. Requires a
	// session id.
	ScopeSessionTree Scope = "session-tree"
	// ScopeSingle is one lane — either a session's main lane or one agent's. Requires
	// exactly one of session or agent.
	ScopeSingle Scope = "single"
	// ScopeTimeRange bounds the corpus by record instant. Requires at least one of
	// since/until.
	ScopeTimeRange Scope = "time-range"
)

// ScopeValues returns the accepted scope vocabulary. It is the SINGLE declaration the
// validation error and the tool schema's enum both read from — two hand-kept lists drift,
// and a schema that advertises a value the validator rejects is worse than either alone.
func ScopeValues() []string {
	return []string{string(ScopeAll), string(ScopeSessionTree), string(ScopeSingle), string(ScopeTimeRange)}
}

// Filters are the parameterized base filters the analyzer applies uniformly at corpus
// intake (Filters.keep, corpus.go). A zero value means "no filter on this dimension". The
// synthetic-model marker AND genuine is_meta==true rows are ALWAYS excluded (the analyzer's
// baseline); a MISSING/false is_meta is KEPT (the CEO is_meta fix — the OPPOSITE of the old
// duckdb NOT-NULL exclusion). is_sidechain is NEVER filtered in the baseline — doing so
// would zero out the subagent detectors, which select themselves via agent_id <> "" (only
// sidechain rows carry an agent_id).
//
// This one type is the whole parameterization spine: the scope selector is three more
// fields on it rather than a second filter type or a per-scope corpus loader, which is what
// makes "one implementation, parameterized" checkable rather than aspirational.
type Filters struct {
	Since   time.Time // record_ts >= Since (zero = unbounded)
	Until   time.Time // record_ts <  Until  (zero = unbounded)
	Model   string    // model = Model
	Tool    string    // tool_name = Tool
	Project string    // project = Project
	Scope   Scope     // the population selector; empty is ScopeAll
	// SessionID and AgentID are the scope's selectors. Which of them a scope consumes is
	// part of that scope's contract, and supplying one a scope does NOT consume is an
	// error rather than a value quietly ignored.
	SessionID string
	AgentID   string
}

// resolved returns the scope with the empty zero value read as ScopeAll, so callers that
// never set one get today's behaviour.
func (f Filters) resolved() Scope {
	if f.Scope == "" {
		return ScopeAll
	}
	return f.Scope
}

// Selector renders what narrowed this report — the session id, the agent id, or the time
// range — for the provenance block. ScopeAll narrows nothing and renders empty. A
// before/after comparison of two time-range runs is unreadable without this.
func (f Filters) Selector() string {
	switch f.resolved() {
	case ScopeSessionTree:
		return f.SessionID
	case ScopeSingle:
		if f.AgentID != "" {
			return f.AgentID
		}
		return f.SessionID
	case ScopeTimeRange:
		parts := make([]string, 0, 2)
		if !f.Since.IsZero() {
			parts = append(parts, "since="+f.Since.Format(time.RFC3339))
		}
		if !f.Until.IsZero() {
			parts = append(parts, "until="+f.Until.Format(time.RFC3339))
		}
		return strings.Join(parts, " ")
	case ScopeAll:
		return ""
	default:
		return ""
	}
}

// Validate rejects every scope-and-selector combination that does not describe a population,
// naming the accepted vocabulary when the scope itself is the problem.
//
// It NEVER coerces. A scope that silently widened to the whole corpus would return a
// perfectly plausible report about the wrong population, and the caller would have no way to
// tell — which is the failure this exists to prevent, not a convenience it withholds.
func (f Filters) Validate() error {
	switch f.resolved() {
	case ScopeAll:
		return f.rejectUnconsumed("all", f.SessionID != "" || f.AgentID != "" || !f.Since.IsZero() || !f.Until.IsZero(),
			"session, agent, since and until")
	case ScopeSessionTree:
		if f.SessionID == "" {
			return fmt.Errorf("transcriptanalytics: scope %q requires a session id", ScopeSessionTree)
		}
		return f.rejectUnconsumed("session-tree", f.AgentID != "" || !f.Since.IsZero() || !f.Until.IsZero(), "agent, since and until")
	case ScopeSingle:
		if f.SessionID == "" && f.AgentID == "" {
			return fmt.Errorf("transcriptanalytics: scope %q requires exactly one of session or agent, and neither was given", ScopeSingle)
		}
		if f.SessionID != "" && f.AgentID != "" {
			return fmt.Errorf("transcriptanalytics: scope %q requires exactly one of session or agent, and both were given", ScopeSingle)
		}
		return f.rejectUnconsumed("single", !f.Since.IsZero() || !f.Until.IsZero(), "since and until")
	case ScopeTimeRange:
		if f.Since.IsZero() && f.Until.IsZero() {
			return fmt.Errorf("transcriptanalytics: scope %q requires at least one of since or until", ScopeTimeRange)
		}
		return f.rejectUnconsumed("time-range", f.SessionID != "" || f.AgentID != "", "session and agent")
	default:
		return fmt.Errorf("transcriptanalytics: unknown scope %q; accepted values are %s",
			f.Scope, strings.Join(ScopeValues(), ", "))
	}
}

// rejectUnconsumed reports a selector supplied under a scope that does not read it. A
// silently-ignored selector reads to the caller as a filter that was applied.
func (f Filters) rejectUnconsumed(scope string, supplied bool, names string) error {
	if !supplied {
		return nil
	}
	return fmt.Errorf("transcriptanalytics: scope %q does not accept %s; accepted scope values are %s",
		scope, names, strings.Join(ScopeValues(), ", "))
}
