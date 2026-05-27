package pdfcpu

import (
	"errors"
	"fmt"

	pdftypes "github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

// StructTreeRootRef is the wrapper-package handle for a tagged-PDF
// /StructTreeRoot dictionary. Opaque to consumers — the structtree
// package navigates via methods so the underlying dict / xref table
// stay confined inside this package boundary.
type StructTreeRootRef struct {
	dict pdftypes.Dict
	xref *xrefBundle
}

// StructElemRef is the wrapper-package handle for a single tagged-PDF
// structure element (one /S "P", "H1", ... node). Like
// StructTreeRootRef, the underlying pdf-types dict and xref bundle
// stay unexported; callers go through methods.
type StructElemRef struct {
	dict pdftypes.Dict
	xref *xrefBundle
}

// xrefBundle pairs an XRefTable with the lazily-built page-indirect-ref
// cache used by StructElemRef.PageIndex. The bundle hangs off
// *Context.structXref so every StructTreeRootRef / StructElemRef
// produced from the same Context shares the same cache.
type xrefBundle struct {
	ctx        *Context
	pageByONum map[int]int // obj-number → 0-based page index; lazily filled
}

// StructTreeRoot returns the document's /StructTreeRoot dictionary as
// a typed reference, or (nil, nil) when the document is untagged
// (catalog has no /StructTreeRoot entry). Mirrors xobject_form.go's
// FormXObject(name) for the find-then-DereferenceDict cascade.
func (c *Context) StructTreeRoot() (*StructTreeRootRef, error) {
	if c == nil || c.inner == nil {
		return nil, errors.New("pdfcpu wrapper: nil context")
	}
	xRefTable := c.inner.XRefTable
	if xRefTable == nil {
		return nil, errors.New("pdfcpu wrapper: nil xref table")
	}
	rootDict, err := xRefTable.Catalog()
	if err != nil {
		return nil, fmt.Errorf("pdfcpu wrapper: catalog: %w", err)
	}
	stObj, ok := rootDict.Find("StructTreeRoot")
	if !ok {
		return nil, nil
	}
	stDict, err := xRefTable.DereferenceDict(stObj)
	if err != nil {
		return nil, fmt.Errorf("pdfcpu wrapper: dereference StructTreeRoot: %w", err)
	}
	if stDict == nil {
		return nil, nil
	}
	bundle := c.ensureStructXref()
	return &StructTreeRootRef{dict: stDict, xref: bundle}, nil
}

// ensureStructXref returns the lazily-allocated xrefBundle that
// page-index lookups share. Created on first call.
func (c *Context) ensureStructXref() *xrefBundle {
	if c.structXref == nil {
		c.structXref = &xrefBundle{ctx: c}
	}
	return c.structXref
}

// KArray returns the /K entries on the /StructTreeRoot dict as a
// dereferenced object slice. Tolerates the single-dict shape allowed
// by PDF 32000-1:2008 §14.7.2 by wrapping it in a 1-element slice.
// Returns (nil, nil) when /K is absent.
func (r *StructTreeRootRef) KArray() ([]pdftypes.Object, error) {
	if r == nil || r.xref == nil || r.xref.ctx == nil || r.xref.ctx.inner == nil {
		return nil, errors.New("pdfcpu wrapper: nil StructTreeRootRef")
	}
	return dereferenceKArray(r.xref.ctx.inner.XRefTable, r.dict)
}

// ResolveStructElem dereferences an arbitrary /K entry value into a
// StructElemRef when the value is a dict (or an indirect ref to a
// dict). Returns (nil, nil) for non-dict values — bare integers (raw
// MCID), names, MCR/OBJR markers — those are handled via Kids() at
// the parent.
func (c *Context) ResolveStructElem(o pdftypes.Object) (*StructElemRef, error) {
	if c == nil || c.inner == nil || c.inner.XRefTable == nil {
		return nil, errors.New("pdfcpu wrapper: nil context")
	}
	d, err := c.inner.DereferenceDict(o)
	if err != nil {
		return nil, fmt.Errorf("pdfcpu wrapper: dereference StructElem: %w", err)
	}
	if d == nil {
		return nil, nil
	}
	return &StructElemRef{dict: d, xref: c.ensureStructXref()}, nil
}

// Type returns the /S entry (the structure-type name, e.g. "P", "H1",
// "Figure"). Empty string when absent.
func (s *StructElemRef) Type() string {
	if s == nil {
		return ""
	}
	if name := s.dict.NameEntry("S"); name != nil {
		return *name
	}
	return ""
}

// PageIndex returns the 0-based page index this structure element
// references via /Pg, or (-1, false) when /Pg is absent or the
// referenced page cannot be resolved. Lazy-builds an obj-number →
// page-index map shared across all StructElemRef instances spawned
// from the same Context.
func (s *StructElemRef) PageIndex() (int, bool) {
	if s == nil || s.xref == nil || s.xref.ctx == nil {
		return -1, false
	}
	pgRef := s.dict.IndirectRefEntry("Pg")
	if pgRef == nil {
		return -1, false
	}
	if err := s.xref.ensurePageMap(); err != nil {
		return -1, false
	}
	idx, ok := s.xref.pageByONum[pgRef.ObjectNumber.Value()]
	if !ok {
		return -1, false
	}
	return idx, true
}

// ensurePageMap fills pageByONum lazily on first access. After this
// call, every page's IndRef object-number maps to its 0-based page
// index. Falls back to an empty map (and nil error) when the document
// has zero pages.
func (b *xrefBundle) ensurePageMap() error {
	if b.pageByONum != nil {
		return nil
	}
	count := b.ctx.PageCount()
	if count == 0 {
		b.pageByONum = make(map[int]int)
		return nil
	}
	b.pageByONum = make(map[int]int, count)
	xRefTable := b.ctx.inner.XRefTable
	if xRefTable == nil {
		return errors.New("pdfcpu wrapper: nil xref table")
	}
	for i := 1; i <= count; i++ {
		ir, err := xRefTable.PageDictIndRef(i)
		if err != nil || ir == nil {
			continue
		}
		b.pageByONum[ir.ObjectNumber.Value()] = i - 1
	}
	return nil
}

// ActualText returns the value of /A << /ActualText (...) >> on the
// element, or "" when absent. /A may be either a single dict or an
// array of dicts (one per /O owner); ActualText is searched in any
// such dict. Per the ticket, ActualText is the only /A attribute T6
// honors.
func (s *StructElemRef) ActualText() string {
	if s == nil {
		return ""
	}
	a, ok := s.dict.Find("A")
	if !ok {
		return ""
	}
	if t := actualTextFromObject(s.xref.ctx.inner.XRefTable, a); t != "" {
		return t
	}
	return ""
}

// Attributes returns a flat snapshot of /A entries beyond ActualText
// (e.g. "O"→"Layout", "BBox"→"[0 0 100 100]"). Best-effort string
// stringification via fmt.Sprintf %v on non-string scalars; ActualText
// is intentionally omitted so callers consume it via the dedicated
// method.
func (s *StructElemRef) Attributes() map[string]string {
	if s == nil {
		return nil
	}
	a, ok := s.dict.Find("A")
	if !ok {
		return nil
	}
	out := make(map[string]string)
	collectAttributes(s.xref.ctx.inner.XRefTable, a, out)
	if len(out) == 0 {
		return nil
	}
	return out
}

// Kids returns the /K entries on the element as typed Kid values, in
// /K-array order. A bare /K (not an array) is treated as a 1-element
// array. /K is optional; absence returns nil-with-nil-error.
func (s *StructElemRef) Kids() ([]Kid, error) {
	if s == nil || s.xref == nil || s.xref.ctx == nil || s.xref.ctx.inner == nil {
		return nil, errors.New("pdfcpu wrapper: nil StructElemRef")
	}
	xRefTable := s.xref.ctx.inner.XRefTable
	objs, err := dereferenceKArray(xRefTable, s.dict)
	if err != nil {
		return nil, err
	}
	if len(objs) == 0 {
		return nil, nil
	}
	parentPg := s.dict.IndirectRefEntry("Pg") // for /MCID-int kids inheriting /Pg
	parentPgIdx := -1
	if parentPg != nil {
		if err := s.xref.ensurePageMap(); err == nil {
			if idx, ok := s.xref.pageByONum[parentPg.ObjectNumber.Value()]; ok {
				parentPgIdx = idx
			}
		}
	}
	out := make([]Kid, 0, len(objs))
	for _, o := range objs {
		k, err := classifyKid(s.xref, o, parentPgIdx)
		if err != nil {
			return nil, err
		}
		if k != nil {
			out = append(out, k)
		}
	}
	return out, nil
}
