package pdf_test

import (
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf"
)

// chunksFixture is the canonical multi-line paragraph fixture used by
// the chunker integration test. T1 plan + T4 fixturelib step pinned
// this fixture; layout/integration_test.go also hard-codes it.
const chunksFixture = "testdata/t4_paragraph_simple.pdf"

// TestChunksIntegration_OpenFileToChunks_ParagraphMode drives the
// public OpenFile → Document.Chunks pipeline on a real fixture and
// asserts ≥1 non-heading Chunk with non-empty Text. Confirms the
// documentAdapter wiring + chunk.Build orchestration are end-to-end
// correct for ModeParagraph.
func TestChunksIntegration_OpenFileToChunks_ParagraphMode(t *testing.T) {
	t.Parallel()
	doc, err := pdf.OpenFile(chunksFixture)
	if err != nil {
		t.Fatalf("OpenFile(%q): %v", chunksFixture, err)
	}
	defer doc.Close()

	chunks, err := doc.Chunks(pdf.ChunkOptions{
		Mode: pdf.ChunkModeParagraph,
		LayoutParams: pdf.LayoutParams{
			LineMargin: 0.4, CharMargin: 2.0, WordMargin: 0.1, BoxesFlow: 0.5, ParagraphGapRatio: 1.6,
		},
		ClassifyParams: pdf.ClassifyParams{
			HeadingFontSizeRatio: 1.15,
			HeadingMinBoldOnly:   true,
			CodeMonospaceRatio:   0.8,
		},
		SkipHeadersFooters: true, // T5 not shipped — graceful no-op
		SkipFootnotes:      false,
		MinChunkChars:      0,
	})
	if err != nil {
		t.Fatalf("doc.Chunks: %v", err)
	}
	if len(chunks) == 0 {
		t.Fatalf("expected ≥1 Chunk from %q, got 0", chunksFixture)
	}
	foundBody := false
	for _, c := range chunks {
		if c.Kind != pdf.BlockHeading && len(c.Text) > 0 {
			foundBody = true
			break
		}
	}
	if !foundBody {
		t.Errorf("no non-heading Chunk with non-empty Text in %d chunks", len(chunks))
	}
}

// TestChunksIntegration_DefaultOptionsViaTopLevelAlias smoke-tests
// the convenience pattern: zero-value ChunkOptions + explicit Mode.
// Confirms the alias pipeline composes with the chunker and that
// zero-value LayoutParams / ClassifyParams flow through fine (chunk
// applies its own defaults internally where needed; layout/classify
// applyParamDefaults handle their own zero-value fill).
func TestChunksIntegration_DefaultOptionsViaTopLevelAlias(t *testing.T) {
	t.Parallel()
	doc, err := pdf.OpenFile(chunksFixture)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	defer doc.Close()
	if _, err := doc.Chunks(pdf.ChunkOptions{Mode: pdf.ChunkModeParagraph}); err != nil {
		t.Fatalf("doc.Chunks(zero+Mode): %v", err)
	}
}
