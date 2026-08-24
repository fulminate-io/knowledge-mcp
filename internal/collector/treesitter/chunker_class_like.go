// SPDX-License-Identifier: Apache-2.0

package treesitter

// classLikeByLang holds, per language, the AST node types that represent a
// named container whose members another query pattern chunks separately — a
// class, interface, trait, protocol, module or namespace. Membership is a
// census of the 32 queries_*.go TopLevel queries: a kind belongs here when
// that language's query chunks a member declaration nested inside it and
// containerName can resolve the container's own name from one of its three
// sources.
//
// NO GO NODE KIND APPEARS HERE. Go's containers are type_declaration,
// type_spec, struct_type and interface_type; their absence is what makes Go's
// behavior unchanged by construction rather than by measurement. Note that
// method_definition and method_declaration are function-like, not class-like,
// and stay in functionLikeTypes.
//
// ADMISSION IS PER (LANGUAGE, KIND), AND THAT DIMENSION IS THE POINT. A kind
// admitted for one language is admitted for THAT LANGUAGE ONLY. Tree-sitter
// grammars reuse kind spellings freely, so a table keyed on the bare spelling
// made every admission global: `module` is Ruby's module declaration and also
// the kind a TypeScript `module Sink { ... }` block parses as, and admitting
// it for Ruby parented a TypeScript module's functions as though the block
// were a class — a TypeScript module block is a namespace rather than a
// class-like container, and its members belong to the file. Adding a kind to
// one language's row now says nothing about any other language's, so the
// grammar-wide census a bare-spelling table demanded before each addition is
// replaced by a per-language row plus the structural census tests that check
// every row against the grammar that owns it.
//
// A MISSING KEY AND AN EMPTY ROW BEHAVE IDENTICALLY, BY GO'S OWN RULES. A read
// of a missing language key yields a nil map, and indexing a nil map yields
// false without panicking, so a language with an empty row or no row at all
// never takes the class-like branch. That reproduces today's behavior for Go
// and Elixir exactly rather than by accident. Every registered language
// carries an explicit row regardless, and TestClassLikeByLangCoversEveryLanguage
// fails the build if one is ever missing — an absent row is a question nobody
// answered, while an empty row is a recorded answer.
//
// Containment is SINGLE-ANCESTOR: a member takes the name of its nearest named
// container, never a dotted chain of every container above it. A C++ member of
// `namespace outer { namespace inner { ... } }` therefore carries "inner"
// alone. Where a grammar itself hands over a qualified spelling as one name
// node — C#'s `namespace App.Models`, C++17's `namespace a::b` — that full
// path is kept, because it arrives as the container's own name.
//
// Deliberately excluded, with the grammar reason: Elixir's container and
// member are both the call kind, so no kind-based rule can tell defmodule from
// def; C#'s file_scoped_namespace_declaration and PHP's semicolon-form
// namespace are SIBLINGS of the declarations they name rather than ancestors,
// so no upward walk reaches them and they are resolved from the file's own
// declaration instead.
//
// EVERY GRADE BELOW WAS MEASURED, by running the real Chunker over a minimal
// fixture per pair and reading the member's ParentName back. A grade reading
// "no member chunked by the fixture" means exactly that — the FIXTURE, not
// "dead". A shape the probe did not write may well chunk a member; that is
// precisely how five pairs recorded as inert turned out to be live once the
// query sets that chunk their members landed.
var classLikeByLang = map[Language]map[string]bool{
	// Python's `module` kind is the FILE ROOT node and therefore sits on the
	// ancestor chain of every top-level declaration. It is NOT admitted: it
	// resolved to no name only because containerName's third source scans
	// direct named children for a type_identifier or a simple_identifier and
	// the python grammar declares neither, so admitting it bought nothing and
	// staked the file root's containment on a grammar accident.
	LangPython: {
		"class_definition": true, // member takes the class
	},
	LangScala: {
		"class_definition":  true, // member takes the class
		"object_definition": true, // member takes the object
		// A trait's method declarations chunk, so its members take the trait.
		"trait_definition": true,
	},
	LangGroovy: {
		"class_definition": true, // member takes the class
	},
	LangOCaml: {
		// module_definition binds no fields, so the ascent admits the inner
		// module_binding, which carries the name.
		"module_binding": true, // member takes the module
		// No member chunked by the fixture — the grade is the fixture's, not a
		// claim that an OCaml class body can never chunk one.
		"class_definition": true,
	},
	LangTypeScript: {
		"class_declaration": true, // member takes the class
		// An abstract class is a named declaration of its own kind; its
		// members take it exactly as a concrete class's do.
		"abstract_class_declaration": true,
		// The kind of a class EXPRESSION. A member of a named class
		// expression takes the class's name; a member of an anonymous one
		// takes nothing, because containerName resolves no name and the
		// ascent continues.
		"class": true,
		// An interface's method_signature members chunk, so they take the
		// interface.
		"interface_declaration": true,
		// No member chunked by the fixture.
		"enum_declaration": true,
	},
	// tsx rides the SEPARATE typescript/tsx grammar but shares tsQueries, so
	// its row is TypeScript's row. The two are spelled out rather than
	// aliased: a shared map value would let an edit to one language's
	// admissions silently move the other's.
	LangTSX: {
		"class_declaration":          true, // member takes the class
		"abstract_class_declaration": true,
		"class":                      true, // class expression
		"interface_declaration":      true,
		"enum_declaration":           true, // no member chunked by the fixture
	},
	LangJavaScript: {
		"class_declaration": true, // member takes the class
		"class":             true, // class expression
	},
	LangJava: {
		"class_declaration":     true, // member takes the class
		"interface_declaration": true, // member takes the interface
		"enum_declaration":      true, // member takes the enum
	},
	LangCSharp: {
		"class_declaration":     true, // member takes the class
		"interface_declaration": true, // member takes the interface
		"struct_declaration":    true, // member takes the struct
		// Block form only. The file-scoped form is a SIBLING of the
		// declarations it names, so no upward walk reaches it.
		"namespace_declaration": true,
		// No member chunked by the fixture.
		"enum_declaration": true,
	},
	LangPHP: {
		"class_declaration":     true, // member takes the class
		"interface_declaration": true, // member takes the interface
		"trait_declaration":     true, // member takes the trait
		"enum_declaration":      true, // member takes the enum
		// Braced form only, for the same sibling reason as C#'s file-scoped
		// form.
		"namespace_definition": true,
	},
	LangSwift: {
		"class_declaration": true, // member takes the class
		// A protocol's function declarations chunk, so they take the protocol.
		"protocol_declaration": true,
	},
	// Kotlin's class_declaration and object_declaration bind their name
	// POSITIONALLY — ChildByFieldName("name") returns nil on both, so their
	// names come from containerName's third source, the scan of direct named
	// children.
	LangKotlin: {
		"class_declaration":  true, // member takes the class
		"object_declaration": true, // member takes the object
	},
	LangRuby: {
		"class": true, // member takes the class
		// THE ONE CORRECT `module` ADMISSION. Ruby's module is a real named
		// container of its methods; the identical spelling in the typescript,
		// tsx, python and elm grammars names something else entirely, which is
		// why this admission is scoped to Ruby rather than to the spelling.
		"module": true,
	},
	LangCPP: {
		"class_specifier":      true, // member takes the class
		"struct_specifier":     true, // member takes the struct
		"namespace_definition": true, // member takes the namespace
	},
	LangC: {
		// A struct's function-pointer fields chunk as field declarations, so
		// they take the struct.
		"struct_specifier": true,
	},
	LangRust: {
		"mod_item": true, // member takes the module
		// impl_item binds type: and no name: — see containerName's second
		// source, which accepts it only when it binds a type_identifier.
		"impl_item": true,
		// A trait's method specs, default methods and associated types take
		// the trait, which is what declared-conformance member pairing and a
		// call through a trait-typed value both key on.
		"trait_item": true,
	},

	// EXPLICIT EMPTY ROWS. Each records a measured or structural answer, not
	// an omission.
	//
	// Go's containers are type_declaration, type_spec, struct_type and
	// interface_type, and none participates in the ascent — Go's behavior is
	// unchanged BY CONSTRUCTION.
	LangGo: {},
	// Elixir's container and its members are both the `call` kind, so no
	// kind-based rule can tell defmodule from def.
	LangElixir: {},
	// Elm's `module` is a keyword LEAF inside module_declaration, never an
	// ancestor of a declaration.
	LangElm: {},
	// No container construct whose members the language's query chunks
	// separately.
	LangLua:        {},
	LangBash:       {},
	LangHCL:        {},
	LangProtobuf:   {},
	LangCSS:        {},
	LangHTML:       {},
	LangSQL:        {},
	LangDockerfile: {},
	LangSvelte:     {},
	LangToml:       {},
	LangYaml:       {},
	LangMarkdown:   {},
	LangCue:        {},
}
