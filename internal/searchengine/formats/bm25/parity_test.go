// SPDX-License-Identifier: Apache-2.0

package bm25

import (
	"fmt"
	"math/rand/v2"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// skewedCorpus builds n documents with a DELIBERATELY SKEWED term distribution so
// IDF discriminates: a handful of "common" terms appear in most docs (low IDF)
// while many "rare" terms appear in few (high IDF). A per-segment-IDF bug would
// visibly diverge here because each small segment sees a different local DF for a
// common term than the corpus as a whole does.
func skewedCorpus(n int) []searchengine.Document {
	rng := rand.New(rand.NewPCG(0x9E37, 0x79B9))
	common := []string{"service", "handler", "context", "error", "request"}
	docs := make([]searchengine.Document, n)
	for i := range docs {
		// Every doc gets 1-3 common terms (so they are corpus-wide frequent).
		var summaryB strings.Builder
		for c := 0; c < 1+rng.IntN(3); c++ {
			summaryB.WriteString(common[rng.IntN(len(common))])
			summaryB.WriteByte(' ')
		}
		summary := summaryB.String()
		// A doc-unique term with NO shared camelCase/snake sub-tokens (so it does not
		// create a giant equal-score tie tail across docs). Distinct digit strings
		// tokenize to distinct single tokens.
		uniq := fmt.Sprintf("zq%d%d%d", i, i*7+3, i*131+11)
		// A clustered rare term shared by a small neighborhood of docs (a handful of
		// distinct clusters → mid-range IDF that discriminates).
		cluster := fmt.Sprintf("clu%d", i/37)
		docs[i] = searchengine.Document{
			ID: fmt.Sprintf("doc%d", i),
			Fields: map[string]string{
				searchengine.FieldSymbolName: uniq,
				searchengine.FieldSummary:    summary + cluster,
				searchengine.FieldKeywords:   common[i%len(common)] + " " + cluster,
			},
		}
	}
	return docs
}

// engineRanked drives the REAL engine's fan-out Search and returns ranked (id,score).
func engineRanked(eng *searchengine.SegmentedIndex[Query, *CorpusStats], qText string, k int) []searchengine.Hit {
	return eng.Search(NewQuery(qText), k)
}

// TestCrossSegmentScoringParity is Phase 2 Step 1's load-bearing criterion: a
// fan-out engine with >= 8 sealed segments returns IDENTICAL ranked id order and
// scores (epsilon 1e-9) to a single-segment baseline across every test query. This
// exercises the engine's cached-S path end-to-end: AggregateStats folds the per-
// segment rollups into corpus-global stats (recomputed on every set change in
// segmentset.go), and Search threads that cached S into every segment's Search.
func TestCrossSegmentScoringParity(t *testing.T) {
	const corpus = 512
	docs := skewedCorpus(corpus)

	// BASELINE: one segment holds the whole corpus. MinSegmentDocs == corpus so the
	// single Add of `corpus` docs crosses the coalescing threshold and seals exactly
	// one segment (the engine seals when len(active) >= MinSegmentDocs).
	baseline := searchengine.New[Query, *CorpusStats](Format{}, searchengine.Options{
		MinSegmentDocs:     corpus,
		DeletesPctAllowed:  2.0,     // never auto-merge
		SegmentCountTarget: 1 << 30, // never auto-merge
	})
	defer baseline.Close()
	require.NoError(t, baseline.Add(docs))
	require.Equal(t, 1, baseline.Metrics().SegmentCount, "baseline must hold exactly one segment")

	// FANOUT: small MinSegmentDocs so >= 8 segments seal.
	const perSeg = corpus / 8 // 64 → exactly 8 segments
	fanout := searchengine.New[Query, *CorpusStats](Format{}, searchengine.Options{
		MinSegmentDocs:     perSeg,
		DeletesPctAllowed:  2.0,
		SegmentCountTarget: 1 << 30,
	})
	defer fanout.Close()
	for i := 0; i < corpus; i += perSeg {
		end := min(i+perSeg, corpus)
		require.NoError(t, fanout.Add(docs[i:end]))
	}
	require.GreaterOrEqual(t, fanout.Metrics().SegmentCount, 8,
		"fan-out engine must seal >= 8 segments to genuinely exercise the cross-segment path")

	queries := []string{
		"service handler", // common terms — low IDF, sensitive to per-segment DF skew
		"clu3",            // clustered rare term spanning a small neighborhood
		"context error request clu5",
		"handler clu7 service",
	}
	for _, q := range queries {
		base := engineRanked(baseline, q, 20)
		fan := engineRanked(fanout, q, 20)
		assertRankingParity(t, q, base, fan)
	}
}

// assertRankingParity asserts the fan-out ranking matches the single-segment
// baseline. Scores are deterministic (both paths compute identical corpus-global
// scores) and must match position-by-position within 1e-9. IDs must match exactly
// for every position whose score is STRICTLY GREATER than the last returned
// (boundary) score — those are unambiguously ranked. The boundary tie-group (docs
// sharing the minimum returned score) is compared as a SET intersection: a bounded
// top-k necessarily cuts an equal-score tie-group at an arbitrary point, so which
// members of an equal-score group land in the last slots is not a correctness
// property — the SCORES being identical and the unambiguous prefix matching is.
func assertRankingParity(t *testing.T, q string, base, fan []searchengine.Hit) {
	t.Helper()
	require.Len(t, fan, len(base), "query %q: hit count must match", q)
	if len(base) == 0 {
		return
	}
	// Scores must match position-by-position — this is the load-bearing assertion
	// (corpus-global IDF makes the scores identical; per-segment IDF would not).
	for i := range base {
		require.InDelta(t, base[i].Score, fan[i].Score, 1e-9,
			"query %q rank %d: score must match (corpus-global IDF)", q, i)
	}
	boundary := base[len(base)-1].Score
	// IDs must match exactly above the boundary tie-group.
	for i := range base {
		if base[i].Score-boundary > 1e-9 {
			require.Equal(t, base[i].ID, fan[i].ID,
				"query %q rank %d: id must match for unambiguously-ranked hits", q, i)
		}
	}
	// The full id sets must be equal when there is no boundary tie ambiguity (the
	// common case for well-separated queries); when a tie spans the boundary the
	// non-tied prefix already matched above and the scores all matched.
	baseIDs := idSet(base)
	fanIDs := idSet(fan)
	if !boundaryTied(base, boundary) {
		require.Equal(t, baseIDs, fanIDs, "query %q: full id set must match (no boundary tie)", q)
	}
}

func idSet(hits []searchengine.Hit) map[string]struct{} {
	m := make(map[string]struct{}, len(hits))
	for _, h := range hits {
		m[h.ID] = struct{}{}
	}
	return m
}

// boundaryTied reports whether more than one returned hit shares the minimum score
// (an equal-score group sitting on the top-k boundary).
func boundaryTied(hits []searchengine.Hit, boundary float64) bool {
	n := 0
	for _, h := range hits {
		if absf(h.Score-boundary) <= 1e-9 {
			n++
		}
	}
	return n > 1
}

// TestNegativeControlPerSegmentIDFDiverges proves corpus-global IDF is load-bearing:
// scoring the SAME fan-out segments with PER-SEGMENT stats (each segment's own
// AggregateStats over itself) instead of the corpus-global cached stats produces a
// DIFFERENT ranking/score than the single-index baseline on at least one skewed
// query. If this divergence did NOT appear, the parity test above would be
// false-green (per-segment and corpus-global would be indistinguishable).
func TestNegativeControlPerSegmentIDFDiverges(t *testing.T) {
	const corpus = 512
	const perSeg = corpus / 8
	docs := skewedCorpus(corpus)

	// Single-segment baseline scores (corpus-global by construction).
	baseSeg, baseStats := buildOne(t, docs)

	// Fan-out segments built the same way the engine seals them.
	var segs []*mappedSegment
	for i := 0; i < corpus; i += perSeg {
		end := min(i+perSeg, corpus)
		s, _, err := Format{}.Build(docs[i:end])
		require.NoError(t, err)
		segs = append(segs, s.(*mappedSegment))
	}
	require.GreaterOrEqual(t, len(segs), 8)

	// PER-SEGMENT IDF (the BUG): each segment scored with its OWN stats, results
	// merged by score desc. This is what a naive non-AggregateStats engine would do.
	perSegRanked := func(qText string, k int) []searchengine.Hit {
		q := NewQuery(qText)
		var all []searchengine.Hit
		for _, s := range segs {
			localStats := Format{}.AggregateStats([]searchengine.Segment[Query, *CorpusStats]{s})
			all = append(all, s.Search(q, localStats, k, nil)...)
		}
		sort.Slice(all, func(i, j int) bool {
			if all[i].Score != all[j].Score {
				return all[i].Score > all[j].Score
			}
			return all[i].ID < all[j].ID
		})
		if len(all) > k {
			all = all[:k]
		}
		return all
	}

	// Corpus-global baseline ranking for a common-term query (the most IDF-sensitive).
	const q = "service handler"
	base := baseSeg.Search(NewQuery(q), baseStats, 20, nil)
	bad := perSegRanked(q, 20)

	// At least one of: different top-id ordering OR a score mismatch beyond epsilon.
	diverged := len(base) != len(bad)
	if !diverged {
		for i := range base {
			if base[i].ID != bad[i].ID {
				diverged = true
				break
			}
			if absf(base[i].Score-bad[i].Score) > 1e-6 {
				diverged = true
				break
			}
		}
	}
	require.True(t, diverged,
		"per-segment IDF must diverge from corpus-global baseline on a skewed query — corpus-global IDF is load-bearing")
}

func absf(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}
