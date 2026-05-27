package pdf_test

// accuracy_metrics_test.go: 6-metric scoring functions for the T9
// corpus harness. Peer to accuracy_test.go — split per absorbed
// reviewer finding T3#3 to keep each file under the 300-line cap.
//
// Metric semantics:
//
//   - ChunkCountDelta              — |a - g| / max(a, g). 0 best.
//   - BoundaryIoU                  — mean coverage of golden bboxes by
//                                    actual chunks (asymmetric per
//                                    locked decision #8). 1 best.
//   - ClassificationAccuracy       — fraction of best-coverage matches
//                                    where the actual chunk's Kind
//                                    equals the golden Kind. 1 best.
//   - HeadingLevelAgreement        — among golden HEADING chunks with a
//                                    matched actual heading, fraction
//                                    with same HeadingLevel. 1 best.
//   - ReadingOrderKendallTau       — (1 - tau) / 2 over the permutation
//                                    of golden indices induced by
//                                    actual-side ordering. 0 best.
//   - TextSimilarityLevenshtein    — word-level edit-distance ratio
//                                    over concatenated chunk texts.
//                                    0 best.

import (
	"sort"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/layout"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/testdata/accuracy"
)

// metricsBundle is the per-fixture scored output. Every value in
// [0, 1].
type metricsBundle struct {
	ActualCount, GoldenCount         int
	GoldenHeadingCount               int
	ChunkCountDelta                  float64
	BoundaryIoU                      float64
	ClassificationAccuracy           float64
	HeadingLevelAgreement            float64
	ReadingOrderKendallTauDivergence float64
	TextSimilarityLevenshtein        float64
}

// chunkMatch is the result of pairing one golden chunk with its
// best-coverage actual chunk. ActualIdx == -1 when no actual chunk
// overlapped the golden's bbox at all.
type chunkMatch struct {
	GoldenIdx, ActualIdx int
	Coverage             float64
}

// scoreMetrics computes all 6 metrics for one fixture.
func scoreMetrics(actual []pdf.Chunk, golden []goldenChunk) metricsBundle {
	matches := matchChunks(actual, golden)
	gHeadings := 0
	for _, g := range golden {
		if g.Kind == string(layout.BlockHeading) {
			gHeadings++
		}
	}
	return metricsBundle{
		ActualCount:                      len(actual),
		GoldenCount:                      len(golden),
		GoldenHeadingCount:               gHeadings,
		ChunkCountDelta:                  chunkCountDelta(len(actual), len(golden)),
		BoundaryIoU:                      boundaryIoUFromMatches(matches, len(golden)),
		ClassificationAccuracy:           classificationAccuracy(matches, actual, golden),
		HeadingLevelAgreement:            headingLevelAgreement(matches, actual, golden),
		ReadingOrderKendallTauDivergence: readingOrderKendallTauDivergence(matches),
		TextSimilarityLevenshtein:        textSimilarityLevenshtein(actual, golden),
	}
}

// boundaryIoUFromMatches computes the mean per-golden coverage by the
// best-matching actual chunk. Reuses chunkMatch.Coverage from
// matchChunks (which is the asymmetric coverage of golden by best
// actual) — saves a redundant O(n*m) scan and keeps the boundary
// metric consistent with the page-aware match logic.
func boundaryIoUFromMatches(matches []chunkMatch, goldenCount int) float64 {
	if goldenCount == 0 {
		return 0
	}
	var sum float64
	for _, m := range matches {
		sum += m.Coverage
	}
	return sum / float64(goldenCount)
}

// matchChunks pairs each golden chunk with the actual chunk having
// maximum bbox coverage of the golden's area. ActualIdx == -1 on no
// overlap. Match candidates are restricted to chunks whose PageRange
// overlaps the golden's PageRange — bboxes are page-local so a
// page-1 box would otherwise spuriously match every same-x-y page.
func matchChunks(actual []pdf.Chunk, golden []goldenChunk) []chunkMatch {
	out := make([]chunkMatch, len(golden))
	for gi, g := range golden {
		gBox := goldenBoxOf(g.BBox)
		bestIdx := -1
		var bestCov float64
		for ai, a := range actual {
			if !pageRangeOverlap(a.PageRange, g.PageRange) {
				continue
			}
			cov := boxCoverage(actualBoxOf(a), gBox)
			if cov > bestCov {
				bestCov = cov
				bestIdx = ai
			}
		}
		out[gi] = chunkMatch{GoldenIdx: gi, ActualIdx: bestIdx, Coverage: bestCov}
	}
	return out
}

func pageRangeOverlap(a, b [2]int) bool {
	return a[0] <= b[1] && b[0] <= a[1]
}

func goldenBoxOf(b [4]float64) accuracy.Box {
	return accuracy.Box{X0: b[0], Y0: b[1], X1: b[2], Y1: b[3]}
}

func actualBoxOf(c pdf.Chunk) accuracy.Box {
	return accuracy.Box{X0: c.BBox.X0, Y0: c.BBox.Y0, X1: c.BBox.X1, Y1: c.BBox.Y1}
}

// boxCoverage returns the fraction of g's area covered by the
// intersection with a. Frame-agnostic.
func boxCoverage(a, g accuracy.Box) float64 {
	gArea := (g.X1 - g.X0) * (g.Y1 - g.Y0)
	if gArea <= 0 {
		return 0
	}
	ix0 := maxF(g.X0, a.X0)
	iy0 := maxF(g.Y0, a.Y0)
	ix1 := minF(g.X1, a.X1)
	iy1 := minF(g.Y1, a.Y1)
	if ix1 <= ix0 || iy1 <= iy0 {
		return 0
	}
	return (ix1 - ix0) * (iy1 - iy0) / gArea
}

func maxF(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func minF(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

// chunkCountDelta: |a - g| / max(a, g). 0 when both 0.
func chunkCountDelta(a, g int) float64 {
	if a == 0 && g == 0 {
		return 0
	}
	d := a - g
	if d < 0 {
		d = -d
	}
	return float64(d) / float64(max(a, g))
}

// classificationAccuracy: fraction of golden chunks that have a
// matched actual chunk of the same Kind. Unmatched goldens count as
// misses. Returns 0 when len(golden) == 0.
func classificationAccuracy(matches []chunkMatch, actual []pdf.Chunk, golden []goldenChunk) float64 {
	if len(golden) == 0 {
		return 0
	}
	hits := 0
	for _, m := range matches {
		if m.ActualIdx < 0 {
			continue
		}
		if string(actual[m.ActualIdx].Kind) == golden[m.GoldenIdx].Kind {
			hits++
		}
	}
	return float64(hits) / float64(len(golden))
}

// headingLevelAgreement: among golden headings with a matched actual
// heading, fraction with same HeadingLevel. Returns 0 when no golden
// headings exist (caller suppresses threshold check via
// GoldenHeadingCount > 0).
func headingLevelAgreement(matches []chunkMatch, actual []pdf.Chunk, golden []goldenChunk) float64 {
	totalGoldenHeadings, sameLevel := 0, 0
	for _, m := range matches {
		g := golden[m.GoldenIdx]
		if g.Kind != string(layout.BlockHeading) {
			continue
		}
		totalGoldenHeadings++
		if m.ActualIdx < 0 {
			continue
		}
		ac := actual[m.ActualIdx]
		if ac.Kind != layout.BlockHeading {
			continue
		}
		if ac.HeadingLevel == g.HeadingLevel {
			sameLevel++
		}
	}
	if totalGoldenHeadings == 0 {
		return 0
	}
	return float64(sameLevel) / float64(totalGoldenHeadings)
}

// readingOrderKendallTauDivergence: (1 - tau) / 2 over the permutation
// of golden indices induced by sorting matches by ActualIdx. Drops
// unmatched. Returns 0 when fewer than 2 matched pairs.
func readingOrderKendallTauDivergence(matches []chunkMatch) float64 {
	type pair struct{ goldenIdx, actualIdx int }
	var pairs []pair
	for _, m := range matches {
		if m.ActualIdx >= 0 {
			pairs = append(pairs, pair{goldenIdx: m.GoldenIdx, actualIdx: m.ActualIdx})
		}
	}
	if len(pairs) < 2 {
		return 0
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].actualIdx < pairs[j].actualIdx })
	perm := make([]int, len(pairs))
	for i, p := range pairs {
		perm[i] = p.goldenIdx
	}
	tau := accuracy.NormalizedKendallTau(perm)
	return (1 - tau) / 2
}

// textSimilarityLevenshtein concatenates chunk texts in source order
// and runs a word-level Levenshtein ratio.
func textSimilarityLevenshtein(actual []pdf.Chunk, golden []goldenChunk) float64 {
	var aWords, gWords []string
	for _, c := range actual {
		aWords = append(aWords, strings.Fields(c.Text)...)
	}
	for _, c := range golden {
		gWords = append(gWords, strings.Fields(c.Text)...)
	}
	return accuracy.WordEditDistanceRatio(aWords, gWords)
}
