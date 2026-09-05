// SPDX-License-Identifier: Apache-2.0

package bm25

import (
	"crypto/sha256"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// manyTermDocs builds a deterministic corpus with a large, varied vocabulary so
// the docFreq + postings maps hold enough distinct keys that Go's randomized map
// iteration would reorder the emit layout run-to-run WITHOUT the sort in Encode.
// A handful of terms (sampleDocs) can hash to a stable bucket order and false-green
// the regression guard; hundreds of distinct terms make the unsorted layout vary.
func manyTermDocs(n int) []searchengine.Document {
	rng := rand.New(rand.NewPCG(0xB725, 0xDE7E))
	vocab := make([]string, 600)
	for i := range vocab {
		vocab[i] = fmt.Sprintf("term%04d", i)
	}
	docs := make([]searchengine.Document, n)
	for i := range docs {
		// Each doc draws ~12 random vocab terms into its summary + content.
		var summary, content strings.Builder
		for range 12 {
			summary.WriteString(vocab[rng.IntN(len(vocab))] + " ")
			content.WriteString(vocab[rng.IntN(len(vocab))] + " ")
		}
		docs[i] = searchengine.Document{
			ID: fmt.Sprintf("d%d", i),
			Fields: map[string]string{
				searchengine.FieldSummary: summary.String(),
				searchengine.FieldContent: content.String(),
			},
		}
	}
	return docs
}

// TestEncodeByteDeterministic proves the BM25 arm of cross-writer convergence: the
// same corpus built into a sealed segment twice (two independent Build calls)
// serializes byte-identically, and therefore content-hashes identically. The
// segment id is sha256(Encode()), so a content-addressed store dedups two writers'
// blobs to a single copy ONLY if Encode is byte-stable. Before the docFreq/postings
// key-sort, ranging the maps directly made this fail even on one machine.
func TestEncodeByteDeterministic(t *testing.T) {
	docs := manyTermDocs(400)

	seg1 := buildBM25(t, docs)
	seg2 := buildBM25(t, docs)

	blob1, err := seg1.Encode()
	require.NoError(t, err)
	blob2, err := seg2.Encode()
	require.NoError(t, err)

	require.Equal(t, blob1, blob2, "two independent builds of the same corpus must Encode byte-identically")
	require.Equal(t, sha256.Sum256(blob1), sha256.Sum256(blob2), "byte-identical Encode ⇒ identical content hash (segment id convergence)")
}

// TestEncodeStableAcrossManyBuilds repeats the build many times to flush out
// rare bucket-order coincidences: every Encode must equal the first. A single pass
// can accidentally agree under map randomization; N passes make a non-deterministic
// emit overwhelmingly likely to diverge at least once.
func TestEncodeStableAcrossManyBuilds(t *testing.T) {
	docs := manyTermDocs(300)

	first, err := buildBM25(t, docs).Encode()
	require.NoError(t, err)

	for i := range 8 {
		blob, err := buildBM25(t, docs).Encode()
		require.NoError(t, err)
		require.Equal(t, first, blob, "build %d Encode diverged from the first — emit is not byte-deterministic", i)
	}
}

// buildBM25 seals a BM25 segment from docs (the convergence tests only inspect the
// serialized bytes, so corpus stats are not needed).
func buildBM25(t *testing.T, docs []searchengine.Document) *mappedSegment {
	t.Helper()
	segIface, _, err := Format{}.Build(docs)
	require.NoError(t, err)
	return segIface.(*mappedSegment)
}
