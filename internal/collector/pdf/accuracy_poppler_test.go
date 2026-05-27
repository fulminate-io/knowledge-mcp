//go:build pdfcompare_poppler

// accuracy_poppler_test.go: opt-in cross-validation harness comparing
// our chunker output against poppler's pdftotext per-fixture. Run via:
//
//	go test -tags pdfcompare_poppler ./collector/pdf/...
//
// CI does NOT include this build tag — the test is local-only and
// shells out to a poppler subprocess at test time. CI uses the
// pre-baked text fixtures committed under
// testdata/corpus/<fixture>/poppler-references/ where applicable
// (the canonical-shape exception documented in CONTRIBUTING.md).
//
// Mirror shape: collector/pdf/layout/pdfminer_xval_test.go:1-22
// build-tag-gated header.

package pdf_test

import (
	"bytes"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/testdata/accuracy"
)

// popplerExecutable returns the path to pdftotext. Defaults to
// looking up "pdftotext" in PATH; PDFCOMPARE_POPPLER overrides for
// non-default installs.
func popplerExecutable() string {
	if p := os.Getenv("PDFCOMPARE_POPPLER"); p != "" {
		return p
	}
	return "pdftotext"
}

// TestAccuracyPoppler_AllFixtures is the opt-in cross-validation
// harness. Loops over the same corpus that TestAccuracy_AllFixtures
// drives; per-fixture, runs pdftotext on source.pdf and compares
// against our concatenated chunk text via word-level Levenshtein.
//
// Threshold: 0.10 word-edit-distance. Looser than the default-on
// 0.05 text-similarity threshold because pdftotext's hard-newline
// + page-break formatting introduces token-level differences vs.
// our chunk-stream concatenation that aren't real divergences.
func TestAccuracyPoppler_AllFixtures(t *testing.T) {
	t.Parallel()
	cases := discoverFixtures(t)
	if len(cases) == 0 {
		t.Skipf("no fixtures under %s — corpus empty", corpusRoot)
	}
	if _, err := exec.LookPath(popplerExecutable()); err != nil {
		t.Skipf("pdftotext not on PATH (set PDFCOMPARE_POPPLER or install poppler): %v", err)
	}
	for _, fc := range cases {
		t.Run(fc.Name, func(t *testing.T) {
			t.Parallel()
			if fc.SkipReason != "" {
				t.Skipf("fixture %s marked .skip: %s", fc.Name, fc.SkipReason)
			}
			// .skip-poppler is honored only by the build-tag-gated
			// poppler harness — it lets fixtures that intentionally
			// exercise our-vs-poppler divergence (e.g. encoding edge
			// cases where our chunker resolves a glyph that poppler
			// drops to a fallback) opt out without affecting the
			// default-on harness.
			marker := fc.Dir + "/.skip-poppler"
			if reason, err := os.ReadFile(marker); err == nil {
				t.Skipf("fixture %s marked .skip-poppler: %s",
					fc.Name, strings.TrimSpace(string(reason)))
			}
			runPopplerFixture(t, fc)
		})
	}
}

// runPopplerFixture extracts text via our chunker AND via pdftotext,
// then compares.
func runPopplerFixture(t *testing.T, fc fixtureCase) {
	t.Helper()
	doc, err := pdf.OpenFile(fc.SourcePath)
	if err != nil {
		t.Fatalf("OpenFile(%s): %v", fc.SourcePath, err)
	}
	defer doc.Close()

	chunks, err := doc.Chunks(pdf.ChunkOptions{
		Mode: pdf.ChunkModeParagraph,
		LayoutParams: pdf.LayoutParams{
			LineMargin: 0.4, CharMargin: 2.0, WordMargin: 0.1,
			BoxesFlow: 0.5, ParagraphGapRatio: 1.6,
		},
		ClassifyParams: pdf.ClassifyParams{
			HeadingFontSizeRatio: 1.15, HeadingMinBoldOnly: true,
			CodeMonospaceRatio: 0.8,
		},
		SkipHeadersFooters: true,
	})
	if err != nil {
		t.Fatalf("doc.Chunks(%s): %v", fc.SourcePath, err)
	}
	var ours bytes.Buffer
	for _, c := range chunks {
		ours.WriteString(c.Text)
		ours.WriteByte(' ')
	}

	cmd := exec.Command(popplerExecutable(), "-layout", fc.SourcePath, "-")
	cmd.Stdin = nil
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Skipf("pdftotext failed on %s: %v; stderr=%q", fc.SourcePath, err, stderr.String())
	}

	ourWords := strings.Fields(ours.String())
	theirWords := strings.Fields(stdout.String())
	dist := accuracy.WordEditDistanceRatio(ourWords, theirWords)
	t.Logf("poppler-xval fixture=%s ours=%d words theirs=%d words wordEditDist=%.4f",
		fc.Name, len(ourWords), len(theirWords), dist)
	const popplerThreshold = 0.10
	if dist > popplerThreshold {
		t.Errorf("%s: word edit distance %.4f > poppler-xval threshold %.4f",
			fc.Name, dist, popplerThreshold)
	}
}
