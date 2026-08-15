// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	sitter "github.com/smacker/go-tree-sitter"
)

// ImportKind classifies WHAT an import statement binds, in terms every language
// shares rather than in ECMAScript's terms. A language's arm picks the kind
// whose description matches what its own grammar does; no kind is reserved to
// one language.
type ImportKind int

const (
	// ImportNamed is a NAMED entity taken from the module, optionally under a
	// different local name.
	ImportNamed ImportKind = iota
	// ImportDefault is the module's default export, bound under a local name.
	ImportDefault
	// ImportNamespace is the MODULE ITSELF bound under a local name.
	ImportNamespace
	// ImportWildcard is every exported name of the module entering this scope
	// unqualified.
	//
	// No ECMAScript form produces it — the language has no wildcard import —
	// and it is declared here anyway because the carrier is a shared contract
	// and a carrier that cannot hold a shape is a structural blocker for the
	// language that has it. rust `use x::*`, python `from x import *`, java
	// `import com.x.*` and `import static C.*`, csharp `using N;` and
	// `using static A.B;`, and scala `import a.b._` all bind an unbounded set
	// of names under NO local name, so no (Imported, Local) pair can represent
	// them and no other kind fits. Its consumer in this package's own ticket is
	// the module resolver, which switches on Kind and refuses it.
	ImportWildcard
	// ImportSideEffect is the module being loaded while binding no name at all.
	ImportSideEffect
)

// ImportBinding is ONE NAME an import statement brings into a file, recorded in
// language-neutral terms. It is the shared carrier for every language's import
// capture, not an ECMAScript-local record: the per-language arms registered in
// importParsers all produce this same struct.
//
// A statement that binds several names produces SEVERAL ImportBindings — a
// combined default-and-named import is one statement and two bindings. That is
// deliberately NOT the same count as ctx.Imports, which gains exactly one entry
// per dependency-declaring STATEMENT because each of its entries becomes an
// IMPORTS edge.
//
// THE ALIAS IS THE (Imported, Local) PAIR, and it is language-neutral by
// construction:
//
//	kotlin  import a.b.C as D      -> Specifier "a.b",   Imported "C", Local "D"
//	php     use A\B as C;          -> Specifier "A",     Imported "B", Local "C"
//	rust    use x::y as z;         -> Specifier "x",     Imported "y", Local "z"
//	python  from x import a as b   -> Specifier "x",     Imported "a", Local "b"
//	scala   import a.b.{C => D}    -> Specifier "a.b",   Imported "C", Local "D"
//	csharp  using X = A.B;         -> Specifier "A",     Imported "B", Local "X"
//	java    import com.x.Y;        -> Specifier "com.x", Imported "Y", Local "Y"
type ImportBinding struct {
	// Specifier is the module / namespace / package specifier verbatim, with
	// quote characters stripped.
	Specifier string

	// Local is THE NAME BOUND IN THIS FILE, CARRIED VERBATIM, AND IT IS NEVER
	// NORMALIZED. It holds exactly what the source wrote, including all three
	// of these, none of which any arm or any later "cleanup" may collapse into
	// another:
	//
	//	""  — no alias clause. It does NOT mean "unknown" and MUST NOT be
	//	      filled with a derived default: not a last path segment, not a
	//	      file basename, not a package name. A consumer needing a fallback
	//	      derives it at the point of use and says so there.
	//	"." — Go's dot-import `import . "x/y"`, which merges the package's
	//	      exported names into file scope. A DISTINCT case, not a missing
	//	      alias.
	//	"_" — Go's blank import `import _ "x/y"`, which binds nothing and
	//	      exists for side effects.
	//
	// Collapsing "." or "_" into "" or into a derived name silently breaks the
	// Go arm's binding rules: a blank import would start binding a name it must
	// not bind, and a dot import would become indistinguishable from a plain
	// one. No test can catch a normalization nobody has written yet, which is
	// exactly why this rule lives in the code as the durable artifact.
	Local string

	// Imported is the name taken FROM the module. Empty for the default,
	// namespace, wildcard and side-effect kinds, none of which names a member.
	Imported string

	// Kind is what the statement binds.
	Kind ImportKind

	// TypeOnly is set only by languages that distinguish a type-only import,
	// such as TypeScript's `import type {A}`. Ignored elsewhere.
	TypeOnly bool
}

// ReExport is one name a file re-exports FROM another module — `export {X} from
// './y'` and `export * from './w'`. It is recorded separately from
// ImportBinding because a re-export binds nothing in THIS file's scope: it
// forwards a name onward, which is what lets a resolver follow a barrel file's
// chain to the declaring module without re-parsing it.
//
// A re-export specifier IS a real dependency and does earn a ctx.Imports entry;
// a sourceless `export {X}` declares no dependency and contributes nothing.
//
// THE THREE FIELDS ARE DECLARED ON ONE LINE ON PURPOSE and a future reader
// should not split them: they are the same three language-neutral roles
// ImportBinding carries, and the carrier-vocabulary gate counts the neutral
// field lines of the CARRIER. Splitting these three would double that count
// against a file whose shape had not actually changed.
//
//	Specifier — the `from '<spec>'` target.
//	Local     — the name this module re-exports it AS; "" for `export * from`.
//	Imported  — the name in the SOURCE module; "" for `export * from`.
type ReExport struct {
	Specifier, Local, Imported string
}

// importParsers holds the per-language import-capture arms. A registered arm
// OWNS every capture its language's Imports query produces: extractFileContext
// dispatches to it before it looks at any capture name, so an arm decides for
// itself what becomes a ctx.Imports entry and what becomes an ImportBinding.
//
// EMPTY for the 29 languages with no arm, whose captures take the default path
// and land in ctx.Imports exactly as they always have.
//
// AN ARM IS INVOKED ONCE PER CAPTURE, NOT ONCE PER MATCH. A language whose
// query binds two captures to a single statement — python's does today,
// `[(import_statement) (import_from_statement module_name: (dotted_name) @path)]
// @import` — must either make its arm idempotent per statement or narrow its
// query when it registers one.
var importParsers = map[Language]func(node *sitter.Node, src []byte, ctx *ChunkContext){}
