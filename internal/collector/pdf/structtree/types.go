package structtree

// Rect is the structtree-package's local copy of a PDF rectangle. Like
// the layout / internal/pdfcpu copies, it duplicates pdf.Rect to avoid
// the import cycle that would result from depending on the top-level
// pdf package.
type Rect struct {
	X0, Y0, X1, Y1 float64
}

// Element is a single node in the PDF structure tree (tagged-PDF
// /StructTreeRoot). T1 pins the 6-field surface; T6 fills the values
// during structure-tree traversal.
type Element struct {
	// Type is the structure-tree role string (the /S value, e.g. "P",
	// "H1", "Figure", "TR"). Empty for the synthetic root that holds
	// the document's top-level children.
	Type string

	// Children are the child elements in document order.
	Children []*Element

	// Page is the 0-indexed page the element's content lives on.
	// Negative when the element spans multiple pages or is purely
	// structural (the synthetic root).
	Page int

	// BBox is the bounding box that encloses every marked-content
	// region the element references. Zero rect when no MCIDs exist.
	BBox Rect

	// MCIDs is the marked-content-ID list referenced by this element.
	// Used by T6 to map structure-tree elements onto extracted text
	// runs (matched against text.TextRun.MCID).
	MCIDs []int

	// Attrs is a free-form key/value attribute map (e.g.
	// "ListNumbering"→"Decimal", "ColSpan"→"2"). Empty when the
	// structure-tree node carries no /A or /A entries.
	Attrs map[string]string
}
