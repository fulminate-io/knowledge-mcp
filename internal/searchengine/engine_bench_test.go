package searchengine

import (
	"fmt"
	"testing"
)

// searchSerial is a test-only baseline: the same query/merge as Search but
// walking segments sequentially (no fan-out). Used to contrast with the real
// parallel Search in the benchmarks below.
func (e *SegmentedIndex[Q, S]) searchSerial(q Q, k int) []Hit {
	set := e.set.Load()
	if len(set.entries) == 0 || k <= 0 {
		return nil
	}
	results := make([][]Hit, len(set.entries))
	for i, entry := range set.entries {
		accept := func(id ExternalID) bool {
			ord, ok := entry.members[id]
			return ok && entry.live.Live(ord)
		}
		results[i] = entry.payload.Search(q, set.stats, k, accept)
	}
	return mergeTopK(results, k)
}

// benchEngine builds an engine holding many sealed segments, each with several
// docs, so cross-segment fan-out has work to parallelize.
func benchEngine(b *testing.B, segments, docsPerSeg int) *SegmentedIndex[mockQuery, mockStats] {
	b.Helper()
	e := closeOnCleanup(b, New[mockQuery, mockStats](mockFormat{}, Options{
		MinSegmentDocs:     docsPerSeg,
		DeletesPctAllowed:  2.0, // no merge interference
		SegmentCountTarget: 1 << 30,
	}))
	id := 0
	for range segments {
		batch := make([]Document, docsPerSeg)
		for d := range docsPerSeg {
			batch[d] = doc(fmt.Sprintf("d%d", id), "term term filler")
			id++
		}
		if err := e.Add(batch); err != nil {
			b.Fatal(err)
		}
	}
	return e
}

// On the dev machine (Apple M4 Max, GOMAXPROCS=16), BenchmarkSearchParallel
// beats BenchmarkSearchSerial at high segment count (256 segments × 64 docs):
// observed ~326µs/op parallel vs ~518µs/op serial. The per-segment goroutine
// fan-out amortizes the scan across cores while the serial walk pays it
// sequentially. Both emit ns/op; the win grows with segment count.
func BenchmarkSearchParallel(b *testing.B) {
	e := benchEngine(b, 256, 64)
	defer e.Close()
	b.ResetTimer()
	for range b.N {
		_ = e.Search(mockQuery{term: "term"}, 10)
	}
}

func BenchmarkSearchSerial(b *testing.B) {
	e := benchEngine(b, 256, 64)
	defer e.Close()
	b.ResetTimer()
	for range b.N {
		_ = e.searchSerial(mockQuery{term: "term"}, 10)
	}
}
