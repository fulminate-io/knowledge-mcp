// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	sitter "github.com/smacker/go-tree-sitter"
)

func init() {
	RegisterSwiftQualifierTypes()
	RegisterSwiftTypeFacts()
}

// RegisterSwiftQualifierTypes installs the swift qualifier-type arm. It is
// EXPORTED for the reason RegisterGoQualifierTypes documents: a test that takes
// the arm OUT to measure an unarmed baseline must RESTORE the production
// registration on cleanup, and UnregisterQualifierTypes DELETES the entry
// rather than parking it.
func RegisterSwiftQualifierTypes() {
	RegisterQualifierTypes(LangSwift, swiftQualifierTypes)
}

// RegisterSwiftTypeFacts installs the swift type-facts arm, exported for the
// same restore-not-delete reason as RegisterSwiftQualifierTypes.
func RegisterSwiftTypeFacts() {
	RegisterTypeFacts(LangSwift, swiftTypeFacts)
}

// swiftTypeFacts records the declared-conformance facts swift's inheritance
// clause carries: every supertype a type, extension or protocol names, and the
// fact that a protocol is a contract.
//
// EVERY SUPERTYPE IS RECORDED WITH THE KIND `undeclared`, SUPERCLASS AND
// PROTOCOL ALIKE, AND THAT IS THE HONEST ANSWER RATHER THAN A FALLBACK.
// `class Server: Base, Greeter` produces two structurally identical
// inheritance_specifier nodes, and NOTHING in the parse tree says which named a
// class and which named a protocol. Guessing here would state a fact the syntax
// does not carry. The distinction is decidable, but only against a COMPLETE
// declaration index — the deciding fact lives in the supertype's OWN
// declaration, which may be in another file — so the emission path applies it:
// a supertype resolving to a non-contract declaration emits nothing and is
// counted separately from one that resolves to nothing at all.
//
// A PROTOCOL'S OWN INHERITANCE CLAUSE IS RECORDED TOO, so `protocol A: B`
// yields an edge from B once resolution runs. A refinement is a conformance
// like any other, and dropping it would lose the only relationship a protocol
// hierarchy has.
//
// ONE NODE KIND SERVES class, struct AND extension in this grammar, which is
// why no arm below tells them apart: an extension contributes conformance to
// the type it extends, so recording its clause under the extended type's name
// is exactly right.
//
// EVERY CONTAINER OF A REOPENABLE TYPE IS MARKED PartialBody, THE TYPE AND ITS
// EXTENSIONS ALIKE, and it is set unconditionally rather than read off a
// keyword because swift has none to read: "a type and its extensions are ONE
// owner" is a rule of the language rather than a property a declaration opts
// into, so every class, struct and extension carries it. That is why the same
// node kind serving all three is a convenience here rather than a limitation.
// A conformance declared on either half is then satisfied by a requirement
// implemented in the other, which is what a swift reader means by conformance.
//
// A PROTOCOL IS DELIBERATELY NOT MARKED, and the exclusion is LOAD-BEARING. A
// protocol extension supplies DEFAULT IMPLEMENTATIONS of the very requirements
// the protocol declares, so marking the protocol would place a requirement and
// its default under ONE owner, hand the supertype side two candidates for one
// name, and lose the member edge. Measured on the mutated tree: the same
// fixture reports member_pairs=0 with ambiguous_member=1. Left unmarked, the
// two stay attributable to different owners — the requirement pairs and the
// extension's default declines, which is what a swift reader means by a default
// implementation.
//
// IT BECAME LOAD-BEARING RATHER THAN ALWAYS HAVING BEEN, and the distinction is
// why this block reads as it does. Such a protocol used to be declined a whole
// LAYER ABOVE member pairing: a protocol carrying an extension anywhere in its
// module shares its declaration key with that extension, so the supertype
// spelling resolved to two declarations and was declined as ambiguous, no
// type-level pair formed, and no member lookup ran for the flag to change. The
// parser now narrows that collided set to the contract its extension reopens,
// so the pair forms and this flag decides a real outcome on every one of them.
//
// THE NARROWING'S OWN SAFETY DOES NOT REST ON THIS FLAG, which is worth writing
// down because a wrong reason on a right rule is the sentence that later
// licenses loosening it. PartialBody is set on every reopenable container
// WITHOUT CONDITION, and one node kind serves class, struct, enum, actor and
// extension, so an unrelated `class Greeter` is indistinguishable from an
// extension of `protocol Greeter` in every field the narrowing reads —
// measured: such a fixture DOES narrow. What makes the narrowing sound is that
// the collision cannot arise. Two same-named top-level types in one swift
// module do not compile, and swiftModuleScope (chunker_refsite.go:281) keys
// `Sources/X` and `Tests/X` as different scopes, so a same-named type in a test
// target never joins the candidate set.
//
// EVERY OTHER TypeFacts FIELD STAYS ZERO. Nothing in this ticket consumes a
// swift result type, field type or signature, and a carrier filled with no
// consumer is a dead field that later readers must still keep true.
func swiftTypeFacts(declNode *sitter.Node, _ string, src []byte) *TypeFacts {
	if declNode == nil {
		return nil
	}
	classes := swiftKinds()
	switch classes.class(declNode.Symbol()) {
	case swiftKindClassDeclaration:
		// NO EMPTY-CONFORMANCE EARLY RETURN. A container declaring no supertype
		// still states the co-ownership fact, and it is exactly the half that
		// declares nothing — a bare `class Server` beside `extension Server:
		// Greeter`, or a bare extension beside a conforming class — whose flag
		// decides whether the crossing member survives.
		return &TypeFacts{
			Conforms:    swiftDeclaredSupertypes(classes, declNode, src),
			PartialBody: true,
		}
	case swiftKindProtocolDeclaration:
		return &TypeFacts{
			Conforms:    swiftDeclaredSupertypes(classes, declNode, src),
			IsInterface: true,
		}
	}
	return nil
}

// swiftDeclaredSupertypes collects every supertype spelling a declaration's
// inheritance clause names, IN SOURCE ORDER.
//
// THE CLAUSE IS READ AS A NODE, NEVER AS A TOKEN. The `:` introducing it is
// anonymous in this grammar and so cannot be classified at all, while
// `inheritance_specifier` is a regular kind — so the walk reads the specifiers
// and the colon never enters the picture.
func swiftDeclaredSupertypes(classes symbolClasses, declNode *sitter.Node, src []byte) []DeclaredSupertype {
	var out []DeclaredSupertype
	for i := range int(declNode.NamedChildCount()) {
		child := declNode.NamedChild(i)
		if classes.class(child.Symbol()) != swiftKindInheritanceSpecifier {
			continue
		}
		text := swiftSpecifierText(classes, child, src)
		if text == "" {
			continue
		}
		out = append(out, DeclaredSupertype{Text: text, Kind: ConformUndeclared})
	}
	return out
}

// swiftSpecifierText returns the type spelling one inheritance specifier names.
//
// It descends to the specifier's own type node and normalizes it through the
// SAME closed allowlist a qualifier binds through, so a supertype and a
// qualifier of the same type produce the same text and resolve to the same
// declaration.
func swiftSpecifierText(classes symbolClasses, specifier *sitter.Node, src []byte) string {
	for i := range int(specifier.NamedChildCount()) {
		child := specifier.NamedChild(i)
		switch classes.class(child.Symbol()) {
		case swiftKindUserType, swiftKindTypeIdentifier:
			return swiftQualTypeText(classes, child, src)
		}
	}
	return ""
}

// The swift kind classes. swiftKindOther is the ZERO VALUE and therefore the
// class of every symbol the table does not name, which is what makes an
// unclassified symbol behave like a kind no arm matches rather than like a
// wrong one.
//
// Every constant below names a kind this file SPELLS. `class`, `protocol`,
// `extension` and `:` are deliberately ABSENT: the swift grammar carries no
// REGULAR symbol for any of them, and newSymbolClasses panics on a kinds-map
// name it never assigned. That constraint bites hardest in this language,
// because ONE node kind — class_declaration — serves `class`, `struct` AND
// `extension`, and the distinguishing token is the anonymous one sitting first.
// Nothing here needs to tell them apart: the query makes the distinction for
// chunking, and `self` binds to whichever container the walk finds.
const (
	swiftKindOther uint8 = iota
	swiftKindClassDeclaration
	swiftKindProtocolDeclaration
	swiftKindFunctionDeclaration
	swiftKindProtocolFunctionDeclaration
	swiftKindParameter
	swiftKindLambdaParameter
	swiftKindPropertyDeclaration
	swiftKindPattern
	swiftKindTypeAnnotation
	swiftKindSimpleIdentifier
	swiftKindTypeIdentifier
	swiftKindUserType
	swiftKindOptionalType
	swiftKindInheritanceSpecifier
	swiftKindCallExpression
	swiftKindNavigationExpression
	swiftKindCallSuffix
	swiftKindValueArguments
	swiftKindValueArgument
	swiftKindAssignment
	swiftKindDirectlyAssignableExpression
	swiftKindNavigationSuffix
	swiftKindControlTransferStatement
	swiftKindSelfExpression
	swiftKindLambdaLiteral
)

// swiftKindNames maps every swift node-kind spelling this arm names onto its
// class code. It is the input to newSymbolClasses, so a kind added to the walk
// without an entry here classifies as swiftKindOther and binds nothing rather
// than mis-binding.
var swiftKindNames = map[string]uint8{
	"class_declaration":              swiftKindClassDeclaration,
	"protocol_declaration":           swiftKindProtocolDeclaration,
	"function_declaration":           swiftKindFunctionDeclaration,
	"protocol_function_declaration":  swiftKindProtocolFunctionDeclaration,
	"parameter":                      swiftKindParameter,
	"lambda_parameter":               swiftKindLambdaParameter,
	"property_declaration":           swiftKindPropertyDeclaration,
	"pattern":                        swiftKindPattern,
	"type_annotation":                swiftKindTypeAnnotation,
	"simple_identifier":              swiftKindSimpleIdentifier,
	"type_identifier":                swiftKindTypeIdentifier,
	"user_type":                      swiftKindUserType,
	"optional_type":                  swiftKindOptionalType,
	"inheritance_specifier":          swiftKindInheritanceSpecifier,
	"call_expression":                swiftKindCallExpression,
	"navigation_expression":          swiftKindNavigationExpression,
	"call_suffix":                    swiftKindCallSuffix,
	"value_arguments":                swiftKindValueArguments,
	"value_argument":                 swiftKindValueArgument,
	"assignment":                     swiftKindAssignment,
	"directly_assignable_expression": swiftKindDirectlyAssignableExpression,
	"navigation_suffix":              swiftKindNavigationSuffix,
	"control_transfer_statement":     swiftKindControlTransferStatement,
	"self_expression":                swiftKindSelfExpression,
	"lambda_literal":                 swiftKindLambdaLiteral,
}

// swiftKindTable memoizes the swift class table for the process.
var swiftKindTable = kindTable{lang: LangSwift, names: swiftKindNames}

// swiftKinds returns the memoized swift symbol class table.
func swiftKinds() symbolClasses {
	return swiftKindTable.get()
}

// swiftQualifierTypes is the swift arm: one walk of a declaration's subtree,
// returning the qualifier names it makes visible mapped to their declared
// types.
//
// It is a plain recursive node walk rather than a tree-sitter query, for the
// reason goQualifierTypes states: a QueryCursor is a cgo handle that must be
// closed on every path, and this walk needs no pattern matching NamedChild
// cannot express.
func swiftQualifierTypes(declNode *sitter.Node, src []byte) map[string]QualType {
	if declNode == nil {
		return nil
	}
	b := &qualBinder{classes: swiftKinds()}

	bindSwiftSelf(b, declNode, src)
	// A function's parameters are its DIRECT named children rather than
	// entries in a list node, so they are bound here rather than in the walk —
	// which keeps a nested declaration's parameters out of this declaration's
	// map.
	for i := range int(declNode.NamedChildCount()) {
		child := declNode.NamedChild(i)
		if b.classes.class(child.Symbol()) == swiftKindParameter {
			bindSwiftAnnotatedPair(b, child, src)
		}
	}
	walkSwiftQualifiers(b, declNode, src)

	if len(b.types) == 0 {
		return nil
	}
	return b.types
}

// bindSwiftSelf binds `self` to the name of the enclosing type, extension or
// protocol.
//
// A CLASS AND AN EXTENSION ARE THE SAME NODE KIND and are deliberately NOT told
// apart: `extension Server` contributes members to Server, so a method in
// either one sees the same `self`. The two differ only in what the `name:`
// field binds — a type_identifier for a class or struct, a user_type for an
// extension — and the type-text allowlist accepts both.
func bindSwiftSelf(b *qualBinder, declNode *sitter.Node, src []byte) {
	switch b.classes.class(declNode.Symbol()) {
	case swiftKindFunctionDeclaration, swiftKindProtocolFunctionDeclaration:
	default:
		return
	}
	for n := declNode.Parent(); n != nil; n = n.Parent() {
		switch b.classes.class(n.Symbol()) {
		case swiftKindClassDeclaration, swiftKindProtocolDeclaration:
			b.bind("self", QualType{Text: swiftQualTypeText(b.classes, n.ChildByFieldName("name"), src)})
			return
		}
	}
}

// bindSwiftAnnotatedPair binds one declaration that carries an identifier and a
// type: a `parameter`, a `lambda_parameter`, or a `property_declaration` whose
// annotation is present.
//
// The identifier and the type are found BY KIND among the named children rather
// than by ordinal, because the three shapes arrange them differently: a
// parameter puts the identifier and the type side by side, while a property
// wraps them in a `pattern` and a `type_annotation`.
func bindSwiftAnnotatedPair(b *qualBinder, node *sitter.Node, src []byte) {
	var name, typeNode *sitter.Node
	for i := range int(node.NamedChildCount()) {
		child := node.NamedChild(i)
		switch b.classes.class(child.Symbol()) {
		case swiftKindSimpleIdentifier:
			if name == nil {
				name = child
			}
		case swiftKindPattern:
			if name == nil {
				name = swiftFirstOfKind(b, child, swiftKindSimpleIdentifier)
			}
		case swiftKindTypeAnnotation:
			if typeNode == nil {
				typeNode = swiftAnnotatedType(b, child)
			}
		case swiftKindUserType, swiftKindOptionalType:
			if typeNode == nil {
				typeNode = child
			}
		}
	}
	if name == nil || typeNode == nil {
		return
	}
	b.bind(name.Content(src), QualType{Text: swiftQualTypeText(b.classes, typeNode, src)})
}

// swiftAnnotatedType returns the type a `type_annotation` carries. Its own
// first child is the anonymous colon, so the type is the first NAMED child.
func swiftAnnotatedType(b *qualBinder, annotation *sitter.Node) *sitter.Node {
	for i := range int(annotation.NamedChildCount()) {
		child := annotation.NamedChild(i)
		switch b.classes.class(child.Symbol()) {
		case swiftKindUserType, swiftKindOptionalType:
			return child
		}
	}
	return nil
}

// swiftFirstOfKind returns a node's first direct named child of one class.
func swiftFirstOfKind(b *qualBinder, node *sitter.Node, kind uint8) *sitter.Node {
	for i := range int(node.NamedChildCount()) {
		if child := node.NamedChild(i); b.classes.class(child.Symbol()) == kind {
			return child
		}
	}
	return nil
}

// swiftQualTypeText renders a type expression as the text a qualifier's type is
// recorded under, or "" to decline it.
//
// IT IS A CLOSED ALLOWLIST, for the reason goQualTypeText's own comment gives:
// declining by default is what keeps a container from binding a method its
// value does not have. An array, a dictionary and a function type all reach the
// default and decline, and so does an optional wrapping any of them, because
// the optional arm re-enters through this function rather than digging out the
// first type identifier it can find.
//
// AN OPTIONAL IS STRIPPED, NOT DECLINED. `Server?` has Server's members —
// reaching them needs unwrapping in the source, but the declaration a call
// through it targets is Server's.
//
// A NESTED OR GENERIC user_type KEEPS ONLY ITS LEADING type_identifier, which
// is the same shape the other arms use for a generic instantiation: the type
// arguments name other declarations, not this one.
//
// It takes the class table rather than the binder because the type-facts arm
// normalizes a declared supertype through it and binds nothing.
func swiftQualTypeText(classes symbolClasses, typeNode *sitter.Node, src []byte) string {
	if typeNode == nil {
		return ""
	}
	switch classes.class(typeNode.Symbol()) {
	case swiftKindTypeIdentifier:
		return typeNode.Content(src)
	case swiftKindUserType, swiftKindOptionalType:
		for i := range int(typeNode.NamedChildCount()) {
			child := typeNode.NamedChild(i)
			switch classes.class(child.Symbol()) {
			case swiftKindTypeIdentifier, swiftKindUserType, swiftKindOptionalType:
				return swiftQualTypeText(classes, child, src)
			}
		}
	}
	return ""
}

// walkSwiftQualifiers descends one declaration binding the local syntax that
// makes a qualifier visible: annotated local properties and closure
// parameters, at any depth.
//
// AN UNANNOTATED PROPERTY BINDS NOTHING. This arm reads declared types and does
// not infer one from the initializer, matching the Go arm's treatment of an
// untyped declaration.
//
// THE SWITCH IS ON THE NUMERIC SYMBOL, NOT ON THE KIND NAME, and that is the
// measured cost of this function: reading a node's kind name converts a cgo
// C-string into a FRESH Go string on every call, so a recursive walk that names
// every named node at every depth allocates once per node visited. b.classes
// turns the symbol the binding already holds into one bounds-checked index.
func walkSwiftQualifiers(b *qualBinder, node *sitter.Node, src []byte) {
	for i := range int(node.NamedChildCount()) {
		child := node.NamedChild(i)
		switch b.classes.class(child.Symbol()) {
		case swiftKindPropertyDeclaration, swiftKindLambdaParameter:
			// A closure's parameters are local syntax of the SAME declaration,
			// so they are qualifiers of it at any depth — while an ordinary
			// `parameter` is bound only at the declaration's own top level, so
			// a nested function's signature stays out of this map.
			bindSwiftAnnotatedPair(b, child, src)
		}
		walkSwiftQualifiers(b, child, src)
	}
}
