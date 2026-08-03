// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// distinctResidentDocCount exposes the engine's DISTINCT resident doc count through
// the coverageArm seam. engine is a generic FIELD, so the interface cannot reach it
// directly.
func (m *distManager[Q, S]) distinctResidentDocCount() int {
	return m.engine.DistinctResidentDocCount()
}

// residentSegmentCount exposes HOW MANY sealed segments are resident through the
// coverageArm seam. It counts the cheap id-only snapshot walk rather than Export,
// which re-serializes every payload to answer what is only a set-size question.
func (m *distManager[Q, S]) residentSegmentCount() int {
	return len(m.engine.ResidentSegmentIDs())
}

// ReBucketNeeded reports whether a graph's resident layout is a FULL DOUBLING behind
// the partition count its corpus now derives, and returns the two operands the
// answer was taken on so a caller acting on it can record WHY it acted.
//
// It is the QUIET-GRAPH detector. A graph whose growth left part of the partition
// space untouched across at least two doublings and then stopped writing has nothing
// else that will converge it: the delta path is always scoped to the partitions a
// write actually reached, and write-driven realignment needs writes.
//
// PER FORMAT, FIRING IF EITHER ARM IS BEHIND. The formats are evaluated
// independently and either one being behind is enough, because ONE reset re-buckets
// both — a per-format answer would give the caller nothing to do differently, while
// requiring BOTH to be behind would let a lagging format sit uncorrected behind a
// converged one.
//
// THE RULE IS candidate >= 2*current — a FULL doubling behind, not merely unequal.
// Requiring a whole step is what makes the detector immune to partial realignment
// and to transient segments: a candidate != current rule would fire on every
// actively-realigning graph and on every drain that has just sealed a thin tail,
// turning a one-time correction into a per-tick rebuild storm.
//
// THE PRICE OF THE FULL-DOUBLING RULE IS REAL AND IS NOT HIDDEN. Growth confined to
// part of the partition space across EXACTLY ONE doubling never satisfies it — four
// partitions crossing to eight leaves five segments against a derived eight, and
// eight is not at or above ten — so that population stays under-partitioned and no
// quiet-graph mechanism converges it. That is a deliberate trade for storm immunity,
// tracked as its own piece of work; it is not an oversight to repair by loosening
// the rule here.
//
// THE OPERANDS ARE TWO FREE LOCAL READS, and both choices are load-bearing:
//
//   - candidate is BucketCountFor of the DISTINCT resident doc count. The plain
//     resident count sums per-segment doc counts, so a document resident in two
//     segments across an un-reclaimed window counts twice and would manufacture a
//     crossing the corpus never made.
//   - current is the number of resident segment ids — one atomic snapshot load and a
//     walk of the entry metas. Export answers the same question by re-serializing
//     every payload, tens of megabytes of encoding on a full corpus, which this
//     detector cannot afford: it runs per format, per graph, per tick,
//     unconditionally.
//
// AN ARM WITH NO RESIDENT SEGMENTS IS SKIPPED, and that is the ordinary case rather
// than a corner: a graph written through the vector path alone leaves the field arm
// empty, and 2*0 == 0 would fire on any non-empty candidate. An empty or unloaded
// engine is the degeneracy probe's business, not this one's.
//
// DOWN-CROSSINGS ARE OUT BY CONSTRUCTION — a shrinking corpus makes candidate
// smaller, never larger than twice current — and the asymmetry is deliberate.
// Under-partitioning coarsens re-emit granularity, which is the harm this exists to
// stop; over-partitioning costs only search fan-out, is bounded by the derivation's
// own cap, and leaves granularity FINER than the target rather than coarser.
//
// The returned operands are the FIRING arm's when one fires, and the first
// measurable arm's when none does. Nothing else is read: two atomic snapshot loads
// and an integer comparison per format, no source access and no lock beyond the
// engine's own snapshot load — which is why this can run unconditionally rather than
// behind a sampling gate of its own.
func (m *Manager) ReBucketNeeded(gt kgtypes.GraphType, name string) (candidate, current int, needed bool) {
	for _, arm := range []coverageArm{m.managerFor(gt, name), m.bm25ManagerFor(gt, name)} {
		armCurrent := arm.residentSegmentCount()
		if armCurrent == 0 {
			continue
		}
		armCandidate := searchengine.BucketCountFor(arm.distinctResidentDocCount())
		if armCandidate >= 2*armCurrent {
			return armCandidate, armCurrent, true
		}
		if current == 0 {
			candidate, current = armCandidate, armCurrent
		}
	}
	return candidate, current, false
}
