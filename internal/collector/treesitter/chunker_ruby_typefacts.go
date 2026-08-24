// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	sitter "github.com/smacker/go-tree-sitter"
)

func init() {
	RegisterRubyTypeFacts()
}

// RegisterRubyTypeFacts installs the ruby type-facts arm, exported for the
// restore-not-delete reason RegisterGoTypeFacts states.
func RegisterRubyTypeFacts() {
	RegisterTypeFacts(LangRuby, rubyTypeFacts)
}

// rubyMixinCalls is the CLOSED list of body-level calls that declare
// conformance. Ruby models all three as a plain `call` with an identifier method
// — the grammar has no dedicated node kind for any of them, and the symbol
// census confirms it declares neither `command` nor `method_call` — so the
// identifier's TEXT is the whole discriminator, and keeping it a closed list is
// what stops the arm widening to every one-argument call in a class body.
var rubyMixinCalls = map[string]bool{
	"include": true,
	"extend":  true,
	"prepend": true,
}

// rubyTypeFacts records a ruby declaration's declared conformance and the
// contract predicate.
//
// RUBY DECLARES NO TYPES ANYWHERE ELSE: no parameter annotations, no field
// annotations and no return types, so Fields and Results stay empty for every
// ruby declaration and only Conforms and IsInterface are ever set. Sig is never
// set, for the reason every non-Go arm states: it feeds signature-key matching
// the method-set derivation's non-Go skip makes unreachable from here.
func rubyTypeFacts(declNode *sitter.Node, chunkType string, src []byte) *TypeFacts {
	if declNode == nil {
		return nil
	}
	classes := rbKinds()

	switch chunkType {
	case "class":
		facts := TypeFacts{Conforms: rubyClassConformance(classes, declNode, src)}
		if len(facts.Conforms) == 0 {
			return nil
		}
		return &facts

	case "module":
		// A RUBY MODULE IS THE LANGUAGE'S CONTRACT CONSTRUCT. It cannot be
		// instantiated and exists to be mixed into classes, which is exactly the
		// relationship the conformance edge records; a class is the concrete thing
		// on the other end.
		//
		// THE CONSEQUENCE FOR THE OTHER SHAPE IS DELIBERATE: `class A < Base`
		// resolves to a CLASS, which is not a contract, so it emits nothing and is
		// counted as a non-contract instead. Captured, counted, and honest —
		// fanning a call resolved to a concrete base class across its subclasses
		// would state something this edge does not mean.
		return &TypeFacts{IsInterface: true, Conforms: rubyModuleConformance(classes, declNode, src)}
	}
	return nil
}

// rubyClassConformance captures a class's superclass and its body-level mixins.
func rubyClassConformance(classes symbolClasses, decl *sitter.Node, src []byte) []DeclaredSupertype {
	var out []DeclaredSupertype
	if sup := rubyFirstChildOfClass(classes, decl, rbKindSuperclass); sup != nil {
		// The superclass node holds an ANONYMOUS `<` and the constant, so the
		// constant is read as the named child and the operator is never named in
		// any kind map.
		if text := rubySupertypeText(classes, sup, src); text != "" {
			out = append(out, DeclaredSupertype{Text: text, Kind: ConformExtends})
		}
	}
	return append(out, rubyBodyMixins(classes, decl, src)...)
}

// rubyModuleConformance captures a module's body-level mixins. A module has no
// superclass clause; it composes only by mixing other modules in.
func rubyModuleConformance(classes symbolClasses, decl *sitter.Node, src []byte) []DeclaredSupertype {
	return rubyBodyMixins(classes, decl, src)
}

// rubyBodyMixins captures the include / extend / prepend calls declared at the
// container's OWN body level.
//
// BODY LEVEL ONLY, AND THE WALK IS NON-RECURSIVE FOR EXACTLY THAT REASON. An
// `include` inside a method body is a runtime call on whatever receiver is in
// scope, not a declaration of conformance, and admitting it would manufacture
// edges out of control flow. The measured shapes separate cleanly: a body-level
// mixin sits at container -> body_statement -> call, while a method-body one
// sits a further two levels down under a `method` node — so scanning only the
// body_statement's direct children draws the line without any depth bookkeeping.
func rubyBodyMixins(classes symbolClasses, decl *sitter.Node, src []byte) []DeclaredSupertype {
	body := rubyFirstChildOfClass(classes, decl, rbKindBodyStatement)
	if body == nil {
		return nil
	}
	var out []DeclaredSupertype
	for i := range int(body.NamedChildCount()) {
		call := body.NamedChild(i)
		if classes.class(call.Symbol()) != rbKindCall || call.NamedChildCount() < 2 {
			continue
		}
		method := call.NamedChild(0)
		if classes.class(method.Symbol()) != rbKindIdentifier || !rubyMixinCalls[method.Content(src)] {
			continue
		}
		args := rubyFirstChildOfClass(classes, call, rbKindArgumentList)
		if args == nil {
			continue
		}
		for j := range int(args.NamedChildCount()) {
			if text := rubySupertypeText(classes, args.NamedChild(j), src); text != "" {
				out = append(out, DeclaredSupertype{Text: text, Kind: ConformMixin})
			}
		}
	}
	return out
}

// rubySupertypeText renders a supertype spelling from a node that either IS a
// name or CONTAINS one, or "" to decline.
//
// A scope_resolution (`Foo::Bar`) records its written spelling with the
// qualifier RETAINED, per the carrier's normalization contract: binding a name
// to a scope is the parser's job and it happens against the declaring file's
// imports, so stripping the qualifier here would destroy the parser's only
// input. Anything that is not a name — a method call, a variable — declines.
func rubySupertypeText(classes symbolClasses, node *sitter.Node, src []byte) string {
	switch classes.class(node.Symbol()) {
	case rbKindConstant, rbKindScopeResolution:
		return node.Content(src)
	case rbKindSuperclass:
		if inner := rubyFirstNameChild(classes, node); inner != nil {
			return inner.Content(src)
		}
	}
	return ""
}

// rubyFirstNameChild returns a node's first direct named child that is a
// constant or a scope resolution.
func rubyFirstNameChild(classes symbolClasses, node *sitter.Node) *sitter.Node {
	for i := range int(node.NamedChildCount()) {
		switch child := node.NamedChild(i); classes.class(child.Symbol()) {
		case rbKindConstant, rbKindScopeResolution:
			return child
		}
	}
	return nil
}

// rubyFirstChildOfClass returns a node's first direct named child of one class,
// or nil.
func rubyFirstChildOfClass(classes symbolClasses, node *sitter.Node, class uint8) *sitter.Node {
	for i := range int(node.NamedChildCount()) {
		if child := node.NamedChild(i); classes.class(child.Symbol()) == class {
			return child
		}
	}
	return nil
}
