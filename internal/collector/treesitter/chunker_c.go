// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	sitter "github.com/smacker/go-tree-sitter"
)

func init() {
	declNameResolvers[LangC] = resolveDeclNameC
}

// resolveDeclNameC names a C VARIABLE declaration from its parsed node.
//
// IT IS A RESOLVER RATHER THAN A TIGHTENED QUERY, for the reason
// declNameResolvers documents and which C makes vivid: a tree-sitter pattern
// naming a field FILTERS on it, so an arm per declarator shape would DELETE
// every shape no arm covers. MEASURED on redis 7.4.0's src/, 616 top-level
// declarations take TWELVE distinct declarator shapes — identifier,
// init>identifier, ptr>identifier, ptr>ptr>identifier, init>ptr>identifier,
// init>ptr>ptr>identifier, array_declarator and four more wrapping a
// function_declarator — and the pointer nesting has no fixed bound, so the
// alternation cannot be written closed. The resolver leaves the chunk set
// BYTE-IDENTICAL and adds only the name.
//
// IT NAMES VARIABLES AND DECLINES PROTOTYPES, AND THAT IS THE LOAD-BEARING
// RULE. A function prototype and its definition share a name in the same file,
// so naming the prototype would put TWO declarations under one key and make
// every call to that function ambiguous — a regression, since an unnamed
// prototype is invisible to the index today. 350 of redis's 616 top-level
// declarations are prototypes. A variable cannot collide the same way: C gives
// a translation unit one declaration of each object name.
//
// A MULTI-DECLARATOR DECLARATION IS DECLINED. `int a, b;` is one node declaring
// two things, and naming it after either would be a partial truth — the same
// rule the Lua resolver applies to `local a, b = 1, 2`.
func resolveDeclNameC(declNode *sitter.Node, src []byte, chunkType string) string {
	if chunkType != "declaration" {
		return ""
	}
	classes := cKinds()
	var only *sitter.Node
	for i := range int(declNode.NamedChildCount()) {
		child := declNode.NamedChild(i)
		if !cIsDeclarator(classes, child) {
			continue
		}
		if only != nil {
			// A second declarator: the node declares more than one thing.
			return ""
		}
		only = child
	}
	if only == nil {
		return ""
	}
	name := cDeclaredObjectName(classes, only)
	if name == nil {
		return ""
	}
	return name.Content(src)
}

// cIsDeclarator reports whether a direct child of a declaration is one of its
// declarators rather than part of its type.
func cIsDeclarator(classes symbolClasses, n *sitter.Node) bool {
	switch classes.class(n.Symbol()) {
	case cKindIdentifier, cKindInitDeclarator, cKindPointerDeclarator,
		cKindArrayDeclarator, cKindFunctionDeclarator:
		return true
	}
	return false
}

// cDeclaredObjectName digs the declared identifier out of a declarator, reading
// through the initializer, pointer and array wrappers, or returns nil.
//
// A function_declarator anywhere in the chain returns nil: that declaration is a
// prototype or a function-pointer variable, and neither is named here. A
// prototype shares its name with the definition it declares, and naming both
// would make every call to that function ambiguous.
func cDeclaredObjectName(classes symbolClasses, n *sitter.Node) *sitter.Node {
	switch classes.class(n.Symbol()) {
	case cKindIdentifier:
		return n
	case cKindFunctionDeclarator:
		return nil
	case cKindInitDeclarator, cKindPointerDeclarator, cKindArrayDeclarator:
		for i := range int(n.NamedChildCount()) {
			child := n.NamedChild(i)
			if !cIsDeclarator(classes, child) {
				continue
			}
			return cDeclaredObjectName(classes, child)
		}
	}
	return nil
}
