// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	sitter "github.com/smacker/go-tree-sitter"
)

// How a Go declaration's LEADING IDENTIFIERS are read, and when they are not
// names at all.
//
// THE THREE READERS OF THIS FILE SIT IN THREE OTHERS, which is why it is its
// own: the qualifier binder (chunker_go_qualtypes.go) needs the names a
// declaration introduces, the signature renderer (chunker_go_sig.go) needs the
// TYPES an unnamed run spells, and the type-facts arm
// (chunker_go_typefacts.go) needs the declared results. One splitter serves all
// three, and the reserved-word rule below has to mean the same thing to each of
// them or a qualifier binds to something the signature does not agree exists.

// goTypeKeywords are the Go RESERVED WORDS that begin a type expression and are
// lexed as a bare `identifier` when the grammar mis-reads an all-unnamed
// parameter list as a named one.
//
// THE SET IS CLOSED AND MEASURED. Executing the grammar over the type vocabulary
// inside an unnamed list shows generic, variadic, qualified, pointer, slice,
// function, struct, interface and both directional channel forms each parsing as
// their own single node; only these two fuse with the node that follows them.
// `func`, `struct` and `interface` never appear here because the grammar gives
// each its own node kind rather than an identifier.
//
// Membership is PROOF, not a heuristic: neither word can be a declared name in
// Go, so an identifier spelling one in a name position cannot be a name.
var goTypeKeywords = map[string]bool{"map": true, "chan": true}

// goLeadingIdentifiers splits a parameter_declaration, var_spec or const_spec
// into its leading run of identifiers and the node that FOLLOWS that run.
//
// It reads POSITIONS rather than field names, because the same positional shape
// is what distinguishes `var a, b Pair` (identifier, identifier, type_identifier)
// from `var d, e = 1, 2` (identifier, identifier, expression_list): the node
// after the run of identifiers is the explicit type ONLY when it is not the
// value list.
//
// IT MAKES NO JUDGMENT ABOUT WHAT THE IDENTIFIERS MEAN. Both callers need the
// raw run — goNamesAndType to reject it when it is really a type list, and
// goUnnamedParamExprs to render it as one — so the judgment lives in each of
// them rather than here.
func goLeadingIdentifiers(node *sitter.Node) (ids []*sitter.Node, next *sitter.Node) {
	classes := goKinds()
	n := int(node.NamedChildCount())
	i := 0
	for ; i < n && classes.class(node.NamedChild(i).Symbol()) == goKindIdentifier; i++ {
		ids = append(ids, node.NamedChild(i))
	}
	if i >= n {
		return ids, nil
	}
	return ids, node.NamedChild(i)
}

// goNamesAndType splits a declaration into the names it DECLARES and its
// trailing type node. A spec with no explicit type returns a nil type node and
// binds nothing, which is correct — its type is inferred from the value and this
// arm does not infer.
//
// A RESERVED WORD IN THE RUN PROVES THE RUN IS TYPES, AND THE WHOLE DECLARATION
// THEN DECLARES NO NAMES. Go forbids mixing named and unnamed parameters in one
// list, and tree-sitter resolves an all-unnamed list ending in a type that
// begins with a keyword — `map[K]V`, `chan T` — by reading the run as NAMES: the
// keyword is lexed as a bare identifier in a name position and fused with the
// node after it. `map` and `chan` are RESERVED WORDS that can never be a
// declared name, so their presence is local, sufficient proof of the mis-read.
// Returning no names is what keeps this arm from binding a qualifier called
// `string` to whatever the fusion left behind.
//
// THE SIBLING-DECLARATION SIGNAL WOULD NOT DO, and that is measured rather than
// assumed. "Some other entry in the list is a bare type" is NOT SUFFICIENT —
// `f(string, map[string]string)` mis-parses into a single declaration with no
// bare-type sibling anywhere — and NOT NECESSARY, since `f(int, error)` splits
// correctly with no help. Keying on the fused tree SHAPE instead would be worse
// than either: a genuinely named `c [maxLen]byte` produces the identical
// (identifier, array_type) shape, so a shape rule corrupts correct code.
func goNamesAndType(node *sitter.Node, src []byte) (names []*sitter.Node, typeNode *sitter.Node) {
	names, next := goLeadingIdentifiers(node)
	if len(names) == 0 || next == nil {
		return names, nil
	}
	for _, name := range names {
		if goTypeKeywords[name.Content(src)] {
			return nil, nil
		}
	}
	if goKinds().class(next.Symbol()) != goKindExpressionList {
		typeNode = next
	}
	return names, typeNode
}
