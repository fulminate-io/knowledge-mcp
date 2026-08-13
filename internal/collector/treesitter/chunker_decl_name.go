// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	sitter "github.com/smacker/go-tree-sitter"
)

// declNameResolver recovers a declaration's name from the parsed node.
// Returning "" leaves the chunk unnamed exactly as today.
type declNameResolver func(declNode *sitter.Node, src []byte, chunkType string) string

// declNameResolvers is a closed per-language registry populated by each
// chunker_<lang>.go init, mirroring testKindClassifiers.
//
// A resolver exists instead of a tightened TopLevel query because a tree-sitter
// pattern that names a field FILTERS on it as well as capturing it, so a
// declaration lacking that field stops being chunked at all. Measured against
// this package's own corpus fixtures: requiring pattern:(value_name) on OCaml's
// value_definition drops `let () = ...` and `let%test "x" = ...`; requiring
// name:(namespace_name) on PHP's namespace_definition drops the unnamed global
// `namespace { ... }`; and an unanchored Lua variable_declarator pattern matches
// `local a, b = 1, 2` twice, emitting two chunks over one byte range. Resolving
// on the parsed node leaves the chunk set byte-identical and adds only the Name.
var declNameResolvers = map[Language]declNameResolver{}

// fieldNamed returns the text of n's `field` child when it is of kind want,
// else "". The kind check is what keeps a resolver from naming a declaration
// after a node the query would not have chunked.
func fieldNamed(n *sitter.Node, src []byte, field, want string) string {
	c := n.ChildByFieldName(field)
	if c == nil || c.Type() != want {
		return ""
	}
	return c.Content(src)
}

// firstNamedChildOfKind returns n's first direct named child of kind, or nil.
func firstNamedChildOfKind(n *sitter.Node, kind string) *sitter.Node {
	for i := range int(n.NamedChildCount()) {
		if c := n.NamedChild(i); c.Type() == kind {
			return c
		}
	}
	return nil
}

// countNamedChildrenOfKind returns how many direct named children of n are of
// kind. Used where naming a declaration is only correct when it declares
// exactly one thing.
func countNamedChildrenOfKind(n *sitter.Node, kind string) int {
	count := 0
	for i := range int(n.NamedChildCount()) {
		if n.NamedChild(i).Type() == kind {
			count++
		}
	}
	return count
}
