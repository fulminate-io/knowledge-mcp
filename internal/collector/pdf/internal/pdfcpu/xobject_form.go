package pdfcpu

import (
	"errors"
	"fmt"

	pdftypes "github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

// FormResources is the wrapper-package's opaque handle for a Form
// XObject's /Resources dict. It is a type alias over the underlying
// pdfcpu types.Dict so that callers in collector/pdf/text/ (which are
// confined-by-rule from importing pdfcpu/pdfcpu directly) can carry
// the dict through the walker without taking a transitive dependency
// on pdfcpu's type package. ResolvedFontInResources / FontResourceIn
// Resources accept this type directly.
type FormResources = pdftypes.Dict

// FormXObject is the resolved view of a Form XObject (PDF 32000-1:2008
// §8.10.2). The walker consumes Bytes as a nested content stream
// inheriting the surrounding graphics state, optionally translated by
// Matrix and resolving font/XObject references against Resources (when
// non-nil; otherwise the Form inherits the parent's resources).
//
// ObjectKey is the indirect-reference key ("<obj> <gen> R") for the
// Form's underlying StreamDict, when the Form was reached via an
// IndirectRef (the common case). It uniquely identifies the Form
// across the document and lets the walker detect cyclic Do chains
// (Form A → Form B → Form A) regardless of resource-name reuse.
// Empty string when the entry was an inline (non-indirect) object —
// rare but legal; cycle detection is unnecessary in that case
// because inlined Forms can't reference themselves.
type FormXObject struct {
	Bytes     []byte        // decoded content stream
	Matrix    [6]float64    // /Matrix entry; identity when absent
	HasMatrix bool          // false → Matrix is the implicit identity
	Resources FormResources // /Resources entry; nil → inherit parent
	ObjectKey string        // indirect-ref identity for cycle detection
}

// FormXObject resolves the named Form XObject in the page's
// /Resources/XObject subdict, decoding its content stream and pulling
// the /Matrix and /Resources entries. Returns (nil, nil) when the
// named XObject does not exist OR is not a Form XObject.
func (p *PageObject) FormXObject(name string) (*FormXObject, error) {
	if p == nil || p.ctx == nil || p.ctx.inner == nil {
		return nil, errors.New("pdfcpu wrapper: nil page or context")
	}
	xRefTable := p.ctx.inner.XRefTable
	if xRefTable == nil {
		return nil, errors.New("pdfcpu wrapper: nil xref table")
	}

	_, _, attrs, err := p.ctx.inner.PageDict(p.index+1, false)
	if err != nil {
		return nil, fmt.Errorf("pdfcpu wrapper: page %d dict: %w", p.index, err)
	}
	if attrs == nil || attrs.Resources == nil {
		return nil, nil
	}
	xobjsObj, ok := attrs.Resources.Find("XObject")
	if !ok {
		return nil, nil
	}
	xobjsDict, err := xRefTable.DereferenceDict(xobjsObj)
	if err != nil {
		return nil, fmt.Errorf("pdfcpu wrapper: dereference XObject subdict: %w", err)
	}
	if xobjsDict == nil {
		return nil, nil
	}
	entryObj, ok := xobjsDict.Find(name)
	if !ok {
		return nil, nil
	}
	objectKey := indirectRefKey(entryObj)
	sd, _, err := xRefTable.DereferenceStreamDict(entryObj)
	if err != nil {
		return nil, fmt.Errorf("pdfcpu wrapper: dereference Form XObject %q: %w", name, err)
	}
	if sd == nil || nameEntry(sd.Dict, "Subtype") != string(XObjectForm) {
		return nil, nil
	}
	if err := sd.Decode(); err != nil {
		return nil, fmt.Errorf("pdfcpu wrapper: decode Form XObject %q: %w", name, err)
	}
	fx := &FormXObject{Bytes: append([]byte{}, sd.Content...), ObjectKey: objectKey}

	if mObj, ok := sd.Find("Matrix"); ok {
		if arr, err := xRefTable.DereferenceArray(mObj); err == nil && len(arr) == 6 {
			ok := true
			for i := range 6 {
				v, vok := numericFloat(arr[i])
				if !vok {
					ok = false
					break
				}
				fx.Matrix[i] = v
			}
			fx.HasMatrix = ok
		}
	}
	if rObj, ok := sd.Find("Resources"); ok {
		if rDict, err := xRefTable.DereferenceDict(rObj); err == nil {
			fx.Resources = rDict
		}
	}
	return fx, nil
}

// indirectRefKey returns "<obj> <gen> R" for an IndirectRef object,
// or "" for any other object type. Shared by FormXObject lookup and
// the font-dict lookup in font.go to extract a stable per-document
// identity for caching.
func indirectRefKey(o pdftypes.Object) string {
	if ir, ok := o.(pdftypes.IndirectRef); ok {
		return ir.PDFString()
	}
	if irp, ok := o.(*pdftypes.IndirectRef); ok && irp != nil {
		return irp.PDFString()
	}
	return ""
}

// numericFloat extracts a float from a PDF numeric (Integer or Float).
// Returns (0, false) for any other kind. Indirect references should be
// dereferenced by the caller before invoking.
func numericFloat(o pdftypes.Object) (float64, bool) {
	switch v := o.(type) {
	case pdftypes.Float:
		return v.Value(), true
	case pdftypes.Integer:
		return float64(v.Value()), true
	}
	return 0, false
}
