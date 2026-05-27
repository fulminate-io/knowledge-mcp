package pdfcpu

import (
	"errors"
	"fmt"
)

// XObjectKind is the value of an XObject's /Subtype entry. T2 uses
// it to discriminate between Form XObjects (carry their own content
// stream — log+skip), Image XObjects (silently skip), and any other
// subtype (silently skip).
type XObjectKind string

// Known XObject subtypes per PDF 32000-1:2008 §8.10.
const (
	XObjectForm  XObjectKind = "Form"
	XObjectImage XObjectKind = "Image"
)

// XObjectKind resolves the named XObject in the page's
// /Resources/XObject subdict and returns its /Subtype value, or
// ("", nil) when:
//   - the page has no /Resources entry,
//   - /Resources has no /XObject subdict,
//   - the XObject subdict has no entry for `name`,
//   - the resolved entry has no /Subtype.
//
// The walker treats ("", nil) as "unknown XObject; skip" so
// malformed PDFs that name non-existent XObjects don't break
// extraction.
func (p *PageObject) XObjectKind(name string) (XObjectKind, error) {
	if p == nil || p.ctx == nil || p.ctx.inner == nil {
		return "", errors.New("pdfcpu wrapper: nil page or context")
	}
	xRefTable := p.ctx.inner.XRefTable
	if xRefTable == nil {
		return "", errors.New("pdfcpu wrapper: nil xref table")
	}

	_, _, attrs, err := p.ctx.inner.PageDict(p.index+1, false)
	if err != nil {
		return "", fmt.Errorf("pdfcpu wrapper: page %d dict: %w", p.index, err)
	}
	if attrs == nil || attrs.Resources == nil {
		return "", nil
	}
	xobjsObj, ok := attrs.Resources.Find("XObject")
	if !ok {
		return "", nil
	}
	xobjsDict, err := xRefTable.DereferenceDict(xobjsObj)
	if err != nil {
		return "", fmt.Errorf("pdfcpu wrapper: dereference XObject subdict: %w", err)
	}
	if xobjsDict == nil {
		return "", nil
	}
	entryObj, ok := xobjsDict.Find(name)
	if !ok {
		return "", nil
	}
	// XObjects are streams. DereferenceStreamDict yields the stream
	// dict whose /Subtype field carries the kind.
	sd, _, err := xRefTable.DereferenceStreamDict(entryObj)
	if err != nil {
		return "", fmt.Errorf("pdfcpu wrapper: dereference XObject stream %q: %w", name, err)
	}
	if sd == nil {
		return "", nil
	}
	return XObjectKind(nameEntry(sd.Dict, "Subtype")), nil
}
