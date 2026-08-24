// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
)

// goSigTypeExpr renders one Go type expression as a TypeExpr: the composition
// shape with every resolvable leaf replaced by TypeExprLeafSep, plus those
// leaves' written spellings in left-to-right order.
//
// IT IS A PORT, NOT AN INVENTION. The composition grammar mirrors the measured
// prototype's typeKey/sigKey under
// ~/.knowledge/treesitter-parity-evidence/fanout/analyze/sigmatch.go, which is
// what produced the precision figures this work is scored against. The port is
// structural rather than literal: the prototype walks go/ast and resolves each
// leaf inline against its own index, while this walks tree-sitter nodes and
// DEFERS leaf resolution to the parser, because the chunker holds no declaration
// index. Preserving the prototype's shape rules verbatim is what preserves the
// measured precision; changing one of them silently moves the score.
//
// AN UNHANDLED KIND RENDERS A DISTINGUISHABLE LITERAL, "?"+kind, and never the
// empty string. An empty rendering makes two DIFFERENT type expressions compare
// equal, which is the false-match direction — the one direction a precision
// floor cannot tolerate. Tagging by kind is strictly narrower than the
// prototype's bare "?" and can only decline matches the prototype also declined
// on shape.
func goSigTypeExpr(node *sitter.Node, src []byte) TypeExpr {
	var b strings.Builder
	var leaves []string
	writeGoTypeExpr(&b, &leaves, node, src)
	return TypeExpr{Shape: b.String(), Leaves: leaves}
}

// writeGoTypeExpr is goSigTypeExpr's recursion, appending to one builder and one
// leaf slice so a nested function type costs no intermediate allocation.
func writeGoTypeExpr(b *strings.Builder, leaves *[]string, node *sitter.Node, src []byte) {
	if node == nil {
		b.WriteString("?nil")
		return
	}
	switch node.Type() {
	case "parenthesized_type":
		// Parens carry no identity — `(*T)` and `*T` are the same type.
		writeGoTypeExpr(b, leaves, lastNamedChild(node), src)
	case "pointer_type":
		b.WriteString("*")
		writeGoTypeExpr(b, leaves, lastNamedChild(node), src)
	case "slice_type":
		b.WriteString("[]")
		writeGoTypeExpr(b, leaves, lastNamedChild(node), src)
	case "array_type":
		// THE LENGTH IS DROPPED, matching the prototype, whose ast.ArrayType arm
		// renders "[]"+elem for a sized array and a slice alike. The element is
		// the LAST named child because a sized array's length literal is a named
		// child preceding it.
		b.WriteString("[]")
		writeGoTypeExpr(b, leaves, lastNamedChild(node), src)
	case "channel_type":
		// Direction is dropped with the same reasoning as the array length: the
		// prototype's ast.ChanType arm renders "chan "+value regardless of Dir.
		b.WriteString("chan ")
		writeGoTypeExpr(b, leaves, lastNamedChild(node), src)
	case "map_type":
		// Exactly two named children, key then value.
		b.WriteString("map[")
		writeGoTypeExpr(b, leaves, node.NamedChild(0), src)
		b.WriteString("]")
		writeGoTypeExpr(b, leaves, lastNamedChild(node), src)
	case "function_type":
		b.WriteString("func")
		writeGoSigShape(b, leaves, node, src)
	case "generic_type":
		// `Box[int]` — the base is the `type` field and the arguments are a
		// sibling type_arguments node whose entries are type_elem wrappers.
		writeGoTypeExpr(b, leaves, node.ChildByFieldName("type"), src)
		b.WriteString("[")
		writeGoTypeArgs(b, leaves, node, src)
		b.WriteString("]")
	case "interface_type":
		// An inline interface has no name to resolve, so it renders as a literal
		// rather than a leaf. Empty and non-empty are DISTINCT: `any` is
		// satisfied by everything, a non-empty inline interface is not.
		if node.NamedChildCount() == 0 {
			b.WriteString("ext:any")
			return
		}
		b.WriteString("ext:iface")
	case "type_identifier", "qualified_type":
		// THE ONLY LEAVES. A bare name or a package-qualified name is what the
		// parser can bind to a declaration; everything else above is composition.
		b.WriteString(TypeExprLeafSep)
		*leaves = append(*leaves, node.Content(src))
	default:
		b.WriteString("?")
		b.WriteString(node.Type())
	}
}

// writeGoTypeArgs renders a generic instantiation's type arguments, comma-joined.
func writeGoTypeArgs(b *strings.Builder, leaves *[]string, generic *sitter.Node, src []byte) {
	args := generic.ChildByFieldName("type_arguments")
	if args == nil {
		// The grammar does not always attach the field name; the arguments are
		// the sibling type_arguments node.
		for i := range int(generic.NamedChildCount()) {
			if generic.NamedChild(i).Type() == "type_arguments" {
				args = generic.NamedChild(i)
				break
			}
		}
	}
	if args == nil {
		return
	}
	first := true
	for i := range int(args.NamedChildCount()) {
		arg := args.NamedChild(i)
		// Each argument arrives wrapped in a type_elem; unwrap to the type.
		if arg.Type() == "type_elem" {
			arg = lastNamedChild(arg)
		}
		if !first {
			b.WriteString(",")
		}
		first = false
		writeGoTypeExpr(b, leaves, arg, src)
	}
}

// writeGoSigShape renders `(params)(results)` for a node carrying parameter
// lists — a function_type nested inside a larger type expression.
func writeGoSigShape(b *strings.Builder, leaves *[]string, fn *sitter.Node, src []byte) {
	var params, result *sitter.Node
	n := int(fn.NamedChildCount())
	i := 0
	for ; i < n; i++ {
		if fn.NamedChild(i).Type() == "parameter_list" {
			params = fn.NamedChild(i)
			i++
			break
		}
	}
	if i < n {
		result = fn.NamedChild(i)
	}
	b.WriteString("(")
	writeGoParamShapes(b, leaves, params, src)
	b.WriteString(")(")
	writeGoResultShapes(b, leaves, result, src)
	b.WriteString(")")
}

// goParamTypeExprs renders one parameter_list as one TypeExpr per PARAMETER.
//
// A parameter_declaration carrying N names and one type contributes N entries
// and the names themselves are dropped — the same rule goDeclaredResults and the
// prototype's sigKey both apply, and the reason a first-child rule is wrong: `a,
// b int` is two int parameters, not one.
//
// AN ALL-UNNAMED LIST IS TRIED FIRST, because for the two fusing shapes the
// named reading is not merely a different arrangement of the same parameters —
// it reports the wrong COUNT and the wrong TYPES, and does so plausibly.
func goParamTypeExprs(list *sitter.Node, src []byte) []TypeExpr {
	if list == nil {
		return nil
	}
	out := make([]TypeExpr, 0, int(list.NamedChildCount()))
	for i := range int(list.NamedChildCount()) {
		decl := list.NamedChild(i)
		switch decl.Type() {
		case "parameter_declaration":
			if exprs, unnamed := goUnnamedParamExprs(decl, src); unnamed {
				out = append(out, exprs...)
				continue
			}
			names, typeNode := goNamesAndType(decl, src)
			if typeNode == nil {
				// An UNNAMED parameter is a type with no identifiers before it,
				// so goNamesAndType finds no names and returns no type. Its type
				// is the declaration's only named child.
				typeNode = lastNamedChild(decl)
				names = nil
			}
			reps := len(names)
			if reps == 0 {
				reps = 1
			}
			expr := goSigTypeExpr(typeNode, src)
			for range reps {
				out = append(out, expr)
			}
		case "variadic_parameter_declaration":
			// `...T` is its own declaration kind in this grammar rather than an
			// ellipsis wrapping the type, so the marker is written here. It stays
			// DISTINCT from `[]T`: the two are different types, and a signature
			// key that conflated them would match a variadic method against a
			// slice-taking one.
			expr := goSigTypeExpr(lastNamedChild(decl), src)
			out = append(out, TypeExpr{Shape: "..." + expr.Shape, Leaves: expr.Leaves})
		}
	}
	return out
}

// goUnnamedParamExprs renders a parameter_declaration the grammar MIS-BRACKETED
// as named parameters, and reports whether it did so.
//
// THE DEFECT IT REPAIRS. `SetInstanceLabels(context.Context, string, string,
// string, map[string]string)` arrives as TWO declarations: a bare
// `context.Context`, then one declaration holding FOUR identifiers — string,
// string, string, map — and an `array_type` spelling `[string]string`. Read as
// names-plus-type that is four parameters of type `[]string`, which is the right
// parameter COUNT and entirely the wrong types, so it produces a plausible key
// that matches nothing rather than an error.
//
// THE RUN'S LAST IDENTIFIER FUSES WITH THE NODE AFTER IT and every earlier one
// stands alone. That is forced by the tree rather than assumed: the identifiers
// are separated by the declaration's own anonymous comma tokens, and there is no
// comma between the keyword and the node it fused with. A shape where the
// keyword is NOT last is unrecognized, and renders a distinguishable literal
// rather than a guess — an unrecognized rendering that merely LOOKED like a
// signature would match something.
func goUnnamedParamExprs(decl *sitter.Node, src []byte) ([]TypeExpr, bool) {
	ids, rest := goLeadingIdentifiers(decl)
	if len(ids) == 0 || rest == nil {
		return nil, false
	}
	keyword := -1
	for i, id := range ids {
		if goTypeKeywords[id.Content(src)] {
			keyword = i
			break
		}
	}
	if keyword < 0 {
		return nil, false
	}

	out := make([]TypeExpr, 0, len(ids))
	for _, id := range ids[:keyword] {
		// An identifier standing alone in an unnamed run spells a whole type, so
		// it is a LEAF — the same rendering a type_identifier gets. Passing it to
		// writeGoTypeExpr would tag it `?identifier`, since `identifier` is a name
		// kind and never appears where that walk expects a type.
		out = append(out, goLeafTypeExpr(id.Content(src)))
	}
	if keyword != len(ids)-1 {
		return append(out, TypeExpr{Shape: "?unnamed-run"}), true
	}
	return append(out, goFusedKeywordExpr(ids[keyword].Content(src), rest, src)), true
}

// goLeafTypeExpr renders one written spelling as a resolvable leaf.
func goLeafTypeExpr(text string) TypeExpr {
	return TypeExpr{Shape: TypeExprLeafSep, Leaves: []string{text}}
}

// goFusedKeywordExpr re-composes the one type a keyword and the node after it
// were split across.
//
// `map` fused with an array_type: the map's KEY sits in the array's length slot
// and its VALUE is the array's element, so `map[string][]byte` arrives as
// array_type(identifier `string`, slice_type) and recomposes exactly. The key is
// written through goLeafTypeExpr's rule for the same reason the standalone
// identifiers are — it is an `identifier` node sitting where a type belongs.
//
// `chan` fused with its element type needs no unwrapping: the element is a whole
// type node already. Only the UNDIRECTED form reaches here, because `chan<- T`
// and `<-chan T` both parse as a channel_type of their own.
func goFusedKeywordExpr(keyword string, rest *sitter.Node, src []byte) TypeExpr {
	var b strings.Builder
	var leaves []string
	switch {
	case keyword == "map" && rest.Type() == "array_type" && rest.NamedChildCount() >= 2:
		b.WriteString("map[")
		writeGoUnnamedLeaf(&b, &leaves, rest.NamedChild(0), src)
		b.WriteString("]")
		writeGoTypeExpr(&b, &leaves, lastNamedChild(rest), src)
	case keyword == "chan":
		b.WriteString("chan ")
		writeGoTypeExpr(&b, &leaves, rest, src)
	default:
		// DISTINGUISHABLE AND NEVER-MATCHING, the same discipline the unhandled
		// kind above takes: a fusion this function does not recognize must not
		// render as something that could compare equal to a real type.
		return TypeExpr{Shape: "?fused-" + keyword}
	}
	return TypeExpr{Shape: b.String(), Leaves: leaves}
}

// writeGoUnnamedLeaf renders a node the mis-parse left as a bare `identifier`
// where a type belongs, and delegates every other kind to the normal walk — a
// qualified or composed map key still arrives as its own node and must be
// rendered as one.
func writeGoUnnamedLeaf(b *strings.Builder, leaves *[]string, node *sitter.Node, src []byte) {
	if node != nil && node.Type() == "identifier" {
		b.WriteString(TypeExprLeafSep)
		*leaves = append(*leaves, node.Content(src))
		return
	}
	writeGoTypeExpr(b, leaves, node, src)
}

// goResultTypeExprs renders a declaration's result node as one TypeExpr per
// result: a parameter_list expands per entry, and any other node is a single
// bare result.
func goResultTypeExprs(result *sitter.Node, src []byte) []TypeExpr {
	if result == nil {
		return nil
	}
	if result.Type() == "parameter_list" {
		return goParamTypeExprs(result, src)
	}
	return []TypeExpr{goSigTypeExpr(result, src)}
}

// writeGoParamShapes writes a nested function type's parameter shapes, joined by
// commas. Leaves accumulate into the enclosing expression's slice in order.
func writeGoParamShapes(b *strings.Builder, leaves *[]string, list *sitter.Node, src []byte) {
	for i, e := range goParamTypeExprs(list, src) {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(e.Shape)
		*leaves = append(*leaves, e.Leaves...)
	}
}

// writeGoResultShapes is writeGoParamShapes for the result position.
func writeGoResultShapes(b *strings.Builder, leaves *[]string, result *sitter.Node, src []byte) {
	for i, e := range goResultTypeExprs(result, src) {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(e.Shape)
		*leaves = append(*leaves, e.Leaves...)
	}
}

// goMethodElemSigParts splits an interface method spec into its parameter list
// and its result node.
//
// A method_elem is NOT accepted by goSignatureParts, whose kind guard admits
// only function_declaration and method_declaration; widening that guard would
// change the contract its existing callers depend on. The shapes are otherwise
// identical — a spec's named children are the field_identifier, then the
// parameter_list, then the optional result — which is exactly why a spec and the
// method satisfying it render alike.
func goMethodElemSigParts(declNode *sitter.Node) (params, result *sitter.Node) {
	if declNode == nil || declNode.Type() != "method_elem" {
		return nil, nil
	}
	n := int(declNode.NamedChildCount())
	i := 0
	for ; i < n; i++ {
		if declNode.NamedChild(i).Type() == "parameter_list" {
			params = declNode.NamedChild(i)
			i++
			break
		}
	}
	if params != nil && i < n {
		result = declNode.NamedChild(i)
	}
	return params, result
}

// goSigFacts composes a declaration's signature from its parameter list and
// result node. Returns nil when neither carries anything.
func goSigFacts(params, result *sitter.Node, src []byte) *SigFacts {
	p := goParamTypeExprs(params, src)
	r := goResultTypeExprs(result, src)
	if len(p) == 0 && len(r) == 0 {
		return nil
	}
	return &SigFacts{Params: p, Results: r}
}

// extractGoInterfaceEmbeds returns the embedded-element spellings of an
// INTERFACE type declaration.
//
// AN ELEMENT IS AN EMBED WHEN IT HAS EXACTLY ONE NAMED CHILD of kind
// type_identifier, qualified_type or generic_type, and DECLINES otherwise. That
// single rule is what separates an embed from a TYPE SET: parsing `type Num
// interface { ~int | ~float64; comparable; io.Reader; Other }` gives ONE
// type_elem holding TWO negated_type children for the union — declined by the
// count — while comparable, io.Reader and Other are single-child type_elems and
// are accepted as spellings.
//
// A SPELLING THAT NAMES NOTHING IN-REPO IS STILL RECORDED HERE and declines
// later, at resolution. That is not the same as declining now: a consumer needs
// to know an interface HAS an unexpandable embed, because such an interface's
// method set is under-known and the set of types that could satisfy it is
// correspondingly wider than the syntax can prove.
//
// THE BODY IS BOUND FROM THE type_spec's `type` FIELD, NEVER SEARCHED FOR — the
// same anchoring extractGoEmbeds takes and for the same reason in mirror image:
// an unanchored descent for an interface_type reports `type HasAnonIfaceField
// struct { X interface{ TokenSource }; C int }` as embedding TokenSource, when
// it is a NAMED field whose type happens to be an anonymous interface and it
// embeds nothing at all. Proven by executing both walks over that fixture:
// unanchored [TokenSource], anchored []. No such declaration exists in this
// repository today, which is exactly why the rule is written down — an
// acceptance corpus that never exercises a shape is structurally blind to a
// wrong answer on it.
func extractGoInterfaceEmbeds(node *sitter.Node, src []byte) []string {
	spec := goSoleTypeSpec(node)
	if spec == nil {
		return nil
	}
	body := spec.ChildByFieldName("type")
	if body == nil || body.Type() != "interface_type" {
		return nil
	}
	var embeds []string
	for i := range int(body.NamedChildCount()) {
		elem := body.NamedChild(i)
		if elem.Type() != "type_elem" || elem.NamedChildCount() != 1 {
			continue
		}
		inner := elem.NamedChild(0)
		switch inner.Type() {
		case "type_identifier", "qualified_type", "generic_type":
			if name := qualifiedTypeName(inner, src); name != "" {
				embeds = append(embeds, name)
			}
		}
	}
	return embeds
}

// goAllEmbeds returns a type declaration's embedded-type spellings whichever
// kind of body it declares — struct fields and interface elements alike.
//
// ONE DEFINITION, TWO CONSUMERS, deliberately: the type-facts carrier reads it
// and so does the EMBEDS emission arm. Concatenating the two extractors inline
// at either site is how two spellings of one rule drift apart.
func goAllEmbeds(declNode *sitter.Node, src []byte) []string {
	return append(extractGoEmbeds(declNode, src), extractGoInterfaceEmbeds(declNode, src)...)
}

// lastNamedChild returns a node's final named child, or nil when it has none.
//
// It is the exact accessor for every single-operand type wrapper in this
// grammar: a pointer, slice, channel or parenthesized type has one named child,
// and a sized array's length literal PRECEDES its element — so the last named
// child is the operand in every case, without depending on a field name the
// grammar may not attach.
func lastNamedChild(node *sitter.Node) *sitter.Node {
	if node == nil {
		return nil
	}
	n := int(node.NamedChildCount())
	if n == 0 {
		return nil
	}
	return node.NamedChild(n - 1)
}
