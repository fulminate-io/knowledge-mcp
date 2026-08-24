// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	sitter "github.com/smacker/go-tree-sitter"
)

func init() {
	RegisterRustQualifierTypes()
	RegisterRustTypeFacts()
}

// RegisterRustQualifierTypes installs the rust qualifier-type arm. It is
// EXPORTED for the reason RegisterGoQualifierTypes documents: a test that takes
// the arm OUT to measure an unarmed baseline must RESTORE the production
// registration on cleanup, and UnregisterQualifierTypes DELETES the entry
// rather than parking it.
func RegisterRustQualifierTypes() {
	RegisterQualifierTypes(LangRust, rustQualifierTypes)
}

// RegisterRustTypeFacts installs the rust type-facts arm, exported for the same
// restore-not-delete reason as RegisterRustQualifierTypes.
func RegisterRustTypeFacts() {
	RegisterTypeFacts(LangRust, rustTypeFacts)
}

// rustTypeFacts records the two declared-conformance facts rust's grammar
// carries: the trait an impl block implements, and the fact that a trait is a
// contract.
//
// RUST IS THE ONE LANGUAGE IN THIS TICKET THAT CAN NAME ITS CLAUSE HONESTLY.
// An impl_item's `trait:` slot is filled only by `impl Trait for Type`, so the
// kind recorded is ConformTrait rather than ConformUndeclared — where swift's
// inheritance_specifier and cpp's base_class_clause are structurally identical
// for a superclass and a contract, and must record what the tree actually says.
//
// AN INHERENT IMPL DECLARES NO CONFORMANCE. `impl Server { }` fills no trait
// slot and returns nil, which is the carrier's own "declares no supertype"
// answer rather than an empty list.
//
// GENERICS ARE NOT UNIFIED — first-order conformance only, the same documented
// limitation Go generics carry. NO DECLINE IS WRITTEN FOR THEM, and the reason
// is mechanical rather than a choice made here: TopLevel matches
// `(impl_item type: (type_identifier) @name)`, while `impl<T> Gen<T> for W<T>`
// binds a generic_type in that slot — so the block produces no chunk at all and
// this arm is never reached for it. A decline arm here would be dead code
// guarding a case that cannot arrive.
//
// EVERY OTHER TypeFacts FIELD STAYS ZERO. Nothing in this ticket consumes a
// rust result type, field type or signature, and a carrier filled with no
// consumer is a dead field that later readers must still keep true.
func rustTypeFacts(declNode *sitter.Node, _ string, src []byte) *TypeFacts {
	if declNode == nil {
		return nil
	}
	classes := rustKinds()
	switch classes.class(declNode.Symbol()) {
	case rustKindImplItem:
		text := rustQualTypeText(classes, declNode.ChildByFieldName("trait"), src)
		if text == "" {
			return nil
		}
		return &TypeFacts{Conforms: []DeclaredSupertype{{Text: text, Kind: ConformTrait}}}
	case rustKindTraitItem:
		return &TypeFacts{IsInterface: true}
	}
	return nil
}

// The rust kind classes. rustKindOther is the ZERO VALUE and therefore the
// class of every symbol the table does not name, which is what makes an
// unclassified symbol behave like a kind no arm matches rather than like a
// wrong one.
//
// Every constant below names a kind this file SPELLS. `impl`, `for` and `trait`
// are deliberately ABSENT: the rust grammar carries no REGULAR symbol for any of
// them, and newSymbolClasses panics on a kinds-map name it never assigned. The
// trait slot is read through ChildByFieldName rather than by walking to a
// keyword, so no keyword needs classifying at all.
const (
	rustKindOther uint8 = iota
	rustKindFunctionItem
	rustKindImplItem
	rustKindTraitItem
	rustKindParameters
	rustKindParameter
	rustKindSelfParameter
	rustKindLetDeclaration
	rustKindIdentifier
	rustKindTypeIdentifier
	rustKindScopedTypeIdentifier
	rustKindGenericType
	rustKindReferenceType
	rustKindDynamicType
	rustKindMutableSpecifier
	rustKindClosureExpression
	rustKindClosureParameters
)

// rustKindNames maps every rust node-kind spelling this arm names onto its
// class code. It is the input to newSymbolClasses, so a kind added to the walk
// without an entry here classifies as rustKindOther and binds nothing rather
// than mis-binding.
var rustKindNames = map[string]uint8{
	"function_item":          rustKindFunctionItem,
	"impl_item":              rustKindImplItem,
	"trait_item":             rustKindTraitItem,
	"parameters":             rustKindParameters,
	"parameter":              rustKindParameter,
	"self_parameter":         rustKindSelfParameter,
	"let_declaration":        rustKindLetDeclaration,
	"identifier":             rustKindIdentifier,
	"type_identifier":        rustKindTypeIdentifier,
	"scoped_type_identifier": rustKindScopedTypeIdentifier,
	"generic_type":           rustKindGenericType,
	"reference_type":         rustKindReferenceType,
	"dynamic_type":           rustKindDynamicType,
	"mutable_specifier":      rustKindMutableSpecifier,
	"closure_expression":     rustKindClosureExpression,
	"closure_parameters":     rustKindClosureParameters,
}

// rustKindTable memoizes the rust class table for the process.
var rustKindTable = kindTable{lang: LangRust, names: rustKindNames}

// rustKinds returns the memoized rust symbol class table.
func rustKinds() symbolClasses {
	return rustKindTable.get()
}

// rustQualifierTypes is the rust arm: one walk of a declaration's subtree,
// returning the qualifier names it makes visible mapped to their declared
// types.
//
// It is a plain recursive node walk rather than a tree-sitter query, for the
// reason goQualifierTypes states: a QueryCursor is a cgo handle that must be
// closed on every path, and this walk needs no pattern matching NamedChild
// cannot express.
func rustQualifierTypes(declNode *sitter.Node, src []byte) map[string]QualType {
	if declNode == nil {
		return nil
	}
	b := &qualBinder{classes: rustKinds()}

	// The receiver, then the signature, then the body — the order the Go arm
	// uses, and under the same first-binding-wins rule qualBinder implements.
	bindRustSelf(b, declNode, src)
	bindRustParameterList(b, rustParameterList(b, declNode), src)
	walkRustQualifiers(b, declNode, src)

	if len(b.types) == 0 {
		return nil
	}
	return b.types
}

// bindRustSelf binds `self` to the type the enclosing impl or trait block names.
//
// THE SLOT IS READ BY FIELD NAME, NEVER BY WALKING TO A KEYWORD. An impl_item's
// children are the anonymous `impl`, an optional type_parameters, an optional
// TRAIT node, the anonymous `for`, then the TYPE node — so "the node before the
// `for`" invites a token walk over symbols the grammar declares no regular id
// for. `type:` names the implementing type in both the `impl T` and the
// `impl Trait for T` arrangements, so one field read covers both.
//
// A trait's own methods bind `self` to the TRAIT, which is what makes a call on
// `self` inside a default method resolve to the trait's own member.
//
// IT REQUIRES A RECEIVER. An associated function declares no `self`, so nothing
// in its body can name one; binding it anyway would record a qualifier the
// source cannot write.
func bindRustSelf(b *qualBinder, declNode *sitter.Node, src []byte) {
	if b.classes.class(declNode.Symbol()) != rustKindFunctionItem {
		return
	}
	if !rustHasSelfParameter(b, declNode) {
		return
	}
	for n := declNode.Parent(); n != nil; n = n.Parent() {
		switch b.classes.class(n.Symbol()) {
		case rustKindImplItem:
			b.bind("self", QualType{Text: rustQualTypeText(b.classes, n.ChildByFieldName("type"), src)})
			return
		case rustKindTraitItem:
			b.bind("self", QualType{Text: rustQualTypeText(b.classes, n.ChildByFieldName("name"), src)})
			return
		}
	}
}

// rustHasSelfParameter reports whether a function declares a receiver.
func rustHasSelfParameter(b *qualBinder, declNode *sitter.Node) bool {
	list := rustParameterList(b, declNode)
	if list == nil {
		return false
	}
	for i := range int(list.NamedChildCount()) {
		if b.classes.class(list.NamedChild(i).Symbol()) == rustKindSelfParameter {
			return true
		}
	}
	return false
}

// rustParameterList returns a function's `parameters` node, or nil.
//
// It is found BY KIND among the direct named children rather than by ordinal: a
// function_item's children are its name, its parameters, an optional return
// type and its block, and a generic function inserts type_parameters before the
// parameters, so no fixed position holds.
func rustParameterList(b *qualBinder, declNode *sitter.Node) *sitter.Node {
	for i := range int(declNode.NamedChildCount()) {
		child := declNode.NamedChild(i)
		if b.classes.class(child.Symbol()) == rustKindParameters {
			return child
		}
	}
	return nil
}

// bindRustParameterList binds every annotated entry of one parameter list.
//
// A `parameter` carries a `pattern:` and a `type:`. A pattern that is not a
// plain identifier — a tuple or struct destructuring — names no single
// qualifier and binds nothing.
func bindRustParameterList(b *qualBinder, list *sitter.Node, src []byte) {
	if list == nil {
		return
	}
	for i := range int(list.NamedChildCount()) {
		param := list.NamedChild(i)
		if b.classes.class(param.Symbol()) != rustKindParameter {
			continue
		}
		bindRustAnnotated(b, param, src)
	}
}

// bindRustAnnotated binds one `pattern: type` pair, which is the shape a
// parameter and an annotated `let` share.
func bindRustAnnotated(b *qualBinder, node *sitter.Node, src []byte) {
	name := node.ChildByFieldName("pattern")
	if name == nil || b.classes.class(name.Symbol()) != rustKindIdentifier {
		return
	}
	typeNode := node.ChildByFieldName("type")
	if typeNode == nil {
		return
	}
	b.bind(name.Content(src), QualType{Text: rustQualTypeText(b.classes, typeNode, src)})
}

// rustQualTypeText renders a type expression as the text a qualifier's type is
// recorded under, or "" to decline it.
//
// IT IS A CLOSED ALLOWLIST, for the reason goQualTypeText's own comment gives:
// declining by default is what keeps a container from binding a method its
// value does not have. A tuple, a slice, an array and a function type all reach
// the default and decline, and so does a wrapper around any of them, because
// the wrapper arms re-enter through this function rather than digging out the
// first type identifier they can find.
//
// A scoped spelling is returned AS WRITTEN. `crate::mod_a::Thing` keeps its
// path because binding a path to a declaration is the parser's job, against the
// declaring file's own `use` statements.
// It takes the class table rather than the binder because the type-facts arm
// normalizes a declared supertype through it and binds nothing.
func rustQualTypeText(classes symbolClasses, typeNode *sitter.Node, src []byte) string {
	if typeNode == nil {
		return ""
	}
	switch classes.class(typeNode.Symbol()) {
	case rustKindTypeIdentifier, rustKindScopedTypeIdentifier:
		return typeNode.Content(src)
	case rustKindReferenceType, rustKindDynamicType, rustKindGenericType:
		// `&T`, `&mut T`, `&dyn T` and `Box<dyn T>` all record the type they
		// wrap. A generic instantiation records its own name with the type
		// ARGUMENTS dropped, so `Box<dyn Greeter>` records `Box` — the first
		// named child is the instantiated type and the type_arguments node is
		// its sibling.
		return rustQualTypeText(classes, rustWrappedType(classes, typeNode), src)
	}
	return ""
}

// rustWrappedType returns the type a wrapper wraps: its first named child that
// is not the mutable specifier.
//
// THE SKIP IS WHY mutable_specifier IS CLASSIFIED AT ALL. `&mut Config` puts
// the specifier in the first named slot, so a plain first-child rule would
// re-enter on it, find no child of its own, and decline a reference this rung
// can bind.
func rustWrappedType(classes symbolClasses, node *sitter.Node) *sitter.Node {
	for i := range int(node.NamedChildCount()) {
		child := node.NamedChild(i)
		if classes.class(child.Symbol()) == rustKindMutableSpecifier {
			continue
		}
		return child
	}
	return nil
}

// walkRustQualifiers descends one declaration binding the local syntax that
// makes a qualifier visible: annotated `let` bindings and closure parameters,
// at any depth.
//
// An UNANNOTATED `let` binds NOTHING. This arm reads declared types and does
// not infer one from the initializer, matching the Go arm's treatment of an
// untyped declaration.
//
// THE SWITCH IS ON THE NUMERIC SYMBOL, NOT ON THE KIND NAME, and that is the
// measured cost of this function: reading a node's kind name converts a cgo
// C-string into a FRESH Go string on every call, so a recursive walk that names
// every named node at every depth allocates once per node visited. b.classes
// turns the symbol the binding already holds into one bounds-checked index.
func walkRustQualifiers(b *qualBinder, node *sitter.Node, src []byte) {
	for i := range int(node.NamedChildCount()) {
		child := node.NamedChild(i)
		switch b.classes.class(child.Symbol()) {
		case rustKindLetDeclaration:
			bindRustAnnotated(b, child, src)
		case rustKindClosureExpression:
			// Closures are local syntax of the SAME declaration, so their
			// annotated parameters are qualifiers of it, at any depth.
			bindRustParameterList(b, rustClosureParameters(b, child), src)
		}
		walkRustQualifiers(b, child, src)
	}
}

// rustClosureParameters returns a closure's parameter list, or nil. The list is
// a closure_parameters node rather than the `parameters` a function carries,
// but its entries are the same `parameter` kind.
func rustClosureParameters(b *qualBinder, closure *sitter.Node) *sitter.Node {
	for i := range int(closure.NamedChildCount()) {
		child := closure.NamedChild(i)
		if b.classes.class(child.Symbol()) == rustKindClosureParameters {
			return child
		}
	}
	return nil
}
