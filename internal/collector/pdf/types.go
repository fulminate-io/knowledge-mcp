package pdf

import "time"

// Rect is the public-facing rectangle type used everywhere a layout-aware
// region is exposed by the top-level package (Page.MediaBox, Block.BBox
// after T4, Chunk.BBox after T7).
//
// Rect is owned at the top level — sub-packages (layout, structtree,
// chunk, internal/pdfcpu) duplicate the shape locally because aliasing
// back to pdf.Rect would create import cycles. Conversion happens at the
// public boundary (Page.MediaBox() does explicit field-by-field copy).
type Rect struct {
	X0, Y0, X1, Y1 float64
}

// Metadata is the document-level metadata exposed by Document.Metadata().
// All fields read from the PDF's Info dictionary; missing entries leave
// the corresponding field at its zero value (empty string / zero
// time.Time). T1 fills every field via the internal/pdfcpu wrapper.
type Metadata struct {
	// Title is the /Title entry from the document Info dict.
	Title string

	// Author is the /Author entry from the document Info dict.
	Author string

	// Subject is the /Subject entry from the document Info dict.
	Subject string

	// Keywords is the /Keywords entry from the document Info dict, in
	// its original (typically comma- or semicolon-separated) form.
	Keywords string

	// Producer is the /Producer entry — the PDF generator software
	// (e.g. "pdfTeX-1.40.21").
	Producer string

	// Creator is the /Creator entry — the upstream authoring app (e.g.
	// "TeX", "Microsoft Word").
	Creator string

	// CreationDate is the /CreationDate entry parsed to time.Time.
	// Zero time.Time when the entry is absent or unparsable.
	CreationDate time.Time

	// ModDate is the /ModDate entry parsed to time.Time. Zero
	// time.Time when the entry is absent or unparsable.
	ModDate time.Time
}
