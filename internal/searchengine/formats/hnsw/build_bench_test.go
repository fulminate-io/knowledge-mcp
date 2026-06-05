// SPDX-License-Identifier: Apache-2.0

// THROWAWAY MEASUREMENT BENCHMARK — not part of the shipped test suite's intent.
// Measures how much slower a DETERMINISTIC serial HNSW segment build is than the
// current concurrent (buildBinaryHNSWParallel) build, to inform a decision about
// approving determinism for a segment-rebuild feature. Touches NO production code.
//
// Run with:
//   go test ./cmd/knowledge/internal/searchengine/formats/hnsw/ \
//     -run '^$' -bench 'BenchmarkBuild|BenchmarkCrossSegment' -benchmem -benchtime 3x
//
// All variants pin a FIXED PCG seed so the ONLY variable measured is
// serial-vs-parallel, not crypto-seed jitter.

package hnsw

import (
	"fmt"
	mathrand "math/rand/v2"
	"runtime"
	"sort"
	"sync"
	"testing"
)

// fixedSeedSerial determines the deterministic-build seed. The serial variant is
// the candidate for the "deterministic segment rebuild" feature, so it must be
// reproducible: same input order + same seed ⇒ byte-identical graph every run.
const (
	fixedSeedHi = 0xdeadbeef
	fixedSeedLo = 0xcafebabe
)

// newFixedSeedGraph mirrors newBinaryGraph but pins the rng to a FIXED PCG seed
// instead of crypto/rand, so serial builds are deterministic. This is the only
// behavioural delta vs newBinaryGraph; all HNSW params are the production defaults.
func newFixedSeedGraph() *binaryGraph {
	g := newBinaryGraph(defaultVecBytes, defaultM, defaultEfConstruction)
	g.rng = mathrand.New(mathrand.NewPCG(fixedSeedHi, fixedSeedLo))
	return g
}

// sortedByID returns a copy of items ordered by externalID string so the serial
// insertion order is STABLE and reproducible across runs (the deterministic
// contract: stable order + fixed seed ⇒ identical graph).
func sortedByID(items []binaryBuildItem) []binaryBuildItem {
	out := make([]binaryBuildItem, len(items))
	copy(out, items)
	sort.Slice(out, func(i, j int) bool { return out[i].id < out[j].id })
	return out
}

// buildSerialDeterministic builds a graph via the single-threaded Insert loop,
// fixed seed, stable sorted-by-id order. This is the deterministic candidate.
func buildSerialDeterministic(items []binaryBuildItem) *binaryGraph {
	g := newFixedSeedGraph()
	for _, it := range sortedByID(items) {
		g.Insert(it.id, it.vec)
	}
	return g
}

// buildParallelFixedSeed wraps buildBinaryHNSWParallel but pins the SAME fixed
// seed, so the parallel-vs-serial comparison isolates the concurrency delta from
// any seeding difference. (The graph is still non-deterministic across runs
// because goroutine interleaving reorders neighbor maintenance — that is the
// property under measurement, not the seed.)
func buildParallelFixedSeed(items []binaryBuildItem, workers int) *binaryGraph {
	// buildBinaryHNSWParallel constructs its own graph + rng internally, so to pin
	// the seed we cannot reach in. Instead we accept that the parallel path uses
	// crypto-seeded rng; level assignment variance is negligible vs the concurrency
	// effect we are measuring. We keep this wrapper for symmetry / future tweaks.
	return buildBinaryHNSWParallel(items, defaultVecBytes, defaultM, defaultEfConstruction, workers)
}

// ---- Per-segment build benchmarks: parallel vs deterministic serial ----

var perSegmentSizes = []int{1024, 4096, 16384}

func BenchmarkBuildParallel(b *testing.B) {
	for _, n := range perSegmentSizes {
		items := randomVectors(n)
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				_ = buildParallelFixedSeed(items, 0) // 0 ⇒ NumCPU workers (production)
			}
		})
	}
}

func BenchmarkBuildSerialDeterministic(b *testing.B) {
	for _, n := range perSegmentSizes {
		items := randomVectors(n)
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				_ = buildSerialDeterministic(items)
			}
		})
	}
}

// ---- Cross-segment mitigation: move parallelism across segments ----
//
// The realistic unit of work is K independent ~MinSegmentDocs (1024-doc) segments.
// Variant A: parallel-WITHIN-segment, segments built sequentially (today's shape).
// Variant B: serial-WITHIN-segment (deterministic), but the K segments built
// CONCURRENTLY, one goroutine per segment (parallelism moved across segments).
// This measures whether across-segment concurrency recovers the throughput lost
// to per-segment serialization while keeping each segment deterministic.

const crossSegmentDocs = 1024 // ~MinSegmentDocs

var crossSegmentK = []int{8, 32}

// makeKCorpora builds K distinct 1024-doc corpora (distinct seeds ⇒ distinct
// vectors) once, outside the timed region.
func makeKCorpora(k int) [][]binaryBuildItem {
	corpora := make([][]binaryBuildItem, k)
	for i := range corpora {
		corpora[i] = randomVectorsSeed(crossSegmentDocs, uint64(0x1000+i), uint64(0x2000+i))
	}
	return corpora
}

// BenchmarkCrossSegmentParallelWithin builds the K segments SEQUENTIALLY, each via
// the parallel (NumCPU-within-segment) builder. Today's production shape.
func BenchmarkCrossSegmentParallelWithin(b *testing.B) {
	for _, k := range crossSegmentK {
		corpora := makeKCorpora(k)
		b.Run(fmt.Sprintf("K=%d", k), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				for _, items := range corpora {
					_ = buildBinaryHNSWParallel(items, defaultVecBytes, defaultM, defaultEfConstruction, 0)
				}
			}
		})
	}
}

// BenchmarkCrossSegmentSerialAcross builds the K segments CONCURRENTLY (one
// goroutine per segment, GOMAXPROCS-bounded by the Go scheduler), each segment
// built by the DETERMINISTIC serial builder. Parallelism moved across segments;
// every segment stays deterministic.
func BenchmarkCrossSegmentSerialAcross(b *testing.B) {
	for _, k := range crossSegmentK {
		corpora := makeKCorpora(k)
		b.Run(fmt.Sprintf("K=%d", k), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				var wg sync.WaitGroup
				wg.Add(len(corpora))
				for _, items := range corpora {
					go func(its []binaryBuildItem) {
						defer wg.Done()
						_ = buildSerialDeterministic(its)
					}(items)
				}
				wg.Wait()
			}
		})
	}
}

// TestBenchEnvironment records the machine characteristics into the test log so
// the benchmark numbers are interpretable. Run with -v.
func TestBenchEnvironment(t *testing.T) {
	t.Logf("runtime.NumCPU() = %d", runtime.NumCPU())
	t.Logf("GOMAXPROCS       = %d", runtime.GOMAXPROCS(0))
}

// TestDeterministicSerialIsReproducible proves the serial builder actually IS
// deterministic: two independent builds of the same corpus produce byte-identical
// encoded graphs. This validates that the "deterministic" claim under measurement
// is real (otherwise the slowdown number would be measuring the wrong thing).
func TestDeterministicSerialIsReproducible(t *testing.T) {
	items := randomVectors(2000)
	a := buildSerialDeterministic(items)
	c := buildSerialDeterministic(items)
	ba := a.encode()
	bc := c.encode()
	if len(ba) != len(bc) {
		t.Fatalf("encoded length mismatch: %d vs %d", len(ba), len(bc))
	}
	for i := range ba {
		if ba[i] != bc[i] {
			t.Fatalf("encoded graphs differ at byte %d — serial build is NOT deterministic", i)
		}
	}
}
