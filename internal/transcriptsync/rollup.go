// SPDX-License-Identifier: Apache-2.0

// rollup.go — the pure-Go, in-memory per-session usage-rollup compute. It runs INSIDE
// prepareFile (run.go) over the []transcripts.Row already resident in RAM at conversion
// time and produces the unexported wire DTOs (wire.go) that ride on the confirm-batch
// POST. It is genuinely-new client code: the daemon-local transcriptanalytics engine is
// a DuckDB corpus-wide query engine with the OPPOSITE baseline (it ALWAYS excludes
// synthetic-model + is_meta rows, whereas this rollup ships them verbatim for the sync
// backend to filter at read time) and its global DuckDB thread/memory settings would
// serialize the NumCPU-parallel producer pipeline — so a single O(n) map pass here is
// both correct AND sub-ms for a median ~200-record session. It imports NEITHER
// database/sql, the duckdb driver, NOR transcriptanalytics; the only thing borrowed is
// the idle-guard SEMANTICS (mirrored below) and the transcripts.Row field contract.

package transcriptsync

import (
	"math/bits"
	"sort"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/transcripts"
)

// rollupSlowCallsPerTool bounds the slow-call family to the top-N slowest trustworthy
// calls PER (session, tool_name) — a per-tool drill-down, NOT a cross-tool top-N that
// would starve a second heavy tool. Hand-mirrored from the sync backend's per-tool
// LIMIT.
const rollupSlowCallsPerTool = 100

// rollupTrustworthy reports whether a row's DurationMs is a trustworthy tool-execution
// span: NOT interrupted AND 1 <= DurationMs <= rollupIdleGuardCeilingMs. It mirrors the
// sync backend's idle-guard predicate (and the daemon-local idleGuardExpr): a span >2h is
// assumed to straddle a paused/resumed session and is EXCLUDED from trustworthy-time
// metrics — it is NOT clipped. A non-trustworthy row still COUNTS toward record_count /
// run_count / interrupted_count but contributes 0ms to trustworthy_duration_ms +
// wasted_duration_ms and is excluded from latency_hist + slow_calls.
func rollupTrustworthy(r transcripts.Row) bool {
	return !r.Interrupted && r.DurationMs >= 1 && r.DurationMs <= rollupIdleGuardCeilingMs
}

// durationBucket maps a duration in ms to its sparse log2 histogram bucket: 0 for ms<=0,
// else min(floor(log2(ms)), rollupBucketMaxExp). floor(log2(ms)) == bits.Len64(ms)-1
// (no per-call alloc). Bucket b covers [2^b, 2^(b+1)) ms. Hand-mirrored from the sync
// backend's bucket scheme. Boundaries: ms=1→0, ms=2→1, ms=3→1, huge→clamped 31.
func durationBucket(ms int64) int {
	if ms <= 0 {
		return 0
	}
	b := bits.Len64(uint64(ms)) - 1
	if b > rollupBucketMaxExp {
		return rollupBucketMaxExp
	}
	return b
}

// factKey is the (day × full 12-dimension tuple) grain the fact rows aggregate on. All
// fields are comparable so the struct is a valid map key.
type factKey struct {
	day, model, toolName, project, subagentType, agentID string
	isSidechain, isMeta                                  bool
	mcpServer, mcpTool, skill, serviceTier, stopReason   string
}

// histKey is the sparse latency-histogram grain (is_meta joins it per the frozen
// contract).
type histKey struct {
	day, toolName, model, project string
	isSidechain, isMeta           bool
	bucket                        int
}

// dupKey is the FINE-grain duplicate-command key: the full tuple any sync-backend filter
// can touch, so the backend can filter these columns row-wise before re-aggregating up to
// (session, tool, hash).
type dupKey struct {
	day, toolName, toolInputHash, model, project string
	isSidechain, isMeta                          bool
}

// dupSessionKey is the (tool_name, tool_input_hash) session-total key that gates emission:
// a fine row ships only when its parent session-total run_count > 1.
type dupSessionKey struct {
	toolName, toolInputHash string
}

// factAcc accumulates one fact grain plus the min/max record instants tracked as
// time.Time (compared as INSTANTS, then formatted once at the end — never string-compared,
// which would misorder across differing UTC offsets).
type factAcc struct {
	row          factRow
	minTS, maxTS time.Time
}

// dupAcc accumulates one fine duplicate grain. firstTS is the MIN record instant
// (time.Time instant comparison); row.SamplePreview is the MIN preview under plain
// byte-wise Go string comparison.
type dupAcc struct {
	row     duplicateRow
	firstTS time.Time
}

// rollupAgg is the single-pass accumulator behind computeSessionRollup. It holds the
// session scalar accumulator plus one map per row-kind grain; each add* method folds one
// row into the relevant accumulators, and finish() materializes the wire slices. Splitting
// the pass across small methods keeps each concern independently readable (and each below
// the per-function complexity budget) while staying a single O(n) walk.
type rollupAgg struct {
	sess             sessionScalars
	firstTS, lastTS  time.Time
	tsInit           bool
	chainAgents      map[string]struct{} // distinct subagent ids among sidechain rows
	factAccs         map[factKey]*factAcc
	histCounts       map[histKey]int64
	slowByTool       map[string][]slowCallRow
	dupAccs          map[dupKey]*dupAcc
	dupSessionTotals map[dupSessionKey]int64
}

func newRollupAgg() *rollupAgg {
	return &rollupAgg{
		chainAgents:      map[string]struct{}{},
		factAccs:         map[factKey]*factAcc{},
		histCounts:       map[histKey]int64{},
		slowByTool:       map[string][]slowCallRow{},
		dupAccs:          map[dupKey]*dupAcc{},
		dupSessionTotals: map[dupSessionKey]int64{},
	}
}

// add folds one row into every relevant accumulator.
func (a *rollupAgg) add(r transcripts.Row) {
	day := r.RecordTS.Format("2006-01-02") // record's OWN offset — NOT UTC-normalized.
	trust := rollupTrustworthy(r)
	a.addSession(r)
	a.addFact(r, day, trust)
	if trust && r.ToolName != "" {
		a.addTrustworthyTool(r, day) // latency hist + slow-call candidate.
	}
	if r.ToolInputHash != "" {
		a.addDuplicate(r, day, trust)
	}
}

// addSession folds a row into the per-session scalar aggregates (over ALL rows).
func (a *rollupAgg) addSession(r transcripts.Row) {
	a.sess.RecordCount++
	a.sess.InputTokens += r.InputTokens
	a.sess.OutputTokens += r.OutputTokens
	a.sess.CacheReadTokens += r.CacheReadTokens
	a.sess.CacheCreationTokens += r.CacheCreationTokens
	a.sess.CacheCreation1hTokens += r.CacheCreation1hTokens
	a.sess.CacheCreation5mTokens += r.CacheCreation5mTokens
	a.sess.DurationMs += r.DurationMs // RAW (un-idle-guarded) sum.
	a.sess.WebSearchCount += r.WebSearchCount
	a.sess.WebFetchCount += r.WebFetchCount
	if r.IsError {
		a.sess.ErrorCount++
	}
	if r.IsAPIError {
		a.sess.APIErrorCount++
	}
	if r.Interrupted {
		a.sess.InterruptedCount++
	}
	if a.sess.Project == "" && r.Project != "" {
		a.sess.Project = r.Project // first non-empty project in record order.
	}
	// first/last_record_ts: MIN/MAX over time.Time INSTANTS (not formatted strings).
	if !a.tsInit {
		a.firstTS, a.lastTS, a.tsInit = r.RecordTS, r.RecordTS, true
	} else {
		if r.RecordTS.Before(a.firstTS) {
			a.firstTS = r.RecordTS
		}
		if r.RecordTS.After(a.lastTS) {
			a.lastTS = r.RecordTS
		}
	}
	if r.IsSidechain && r.AgentID != "" {
		a.chainAgents[r.AgentID] = struct{}{}
	}
}

// addFact folds a row into its (day × full dimension tuple) fact grain. Synthetic-model and
// is_meta rows are INCLUDED verbatim.
func (a *rollupAgg) addFact(r transcripts.Row, day string, trust bool) {
	fk := factKey{
		day: day, model: r.Model, toolName: r.ToolName, project: r.Project,
		subagentType: r.SubagentType, agentID: r.AgentID, isSidechain: r.IsSidechain,
		isMeta: r.IsMeta, mcpServer: r.MCPServer, mcpTool: r.MCPTool, skill: r.Skill,
		serviceTier: r.ServiceTier, stopReason: r.StopReason,
	}
	fa := a.factAccs[fk]
	if fa == nil {
		fa = &factAcc{
			row: factRow{
				Day: day, Model: r.Model, ToolName: r.ToolName, Project: r.Project,
				SubagentType: r.SubagentType, AgentID: r.AgentID, IsSidechain: r.IsSidechain,
				IsMeta: r.IsMeta, MCPServer: r.MCPServer, MCPTool: r.MCPTool, Skill: r.Skill,
				ServiceTier: r.ServiceTier, StopReason: r.StopReason,
			},
			minTS: r.RecordTS, maxTS: r.RecordTS,
		}
		a.factAccs[fk] = fa
	}
	fa.row.RecordCount++
	fa.row.InputTokens += r.InputTokens
	fa.row.OutputTokens += r.OutputTokens
	fa.row.CacheReadTokens += r.CacheReadTokens
	fa.row.CacheCreationTokens += r.CacheCreationTokens
	fa.row.CacheCreation1hTokens += r.CacheCreation1hTokens
	fa.row.CacheCreation5mTokens += r.CacheCreation5mTokens
	fa.row.DurationMs += r.DurationMs // RAW sum over ALL rows in grain.
	if trust {
		fa.row.TrustworthyDurationMs += r.DurationMs // idle-guarded rows only.
	}
	if r.IsAPIError {
		fa.row.APIErrorCount++
	}
	if r.Interrupted {
		fa.row.InterruptedCount++
	}
	if r.IsError {
		fa.row.ErrorCount++
	}
	fa.row.WebSearchCount += r.WebSearchCount
	fa.row.WebFetchCount += r.WebFetchCount
	if r.RecordTS.Before(fa.minTS) {
		fa.minTS = r.RecordTS
	}
	if r.RecordTS.After(fa.maxTS) {
		fa.maxTS = r.RecordTS
	}
}

// addTrustworthyTool folds a trustworthy, named-tool row into the latency histogram and the
// per-tool slow-call candidate list. Caller guarantees trust && ToolName != "".
func (a *rollupAgg) addTrustworthyTool(r transcripts.Row, day string) {
	a.histCounts[histKey{
		day: day, toolName: r.ToolName, model: r.Model, project: r.Project,
		isSidechain: r.IsSidechain, isMeta: r.IsMeta, bucket: durationBucket(r.DurationMs),
	}]++
	a.slowByTool[r.ToolName] = append(a.slowByTool[r.ToolName], slowCallRow{
		Day: day, ToolName: r.ToolName, Model: r.Model, Project: r.Project,
		IsSidechain: r.IsSidechain, MCPServer: r.MCPServer, MCPTool: r.MCPTool,
		DurationMs: r.DurationMs, RecordTS: r.RecordTS.Format(time.RFC3339Nano),
		ToolInputPreview: r.ToolInputPreview,
	})
}

// addDuplicate folds a hashed row into its fine-grain dup accumulator AND the (tool,hash)
// session-total counter that gates emission. Caller guarantees ToolInputHash != "".
func (a *rollupAgg) addDuplicate(r transcripts.Row, day string, trust bool) {
	a.dupSessionTotals[dupSessionKey{toolName: r.ToolName, toolInputHash: r.ToolInputHash}]++
	dk := dupKey{
		day: day, toolName: r.ToolName, toolInputHash: r.ToolInputHash, model: r.Model,
		project: r.Project, isSidechain: r.IsSidechain, isMeta: r.IsMeta,
	}
	da := a.dupAccs[dk]
	if da == nil {
		a.dupAccs[dk] = &dupAcc{
			row: duplicateRow{
				Day: day, ToolName: r.ToolName, ToolInputHash: r.ToolInputHash, Model: r.Model,
				Project: r.Project, IsSidechain: r.IsSidechain, IsMeta: r.IsMeta,
				SamplePreview: r.ToolInputPreview,
			},
			firstTS: r.RecordTS,
		}
		da = a.dupAccs[dk]
	} else {
		if r.ToolInputPreview < da.row.SamplePreview {
			da.row.SamplePreview = r.ToolInputPreview // MIN: plain byte-wise Go string comparison.
		}
		if r.RecordTS.Before(da.firstTS) {
			da.firstTS = r.RecordTS // MIN: time.Time instant comparison.
		}
	}
	da.row.RunCount++ // counts ALL fine-grain rows, trustworthy or not.
	if trust {
		da.row.WastedDurationMs += r.DurationMs // trustworthy rows only.
	}
}

// finish materializes the accumulated grains into the wire rollupPayload.
func (a *rollupAgg) finish() rollupPayload {
	payload := rollupPayload{SchemaVersion: rollupSchemaVersion}
	if a.tsInit {
		a.sess.FirstRecordTS = a.firstTS.Format(time.RFC3339Nano)
		a.sess.LastRecordTS = a.lastTS.Format(time.RFC3339Nano)
	}
	a.sess.AgentChainDepth = int64(len(a.chainAgents))
	payload.Session = a.sess

	for _, fa := range a.factAccs {
		fa.row.MinRecordTS = fa.minTS.Format(time.RFC3339Nano)
		fa.row.MaxRecordTS = fa.maxTS.Format(time.RFC3339Nano)
		payload.Facts = append(payload.Facts, fa.row)
	}
	for hk, count := range a.histCounts {
		payload.LatencyHist = append(payload.LatencyHist, latencyHistRow{
			Day: hk.day, ToolName: hk.toolName, Model: hk.model, Project: hk.project,
			IsSidechain: hk.isSidechain, IsMeta: hk.isMeta, Bucket: hk.bucket, CallCount: count,
		})
	}
	// slow_calls: top-100 by duration desc PER tool (stable), concatenated across tools.
	for tool := range a.slowByTool {
		calls := a.slowByTool[tool]
		sort.SliceStable(calls, func(i, j int) bool { return calls[i].DurationMs > calls[j].DurationMs })
		if len(calls) > rollupSlowCallsPerTool {
			calls = calls[:rollupSlowCallsPerTool]
		}
		payload.SlowCalls = append(payload.SlowCalls, calls...)
	}
	// duplicate_commands: emit every fine row whose parent (tool,hash) session-total > 1.
	// The sync backend's filters are purely SUBTRACTIVE, so a (tool,hash) group whose
	// unfiltered session-total <= 1 can never be a duplicate under any filter — omitting it
	// is lossless; the backend re-aggregates the shipped fine rows GROUP BY session,tool,
	// hash HAVING SUM(run_count) > 1.
	for dk, da := range a.dupAccs {
		if a.dupSessionTotals[dupSessionKey{toolName: dk.toolName, toolInputHash: dk.toolInputHash}] > 1 {
			da.row.FirstRecordTS = da.firstTS.Format(time.RFC3339Nano)
			payload.DuplicateCommands = append(payload.DuplicateCommands, da.row)
		}
	}
	return payload
}

// computeSessionRollup aggregates one session's rows into the wire rollupPayload in a
// single O(n) pass (plus a per-tool sort of the trustworthy tool rows and a final
// emission pass over the fine duplicate map). It is pure in-memory work — no DuckDB, no
// I/O — safe to run inside the NumCPU-parallel producer. Synthetic-model and is_meta rows
// are INCLUDED verbatim; the sync backend applies read-time filters.
func computeSessionRollup(rows []transcripts.Row) rollupPayload {
	agg := newRollupAgg()
	for i := range rows {
		agg.add(rows[i])
	}
	return agg.finish()
}
