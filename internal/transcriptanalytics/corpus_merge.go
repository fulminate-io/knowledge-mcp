// SPDX-License-Identifier: Apache-2.0

// corpus_merge.go — the associative merge side of the corpus fold: how two partial corpora
// built by the parallel file loader combine into one.
//
// Split out of corpus.go so that file stays within the repo's 500-line commit gate while the
// analyzer grows. The split is along the add/merge seam: corpus.go holds the add* methods
// that fold a single row, this file holds the merge* methods that fold a whole partial
// corpus. Every helper here must stay ASSOCIATIVE — the loader merges partials in
// goroutine-completion order, and every final ORDER BY has a deterministic tie-break, so
// merge order must not affect any materialized result.
package transcriptanalytics

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
	mergeTokens(c.tokensBySubagent, o.tokensBySubagent)
	c.mergeDupes(o)
	c.mergeResidency(o)
}

// mergeResidency folds the per-tool residency totals. Every field is an int64 sum, so the
// merge is associative and the result does not depend on file-completion order.
func (c *corpus) mergeResidency(o *corpus) {
	c.lanesWithResultBytes += o.lanesWithResultBytes
	for tool, acc := range o.residency {
		dst := c.residency[tool]
		if dst == nil {
			cp := *acc
			c.residency[tool] = &cp
			continue
		}
		dst.calls += acc.calls
		dst.resultBytes += acc.resultBytes
		dst.resultTokens += acc.resultTokens
		dst.residentTokens += acc.residentTokens
		dst.spilledResults += acc.spilledResults
	}
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

// mergeScalars folds the global cache + waste sums and the provenance counters. laneCount
// is deliberately absent: it is assigned once by the loader, not accumulated per partial.
func (c *corpus) mergeScalars(o *corpus) {
	c.recordCount += o.recordCount
	c.extendWindow(o.minTS, o.maxTS)
	c.laneTurns += o.laneTurns
	c.laneModelMs += o.laneModelMs
	c.laneActiveMs += o.laneActiveMs
	c.laneWaits = mergeWaits(c.laneWaits, o.laneWaits)
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
		dst.activeMs += acc.activeMs
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
			cur.activeMs += acc.activeMs
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
		dst.trustCount += acc.trustCount
		dst.backgroundCount += acc.backgroundCount
		dst.wastedSum += acc.wastedSum
		dst.preview = minStr(dst.preview, acc.preview)
	}
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
