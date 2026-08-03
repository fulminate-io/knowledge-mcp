// SPDX-License-Identifier: Apache-2.0

// Bucket fan-out search benchmark: what does splitting one consolidated corpus
// into hash-partitioned segments cost per query?
//
// The question this answers was previously REASONED rather than measured. The
// existing engine benchmark that looks related — BenchmarkSearchParallel — runs
// over a mock format, so it bounds the goroutine fan-out overhead and says nothing
// about the per-segment HNSW graph traversal, which is the cost that actually
// scales with the partition count.
//
// Run with:
//   go test ./cmd/knowledge/internal/searchengine/formats/hnsw/ \
//     -run '^$' -bench '^BenchmarkBucketFanoutSearch$' -benchtime 10x -timeout 30m
//
// MEASURED RESULTS.
//
//	machine:       Apple M4 Max, darwin/arm64
//	GOMAXPROCS:    16
//	go test flags: -benchtime 10x -timeout 30m
//
//	arm            corpus   segments   run 1 ns/op   run 2 ns/op
//	------------   ------   --------   -----------   -----------
//	consolidated    16384          1        42_554        50_083
//	bucketed        16384         16       150_162       183_183
//	consolidated    98304          1        57_533        64_150
//	bucketed        98304        128       450_096       457_183
//
// PARTITIONING COSTS ROUGHLY 3.6x PER QUERY AT 16384 AND ROUGHLY 7.5x AT 98304,
// and the ratio grows with the partition count rather than with the corpus: the
// 98304 consolidated arm holds six times the documents of the 16384 one for about
// 35% more query time (one HNSW graph absorbs corpus growth logarithmically),
// while the bucketed arm pays per partition searched. That is the honest shape of
// the trade the partitioning buys its re-emit cost with, recorded rather than
// tuned away.
//
// Two runs are recorded because ten iterations is a small sample: the absolute
// numbers move up to ~20% between runs (the machine was not otherwise idle), while
// the consolidated-to-bucketed RATIO holds at 3.5-3.7x and 7.1-7.8x. Read the
// ratio as the result and the absolute figures as the scale.
//
// The segment counts are recorded because they are the whole point of the
// comparison and are easy to get wrong: the bucketed arm runs at
// searchengine.BucketCountFor(corpus), which ROUNDS UP to a power of two, so at
// 98304 it is 128 — not the 96 that corpus/DefaultMinSegmentDocs would suggest.
// Measuring 96 would understate the fan-out this benchmark exists to size. At
// 16384 the two happen to agree at 16, which is exactly why hand-computing looks
// correct in the smaller arm and is wrong only in the arm that represents the real
// corpus. Both counts are asserted at run time, and each arm reports the count it
// actually ran with as a "segs" metric beside its ns/op.

package hnsw

import (
	"fmt"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// fanoutCorpusSizes are the two corpus sizes swept. 98304 approximates the
// ~97k vectored nodes of a real knowledge graph — the corpus the partitioning
// exists to serve — and 16384 is the smaller arm that shows how the cost scales.
var fanoutCorpusSizes = []int{16384, 98304}

// fanoutQueries is how many distinct query vectors each arm cycles through, so a
// measured iteration is not repeatedly hitting one warm path.
const fanoutQueries = 64

// fanoutTopK is the result count every measured Search asks for.
const fanoutTopK = 10

// newFanoutEngine builds an engine over the production HNSW format with the
// BACKGROUND MERGE TRIGGERS DISARMED. That is load-bearing for the bucketed arm:
// left armed, the count-target trigger would consolidate the partitions before the
// timer even starts and the arm would silently measure the consolidated shape.
func newFanoutEngine(minSegmentDocs int) *searchengine.SegmentedIndex[[]byte, struct{}] {
	return searchengine.New[[]byte, struct{}](New(), searchengine.Options{
		MinSegmentDocs:     minSegmentDocs,
		SegmentCountTarget: searchengine.MergeDisabledCountTarget,
		DeletesPctAllowed:  searchengine.MergeDisabledDeadRatio,
	})
}

// buildConsolidated puts the whole corpus in ONE sealed segment — the shape the
// partitioning replaced, and the baseline the bucketed arm is measured against.
func buildConsolidated(b *testing.B, docs []searchengine.Document) *searchengine.SegmentedIndex[[]byte, struct{}] {
	b.Helper()
	eng := newFanoutEngine(len(docs))
	if err := eng.Add(docs); err != nil {
		b.Fatalf("consolidated Add: %v", err)
	}
	if err := eng.Flush(); err != nil {
		b.Fatalf("consolidated Flush: %v", err)
	}
	return eng
}

// buildBucketed splits the SAME corpus into exactly searchengine.BucketCountFor(n)
// segments, one per hash partition, by adding each partition and force-sealing it.
// The seal threshold is set above the corpus so only the explicit Flush seals —
// otherwise a large partition would seal mid-add and split in two.
func buildBucketed(b *testing.B, docs []searchengine.Document, bucketCount int) *searchengine.SegmentedIndex[[]byte, struct{}] {
	b.Helper()
	groups := make([][]searchengine.Document, bucketCount)
	for _, d := range docs {
		bucket := searchengine.BucketOf(d.ID, bucketCount)
		groups[bucket] = append(groups[bucket], d)
	}

	eng := newFanoutEngine(len(docs) + 1)
	for i, group := range groups {
		if len(group) == 0 {
			b.Fatalf("partition %d is empty — the corpus no longer spans every partition, so this arm would run at the wrong segment count", i)
		}
		if err := eng.Add(group); err != nil {
			b.Fatalf("bucketed Add[%d]: %v", i, err)
		}
		if err := eng.Flush(); err != nil {
			b.Fatalf("bucketed Flush[%d]: %v", i, err)
		}
	}
	return eng
}

// runFanoutArm measures Search over an already-built engine. Construction is
// excluded by ResetTimer, so the number is query cost alone; the segment count the
// arm actually ran with is reported alongside it so a reader never has to trust
// that the arm was shaped as its name claims.
func runFanoutArm(b *testing.B, eng *searchengine.SegmentedIndex[[]byte, struct{}], wantSegments int) {
	segments := eng.Metrics().SegmentCount
	if segments != wantSegments {
		b.Fatalf("arm ran at %d segments, want %d — the measurement would not be the one this benchmark claims to make", segments, wantSegments)
	}
	queries := vecDocsSeed(fanoutQueries, 0x9E3779B97F4A7C15, 0xBF58476D1CE4E5B9, "q-")

	b.ResetTimer()
	for i := 0; b.Loop(); i++ {
		hits := eng.Search(queries[i%len(queries)].Vector, fanoutTopK)
		if len(hits) == 0 {
			b.Fatal("search returned no hits — the arm is not searching the corpus it built")
		}
	}
	b.StopTimer()
	b.ReportMetric(float64(segments), "segs")
}

// BenchmarkBucketFanoutSearch compares per-query cost between one consolidated
// segment and the hash partitions that replaced it, over the same corpus, at two
// corpus sizes.
//
// THE MEASUREMENT IS THE DELIVERABLE. If the bucketed arm proves materially slower
// at the larger corpus, that is the finding — record it, do not tune the partition
// count until the number reads better.
func BenchmarkBucketFanoutSearch(b *testing.B) {
	for _, corpus := range fanoutCorpusSizes {
		docs := vecDocs(corpus)
		// CALL the partition function; do not hand-compute corpus/DefaultMinSegmentDocs.
		// The two disagree at 98304 (128 vs 96), and 96 is a segment count production
		// never produces.
		bucketCount := searchengine.BucketCountFor(corpus)

		b.Run(fmt.Sprintf("consolidated/%d", corpus), func(b *testing.B) {
			eng := buildConsolidated(b, docs)
			defer eng.Close()
			runFanoutArm(b, eng, 1)
		})

		b.Run(fmt.Sprintf("bucketed/%d", corpus), func(b *testing.B) {
			eng := buildBucketed(b, docs, bucketCount)
			defer eng.Close()
			runFanoutArm(b, eng, bucketCount)
		})
	}
}
