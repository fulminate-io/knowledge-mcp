// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
)

func init() {
	RegisterCTypeFacts()
}

// RegisterCTypeFacts installs the C type-facts arm, exported for the same
// restore-not-delete reason as RegisterCQualifierTypes.
func RegisterCTypeFacts() {
	RegisterTypeFacts(LangC, cTypeFacts)
}

// slotPositional is the Index a DESIGNATED bind carries, so the two shapes are
// distinguishable without a third field.
const slotDesignated = -1

// cTypeFacts records the two facts C's dispatch tables rest on: the order of a
// struct's fields, and the slots a composite literal filled.
//
// IT NEVER SETS IsInterface, AND THAT IS LOAD-BEARING RATHER THAN AN OMISSION.
// C declares no contract in the declared-conformance sense, and
// indexDeclaration copies IsInterface for ANY language — so a C declaration
// marked a contract would enter the interface-keyed views of a derivation that
// was never meant to see it. The zero value is the whole of the contract here;
// the language gate on that derivation is the second belt, not the first.
//
// IT LIVES IN ITS OWN FILE, apart from the C qualifier arm, so neither arm's
// source-level fence can be moved by the other's edits. The KIND TABLE is
// deliberately SHARED — a language has one table — and is declared beside the
// qualifier arm with both arms' vocabularies unioned into it.
func cTypeFacts(declNode *sitter.Node, _ string, src []byte) *TypeFacts {
	if declNode == nil {
		return nil
	}
	classes := cKinds()
	switch classes.class(declNode.Symbol()) {
	case cKindStructSpecifier:
		order := cFieldOrder(classes, declNode, src)
		if len(order) == 0 {
			return nil
		}
		return &TypeFacts{FieldOrder: order}
	case cKindDeclaration:
		binds := cSlotBinds(classes, declNode, src)
		if len(binds) == 0 {
			return nil
		}
		return &TypeFacts{SlotBinds: binds}
	}
	return nil
}

// cFieldOrder lists a struct's field names in SOURCE ORDER, holding a position
// for every member including the ones no name can be read from.
//
// AN ANONYMOUS MEMBER RECORDS THE EMPTY STRING rather than being dropped. A
// dropped entry shifts every later position by one and silently rebinds those
// positional slots to the wrong field — which is the whole failure this
// carrier exists to prevent.
func cFieldOrder(classes symbolClasses, declNode *sitter.Node, src []byte) []string {
	body := cChildOfKind(classes, declNode, cKindFieldDeclarationList)
	if body == nil {
		return nil
	}
	out := make([]string, 0, int(body.NamedChildCount()))
	for i := range int(body.NamedChildCount()) {
		field := body.NamedChild(i)
		if classes.class(field.Symbol()) != cKindFieldDeclaration {
			continue
		}
		out = append(out, cFieldName(classes, field, src))
	}
	return out
}

// cFieldName reads one field declaration's name, or "" when it has none this
// walk can reach — an anonymous struct or union member.
//
// A FUNCTION-POINTER FIELD'S NAME IS NESTED, which is why this descends rather
// than reading a direct child: `int (*flush)(struct conn *)` wraps its
// field_identifier in a parenthesized_declarator inside a pointer_declarator
// inside a function_declarator, and those fields are exactly the ones a slot
// bind targets. An array field nests one level for the same reason.
//
// IT DESCENDS INTO DECLARATORS ONLY, NEVER INTO A NESTED struct_specifier.
// An anonymous struct member holds field_identifiers of its OWN inside it, and
// returning one of those would name the outer position after an inner field.
func cFieldName(classes symbolClasses, node *sitter.Node, src []byte) string {
	for i := range int(node.NamedChildCount()) {
		child := node.NamedChild(i)
		switch classes.class(child.Symbol()) {
		case cKindFieldIdentifier:
			return child.Content(src)
		case cKindFunctionDeclarator, cKindParenthesizedDeclarator, cKindPointerDeclarator, cKindArrayDeclarator:
			if name := cFieldName(classes, child, src); name != "" {
				return name
			}
		}
	}
	return ""
}

// cSlotBinds reads the slots one declaration's composite literal filled.
//
// THE WHOLE INITIALIZER IS THE UNIT OF DECLINE for every shape that could
// mis-index, and that is the design rather than a convenience. C99 lets a
// designator RESET the position, so a partial reading of a mixed initializer is
// precisely how a wrong target gets manufactured — the safe answer for a
// sequence this arm cannot read exactly is no binds at all.
func cSlotBinds(classes symbolClasses, declNode *sitter.Node, src []byte) []SlotBind {
	_, typeText := cDeclaredType(classes, declNode, src)
	if typeText == "" {
		return nil
	}
	list := cInitializerList(classes, declNode)
	if list == nil {
		return nil
	}

	var designated, positional []SlotBind
	sawDesignated := false
	pos := 0
	for i := range int(list.NamedChildCount()) {
		element := list.NamedChild(i)
		if classes.class(element.Symbol()) == cKindInitializerPair {
			field, target, ok := cDesignatedPair(classes, element, src)
			if !ok {
				// A subscript designator indexes an ARRAY, not a field, so it
				// names no slot this carrier describes — and mixing one in
				// means the sequence cannot be read exactly.
				return nil
			}
			sawDesignated = true
			if field != "" && target != "" {
				designated = append(designated, SlotBind{Type: typeText, Field: field, Target: target, Index: slotDesignated})
			}
			continue
		}
		// A POSITIONAL ELEMENT CONSUMES ITS POSITION WHETHER OR NOT IT BINDS.
		// A macro, a literal or a nested list names no target, but the elements
		// after it still sit where the source put them.
		if target := cSlotTarget(classes, element, src); target != "" {
			positional = append(positional, SlotBind{Type: typeText, Target: target, Index: pos})
		}
		pos++
	}

	// MIXED DECLINES WHOLE. Either shape alone is exactly readable; together
	// they are not, because a designator resets the position the later
	// positional elements would be read against.
	//
	// THE TEST IS "A DESIGNATOR WAS SEEN", NOT "A DESIGNATED BIND WAS
	// RECORDED". A pair like `.version = 2` names a field and binds nothing,
	// and a literal mixing it with positional elements is still mixed — reading
	// the positional half of it would index against a position the designator
	// already moved.
	if sawDesignated && pos > 0 {
		return nil
	}
	if sawDesignated {
		return designated
	}
	return positional
}

// cInitializerList returns the initializer_list a declaration's init_declarator
// assigns, or nil.
func cInitializerList(classes symbolClasses, declNode *sitter.Node) *sitter.Node {
	init := cChildOfKind(classes, declNode, cKindInitDeclarator)
	if init == nil {
		return nil
	}
	return cChildOfKind(classes, init, cKindInitializerList)
}

// cDesignatedPair reads one `.field = target` pair. It reports false when the
// designator is not a FIELD designator, which is the caller's signal to decline
// the whole initializer.
//
// THE DESIGNATOR DISCRIMINATION NEEDS NO TOKEN. An initializer_pair exposes its
// designator as a node, and both `field_designator` and `subscript_designator`
// are regular kinds — so telling `.a =` from `[3] =` is a kind comparison
// rather than a walk to a punctuation character.
func cDesignatedPair(classes symbolClasses, pair *sitter.Node, src []byte) (field, target string, ok bool) {
	for i := range int(pair.NamedChildCount()) {
		child := pair.NamedChild(i)
		switch classes.class(child.Symbol()) {
		case cKindSubscriptDesignator:
			return "", "", false
		case cKindFieldDesignator:
			if name := cChildOfKind(classes, child, cKindFieldIdentifier); name != nil {
				field = name.Content(src)
			}
		default:
			if target == "" {
				target = cSlotTarget(classes, child, src)
			}
		}
	}
	return field, target, true
}

// cSlotTarget returns the identifier one initializer element names, or "" when
// it names none.
//
// THE ADDRESS-OF FORM IS ACCEPTED AND THE DEREFERENCE FORM IS NOT, and telling
// them apart is the one place in this arm that has to read an ANONYMOUS token.
// `pointer_expression` covers `&fn` and `*p` alike, and the C grammar declares
// no regular symbol for either operator — so the class table cannot answer the
// question at all and the node's own `operator:` field is read instead. Taking
// the address of a function names that function; dereferencing a pointer names
// a value, which is not a dispatch target.
func cSlotTarget(classes symbolClasses, element *sitter.Node, src []byte) string {
	switch classes.class(element.Symbol()) {
	case cKindIdentifier:
		return element.Content(src)
	case cKindPointerExpression:
		op := element.ChildByFieldName("operator")
		if op == nil || strings.TrimSpace(op.Content(src)) != "&" {
			return ""
		}
		for i := range int(element.NamedChildCount()) {
			child := element.NamedChild(i)
			if classes.class(child.Symbol()) == cKindIdentifier {
				return child.Content(src)
			}
		}
	}
	return ""
}

// cChildOfKind returns a node's first direct named child of one class.
func cChildOfKind(classes symbolClasses, node *sitter.Node, kind uint8) *sitter.Node {
	for i := range int(node.NamedChildCount()) {
		if child := node.NamedChild(i); classes.class(child.Symbol()) == kind {
			return child
		}
	}
	return nil
}
