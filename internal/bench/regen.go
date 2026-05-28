// SPDX-License-Identifier: Apache-2.0

// regen.go — golden-file regenerator for the validation corpus under
// collector/pdf/testdata/corpus/<fixture>. Reads <fixture>/source.pdf,
// runs the chunker in both ModeParagraph and ModeSection, and writes
// chunks.golden.json + sections.golden.json with the current chunker
// output. Used after deliberate chunker reshapes (per T9's
// CONTRIBUTING.md: "Future chunker changes deliberately reshape the
// golden in the same atomic commit").

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/chunk"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/layout"
)

// schemaVersion mirrors collector/pdf/accuracy_golden_test.go's
// schemaVersionCurrent. Kept inline so cmd/bench doesn't pull a test
// dependency.
const schemaVersion = 1

// regenGoldensInDir loads <dir>/source.pdf, chunks it twice (paragraph
// + section modes), and writes the two golden JSON files in place.
// Pre-existing per-fixture threshold overrides on chunks.golden.json
// are preserved.
func regenGoldensInDir(dir string) error {
	if !filepath.IsAbs(dir) {
		return fmt.Errorf("fixture dir must be absolute, got %q", dir)
	}
	pdfPath := filepath.Join(dir, "source.pdf")
	if _, err := os.Stat(pdfPath); err != nil {
		return fmt.Errorf("source.pdf not found at %s: %w", pdfPath, err)
	}

	// Read existing thresholds (if any) to preserve them across regen.
	prevThresholds, _ := readExistingThresholds(filepath.Join(dir, "chunks.golden.json"))

	doc, err := pdf.OpenFile(pdfPath)
	if err != nil {
		return fmt.Errorf("open %s: %w", pdfPath, err)
	}
	defer doc.Close()

	paragraphs, err := doc.Chunks(pdf.ChunkOptions{
		Mode:               chunk.ModeParagraph,
		LayoutParams:       layout.DefaultLayoutParams,
		ClassifyParams:     chunk.DefaultOptions.ClassifyParams,
		SkipHeadersFooters: true,
	})
	if err != nil {
		return fmt.Errorf("paragraph chunks: %w", err)
	}
	sections, err := doc.Chunks(pdf.ChunkOptions{
		Mode:               chunk.ModeSection,
		LayoutParams:       layout.DefaultLayoutParams,
		ClassifyParams:     chunk.DefaultOptions.ClassifyParams,
		SkipHeadersFooters: true,
	})
	if err != nil {
		return fmt.Errorf("section chunks: %w", err)
	}

	if err := writeChunksGolden(filepath.Join(dir, "chunks.golden.json"), paragraphs, prevThresholds); err != nil {
		return err
	}
	if err := writeSectionsGolden(filepath.Join(dir, "sections.golden.json"), sections); err != nil {
		return err
	}

	fmt.Printf("regen-goldens: %s\n", dir)
	fmt.Printf("  chunks.golden.json:   %d top-level chunks\n", len(paragraphs))
	fmt.Printf("  sections.golden.json: %d top-level sections\n", len(sections))
	return nil
}

// goldenChunk mirrors the schema in
// collector/pdf/accuracy_golden_test.go:goldenChunk.
type goldenChunkOut struct {
	Kind         string           `json:"kind"`
	Text         string           `json:"text"`
	HeadingLevel int              `json:"heading_level,omitempty"`
	PageRange    [2]int           `json:"page_range"`
	BBox         [4]float64       `json:"bbox,omitempty"`
	StructRole   string           `json:"struct_role,omitempty"`
	Children     []goldenChunkOut `json:"children,omitempty"`
}

// goldenSection mirrors goldenSection (title-only; recursive children).
type goldenSectionOut struct {
	Title     string             `json:"title"`
	Level     int                `json:"level"`
	PageRange [2]int             `json:"page_range"`
	BBox      [4]float64         `json:"bbox,omitempty"`
	Children  []goldenSectionOut `json:"children,omitempty"`
}

// goldenFileOut is the chunks.golden.json envelope.
type goldenFileOut struct {
	SchemaVersion int              `json:"schema_version"`
	Thresholds    json.RawMessage  `json:"thresholds,omitempty"`
	Chunks        []goldenChunkOut `json:"chunks"`
}

// goldenSectionFileOut is the sections.golden.json envelope.
type goldenSectionFileOut struct {
	SchemaVersion int                `json:"schema_version"`
	Sections      []goldenSectionOut `json:"sections"`
}

func writeChunksGolden(path string, chunks []pdf.Chunk, thresholds json.RawMessage) error {
	out := goldenFileOut{
		SchemaVersion: schemaVersion,
		Thresholds:    thresholds,
		Chunks:        toGoldenChunks(chunks),
	}
	return writeJSON(path, out)
}

func writeSectionsGolden(path string, sections []pdf.Chunk) error {
	out := goldenSectionFileOut{
		SchemaVersion: schemaVersion,
		Sections:      toGoldenSections(sections),
	}
	return writeJSON(path, out)
}

func toGoldenChunks(chunks []pdf.Chunk) []goldenChunkOut {
	if len(chunks) == 0 {
		return nil
	}
	out := make([]goldenChunkOut, len(chunks))
	for i, c := range chunks {
		out[i] = goldenChunkOut{
			Kind:         goldenKindString(c.Kind),
			Text:         c.Text,
			HeadingLevel: c.HeadingLevel,
			PageRange:    c.PageRange,
			BBox:         [4]float64{c.BBox.X0, c.BBox.Y0, c.BBox.X1, c.BBox.Y1},
			StructRole:   c.StructRole,
			Children:     toGoldenChunks(c.Children),
		}
	}
	return out
}

func toGoldenSections(chunks []pdf.Chunk) []goldenSectionOut {
	out := make([]goldenSectionOut, 0, len(chunks))
	for _, c := range chunks {
		// Only heading-rooted chunks become section nodes; non-heading
		// roots (orphan paragraphs at top level) are dropped from the
		// section view.
		if c.Kind != pdf.BlockHeading {
			continue
		}
		out = append(out, goldenSectionOut{
			Title:     c.Text,
			Level:     c.HeadingLevel,
			PageRange: c.PageRange,
			BBox:      [4]float64{c.BBox.X0, c.BBox.Y0, c.BBox.X1, c.BBox.Y1},
			Children:  toGoldenSections(c.Children),
		})
	}
	return out
}

// goldenKindString returns the layout.BlockKind string verbatim. The
// accuracy harness in collector/pdf/accuracy_metrics_test.go compares
// `string(actual.Kind) == golden.Kind`, so the golden file must use
// the canonical BlockKind values ("heading", "paragraph", "code_block",
// "list_item", "table") — NOT the abbreviated names ("code",
// "list-item") that an earlier draft of CONTRIBUTING.md mentioned.
// CONTRIBUTING.md will be reconciled when the corpus is next touched.
func goldenKindString(k layout.BlockKind) string {
	if k == "" {
		return "unknown"
	}
	return string(k)
}

func writeJSON(path string, v any) error {
	buf, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal %s: %w", path, err)
	}
	buf = append(buf, '\n')
	if err := os.WriteFile(path, buf, 0600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// readExistingThresholds returns the raw "thresholds" JSON object from
// an existing chunks.golden.json so we preserve per-fixture overrides
// across a regeneration. Returns nil when the file is absent or has no
// thresholds block.
func readExistingThresholds(path string) (json.RawMessage, error) {
	buf, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var envelope struct {
		Thresholds json.RawMessage `json:"thresholds"`
	}
	if err := json.Unmarshal(buf, &envelope); err != nil {
		return nil, err
	}
	return envelope.Thresholds, nil
}
