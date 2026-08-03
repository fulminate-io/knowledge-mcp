// SPDX-License-Identifier: Apache-2.0

package hnsw

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"math/rand/v2"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// convergenceDocs builds a deterministic vector corpus for the merge tests.
func convergenceDocs(n, seed int) []searchengine.Document {
	rng := rand.New(rand.NewPCG(uint64(0x5150+seed), uint64(0x8086+seed)))
	docs := make([]searchengine.Document, n)
	for i := range docs {
		v := make([]byte, defaultVecBytes)
		for b := range v {
			v[b] = byte(rng.UintN(256))
		}
		docs[i] = searchengine.Document{ID: fmt.Sprintf("s%d-n%04d", seed, i), Vector: v}
	}
	return docs
}

func encodedHash(t *testing.T, seg searchengine.Segment[[]byte, struct{}]) string {
	t.Helper()
	blob, err := seg.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	sum := sha256.Sum256(blob)
	return fmt.Sprintf("%x", sum)
}

// TestMergeConverges pins the invariant the whole segment identity scheme rests on:
// a segment id is the hash of its bytes, so one writer repeating the same
// consolidation must produce the same bytes.
//
// Merge used to construct its graph directly and insert in source order, with a
// per-call random seed, so repeating it produced a different blob every time — and
// therefore a different id for identical content. Anything that reasons about
// identity across a rebuild (deduplicating an import, diffing a shipped set,
// deciding a re-emit shipped nothing new) silently stopped working.
func TestMergeConverges(t *testing.T) {
	const (
		perSegment = 40
		runs       = 8
	)
	left := convergenceDocs(perSegment, 1)
	right := convergenceDocs(perSegment, 2)

	build := func(docs []searchengine.Document) searchengine.Segment[[]byte, struct{}] {
		t.Helper()
		seg, err := Format{}.Build(docs)
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		return seg
	}

	t.Run("N independent merges of the same inputs yield ONE hash", func(t *testing.T) {
		seen := map[string]int{}
		for range runs {
			// Fresh inputs each run: a merge must not depend on which objects it was
			// handed, only on the content it accepts.
			merged, err := Format{}.Merge(
				[]searchengine.Segment[[]byte, struct{}]{build(left), build(right)},
				[]func(searchengine.ExternalID) bool{nil, nil},
			)
			if err != nil {
				t.Fatalf("Merge: %v", err)
			}
			seen[encodedHash(t, merged)]++
		}
		if len(seen) != 1 {
			t.Fatalf("%d merges produced %d distinct encoded hashes, want 1 — the merge does not converge, "+
				"so identical content lands under different ids", runs, len(seen))
		}
	})

	t.Run("a merge-of-one reproduces Build's bytes", func(t *testing.T) {
		built := build(left)
		merged, err := Format{}.Merge(
			[]searchengine.Segment[[]byte, struct{}]{build(left)},
			[]func(searchengine.ExternalID) bool{nil},
		)
		if err != nil {
			t.Fatalf("Merge: %v", err)
		}
		builtBlob, err := built.Encode()
		if err != nil {
			t.Fatalf("Encode built: %v", err)
		}
		mergedBlob, err := merged.Encode()
		if err != nil {
			t.Fatalf("Encode merged: %v", err)
		}
		if !bytes.Equal(builtBlob, mergedBlob) {
			t.Fatalf("merge-of-one produced %d bytes, Build produced %d, and they differ — "+
				"consolidating a partition back to its own content would publish a new id",
				len(mergedBlob), len(builtBlob))
		}
	})

	t.Run("the merge is independent of input ORDER", func(t *testing.T) {
		forward, err := Format{}.Merge(
			[]searchengine.Segment[[]byte, struct{}]{build(left), build(right)},
			[]func(searchengine.ExternalID) bool{nil, nil},
		)
		if err != nil {
			t.Fatalf("Merge forward: %v", err)
		}
		reversed, err := Format{}.Merge(
			[]searchengine.Segment[[]byte, struct{}]{build(right), build(left)},
			[]func(searchengine.ExternalID) bool{nil, nil},
		)
		if err != nil {
			t.Fatalf("Merge reversed: %v", err)
		}
		if encodedHash(t, forward) != encodedHash(t, reversed) {
			t.Fatal("merging the same segments in the opposite order produced different bytes — " +
				"a repeated consolidation is then not idempotent, which is what the retain-on-error " +
				"disposition depends on")
		}
	})
}
