package pdf_test

// accuracy_test.go: corpus walker + 6-metric harness driver for the
// T9 validation corpus. Default-on (no build tag). Walks
// testdata/corpus/<fixture>/, opens source.pdf via pdf.OpenFile, emits
// chunks via doc.Chunks, and scores against per-fixture-merged
// thresholds from chunks.golden.json.
//
// Per absorbed reviewer finding T3#1: discoverFixtures honors a
// `.skip` marker file in the fixture directory — fixtures awaiting
// human curation use this to defer without dropping the directory.
//
// The metric scoring functions live in accuracy_metrics_test.go (peer
// file) per absorbed finding T3#3 — keeps each file under the 300-line
// cap.

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf"
)

// corpusRoot is the relative path (from package dir) to the corpus
// directory. Each subdirectory is one fixture.
const corpusRoot = "testdata/corpus"

// fixtureCase describes one fixture discovered under corpusRoot.
type fixtureCase struct {
	Name               string // basename of the fixture directory
	Dir                string
	SourcePath         string // <Dir>/source.pdf
	ChunksGoldenPath   string // <Dir>/chunks.golden.json
	SectionsGoldenPath string // <Dir>/sections.golden.json (may not exist)
	SkipMarkerPath     string // <Dir>/.skip when present
	SkipReason         string // contents of .skip when present
}

// discoverFixtures walks corpusRoot and returns one fixtureCase per
// subdirectory. Subdirs with a `.skip` marker are emitted with
// SkipReason populated; the harness handles the skip via t.Skipf so
// the case appears in the test list under -v.
//
// Subdirs missing source.pdf without a .skip are silently dropped
// (poppler-references/ exception subdirectory, in-progress drafts).
func discoverFixtures(t *testing.T) []fixtureCase {
	t.Helper()
	if _, err := os.Stat(corpusRoot); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("stat %s: %v", corpusRoot, err)
	}
	entries, err := os.ReadDir(corpusRoot)
	if err != nil {
		t.Fatalf("readdir %s: %v", corpusRoot, err)
	}
	var cases []fixtureCase
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		fc := newFixtureCase(corpusRoot, e.Name())
		if reason, err := os.ReadFile(fc.SkipMarkerPath); err == nil {
			fc.SkipReason = strings.TrimSpace(string(reason))
			if fc.SkipReason == "" {
				fc.SkipReason = "(no reason given in .skip)"
			}
			cases = append(cases, fc)
			continue
		}
		// Without .skip, source.pdf + chunks.golden.json are required.
		if _, err := os.Stat(fc.SourcePath); err != nil {
			if isNotExist(err) {
				t.Logf("fixture %s: no source.pdf and no .skip — silently dropped", e.Name())
				continue
			}
			t.Fatalf("stat %s: %v", fc.SourcePath, err)
		}
		if _, err := os.Stat(fc.ChunksGoldenPath); err != nil {
			if isNotExist(err) {
				t.Logf("fixture %s: missing chunks.golden.json — silently dropped", e.Name())
				continue
			}
			t.Fatalf("stat %s: %v", fc.ChunksGoldenPath, err)
		}
		cases = append(cases, fc)
	}
	sort.Slice(cases, func(i, j int) bool { return cases[i].Name < cases[j].Name })
	return cases
}

func newFixtureCase(root, name string) fixtureCase {
	dir := filepath.Join(root, name)
	return fixtureCase{
		Name:               name,
		Dir:                dir,
		SourcePath:         filepath.Join(dir, "source.pdf"),
		ChunksGoldenPath:   filepath.Join(dir, "chunks.golden.json"),
		SectionsGoldenPath: filepath.Join(dir, "sections.golden.json"),
		SkipMarkerPath:     filepath.Join(dir, ".skip"),
	}
}

func isNotExist(err error) bool {
	return err != nil && (os.IsNotExist(err) || strings.Contains(err.Error(), fs.ErrNotExist.Error()))
}

// TestAccuracy_AllFixtures is the top-level corpus harness entry
// point. Default-on; runs against every discovered fixture under
// testdata/corpus/. Empty corpus → t.Skipf (clean pass).
func TestAccuracy_AllFixtures(t *testing.T) {
	t.Parallel()
	cases := discoverFixtures(t)
	if len(cases) == 0 {
		t.Skipf("no fixtures under %s — corpus empty (test passes)", corpusRoot)
	}
	for _, fc := range cases {
		t.Run(fc.Name, func(t *testing.T) {
			t.Parallel()
			if fc.SkipReason != "" {
				t.Skipf("fixture %s marked .skip: %s", fc.Name, fc.SkipReason)
			}
			runAccuracyFixture(t, fc)
		})
	}
}

// runAccuracyFixture loads, opens, chunks, scores, and threshold-checks
// one fixture.
func runAccuracyFixture(t *testing.T, fc fixtureCase) {
	t.Helper()
	gf := loadGoldenChunks(t, fc.ChunksGoldenPath)
	thr := mergedThresholds(gf.Thresholds)

	doc, err := pdf.OpenFile(fc.SourcePath)
	if err != nil {
		t.Fatalf("OpenFile(%s): %v", fc.SourcePath, err)
	}
	defer doc.Close()

	actual, err := doc.Chunks(pdf.ChunkOptions{
		Mode: pdf.ChunkModeParagraph,
		LayoutParams: pdf.LayoutParams{
			LineMargin: 0.4, CharMargin: 2.0, WordMargin: 0.1,
			BoxesFlow: 0.5, ParagraphGapRatio: 1.6,
		},
		ClassifyParams: pdf.ClassifyParams{
			HeadingFontSizeRatio: 1.15, HeadingMinBoldOnly: true,
			CodeMonospaceRatio: 0.8,
		},
		SkipHeadersFooters: true, // graceful no-op until T5 lands
		MinChunkChars:      0,
	})
	if err != nil {
		t.Fatalf("doc.Chunks: %v", err)
	}

	m := scoreMetrics(actual, gf.Chunks)
	t.Logf("fixture=%s actual=%d golden=%d chunkCountDelta=%.3f boundaryIoU=%.3f classAcc=%.3f headLvlAgree=%.3f kendallDiv=%.3f textLev=%.3f",
		fc.Name, m.ActualCount, m.GoldenCount, m.ChunkCountDelta, m.BoundaryIoU,
		m.ClassificationAccuracy, m.HeadingLevelAgreement, m.ReadingOrderKendallTauDivergence,
		m.TextSimilarityLevenshtein)

	enforceThresholds(t, fc.Name, m, thr)
}

// enforceThresholds calls t.Errorf with a diagnostic naming the
// metric, measured value, and threshold for any out-of-band metric.
// Required by criterion fc5aaae1af55e8088e96cebb17387f80.
func enforceThresholds(t *testing.T, name string, m metricsBundle, thr resolvedThresholds) {
	t.Helper()
	if m.ChunkCountDelta > thr.ChunkCountDeltaMax {
		t.Errorf("%s: chunkCountDelta %.3f > threshold %.3f (actual=%d golden=%d)",
			name, m.ChunkCountDelta, thr.ChunkCountDeltaMax, m.ActualCount, m.GoldenCount)
	}
	if m.BoundaryIoU < thr.BoundaryIoUMin {
		t.Errorf("%s: boundaryIoU %.3f < threshold %.3f", name, m.BoundaryIoU, thr.BoundaryIoUMin)
	}
	if m.GoldenCount > 0 && m.ClassificationAccuracy < thr.ClassificationAccuracyMin {
		t.Errorf("%s: classificationAccuracy %.3f < threshold %.3f",
			name, m.ClassificationAccuracy, thr.ClassificationAccuracyMin)
	}
	if m.GoldenHeadingCount > 0 && m.HeadingLevelAgreement < thr.HeadingLevelAgreementMin {
		t.Errorf("%s: headingLevelAgreement %.3f < threshold %.3f (golden headings=%d)",
			name, m.HeadingLevelAgreement, thr.HeadingLevelAgreementMin, m.GoldenHeadingCount)
	}
	if m.ReadingOrderKendallTauDivergence > thr.ReadingOrderKendallTauMax {
		t.Errorf("%s: readingOrderKendallTauDivergence %.3f > threshold %.3f",
			name, m.ReadingOrderKendallTauDivergence, thr.ReadingOrderKendallTauMax)
	}
	if m.TextSimilarityLevenshtein > thr.TextSimilarityLevenshteinMax {
		t.Errorf("%s: textSimilarityLevenshtein %.3f > threshold %.3f",
			name, m.TextSimilarityLevenshtein, thr.TextSimilarityLevenshteinMax)
	}
}
