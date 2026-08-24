// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// langProfile carries the three per-language knobs the resolution ladder reads:
// how a qualified reference's qualifier is separated from its name, whether an
// import outranks a local declaration of the same bare name, and whether a bare
// name can reach a sibling member of its own container.
type langProfile struct {
	// Separators are the qualifier separators the language writes, longest
	// first at a tie position. An EXPLICITLY EMPTY set means NEVER SPLIT, which
	// is not the same as having no row at all — see profileFor.
	Separators []string

	// ImportsBeatLocals orders the unqualified import rung relative to the
	// bare-name rungs. True keeps R4 ahead of R5/R6; false moves it behind
	// them, for the languages where a local declaration legally shadows an
	// import of the same name.
	ImportsBeatLocals bool

	// SkipSiblingRung is true when a bare name never means a sibling member in
	// this language: a receiverless call carries no implicit receiver, so R5
	// would bind an edge the language itself does not have.
	//
	// FALSE MEANS THE SIBLING RUNG APPLIES, which is what every language did
	// before this field existed. The polarity is deliberate: the zero value is
	// today's behavior, so a row that omits the field is unchanged and the
	// languages nobody derived cannot be moved by inattention. A knob whose
	// zero value meant SKIP would have disabled R5 for every existing row the
	// moment it was added, with no edit to any of them.
	SkipSiblingRung bool

	// SkipDynamicRung is true when the dynamic rung would do this language more
	// harm than good: R3's candidate set is every declaration of the name in
	// the reference's own scope regardless of parent, which is an open set
	// asserting "one of these, or something static analysis cannot reach".
	//
	// FALSE MEANS THE DYNAMIC RUNG APPLIES, which is what every language did
	// before this field existed. The polarity follows SkipSiblingRung's rule
	// verbatim and for the same reason: the zero value is today's behavior, so
	// a row that omits the field is unchanged and the languages nobody derived
	// cannot be moved by inattention.
	//
	// ONLY C SETS IT, AND ON A MEASURED NUMBER RATHER THAN A PREFERENCE. C's
	// dispatch is written through function-pointer struct fields, and the
	// dominant shape names the field the same way the enclosing function is
	// named — measured on curl 8.9.1, three colliding names in one file, each
	// a `static int domore_getsock(...)` whose own body dispatches
	// `conn->handler->domore_getsock(...)`. The local function is the CALLER
	// and provably not the referent, so an R3 group there would assert a false
	// SELF-CALL. That is the class R2X's own comment calls entirely false
	// rather than merely incomplete. The cost of the knob is separately
	// measured at zero: C reached this rung through no path at all before its
	// separators landed in the same change, so it removes no existing edge.
	SkipDynamicRung bool
}

// langProfiles is an OVERRIDE table, not a closed enumeration: a language with
// no row here reaches the default through profileFor, and that is how every
// unlisted language keeps today's behavior without an entry. It is deliberately
// NOT merged into treesitter's scopeKinds, which is closed and carries a
// per-language completeness obligation this table does not.
//
// Every row states ImportsBeatLocals explicitly. The field's zero value is
// false, so a row that omitted it would silently move the import rung for a
// language that never asked for it. SkipSiblingRung is the OPPOSITE case and
// deliberately so: its zero value is the unchanged behavior, so only the rows
// that were derived state it and every other row is left alone.
var langProfiles = map[treesitter.Language]langProfile{
	// Dot-separated, import-first.
	treesitter.LangJava:   {Separators: []string{"."}, ImportsBeatLocals: true},
	treesitter.LangSwift:  {Separators: []string{"."}, ImportsBeatLocals: true},
	treesitter.LangGroovy: {Separators: []string{"."}, ImportsBeatLocals: true},

	// Dot-separated, import-first, AND a bare name cannot reach a sibling
	// member. THESE FOUR HAD NO ROW AT ALL and reached the default, so each one
	// restates the default's other two fields VERBATIM: a row that omitted
	// Separators would register an EXPLICITLY EMPTY set, which means NEVER SPLIT
	// — the opposite of the default — and would retire the qualified rungs for
	// Go and the whole TypeScript family.
	//
	// THE ROWS WERE DERIVED BY RUNNING THE LANGUAGE. The question each answers:
	// does a bare, receiverless call inside a member resolve to a sibling member
	// of the same container?
	//   go         EXECUTED  go build -> `./x.go:7:30: undefined: a`
	//   javascript EXECUTED  node -> `ReferenceError a is not defined`
	//   typescript CITED     inherits the javascript execution by language
	//   tsx        CITED     identity — TypeScript's class semantics are
	//                        JavaScript's, a bare call in a method has no
	//                        implicit receiver in either, and the collector's
	//                        tsx files are TypeScript.
	treesitter.LangGo:         {Separators: []string{"."}, ImportsBeatLocals: true, SkipSiblingRung: true},
	treesitter.LangJavaScript: {Separators: []string{"."}, ImportsBeatLocals: true, SkipSiblingRung: true},
	treesitter.LangTypeScript: {Separators: []string{"."}, ImportsBeatLocals: true, SkipSiblingRung: true},
	treesitter.LangTSX:        {Separators: []string{"."}, ImportsBeatLocals: true, SkipSiblingRung: true},

	// Dot-separated, and a local legally shadows an import: a python
	// `from x import foo` followed by `def foo()` rebinds the name and the
	// local wins. Same for csharp, elixir, ruby, scala and kotlin.
	//
	// PYTHON ALSO SKIPS THE SIBLING RUNG, and ruby and java DELIBERATELY DO NOT.
	// All three were EXECUTED at the toolchains on this machine, on a container
	// declaring a member `a` and a member `b` whose body calls a bare `a()`:
	//   python EXECUTED  python3 -> `NameError: name 'a' is not defined. Did you
	//                    mean: 'self.a'?`                          SKIP
	//   ruby   EXECUTED  bare sibling call runs (implicit self)    KEEP
	//   java   EXECUTED  compiles and runs (implicit this)         KEEP
	// RUBY AND JAVA ARE DERIVED-KEEP, NOT UNDERIVED. Their conclusion is the
	// field's zero value, so the correct edit to their rows is none, and this
	// comment is what lets a later reader tell a derived-keep from a row nobody
	// ever ran. An earlier sentence in this ticket's own text grouped python
	// with them; the interpreter says otherwise, and the interpreter is why
	// python carries the field and they do not.
	treesitter.LangPython: {Separators: []string{"."}, ImportsBeatLocals: false, SkipSiblingRung: true},
	treesitter.LangCSharp: {Separators: []string{"."}, ImportsBeatLocals: false},
	treesitter.LangElixir: {Separators: []string{"."}, ImportsBeatLocals: false},
	treesitter.LangRuby:   {Separators: []string{"."}, ImportsBeatLocals: false},
	treesitter.LangScala:  {Separators: []string{"."}, ImportsBeatLocals: false},
	treesitter.LangKotlin: {Separators: []string{"."}, ImportsBeatLocals: false},

	// Languages whose qualified references carry a separator that is not a dot.
	// Each set was read off an AST probe of the language's own call forms:
	// rust `foo::bar` and `obj.do_thing`; cpp `ns::g`, `ptr->m2` and `obj.m`;
	// php `Bar::stat`, `$o->doThing` and `\Other\Thing`; lua `obj:meth`.
	treesitter.LangRust: {Separators: []string{"::", "."}, ImportsBeatLocals: true},
	treesitter.LangCPP:  {Separators: []string{"::", "->", "."}, ImportsBeatLocals: true},
	treesitter.LangPHP:  {Separators: []string{"::", "->", "\\"}, ImportsBeatLocals: true},
	treesitter.LangLua:  {Separators: []string{".", ":"}, ImportsBeatLocals: true},

	// C DISPATCHES THROUGH FUNCTION-POINTER STRUCT FIELDS, written `h->flush(c)`
	// and `ops.flush(c)`, so it does have a qualified call form and its
	// separators are the arrow and the dot, longest first. This row was an
	// EXPLICITLY EMPTY set until the dispatch capture landed, and the comment
	// that stood here said c and bash "resolve exclusively through the
	// unqualified rungs" — true of bash still, and false of c now.
	//
	// THE DYNAMIC RUNG IS OFF FOR C, and the whole derivation is on the
	// SkipDynamicRung field doc rather than repeated here.
	treesitter.LangC: {Separators: []string{"->", "."}, ImportsBeatLocals: true, SkipDynamicRung: true},

	// EXPLICITLY EMPTY, meaning never split. Bash has no qualified call form,
	// and it writes dots inside ordinary bare names — a command name such as
	// `./deploy.sh` — which the defaulting dot-split would tear into a bogus
	// qualifier and name. An empty set also decides which rungs it can ever
	// reach: resolveRef enters resolveQualified only when the qualifier is
	// non-empty, so bash resolves exclusively through the unqualified rungs.
	treesitter.LangBash: {Separators: []string{}, ImportsBeatLocals: true},
}

// profileFor is the ONLY read path into langProfiles. No other code indexes the
// map directly, so the defaulting rule below cannot be bypassed.
//
// THE DEFAULT IS NOT THE ZERO VALUE, and that is what makes this table land
// inert. A language with NO row splits at the dot and keeps the import rung
// first, reproducing the ladder exactly as it stood before this table existed.
// An explicitly registered empty Separators set means the OPPOSITE: never split
// at all.
//
// THE SIBLING KNOB IS THE ONE FIELD WHOSE ZERO VALUE *IS* THE DEFAULT, so this
// literal states it by leaving it out: an unlisted language keeps the sibling
// rung, which is what every language did before that field existed. Twelve
// registered languages — swift, groovy, csharp, elixir, scala, kotlin, rust,
// cpp, php, lua, c and bash — were never derived for it and are unchanged by
// omission for exactly the same reason.
func profileFor(lang treesitter.Language) langProfile {
	if p, ok := langProfiles[lang]; ok {
		return p
	}
	return langProfile{Separators: []string{"."}, ImportsBeatLocals: true}
}

// splitQualifier splits target at the LAST position where any of the language's
// separators occurs, the longest separator winning at a tie position — so a cpp
// `a::b` yields ("a","b") and never ("a:","b").
//
// A target that begins with a separator keeps that separator inside the
// qualifier, because it is part of the namespace's own spelling: php's
// `\Other\Thing::go` splits at the `::` into ("\Other\Thing", "go"), qualifier
// backslash included. A language with an empty separator set never splits, and
// returns the whole target as the name.
//
// It allocates nothing: both results are substrings of the input.
func splitQualifier(lang treesitter.Language, target string) (qualifier, name string) {
	at, sepLen := -1, 0
	for _, sep := range profileFor(lang).Separators {
		if sep == "" {
			continue
		}
		i := strings.LastIndex(target, sep)
		if i < 0 {
			continue
		}
		if i > at || (i == at && len(sep) > sepLen) {
			at, sepLen = i, len(sep)
		}
	}
	if at < 0 {
		return "", target
	}
	return target[:at], target[at+sepLen:]
}

// packageQualifierLangs are the languages in which a qualified reference's
// qualifier may be a PACKAGE PATH rather than a bound name — `com.acme.foo.Bar`
// written out in full, with no import statement anywhere to hang a bind on.
//
// IT IS A SET RATHER THAN A langProfile FIELD because the answer is a plain
// yes/no with no per-language spelling behind it, and a field would oblige every
// existing row to restate it. The three members are exactly the languages
// scopeKinds gives ScopeDeclaredNamespace AND that write a package path in
// source: C# and PHP declare namespaces too, but a C# reference reaches a type
// through a `using` and a PHP one through a `use`, so both are already answered
// by the bound-qualifier rung and adding them here would derive a scope for a
// qualifier a bind has already resolved.
var packageQualifierLangs = map[treesitter.Language]bool{
	treesitter.LangJava:   true,
	treesitter.LangKotlin: true,
	treesitter.LangScala:  true,
}

// qualifierScope maps a fully-qualified reference's QUALIFIER onto the scope its
// declaration would live in, reporting false for every language with no
// package-qualified reference form — which is every language but the three
// above, so the rung that reads it is inert everywhere else and the common path
// pays one map read.
//
// A PACKAGE IS A NAMESPACE, so there is no path derivation here at all and none
// is wanted: kotlin and scala legally permit a package that does not match its
// directory, so any oracle mapping `com.acme.foo` onto `com/acme/foo` is exact
// for java and incomplete for the other two.
//
// THE TOKEN IS BUILT THROUGH treesitter's OWN BUILDER, never assembled here.
// The scope this returns has to be BYTE-IDENTICAL to the one the chunker stamped
// on the declaration, or the lookup silently matches nothing while every gate
// stays green — so both sides call NamespaceToken and both hand it to ScopeID.
func qualifierScope(lang treesitter.Language, qualifier string) (string, bool) {
	if qualifier == "" || !packageQualifierLangs[lang] {
		return "", false
	}
	return treesitter.ScopeID("", lang, treesitter.NamespaceToken(lang, qualifier)), true
}
