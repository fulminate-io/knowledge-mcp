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
//
// For a language with a registered importParsers arm it also pulls that arm's
// richer import facts — ImportBindings, ReExports and DefaultExportName — which
// stay empty for every other language.
func (c *Chunker) extractFileContext(root *sitter.Node, src []byte, filePath string, lang Language, cqs *compiledQuerySet) ChunkContext {
	ctx := ChunkContext{}
	ctx.PackageName = fileNamespace(filePath, lang)

	// Extract imports. The dispatch below is ARM-FIRST WITH A DENY ENTRY, and
	// deliberately NOT a capture-name whitelist — see the three numbered cases.
	if cqs.imports != nil {
		qc := sitter.NewQueryCursor()
		defer qc.Close()
		qc.Exec(cqs.imports, root)
		arm := importParsers[lang]
		for {
			m, ok := qc.NextMatch()
			if !ok {
				break
			}
			m = filterPredicates(cqs.imports, m, src)
			for _, cap := range m.Captures {
				// 1. A REGISTERED ARM OWNS EVERY CAPTURE FOR ITS LANGUAGE.
				// Keying on the arm BEFORE the capture name is what makes this
				// safe: @import is already the only Imports capture of eight
				// languages this ticket does not touch (csharp, groovy, java,
				// php, rust, scala, swift, and python which binds both names),
				// so a name-first dispatch would claim captures belonging to
				// them. An arm decides for itself what becomes a ctx.Imports
				// entry, and it is invoked once per capture.
				if arm != nil {
					arm(cap.Node, src, &ctx)
					continue
				}
				// 2. DENY @alias. A capture so named identifies a local
				// BINDING, not a dependency, and contributes no ctx.Imports
				// entry. It is skipped BY NAME so that a language which adds an
				// @alias capture before it has a registered arm cannot silently
				// emit the alias text as an import path — chunker.go turns
				// every ctx.Imports entry into an IMPORTS edge, and that edge
				// would be bogus. Any future capture naming a binding rather
				// than a dependency must be spelled @alias for this reason.
				if cqs.imports.CaptureNameForId(cap.Index) == "alias" {
					continue
				}
				// 3. DEFAULT — EVERY OTHER CAPTURE, WHATEVER ITS NAME. Byte
				// for byte what this loop did for every language before the
				// dispatch existed, which is why it stays name-blind: a @path
				// whitelist here would empty ctx.Imports for the eight @import
				// languages, killing their framework detection and every
				// IMPORTS edge they emit.
				importPath := cap.Node.Content(src)
				// Strip quotes from Go import paths.
				importPath = strings.Trim(importPath, "\"'`")
				ctx.Imports = append(ctx.Imports, ImportSite{Specifier: importPath})
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

// jvmPackageClause names the node kinds ONE JVM-family language's package clause
// is read off. THE KINDS WERE COMPILED, NOT ASSUMED — each was read from a
// tree-sitter parse of that language's own package clause rather than guessed
// from the grammar's documentation:
//
//	java    `package com.acme.foo;` -> package_declaration > scoped_identifier
//	        `package foo;`          -> package_declaration > identifier
//	                                   (a SINGLE segment is not scoped, which is
//	                                   why java lists two path kinds)
//	kotlin  `package com.acme.foo`  -> package_header > identifier
//	                                   (no package_header node at all when the
//	                                   file declares none)
//	scala   `package com.acme.foo`  -> package_clause > package_identifier
//	        `package a { ... }`      -> package_clause > package_identifier +
//	                                   template_body, the BRACED form
//
// paths is ordered and the FIRST kind present wins. It is a kind list rather
// than "the first named child" because java permits an annotation on the clause,
// which would take that slot.
type jvmPackageClause struct {
	decl  string
	paths []string
	// body is the named child kind whose presence means the BRACED form, or ""
	// when the language has no braced form on this node. The braced form is a
	// true ancestor and the enclosing-scope ascent already handles it, so
	// reading it here as well would qualify the same declaration twice.
	body string
}

var jvmPackageClauses = map[Language]jvmPackageClause{
	LangJava:   {decl: "package_declaration", paths: []string{"scoped_identifier", "identifier"}},
	LangKotlin: {decl: "package_header", paths: []string{"identifier"}},
	LangScala:  {decl: "package_clause", paths: []string{"package_identifier"}, body: "template_body"},
}

// declaredFileNamespace returns the namespace a file declares at file level,
// already carrying the language prefix and separator sanitisation
// fileNamespace applies, or "" when the file is any other shape.
//
// FIVE LANGUAGES ARE READ HERE and they fall into two families. PHP and C#
// declare a NAMESPACE in a sibling form; java, kotlin and scala declare a
// PACKAGE, which is the same thing under another spelling — a unit two files in
// different directories can share — and that is why all five take
// ScopeDeclaredNamespace in scopeKinds. A language absent from both families
// leaves the derived default alone.
//
// The forms are distinguished STRUCTURALLY rather than by counting semicolons.
// PHP's semicolon form is a top-level namespace_definition with a name and NO
// body; the braced form has a body, and the unnamed global `namespace { }` has
// a body and no name. C#'s file-scoped form is its own node kind and has no
// body by construction. Anything else — no namespace, several namespaces, or a
// braced/block form — leaves the derived default alone.
func declaredFileNamespace(root *sitter.Node, src []byte, lang Language) string {
	if clause, ok := jvmPackageClauses[lang]; ok {
		return declaredPackageClause(root, src, lang, clause)
	}

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
	// Built through the ONE token builder every producer of a namespace scope
	// shares, so the language partition and the separator sanitisation are
	// defined in one place and a token assembled here can never disagree with
	// the one an import arm or the fully-qualified rung builds.
	return NamespaceToken(lang, declared)
}

// declaredPackageClause is declaredFileNamespace's JVM-family arm: java, kotlin
// and scala declare a package rather than a namespace, and their clauses carry
// no `name` field for the sibling arm's ChildByFieldName to read.
//
// SEVERAL CLAUSES IN ONE FILE DECLARE NOTHING, the same rule the sibling arm
// applies: scala legally writes `package a` then `package b` to mean a.b, and
// no single clause names the file. Declining leaves the file on its
// directory-derived scope, which can only widen the residue — it can never
// mis-bind, per the rule stated on scopeKinds.
func declaredPackageClause(root *sitter.Node, src []byte, lang Language, clause jvmPackageClause) string {
	decls := namedChildrenOfType(root, clause.decl)
	if len(decls) != 1 {
		return ""
	}
	decl := decls[0]
	if clause.body != "" && len(namedChildrenOfType(decl, clause.body)) > 0 {
		return ""
	}
	for _, kind := range clause.paths {
		if paths := namedChildrenOfType(decl, kind); len(paths) > 0 {
			return NamespaceToken(lang, paths[0].Content(src))
		}
	}
	return ""
}
