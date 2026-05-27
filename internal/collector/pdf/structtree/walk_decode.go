package structtree

import (
	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/font"
	internalpdf "github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/internal/pdfcpu"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/text"
)

// extractRunsForPage extracts the page's TextRuns through
// internalpdf.PageObject and decodes glyphs to UTF-8 via font.Decode.
// This mirrors the chain in collector/pdf/page.go's TextRuns method
// (text.ExtractRuns → font.Decode) — Page.TextRuns lives in the
// top-level pdf package, which structtree cannot import without a
// cycle. The duplication is small and stable.
func extractRunsForPage(ctx *internalpdf.Context, pageIndex int) ([]text.TextRun, error) {
	if pageIndex < 0 || pageIndex >= ctx.PageCount() {
		return nil, nil
	}
	page, err := ctx.Page(pageIndex)
	if err != nil {
		return nil, err
	}
	runs, err := text.ExtractRuns(page)
	if err != nil {
		return nil, err
	}
	if len(runs) == 0 {
		return runs, nil
	}
	wrapped := make([]font.Run, len(runs))
	for i := range runs {
		wrapped[i] = textRunAdapter{r: &runs[i]}
	}
	if err := font.Decode(wrapped, page); err != nil {
		return nil, err
	}
	return runs, nil
}

// textRunAdapter is the structtree-package's font.Run adapter. The
// shape is duplicated from collector/pdf/page.go's adapter for the
// same reason: structtree cannot import the top-level pdf package
// (import cycle), so the adapter ships here in parallel. Decoded
// glyphs land on the underlying TextRun via SetText.
type textRunAdapter struct {
	r *text.TextRun
}

func (a textRunAdapter) GlyphsCopy() []uint16 { return a.r.Glyphs }
func (a textRunAdapter) FontKeyValue() string { return a.r.FontKey }
func (a textRunAdapter) FontResourcesHint() internalpdf.FormResources {
	return a.r.FontResourcesHint()
}
func (a textRunAdapter) SetText(s string) { a.r.Text = s }
func (a textRunAdapter) SetCharFlags(f []uint8) {
	if len(f) == 0 {
		return
	}
	if len(a.r.CharFlags) == len(f) {
		for i, b := range f {
			a.r.CharFlags[i] |= b
		}
		return
	}
	a.r.CharFlags = f
}
