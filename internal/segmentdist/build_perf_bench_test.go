// SPDX-License-Identifier: Apache-2.0

// Deterministic-path live-ship perf benchmark. Drives the REAL write path —
// Manager.AddAndMarkDirty (engine.Add + force-seal) followed by ReEmitDirtyBuckets
// (Format.Build with the deterministic serial builder → Encode → content-hash →
// ship) — across K concurrent segments, so the reported ns/op + alloc/op reflect the
// production embed-writeback cost. Both halves are timed together because together
// they are what one corpus costs; the split between them is a scheduling question,
// not a cost question. There is one builder now (deterministic), so this is
// single-arm: absolute numbers to track allocation/throughput progress, not an
// A-vs-B comparison.
//
// Run with:
//   CGO_ENABLED=1 go test ./cmd/knowledge/internal/segmentdist/ \
//     -run '^$' -bench 'BenchmarkDeterministicMarkDirtyAndReEmit' -benchmem -benchtime 5x

package segmentdist

import (
	"context"
	"fmt"
	"math/rand/v2"
	"sync"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// perfCorpusDocs sizes one corpus so the write path derives MORE THAN ONE PARTITION
// from it, which is what makes this benchmark able to see the per-seal fixed cost at
// all. BucketCountFor(1024) is 1 — at the previous size the write path's
// bucketCount<=1 early return fired and the benchmark measured the single-seal path
// whatever the seal did, producing a confident zero delta. 5000 derives
// ceil(5000/DefaultMinSegmentDocs)=5, so the count is 8.
//
// IT IS A NAMED CONSTANT SO THE DERIVED COUNT STAYS RE-DERIVABLE. Anyone resizing it
// gets a different partition count from BucketCountFor rather than a stale pinned 8.
const perfCorpusDocs = 5000

var perfK = []int{8, 32, 64, 128}

// perfCorpus builds one distinct perfCorpusDocs-document vector corpus for graph
// index i.
func perfCorpus(i int) []searchengine.Document {
	rng := rand.New(rand.NewPCG(uint64(0x9000+i), uint64(0xA000+i)))
	docs := make([]searchengine.Document, perfCorpusDocs)
	for j := range docs {
		v := make([]byte, 32)
		for b := range v {
			v[b] = byte(rng.UintN(256))
		}
		docs[j] = searchengine.Document{ID: fmt.Sprintf("g%d-n%d", i, j), Vector: v}
	}
	return docs
}

func makePerfCorpora(k int) [][]searchengine.Document {
	corpora := make([][]searchengine.Document, k)
	for i := range corpora {
		corpora[i] = perfCorpus(i)
	}
	return corpora
}

// perfFieldCorpus builds one distinct perfCorpusDocs-document BM25 field corpus for
// graph index i — the text-side analog of perfCorpus. Each doc carries a unique symbol-name
// term plus a shared-vocabulary summary body, so postings/docFreq churn during
// build reflects the production embed-writeback shape (mirrors bm25FieldDocs).
func perfFieldCorpus(i int) []searchengine.Document {
	rng := rand.New(rand.NewPCG(uint64(0xB000+i), uint64(0xC000+i)))
	vocab := make([]string, 256)
	for v := range vocab {
		vocab[v] = fmt.Sprintf("term%04d", v)
	}
	docs := make([]searchengine.Document, perfCorpusDocs)
	for j := range docs {
		summary := ""
		for range 12 {
			summary += vocab[rng.IntN(len(vocab))] + " "
		}
		docs[j] = searchengine.Document{
			ID: fmt.Sprintf("g%d-n%d", i, j),
			Fields: map[string]string{
				searchengine.FieldSymbolName: fmt.Sprintf("g%d-uniqueterm%d", i, j),
				searchengine.FieldSummary:    summary,
			},
		}
	}
	return docs
}

func makePerfFieldCorpora(k int) [][]searchengine.Document {
	corpora := make([][]searchengine.Document, k)
	for i := range corpora {
		corpora[i] = perfFieldCorpus(i)
	}
	return corpora
}

// BenchmarkDeterministicMarkDirtyAndReEmit writes and re-emits K distinct
// perfCorpusDocs-document corpora CONCURRENTLY (one goroutine per corpus, each a
// distinct graph), each built by the deterministic serial builder. This is the
// production cross-segment shape.
//
// The errors are collected rather than asserted with require, because a require
// failure calls FailNow, which is only valid on the goroutine running the benchmark.
func BenchmarkDeterministicMarkDirtyAndReEmit(b *testing.B) {
	ctx := context.Background()
	for _, k := range perfK {
		corpora := makePerfCorpora(k)
		b.Run(fmt.Sprintf("K=%d", k), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {

				mgr := NewManager(b.TempDir(), 0)
				var wg sync.WaitGroup
				wg.Add(len(corpora))
				var mu sync.Mutex
				var firstErr error
				for i, docs := range corpora {
					go func(idx int, d []searchengine.Document) {
						defer wg.Done()
						name := fmt.Sprintf("g%d", idx)
						err := mgr.AddAndMarkDirty(ctx, kgtypes.GraphKnowledge, name, d)
						if err == nil {
							err = mgr.ReEmitDirtyBuckets(ctx, kgtypes.GraphKnowledge, name)
						}
						if err != nil {
							mu.Lock()
							if firstErr == nil {
								firstErr = err
							}
							mu.Unlock()
						}
					}(i, docs)
				}
				wg.Wait()
				// Closed per ITERATION, not registered as a cleanup: a cleanup would
				// hold every iteration's engines open for the whole b.N run, and the
				// merger goroutines they keep alive are exactly what this benchmark
				// must not be measuring against a growing background.
				mgr.Close()
				if firstErr != nil {
					b.Fatalf("mark-dirty + re-emit: %v", firstErr)
				}
			}
		})
	}
}

// BenchmarkDeterministicMarkDirtyAndReEmitFields is the BM25 analog of
// BenchmarkDeterministicMarkDirtyAndReEmit: it writes and re-emits K distinct
// perfCorpusDocs-document field corpora CONCURRENTLY (one goroutine per corpus, each
// a distinct graph), driving the REAL write path — AddAndMarkDirtyFields (engine.Add +
// force-seal) then ReEmitDirtyBuckets (bm25.Build → Encode → content-hash → ship).
// The reported allocs/op + alloc/op reflect the BM25 build+Encode cost on the
// production embed-writeback path. Single-arm absolute numbers (BM25 has one engine
// per graph, no deterministic sibling).
//
// Run with:
//
//	go test ./cmd/knowledge/internal/segmentdist/ \
//	  -run '^$' -bench 'BenchmarkDeterministicMarkDirtyAndReEmitFields' -benchmem -benchtime 5x
func BenchmarkDeterministicMarkDirtyAndReEmitFields(b *testing.B) {
	ctx := context.Background()
	for _, k := range perfK {
		corpora := makePerfFieldCorpora(k)
		b.Run(fmt.Sprintf("K=%d", k), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {

				mgr := NewManager(b.TempDir(), 0)
				var wg sync.WaitGroup
				wg.Add(len(corpora))
				var mu sync.Mutex
				var firstErr error
				for i, docs := range corpora {
					go func(idx int, d []searchengine.Document) {
						defer wg.Done()
						name := fmt.Sprintf("g%d", idx)
						err := mgr.AddAndMarkDirtyFields(ctx, kgtypes.GraphKnowledge, name, d)
						if err == nil {
							err = mgr.ReEmitDirtyBuckets(ctx, kgtypes.GraphKnowledge, name)
						}
						if err != nil {
							mu.Lock()
							if firstErr == nil {
								firstErr = err
							}
							mu.Unlock()
						}
					}(i, docs)
				}
				wg.Wait()
				// Per-iteration close, for the reason the vector benchmark above states.
				mgr.Close()
				if firstErr != nil {
					b.Fatalf("mark-dirty + re-emit fields: %v", firstErr)
				}
			}
		})
	}
}
