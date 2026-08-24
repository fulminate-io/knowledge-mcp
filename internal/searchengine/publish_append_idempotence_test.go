package searchengine

import (
	"fmt"
	"testing"
)

// TestPublishAppendIsIdempotentBySegmentID is the catcher for the duplicate-layer
// defect: a second rebuild of an UNCHANGED corpus in the SAME process used to
// append a whole second copy of every segment.
//
// It drives the real seal path (Add buffers, Flush force-seals — the shape the
// rebuild driver's per-bucket Add+Seal loop uses), never publishAppend directly,
// because the production call that has to be wrong is a seal reproducing an id the
// engine already holds. Reaching in past Add/Flush would test a private helper
// instead of the path that reaches it.
//
// The two assertions are different claims and both are load-bearing. The segment
// COUNT catches the duplicate entries. The doc-count IDENTITY catches what the
// duplicates cost: ResidentDocCount SUMS per-segment DocCount, so a corpus resident
// twice reads double, and that inflated number is the publish coverage gate's
// numerator and the operator status column's resident reading.
func TestPublishAppendIsIdempotentBySegmentID(t *testing.T) {
	const (
		buckets      = 4
		docsPerBlock = 5
		corpus       = buckets * docsPerBlock
	)

	// MinSegmentDocs above the group size so Add never self-seals — every segment is
	// produced by the explicit Flush, which is the primitive a caller laying out one
	// segment per group drives directly.
	e := newTestEngine(t, 1<<20)
	defer e.Close()

	groups := make([][]Document, buckets)
	for b := range groups {
		for i := range docsPerBlock {
			id := fmt.Sprintf("n-%d-%d", b, i)
			groups[b] = append(groups[b], doc(id, "alpha beta "+id))
		}
	}

	rebuild := func() {
		t.Helper()
		for _, g := range groups {
			if err := e.Add(g); err != nil {
				t.Fatalf("Add: %v", err)
			}
			if err := e.Flush(); err != nil {
				t.Fatalf("Flush: %v", err)
			}
		}
	}

	rebuild()
	if got := len(e.Export()); got != buckets {
		t.Fatalf("after the FIRST rebuild: %d segments, want %d", got, buckets)
	}
	firstIDs := exportedIDSet(e)

	// The second rebuild re-emits byte-identical groups, so every seal mints an id
	// the engine already holds.
	rebuild()

	if got := len(e.Export()); got != buckets {
		t.Errorf("after the SECOND rebuild of an unchanged corpus: %d segments, want %d — "+
			"a re-sealed group appended a duplicate entry instead of being recognized as resident",
			got, buckets)
	}
	if got := exportedIDSet(e); !sameIDSet(got, firstIDs) {
		t.Errorf("the second rebuild changed the resident id set: got %v, want %v", got, firstIDs)
	}
	resident, distinct := e.ResidentDocCount(), e.DistinctResidentDocCount()
	if resident != distinct {
		t.Errorf("ResidentDocCount=%d but DistinctResidentDocCount=%d — the corpus is resident %.1fx over, "+
			"which is the number the publish coverage gate and the status column read",
			resident, distinct, float64(resident)/float64(distinct))
	}
	if resident != corpus {
		t.Errorf("ResidentDocCount=%d, want the corpus size %d", resident, corpus)
	}
}

// exportedIDSet lists the resident segment ids as a set.
func exportedIDSet(e *SegmentedIndex[mockQuery, mockStats]) map[SegmentID]struct{} {
	out := map[SegmentID]struct{}{}
	for _, b := range e.Export() {
		out[b.ID] = struct{}{}
	}
	return out
}

func sameIDSet(a, b map[SegmentID]struct{}) bool {
	if len(a) != len(b) {
		return false
	}
	for id := range a {
		if _, ok := b[id]; !ok {
			return false
		}
	}
	return true
}
