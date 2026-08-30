// SPDX-License-Identifier: Apache-2.0

package transcriptanalytics

import "sort"

// This file holds the FLOW detectors — where work is redundant (duplicate commands) and
// where orchestration fans out (subagent wall-time + agent-chain over-orchestration) — as
// pure-Go folds over the loaded *corpus.

// The three waste verdicts. Exactly one is set on every emitted duplicate row.
const (
	// wasteVerdictPlausible means the per-call waste sits within the tool's own p99.9, so
	// the time is consistent with the tool actually having run.
	wasteVerdictPlausible = "plausible"
	// wasteVerdictImplausible means the per-call waste EXCEEDS the tool's p99.9 by so much
	// that the span is unlikely to be execution time at all — most often a human approval
	// wait counted between tool_use and tool_result.
	wasteVerdictImplausible = "implausible"
	// wasteVerdictUndetermined means the tool has fewer than minSamplesForP999 trustworthy
	// samples, so no bound could be established. It does NOT mean the row is fine.
	wasteVerdictUndetermined = "undetermined"
)

// DuplicateCommandRow is one redundantly-rerun command: the SAME tool + input fingerprint
// executed more than once WITHIN a single session.
//
// It carries wasted TIME only. Wasted-TOKENS per duplicate is a conscious out-of-scope
// deferral: a tool-call row carries ZERO tokens (the parser splits a turn into a zero-tool
// token row plus zero-token tool_use rows), so wasted-tokens at tool granularity is
// ill-defined and needs an unsolved tool→token attribution — a separate follow-up.
//
// WHAT THE TIME ACTUALLY MEASURES, and why the verdict exists. The duration behind
// WastedDurationMs is tool_use to tool_result, and for a permission-gated call that span
// contains the whole human approval wait. Measured on the real corpus: 16 reruns of one
// gated edit summed to 49,898,496ms of "waste", against Edit's p99.9 of ~14,883ms. The
// pairing is not wrong — the span is faithfully measured — so the row is FLAGGED rather
// than dropped, and WasteVerdict is how a reader tells a genuine rerun cost from a blocked
// one. Note that undetermined means the tool has too few samples for a p99.9 to mean
// anything, NOT that the row has been cleared.
type DuplicateCommandRow struct {
	SessionID        string `json:"session_id"`
	ToolName         string `json:"tool_name"`
	ToolInputHash    string `json:"tool_input_hash"`
	RunCount         int64  `json:"run_count"`
	WastedDurationMs int64  `json:"wasted_duration_ms"`
	PerCallWasteMs   int64  `json:"per_call_waste_ms"`
	ToolP999Ms       int64  `json:"tool_p999_ms"`
	WasteVerdict     string `json:"waste_verdict"`
	// BackgroundRuns counts this group's runs that asked to be backgrounded. It is a COUNT,
	// not a bool, because a group can mix foreground and background runs of the same command
	// and a bool would have to pick one and lie about the rest.
	//
	// IT IS DISCLOSURE, NOT AN EXEMPTION, and the measurements say so: backgrounded Bash
	// calls run p50 36ms / p90 62ms / p99 2,979ms across 2,046 calls, against foreground Bash
	// at p50 117ms / p90 12,887ms / p99 287,077ms. A backgrounded call returns a handle
	// immediately, so it is FASTER — which makes a long background duration MORE anomalous,
	// not less. There is no basis for exempting a background row from the p99.9 bound, and
	// none is applied.
	BackgroundRuns int64  `json:"background_runs"`
	SamplePreview  string `json:"sample_preview"`
}

// SubagentWallTime is one subagent's elapsed time + token cost, reported as TWO measures
// because a lane's elapsed time and its working time are different quantities and only one
// of them is what a reader means by "how long did this agent take".
//
// SpanMs is the floor-epoch span of MAX−MIN record_ts over the agent's rows (a QUERY-side
// aggregate, NOT a stored per-row scalar). It INCLUDES idle time, so an agent resumed after
// a pause reads as having worked across the whole pause.
//
// ActiveMs sums only the inter-event gaps below subagentIdleGapMs, so it EXCLUDES those
// pauses. It is the ranking key for this family. It is a lower bound on work — a long
// single operation that emits no event during it falls into one gap and is excluded — while
// SpanMs is the upper bound, and the pair brackets the truth.
type SubagentWallTime struct {
	AgentID      string `json:"agent_id"`
	SubagentType string `json:"subagent_type"`
	SpanMs       int64  `json:"span_ms"`
	ActiveMs     int64  `json:"active_ms"`
	InputTokens  int64  `json:"input_tokens"`
	OutputTokens int64  `json:"output_tokens"`
}

// AgentChainRow is one MAIN session's over-orchestration proxy: how many distinct subagents
// it spawned, how diverse their types were, and their combined + longest wall-time. It is a
// PROXY, not a true recursive spawn-tree depth: the transcript has no agent-spawn-parent
// column (parent_uuid is message-level), so true recursive spawn-depth is a separate
// schema-enrichment follow-up, deliberately not built here.
//
// The span and active totals carry the same meanings they do on SubagentWallTime: span
// includes each agent's idle time, active excludes it.
type AgentChainRow struct {
	SessionID             string `json:"session_id"`
	SubagentCount         int64  `json:"subagent_count"`
	SubagentTypeDiversity int64  `json:"subagent_type_diversity"`
	TotalSubagentSpanMs   int64  `json:"total_subagent_span_ms"`
	MaxSubagentSpanMs     int64  `json:"max_subagent_span_ms"`
	TotalSubagentActiveMs int64  `json:"total_subagent_active_ms"`
	MaxSubagentActiveMs   int64  `json:"max_subagent_active_ms"`
}

// duplicateCommands emits the (session,tool,hash) groups rerun more than once — mirroring
// the agent's QueryDuplicateCommands (rollup_query.go:287): RunCount over all rows in the
// grain, WastedDurationMs summing trustworthy rows only (de-idled), MIN(preview), HAVING
// run_count>1, ordered by wasted desc → run_count desc → session asc, bounded to the top-N.
// Each row additionally carries the per-trustworthy-run waste, the tool's own p99.9 bound
// and the verdict comparing them, and IMPLAUSIBLE rows sort below every other row so a
// human wait cannot head the ranking.
//
// It returns the PRE-CAP group total alongside the bounded rows so the report's truncation
// disclosure states a number this fold measured, rather than a second count derived
// elsewhere over the same map — two measurements of one quantity drift.
func (c *corpus) duplicateCommands() ([]DuplicateCommandRow, int64) {
	out := make([]DuplicateCommandRow, 0, len(c.dupes))
	for k, d := range c.dupes {
		if d.count <= 1 { // HAVING COUNT(*) > 1
			continue
		}
		row := DuplicateCommandRow{
			SessionID: k.session, ToolName: k.tool, ToolInputHash: k.hash,
			RunCount: d.count, WastedDurationMs: d.wastedSum, SamplePreview: d.preview,
			BackgroundRuns: d.backgroundCount,
		}
		row.PerCallWasteMs, row.ToolP999Ms, row.WasteVerdict = c.wasteVerdict(k.tool, d)
		out = append(out, row)
	}
	sort.Slice(out, func(i, j int) bool {
		li, lj := out[i].WasteVerdict == wasteVerdictImplausible, out[j].WasteVerdict == wasteVerdictImplausible
		if li != lj {
			return lj // an implausible row sorts below one that is not.
		}
		if out[i].WastedDurationMs != out[j].WastedDurationMs {
			return out[i].WastedDurationMs > out[j].WastedDurationMs
		}
		if out[i].RunCount != out[j].RunCount {
			return out[i].RunCount > out[j].RunCount
		}
		return out[i].SessionID < out[j].SessionID
	})
	total := int64(len(out))
	if len(out) > duplicateCommandsLimit {
		out = out[:duplicateCommandsLimit]
	}
	return out, total
}

// wasteVerdict prices one duplicate group's per-call waste against its tool's own latency
// distribution, returning (perCallWasteMs, toolP999Ms, verdict).
//
// The denominator is the group's TRUSTWORTHY run count, not its run count: wastedSum was
// summed over trustworthy rows only, so dividing by every run would dilute the figure by
// runs that contributed no time.
//
// The bound is the tool's p99.9 rather than its max, because the histogram it is computed
// from is itself built over trustworthy rows and therefore CONTAINS the contaminating
// human-wait spans. For a tool that clears the sample floor those spans are a vanishing
// tail — 6 of 27,803 Edit calls — so the p99.9 is unmoved by them while a max would be
// defined by them. Below the floor no bound is claimed at all.
func (c *corpus) wasteVerdict(tool string, d *dupAcc) (perCall, p999 int64, verdict string) {
	if d.trustCount > 0 {
		perCall = d.wastedSum / d.trustCount
	}
	// The per-call figure is still reported when no bound can be established; only the
	// bound itself is withheld, because a zero p999 would read as a real ceiling of zero.
	if d.trustCount == 0 || c.latencyTotal[tool] < minSamplesForP999 {
		return perCall, 0, wasteVerdictUndetermined
	}
	p999 = histPercentile(c.latencyHist[tool], c.latencyTotal[tool], 0.999)
	if perCall > p999 {
		return perCall, p999, wasteVerdictImplausible
	}
	return perCall, p999, wasteVerdictPlausible
}

// subagentWallTime returns per-subagent elapsed time + token cost — mirroring the agent's
// QuerySubagentWallTime (rollup_query.go:257): MIN(subagent_type), the floor-epoch MAX−MIN
// record_ts span, SUM tokens. Only sidechain rows with an agent_id populate c.subagents.
//
// It ranks by ACTIVE time, then span, then agent asc. Ranking by span put a lane that sat
// idle for two days above one that worked for an hour, which is the reading the ticket was
// filed over. The top-N cap is then applied to that ranking, the same shape and constant
// idiom duplicateCommands uses, and the PRE-CAP total is returned beside the rows so the
// report's truncation disclosure quotes this fold's own count rather than recomputing it.
func (c *corpus) subagentWallTime() ([]SubagentWallTime, int64) {
	out := make([]SubagentWallTime, 0, len(c.subagents))
	for id, sa := range c.subagents {
		out = append(out, SubagentWallTime{
			AgentID:      id,
			SubagentType: sa.subagentType,
			SpanMs:       wallMs(sa.minTS, sa.maxTS),
			ActiveMs:     sa.activeMs,
			InputTokens:  sa.inSum,
			OutputTokens: sa.outSum,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ActiveMs != out[j].ActiveMs {
			return out[i].ActiveMs > out[j].ActiveMs
		}
		if out[i].SpanMs != out[j].SpanMs {
			return out[i].SpanMs > out[j].SpanMs
		}
		return out[i].AgentID < out[j].AgentID
	})
	total := int64(len(out))
	if len(out) > subagentWallTimeLimit {
		out = out[:subagentWallTimeLimit]
	}
	return out, total
}

// agentChains returns the per-MAIN-session over-orchestration proxy — mirroring the agent's
// agentChainPG (rollup_insights.go:149): the inner per-(session,agent) wall span, then per
// session the subagent count, the count of DISTINCT (per-agent MIN) subagent types, and the
// total + max subagent span AND active time; ordered by count desc → total-span desc →
// session asc. The ordering key stays SPAN deliberately: this fold corrects the family's
// labels and adds the missing measure, it does not re-rank the family.
func (c *corpus) agentChains() []AgentChainRow {
	out := make([]AgentChainRow, 0, len(c.chains))
	for sess, agents := range c.chains {
		types := make(map[string]struct{}, len(agents))
		var totalSpan, maxSpan, totalActive, maxActive int64
		for _, ca := range agents {
			w := wallMs(ca.minTS, ca.maxTS)
			totalSpan += w
			if w > maxSpan {
				maxSpan = w
			}
			totalActive += ca.activeMs
			if ca.activeMs > maxActive {
				maxActive = ca.activeMs
			}
			types[ca.subagentType] = struct{}{}
		}
		out = append(out, AgentChainRow{
			SessionID:             sess,
			SubagentCount:         int64(len(agents)),
			SubagentTypeDiversity: int64(len(types)),
			TotalSubagentSpanMs:   totalSpan,
			MaxSubagentSpanMs:     maxSpan,
			TotalSubagentActiveMs: totalActive,
			MaxSubagentActiveMs:   maxActive,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].SubagentCount != out[j].SubagentCount {
			return out[i].SubagentCount > out[j].SubagentCount
		}
		if out[i].TotalSubagentSpanMs != out[j].TotalSubagentSpanMs {
			return out[i].TotalSubagentSpanMs > out[j].TotalSubagentSpanMs
		}
		return out[i].SessionID < out[j].SessionID
	})
	return out
}
