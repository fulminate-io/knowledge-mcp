package hnsw

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// dupDocs builds n documents whose ids are shared across layers and whose vectors
// are salted by layer, so two layers describe the same corpus with different bytes.
func dupDocs(n, layer int) []searchengine.Document {
	docs := make([]searchengine.Document, 0, n)
	for i := range n {
		vec := make([]byte, defaultVecBytes)
		for j := range vec {
			vec[j] = byte((i*7 + j*3 + layer*101) % 251)
		}
		docs = append(docs, searchengine.Document{ID: fmt.Sprintf("dup-%04d", i), Vector: vec})
	}
	return docs
}

// TestDuplicateMergeSurvivorIsDeterministic asserts the surviving copy of a
// duplicated id is byte-identical across runs and across shuffled input order.
//
// THE -count=5 FORM IS THE ASSERTION. Nondeterminism here is a coin flip per id —
// measured at a 53/67 split across 120 ids before the fix — so a single run has no
// discriminating power. The criterion's command requires FIVE PASS lines.
//
// WHAT IT GATES that no sibling can see: a dedup which picks an arbitrary survivor
// satisfies the newEntry invariant and can still satisfy the recall gate, while
// returning a different vector run to run. Two of the three ruled pieces live in this
// package and are asserted here — LAST-WINS selection at item collection, and the
// builder's STABLE tie-break. The third, sorting the constituent union by segment id,
// lives at the caller (segmentdist) because that is where constituent order is
// decided; it is what makes the arbitrary-but-stable half of the contract stable.
func TestDuplicateMergeSurvivorIsDeterministic(t *testing.T) {
	var f Format
	const corpus = 64

	buildPair := func(shuffle bool) (a, b searchengine.Segment[[]byte, struct{}]) {
		t.Helper()
		docsA, docsB := dupDocs(corpus, 0), dupDocs(corpus, 99)
		if shuffle {
			// Reverse the WITHIN-segment insertion order. The builder sorts by id, so a
			// correct implementation is unaffected; an unstable tie-break is not.
			for i, j := 0, len(docsA)-1; i < j; i, j = i+1, j-1 {
				docsA[i], docsA[j] = docsA[j], docsA[i]
				docsB[i], docsB[j] = docsB[j], docsB[i]
			}
		}
		var err error
		if a, _, err = f.Build(docsA); err != nil {
			t.Fatalf("build A: %v", err)
		}
		if b, _, err = f.Build(docsB); err != nil {
			t.Fatalf("build B: %v", err)
		}
		return a, b
	}

	mergeBytes := func(a, b searchengine.Segment[[]byte, struct{}]) []byte {
		t.Helper()
		merged, err := mergeSegments(t,
			[]searchengine.Segment[[]byte, struct{}]{a, b},
			[]func(searchengine.ExternalID) bool{nil, nil},
		)
		if err != nil {
			t.Fatalf("merge: %v", err)
		}
		// One node per id: the invariant this whole step exists to restore. Asserted on
		// the RAW member list, since a distinct count is an identity that holds however
		// duplicated the graph is.
		if got := len(merged.IDs()); got != corpus {
			t.Fatalf("merged segment holds %d nodes for %d ids — the merge must keep exactly one per id", got, corpus)
		}
		blob, err := merged.Encode()
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		return blob
	}

	a1, b1 := buildPair(false)
	base := mergeBytes(a1, b1)

	// Repeating the merge over the same constituents reproduces the same bytes.
	if got := mergeBytes(a1, b1); !bytes.Equal(base, got) {
		t.Fatal("re-merging the same constituents produced different bytes — the merge is not reproducible")
	}

	// Shuffled WITHIN-segment order reaches the same result: the builder's sorted-by-id
	// insertion is what fixes the sequence, and its tie-break must be stable.
	a2, b2 := buildPair(true)
	if got := mergeBytes(a2, b2); !bytes.Equal(base, got) {
		t.Fatal("shuffling the input document order changed the merged bytes — the builder's ordering is not stable")
	}

	// THE RULED SELECTION, asserted directly: the LAST constituent's copy survives.
	// The caller appends the freshly built segment last, so this is what makes a fresh
	// write win; between two resident layers it is arbitrary-but-stable, and it is NOT
	// newest-wins overall.
	merged, err := mergeSegments(t,
		[]searchengine.Segment[[]byte, struct{}]{a1, b1},
		[]func(searchengine.ExternalID) bool{nil, nil},
	)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	hs, ok := merged.(*hnswSegment)
	if !ok {
		t.Fatalf("merge returned %T", merged)
	}
	wantLayer := dupDocs(corpus, 99) // b1's layer — the LAST constituent.
	for _, d := range wantLayer {
		got, ok := hs.VectorByID(d.ID)
		if !ok {
			t.Fatalf("id %q missing from the merged segment", d.ID)
		}
		if !bytes.Equal(got, d.Vector) {
			t.Fatalf("id %q kept the FIRST constituent's vector; the ruled contract keeps the LAST, which is what makes a fresh write win", d.ID)
		}
	}
}
