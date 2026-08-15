// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
)

// The import arms for the six languages whose import specifiers are DOT
// separated, registered beside the ECMAScript arm chunker_javascript_imports.go
// installs. Rust and PHP write their own separators and live in the sibling
// file.
//
// EVERY ARM KEEPS ctx.Imports BYTE-IDENTICAL to what the default dispatch
// appended before it was registered, because chunker.go turns each entry into
// an IMPORTS edge and DetectFrameworks matches on those same strings. What the
// arms ADD is ctx.ImportBindings — the per-name table the resolution ladder's
// import rungs read, and the only place an ALIAS survives at all: the default
// dispatch denies @alias by name, so a captured alias would otherwise be
// dropped between the query and the resolver.
func init() {
	importParsers[LangJava] = parseJavaImport
	importParsers[LangKotlin] = parseKotlinImport
	importParsers[LangScala] = parseScalaImport
	importParsers[LangSwift] = parseSwiftImport
	importParsers[LangCSharp] = parseCSharpImport
	importParsers[LangPython] = parsePythonImport
}

// namedChildrenOfType returns every named child of the given kind, in order.
func namedChildrenOfType(node *sitter.Node, kind string) []*sitter.Node {
	var out []*sitter.Node
	for i := range int(node.NamedChildCount()) {
		if c := node.NamedChild(i); c.Type() == kind {
			out = append(out, c)
		}
	}
	return out
}

// hasAnonymousChild reports whether node carries an anonymous child of the given
// token type — the way a modifier keyword with no field name is read, as the
// ECMAScript arm reads `type` on an import statement.
func hasAnonymousChild(node *sitter.Node, kind string) bool {
	for i := range int(node.ChildCount()) {
		if node.Child(i).Type() == kind {
			return true
		}
	}
	return false
}

// splitLastSegment splits a separator-joined path into everything before the
// last separator and the last segment itself. A path with no separator has no
// prefix, and the whole path is the segment.
func splitLastSegment(path, sep string) (prefix, last string) {
	i := strings.LastIndex(path, sep)
	if i < 0 {
		return "", path
	}
	return path[:i], path[i+len(sep):]
}

// parseJavaImport records `import java.util.List;`, `import static a.b.C.d;` and
// `import x.y.*;`.
//
// THE STATIC FORM IS NOT DISTINGUISHED HERE and does not need to be: in both
// shapes the LAST dotted segment is the name bound in this file and everything
// before it is the specifier. Which of the two the specifier names — a package
// or a type — is resolved downstream by trying both candidate paths, so no
// modifier keyword has to be read to tell them apart.
func parseJavaImport(node *sitter.Node, src []byte, ctx *ChunkContext) {
	ctx.Imports = append(ctx.Imports, node.Content(src))

	path := ""
	if ids := namedChildrenOfType(node, "scoped_identifier"); len(ids) > 0 {
		path = ids[0].Content(src)
	}
	if path == "" {
		return
	}
	// `import x.y.*` binds an unbounded set of names under NO local name, so no
	// (Imported, Local) pair can represent it and nothing is bound.
	if len(namedChildrenOfType(node, "asterisk")) > 0 {
		ctx.ImportBindings = append(ctx.ImportBindings, ImportBinding{
			Specifier: path, Kind: ImportWildcard,
		})
		return
	}
	prefix, last := splitLastSegment(path, ".")
	ctx.ImportBindings = append(ctx.ImportBindings, ImportBinding{
		Specifier: prefix, Imported: last, Local: last, Kind: ImportNamed,
	})
}

// parseKotlinImport records `import a.b.C` and `import a.b.D as E`.
//
// ctx.Imports takes the PATH rather than the whole header, which is what the
// previous path-only capture appended.
func parseKotlinImport(node *sitter.Node, src []byte, ctx *ChunkContext) {
	ids := namedChildrenOfType(node, "identifier")
	if len(ids) == 0 {
		return
	}
	path := ids[0].Content(src)
	ctx.Imports = append(ctx.Imports, path)

	if prefix, wildcard := strings.CutSuffix(path, ".*"); wildcard {
		ctx.ImportBindings = append(ctx.ImportBindings, ImportBinding{
			Specifier: prefix, Kind: ImportWildcard,
		})
		return
	}
	prefix, last := splitLastSegment(path, ".")
	local := last
	if aliases := namedChildrenOfType(node, "import_alias"); len(aliases) > 0 {
		if ti := namedChildrenOfType(aliases[0], "type_identifier"); len(ti) > 0 {
			local = ti[0].Content(src)
		}
	}
	ctx.ImportBindings = append(ctx.ImportBindings, ImportBinding{
		Specifier: prefix, Imported: last, Local: local, Kind: ImportNamed,
	})
}

// parseScalaImport records `import a.b.C`, `import a.{D => F}` and
// `import a.b._`.
//
// Scala's grammar keeps the path segments FLAT under the declaration rather
// than nesting them, so the specifier is the join of the leading identifier
// children and the trailing one is the imported name.
func parseScalaImport(node *sitter.Node, src []byte, ctx *ChunkContext) {
	ctx.Imports = append(ctx.Imports, node.Content(src))

	ids := namedChildrenOfType(node, "identifier")
	segments := make([]string, 0, len(ids))
	for _, id := range ids {
		segments = append(segments, id.Content(src))
	}

	// `import a.b._` folds every exported name in under no local name.
	if len(namedChildrenOfType(node, "namespace_wildcard")) > 0 {
		ctx.ImportBindings = append(ctx.ImportBindings, ImportBinding{
			Specifier: strings.Join(segments, "."), Kind: ImportWildcard,
		})
		return
	}

	// `import a.{D => F}` — the selector block holds the renamings, and the
	// leading identifiers are the whole specifier.
	if sel := namedChildrenOfType(node, "namespace_selectors"); len(sel) > 0 {
		spec := strings.Join(segments, ".")
		for _, ren := range namedChildrenOfType(sel[0], "arrow_renamed_identifier") {
			names := namedChildrenOfType(ren, "identifier")
			if len(names) < 2 {
				continue
			}
			ctx.ImportBindings = append(ctx.ImportBindings, ImportBinding{
				Specifier: spec,
				Imported:  names[0].Content(src),
				Local:     names[1].Content(src),
				Kind:      ImportNamed,
			})
		}
		for _, plain := range namedChildrenOfType(sel[0], "identifier") {
			name := plain.Content(src)
			ctx.ImportBindings = append(ctx.ImportBindings, ImportBinding{
				Specifier: spec, Imported: name, Local: name, Kind: ImportNamed,
			})
		}
		return
	}

	if len(segments) == 0 {
		return
	}
	last := segments[len(segments)-1]
	ctx.ImportBindings = append(ctx.ImportBindings, ImportBinding{
		Specifier: strings.Join(segments[:len(segments)-1], "."),
		Imported:  last, Local: last, Kind: ImportNamed,
	})
}

// parseSwiftImport records `import Foundation` and `import struct Ext.Helper`.
//
// A PLAIN swift import names a MODULE, not a member, so it binds no name and is
// recorded as a wildcard — the same shape as java's `import x.y.*`. Only the
// DECLARATION-import forms, which carry a kind keyword, name something.
func parseSwiftImport(node *sitter.Node, src []byte, ctx *ChunkContext) {
	ctx.Imports = append(ctx.Imports, node.Content(src))

	ids := namedChildrenOfType(node, "identifier")
	if len(ids) == 0 {
		return
	}
	path := ids[0].Content(src)

	declKinds := []string{"struct", "class", "func", "enum", "protocol", "typealias", "var", "let"}
	named := false
	for _, k := range declKinds {
		if hasAnonymousChild(node, k) {
			named = true
			break
		}
	}
	if !named {
		ctx.ImportBindings = append(ctx.ImportBindings, ImportBinding{
			Specifier: path, Kind: ImportWildcard,
		})
		return
	}
	prefix, last := splitLastSegment(path, ".")
	ctx.ImportBindings = append(ctx.ImportBindings, ImportBinding{
		Specifier: prefix, Imported: last, Local: last, Kind: ImportNamed,
	})
}

// parseCSharpImport records `using Foo.Bar;`, `using X = Foo.Baz;` and
// `using static A.B;`.
//
// A PLAIN using imports a NAMESPACE and a static using imports every member of
// a type; neither names one member, so neither binds a name. Only the ALIAS
// form does, and recording `Bar` from `using Foo.Bar;` would assert a member
// the source never named.
func parseCSharpImport(node *sitter.Node, src []byte, ctx *ChunkContext) {
	ctx.Imports = append(ctx.Imports, node.Content(src))

	quals := namedChildrenOfType(node, "qualified_name")
	if len(quals) == 0 {
		return
	}
	path := quals[0].Content(src)
	// The alias form is the one carrying a bare identifier child beside the
	// qualified name: `using X = Foo.Baz;`.
	locals := namedChildrenOfType(node, "identifier")
	if len(locals) == 0 {
		ctx.ImportBindings = append(ctx.ImportBindings, ImportBinding{
			Specifier: path, Kind: ImportWildcard,
		})
		return
	}
	prefix, last := splitLastSegment(path, ".")
	ctx.ImportBindings = append(ctx.ImportBindings, ImportBinding{
		Specifier: prefix, Imported: last,
		Local: locals[0].Content(src), Kind: ImportNamed,
	})
}

// parsePythonImport records `import os`, `import json as j`,
// `from x.y import a as b` and `from p import q`.
//
// ctx.Imports REPRODUCES BOTH ENTRIES the previous two-capture query produced —
// the whole statement, and additionally the bare module path for a from-import.
// The framework rules match on both spellings (`s == "pytest"` as well as
// `strings.Contains(s, "import pytest")`), so dropping the second would lose
// detection for `from pytest import fixture`.
func parsePythonImport(node *sitter.Node, src []byte, ctx *ChunkContext) {
	ctx.Imports = append(ctx.Imports, node.Content(src))

	if node.Type() == "import_from_statement" {
		parsePythonFromImport(node, src, ctx)
		return
	}
	// `import x` / `import x as y` — the MODULE itself is bound locally.
	for _, alias := range namedChildrenOfType(node, "aliased_import") {
		dotted := namedChildrenOfType(alias, "dotted_name")
		locals := namedChildrenOfType(alias, "identifier")
		if len(dotted) == 0 || len(locals) == 0 {
			continue
		}
		ctx.ImportBindings = append(ctx.ImportBindings, ImportBinding{
			Specifier: dotted[0].Content(src),
			Local:     locals[0].Content(src), Kind: ImportNamespace,
		})
	}
	for _, dotted := range namedChildrenOfType(node, "dotted_name") {
		path := dotted.Content(src)
		ctx.ImportBindings = append(ctx.ImportBindings, ImportBinding{
			Specifier: path, Local: path, Kind: ImportNamespace,
		})
	}
}

// parsePythonFromImport records the `from <module> import <names>` forms. The
// MODULE is the specifier; each imported name is its own binding.
func parsePythonFromImport(node *sitter.Node, src []byte, ctx *ChunkContext) {
	dotted := namedChildrenOfType(node, "dotted_name")
	if len(dotted) == 0 {
		return
	}
	module := dotted[0].Content(src)
	ctx.Imports = append(ctx.Imports, module)

	// `from x import *` binds an unbounded set under no local name.
	if hasAnonymousChild(node, "*") {
		ctx.ImportBindings = append(ctx.ImportBindings, ImportBinding{
			Specifier: module, Kind: ImportWildcard,
		})
		return
	}
	for _, alias := range namedChildrenOfType(node, "aliased_import") {
		names := namedChildrenOfType(alias, "dotted_name")
		locals := namedChildrenOfType(alias, "identifier")
		if len(names) == 0 || len(locals) == 0 {
			continue
		}
		ctx.ImportBindings = append(ctx.ImportBindings, ImportBinding{
			Specifier: module, Imported: names[0].Content(src),
			Local: locals[0].Content(src), Kind: ImportNamed,
		})
	}
	// The module's own dotted_name is the first; every later one is an
	// unaliased imported name.
	for _, name := range dotted[1:] {
		n := name.Content(src)
		ctx.ImportBindings = append(ctx.ImportBindings, ImportBinding{
			Specifier: module, Imported: n, Local: n, Kind: ImportNamed,
		})
	}
}
