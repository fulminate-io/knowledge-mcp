// SPDX-License-Identifier: Apache-2.0

// corpus.go — the pure-Go, in-memory analytics model + single-pass aggregator that
// replaced the DuckDB read engine. It folds the local parquet cache's
// transcripts.Rows into corpus-wide accumulators mirroring the platform's server-side
// usage-analytics tables, then the per-family folds
// (detectors_{latency,flow,efficiency}.go) materialize the unchanged DetectorReport DTOs
// from those accumulators.
//
// This file holds the intake predicate, the histogram math and the add* fold; its three
// siblings hold the rest of the same model. corpus_accumulators.go holds the accumulator
// types the fold writes into plus their comparison helpers, corpus_finalize.go holds the
// per-file reduction of quantities that depend on the whole lane, and corpus_merge.go holds
// the associative merge that combines two partial corpora from the parallel loader.
//
// LOCKSTEP / provenance (re-authored per-repo, NOT imported — the sanctioned pattern;
// the only shared cross-module contract is generated protobuf, AGENTS.md):
//   - histPercentile + bucketRepresentative implement the same log2-histogram percentile
//     method the platform's server-side usage analytics uses; the bucket scheme is frozen
//     as the sync wire contract.
//   - durationBucket + trustworthy + latencyBucketMaxExp mirror
//     transcriptsync/rollup.go (its rollupTrustworthy/durationBucket) and the
//     rollupBucketMaxExp=31 clamp (transcriptsync/wire.go). This is a LOCKSTEP table with
//     those two sites: a one-sided change to the bucket scheme (here or in transcriptsync)
//     silently desyncs client/cloud percentiles, so the unit tests pin the boundary math +
//     the 31 clamp (mirroring the idle-guard lockstep note in detectors_schema.go).
//   - wallMs mirrors the platform's floor-epoch wall-span expression.
//   - the corpus accumulators mirror the platform's read tables one-for-one (see each field).
//
// Baseline: rows are filtered by Filters.keep at intake, so the accumulators only ever
// see kept rows — is_meta==true and synthetic-model rows never enter a total. A
// MISSING/false is_meta is KEPT (the natural parquet-go zero value); only a genuine
// is_meta==true is excluded.
package transcriptanalytics

import (
	"math"
	"math/bits"
	"sort"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/transcripts"
)

// latencyBucketMaxExp clamps the log2 tool-latency bucket exponent. Re-authored mirror of
// transcriptsync.rollupBucketMaxExp (wire.go:163) — a bucket b covers [2^b, 2^(b+1)) ms;
// b is clamped so a pathological duration cannot explode the sparse histogram key space.
// LOCKSTEP with the sync wire contract: must not drift one-sidedly.
const latencyBucketMaxExp = 31

// bucketRepresentative is the conservative upper-edge latency (ms) a log2 histogram
// bucket stands for: 2^(b+1)-1 — the same representative the platform's server-side usage
// analytics uses (frozen with the sync wire-contract bucket scheme).
func bucketRepresentative(b int) int64 { return int64(1)<<(b+1) - 1 }

// histPercentile approximates the q-quantile of a tool's trustworthy-latency distribution
// from its per-bucket call counts, returning the frozen representative of the bucket where
// the cumulative count first reaches ceil(q*total). Within one bucket-width of the true
// quantile (the histogram's inherent resolution). VERBATIM port of agent
// rollup_insights.go:26.
func histPercentile(counts map[int]int64, total int64, q float64) int64 {
	if total <= 0 {
		return 0
	}
	buckets := make([]int, 0, len(counts))
	for b := range counts {
		buckets = append(buckets, b)
	}
	sort.Ints(buckets)
	target := max(int64(math.Ceil(q*float64(total))), 1)
	var cum int64
	for _, b := range buckets {
		cum += counts[b]
		if cum >= target {
			return bucketRepresentative(b)
		}
	}
	return bucketRepresentative(buckets[len(buckets)-1])
}

// durationBucket maps a duration in ms to its sparse log2 histogram bucket: 0 for ms<=0,
// else min(floor(log2(ms)), latencyBucketMaxExp). floor(log2(ms)) == bits.Len64(ms)-1.
// Mirror of transcriptsync/rollup.go:46. Boundaries: ms=1→0, ms=2→1, ms=3→1, huge→31.
func durationBucket(ms int64) int {
	if ms <= 0 {
		return 0
	}
	b := bits.Len64(uint64(ms)) - 1
	if b > latencyBucketMaxExp {
		return latencyBucketMaxExp
	}
	return b
}

// trustworthy reports whether a row's DurationMs is a trustworthy tool-execution span:
// NOT interrupted AND 1 <= DurationMs <= idleGuardCeilingMs. Mirror of
// transcriptsync/rollup.go:38 (rollupTrustworthy); a >2h span is assumed to straddle a
// paused/resumed session and is EXCLUDED from trustworthy-time metrics (never clipped). A
// non-trustworthy row still COUNTS toward record/run counts but contributes 0ms to
// trustworthy time + wasted time and is excluded from the latency histogram.
func trustworthy(r transcripts.Row) bool {
	return !r.Interrupted && r.DurationMs >= 1 && r.DurationMs <= idleGuardCeilingMs
}

// wallMs is the floor-epoch wall-clock span in milliseconds between two record instants.
// Mirror of the agent's wallMsExpr (rollup_query.go:248): floor-then-subtract per
// timestamp. time.Time.UnixMilli truncates toward the epoch, matching the SQL floor().
func wallMs(minTS, maxTS time.Time) int64 {
	return maxTS.UnixMilli() - minTS.UnixMilli()
}

// keep is the baseline row predicate that replaced the SQL where(): it drops the
// synthetic-model marker and genuine is_meta==true rows from EVERY total, then applies the
// Since/Until/Model/Tool/Project field filters and the population predicate. Per the CEO
// is_meta fix, a MISSING/false is_meta is KEPT (parquet-go zero-fills the absent column to
// false) — the OPPOSITE of the old duckdb NOT-NULL exclusion. Since is inclusive
// (record_ts >= Since); Until is exclusive (record_ts < Until).
//
// This is where a narrowed population is applied, and the only place: rows are dropped at
// intake, before any accumulator sees them, so every detector fold downstream is the same
// code reading a smaller corpus rather than a variant of itself.
func (f Filters) keep(r transcripts.Row) bool {
	if r.Model == syntheticModel || r.IsMeta {
		return false
	}
	if !f.keepInPopulation(r) {
		return false
	}
	if !f.Since.IsZero() && r.RecordTS.Before(f.Since) {
		return false
	}
	if !f.Until.IsZero() && !r.RecordTS.Before(f.Until) {
		return false
	}
	if f.Model != "" && r.Model != f.Model {
		return false
	}
	if f.Tool != "" && r.ToolName != f.Tool {
		return false
	}
	if f.Project != "" && r.Project != f.Project {
		return false
	}
	return true
}

// keepInPopulation is the per-row half of the population selector.
//
// ScopeSessionTree matches on session id alone, which admits the main lane AND every
// subagent lane it spawned: a subagent record carries its PARENT session id, measured
// across all 2,042 on-disk subagent transcripts with zero mismatches.
//
// ScopeAll and ScopeTimeRange add nothing here. Since and Until are already applied above,
// and duplicating the comparison would be a second implementation of one rule.
func (f Filters) keepInPopulation(r transcripts.Row) bool {
	switch f.resolved() {
	case ScopeSessionTree:
		return r.SessionID == f.SessionID
	case ScopeSingle:
		if f.AgentID != "" {
			return r.AgentID == f.AgentID
		}
		return r.SessionID == f.SessionID && !r.IsSidechain
	case ScopeAll, ScopeTimeRange:
		return true
	default:
		return true
	}
}

// corpus holds the corpus-wide accumulators built in a single pass over the kept rows.
// Every map mirrors one of the agent's PG read tables; the per-family folds
// (detectors_{latency,flow,efficiency}.go) materialize the DetectorReport DTOs from these.
type corpus struct {
	// ToolLatency (QueryToolLatency / usage_tool_latency_hist): per-tool bucket→count +
	// per-tool trustworthy total, over trustworthy named-tool rows only.
	latencyHist  map[string]map[int]int64
	latencyTotal map[string]int64
	// ToolTimeTotals (toolTimeTotalPG): per-tool trustworthy-sum + all-row count.
	toolTime map[string]*toolTimeAcc
	// CacheEfficiency (cacheEfficiencyPG) + Waste (wasteInsightsPG) global sums. The
	// max_tokens cost uses RAW duration_ms (NOT trustworthy) per the waste golden.
	cacheRead, inputTokens, cc1h, cc5m           int64
	apiErrorCount, interruptedCount              int64
	maxTokCount, maxTokOutput, maxTokDurationRaw int64
	// SubagentWallTime (QuerySubagentWallTime): per agent_id (sidechain, agent_id<>'').
	subagents map[string]*subagentAcc
	// AgentChains (agentChainPG): per session → per agent_id wall span.
	chains map[string]map[string]*chainAgentAcc
	// AvgTokensPerSession (QueryAvgTokensPerSession): per session token sums.
	sessions map[string]*tokenAcc
	// DuplicateCommands (QueryDuplicateCommands): per (session,tool,hash).
	dupes map[dupKey]*dupAcc
	// TokensBySubagentType (QueryBreakdown): per subagent-type token sums. There is no
	// by-TOOL counterpart: the parser splits a turn into a zero-tool token row plus
	// zero-token tool_use rows, so a per-tool token sum is structurally zero. What a tool
	// actually costs is measured by the residency family below instead.
	tokensBySubagent map[string]*tokenAcc
	// ResultResidencyByTool: per-tool result size and its residency-weighted token cost.
	residency map[string]*residencyAcc
	// resultRows and modelInstants are PARTIAL-only working state for the residency fold,
	// released by finalizeActive once the file's residency is computed.
	resultRows    []residencyRow
	modelInstants []time.Time
	// lanesWithResultBytes counts lanes carrying at least one measured result size. It is
	// 0 or 1 on a partial (a partial is one lane) and sums across the merge.
	lanesWithResultBytes int64
	// CorpusProvenance: the kept-row count and the record-instant window the report
	// discloses as the basis its numbers rest on. Folded in the same pass as the totals.
	recordCount  int64
	minTS, maxTS time.Time
	// laneCount is how many parquet files the loader's glob resolved. It is a LOADER-level
	// fact assigned once by loadCorpus, not a per-row accumulation, so merge does not fold
	// it — a partial corpus has no lane count of its own to contribute.
	laneCount int64
	// Lane-detail accumulators, folded ONLY when collectLane is set — that is, only when the
	// corpus has been narrowed to a single lane and the family will actually be rendered.
	// Over the whole corpus they would be per-row work for a family nobody reads.
	collectLane  bool
	laneTurns    int64
	laneModelMs  int64
	laneActiveMs int64
	laneWaits    []LaneWaitRow
	// laneInstants mirrors agentInstants for the lane fold: a MAIN-session lane carries no
	// agent_id, so its active time has no per-agent list to be reduced from. Released by
	// finalizeActive alongside agentInstants.
	laneInstants []time.Time
	// agentInstants holds one PARTIAL corpus's per-agent record instants while its file is
	// being folded. Active time cannot be accumulated per row because nothing guarantees
	// rows arrive in timestamp order, so the instants are collected and reduced once by
	// finalizeActive — which then releases this map. A MERGED corpus never holds it, so the
	// loader's per-file memory bound is unchanged.
	agentInstants map[string][]time.Time
}

// newCorpus allocates an empty corpus with every accumulator map ready.
func newCorpus() *corpus {
	return &corpus{
		latencyHist:      map[string]map[int]int64{},
		latencyTotal:     map[string]int64{},
		toolTime:         map[string]*toolTimeAcc{},
		subagents:        map[string]*subagentAcc{},
		chains:           map[string]map[string]*chainAgentAcc{},
		sessions:         map[string]*tokenAcc{},
		dupes:            map[dupKey]*dupAcc{},
		tokensBySubagent: map[string]*tokenAcc{},
		residency:        map[string]*residencyAcc{},
		agentInstants:    map[string][]time.Time{},
	}
}

// add folds one KEPT row into every relevant accumulator (single O(1) amortized pass).
// The caller (loadCorpus) has already applied Filters.keep, so add never sees an excluded
// row.
func (c *corpus) add(r transcripts.Row) {
	trust := trustworthy(r)
	c.addSessionAndGlobals(r)
	c.addTokenDims(r)
	c.addResidency(r)
	if r.ToolName != "" {
		c.addTool(r, trust)
	}
	if r.IsSidechain && r.AgentID != "" {
		c.addSubagent(r)
	}
	if r.ToolInputHash != "" {
		c.addDuplicate(r, trust)
	}
	if c.collectLane {
		c.addLane(r, trust)
	}
}

// addLane folds one row into the single-lane accumulators. The model/tool split keys on
// whether the row names a tool: the parser emits a turn as a zero-tool token row plus its
// zero-token tool_use rows, so a row with no tool name carries the model's own latency and
// a row with one carries that tool's execution span.
func (c *corpus) addLane(r transcripts.Row, trust bool) {
	c.laneInstants = append(c.laneInstants, r.RecordTS)
	if r.ToolName == "" {
		c.laneTurns++
		if trust {
			c.laneModelMs += r.DurationMs
		}
		return
	}
	if trust {
		c.laneWaits = insertWait(c.laneWaits, LaneWaitRow{
			ToolName: r.ToolName, DurationMs: r.DurationMs,
			Background: r.RunInBackground, Preview: r.ToolInputPreview,
		})
	}
}

// addSessionAndGlobals folds the per-session token sums (avg-tokens) plus the global cache
// + waste scalars, and the two provenance counters (kept-row count + record window) that
// disclose what the totals were computed over. cache InputTokens == the global input sum
// used by cacheEfficiency.
func (c *corpus) addSessionAndGlobals(r transcripts.Row) {
	c.recordCount++
	c.extendWindow(r.RecordTS, r.RecordTS)

	s := c.sessions[r.SessionID]
	if s == nil {
		s = &tokenAcc{}
		c.sessions[r.SessionID] = s
	}
	s.inSum += r.InputTokens
	s.outSum += r.OutputTokens

	c.cacheRead += r.CacheReadTokens
	c.inputTokens += r.InputTokens
	c.cc1h += r.CacheCreation1hTokens
	c.cc5m += r.CacheCreation5mTokens
	if r.IsAPIError {
		c.apiErrorCount++
	}
	if r.Interrupted {
		c.interruptedCount++
	}
	if r.StopReason == stopReasonMaxTokens {
		c.maxTokCount++
		c.maxTokOutput += r.OutputTokens
		c.maxTokDurationRaw += r.DurationMs // RAW duration (matches wasteInsightsPG).
	}
}

// addTokenDims folds the per-subagent-type token sums (QueryBreakdown), COALESCING a
// missing value to the "" key so every kept row participates. subagent_type is a dimension
// token rows genuinely carry, which is why it survives where the by-tool dimension did not.
func (c *corpus) addTokenDims(r transcripts.Row) {
	addToken(c.tokensBySubagent, r.SubagentType, r)
}

// addResidency collects the working state the per-tool residency fold needs. A tool row
// contributes its result's size; a token row contributes its instant, because a result's
// cost is its size times the number of model calls that come AFTER it.
func (c *corpus) addResidency(r transcripts.Row) {
	if r.ToolName == "" {
		c.modelInstants = append(c.modelInstants, r.RecordTS)
		return
	}
	c.resultRows = append(c.resultRows, residencyRow{
		tool: r.ToolName, ts: r.RecordTS,
		bytes: r.ToolResultBytes, images: r.ToolResultImages, spilled: r.ToolResultSpilled,
	})
}

// addTool folds a named-tool row into the per-tool time total (all rows) and — when
// trustworthy — the latency histogram (QueryToolLatency).
func (c *corpus) addTool(r transcripts.Row, trust bool) {
	acc := c.toolTime[r.ToolName]
	if acc == nil {
		acc = &toolTimeAcc{}
		c.toolTime[r.ToolName] = acc
	}
	acc.count++
	if !trust {
		return
	}
	acc.trustSum += r.DurationMs
	buckets := c.latencyHist[r.ToolName]
	if buckets == nil {
		buckets = map[int]int64{}
		c.latencyHist[r.ToolName] = buckets
	}
	buckets[durationBucket(r.DurationMs)]++
	c.latencyTotal[r.ToolName]++
}

// addSubagent folds a sidechain row (agent_id non-empty) into the per-agent wall accumulator
// (QuerySubagentWallTime) AND the per-(session,agent) chain grain (agentChainPG), and
// collects the row's instant for the active-time reduction finalizeActive performs once the
// whole file has been folded. Caller guarantees IsSidechain && AgentID != "".
func (c *corpus) addSubagent(r transcripts.Row) {
	c.agentInstants[r.AgentID] = append(c.agentInstants[r.AgentID], r.RecordTS)

	sa := c.subagents[r.AgentID]
	if sa == nil {
		c.subagents[r.AgentID] = &subagentAcc{subagentType: r.SubagentType, minTS: r.RecordTS, maxTS: r.RecordTS, inSum: r.InputTokens, outSum: r.OutputTokens}
	} else {
		sa.subagentType = minStr(sa.subagentType, r.SubagentType)
		sa.minTS, sa.maxTS = earlier(sa.minTS, r.RecordTS), latest(sa.maxTS, r.RecordTS)
		sa.inSum += r.InputTokens
		sa.outSum += r.OutputTokens
	}

	agents := c.chains[r.SessionID]
	if agents == nil {
		agents = map[string]*chainAgentAcc{}
		c.chains[r.SessionID] = agents
	}
	ca := agents[r.AgentID]
	if ca == nil {
		agents[r.AgentID] = &chainAgentAcc{subagentType: r.SubagentType, minTS: r.RecordTS, maxTS: r.RecordTS}
	} else {
		ca.subagentType = minStr(ca.subagentType, r.SubagentType)
		ca.minTS, ca.maxTS = earlier(ca.minTS, r.RecordTS), latest(ca.maxTS, r.RecordTS)
	}
}

// addDuplicate folds a hashed row into its (session,tool,hash) grain: run count over all
// rows, wasted-duration AND its own trustworthy-row count over trustworthy rows only, and
// MIN(preview) byte-wise. Caller guarantees ToolInputHash != "".
func (c *corpus) addDuplicate(r transcripts.Row, trust bool) {
	k := dupKey{session: r.SessionID, tool: r.ToolName, hash: r.ToolInputHash}
	d := c.dupes[k]
	if d == nil {
		d = &dupAcc{preview: r.ToolInputPreview}
		c.dupes[k] = d
	} else {
		d.preview = minStr(d.preview, r.ToolInputPreview)
	}
	d.count++
	if r.RunInBackground {
		d.backgroundCount++
	}
	if trust {
		d.trustCount++
		d.wastedSum += r.DurationMs
	}
}

// extendWindow widens the corpus's record-instant window to cover [minTS,maxTS]. A ZERO
// minTS contributes nothing — it is the "folded no row yet" sentinel on the receiving side
// and, on the contributing side, a row whose record_ts never decoded. Shared by the row
// fold (one instant twice) and the partial merge (a whole partial's bounds).
func (c *corpus) extendWindow(minTS, maxTS time.Time) {
	if minTS.IsZero() {
		return
	}
	if c.minTS.IsZero() {
		c.minTS, c.maxTS = minTS, maxTS
		return
	}
	c.minTS, c.maxTS = earlier(c.minTS, minTS), latest(c.maxTS, maxTS)
}

// addToken folds a row's input/output tokens into a per-key token accumulator.
func addToken(m map[string]*tokenAcc, key string, r transcripts.Row) {
	acc := m[key]
	if acc == nil {
		acc = &tokenAcc{}
		m[key] = acc
	}
	acc.inSum += r.InputTokens
	acc.outSum += r.OutputTokens
}
