// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	sitter "github.com/smacker/go-tree-sitter"
)

// The import arms for the two languages whose import specifiers are NOT dot
// separated: rust writes `::` and php writes `\`. They keep ctx.Imports
// byte-identical to the default dispatch and add the per-name binding table,
// exactly as the dotted arms beside them do.
func init() {
	importParsers[LangRust] = parseRustImport
	importParsers[LangPHP] = parsePHPImport
}

// parseRustImport records `use x::y as z;`, `use a::b::c;`, `use a::{b, d as e};`
// and `use q::*;`.
//
// The list form produces SEVERAL bindings from ONE statement, which is what
// ImportBindings is for; ctx.Imports still takes exactly one entry, because
// each of its entries becomes an IMPORTS edge.
func parseRustImport(node *sitter.Node, src []byte, ctx *ChunkContext) {
	ctx.Imports = append(ctx.Imports, node.Content(src))

	for i := range int(node.NamedChildCount()) {
		child := node.NamedChild(i)
		switch child.Type() {
		case "use_as_clause":
			appendRustAliasBinding(child, src, "", ctx)
		case "scoped_identifier":
			prefix, last := splitLastSegment(child.Content(src), "::")
			ctx.ImportBindings = append(ctx.ImportBindings, ImportBinding{
				Specifier: prefix, Imported: last, Local: last, Kind: ImportNamed,
			})
		case "identifier":
			name := child.Content(src)
			ctx.ImportBindings = append(ctx.ImportBindings, ImportBinding{
				Imported: name, Local: name, Kind: ImportNamed,
			})
		case "use_wildcard":
			prefix, _ := splitLastSegment(child.Content(src), "::")
			ctx.ImportBindings = append(ctx.ImportBindings, ImportBinding{
				Specifier: prefix, Kind: ImportWildcard,
			})
		case "scoped_use_list":
			parseRustScopedUseList(child, src, ctx)
		}
	}
}

// parseRustScopedUseList records `use a::{b, d as e};` — the prefix is the
// list's own scope and every member of the list binds against it.
func parseRustScopedUseList(node *sitter.Node, src []byte, ctx *ChunkContext) {
	prefix := ""
	if ids := namedChildrenOfType(node, "identifier"); len(ids) > 0 {
		prefix = ids[0].Content(src)
	}
	if paths := namedChildrenOfType(node, "scoped_identifier"); len(paths) > 0 {
		prefix = paths[0].Content(src)
	}
	lists := namedChildrenOfType(node, "use_list")
	if len(lists) == 0 {
		return
	}
	for i := range int(lists[0].NamedChildCount()) {
		member := lists[0].NamedChild(i)
		switch member.Type() {
		case "use_as_clause":
			appendRustAliasBinding(member, src, prefix, ctx)
		case "identifier":
			name := member.Content(src)
			ctx.ImportBindings = append(ctx.ImportBindings, ImportBinding{
				Specifier: prefix, Imported: name, Local: name, Kind: ImportNamed,
			})
		case "scoped_identifier":
			inner, last := splitLastSegment(member.Content(src), "::")
			spec := prefix
			if inner != "" {
				spec = prefix + "::" + inner
			}
			ctx.ImportBindings = append(ctx.ImportBindings, ImportBinding{
				Specifier: spec, Imported: last, Local: last, Kind: ImportNamed,
			})
		}
	}
}

// appendRustAliasBinding records one `<path> as <local>` clause. scopePrefix is
// the enclosing list's scope, empty for a top-level `use x::y as z;` whose own
// path already carries it.
//
// THE IMPORTED NAME IS THE PATH'S LAST SEGMENT AND THE LOCAL IS THE ALIAS, and
// keeping them distinct is the whole point: the reference writes `z` while the
// target declares `y`, so a bind that recorded only the alias would send the
// resolver looking for a declaration named `z` that does not exist.
func appendRustAliasBinding(clause *sitter.Node, src []byte, scopePrefix string, ctx *ChunkContext) {
	var pathNode, aliasNode *sitter.Node
	for i := range int(clause.NamedChildCount()) {
		c := clause.NamedChild(i)
		switch c.Type() {
		case "scoped_identifier":
			pathNode = c
		case "identifier":
			if pathNode == nil && aliasNode == nil {
				pathNode = c
				continue
			}
			aliasNode = c
		}
	}
	if pathNode == nil || aliasNode == nil {
		return
	}
	prefix, last := splitLastSegment(pathNode.Content(src), "::")
	switch {
	case prefix == "":
		prefix = scopePrefix
	case scopePrefix != "":
		prefix = scopePrefix + "::" + prefix
	}
	ctx.ImportBindings = append(ctx.ImportBindings, ImportBinding{
		Specifier: prefix, Imported: last,
		Local: aliasNode.Content(src), Kind: ImportNamed,
	})
}

// parsePHPImport records `use Foo\Bar;` and `use Foo\Baz as Qux;`.
//
// The qualified_name holds the namespace prefix and the imported name; the
// optional namespace_aliasing_clause renames it locally.
func parsePHPImport(node *sitter.Node, src []byte, ctx *ChunkContext) {
	ctx.Imports = append(ctx.Imports, node.Content(src))

	for _, clause := range namedChildrenOfType(node, "namespace_use_clause") {
		quals := namedChildrenOfType(clause, "qualified_name")
		if len(quals) == 0 {
			continue
		}
		prefix, last := splitLastSegment(quals[0].Content(src), `\`)
		local := last
		if aliases := namedChildrenOfType(clause, "namespace_aliasing_clause"); len(aliases) > 0 {
			if names := namedChildrenOfType(aliases[0], "name"); len(names) > 0 {
				local = names[0].Content(src)
			}
		}
		ctx.ImportBindings = append(ctx.ImportBindings, ImportBinding{
			Specifier: prefix, Imported: last, Local: local, Kind: ImportNamed,
		})
	}
}
