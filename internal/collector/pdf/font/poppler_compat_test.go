package font_test

import (
	"os"
	"strings"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/font"
	internalpdf "github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/internal/pdfcpu"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/text"
)

// TestDecode_PopplerCrossValidation is the T3 ticket DoD acceptance
// test: extract text from a real PDF (RFC 7234, June 2014) and
// compare against poppler's pdftotext reference. Word-level edit
// distance must be ≤ 5%.
//
// The test runs entirely against the COMMITTED reference text at
// collector/pdf/testdata/corpus/rfc-7234-caching/poppler-references/source.pdftotext.txt
// — it does NOT invoke pdftotext. CI does not need poppler installed.
// To regenerate the reference (after pdftotext upgrade or fixture
// replacement):
//
//	pdftotext -layout collector/pdf/testdata/corpus/rfc-7234-caching/source.pdf \
//	    collector/pdf/testdata/corpus/rfc-7234-caching/poppler-references/source.pdftotext.txt
//
// Corpus PDF licensing: source.pdf is RFC 7234 "Hypertext Transfer
// Protocol (HTTP/1.1): Caching" by R. Fielding, M. Nottingham, J.
// Reschke (eds.), June 2014. Licensed under the IETF Trust Legal
// Provisions (TLP) — redistribution permitted subject to TLP terms
// (see Section 4 of "Legal Provisions Relating to IETF Documents",
// https://trustee.ietf.org/license-info). NOT public-domain.
//
//	Source: https://www.rfc-editor.org/rfc/rfc7234.txt
//	PDF rendering: rfc-editor.org PDF version.
func TestDecode_PopplerCrossValidation(t *testing.T) {
	// Not t.Parallel() — test reads ~110KB PDF + 60KB reference and
	// runs full doc-scope decode. Keep it serialized to avoid
	// noise from concurrent stress.
	ctx, err := internalpdf.LoadFile("../testdata/corpus/rfc-7234-caching/source.pdf")
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	defer ctx.Close()

	resolver := font.NewDocResolver(ctx)
	var got strings.Builder
	for i := 0; i < ctx.PageCount(); i++ {
		page, err := ctx.Page(i)
		if err != nil {
			t.Fatalf("Page(%d): %v", i, err)
		}
		runs, err := text.ExtractRuns(page)
		if err != nil {
			t.Fatalf("ExtractRuns(%d): %v", i, err)
		}
		wrapped := make([]font.Run, len(runs))
		for j := range runs {
			wrapped[j] = runAdapter{r: &runs[j]}
		}
		if err := resolver.DecodePage(wrapped, page); err != nil {
			t.Fatalf("DecodePage(%d): %v", i, err)
		}
		for _, r := range runs {
			got.WriteString(r.Text)
			got.WriteString(" ")
		}
	}

	refBytes, err := os.ReadFile("../testdata/corpus/rfc-7234-caching/poppler-references/source.pdftotext.txt")
	if err != nil {
		t.Fatalf("read reference: %v", err)
	}

	gotWords := strings.Fields(got.String())
	wantWords := strings.Fields(string(refBytes))
	t.Logf("got %d words, want %d words", len(gotWords), len(wantWords))

	dist := wordEditDistanceRatio(gotWords, wantWords)
	t.Logf("word-level edit distance: %.4f", dist)
	if dist > 0.05 {
		t.Errorf("word-level edit distance %.4f > 0.05 (DoD ceiling)", dist)
	}
}

// wordEditDistanceRatio computes Levenshtein distance over word
// sequences and returns it as a fraction of max(len(a), len(b)).
// For long inputs we cap the distance computation at len(b)+len(a)
// (early-exit when it gets above the threshold) — full O(N*M)
// matrix would be ~5MB for 2k×2k token comparisons, which is fine.
func wordEditDistanceRatio(a, b []string) float64 {
	if len(a) == 0 && len(b) == 0 {
		return 0
	}
	maxLen := max(len(b), len(a))
	if maxLen == 0 {
		return 0
	}
	dist := wordLevenshtein(a, b)
	return float64(dist) / float64(maxLen)
}

// wordLevenshtein is a classic two-row Levenshtein over token slices.
// Allocates 2×(min+1) ints; runs in O(N×M) time.
func wordLevenshtein(a, b []string) int {
	if len(a) == 0 {
		return len(b)
	}
	if len(b) == 0 {
		return len(a)
	}
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := 0; j <= len(b); j++ {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min3(
				prev[j]+1,      // deletion
				cur[j-1]+1,     // insertion
				prev[j-1]+cost, // substitution
			)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}

func min3(a, b, c int) int {
	if a < b {
		if a < c {
			return a
		}
		return c
	}
	if b < c {
		return b
	}
	return c
}
