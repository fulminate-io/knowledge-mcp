// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"maps"
	"slices"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/bm25"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/hnsw"
)

// distinctResidentDocCount exposes the engine's DISTINCT resident doc count through
// the coverageArm seam. engine is a generic FIELD, so the interface cannot reach it
// directly.
func (m *distManager[Q, S]) distinctResidentDocCount() int {
	return m.engine.DistinctResidentDocCount()
}

// residentSegmentCount exposes HOW MANY sealed segments are resident through the
// coverageArm seam. engine is a generic FIELD, so the interface cannot reach it
// directly.
func (m *distManager[Q, S]) residentSegmentCount() int {
	return m.engine.ResidentSegmentCount()
}

// quarantinedSegmentCount exposes how many segments this engine's L2 store has
// withdrawn for corruption, through the same kind of seam residentSegmentCount uses:
// cache is a FIELD on a generic struct, so an interface cannot reach it directly.
//
// A distManager constructed without a cache — the test engines that drive the
// engine directly — reports zero, which is the true reading for a manager with no
// store to withdraw from.
func (m *distManager[Q, S]) quarantinedSegmentCount() int {
	if m.cache == nil {
		return 0
	}
	return m.cache.quarantinedCount()
}

// cureQuarantinedSegments clears this engine's withdrawal record after a landed reset
// swap, through the same field-bound seam quarantinedSegmentCount reads. A manager
// constructed without a cache has no record to clear.
func (m *distManager[Q, S]) cureQuarantinedSegments() int {
	if m.cache == nil {
		return 0
	}
	return m.cache.cureQuarantined()
}

// recordResidentPeak raises this engine's high-water resident segment count to n
// when n is higher, and leaves it alone otherwise.
//
// IT IS SAMPLED ON THE SEAL PATH, which is the only place that can see the peak. The
// bound brings the count back inside the budget before the write returns, so every
// other reader in this package — the coverage arm, the rebuild driver, the L2 write
// line — samples at ITS OWN cadence and sees only what is left afterwards. A maximum
// taken from those is a maximum of the SUSTAINED count.
//
// A COMPARE-AND-SWAP LOOP rather than a plain Store: seals for one graph are serial
// today, but nothing in the type enforces that, and a lost update here would silently
// lower a high-water mark.
func (m *distManager[Q, S]) recordResidentPeak(n int) {
	for {
		cur := m.residentPeak.Load()
		if int64(n) <= cur || m.residentPeak.CompareAndSwap(cur, int64(n)) {
			return
		}
	}
}

// residentSegmentPeak reports the high-water resident segment count recorded on the
// seal path over this engine's life.
func (m *distManager[Q, S]) residentSegmentPeak() int { return int(m.residentPeak.Load()) }

// segmentSpanCensuses reports how many O(live members) span censuses the
// resident-growth bound has paid for on this engine. Pure observability.
func (m *distManager[Q, S]) segmentSpanCensuses() int64 { return m.censusCount.Load() }

// ResidentSegmentCount reports HOW MANY sealed segments one graph's engine for the
// given format currently holds. It is the PRESENT-SET operand of the rebuild
// cardinality gate: the derivation says how many partitions the corpus should
// occupy, and this says how many the engine actually holds.
//
// IT TAKES NO ctx AND RETURNS NO ERROR, and that is a real consequence of the rail
// deletion rather than a simplification. The operand it replaces was a manifest read
// BACK FROM THE SERVER — a network call that could fail, which is why its caller had
// a whole paragraph about a failed read-back not being a failed rebuild. This is one
// atomic snapshot load and a slice length: it cannot fail, so no caller needs to
// decide what a failure means.
//
// It is one atomic snapshot load and a slice length — no allocation and no walk.
func (m *Manager) ResidentSegmentCount(gt kgtypes.GraphType, name, format string) int {
	if format == bm25.New().Name() {
		return m.bm25ManagerFor(gt, name).residentSegmentCount()
	}
	return m.managerFor(gt, name).residentSegmentCount()
}

// ResidentSegmentCounts reports how many sealed segments each of a graph's ALREADY
// CONSTRUCTED engines holds, keyed by format name. It is what `manage(status)`
// renders per format, and what the release smoke reads to assert the resident set
// stayed inside the search fan-out budget.
//
// IT OBSERVES THIS MANAGER AND EVERY DESTINATION CHILD, and that widening is the
// whole correction this reading needed. A user call binds a {storage,account}
// destination and is served by a CHILD (storage.go), so every segment a keyless
// client seals lands in the {local} child's pool while the ROOT's arm maps stay
// empty for that graph forever — the field read null at one resident segment, at
// forty-two, and after a drain. A reader of one Manager's own maps is therefore
// not a reader of the engines that serve.
//
// PER FORMAT THE NUMBER IS THE MAXIMUM ACROSS DESTINATIONS, never the sum. The
// fan-out budget is per ENGINE: a search of one graph is bound to one destination
// and fans out over THAT engine's resident segments, so the maximum is the number
// the budget bounds, while a sum would report two destinations each holding half a
// budget as a breach of it. ResidentSegmentDestinations reports how many
// destinations went into each, so an aggregate is never rendered as one engine.
//
// IT CONSTRUCTS NOTHING, on the root and on every child alike, and that is the
// whole reason it exists beside ResidentSegmentCount rather than being expressed as
// two calls to it. That one resolves a format through managerFor/bm25ManagerFor,
// which LAZILY CONSTRUCT a per-graph engine and its cache directory for whatever key
// they are handed — so asking it about the vector format of a graph written only
// through the field path would create the pool it was asking about. A status READ
// that materializes state for an instance that does not exist is the defect the
// coverage probe's own declines are written to avoid, and it is what made the one
// non-null reading this field ever produced on a keyless client an EMPTY root vector
// arm the read had just built for itself.
//
// AN ABSENT FORMAT IS OMITTED RATHER THAN REPORTED AS ZERO. A graph with no engine
// for a format and a graph with an empty one are different facts, and a fabricated
// zero states a measurement nobody took. A format whose engine is still being
// constructed is omitted on the same terms: its gate has not closed, it holds
// nothing yet, and waiting on it would make a status read block on a seed.
//
// IT IS A PROJECTION OF ResidentSegmentReadings and is kept for the callers that
// want one of the three: a caller rendering more than one of them calls that
// directly, or it assembles its row out of as many independent walks as it made.
func (m *Manager) ResidentSegmentCounts(gt kgtypes.GraphType, name string) map[string]int {
	counts, _, _ := m.ResidentSegmentReadings(gt, name)
	return counts
}

// ResidentSegmentPeaks reports the HIGH-WATER resident segment count each of a
// graph's already constructed engines has been observed at, keyed by format name,
// under exactly the rules ResidentSegmentCounts reports the current count under —
// the same destination walk and the same per-format maximum across it.
//
// IT IS THE OTHER HALF OF THE PAIR, AND THE HALF AN OPERATOR CANNOT DERIVE. The
// resident-growth bound acts inside the write call, so the CURRENT count is always
// the count after it acted: it can never show the excursion a batch's own seals
// made, and a reader watching only that number would see a bound holding perfectly
// while a lease was briefly fanning a search out over far more segments. The peak is
// sampled on the seal path where that excursion happens (manager_bucket_bound.go)
// and is what makes it visible to manage(status) and to the release smoke.
// It is a projection of ResidentSegmentReadings on that method's own terms.
func (m *Manager) ResidentSegmentPeaks(gt kgtypes.GraphType, name string) map[string]int {
	_, peaks, _ := m.ResidentSegmentReadings(gt, name)
	return peaks
}

// ResidentSegmentDestinations reports HOW MANY destinations contributed to each
// format's readings above — the root and each {storage,account} child holding a
// constructed engine for this graph counting once.
//
// IT IS A DISCLOSURE TERM RATHER THAN A MEASUREMENT OF THE POOL, and it exists
// because the two readings beside it are aggregates. A count of 42 taken from one
// engine and a count of 42 that is the larger of two engines are different facts
// about a daemon, and the second read as the first would have an operator sizing a
// fan-out against a pool that is not the one their search reaches. The text render
// names it only when it exceeds one, so the single-destination cell — every
// single-account daemon's — is byte-identical to what it was before this existed.
// It is a projection of ResidentSegmentReadings on that method's own terms.
func (m *Manager) ResidentSegmentDestinations(gt kgtypes.GraphType, name string) map[string]int {
	_, _, destinations := m.ResidentSegmentReadings(gt, name)
	return destinations
}

// QuarantinedSegmentCounts reports how many segments each of a graph's ALREADY
// CONSTRUCTED engines has withdrawn from service for corruption, keyed by format
// name. It is what manage(status) renders beside the resident counts, and every
// non-zero entry is documents this client cannot search until the graph's segments
// are rebuilt (manage rebuild_segments) — nothing re-fetches or re-indexes them.
//
// IT CONSTRUCTS NOTHING, on the root and on every destination child alike, exactly
// as ResidentSegmentReadings does not: a status read that materialized an engine to
// ask whether it had lost anything would create the pool it was asking about.
//
// IT SUMS ACROSS DESTINATIONS RATHER THAN TAKING THE MAXIMUM, and that is the one
// place it departs from its neighbor. The resident count is a maximum because the
// search fan-out budget is per ENGINE and a sum would report two half-full engines
// as a breach. A withdrawal is not a budget reading: each destination has its OWN
// segment store, so a segment withdrawn in either is documents gone from that store,
// and the honest total for "what has this client lost" is the sum. A maximum would
// hide a second destination's losses behind the first's.
//
// AN ABSENT FORMAT IS OMITTED RATHER THAN REPORTED AS ZERO, on the same terms as the
// resident readings: a graph with no constructed engine for a format has not been
// measured, and a fabricated zero would state a measurement nobody took. A
// constructed engine that has withdrawn nothing DOES report zero, which is a
// measurement and reads as "nothing lost here".
func (m *Manager) QuarantinedSegmentCounts(gt kgtypes.GraphType, name string) map[string]int {
	counts := make(map[string]int, 2)
	m.foldQuarantinedCounts(gt, name, counts)
	for _, child := range m.destinationChildren() {
		child.foldQuarantinedCounts(gt, name, counts)
	}
	if len(counts) == 0 {
		return nil
	}
	return counts
}

// foldQuarantinedCounts folds ONE manager's constructed arms into the reading,
// taking that manager's lock once — the shape foldResidentSegmentReadings uses.
func (m *Manager) foldQuarantinedCounts(gt kgtypes.GraphType, name string, counts map[string]int) {
	k := graphKey{graphType: gt, graphName: name}
	m.mu.Lock()
	hnswGate, hasHNSW := m.managers[k]
	bm25Gate, hasBM25 := m.bm25Managers[k]
	m.mu.Unlock()

	if hasHNSW {
		if dm, ready := constructedArm(hnswGate); ready {
			counts[hnsw.New().Name()] += dm.quarantinedSegmentCount()
		}
	}
	if hasBM25 {
		if dm, ready := constructedArm(bm25Gate); ready {
			counts[bm25.New().Name()] += dm.quarantinedSegmentCount()
		}
	}
}

// ResidentSegmentReadings takes ALL THREE readings under one walk of the
// destinations and one resolution of each manager's arms, so the count, the peak
// and the destination count a caller renders side by side come from the same set of
// engines rather than from three independent lookups that could disagree about
// which arms exist.
//
// IT IS EXPORTED BECAUSE ITS CONSUMER RENDERS THE TRIPLE, and a promise of
// consistency that stops at a package boundary is not one. The status row carries
// all three side by side: assembled from three calls to the projections above, a
// child binding or being evicted between them yields a destination count for a
// format whose count came from a different set of arms — or a peak read off arms
// the count it qualifies was not read from. It is also three walks where one will
// do, each taking this Manager's own mutex — the lock every bindChild and
// resolveDestination on the request path takes — plus one per child, per graph row.
func (m *Manager) ResidentSegmentReadings(
	gt kgtypes.GraphType, name string,
) (counts, peaks, destinations map[string]int) {
	counts, peaks, destinations = make(map[string]int, 2), make(map[string]int, 2), make(map[string]int, 2)
	m.foldResidentSegmentReadings(gt, name, counts, peaks, destinations)
	for _, child := range m.destinationChildren() {
		child.foldResidentSegmentReadings(gt, name, counts, peaks, destinations)
	}
	if len(counts) == 0 {
		return nil, nil, nil
	}
	return counts, peaks, destinations
}

// destinationChildren snapshots this Manager's per-destination children under ONE
// acquisition of its lock, so the caller walks them holding nothing.
//
// A SNAPSHOT RATHER THAN A WALK UNDER THE LOCK, because reading a child's arms
// takes the CHILD's mutex: doing that while holding the parent's would put a lock
// order between two mutexes that bindChild and resolveDestination take
// independently, to buy nothing — a child that binds or is evicted during the walk
// is a destination whose engines this reading either includes or does not, and
// both are true statements about an instant.
//
// IT BINDS NOTHING AND EVICTS NOTHING. A status read must observe the set of
// destinations that exists, never grow it: resolving one through ForDestination
// here would create a child, and its cache directory, for whatever the caller
// happened to be bound to.
func (m *Manager) destinationChildren() []*Manager {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.storageManagers) == 0 {
		return nil
	}
	return slices.Collect(maps.Values(m.storageManagers))
}

// foldResidentSegmentReadings folds ONE manager's constructed arms into the three
// readings, taking that manager's lock once.
func (m *Manager) foldResidentSegmentReadings(
	gt kgtypes.GraphType, name string, counts, peaks, destinations map[string]int,
) {
	k := graphKey{graphType: gt, graphName: name}
	m.mu.Lock()
	hnswGate, hasHNSW := m.managers[k]
	bm25Gate, hasBM25 := m.bm25Managers[k]
	m.mu.Unlock()

	if hasHNSW {
		if dm, ready := constructedArm(hnswGate); ready {
			foldArmReading(hnsw.New().Name(), dm, counts, peaks, destinations)
		}
	}
	if hasBM25 {
		if dm, ready := constructedArm(bm25Gate); ready {
			foldArmReading(bm25.New().Name(), dm, counts, peaks, destinations)
		}
	}
}

// foldArmReading folds one engine's pair of readings into the per-format maxima and
// counts the destination that held it. It is generic because the two arms are
// distManagers over different type parameters and engine is a generic FIELD.
func foldArmReading[Q, S any](
	format string, dm *distManager[Q, S], counts, peaks, destinations map[string]int,
) {
	counts[format] = max(counts[format], dm.residentSegmentCount())
	peaks[format] = max(peaks[format], dm.residentSegmentPeak())
	destinations[format]++
}

// constructedHNSWArm resolves this Manager's vector arm for one graph ONLY IF it
// has already been constructed, and is the READ-ONLY counterpart of managerFor.
//
// IT EXISTS FOR THE READERS ON THE manage(status) ASSEMBLY PATH. managerFor
// constructs lazily, so a reader that reaches it to answer "how much is resident"
// CREATES the pool it asks about — and the per-format resident segment readings,
// taken in the same statement group, then report that fresh arm as an engine
// holding zero. A graph with no constructed arm holds nothing, so the caller
// answers 0 exactly as it would have off the arm this does not build.
func (m *Manager) constructedHNSWArm(gt kgtypes.GraphType, name string) (*distManager[[]byte, struct{}], bool) {
	m.mu.Lock()
	gate, ok := m.managers[graphKey{graphType: gt, graphName: name}]
	m.mu.Unlock()
	if !ok {
		return nil, false
	}
	return constructedArm(gate)
}

// constructedArm reports a gate's manager only once its construction has finished.
// A NON-BLOCKING read of the gate's done channel is the point: the alternative,
// receiving from it, would make a status read wait out a branch seed.
func constructedArm[Q, S any](gate *constructionGate[Q, S]) (*distManager[Q, S], bool) {
	select {
	case <-gate.done:
		return gate.dm, gate.dm != nil
	default:
		return nil, false
	}
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
