package pdfcpu

import (
	"errors"
	"fmt"
	"time"

	pdftypes "github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

// Rect is the wrapper-package's local copy of a PDF rectangle. The public
// pdf.Rect mirrors this struct field-for-field; conversion happens at the
// boundary in the top-level pdf package. The duplication is deliberate:
// importing the top-level package from internal/pdfcpu would create a
// circular import.
type Rect struct {
	X0, Y0, X1, Y1 float64
}

// PageObject is the per-page handle. T1 fills only Index / MediaBox /
// Rotation; later tickets extend with content-stream access (T2),
// font-dict access (T3), structure-tree element lookup (T6).
type PageObject struct {
	ctx       *Context
	index     int           // 0-indexed page number (1-based at the pdfcpu boundary)
	mediaBox  Rect          // resolved at construction
	rotation  int           // resolved at construction
	resources FormResources // /Resources dict, cached at construction
	hasAttrs  bool          // true when PageDict returned non-nil attrs at construction
}

// PageCount returns the page count of the loaded document. Pulled from
// pdfcpu's XRefTable.PageCount field (set during ReadContext).
func (c *Context) PageCount() int {
	if c == nil || c.inner == nil || c.inner.XRefTable == nil {
		return 0
	}
	return c.inner.PageCount
}

// Page returns a PageObject for the 0-indexed page i. The pdfcpu API uses
// 1-based indexing internally; the +1 conversion happens here. Errors:
// out-of-range index, or pdfcpu PageDict resolution failure.
func (c *Context) Page(i int) (*PageObject, error) {
	if c == nil || c.inner == nil {
		return nil, errors.New("pdfcpu wrapper: nil context")
	}
	count := c.PageCount()
	if i < 0 || i >= count {
		return nil, fmt.Errorf("pdfcpu wrapper: page %d out of range (count=%d)", i, count)
	}
	_, _, attrs, err := c.inner.PageDict(i+1, false)
	if err != nil {
		return nil, fmt.Errorf("pdfcpu wrapper: page %d dict: %w", i, err)
	}
	p := &PageObject{ctx: c, index: i}
	if attrs != nil {
		p.hasAttrs = true
		if attrs.MediaBox != nil {
			p.mediaBox = Rect{
				X0: attrs.MediaBox.LL.X,
				Y0: attrs.MediaBox.LL.Y,
				X1: attrs.MediaBox.UR.X,
				Y1: attrs.MediaBox.UR.Y,
			}
		}
		p.rotation = attrs.Rotate
		// Cache the /Resources dict so subsequent ResolvedFont /
		// FontResourceInResources calls don't re-walk the page tree.
		// The page tree walk is O(P) per call — caching converts the
		// per-font loop from O(P × N) to O(P + N) per page.
		p.resources = attrs.Resources
	}
	return p, nil
}

// Index returns the 0-indexed page number.
func (p *PageObject) Index() int { return p.index }

// MediaBox returns the resolved media box for the page. Zero value if the
// PDF declared no MediaBox (rare but legal under inheritance rules; pdfcpu
// fills the inherited value during PageDict).
func (p *PageObject) MediaBox() Rect { return p.mediaBox }

// Rotation returns the page rotation in degrees (0/90/180/270).
func (p *PageObject) Rotation() int { return p.rotation }

// Context returns the document Context that owns this page. Required by
// the document-scoped font resolver (collector/pdf/font NewDocResolver):
// resolving a font dict can require looking up indirect refs on the
// document's xref table, which lives on the Context, not the page. The
// resolver pins ctx (instead of a single page) so its sync.Map cache
// can hit across all pages of the same document. Mirrors the shape of
// the Index() and Rotation() accessors above.
func (p *PageObject) Context() *Context { return p.ctx }

// IsTagged reports whether the document declares a structure tree
// (/StructTreeRoot in the catalog). Pdfcpu sets XRefTable.Tagged during
// validation.
func (c *Context) IsTagged() bool {
	if c == nil || c.inner == nil || c.inner.XRefTable == nil {
		return false
	}
	return c.inner.Tagged
}

// --- Info-dict getters --------------------------------------------------
//
// pdfcpu lifts the document-information dictionary onto XRefTable directly
// (Title, Subject, Author, Creator, Producer, CreationDate, ModDate as
// raw strings, Keywords as a comma-separated string). The wrappers below
// pass the strings through unchanged; date strings are parsed via the
// shared types.DateTime helper which understands PDF date format
// (D:YYYYMMDDHHmmSS+TZ) plus a few popular out-of-spec variants.

// Title returns the Title entry from the document Info dict, or "".
func (c *Context) Title() string {
	return c.infoString(func() string { return c.inner.Title })
}

// Author returns the Author entry from the document Info dict, or "".
func (c *Context) Author() string {
	return c.infoString(func() string { return c.inner.Author })
}

// Subject returns the Subject entry from the document Info dict, or "".
func (c *Context) Subject() string {
	return c.infoString(func() string { return c.inner.Subject })
}

// Keywords returns the Keywords entry from the document Info dict, or "".
// PDFs typically encode this as a single comma-separated string; we leave
// splitting to the caller because PDF authors are inconsistent about
// delimiter choice.
func (c *Context) Keywords() string {
	return c.infoString(func() string { return c.inner.Keywords })
}

// Producer returns the Producer entry from the document Info dict, or "".
func (c *Context) Producer() string {
	return c.infoString(func() string { return c.inner.Producer })
}

// Creator returns the Creator entry from the document Info dict, or "".
func (c *Context) Creator() string {
	return c.infoString(func() string { return c.inner.Creator })
}

// CreationDate returns the parsed CreationDate, or the zero time if the
// PDF has no CreationDate or its format is unparsable.
func (c *Context) CreationDate() time.Time {
	return c.infoTime(func() string { return c.inner.XRefTable.CreationDate })
}

// ModDate returns the parsed ModDate, or the zero time.
func (c *Context) ModDate() time.Time {
	return c.infoTime(func() string { return c.inner.ModDate })
}

// infoString safely dereferences a string field on XRefTable through a
// thunk so a nil ctx / nil XRefTable returns "" instead of panicking.
func (c *Context) infoString(get func() string) string {
	if c == nil || c.inner == nil || c.inner.XRefTable == nil {
		return ""
	}
	return get()
}

// infoTime parses the raw PDF date string returned by `get`. Returns the
// zero time if the string is empty or unparsable. Uses pdfcpu's relaxed
// parser to accept popular out-of-spec variants alongside the canonical
// D:YYYYMMDDHHmmSS+HH'mm' form.
func (c *Context) infoTime(get func() string) time.Time {
	if c == nil || c.inner == nil || c.inner.XRefTable == nil {
		return time.Time{}
	}
	raw := get()
	if raw == "" {
		return time.Time{}
	}
	t, ok := pdftypes.DateTime(raw, true)
	if !ok {
		return time.Time{}
	}
	return t
}
