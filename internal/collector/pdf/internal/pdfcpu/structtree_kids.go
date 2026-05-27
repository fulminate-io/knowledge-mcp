package pdfcpu

import (
	"fmt"

	pdfmodel "github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	pdftypes "github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

// Kid is one element of a structure-element's /K array. The PDF spec
// (§14.7.2) allows three concrete shapes:
//   - a child structure element (dict with /Type /StructElem)
//   - an integer (raw MCID), or a marked-content reference dict
//     {/Type /MCR /MCID n /Pg <ref>}
//   - an object reference {/Type /OBJR ...} pointing at an annotation
//     or form field
//
// Concrete: KidStructElem, KidMCID, KidObjRef. Walk consumers
// type-switch on the dynamic type or call Kind().
type Kid interface {
	// Kind returns "struct" | "mcid" | "objref" for fast dispatch
	// without a type assertion.
	Kind() string
}

// KidStructElem is a child structure element. Ref is non-nil; the
// caller drives the recursive walk through Ref.Kids().
type KidStructElem struct {
	Ref *StructElemRef
}

// Kind reports "struct".
func (KidStructElem) Kind() string { return "struct" }

// KidMCID is a marked-content reference. ID is the /MCID integer.
// PageIndex is the 0-based page resolved either from the kid's /Pg
// (when the kid is a /MCR dict carrying its own /Pg) or inherited
// from the parent structure element's /Pg. -1 when neither is
// resolvable.
type KidMCID struct {
	ID        int
	PageIndex int
}

// Kind reports "mcid".
func (KidMCID) Kind() string { return "mcid" }

// KidObjRef is an /OBJR object reference (annotation, form-field).
// v1 walks through these without interpretation; their presence is
// recorded on the parent's Attributes map as "has_objref"="true".
type KidObjRef struct{}

// Kind reports "objref".
func (KidObjRef) Kind() string { return "objref" }

// dereferenceKArray pulls the /K entry off dict and returns it as a
// slice of dereferenced Object values. Tolerates the single-non-array
// shape per PDF §14.7.2 (a single dict-or-int /K stands for a 1-array)
// by wrapping the value in a slice. Returns (nil, nil) when /K is
// absent.
func dereferenceKArray(xRefTable *pdfmodel.XRefTable, dict pdftypes.Dict) ([]pdftypes.Object, error) {
	kObj, ok := dict.Find("K")
	if !ok {
		return nil, nil
	}
	// Dereference once so we know whether we landed on an array, a
	// dict, or a scalar.
	resolved, err := xRefTable.Dereference(kObj)
	if err != nil {
		return nil, fmt.Errorf("pdfcpu wrapper: dereference /K: %w", err)
	}
	if resolved == nil {
		return nil, nil
	}
	if arr, isArr := resolved.(pdftypes.Array); isArr {
		out := make([]pdftypes.Object, 0, len(arr))
		for _, o := range arr {
			out = append(out, o)
		}
		return out, nil
	}
	// Single dict / int / etc. — wrap as a 1-element slice and let
	// classifyKid sort the shape out per-element.
	return []pdftypes.Object{resolved}, nil
}

// classifyKid converts one /K entry value (already drawn from the
// array) into a Kid. Returns (nil, nil) for shapes not recognized so
// the caller can drop them without aborting the walk.
func classifyKid(bundle *xrefBundle, o pdftypes.Object, parentPgIdx int) (Kid, error) {
	if bundle == nil || bundle.ctx == nil || bundle.ctx.inner == nil {
		return nil, nil
	}
	xRefTable := bundle.ctx.inner.XRefTable
	resolved, err := xRefTable.Dereference(o)
	if err != nil {
		return nil, fmt.Errorf("pdfcpu wrapper: dereference kid: %w", err)
	}
	switch v := resolved.(type) {
	case pdftypes.Integer:
		// Bare MCID; page inherited from parent.
		return KidMCID{ID: v.Value(), PageIndex: parentPgIdx}, nil
	case pdftypes.Dict:
		return classifyKidDict(bundle, v, parentPgIdx)
	}
	return nil, nil
}

// classifyKidDict dispatches a dict-shaped /K entry. Possible /Type
// values: StructElem (child element), MCR (marked-content reference),
// OBJR (object reference). An untyped dict is treated as a structure
// element — pdfcpu validation accepts that shape, the spec recommends
// /Type but does not require it.
func classifyKidDict(bundle *xrefBundle, d pdftypes.Dict, parentPgIdx int) (Kid, error) {
	tp := ""
	if n := d.NameEntry("Type"); n != nil {
		tp = *n
	}
	switch tp {
	case "MCR":
		id := -1
		if i := d.IntEntry("MCID"); i != nil {
			id = *i
		}
		page := parentPgIdx
		if pg := d.IndirectRefEntry("Pg"); pg != nil {
			if err := bundle.ensurePageMap(); err == nil {
				if idx, ok := bundle.pageByONum[pg.ObjectNumber.Value()]; ok {
					page = idx
				}
			}
		}
		if id < 0 {
			return nil, nil
		}
		return KidMCID{ID: id, PageIndex: page}, nil
	case "OBJR":
		return KidObjRef{}, nil
	default:
		// "StructElem" or untyped dict — treat as a structure element.
		return KidStructElem{Ref: &StructElemRef{dict: d, xref: bundle}}, nil
	}
}

// actualTextFromObject extracts /ActualText from an /A attribute dict
// (or array of attribute dicts). PDF §14.9.4 allows /A to be either a
// single dict or an array of dicts (one per /O owner); we search both
// shapes. ActualText is typically a UTF-16BE string literal — pdfcpu
// returns string-literal values pre-decoded.
func actualTextFromObject(xRefTable *pdfmodel.XRefTable, o pdftypes.Object) string {
	resolved, err := xRefTable.Dereference(o)
	if err != nil || resolved == nil {
		return ""
	}
	switch v := resolved.(type) {
	case pdftypes.Dict:
		return actualTextFromDict(xRefTable, v)
	case pdftypes.Array:
		for _, item := range v {
			if d, err := xRefTable.DereferenceDict(item); err == nil && d != nil {
				if t := actualTextFromDict(xRefTable, d); t != "" {
					return t
				}
			}
		}
	}
	return ""
}

// actualTextFromDict reads /ActualText from a single attribute dict.
// Accepts both StringLiteral and HexLiteral (the spec allows either).
func actualTextFromDict(xRefTable *pdfmodel.XRefTable, d pdftypes.Dict) string {
	o, ok := d.Find("ActualText")
	if !ok {
		return ""
	}
	resolved, err := xRefTable.Dereference(o)
	if err != nil || resolved == nil {
		return ""
	}
	switch v := resolved.(type) {
	case pdftypes.StringLiteral:
		return decodePDFString(string(v))
	case pdftypes.HexLiteral:
		// HexLiteral.Bytes() decodes the hex pairs back to raw bytes.
		// PDF hex strings carrying Unicode are typically UTF-16BE with
		// the FE FF BOM; decodePDFString handles the BOM detection.
		if b, err := v.Bytes(); err == nil {
			return decodePDFString(string(b))
		}
	}
	return ""
}

// decodePDFString trims the BOM-prefixed UTF-16BE if present and
// returns the underlying text. pdfcpu's StringLiteral is the
// post-decoded string body (no enclosing parens), but UTF-16BE strings
// carry a U+FEFF BOM prefix (\xFE\xFF) to signal the encoding.
func decodePDFString(s string) string {
	if len(s) >= 2 && s[0] == '\xFE' && s[1] == '\xFF' {
		return decodeUTF16BE([]byte(s[2:]))
	}
	return s
}

// decodeUTF16BE decodes a big-endian UTF-16 byte slice into a Go
// string. Stops at the first half-pair (odd length) — the resulting
// string contains the bytes successfully decoded so far.
func decodeUTF16BE(b []byte) string {
	out := make([]rune, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		out = append(out, rune(uint16(b[i])<<8|uint16(b[i+1])))
	}
	return string(out)
}

// collectAttributes flattens an /A entry (single dict or array of
// dicts) into out, skipping the /ActualText key (consumed by the
// dedicated accessor) and stringifying scalar values via fmt.Sprintf
// %v. Unsupported shapes are silently skipped.
func collectAttributes(xRefTable *pdfmodel.XRefTable, o pdftypes.Object, out map[string]string) {
	resolved, err := xRefTable.Dereference(o)
	if err != nil || resolved == nil {
		return
	}
	switch v := resolved.(type) {
	case pdftypes.Dict:
		flattenAttrDict(v, out)
	case pdftypes.Array:
		for _, item := range v {
			if d, err := xRefTable.DereferenceDict(item); err == nil && d != nil {
				flattenAttrDict(d, out)
			}
		}
	}
}

// flattenAttrDict copies every key/value from d into out except for
// "ActualText". Values are stringified through pdftypes scalar shapes
// and via fmt.Sprintf %v as the catch-all.
func flattenAttrDict(d pdftypes.Dict, out map[string]string) {
	for k, v := range d {
		if k == "ActualText" {
			continue
		}
		out[k] = stringifyPDFValue(v)
	}
}

// stringifyPDFValue renders a pdftypes.Object as a flat string for
// the Attributes map. Primitive scalars use their natural string form;
// arrays/dicts/refs fall back to fmt.Sprintf %v.
func stringifyPDFValue(o pdftypes.Object) string {
	switch v := o.(type) {
	case pdftypes.Name:
		return v.Value()
	case pdftypes.StringLiteral:
		return decodePDFString(string(v))
	case pdftypes.HexLiteral:
		if b, err := v.Bytes(); err == nil {
			return decodePDFString(string(b))
		}
		return string(v)
	case pdftypes.Integer:
		return fmt.Sprintf("%d", v.Value())
	case pdftypes.Float:
		return fmt.Sprintf("%g", v.Value())
	case pdftypes.Boolean:
		if v.Value() {
			return "true"
		}
		return "false"
	}
	return fmt.Sprintf("%v", o)
}
