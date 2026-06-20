// SPDX-License-Identifier: Apache-2.0

// Deterministic-path live-ship perf benchmark. Drives the REAL ship path —
// Manager.AddAndShip → engine.Add/seal → Format.Build (the deterministic serial
// builder) → Encode → content-hash → ship — across K concurrent segments, so the
// reported ns/op + alloc/op reflect the production embed-writeback cost. There is
// one builder now (deterministic), so this is single-arm: absolute numbers to track
// allocation/throughput progress, not an A-vs-B comparison.
//
// Run with:
//   CGO_ENABLED=1 go test ./cmd/knowledge/internal/segmentdist/ \
//     -run '^$' -bench 'BenchmarkDeterministicAddAndShip' -benchmem -benchtime 5x

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

const perfCorpusDocs = 1024 // == MinSegmentDocs: each AddAndShip seals exactly one segment.

var perfK = []int{8, 32, 64, 128}

// perfCorpus builds one distinct 1024-doc vector corpus for graph index i.
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

// perfFieldCorpus builds one distinct 1024-doc BM25 field corpus for graph index
// i — the text-side analog of perfCorpus. Each doc carries a unique symbol-name
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

// BenchmarkDeterministicAddAndShip AddAndShips K distinct 1024-doc corpora
// CONCURRENTLY (one goroutine per call, each a distinct graph), each built by the
// deterministic serial builder. This is the production cross-segment shape.
func BenchmarkDeterministicAddAndShip(b *testing.B) {
	ctx := context.Background()
	for _, k := range perfK {
		corpora := makePerfCorpora(k)
		b.Run(fmt.Sprintf("K=%d", k), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				_, gc := newSegmentHarness(b)
				mgr := NewManager(gc, b.TempDir(), 0)
				var wg sync.WaitGroup
				wg.Add(len(corpora))
				var mu sync.Mutex
				var firstErr error
				for i, docs := range corpora {
					go func(idx int, d []searchengine.Document) {
						defer wg.Done()
						if err := mgr.AddAndShip(ctx, kgtypes.GraphKnowledge, fmt.Sprintf("g%d", idx), d); err != nil {
							mu.Lock()
							if firstErr == nil {
								firstErr = err
							}
							mu.Unlock()
						}
					}(i, docs)
				}
				wg.Wait()
				if firstErr != nil {
					b.Fatalf("AddAndShip: %v", firstErr)
				}
			}
		})
	}
}

// BenchmarkDeterministicAddAndShipFields is the BM25 analog of
// BenchmarkDeterministicAddAndShip: it AddAndShipFields K distinct 1024-doc field
// corpora CONCURRENTLY (one goroutine per call, each a distinct graph), driving the
// REAL ship path — AddAndShipFields → engine.Add/seal → bm25.Build → Encode →
// content-hash → ship. The reported allocs/op + alloc/op reflect the BM25
// build+Encode cost on the production embed-writeback path. Single-arm absolute
// numbers (BM25 has one engine per graph, no deterministic sibling).
//
// Run with:
//
//	go test ./cmd/knowledge/internal/segmentdist/ \
//	  -run '^$' -bench 'BenchmarkDeterministicAddAndShipFields' -benchmem -benchtime 5x
func BenchmarkDeterministicAddAndShipFields(b *testing.B) {
	ctx := context.Background()
	for _, k := range perfK {
		corpora := makePerfFieldCorpora(k)
		b.Run(fmt.Sprintf("K=%d", k), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				_, gc := newSegmentHarness(b)
				mgr := NewManager(gc, b.TempDir(), 0)
				var wg sync.WaitGroup
				wg.Add(len(corpora))
				var mu sync.Mutex
				var firstErr error
				for i, docs := range corpora {
					go func(idx int, d []searchengine.Document) {
						defer wg.Done()
						if err := mgr.AddAndShipFields(ctx, kgtypes.GraphKnowledge, fmt.Sprintf("g%d", idx), d); err != nil {
							mu.Lock()
							if firstErr == nil {
								firstErr = err
							}
							mu.Unlock()
						}
					}(i, docs)
				}
				wg.Wait()
				if firstErr != nil {
					b.Fatalf("AddAndShipFields: %v", firstErr)
				}
			}
		})
	}
}
