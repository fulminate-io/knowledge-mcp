// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	sitter "github.com/smacker/go-tree-sitter"
)

func init() {
	RegisterElixirTypeFacts()
}

// The elixir kind classes. exKindOther is the ZERO VALUE and therefore the
// class of every symbol the table does not name.
const (
	exKindOther uint8 = iota
	exKindCall
	exKindIdentifier
	exKindArguments
	exKindAlias
	exKindDoBlock
	exKindUnaryOperator
)

// exKindNames maps every elixir node-kind spelling the elixir arm names onto its
// class code.
//
// THE `@` SIGIL IS AN ANONYMOUS TOKEN and is therefore ABSENT by necessity: a
// kind map naming it would panic at first use, because newSymbolClasses walks
// only REGULAR symbols and then rejects any name that assigned nothing. The
// attribute wrapper is reached through the regular `unary_operator` kind instead,
// and the sigil itself through the shared child-token probe.
var exKindNames = map[string]uint8{
	"call":           exKindCall,
	"identifier":     exKindIdentifier,
	"arguments":      exKindArguments,
	"alias":          exKindAlias,
	"do_block":       exKindDoBlock,
	"unary_operator": exKindUnaryOperator,
}

// exKindTable is the elixir instance of the shared memo.
var exKindTable = kindTable{lang: LangElixir, names: exKindNames}

// exKinds returns the memoized elixir symbol class table.
func exKinds() symbolClasses { return exKindTable.get() }

// elixirBehaviourAttrs is the CLOSED list of attribute names that declare
// conformance. Elixir accepts both spellings and treats them identically; every
// other module attribute — @spec, @impl, @doc, @moduledoc, @type — declines,
// which is what keeps the discriminator from widening to any attribute that
// happens to carry an alias.
var elixirBehaviourAttrs = map[string]bool{
	"behaviour": true,
	"behavior":  true,
}

// RegisterElixirTypeFacts installs the elixir type-facts arm, exported for the
// restore-not-delete reason RegisterGoTypeFacts states.
func RegisterElixirTypeFacts() {
	RegisterTypeFacts(LangElixir, elixirTypeFacts)
}

// elixirTypeFacts records a defmodule's declared behaviors and whether it IS a
// behaviour.
//
// ELIXIR CONFORMANCE IS MODULE-LEVEL ONLY, AND THAT IS A SUPPORTED OUTCOME
// RATHER THAN A TRUNCATION. The language has no declaration node kind at all — a
// definition is a `call` whose target is a macro keyword, and so is its
// container — so an elixir function carries no container name and there is no
// member node for a member-level pairing to have two ends of. The type-level
// edge stands alone and is the complete, correct answer here.
//
// THERE IS NO ELIXIR QUALIFIER ARM, for a reason that belongs beside this one: a
// call is `Module.function` or bare, a variable never receives a method call,
// and `map.field` is data access rather than dispatch — so a typed-qualifier
// rung would have nothing to bind.
//
// DEFPROTOCOL, DEFIMPL AND `use` ARE OUT OF SCOPE. The first two are elixir's
// other conformance concept and `use` is macro injection rather than a declared
// supertype; none of the three is read here.
func elixirTypeFacts(declNode *sitter.Node, _ string, src []byte) *TypeFacts {
	if declNode == nil {
		return nil
	}
	classes := exKinds()
	body := elixirFirstChildOfClass(classes, declNode, exKindDoBlock)
	if body == nil {
		return nil
	}

	var conforms []DeclaredSupertype
	var isContract bool
	for i := range int(body.NamedChildCount()) {
		name, args := elixirModuleAttribute(classes, body.NamedChild(i), src)
		switch {
		case elixirBehaviourAttrs[name]:
			conforms = elixirAppendAliases(classes, args, src, conforms)
		case name == "callback":
			// @callback IS HOW ELIXIR DEFINES A BEHAVIOUR, so a module declaring
			// at least one is the language's contract construct. Without this the
			// arm would capture every @behaviour and emit nothing for any of them,
			// because the emitter requires the resolved target to be a contract.
			isContract = true
		}
	}
	if len(conforms) == 0 && !isContract {
		return nil
	}
	return &TypeFacts{Conforms: conforms, IsInterface: isContract}
}

// elixirModuleAttribute returns one do-block entry's attribute name and its
// arguments node, or ("", nil) when the entry is not a module attribute.
//
// THE SIGIL IS READ AS A CHILD TOKEN rather than through the class table,
// because it is anonymous and the class table cannot see anonymous symbols at
// all. Requiring it is what separates an attribute from an ordinary call: a
// bare `callback(x)` in a do-block is a function call, and `@callback` is a
// declaration.
func elixirModuleAttribute(classes symbolClasses, node *sitter.Node, src []byte) (string, *sitter.Node) {
	if classes.class(node.Symbol()) != exKindUnaryOperator || !hasAnonymousChild(node, "@") {
		return "", nil
	}
	call := elixirFirstChildOfClass(classes, node, exKindCall)
	if call == nil {
		return "", nil
	}
	name := elixirFirstChildOfClass(classes, call, exKindIdentifier)
	if name == nil {
		return "", nil
	}
	return name.Content(src), elixirFirstChildOfClass(classes, call, exKindArguments)
}

// elixirAppendAliases appends every alias an attribute's arguments name.
//
// ONLY AN ALIAS COUNTS. `@behaviour Runner` puts an alias directly in the
// arguments, while `@spec run(x) :: :ok` puts a binary_operator there — so
// reading aliases alone is what makes the attribute-name check and the argument
// shape agree instead of one silently covering for the other.
func elixirAppendAliases(
	classes symbolClasses, args *sitter.Node, src []byte, out []DeclaredSupertype,
) []DeclaredSupertype {
	if args == nil {
		return out
	}
	for i := range int(args.NamedChildCount()) {
		arg := args.NamedChild(i)
		if classes.class(arg.Symbol()) != exKindAlias {
			continue
		}
		out = append(out, DeclaredSupertype{Text: arg.Content(src), Kind: ConformBehaviour})
	}
	return out
}

// elixirFirstChildOfClass returns a node's first direct named child of one
// class, or nil.
func elixirFirstChildOfClass(classes symbolClasses, node *sitter.Node, class uint8) *sitter.Node {
	for i := range int(node.NamedChildCount()) {
		if child := node.NamedChild(i); classes.class(child.Symbol()) == class {
			return child
		}
	}
	return nil
}
