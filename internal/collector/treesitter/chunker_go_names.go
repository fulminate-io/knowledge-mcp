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

// goIsTypeKeyword reports whether a node's text spells one of the two reserved
// words above.
//
// IT NEVER MATERIALIZES THE TEXT. Node.Content copies the span into a fresh Go
// string, and every caller here throws that string away after one lookup in a
// two-entry map — a per-identifier allocation, paid for every name of every
// parameter and result of every declaration the type-facts arm examines. The
// `m[string(byteSlice)]` form is recognized by the compiler and performs the
// lookup against the bytes in place, so this asks the same question for
// nothing.
func goIsTypeKeyword(node *sitter.Node, src []byte) bool {
	return goTypeKeywords[string(src[node.StartByte():node.EndByte()])]
}

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
//
// IT RETURNS A COUNT, NOT A SLICE, and the identifiers are the node's named
// children at [0, count) — which is the run's definition, so the count loses
// nothing a caller could want. Collecting them cost one slice allocation per
// declaration examined, on a path the type-facts arm walks for every parameter
// and every result of every declaration, while re-reading a child by index is
// FREE: the tree memoizes each node's Go wrapper on first access
// (Tree.cachedNode), so the second read of a child returns the same pointer
// without allocating. The slice was the only allocation here.
func goLeadingIdentifiers(node *sitter.Node) (count int, next *sitter.Node) {
	classes := goKinds()
	n := int(node.NamedChildCount())
	i := 0
	for ; i < n && classes.class(node.NamedChild(i).Symbol()) == goKindIdentifier; i++ {
	}
	if i >= n {
		return i, nil
	}
	return i, node.NamedChild(i)
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
//
// LIKE goLeadingIdentifiers, IT RETURNS THE NAMES AS A COUNT: they are the
// node's named children at [0, names), and a caller that needs their spellings
// reads them back by index for nothing.
func goNamesAndType(node *sitter.Node, src []byte) (names int, typeNode *sitter.Node) {
	names, next := goLeadingIdentifiers(node)
	if names == 0 || next == nil {
		return names, nil
	}
	for i := range names {
		if goIsTypeKeyword(node.NamedChild(i), src) {
			return 0, nil
		}
	}
	if goKinds().class(next.Symbol()) != goKindExpressionList {
		typeNode = next
	}
	return names, typeNode
}
