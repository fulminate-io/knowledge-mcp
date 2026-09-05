// SPDX-License-Identifier: Apache-2.0

package pdfcollector

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

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

	// The instant this collect ran, stamped on the document root so an
	// operator can tell a fresh raw graph from a stale one.
	collectedAt := time.Now().UTC()

	meta := doc.Metadata()
	nodes, edges, err := emit(meta, id, chunks, collectedAt)
	if err != nil {
		// The emitter refuses whole rather than shipping survivors: see emit's
		// doc for why a partial emission on this path would retire the rest of
		// the graph instead of leaving it alone.
		return nil, fmt.Errorf("pdf collector: %w", err)
	}

	return &collectorwire.CollectResult{
		GraphType: kgtypes.GraphPDFRaw,
		GraphName: SourceSlug(id),
		Nodes:     nodes,
		Edges:     edges,
		// REACHING THIS RETURN IS ITSELF THE PROOF OF A COMPLETE WALK, which is why
		// the assertion is unconditional here rather than derived from a census the
		// way the web collector's is. Every per-page failure in this path ERRORS
		// rather than skipping: chunk.Build returns nil plus an error at each of its
		// page-level call sites, its `continue` statements are reached only after a
		// nil error, doc.Chunks returns on that error and the Chunks call above
		// returns on it in turn. So a partially-read document cannot arrive here at
		// all — it left as an error. The assertion is what entitles the server to
		// treat this emission as the document's authoritative set and retire the
		// prior generation; without it the deletion phase stays disabled and every
		// re-collect accumulates.
		WalkComplete: true,
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

// SourceSlug derives a per-source graph name from the input path: the
// sanitized, extension-stripped BASENAME of the file and nothing else.
// A raw graph is named after the document it was collected from, so the
// name is the thing an operator already knows how to read.
//
// THE HASH SUFFIX IS GONE. This used to append a dash and eight hex
// characters of sha256 over the PATH, which made every name unreadable in
// order to keep two same-basename documents apart. Separating those two
// documents is now the collect-time collision refusal's job — see
// precheckRawCollect in the tools package, which reads the source recorded
// on the target graph's document root and refuses an incoming collect that
// came from a different file rather than merging into it or minting a
// suffix. Nothing here disambiguates, and nothing here needs to.
//
// THIS DECLARATION IS THE ONE PRODUCTION DERIVATION of a pdf raw graph's
// name. Its callers are PDFCollector.Collect above, which names the graph
// the collect actually writes; resolveRawSourceGraphName in the tools
// package (collect_recipe_extract.go), which turns a replay id that spells
// the absolute path into the graph name; and tools.CollectGateGraphName,
// the single dispatcher that predicts the name BEFORE the walk. A second
// implementation anywhere would be a second definition of the graph's
// identity and would drift silently.
//
// WHAT THIS FUNCTION DECIDES IS THE NAME, AND ONLY THE NAME — it performs no
// write, so it promises nothing about the contents on its own. Re-collecting the
// same file resolves to the same graph name, and the prior contents are then
// REPLACED by the server: a pdf graph is in the collector-managed full-replace
// set, so Finalize retires whatever the collect did not re-emit. That replacement
// is conditional on the walk assertion Collect makes above — a collect that
// reported an incomplete walk gets no deletion phase, and the graph would
// accumulate instead.
//
// IT IS EXPORTED BECAUSE THE COLLECT DISPATCH RESOLVES REPLAY IDS WITH IT: a
// recipe replay may name the absolute path the collect took rather than the
// slug, and the tools package turns one into the other by calling this. A
// second implementation there would be a second definition of the graph's
// identity and would drift silently, so this declaration is the only one.
func SourceSlug(path string) string {
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	base = sanitizeSlug(base)
	if base == "" {
		base = "pdf"
	}
	return base
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
