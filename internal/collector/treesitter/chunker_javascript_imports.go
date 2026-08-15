// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
)

// The ECMAScript import arm, registered for the three languages that share the
// grammar shape. TypeScript and TSX share one query set (queries_typescript.go)
// and JavaScript has its own, but all three reference jsImportsQuery, so ONE
// arm serves them all.
//
// Registering per-language behavior in an init is the idiom this package
// already uses for testBlockClassifiers and frameworkExtenders. The arm gets
// its own file rather than joining chunker_javascript.go so that neither file
// approaches the 500-line cap, and so the per-language arms the dependent
// tickets add have an obvious file-per-language precedent to copy.
func init() {
	importParsers[LangTypeScript] = parseECMAScriptImport
	importParsers[LangTSX] = parseECMAScriptImport
	importParsers[LangJavaScript] = parseECMAScriptImport
}

// parseECMAScriptImport reads ONE captured import_statement or export_statement
// and records what it declares into ctx.
//
// THE ONE-EDGE-PER-STATEMENT RULE governs the ctx.Imports side: a statement
// contributes exactly one entry there when it declares a dependency, however
// many names its clause carries, because chunker.go turns every ctx.Imports
// entry into an IMPORTS edge. The binding table is where the per-name detail
// goes and it never becomes edges.
func parseECMAScriptImport(node *sitter.Node, src []byte, ctx *ChunkContext) {
	switch node.Type() {
	case "import_statement":
		parseECMAScriptImportStatement(node, src, ctx)
	case "export_statement":
		parseECMAScriptExportStatement(node, src, ctx)
	}
}

// importSpecifierText returns a statement's `from '<spec>'` target with quote
// characters stripped, or "" when the statement names no source.
//
// It uses the same strings.Trim call the default dispatch arm uses, so one
// spelling of the strip exists for every language.
func importSpecifierText(node *sitter.Node, src []byte) string {
	source := node.ChildByFieldName("source")
	if source == nil {
		return ""
	}
	return strings.Trim(source.Content(src), "\"'`")
}

// parseECMAScriptImportStatement handles every import form:
//
//	import './side'            -> one Imports entry; ImportSideEffect, Local ""
//	import A from './x'        -> one Imports entry; ImportDefault, Local "A"
//	import * as ns from './x'  -> one Imports entry; ImportNamespace, Local "ns"
//	import {A} from './x'      -> one Imports entry; ImportNamed A/A
//	import {A as B} from './x' -> one Imports entry; ImportNamed, Local B, Imported A
//	import type {A} from './x' -> as ImportNamed with TypeOnly true
//	import E, {F as G} from    -> ONE Imports entry, TWO ImportBindings
//
// A side-effect import binds no name, so a consumer that binds names skips it;
// it is recorded anyway to keep ImportBindings a faithful record of the
// statement list rather than a filtered one.
func parseECMAScriptImportStatement(node *sitter.Node, src []byte, ctx *ChunkContext) {
	spec := importSpecifierText(node, src)
	if spec == "" {
		return
	}
	ctx.Imports = append(ctx.Imports, spec)

	// A `type` modifier on the STATEMENT is an anonymous child, so it is read
	// by node type rather than by field: `import type {A} from './x'`.
	typeOnly := false
	for i := range int(node.ChildCount()) {
		if node.Child(i).Type() == "type" {
			typeOnly = true
			break
		}
	}

	clause := namedChildOfType(node, "import_clause")
	if clause == nil {
		// No clause at all: the module is loaded for its side effects.
		ctx.ImportBindings = append(ctx.ImportBindings, ImportBinding{
			Specifier: spec,
			Kind:      ImportSideEffect,
			TypeOnly:  typeOnly,
		})
		return
	}

	for i := range int(clause.NamedChildCount()) {
		child := clause.NamedChild(i)
		switch child.Type() {
		case "identifier":
			// The default clause: `import A from './x'`.
			ctx.ImportBindings = append(ctx.ImportBindings, ImportBinding{
				Specifier: spec,
				Local:     child.Content(src),
				Kind:      ImportDefault,
				TypeOnly:  typeOnly,
			})
		case "namespace_import":
			// `import * as ns from './x'` binds the MODULE ITSELF.
			if id := namedChildOfType(child, "identifier"); id != nil {
				ctx.ImportBindings = append(ctx.ImportBindings, ImportBinding{
					Specifier: spec,
					Local:     id.Content(src),
					Kind:      ImportNamespace,
					TypeOnly:  typeOnly,
				})
			}
		case "named_imports":
			for j := range int(child.NamedChildCount()) {
				spec2 := child.NamedChild(j)
				if spec2.Type() != "import_specifier" {
					continue
				}
				imported, local := specifierNames(spec2, src)
				if imported == "" {
					continue
				}
				ctx.ImportBindings = append(ctx.ImportBindings, ImportBinding{
					Specifier: spec,
					Local:     local,
					Imported:  imported,
					Kind:      ImportNamed,
					TypeOnly:  typeOnly || specifierIsTypeOnly(spec2),
				})
			}
		}
	}
}

// parseECMAScriptExportStatement handles the export forms, whose whole
// asymmetry is the presence of a source:
//
//	export {A as B} from './z' -> one Imports entry; ReExport B/A
//	export * from './w'        -> one Imports entry; ReExport with empty names
//	export * as ns from './q'  -> one Imports entry; ReExport with empty names
//	export {X}      (no source)-> NOTHING: it declares no dependency
//	export default function App() {} -> DefaultExportName "App"
//	export default someIdent;        -> DefaultExportName "someIdent"
//
// A re-export specifier IS a real dependency and earns its IMPORTS edge; a
// sourceless export names no other module, so appending for it would be exactly
// the bogus edge the dispatch's deny entry exists to prevent.
func parseECMAScriptExportStatement(node *sitter.Node, src []byte, ctx *ChunkContext) {
	spec := importSpecifierText(node, src)
	if spec == "" {
		recordDefaultExportName(node, src, ctx)
		return
	}
	ctx.Imports = append(ctx.Imports, spec)

	clause := namedChildOfType(node, "export_clause")
	if clause == nil {
		// `export * from './w'` and `export * as ns from './q'` forward an
		// unnamed set, so both names stay empty.
		ctx.ReExports = append(ctx.ReExports, ReExport{Specifier: spec})
		return
	}
	for i := range int(clause.NamedChildCount()) {
		child := clause.NamedChild(i)
		if child.Type() != "export_specifier" {
			continue
		}
		imported, local := specifierNames(child, src)
		if imported == "" {
			continue
		}
		ctx.ReExports = append(ctx.ReExports, ReExport{
			Specifier: spec,
			Local:     local,
			Imported:  imported,
		})
	}
}

// recordDefaultExportName fills ctx.DefaultExportName from a sourceless
// `export default` statement, and leaves it alone for every other sourceless
// export.
//
// The `default` keyword is anonymous, so its presence is read by node type. An
// anonymous default — `export default function () {}` — names nothing and is
// deliberately left unrecorded rather than given a placeholder.
func recordDefaultExportName(node *sitter.Node, src []byte, ctx *ChunkContext) {
	isDefault := false
	for i := range int(node.ChildCount()) {
		if node.Child(i).Type() == "default" {
			isDefault = true
			break
		}
	}
	if !isDefault {
		return
	}
	for i := range int(node.NamedChildCount()) {
		child := node.NamedChild(i)
		switch child.Type() {
		case "identifier":
			// `export default someIdent;`
			ctx.DefaultExportName = child.Content(src)
			return
		case "function_declaration", "class_declaration", "generator_function_declaration":
			// `export default function App() {}`
			if name := child.ChildByFieldName("name"); name != nil {
				ctx.DefaultExportName = name.Content(src)
			}
			return
		}
	}
}

// specifierNames reads an import_specifier or export_specifier as the
// (imported, local) pair the shared carrier stores.
//
// IT READS NAMED CHILDREN RATHER THAN FIELD NAMES. An import_specifier's named
// children are EXACTLY [identifier] or [identifier, identifier] — the `as`
// keyword and an inline `type` modifier are both ANONYMOUS — so named-child
// indexing is stable across {A}, {A as B} and {type A as B}. Reading child 0 as
// the LOCAL name is the single most likely way to get this wrong: child 0 is
// the name in the SOURCE module and child 1, when present, is the local one.
func specifierNames(node *sitter.Node, src []byte) (imported, local string) {
	var ids []string
	for i := range int(node.NamedChildCount()) {
		child := node.NamedChild(i)
		if child.Type() == "identifier" {
			ids = append(ids, child.Content(src))
		}
	}
	switch len(ids) {
	case 0:
		return "", ""
	case 1:
		// `{A}` binds A under its own name.
		return ids[0], ids[0]
	default:
		// `{A as B}`: A in the source module, B here.
		return ids[0], ids[1]
	}
}

// specifierIsTypeOnly reports whether one specifier carries the INLINE type
// modifier — `import {type A} from './x'` — which is anonymous like the
// statement-level one.
func specifierIsTypeOnly(node *sitter.Node) bool {
	for i := range int(node.ChildCount()) {
		if node.Child(i).Type() == "type" {
			return true
		}
	}
	return false
}

// namedChildOfType returns the first named child of the given type, or nil.
func namedChildOfType(node *sitter.Node, kind string) *sitter.Node {
	for i := range int(node.NamedChildCount()) {
		if child := node.NamedChild(i); child.Type() == kind {
			return child
		}
	}
	return nil
}
