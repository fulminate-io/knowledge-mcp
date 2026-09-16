// SPDX-License-Identifier: Apache-2.0

// manager_bucket_bound.go — the RESIDENT SEGMENT COUNT BOUND: the lazy,
// per-partition consolidation that keeps an engine's resident set inside the count
// the search fan-out's latency budget was measured at.
//
// Split out of manager_bucket_backlog.go rather than added to it: that file is the
// write entry points and the reconcile tick, and this is a convergence action that
// runs beside both. The backlog's own accounting lives in
// manager_bucket_backlog_state.go, and the tail cap there is a different mechanism
// with a different job — it flags an earlier reconcile, which is a request for a
// FULL re-emit; this bounds the count directly and cheaply, one partition at a time.

package segmentdist

import (
	"errors"
	"log/slog"
	"slices"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// boundResidentSegments consolidates over-full partitions until the engine's
// resident segment count is back inside searchengine.ResidentSegmentFanoutBudget,
// and does nothing at all while it is already inside it.
//
// WHY IT EXISTS. Both engines are constructed merge-disabled
// (manager_factory.go), so the engine's own count-triggered merge never fires and
// the resident set falls only when a re-emit runs. A write lease seals ONE segment
// per partition it touches, so between reconcile ticks the count climbs without
// limit: a client draining a 150,000-document corpus was measured at 30,615
// resident bm25v2 segments on the keyless wiring and at 25,189 bm25v2 plus 15,586
// hnswv3 on the keyed one — 7.5x, 6.1x and 3.8x the budget. A search fans out one
// goroutine per resident segment (searchengine/engine_search.go), so that count IS
// interactive query latency while a graph is being indexed.
//
// IT IS LAZY, AND THAT IS THE INVARIANT RATHER THAN A PREFERENCE. Nothing sweeps,
// nothing ticks and nothing is scheduled: a partition is consolidated only when a
// write next TOUCHES this engine while it is over budget, which is next-touch
// convergence in its plainest form. The rejected alternative — acting on the tail
// cap's crossing by driving a full re-emit inline — is barred twice over: the write
// path must not block on a rebuild (manager_bucket_backlog_state.go), and every
// trigger of that cap is a FULL corpus re-emit by the arithmetic at the head of
// manager_bucket_backlog.go.
//
// IT IS PER PARTITION, AND THAT IS WHAT KEEPS THE MEMORY PEAK DOWN. The group form
// (ReplaceBucketGroup, used by the drain) holds every output of the group across
// ONE swap, so "peak additional residency approaches a full copy of the resident
// corpus when the group is the whole partition set" (searchengine/bucket_swap_group.go).
// That peak is exactly what the capture which opened this work measured: a 3.99 GB
// RSS spike coincident with a group rebuild unioning 4,447 segments. ReplaceBucket
// holds ONE output across its CAS and releases the constituents immediately after,
// so the peak here is one partition's consolidated output — order 1/bucketCount of
// the corpus.
//
// THE CONSTITUENCY-CLOSURE OBLIGATION IS CHECKED, NEVER ASSUMED. ReplaceBucket
// REMOVES every constituent it resolves, so a constituent holding members of
// partitions this call is not rebuilding would lose them — "the smallest group is
// the per-partition swap that loses data" (bucket_swap_group.go). Segments sealed
// before a doubling legitimately span several partitions at the current count
// (searchengine/bucket.go), and this set accumulates across many windows while the
// corpus grows, so such segments are genuinely present. SegmentSpans answers which
// is which in ONE pass over the members, and only segments spanning EXACTLY one
// partition are offered; a spanning segment is left where it is, for the drain's
// group form to consolidate.
//
// THE CENSUS IS PAID ONLY ON A CROSSING, AND A CROSSING IS MADE OCCASIONAL. The
// count check that gates it is one atomic load and a slice length; the census itself
// is SegmentSpans, which is O(live members) rather than O(segments) — the very
// O(resident) walk this mechanism took off the publish path, and putting it on the
// seal path once per batch would have relocated it rather than removed it. Two
// devices keep it rare: the consolidation runs down to a LOW-WATER mark eight write
// batches below the budget rather than stopping at it, so the next crossing is eight
// batches' worth of seals away; and a census that BOUGHT NOTHING is not re-paid until
// the count has grown by that same headroom again. Bought nothing covers two states,
// not one — the census that could take nothing at all, and the census that took every
// candidate it had and landed back on the count the LAST one left. See
// minCensusReduction: the second state's cost is the merges rather than the walk, and
// it is the more expensive of the two by two orders of magnitude.
//
// A FAILED CONSOLIDATION IS NOT A FAILED WRITE, and that is the point of the missing
// error return. The batch's segments are already published, resident and searchable
// by the time this runs; the bound is a MEMORY AND LATENCY property, not corpus
// correctness. Returning an error here would fail a write that fully succeeded and
// open a durability hole — the caller returns before recordDirty, so the batch lands
// in no backlog, is never drained to L2, and the pipeline stamps a per-node ship
// failure it does not retry. The package's own precedent is manager_search.go:130: a
// failed optimisation must not fail a good operation. The failure is logged at ERROR
// and the engine stays over budget until the next touch, which manage(status)'s
// per-format resident_segments and resident_segments_peak make visible.
//
// NOR IS IT A FAILED CENSUS. A partition whose consolidation fails is SKIPPED and
// the walk continues through the remaining candidates, because the failure is a
// property of that partition's constituents and says nothing about the others.
// Abandoning the walk there was worse than doing nothing: a partition that cannot be
// consolidated only grows, so the most-populated-first order below sorts it FIRST on
// every subsequent crossing, and one bad partition therefore prevented all 127 others
// from ever being consolidated. On the v0.10.6 release candidate that was 19
// consecutive failures on one bucket, no consolidation at all beside them, and a
// resident count of 22,474 against a budget of 4,096.
//
// AND A CORRUPT CONSTITUENT IS WITHDRAWN RATHER THAN RE-OFFERED. ReplaceBucket routes
// a corruption into the engine's own corrupt-segment reporter (searchengine's
// reportCorruptFrom), which drops the segment from the published set and hands it to
// this package's quarantine — so the partition that could not be consolidated is
// consolidated at the NEXT crossing, without it. Before that routing existed, the
// same segment was re-offered on every crossing and the excursion ended only when an
// unrelated search happened to touch the same bytes.
func boundResidentSegments[Q, S any](dm *distManager[Q, S], bucketCount int) {
	// THE HIGH-WATER IS SAMPLED HERE, BEFORE ANY CONSOLIDATION, and the position is
	// the whole point of the reading. A count read after this function returns is the
	// SUSTAINED count — what a search fans out over between write batches — and it
	// cannot see the excursion the batch's own seals just made. That excursion is
	// real memory and real fan-out for the width of one lease, so it is recorded
	// rather than left to be inferred from a number that has already been brought
	// back down.
	observed := dm.engine.ResidentSegmentCount()
	// THE REPLACEMENT GENERATION IS SNAPSHOTTED WITH THE COUNT, because the two are one
	// reading: every number this call goes on to derive is about the resident set as it
	// stands HERE, and a layer or group swap landing while the walk below runs makes all
	// of them statements about a retired set. settleCensusLatch compares this snapshot
	// against the live counter and declines to settle when they differ, so a census
	// overtaken by a replacement leaves the clear in place (distManager.replacementGen).
	gen := dm.replacementGen.Load()
	dm.recordResidentPeak(observed)
	if observed <= searchengine.ResidentSegmentFanoutBudget {
		return
	}
	if bucketCount < 1 {
		bucketCount = 1
	}
	lowWater, slack := residentLowWater(bucketCount)

	// A CENSUS THAT TOOK NOTHING IS NOT RE-PAID ON THE NEXT BATCH. When every
	// remaining candidate spans several partitions there is nothing a per-partition
	// swap may take, and the count then stays over budget — so without this the
	// crossing test would be true on EVERY subsequent batch and each would pay the
	// whole members walk to learn the same thing. It re-arms as soon as the set has
	// grown by the slack, because that much new material can contain a partition
	// worth consolidating. Which censuses arm it is settleCensusLatch's rule
	// (manager_bucket_bound_latch.go), and the count that matters is what it BOUGHT: a
	// census clears the latch only when it withdrew a segment or reduced the count by
	// at least minCensusReduction, whatever it consolidated on the way.
	//
	// THE COUNT THIS GATE CAN ADMIT IS BOUNDED, and that is settleCensusLatch's
	// clamp rather than anything here: `last` is never armed above the budget, so
	// `last + slack` is never above budget + slack. Read this line as "a census is
	// skipped only below that ceiling"; armed at whatever a failed census happened
	// to observe, the same line admitted an unbounded count.
	if last := dm.lastCensusCount.Load(); last > 0 && int64(observed) < last+int64(slack) {
		return
	}
	dm.censusCount.Add(1)

	// SegmentSpans REPORTS MEMBERSHIP, NOT LIVENESS, and the asymmetry matters here:
	// a segment whose only out-of-partition members are DELETED is still reported as
	// spanning, so it stays out of the candidate set. That is the safe direction —
	// the swap's accept predicate is what would drop them — and it is why a corpus
	// with many deletes can leave this function with nothing to do.
	byBucket := consolidatablePartitions(dm.engine.SegmentSpans(bucketCount))
	// MOST-POPULATED FIRST, ties broken by partition number. Descending size is what
	// makes the walk short — one consolidation of the fullest partition usually
	// returns the whole engine to budget — and the tie-break is what makes the
	// resulting segment order deterministic, which the L2 blob order depends on
	// (sealPerPartition states why Export's order reaches durable state).
	order := make([]int, 0, len(byBucket))
	for b := range byBucket {
		order = append(order, b)
	}
	slices.SortFunc(order, func(a, b int) int {
		if n := len(byBucket[b]) - len(byBucket[a]); n != 0 {
			return n
		}
		return a - b
	})

	consolidated, failed, withdrew := 0, 0, 0
	for _, b := range order {
		if dm.engine.ResidentSegmentCount() <= lowWater {
			break
		}
		// Empty docs and empty superseded: this consolidates the constituents ALONE,
		// which is the shape ReplaceBucket's godoc names. Nothing is written, nothing
		// is killed, and the documents these segments hold are carried into the output
		// by the merge's live-member predicate.
		if _, err := dm.engine.ReplaceBucket(b, bucketCount, byBucket[b], nil, nil); err != nil {
			// LOGGED AT ERROR AND SKIPPED — never returned, and no longer ABANDONING
			// the walk. See the paragraph above for why it is not returned; the
			// continue is the RC's own lesson. A partition that cannot be consolidated
			// only grows, so the most-populated-first sort puts it FIRST on every
			// later crossing: abandoning here let one bad partition prevent all 127
			// others from ever being offered, and a run logged 19 consecutive failures
			// on one bucket with not a single consolidation beside them while the
			// count climbed to 22,474 against a budget of 4,096.
			//
			// ONE LINE PER FAILING PARTITION PER CENSUS. The bucket is named because a
			// merge that refuses is a property of ITS constituents, so an operator
			// reading this needs to know which partition to look at.
			failed++
			// A CORRUPTION IS A CENSUS THAT CHANGED THE WORLD, and the latch is told
			// so below. ReplaceBucket withdrew the segment it named before returning
			// this error, so the partition that just failed is consolidatable NOW —
			// counting this census as one that learned nothing would defer the retry
			// by a whole slack of growth and make the header's "consolidated at the
			// NEXT crossing" false.
			var corrupt *searchengine.CorruptSegmentError
			if errors.As(err, &corrupt) {
				withdrew++
			}
			slog.Error("segmentdist: a resident-bound consolidation failed; the engine stays over its fan-out budget",
				"graph", dm.target.GetGraph(), "name", dm.target.GetName(), "repo", dm.target.GetRepo(),
				"format", dm.format, "bucket", b, "bucket_count", bucketCount,
				"constituents", len(byBucket[b]), "error", err,
				"consequence", "the write itself succeeded and its documents are searchable; this partition is skipped "+
					"and the remaining candidates are still consolidated")
			continue
		}
		consolidated++
	}

	after := dm.engine.ResidentSegmentCount()
	// THE FLOOR IS READ BEFORE THE LATCH IS SETTLED, because settling overwrites it
	// with this census's own floor. It is what turns "reduced by something" into
	// "reduced by something that had not simply arrived since the last census".
	settleCensusLatch(dm, censusOutcome{
		observed:     observed,
		after:        after,
		consolidated: consolidated,
		withdrew:     withdrew,
		minReduction: minCensusReduction(observed, int(dm.lastCensusFloor.Load()), slack),
		gen:          gen,
	})
	if consolidated > 0 {
		slog.Info("segmentdist: consolidated partitions to hold the resident segment count inside the fan-out budget",
			"graph", dm.target.GetGraph(), "name", dm.target.GetName(), "repo", dm.target.GetRepo(),
			"format", dm.format, "bucket_count", bucketCount,
			"partitions_consolidated", consolidated, "partitions_failed", failed,
			"segments_withdrawn", withdrew, "resident_segments", after,
			"low_water", lowWater, "budget", searchengine.ResidentSegmentFanoutBudget)
	}
	if after > searchengine.ResidentSegmentFanoutBudget {
		// NOT A SILENT DEGRADE: the count is still over budget and the reason is
		// nameable — every remaining candidate spans more than one partition at this
		// count, so consolidating it per partition would DROP the members it holds for
		// the partitions this call is not rebuilding. Those segments are the drain's
		// group form to consolidate, and the drain is already flagged by the tail cap.
		// Reporting it is what stops a bound that quietly stopped working from reading
		// as one that is holding.
		//
		// IT IS LATCHED BY THE CENSUS GATE ABOVE rather than by a timer: this line is
		// only reachable on a batch that actually censused, and an engine that can
		// take NOTHING censuses once per slack of growth instead of once per batch.
		// An engine that took something and still bought less than
		// minCensusReduction is latched the same way, at the count it left; one that
		// reduced by at least that censuses again at the next crossing, against the
		// lower count it just produced.
		slog.Warn("segmentdist: the resident segment count is over the fan-out budget and no partition could be consolidated alone",
			"graph", dm.target.GetGraph(), "name", dm.target.GetName(), "repo", dm.target.GetRepo(),
			"format", dm.format, "bucket_count", bucketCount, "resident_segments", after,
			"partitions_failed", failed,
			"low_water", lowWater, "budget", searchengine.ResidentSegmentFanoutBudget,
			"consequence", overBudgetConsequence(failed))
	}
}

// overBudgetConsequence names WHY the count is still over budget, because the two
// reasons call for different actions and a line that named only one of them was
// wrong half the time once a failing partition stopped ending the walk.
func overBudgetConsequence(failed int) string {
	if failed > 0 {
		return "at least one partition's consolidation FAILED (see the ERROR lines above for which and why); the rest " +
			"were still consolidated, and a partition whose constituents cannot be merged stays over budget until they are repaired"
	}
	return "these segments span several partitions and only the drain's group rebuild can consolidate them"
}

// consolidatablePartitions groups the resident segments that occupy EXACTLY ONE
// partition by the partition they occupy, and reports only the partitions a swap
// could actually REDUCE.
//
// A SEGMENT SPANNING MORE THAN ONE PARTITION IS OMITTED ENTIRELY — offering it to a
// per-partition swap is the shape that loses data (see boundResidentSegments).
//
// AND A PARTITION LEFT HOLDING ONE CANDIDATE IS OMITTED TOO, because consolidating
// it reduces nothing: the swap would merge one segment into itself, re-publish the
// content hash it already carried and remove nothing, so a caller walking the whole
// order would pay a merge per partition for no reduction at all. Dropping it here
// rather than skipping it at the call site is what makes the rule observable: the
// answer this returns IS the set of partitions a swap can reduce.
func consolidatablePartitions(spans map[searchengine.SegmentID][]int) map[int][]searchengine.SegmentID {
	byBucket := make(map[int][]searchengine.SegmentID, len(spans))
	for id, buckets := range spans {
		if len(buckets) != 1 {
			continue
		}
		byBucket[buckets[0]] = append(byBucket[buckets[0]], id)
	}
	for b, ids := range byBucket {
		if len(ids) < 2 {
			delete(byBucket, b)
		}
	}
	// SegmentSpans returns a MAP, and Go randomizes map range, so the constituent
	// order of a partition would otherwise differ run to run. The consolidated
	// segment is content-hashed over its merged contents rather than over this
	// order, but the order reaches the merge and the L2 write, and determinism there
	// is a property this package pins elsewhere.
	for b := range byBucket {
		slices.Sort(byBucket[b])
	}
	return byBucket
}
