// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
)

func init() {
	RegisterCPPQualifierTypes()
	RegisterCPPTypeFacts()
}

// RegisterCPPTypeFacts installs the cpp type-facts arm, exported for the same
// restore-not-delete reason as RegisterCPPQualifierTypes.
func RegisterCPPTypeFacts() {
	RegisterTypeFacts(LangCPP, cppTypeFacts)
}

// cppTypeFacts records the two declared-conformance facts C++'s grammar
// carries: every first-level base a class or struct names, and whether the
// declaration is a contract.
//
// EVERY BASE IS RECORDED WITH THE KIND `undeclared`, AND THAT IS THE HONEST
// RECORD RATHER THAN A FALLBACK. C++ has NO SYNTACTIC INTERFACE CONSTRUCT — an
// abstract base is a convention, not a keyword — so a base-class clause cannot
// say whether it named a contract or a concrete parent. The distinction is
// decidable, but only against a COMPLETE declaration index, because the
// deciding fact lives in the base's OWN declaration and may be in another
// header; the emission path applies it, emitting nothing for a base that
// resolves to a non-contract and counting that outcome separately from a base
// that resolves to nothing at all.
//
// ACCESS SPECIFIERS ARE DROPPED FROM THE RECORDED SPELLING AND GATE NOTHING.
// Private inheritance is still a declared base-class relationship, and
// encoding visibility would need a Method vocabulary nothing consumes.
//
// A CONTRACT IS A DECLARATION WITH AT LEAST ONE PURE-VIRTUAL MEMBER. That is
// the only structural signal C++ offers and it is exactly the abstract-base
// idiom the two-hop model serves. It is deliberately NOT widened to "has a
// virtual member", which would make every polymorphic concrete class a
// contract and fan every call resolved to one out across its whole hierarchy.
//
// EVERY OTHER TypeFacts FIELD STAYS ZERO. Nothing in this ticket consumes a cpp
// result type, field type or signature, and a carrier filled with no consumer
// is a dead field that later readers must still keep true.
func cppTypeFacts(declNode *sitter.Node, _ string, src []byte) *TypeFacts {
	if declNode == nil {
		return nil
	}
	classes := cppKinds()
	switch classes.class(declNode.Symbol()) {
	case cppKindClassSpecifier, cppKindStructSpecifier:
	default:
		return nil
	}
	conforms := cppDeclaredSupertypes(classes, declNode, src)
	contract := cppHasPureVirtual(classes, declNode)
	if len(conforms) == 0 && !contract {
		return nil
	}
	return &TypeFacts{Conforms: conforms, IsInterface: contract}
}

// cppDeclaredSupertypes collects every base spelling a base-class clause names,
// IN SOURCE ORDER.
//
// THE ACCESS-SPECIFIER SKIP IS A KIND COMPARISON, NOT A TOKEN WALK. `public`,
// `private` and `protected` are anonymous in this grammar and could not be
// classified at all — but `access_specifier`, the node that WRAPS one, is a
// regular kind, so the clause is read entirely through the class table.
func cppDeclaredSupertypes(classes symbolClasses, declNode *sitter.Node, src []byte) []DeclaredSupertype {
	clause := cppChildOfKind(classes, declNode, cppKindBaseClassClause)
	if clause == nil {
		return nil
	}
	var out []DeclaredSupertype
	for i := range int(clause.NamedChildCount()) {
		child := clause.NamedChild(i)
		if classes.class(child.Symbol()) == cppKindAccessSpecifier {
			continue
		}
		if text := cppQualTypeText(classes, child, src); text != "" {
			out = append(out, DeclaredSupertype{Text: text, Kind: ConformUndeclared})
		}
	}
	return out
}

// cppHasPureVirtual reports whether a class or struct declares at least one
// pure-virtual member.
func cppHasPureVirtual(classes symbolClasses, declNode *sitter.Node) bool {
	body := cppChildOfKind(classes, declNode, cppKindFieldDeclarationList)
	if body == nil {
		return false
	}
	for i := range int(body.NamedChildCount()) {
		member := body.NamedChild(i)
		if classes.class(member.Symbol()) != cppKindFunctionDefinition {
			continue
		}
		if cppChildOfKind(classes, member, cppKindPureVirtualClause) != nil {
			return true
		}
	}
	return false
}

// cppChildOfKind returns a node's first direct named child of one class.
func cppChildOfKind(classes symbolClasses, node *sitter.Node, kind uint8) *sitter.Node {
	for i := range int(node.NamedChildCount()) {
		if child := node.NamedChild(i); classes.class(child.Symbol()) == kind {
			return child
		}
	}
	return nil
}

// RegisterCPPQualifierTypes installs the cpp qualifier-type arm. It is EXPORTED
// for the reason RegisterGoQualifierTypes documents: a test that takes the arm
// OUT to measure an unarmed baseline must RESTORE the production registration
// on cleanup, and UnregisterQualifierTypes DELETES the entry rather than
// parking it.
func RegisterCPPQualifierTypes() {
	RegisterQualifierTypes(LangCPP, cppQualifierTypes)
}

// The cpp kind classes. cppKindOther is the ZERO VALUE and therefore the class
// of every symbol the table does not name, which is what makes an unclassified
// symbol behave like a kind no arm matches rather than like a wrong one.
//
// Every constant below names a kind this file SPELLS. `public`, `private`,
// `protected` and `:` are ANONYMOUS in this grammar and may never be tabled —
// newSymbolClasses panics on a kinds-map name it never assigned. That costs
// this arm nothing: `access_specifier` ITSELF is a regular kind, so the one
// place an access keyword matters is reachable as a NODE, and nothing here has
// to read a keyword at all.
const (
	cppKindOther uint8 = iota
	cppKindParameterDeclaration
	cppKindDeclaration
	cppKindInitDeclarator
	cppKindPointerDeclarator
	cppKindReferenceDeclarator
	cppKindIdentifier
	cppKindTypeIdentifier
	cppKindQualifiedIdentifier
	cppKindNamespaceIdentifier
	cppKindTemplateType
	cppKindClassSpecifier
	cppKindStructSpecifier
	cppKindBaseClassClause
	cppKindAccessSpecifier
	cppKindFieldDeclarationList
	cppKindFunctionDefinition
	cppKindPureVirtualClause
	cppKindPrimitiveType
	cppKindParameterList
	cppKindCallExpression
	cppKindArgumentList
	cppKindFieldExpression
	cppKindFieldIdentifier
	cppKindAssignmentExpression
	cppKindReturnStatement
	cppKindFunctionDeclarator
	cppKindLambdaExpression
)

// cppKindNames maps every cpp node-kind spelling this arm names onto its class
// code. It is the input to newSymbolClasses, so a kind added to the walk
// without an entry here classifies as cppKindOther and binds nothing rather
// than mis-binding.
var cppKindNames = map[string]uint8{
	"parameter_declaration":  cppKindParameterDeclaration,
	"declaration":            cppKindDeclaration,
	"init_declarator":        cppKindInitDeclarator,
	"pointer_declarator":     cppKindPointerDeclarator,
	"reference_declarator":   cppKindReferenceDeclarator,
	"identifier":             cppKindIdentifier,
	"type_identifier":        cppKindTypeIdentifier,
	"qualified_identifier":   cppKindQualifiedIdentifier,
	"namespace_identifier":   cppKindNamespaceIdentifier,
	"template_type":          cppKindTemplateType,
	"class_specifier":        cppKindClassSpecifier,
	"struct_specifier":       cppKindStructSpecifier,
	"base_class_clause":      cppKindBaseClassClause,
	"access_specifier":       cppKindAccessSpecifier,
	"field_declaration_list": cppKindFieldDeclarationList,
	"function_definition":    cppKindFunctionDefinition,
	"pure_virtual_clause":    cppKindPureVirtualClause,
	"primitive_type":         cppKindPrimitiveType,
	"parameter_list":         cppKindParameterList,
	"call_expression":        cppKindCallExpression,
	"argument_list":          cppKindArgumentList,
	"field_expression":       cppKindFieldExpression,
	"field_identifier":       cppKindFieldIdentifier,
	"assignment_expression":  cppKindAssignmentExpression,
	"return_statement":       cppKindReturnStatement,
	"function_declarator":    cppKindFunctionDeclarator,
	"lambda_expression":      cppKindLambdaExpression,
}

// cppKindTable memoizes the cpp class table for the process.
var cppKindTable = kindTable{lang: LangCPP, names: cppKindNames}

// cppKinds returns the memoized cpp symbol class table.
func cppKinds() symbolClasses {
	return cppKindTable.get()
}

// cppQualifierTypes is the cpp arm: one walk of a declaration's subtree,
// returning the qualifier names it makes visible mapped to their declared
// types.
//
// It is a plain recursive node walk rather than a tree-sitter query, for the
// reason goQualifierTypes states: a QueryCursor is a cgo handle that must be
// closed on every path, and this walk needs no pattern matching NamedChild
// cannot express.
func cppQualifierTypes(declNode *sitter.Node, src []byte) map[string]QualType {
	if declNode == nil {
		return nil
	}
	b := &qualBinder{classes: cppKinds()}
	walkCPPQualifiers(b, declNode, src)
	if len(b.types) == 0 {
		return nil
	}
	return b.types
}

// walkCPPQualifiers descends one declaration binding every parameter and every
// local declaration it meets.
//
// PARAMETERS ARE BOUND AT ANY DEPTH, and that is what covers a lambda's
// parameter list without this arm having to recognize a lambda at all: a
// lambda's parameters are ordinary parameter_declaration nodes under an
// abstract_function_declarator. C++ has no nested function declarations, so
// depth carries no risk of another declaration's signature leaking in.
//
// THE SWITCH IS ON THE NUMERIC SYMBOL, NOT ON THE KIND NAME, and that is the
// measured cost of this function: reading a node's kind name converts a cgo
// C-string into a FRESH Go string on every call, so a recursive walk that names
// every named node at every depth allocates once per node visited. b.classes
// turns the symbol the binding already holds into one bounds-checked index.
func walkCPPQualifiers(b *qualBinder, node *sitter.Node, src []byte) {
	for i := range int(node.NamedChildCount()) {
		child := node.NamedChild(i)
		switch b.classes.class(child.Symbol()) {
		case cppKindParameterDeclaration, cppKindDeclaration:
			bindCPPTypeFirst(b, child, src)
		}
		walkCPPQualifiers(b, child, src)
	}
}

// bindCPPTypeFirst binds one declaration written in C++'s TYPE-FIRST shape.
//
// THE TYPE COMES FIRST AND THE NAME COMES SECOND, WHICH IS THE OPPOSITE OF GO.
// A Go parameter_declaration is (identifier..., type); a C++ one is (type,
// declarator). A positional rule copied from the Go arm would bind the wrong
// node, so the type is read as the FIRST named child and the name is dug out of
// whatever declarator follows it.
//
// POINTER AND REFERENCE WRAPPING LIVES ON THE DECLARATOR, NOT ON THE TYPE.
// A reader who has only seen the Go arm will look for a pointer_type case in
// the type-text allowlist and not find one: `Config* c` is a plain
// type_identifier followed by a pointer_declarator, so the star is stripped by
// reading THROUGH the declarator for the name rather than by unwrapping a type.
//
// THE TYPE IS FOUND BY KIND RATHER THAN AT INDEX ZERO. `static Config cfg;` and
// `const Config cfg;` both put a specifier in the first named slot, so a
// positional rule declines them — a missed binding rather than a wrong one, but
// a large population to lose. Scanning for the first child that IS a type skips
// those specifiers without either being classified.
func bindCPPTypeFirst(b *qualBinder, node *sitter.Node, src []byte) {
	typeIdx, text := cppDeclaredType(b.classes, node, src)
	if text == "" {
		return
	}
	for i := typeIdx + 1; i < int(node.NamedChildCount()); i++ {
		if name := cppDeclaratorName(b.classes, node.NamedChild(i)); name != nil {
			b.bind(name.Content(src), QualType{Text: text})
		}
	}
}

// cppDeclaredType returns the index of a declaration's type node and the text it
// records, or -1 and "" when the declaration names no type this rung binds.
func cppDeclaredType(classes symbolClasses, node *sitter.Node, src []byte) (int, string) {
	for i := range int(node.NamedChildCount()) {
		child := node.NamedChild(i)
		switch classes.class(child.Symbol()) {
		case cppKindTypeIdentifier, cppKindQualifiedIdentifier, cppKindTemplateType:
			return i, cppQualTypeText(classes, child, src)
		case cppKindPrimitiveType:
			// A primitive declares nothing under its name, so the declaration
			// binds nothing — but its position is still the type slot, and
			// returning here stops the scan from walking on into the declarator
			// and mistaking an identifier for a type.
			return i, ""
		}
	}
	return -1, ""
}

// cppDeclaratorName digs the declared identifier out of a declarator, reading
// through the pointer, reference and initializer wrappers, or returns nil.
//
// AN OUT-OF-CLASS DEFINITION DECLINES HERE NATURALLY rather than by special
// case: `int K::shared = 7;` binds a qualified_identifier where this function
// requires a bare identifier, so it names no local qualifier and binds nothing.
func cppDeclaratorName(classes symbolClasses, node *sitter.Node) *sitter.Node {
	switch classes.class(node.Symbol()) {
	case cppKindIdentifier:
		return node
	case cppKindInitDeclarator, cppKindPointerDeclarator, cppKindReferenceDeclarator:
		for i := range int(node.NamedChildCount()) {
			if name := cppDeclaratorName(classes, node.NamedChild(i)); name != nil {
				return name
			}
		}
	}
	return nil
}

// cppQualTypeText renders a type expression as the text a qualifier's type is
// recorded under, or "" to decline it.
//
// IT IS A CLOSED ALLOWLIST, for the reason goQualTypeText's own comment gives:
// declining by default is what keeps a container from binding a method its
// value does not have. A primitive type declines because nothing is declared
// under its name, and so does `auto`, which this grammar spells as a
// placeholder_type_specifier — this arm reads DECLARED types and does not infer
// one from an initializer.
//
// A QUALIFIED SPELLING IS REBUILT SEGMENT BY SEGMENT RATHER THAN TAKEN
// VERBATIM, and that is what keeps the normalization contract — qualifier
// retained, type arguments stripped — true for the shape that carries both.
// `std::vector<Config>` is ONE qualified_identifier whose last segment is a
// template_type, so its raw text still holds the arguments; rendering per
// segment yields `std::vector`, while a text cut at the first `<` would turn
// `A<B>::C` into `A`.
//
// It takes the class table rather than the binder because the type-facts arm
// normalizes a declared supertype through it and binds nothing.
func cppQualTypeText(classes symbolClasses, typeNode *sitter.Node, src []byte) string {
	if typeNode == nil {
		return ""
	}
	switch classes.class(typeNode.Symbol()) {
	case cppKindTypeIdentifier:
		return typeNode.Content(src)
	case cppKindTemplateType:
		// The instantiated type's own name, with the argument list dropped.
		for i := range int(typeNode.NamedChildCount()) {
			if text := cppQualTypeText(classes, typeNode.NamedChild(i), src); text != "" {
				return text
			}
		}
	case cppKindQualifiedIdentifier:
		var parts []string
		for i := range int(typeNode.NamedChildCount()) {
			child := typeNode.NamedChild(i)
			switch classes.class(child.Symbol()) {
			case cppKindNamespaceIdentifier:
				parts = append(parts, child.Content(src))
			default:
				if text := cppQualTypeText(classes, child, src); text != "" {
					parts = append(parts, text)
				}
			}
		}
		if len(parts) == 0 {
			return ""
		}
		return strings.Join(parts, "::")
	}
	return ""
}
