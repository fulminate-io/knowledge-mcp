package pdfcpu

import (
	"errors"
	"fmt"
	"io"

	pdfcpulib "github.com/pdfcpu/pdfcpu/pkg/pdfcpu"
)

// ContentStream returns the page's consolidated content-stream bytes,
// already decoded (FlateDecode / LZW / etc. are run by pdfcpu before the
// reader is constructed). T2's content-stream walker is the primary
// caller: it tokenises these bytes into PDF operators and emits TextRun
// values.
//
// The bytes here are PDF 32000-1:2008 §7.5 / §9 content-stream syntax —
// raw operators, operands, strings, and numbers. The wrapper does NOT
// pre-tokenise; that's the walker's job. We surface decoded bytes so the
// walker doesn't have to know about pdfcpu filter chains.
//
// Returns (nil, nil) — not an error — when the page has no content stream
// (a legal-but-unusual PDF: a blank page with no /Contents entry). The
// walker treats no-content as "zero TextRuns" rather than an error.
//
// Other error cases (missing page, pdfcpu read failure) surface as a
// wrapped error.
func (p *PageObject) ContentStream() ([]byte, error) {
	if p == nil || p.ctx == nil || p.ctx.inner == nil {
		return nil, errors.New("pdfcpu wrapper: nil page or context")
	}
	// pdfcpu's ExtractPageContent uses 1-based page numbering; our
	// PageObject.index is 0-based. Mirror the +1 convention used by
	// page.go's Page constructor.
	rdr, err := pdfcpulib.ExtractPageContent(p.ctx.inner, p.index+1)
	if err != nil {
		return nil, fmt.Errorf("pdfcpu wrapper: extract page content: %w", err)
	}
	if rdr == nil {
		// pdfcpu can return (nil, nil) for a page with no content.
		// Surface that as zero bytes so callers can handle uniformly.
		return nil, nil
	}
	bb, err := io.ReadAll(rdr)
	if err != nil {
		return nil, fmt.Errorf("pdfcpu wrapper: read content stream: %w", err)
	}
	return bb, nil
}
