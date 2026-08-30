// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	sitter "github.com/smacker/go-tree-sitter"
)

func init() {
	RegisterCQualifierTypes()
}

// RegisterCQualifierTypes installs the C qualifier-type arm. It is EXPORTED for
// the reason RegisterGoQualifierTypes documents: a test that takes the arm OUT
// to measure an unarmed baseline must RESTORE the production registration on
// cleanup, and UnregisterQualifierTypes DELETES the entry rather than parking
// it.
func RegisterCQualifierTypes() {
	RegisterQualifierTypes(LangC, cQualifierTypes)
}

// The C kind classes. cKindOther is the ZERO VALUE and therefore the class of
// every symbol the table does not name, which is what makes an unclassified
// symbol behave like a kind no arm matches rather than like a wrong one.
//
// THE TABLE IS THE UNION OF TWO ARMS' VOCABULARIES, because a language has ONE
// class table: the kinds below the qualifier walk's own are spelled by the
// slot-bind arm in chunker_c_slots.go, which consumes this table rather than
// declaring a second one that could drift from it.
//
// `.`, `=` and `&` are deliberately ABSENT: the C grammar carries no REGULAR
// symbol for any of them, and newSymbolClasses panics on a kinds-map name it
// never assigned. C is the one language here that genuinely has to READ an
// anonymous token — pointer_expression covers `&fn` and `*p` alike and the slot
// arm must tell them apart, since address-of a function is a valid dispatch
// target and a dereference is not — and it reads it through the node's
// `operator:` field, never through this table.
const (
	cKindOther uint8 = iota
	cKindFunctionDefinition
	cKindFunctionDeclarator
	cKindParameterList
	cKindParameterDeclaration
	cKindDeclaration
	cKindInitDeclarator
	cKindPointerDeclarator
	cKindParenthesizedDeclarator
	cKindIdentifier
	cKindFieldIdentifier
	cKindTypeIdentifier
	cKindStructSpecifier
	cKindPrimitiveType
	cKindCompoundStatement
	cKindInitializerList
	cKindInitializerPair
	cKindFieldDesignator
	cKindSubscriptDesignator
	cKindPointerExpression
	cKindFieldDeclaration
	cKindFieldDeclarationList
	cKindArrayDeclarator
	cKindAssignmentExpression
	cKindCallExpression
	cKindArgumentList
	cKindFieldExpression
	cKindReturnStatement
	cKindParenthesizedExpression
)

// cKindNames maps every C node-kind spelling the two C arms name onto its class
// code. It is the input to newSymbolClasses, so a kind added to either walk
// without an entry here classifies as cKindOther and binds nothing rather than
// mis-binding.
var cKindNames = map[string]uint8{
	"function_definition":      cKindFunctionDefinition,
	"function_declarator":      cKindFunctionDeclarator,
	"parameter_list":           cKindParameterList,
	"parameter_declaration":    cKindParameterDeclaration,
	"declaration":              cKindDeclaration,
	"init_declarator":          cKindInitDeclarator,
	"pointer_declarator":       cKindPointerDeclarator,
	"parenthesized_declarator": cKindParenthesizedDeclarator,
	"identifier":               cKindIdentifier,
	"field_identifier":         cKindFieldIdentifier,
	"type_identifier":          cKindTypeIdentifier,
	"struct_specifier":         cKindStructSpecifier,
	"primitive_type":           cKindPrimitiveType,
	"compound_statement":       cKindCompoundStatement,
	"initializer_list":         cKindInitializerList,
	"initializer_pair":         cKindInitializerPair,
	"field_designator":         cKindFieldDesignator,
	"subscript_designator":     cKindSubscriptDesignator,
	"pointer_expression":       cKindPointerExpression,
	"field_declaration":        cKindFieldDeclaration,
	"field_declaration_list":   cKindFieldDeclarationList,
	"array_declarator":         cKindArrayDeclarator,
	"assignment_expression":    cKindAssignmentExpression,
	"call_expression":          cKindCallExpression,
	"argument_list":            cKindArgumentList,
	"field_expression":         cKindFieldExpression,
	"return_statement":         cKindReturnStatement,
	"parenthesized_expression": cKindParenthesizedExpression,
}

// cKindTable memoizes the C class table for the process.
var cKindTable = kindTable{lang: LangC, names: cKindNames}

// cKinds returns the memoized C symbol class table.
func cKinds() symbolClasses {
	return cKindTable.get()
}

// cQualifierTypes is the C arm: one walk of a declaration's subtree, returning
// the qualifier names it makes visible mapped to their declared types.
//
// WITHOUT THIS ARM THE REST OF C'S DISPATCH CAPTURE DOES NOTHING. The field-node
// row gives a dispatch reference a TARGET and the widened Calls query makes the
// reference EXIST, but the typed-qualifier rung keys its lookup on the
// QUALIFIER'S TYPE — so with no recorded type for `h` in `h->flush(c)` the rung
// declines and the reference falls to the open-set rung over the whole file,
// which is the wrong-target generator the whole phase is built to avoid.
func cQualifierTypes(declNode *sitter.Node, src []byte) map[string]QualType {
	if declNode == nil {
		return nil
	}
	b := &qualBinder{classes: cKinds()}
	walkCQualifiers(b, declNode, src)
	if len(b.types) == 0 {
		return nil
	}
	return b.types
}

// walkCQualifiers descends one declaration binding every parameter and every
// local declaration it meets.
//
// THE SWITCH IS ON THE NUMERIC SYMBOL, NOT ON THE KIND NAME, for the measured
// allocation reason walkGoQualifiers documents: reading a node's kind name
// converts a cgo C-string into a fresh Go string on every call.
func walkCQualifiers(b *qualBinder, node *sitter.Node, src []byte) {
	for i := range int(node.NamedChildCount()) {
		child := node.NamedChild(i)
		switch b.classes.class(child.Symbol()) {
		case cKindParameterDeclaration, cKindDeclaration:
			bindCTypeFirst(b, child, src)
		}
		walkCQualifiers(b, child, src)
	}
}

// bindCTypeFirst binds one declaration written in C's TYPE-FIRST shape.
//
// THE TYPE IS FOUND BY KIND RATHER THAN AT INDEX ZERO. `static struct http_ops
// ops = {...}` puts a storage_class_specifier in the first named slot, and a
// `const` qualifier does the same — so a positional rule silently declines
// exactly the file-scope statics that hold C's dispatch tables. Scanning for the
// first child that IS a type also skips those specifiers without either being
// classified.
func bindCTypeFirst(b *qualBinder, node *sitter.Node, src []byte) {
	typeIdx, text := cDeclaredType(b.classes, node, src)
	if text == "" {
		return
	}
	for i := typeIdx + 1; i < int(node.NamedChildCount()); i++ {
		if name := cDeclaratorName(b.classes, node.NamedChild(i)); name != nil {
			b.bind(name.Content(src), QualType{Text: text})
		}
	}
}

// cDeclaredType returns the index of a declaration's type node and the text it
// records, or -1 and "" when the declaration names no type this rung binds.
func cDeclaredType(classes symbolClasses, node *sitter.Node, src []byte) (int, string) {
	for i := range int(node.NamedChildCount()) {
		child := node.NamedChild(i)
		switch classes.class(child.Symbol()) {
		case cKindTypeIdentifier, cKindStructSpecifier:
			return i, cQualTypeText(classes, child, src)
		case cKindPrimitiveType:
			// A primitive declares nothing under its name, so the declaration
			// binds nothing — but its position is still the type slot, and
			// returning here stops the scan from walking on into the declarator
			// and mistaking an identifier for a type.
			return i, ""
		}
	}
	return -1, ""
}

// cDeclaratorName digs the declared identifier out of a declarator, reading
// through the pointer and initializer wrappers, or returns nil.
//
// A FUNCTION-POINTER DECLARATOR IS DECLINED rather than followed: its identifier
// sits under a parenthesized_declarator inside a function_declarator, and the
// thing it names is a callable slot rather than a value with a struct type.
func cDeclaratorName(classes symbolClasses, node *sitter.Node) *sitter.Node {
	switch classes.class(node.Symbol()) {
	case cKindIdentifier:
		return node
	case cKindInitDeclarator, cKindPointerDeclarator:
		for i := range int(node.NamedChildCount()) {
			if name := cDeclaratorName(classes, node.NamedChild(i)); name != nil {
				return name
			}
		}
	}
	return nil
}

// cQualTypeText renders a type expression as the text a qualifier's type is
// recorded under, or "" to decline it.
//
// IT IS A CLOSED ALLOWLIST, for the reason goQualTypeText's own comment gives.
// A struct_specifier records the type_identifier it wraps, so `struct Server *s`
// records `Server` — WHICH IS THE WHOLE POINT FOR THIS LANGUAGE, since a C
// dispatch table is almost always reached through a `struct X *`. That spelling
// has to agree with the name the struct's own declaration chunk carries, or the
// member lookup can never match; it does, because both read the same
// type_identifier out of the same struct_specifier.
//
// It takes the class table rather than the binder because the slot-bind arm
// normalizes an initialized variable's type through it and binds nothing.
func cQualTypeText(classes symbolClasses, typeNode *sitter.Node, src []byte) string {
	if typeNode == nil {
		return ""
	}
	switch classes.class(typeNode.Symbol()) {
	case cKindTypeIdentifier:
		return typeNode.Content(src)
	case cKindStructSpecifier:
		for i := range int(typeNode.NamedChildCount()) {
			child := typeNode.NamedChild(i)
			if classes.class(child.Symbol()) == cKindTypeIdentifier {
				return child.Content(src)
			}
		}
	}
	return ""
}
