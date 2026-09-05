// SPDX-License-Identifier: Apache-2.0

package bm25

import (
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
	"github.com/stretchr/testify/require"
)

// docWithContent builds a document carrying indexable text, so every document in
// these tests WOULD be a segment member if it survives tokenization.
func docWithContent(id searchengine.ExternalID) searchengine.Document {
	return searchengine.Document{
		ID:     id,
		Fields: map[string]string{searchengine.FieldContent: "getUserByID parseHTTPResponse"},
	}
}

// TestTokenizeDocsParallelRecoversPerDocument asserts a panic on ONE document
// costs only that document. The bad document is FIRST on purpose: under the
// pre-fix per-goroutine recover the three that follow are the ones abandoned, so
// the ordering is what makes the observation discriminating.
//
// WORKERS=1 IS LOAD-BEARING. At workers=NumCPU the four documents scatter across
// chunks and the pre-fix behaviour would lose only one, so the test would still
// discriminate but far more weakly.
//
// The OBSERVABLE is segment membership — what a reader can actually search —
// not an internal counter.
func TestTokenizeDocsParallelRecoversPerDocument(t *testing.T) {
	docs := []searchengine.Document{
		docWithContent("bad"),
		docWithContent("clean-1"),
		docWithContent("clean-2"),
		docWithContent("clean-3"),
	}

	// The injected unit of work reproduces the PRODUCTION panic's shape rather
	// than an arbitrary one.
	one := func(d searchengine.Document) docFieldTokens {
		if d.ID == "bad" {
			panic("runtime error: slice bounds out of range [:9] with length 8")
		}
		return tokenizeOne(d)
	}

	results, degraded := tokenizeDocsParallelWith(docs, 1, one)

	require.Equal(t,
		[]searchengine.ExternalID{"clean-1", "clean-2", "clean-3"},
		buildSegment(results).members,
		"a panic on the first document must not abandon the rest of the worker's chunk")
	require.Equal(t, map[string]int{degradeTokenizePanic: 1}, degraded,
		"the dropped document must be counted into the fixed-vocabulary census")
}

// TestTokenizeDocsParallelCleanBatchCountsNothing is the other half of the
// property pair. Without it, a census that returned a non-nil map on every batch
// — or an implementation that counted a drop for every document — would pass the
// test above. It is also the same-run known negative for the seam itself: with
// the REAL tokenizeOne injected, nothing drops, which proves the drop above is
// produced by the injected panic and not by the harness.
func TestTokenizeDocsParallelCleanBatchCountsNothing(t *testing.T) {
	docs := []searchengine.Document{docWithContent("clean-1"), docWithContent("clean-2")}

	results, degraded := tokenizeDocsParallelWith(docs, 1, tokenizeOne)

	require.Nil(t, degraded, "a clean batch must report NO census, not an empty one")
	require.Len(t, buildSegment(results).members, 2)
}
