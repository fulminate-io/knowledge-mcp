// SPDX-License-Identifier: Apache-2.0

// Deterministic-path build benchmarks. The HNSW builder is deterministic
// everywhere now (the byte-reproducible serial path is the only builder), so these
// measure that path's absolute throughput + the reproducibility guarantee — there
// is no parallel arm to compare against.
//
// Run with:
//   go test ./cmd/knowledge/internal/searchengine/formats/hnsw/ \
//     -run '^$' -bench 'BenchmarkBuild|BenchmarkCrossSegment' -benchmem -benchtime 3x

package hnsw

import (
	"fmt"
	"runtime"
	"sync"
	"testing"
)

// buildSerialDeterministic is the deterministic serial build under measurement —
// the same path Format.Build runs in production.
func buildSerialDeterministic(items []binaryBuildItem) *binaryGraph {
	return buildBinaryHNSWSerialDeterministic(items, defaultVecBytes, defaultM, defaultEfConstruction)
}

// ---- Per-segment deterministic build throughput ----

var perSegmentSizes = []int{1024, 4096, 16384}

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

// ---- Across-segment concurrency: K independent ~MinSegmentDocs segments ----
//
// The realistic unit of work is K independent ~MinSegmentDocs (1024-doc) segments,
// each built by the deterministic serial builder, the K segments built CONCURRENTLY
// (one goroutine per segment). This measures the production cross-segment throughput
// shape; every segment stays byte-deterministic regardless of goroutine timing.

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

// BenchmarkCrossSegmentSerialAcross builds the K segments CONCURRENTLY (one
// goroutine per segment, GOMAXPROCS-bounded by the Go scheduler), each by the
// deterministic serial builder.
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

// TestDeterministicSerialIsReproducible proves the serial builder IS deterministic:
// two independent builds of the same corpus produce byte-identical encoded graphs.
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
