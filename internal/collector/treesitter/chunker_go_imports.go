// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
)

// The Go import arm. Registering per-language behavior in an init is the idiom
// this package already uses for importParsers (chunker_javascript_imports.go),
// testBlockClassifiers and frameworkExtenders, and the arm gets its own file so
// neither it nor chunker_go.go approaches the 500-line cap.
//
// Go NEEDS an arm rather than a richer query: ctx.ImportBindings is populated
// only for a language with a registered arm, so without one an import's local
// name — the alias, the dot, the blank — would be captured and then discarded,
// and the Go BindsResolver would have no alias data to map onto a scope.
func init() {
	importParsers[LangGo] = parseGoImport
}

// parseGoImport reads ONE captured import_spec and records what it declares
// into ctx.
//
// Both halves matter and only one of them is compile-checked. The
// ctx.ImportBindings append is this arm's reason to exist; the ctx.Imports
// append is what keeps Go's IMPORTS edges and framework detection alive, since
// an arm OWNS every capture for its language and chunker.go turns every
// ctx.Imports entry into an IMPORTS edge. Dropping the first append loses
// binding data; dropping the second emits zero IMPORTS edges for every Go file
// in the repo, with no compile error, because the field is simply never
// written. TestGoImportEdgesUnchanged is the named catcher for the second.
//
// A Go import statement binds THE PACKAGE under one local name and names no
// member of it, so every form is ImportNamespace except the blank import, which
// binds nothing and is ImportSideEffect. Imported stays empty for the same
// reason: it holds the name taken FROM the module, and no Go import names one.
//
//	import "x/y"       -> Specifier "x/y", Local "",     ImportNamespace
//	import al "x/y"    -> Specifier "x/y", Local "al",   ImportNamespace
//	import . "x/y"     -> Specifier "x/y", Local ".",    ImportNamespace
//	import _ "x/y"     -> Specifier "x/y", Local "_",    ImportSideEffect
//
// THE DOT AND BLANK FORMS ARE RECORDED, NOT SKIPPED, and their Local is carried
// VERBATIM per the rule written onto the field at chunker_imports.go: the Go
// BindsResolver reads "." to derive a dot scope and "_" to bind nothing, so
// normalising either into "" or into a derived package name would make both
// rules unimplementable. ImportWildcard is deliberately not used — it is
// defined as binding an unbounded set under NO local name, and Go's dot import
// has a local name of "." that the arm and the resolution walk both key on.
func parseGoImport(node *sitter.Node, src []byte, ctx *ChunkContext) {
	path := node.ChildByFieldName("path")
	if path == nil {
		return
	}
	// The path field's content INCLUDES its quote characters. Stripped with the
	// same call the default dispatch arm uses (chunker_filecontext.go), so a Go
	// specifier is byte-identical to what ctx.Imports carried before this arm.
	spec := strings.Trim(path.Content(src), "\"'`")

	ctx.Imports = append(ctx.Imports, spec)

	local := ""
	if name := node.ChildByFieldName("name"); name != nil {
		local = name.Content(src)
	}
	kind := ImportNamespace
	if local == "_" {
		kind = ImportSideEffect
	}
	ctx.ImportBindings = append(ctx.ImportBindings, ImportBinding{
		Specifier: spec,
		Local:     local,
		Kind:      kind,
	})
}
