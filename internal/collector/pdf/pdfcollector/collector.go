// SPDX-License-Identifier: Apache-2.0

package pdfcollector

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/collector"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/chunk"
	"github.com/fulminate-io/knowledge-mcp/internal/collectorwire"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

func init() {
	collector.Register(&PDFCollector{})
}

// PDFCollector reads a single PDF file and emits typed raw-graph
// records into a per-source graph under kgtypes.GraphPDFRaw. See package
// doc for the emitted node shape.
type PDFCollector struct{}

// Name returns the collector identifier used for registry lookup.
func (c *PDFCollector) Name() string { return "pdf" }

// Collect opens the PDF at id, chunks it with chunk.DefaultOptions, and
// returns a CollectResult targeting kgtypes.GraphPDFRaw keyed by a slug
// derived from id. id MUST be an absolute filesystem path; relative
// paths are rejected so the per-source graph name remains stable across
// invocations regardless of caller cwd.
//
// ErrEncrypted from the underlying parser is propagated wrapped in a
// context error; errors.Is(err, pdf.ErrEncrypted) still matches so
// callers can branch on encrypted-input failure cleanly.
func (c *PDFCollector) Collect(
	_ context.Context,
	id string,
	_ collector.CollectOptions,
) (*collectorwire.CollectResult, error) {
	if id == "" {
		return nil, fmt.Errorf("pdf collector: id is required (absolute path to PDF)")
	}
	if !filepath.IsAbs(id) {
		return nil, fmt.Errorf("pdf collector: id %q must be an absolute path", id)
	}

	doc, err := pdf.OpenFile(id)
	if err != nil {
		// pdf.OpenFile -> pdfcpu wrapper already names the path; another
		// "open %q" wrap here produces a duplicate "open" prefix.
		return nil, fmt.Errorf("pdf collector: %w", err)
	}
	defer doc.Close()

	chunks, err := doc.Chunks(defaultChunkOptions())
	if err != nil {
		return nil, fmt.Errorf("pdf collector: chunk %q: %w", id, err)
	}

	meta := doc.Metadata()
	nodes, edges := emit(meta, id, chunks)

	return &collectorwire.CollectResult{
		GraphType: kgtypes.GraphPDFRaw,
		GraphName: sourceSlug(id),
		Nodes:     nodes,
		Edges:     edges,
	}, nil
}

// defaultChunkOptions is the canonical Build configuration for the
// collector. chunk.DefaultOptions already pins Mode=ModeSection and
// every other field; we intentionally do not re-export the underlying
// LayoutParams / ClassifyParams so a single source-of-truth flip in the
// chunk package propagates here without coordination.
func defaultChunkOptions() pdf.ChunkOptions {
	return chunk.DefaultOptions
}

// sourceSlug derives a per-source graph name from the input path. The
// slug is "<basename-without-extension>-<sha256[:8]>", which is stable
// across invocations on the same path and unique-enough across distinct
// documents that share a basename. Re-collecting the same file produces
// the same graph name and overwrites the prior contents.
func sourceSlug(path string) string {
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	base = sanitizeSlug(base)
	if base == "" {
		base = "pdf"
	}
	sum := sha256.Sum256([]byte(path))
	return fmt.Sprintf("%s-%s", base, hex.EncodeToString(sum[:4]))
}

// sanitizeSlug replaces any character that is not [a-z0-9-_] with '-'
// and lowercases the result. Mirrors the slug-shape used by the web
// collector's Source field so on-disk files have a uniform feel.
func sanitizeSlug(s string) string {
	s = strings.ToLower(s)
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-', c == '_':
			out = append(out, c)
		default:
			out = append(out, '-')
		}
	}
	// Collapse runs of '-' so a path like "Designing Data..." doesn't
	// produce "designing-data---".
	collapsed := make([]byte, 0, len(out))
	prevDash := false
	for _, c := range out {
		if c == '-' {
			if prevDash {
				continue
			}
			prevDash = true
		} else {
			prevDash = false
		}
		collapsed = append(collapsed, c)
	}
	return strings.Trim(string(collapsed), "-")
}
