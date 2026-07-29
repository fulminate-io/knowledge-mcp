// SPDX-License-Identifier: Apache-2.0

// corpus.go — the pure-Go, in-memory analytics model + single-pass aggregator that
// replaced the DuckDB read engine. It folds the local parquet cache's
// transcripts.Rows into corpus-wide accumulators mirroring the platform's server-side
// usage-analytics tables, then the per-family folds
// (detectors_{latency,flow,efficiency}.go) materialize the unchanged DetectorReport DTOs
// from those accumulators.
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
// Since/Until/Model/Tool/Project field filters. Per the CEO is_meta fix, a MISSING/false
// is_meta is KEPT (parquet-go zero-fills the absent column to false) — the OPPOSITE of the
// old duckdb NOT-NULL exclusion. Since is inclusive (record_ts >= Since); Until is
// exclusive (record_ts < Until).
func (f Filters) keep(r transcripts.Row) bool {
	if r.Model == syntheticModel || r.IsMeta {
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

// Accumulators — each mirrors one agent read table (see corpus fields).

// toolTimeAcc mirrors a usage_fact per-tool group: trustSum = SUM(trustworthy_duration_ms),
// count = SUM(record_count) over ALL rows with that tool (trustworthy or not).
type toolTimeAcc struct {
	trustSum int64
	count    int64
}

// tokenAcc is a per-dimension input/output token sum (usage_fact SUM(input/output_tokens)).
type tokenAcc struct {
	inSum  int64
	outSum int64
}

// subagentAcc mirrors a usage_fact per-agent_id group: MIN(subagent_type), the min/max
// record instants for the floor-epoch wall span, and the token sums.
type subagentAcc struct {
	subagentType  string
	minTS, maxTS  time.Time
	inSum, outSum int64
}

// chainAgentAcc mirrors the agentChainPG inner per-(session,agent_id) grain: MIN
// subagent_type + the min/max instants for that agent's wall span.
type chainAgentAcc struct {
	subagentType string
	minTS, maxTS time.Time
}

// dupKey is the (session,tool,hash) duplicate-command grain.
type dupKey struct {
	session, tool, hash string
}

// dupAcc mirrors a usage_duplicate_commands (session,tool,hash) group: run count, the
// trustworthy-only wasted-duration sum, and MIN(sample_preview) byte-wise.
type dupAcc struct {
	count     int64
	wastedSum int64
	preview   string
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
	// TokensByTool / TokensBySubagentType (QueryBreakdown): per dimension token sums.
	tokensByTool     map[string]*tokenAcc
	tokensBySubagent map[string]*tokenAcc
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
		tokensByTool:     map[string]*tokenAcc{},
		tokensBySubagent: map[string]*tokenAcc{},
	}
}

// add folds one KEPT row into every relevant accumulator (single O(1) amortized pass).
// The caller (loadCorpus) has already applied Filters.keep, so add never sees an excluded
// row.
func (c *corpus) add(r transcripts.Row) {
	trust := trustworthy(r)
	c.addSessionAndGlobals(r)
	c.addTokenDims(r)
	if r.ToolName != "" {
		c.addTool(r, trust)
	}
	if r.IsSidechain && r.AgentID != "" {
		c.addSubagent(r)
	}
	if r.ToolInputHash != "" {
		c.addDuplicate(r, trust)
	}
}

// addSessionAndGlobals folds the per-session token sums (avg-tokens) plus the global cache
// + waste scalars. cache InputTokens == the global input sum used by cacheEfficiency.
func (c *corpus) addSessionAndGlobals(r transcripts.Row) {
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

// addTokenDims folds the per-tool and per-subagent-type token sums (QueryBreakdown). Both
// dimensions COALESCE a missing value to the "" key, so every kept row participates.
func (c *corpus) addTokenDims(r transcripts.Row) {
	addToken(c.tokensByTool, r.ToolName, r)
	addToken(c.tokensBySubagent, r.SubagentType, r)
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

// addSubagent folds a sidechain row (agent_id<>”) into the per-agent wall accumulator
// (QuerySubagentWallTime) AND the per-(session,agent) chain grain (agentChainPG). Caller
// guarantees IsSidechain && AgentID != "".
func (c *corpus) addSubagent(r transcripts.Row) {
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
// rows, wasted-duration over trustworthy rows only, and MIN(preview) byte-wise. Caller
// guarantees ToolInputHash != "".
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
	if trust {
		d.wastedSum += r.DurationMs
	}
}

// merge folds another partial corpus into c (associative, for the parallel file loader).
// Every final ORDER BY has a deterministic tie-break, so merge order does not affect any
// materialized result. Split across per-grain helpers to stay readable + under the
// per-function budget.
func (c *corpus) merge(o *corpus) {
	c.mergeLatency(o)
	c.mergeToolTime(o)
	c.mergeScalars(o)
	c.mergeSubagents(o)
	c.mergeChains(o)
	mergeTokens(c.sessions, o.sessions)
	mergeTokens(c.tokensByTool, o.tokensByTool)
	mergeTokens(c.tokensBySubagent, o.tokensBySubagent)
	c.mergeDupes(o)
}

// mergeLatency folds the per-tool latency histogram + trustworthy totals.
func (c *corpus) mergeLatency(o *corpus) {
	for tool, buckets := range o.latencyHist {
		dst := c.latencyHist[tool]
		if dst == nil {
			dst = map[int]int64{}
			c.latencyHist[tool] = dst
		}
		for b, n := range buckets {
			dst[b] += n
		}
	}
	for tool, n := range o.latencyTotal {
		c.latencyTotal[tool] += n
	}
}

// mergeToolTime folds the per-tool trustworthy-sum + all-row count.
func (c *corpus) mergeToolTime(o *corpus) {
	for tool, acc := range o.toolTime {
		dst := c.toolTime[tool]
		if dst == nil {
			dst = &toolTimeAcc{}
			c.toolTime[tool] = dst
		}
		dst.trustSum += acc.trustSum
		dst.count += acc.count
	}
}

// mergeScalars folds the global cache + waste sums.
func (c *corpus) mergeScalars(o *corpus) {
	c.cacheRead += o.cacheRead
	c.inputTokens += o.inputTokens
	c.cc1h += o.cc1h
	c.cc5m += o.cc5m
	c.apiErrorCount += o.apiErrorCount
	c.interruptedCount += o.interruptedCount
	c.maxTokCount += o.maxTokCount
	c.maxTokOutput += o.maxTokOutput
	c.maxTokDurationRaw += o.maxTokDurationRaw
}

// mergeSubagents folds the per-agent_id wall accumulators.
func (c *corpus) mergeSubagents(o *corpus) {
	for id, acc := range o.subagents {
		dst := c.subagents[id]
		if dst == nil {
			cp := *acc
			c.subagents[id] = &cp
			continue
		}
		dst.subagentType = minStr(dst.subagentType, acc.subagentType)
		dst.minTS, dst.maxTS = earlier(dst.minTS, acc.minTS), latest(dst.maxTS, acc.maxTS)
		dst.inSum += acc.inSum
		dst.outSum += acc.outSum
	}
}

// mergeChains folds the per-(session,agent) chain grains.
func (c *corpus) mergeChains(o *corpus) {
	for sess, agents := range o.chains {
		dst := c.chains[sess]
		if dst == nil {
			dst = map[string]*chainAgentAcc{}
			c.chains[sess] = dst
		}
		for id, acc := range agents {
			cur := dst[id]
			if cur == nil {
				cp := *acc
				dst[id] = &cp
				continue
			}
			cur.subagentType = minStr(cur.subagentType, acc.subagentType)
			cur.minTS, cur.maxTS = earlier(cur.minTS, acc.minTS), latest(cur.maxTS, acc.maxTS)
		}
	}
}

// mergeDupes folds the per-(session,tool,hash) duplicate grains.
func (c *corpus) mergeDupes(o *corpus) {
	for k, acc := range o.dupes {
		dst := c.dupes[k]
		if dst == nil {
			cp := *acc
			c.dupes[k] = &cp
			continue
		}
		dst.count += acc.count
		dst.wastedSum += acc.wastedSum
		dst.preview = minStr(dst.preview, acc.preview)
	}
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

// mergeTokens folds src token sums into dst (associative).
func mergeTokens(dst, src map[string]*tokenAcc) {
	for key, acc := range src {
		d := dst[key]
		if d == nil {
			cp := *acc
			dst[key] = &cp
			continue
		}
		d.inSum += acc.inSum
		d.outSum += acc.outSum
	}
}

// minStr returns the byte-wise-lesser of two strings (MIN semantics).
func minStr(a, b string) string {
	if b < a {
		return b
	}
	return a
}

// earlier / latest return the min / max of two record instants (INSTANT comparison, never
// string comparison, which would misorder across differing UTC offsets).
func earlier(a, b time.Time) time.Time {
	if b.Before(a) {
		return b
	}
	return a
}

func latest(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}
