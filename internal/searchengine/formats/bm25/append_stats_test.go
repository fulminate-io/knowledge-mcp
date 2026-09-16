// SPDX-License-Identifier: Apache-2.0

// append_stats_test.go — the INCREMENTAL CORPUS STATISTICS agree with the fold
// (the resident-growth work, per-publish-cost requirement).
//
// The engine's publish path no longer folds AggregateStats over every resident
// segment; it derives the next generation's statistics from the previous
// generation's plus the ONE segment being appended. The two shapes alternate over
// one engine's life — a snapshot that outgrows the route-tail limit flattens
// through AggregateStats mid-sequence — so a disagreement between them would make
// that boundary observable in BM25 scores.
//
// WHAT IS COMPARED IS THE TWO ARTIFACTS THE REQUIREMENT NAMES and nothing else:
// the stats reached by a sequence of AppendStats calls, and the stats
// AggregateStats folds over the SAME segments. TotalDocs, every FieldAvgLen key
// and value, and a docFreqOf answer for a term resolved AFTER the generation was
// built — so the probe list is compared by BEHAVIOUR rather than by length.

package bm25

import (
	"fmt"
	"maps"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// appendStatsSegment builds one sealed segment over the given documents.
func appendStatsSegment(t *testing.T, docs []searchengine.Document) searchengine.Segment[Query, *CorpusStats] {
	t.Helper()
	seg, _, err := Format{}.Build(docs)
	require.NoError(t, err)
	return seg
}

// appendStatsDoc is one document carrying both indexed fields, so FieldAvgLen has
// more than one key to disagree on.
func appendStatsDoc(i int, body string) searchengine.Document {
	return searchengine.Document{
		ID: fmt.Sprintf("append-%03d", i),
		Fields: map[string]string{
			searchengine.FieldSummary: fmt.Sprintf("summary%d shared", i),
			searchengine.FieldContent: body,
		},
	}
}

// requireStatsAgree compares the two artifacts on every axis the format publishes.
func requireStatsAgree(t *testing.T, incremental, folded *CorpusStats, terms []string) {
	t.Helper()
	require.Equal(t, folded.TotalDocs, incremental.TotalDocs, "TotalDocs is the N in every IDF term")
	require.Equal(t, folded.FieldAvgLen, incremental.FieldAvgLen,
		"FieldAvgLen drives length normalization; a missing key disables it for that field silently")
	for _, term := range terms {
		require.Equal(t, folded.docFreqOf(term), incremental.docFreqOf(term),
			"the corpus-global document frequency for %q must not depend on which shape built the generation", term)
	}
}

// TestAppendStatsAgreesWithTheFold is the agreement row.
func TestAppendStatsAgreesWithTheFold(t *testing.T) {
	t.Parallel()
	f := Format{}

	segs := make([]searchengine.Segment[Query, *CorpusStats], 0, 12)
	for i := range 12 {
		body := "alpha beta"
		if i%3 == 0 {
			body = "alpha gamma delta epsilon"
		}
		segs = append(segs, appendStatsSegment(t, []searchengine.Document{appendStatsDoc(i, body)}))
	}

	// The incremental route, one segment at a time, exactly as withAppended walks it.
	incremental := f.AggregateStats(nil)
	for _, seg := range segs {
		incremental = f.AppendStats(incremental, seg)
	}

	requireStatsAgree(t, incremental, f.AggregateStats(segs), []string{"alpha", "beta", "gamma", "shared", "absent"})
	require.Positive(t, incremental.docFreqOf("alpha"),
		"an agreement instrument that answers zero on both sides compares two empty things")
}

// TestAppendStatsInputClasses walks the classes the specification names for the
// append path.
func TestAppendStatsInputClasses(t *testing.T) {
	t.Parallel()
	f := Format{}

	t.Run("an_empty_receiver_is_the_first_publish", func(t *testing.T) {
		t.Parallel()
		seg := appendStatsSegment(t, []searchengine.Document{appendStatsDoc(0, "alpha beta")})
		// The engine's empty set carries AggregateStats(nil); appending onto it is the
		// first publish of a graph's life.
		first := f.AppendStats(f.AggregateStats(nil), seg)
		requireStatsAgree(t, first, f.AggregateStats([]searchengine.Segment[Query, *CorpusStats]{seg}),
			[]string{"alpha", "beta"})
		require.Equal(t, int64(1), first.TotalDocs)
	})

	t.Run("a_nil_receiver_contributes_nothing_rather_than_panicking", func(t *testing.T) {
		t.Parallel()
		seg := appendStatsSegment(t, []searchengine.Document{appendStatsDoc(1, "alpha")})
		got := f.AppendStats(nil, seg)
		require.Equal(t, int64(1), got.TotalDocs,
			"S is a pointer type, so the zero value is nil and the append path must treat it as an empty corpus")
	})

	t.Run("a_field_the_receiver_had_never_seen_gains_a_key", func(t *testing.T) {
		t.Parallel()
		summaryOnly := searchengine.Document{
			ID:     "summary-only",
			Fields: map[string]string{searchengine.FieldSummary: "alpha"},
		}
		first := f.AppendStats(f.AggregateStats(nil), appendStatsSegment(t, []searchengine.Document{summaryOnly}))
		second := f.AppendStats(first, appendStatsSegment(t, []searchengine.Document{appendStatsDoc(2, "beta gamma")}))
		require.Contains(t, second.FieldAvgLen, searchengine.FieldContent,
			"a field first seen on the appended segment must reach the next generation's averages")
	})

	t.Run("a_segment_this_format_did_not_build_is_skipped_on_both_shapes", func(t *testing.T) {
		t.Parallel()
		real := appendStatsSegment(t, []searchengine.Document{appendStatsDoc(3, "alpha beta")})
		base := f.AppendStats(f.AggregateStats(nil), real)
		foreign := f.AppendStats(base, notAMappedSegment{})
		require.Equal(t, base.TotalDocs, foreign.TotalDocs,
			"AggregateStats continues past a non-*mappedSegment, so the append path must skip it identically")
		require.Equal(t, base.FieldAvgLen, foreign.FieldAvgLen)
		require.NotSame(t, base, foreign,
			"and it must still produce a FRESH object: a shared stats object would share a memo across two generations")
	})

	t.Run("the_receiver_is_never_mutated", func(t *testing.T) {
		t.Parallel()
		base := f.AppendStats(f.AggregateStats(nil),
			appendStatsSegment(t, []searchengine.Document{appendStatsDoc(4, "alpha")}))
		baseDocs, baseAvg := base.TotalDocs, maps.Clone(base.FieldAvgLen)
		_ = f.AppendStats(base, appendStatsSegment(t, []searchengine.Document{appendStatsDoc(5, "beta gamma delta")}))
		require.Equal(t, baseDocs, base.TotalDocs,
			"prev belongs to a PUBLISHED snapshot that readers are serving from")
		require.Equal(t, baseAvg, base.FieldAvgLen)
	})
}

// notAMappedSegment is a Segment this format did not build. The fold's type assert
// skips it silently, so the append path must too.
type notAMappedSegment struct{}

func (notAMappedSegment) Search(Query, *CorpusStats, int, func(searchengine.ExternalID) bool) []searchengine.Hit {
	return nil
}
func (notAMappedSegment) IDs() []searchengine.ExternalID { return nil }
func (notAMappedSegment) Encode() ([]byte, error)        { return nil, nil }
func (notAMappedSegment) HeapBytes() int64               { return 0 }
