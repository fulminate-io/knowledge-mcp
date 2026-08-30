// SPDX-License-Identifier: Apache-2.0

// corpus_finalize.go — the per-file REDUCTION pass: the quantities that cannot be
// accumulated one row at a time, because they depend on the whole lane.
//
// Split out of corpus.go so that file stays within the repo's 500-line commit gate. The
// split follows the fold's own three phases, one file each: corpus.go adds a row,
// this file reduces a finished partial, and corpus_merge.go combines two partials.
//
// Everything here runs ONCE per partial corpus, after every row of that partial's file has
// been folded and before the partial is merged — and each reduction RELEASES the per-row
// working state it consumed, so a merged corpus never carries per-row data and the loader's
// documented per-file memory bound holds.
package transcriptanalytics

import (
	"math"
	"sort"
	"time"
)

// finalizeActive reduces one PARTIAL corpus's collected per-agent instants to a single
// activeMs per agent, writing it onto both the subagent and the chain accumulator, then
// RELEASES the instants. A subagent lane is one file (the cache keys on the file stem), so
// one file holds that agent's whole record sequence and the per-file figures merge
// additively from there.
func (c *corpus) finalizeActive() {
	active := make(map[string]int64, len(c.agentInstants))
	for id, instants := range c.agentInstants {
		active[id] = activeMsFromInstants(instants)
	}
	for id, sa := range c.subagents {
		sa.activeMs = active[id]
	}
	for _, agents := range c.chains {
		for id, ca := range agents {
			ca.activeMs = active[id]
		}
	}
	c.agentInstants = nil

	c.laneActiveMs = activeMsFromInstants(c.laneInstants)
	c.laneInstants = nil

	c.finalizeResidency()
}

// finalizeResidency reduces one PARTIAL corpus's collected tool results to per-tool
// residency totals, then RELEASES the working state. It runs in the same per-file pass as
// the active-time reduction, so the lane's rows are walked once rather than twice.
//
// A result's residency is its token size times the number of model calls issued AFTER it,
// because that is how many times the caller paid to carry it. The boundary is STRICTLY
// greater than the tool row's instant: the assistant record that ISSUED the call shares
// that instant and must not count, having been billed before the result existed.
func (c *corpus) finalizeResidency() {
	sort.Slice(c.modelInstants, func(i, j int) bool { return c.modelInstants[i].Before(c.modelInstants[j]) })
	for _, rr := range c.resultRows {
		acc := c.residency[rr.tool]
		if acc == nil {
			acc = &residencyAcc{}
			c.residency[rr.tool] = acc
		}
		acc.calls++
		acc.resultBytes += rr.bytes
		if rr.bytes > 0 {
			c.lanesWithResultBytes = 1
		}
		if rr.spilled {
			acc.spilledResults++
		}
		tokens := resultTokens(rr.bytes, rr.images)
		acc.resultTokens += tokens
		after := int64(len(c.modelInstants) - sort.Search(len(c.modelInstants), func(i int) bool {
			return c.modelInstants[i].After(rr.ts)
		}))
		acc.residentTokens += tokens * after
	}
	c.resultRows, c.modelInstants = nil, nil
}

// resultTokens estimates one tool result's token size from its measured bytes and image
// count. It rounds to int64 at the ROW, deliberately: the partials are merged in
// goroutine-completion order and float addition is not associative, so accumulating floats
// would make the total depend on which file finished first.
func resultTokens(nbytes, images int64) int64 {
	return int64(math.Round(float64(nbytes)/bytesPerResultToken)) + images*imageResultTokens
}

// activeMsFromInstants sums the inter-event gaps strictly below subagentIdleGapMs over a
// lane's record instants, which is its working time with the pauses taken out. The instants
// are sorted first because the fold sees rows in file order, not timestamp order. Fewer
// than two instants means no gap exists, so the result is 0 — the same answer a lane with
// a single record gives for its span. Gaps are measured on the floor-epoch millisecond, the
// same basis wallMs uses, so active and span stay comparable.
func activeMsFromInstants(instants []time.Time) int64 {
	if len(instants) < 2 {
		return 0
	}
	sort.Slice(instants, func(i, j int) bool { return instants[i].Before(instants[j]) })
	var active int64
	for i := 1; i < len(instants); i++ {
		if gap := instants[i].UnixMilli() - instants[i-1].UnixMilli(); gap < subagentIdleGapMs {
			active += gap
		}
	}
	return active
}
