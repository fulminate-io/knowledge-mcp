// SPDX-License-Identifier: Apache-2.0

// publish_residency_test.go — the two rows about what publication COSTS the
// process over time rather than per call (GitHub issue #172, requirements R7 and R4).
//
// T4/R7: publication is CHURN, not retention — retained heap after a quiescent GC
// tracks the corpus, not the number of publishes. That already held before the
// route change (the whole-map copy was garbage the moment the next snapshot
// replaced it), so this is a KEEP-GREEN pin: it exists to redden if the two-level
// route's shared base ever turns a publish into a retention.
//
// The second row is what the resident-segment cap is FOR: search fans out one
// goroutine per resident segment, so the resident count is what a query pays
// during indexing. It asserts the FAN-OUT COUNT, which is deterministic; the
// latency readings that count stands in for live in the benchmarks next door.

package searchengine

import (
	"fmt"
	"runtime"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestPublicationIsChurnNotRetention is R7.
//
// THE GATE IS A RATIO AGAINST THE CORPUS, not an absolute byte count, because the
// absolute is allocator behaviour. Retained heap is read at two corpus sizes that
// differ by 8x while the PUBLISH count differs by far more, so a snapshot chain
// that retained its predecessors would show heap tracking publishes and fail.
func TestPublicationIsChurnNotRetention(t *testing.T) {
	const (
		smallCorpus = 13000
		largeCorpus = 104000
		batch       = 500
		// perDocSlackFactor is how far above the small corpus's own bytes-per-document
		// reading the large corpus may land. Retention that tracked PUBLISHES rather
		// than documents would multiply this by the publish ratio, which is 8x here.
		perDocSlackFactor = 2.0
	)

	e := publishCostEngine(t)
	published := 0
	grow := func(from, to int) {
		for i := from; i < to; i += batch {
			docs := make([]Document, 0, batch)
			for j := i; j < min(i+batch, to); j++ {
				docs = append(docs, doc(fmt.Sprintf("churn%08d", j), "x"))
			}
			_, err := e.AddSealAndSupersede(docs)
			require.NoError(t, err)
			published++
		}
	}

	grow(0, smallCorpus)
	smallPublishes := published
	smallHeap := quiescentHeapInuse()

	grow(smallCorpus, largeCorpus)
	largeHeap := quiescentHeapInuse()

	smallPerDoc := float64(smallHeap) / float64(smallCorpus)
	largePerDoc := float64(largeHeap) / float64(largeCorpus)
	t.Logf("retained heap: %d docs / %d publishes → %d B (%.1f B/doc); %d docs / %d publishes → %d B (%.1f B/doc)",
		smallCorpus, smallPublishes, smallHeap, smallPerDoc, largeCorpus, published, largeHeap, largePerDoc)

	require.Positive(t, smallHeap, "an instrument reading zero retained heap cannot tell churn from retention")
	require.LessOrEqual(t, largePerDoc, smallPerDoc*perDocSlackFactor,
		"retained heap per document grew from %.1f B to %.1f B across %d publishes; publication is retaining its snapshots instead of releasing them",
		smallPerDoc, largePerDoc, published)
}

// quiescentHeapInuse is retained heap after the collector has run twice — once to
// collect, once to finish what the first cycle's finalizers freed.
func quiescentHeapInuse() uint64 {
	runtime.GC()
	runtime.GC()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return ms.HeapInuse
}

// countingFanoutSegment is a mock segment that COUNTS the searches routed to it.
type countingFanoutSegment struct {
	*mockSegment
	calls *atomic.Int64
}

// Search records the invocation and answers exactly as the mock does.
func (c *countingFanoutSegment) Search(q mockQuery, st mockStats, k int, accept func(ExternalID) bool) []Hit {
	c.calls.Add(1)
	return c.mockSegment.Search(q, st, k, accept)
}

// countingFanoutFormat is mockFormat whose segments count their searches.
type countingFanoutFormat struct {
	mockFormat
	calls *atomic.Int64
}

func (f countingFanoutFormat) Build(docs []Document) (Segment[mockQuery, mockStats], BuildReport, error) {
	seg, rep, err := f.mockFormat.Build(docs)
	if err != nil {
		return nil, rep, err
	}
	return &countingFanoutSegment{mockSegment: seg.(*mockSegment), calls: f.calls}, rep, nil
}

// AggregateStats unwraps the counting segment. The embedded mock's own
// implementation type-asserts to *mockSegment and would panic on the wrapper.
func (f countingFanoutFormat) AggregateStats(segs []Segment[mockQuery, mockStats]) mockStats {
	total := 0
	for _, s := range segs {
		total += len(s.(*countingFanoutSegment).rows)
	}
	return mockStats{totalDocs: total}
}

// AppendStats unwraps the counting segment for the same reason AggregateStats
// does, and agrees with it: the total after one append is the previous total plus
// that segment's rows.
func (f countingFanoutFormat) AppendStats(prev mockStats, seg Segment[mockQuery, mockStats]) mockStats {
	return mockStats{totalDocs: prev.totalDocs + len(seg.(*countingFanoutSegment).rows)}
}

// TestSearchFansOutOncePerResidentSegment is what the resident-segment cap exists
// to bound, asserted as the DETERMINISTIC observable rather than as a clock.
//
// WHY NOT A LATENCY RATIO. This row used to time two searches — one at the
// fan-out budget and one at four times it — and require their p50 ratio to exceed
// a floor. That is a quotient of two loaded-machine wall-clock medians taken
// minutes apart, with the noisier term in the DENOMINATOR, so noise drives it red:
// compliant readings measured across a GOMAXPROCS sweep on one machine spanned
// 1.93x to 3.47x, and main's CI failed the same assertion at 2.06x and 2.41x on
// two different runner classes. The corpus is fixed and the fan-out is gated by a
// NumCPU semaphore, so core count largely cancels in the ratio; what does not
// cancel is contention landing asymmetrically on two medians.
//
// WHAT THE CLOCK STOOD IN FOR is exactly this: SearchAccepting invokes
// entry.payload.Search ONCE PER RESIDENT SEGMENT, so the resident count IS the
// per-query goroutine count the cap bounds. That is hardware-independent and has
// no distribution at all. The wall-clock readings now live in this package's
// benchmarks (engine_bench_test.go), recorded per machine rather than gated on.
func TestSearchFansOutOncePerResidentSegment(t *testing.T) {
	const corpus = 104448
	for _, segments := range []int{ResidentSegmentFanoutBudget, 4 * ResidentSegmentFanoutBudget} {
		t.Run(fmt.Sprintf("resident_segments_%d", segments), func(t *testing.T) {
			calls := &atomic.Int64{}
			e := seedFanoutCorpus(t, calls, corpus, segments)
			require.Len(t, e.set.Load().entries, segments,
				"the fixture must reach the resident segment count whose fan-out it is asserting")

			calls.Store(0)
			hits := e.Search(mockQuery{term: "needle"}, 10)
			require.NotEmpty(t, hits,
				"a search returning nothing would make the invocation count a reading of an empty fan-out")

			require.Equal(t, int64(segments), calls.Load(),
				"the search must reach every resident segment exactly once: %d resident segments, %d payload searches. "+
					"Fewer means a segment's documents are unreachable; more means one is scanned twice",
				segments, calls.Load())
		})
	}
}

// seedFanoutCorpus fills one engine with exactly `segments` resident segments over
// a fixed corpus, through the same merge-disabled options production builds with.
func seedFanoutCorpus(t *testing.T, calls *atomic.Int64, corpus, segments int) *SegmentedIndex[mockQuery, mockStats] {
	t.Helper()
	e := closeOnCleanup(t, New[mockQuery, mockStats](countingFanoutFormat{calls: calls}, Options{
		MinSegmentDocs:     1,
		DeletesPctAllowed:  MergeDisabledDeadRatio,
		SegmentCountTarget: MergeDisabledCountTarget,
	}))
	per := corpus / segments
	require.Positive(t, per, "the corpus must divide into the segment count")
	for s := range segments {
		docs := make([]Document, 0, per)
		for j := range per {
			docs = append(docs, doc(fmt.Sprintf("fanout%08d", s*per+j), "needle"))
		}
		_, err := e.AddSealAndSupersede(docs)
		require.NoError(t, err)
	}
	return e
}
