// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
)

// extractFileContext pulls imports and the symbol namespace from the AST root.
// The namespace defaults to the file's derived namespace for every language,
// and is overwritten by the Go package clause or by a PHP/C# sibling-form
// namespace declaration when the file carries one.
func (c *Chunker) extractFileContext(root *sitter.Node, src []byte, filePath string, lang Language, cqs *compiledQuerySet) ChunkContext {
	ctx := ChunkContext{}
	ctx.PackageName = fileNamespace(filePath, lang)

	// Extract imports.
	if cqs.imports != nil {
		qc := sitter.NewQueryCursor()
		defer qc.Close()
		qc.Exec(cqs.imports, root)
		for {
			m, ok := qc.NextMatch()
			if !ok {
				break
			}
			m = filterPredicates(cqs.imports, m, src)
			for _, cap := range m.Captures {
				importPath := cap.Node.Content(src)
				// Strip quotes from Go import paths.
				importPath = strings.Trim(importPath, "\"'`")
				ctx.Imports = append(ctx.Imports, importPath)
			}
		}
	}

	// Extract the Go package name, which overwrites the derived default.
	for i := range int(root.ChildCount()) {
		child := root.Child(i)
		if child.Type() == "package_clause" {
			for j := range int(child.ChildCount()) {
				gc := child.Child(j)
				if gc.Type() == "package_identifier" {
					ctx.PackageName = gc.Content(src)
					break
				}
			}
			break
		}
	}

	// A PHP or C# file that declares its namespace in the SIBLING form names
	// its own symbol namespace better than its parent directory does: two
	// files of one PSR-4 namespace routinely live in different directories,
	// and resolution keyed on the directory cannot see they are the same
	// namespace. Only the sibling forms are read here — the braced and block
	// forms are true ancestors and the enclosing-scope ascent already handles
	// them, so reading them here as well would qualify the same declaration
	// twice.
	if declared := declaredFileNamespace(root, src, lang); declared != "" {
		ctx.PackageName = declared
	}

	return ctx
}

// declaredFileNamespace returns the namespace a PHP or C# file declares in the
// sibling form, already carrying the language prefix and separator sanitisation
// fileNamespace applies, or "" when the file is any other shape.
//
// The forms are distinguished STRUCTURALLY rather than by counting semicolons.
// PHP's semicolon form is a top-level namespace_definition with a name and NO
// body; the braced form has a body, and the unnamed global `namespace { }` has
// a body and no name. C#'s file-scoped form is its own node kind and has no
// body by construction. Anything else — no namespace, several namespaces, or a
// braced/block form — leaves the derived default alone.
func declaredFileNamespace(root *sitter.Node, src []byte, lang Language) string {
	var kind string
	switch lang {
	case LangPHP:
		kind = "namespace_definition"
	case LangCSharp:
		kind = "file_scoped_namespace_declaration"
	default:
		return ""
	}

	var declared string
	seen := 0
	for i := range int(root.NamedChildCount()) {
		child := root.NamedChild(i)
		if child.Type() != kind {
			continue
		}
		seen++
		if seen > 1 {
			// Several namespaces in one file: no single one names the file.
			return ""
		}
		name := child.ChildByFieldName("name")
		if name == nil || child.ChildByFieldName("body") != nil {
			continue
		}
		declared = name.Content(src)
	}
	if declared == "" {
		return ""
	}
	// Built the same way fileNamespace builds a directory-derived namespace, so
	// the language partition and the separator sanitisation are defined in one
	// place. The sanitiser is load-bearing on this input: edge resolution reads
	// everything before the FIRST '.' as the namespace token, so a C#
	// "App.Models" would otherwise be split in half.
	return string(lang) + ":" + namespaceSanitizer.Replace(declared)
}
