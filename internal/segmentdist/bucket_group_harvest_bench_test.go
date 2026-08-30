// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"runtime"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/hnsw"
)

// bucket_group_harvest_bench_test.go is the perf evidence for the parallel bucket
// harvest: the same ReplaceBucketGroup call timed with the scheduler pinned to one
// P and then to every P.
//
// PACKAGE segmentdist, chosen because this package ALREADY imports formats/hnsw in
// production (advice.go), so the benchmark drives a real hnsw-backed
// searchengine.SegmentedIndex with no new dependency edge and no import cycle.
//
// BOTH ARMS RUN THE SAME PRODUCTION CODE. There is no serial code path to select;
// GOMAXPROCS(1) serializes the pool's goroutines and GOMAXPROCS(NumCPU) overlaps
// them. runtime.NumCPU() is unaffected by GOMAXPROCS, so both arms build the same
// min(NumCPU, len(work)) workers and only their overlap differs.
//
// IT DOES NOT RIDE make test. go test runs no benchmarks without -bench, so
// nothing is needed to keep it out — stated so nobody wraps it in a t.Run.

const (
	// benchPartitions x benchDocsPerPartition MATCHES THE ARM THE TICKET PRICES
	// WITH (32 partitions of ~MinSegmentDocs each), so the number here is directly
	// comparable to the measured 415.5ms-per-1024-doc serial build rather than
	// being a new unit.
	benchPartitions       = 32
	benchDocsPerPartition = 1024
)

// harvestGroupWork builds the group's per-partition work OUTSIDE any timed region:
// benchPartitions partitions, each carrying its own distinct 1024-document share.
//
// The group deliberately has NO resident constituents. The subject is the HARVEST
// — the per-partition Build+Merge that dominates the call and that step 2.1
// parallelized — and adding constituents would multiply every partition's read of
// them into the measurement without changing which code is under test.
func harvestGroupWork() []searchengine.BucketWork {
	work := make([]searchengine.BucketWork, benchPartitions)
	for p := range work {
		docs := vecContentDocsSeed(benchDocsPerPartition, (p+1)*1_000_000)
		work[p] = searchengine.BucketWork{Bucket: p, Docs: docs}
	}
	return work
}

// harvestBenchEngine returns a fresh engine with the background merge triggers
// disarmed, exactly as the production bucket-partitioned owner constructs them, so
// nothing consolidates across bucket boundaries mid-measurement.
func harvestBenchEngine() *searchengine.SegmentedIndex[[]byte, struct{}] {
	return searchengine.New[[]byte, struct{}](hnsw.New(), searchengine.Options{
		MinSegmentDocs:     1 << 20,
		DeletesPctAllowed:  searchengine.MergeDisabledDeadRatio,
		SegmentCountTarget: searchengine.MergeDisabledCountTarget,
	})
}

// BenchmarkBucketGroupHarvest times ONE ReplaceBucketGroup over a 32-partition
// group, once with the scheduler pinned to a single P and once with every P.
//
// The sub-benchmark names `serial` and `parallel` are parsed verbatim by this
// step's criterion, which asserts serial ns/op >= 2x parallel ns/op.
//
// THE THRESHOLD IS 2x RATHER THAN THE ~11.7x MEASURED ON A 16-CPU MACHINE, and
// that is deliberate: a criterion runs on whatever machine the implementer and the
// reviewer have. 2x is reachable on any box with two usable cores and is
// UNREACHABLE by the unconverted serial loop, where both arms do identical work
// and the ratio is ~1.0. Pinning 10x would make the gate a property of the
// hardware rather than of the change.
func BenchmarkBucketGroupHarvest(b *testing.B) {
	work := harvestGroupWork()

	arm := func(b *testing.B, procs int) {
		prior := runtime.GOMAXPROCS(procs)
		defer runtime.GOMAXPROCS(prior)

		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			// Engine construction is setup, not subject — the timer covers the group
			// swap alone, matching makeKCorpora's discipline in the hnsw build
			// benchmark.
			b.StopTimer()
			eng := harvestBenchEngine()
			b.StartTimer()

			published, _, err := eng.ReplaceBucketGroup(benchPartitions, nil, work)

			b.StopTimer()
			if err != nil {
				eng.Close()
				b.Fatalf("ReplaceBucketGroup: %v", err)
			}
			// A run that published nothing would time an empty call and report a
			// meaningless ratio, so the measurement asserts it did the work.
			if len(published) != benchPartitions {
				eng.Close()
				b.Fatalf("published %d partitions, want %d — the benchmark measured a call that did not harvest", len(published), benchPartitions)
			}
			eng.Close()
			b.StartTimer()
		}
	}

	b.Run("serial", func(b *testing.B) { arm(b, 1) })
	b.Run("parallel", func(b *testing.B) { arm(b, runtime.NumCPU()) })
}
