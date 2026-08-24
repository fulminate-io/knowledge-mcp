// SPDX-License-Identifier: Apache-2.0

package treesitter

// This file declares the TABLE and its single read path. The span scan and the
// string and tree helpers the normalization applies live beside it in
// chunker_callee_scan.go; the split is the repository's per-file line block, not
// a boundary of meaning.

// calleeProfile carries the per-language knobs the composed-callee
// normalization reads. Every one of them exists because a rule that is right
// for one grammar destroys another: a guard that strips `?` and `!` repairs
// Kotlin's `o!!.length` and DESTROYS Elixir's `Map.has_key?` and Ruby's
// `x.save!`, and a guard that declines a callee carrying a brace, a quote or a
// slash is right everywhere except a shell, where the callee is a COMMAND WORD
// and all three are ordinary characters.
//
// THE ZERO VALUE IS TODAY'S BEHAVIOR FOR EVERY GATED KNOB, with one documented
// exception noted on ChainFollow.
type calleeProfile struct {
	// ChainOps are the runes that decorate a chained access and belong to
	// neither the qualifier nor the name — `?` and `!` in the ECMAScript family,
	// Kotlin, Swift and C#; `&` in Ruby; `?` in PHP. A maximal RUN of them is
	// dropped when the rune following the run is in ChainFollow, so Kotlin's
	// two-rune `!!` comes out as `o.length` rather than the still-unnameable
	// `o!.length`. ASCII by construction; every operator any registered grammar
	// spells is ASCII.
	ChainOps string
	// ChainFollow is the set of runes that may follow a ChainOps run for the
	// drop to fire.
	//
	// ITS ZERO VALUE IS NOT ITS DEFAULT: empty means ".", which is what keeps
	// every row except PHP byte-identical to the behavior that shipped before
	// this field existed. PHP's nullsafe operator is `?->`, so its `?` is
	// followed by `-` and a followed-by-`.` rule could never fire for it. This
	// is the documented idiom of the parser package's profileFor in
	// lang_profile.go, where one field's zero value is its default and a
	// sibling's is not, and the reader supplies the difference.
	ChainFollow string
	// ElideLiteralBodies removes each balanced top-level `{...}` run from the
	// composed span, so a composite-literal receiver emits its TYPE:
	// `Format{}.Build` becomes `Format.Build`, which the resolver binds exactly
	// at the qualified-parent rung. Set only where a probed shape at that
	// grammar actually reached it.
	ElideLiteralBodies bool
	// DeclineNonName drops a callee that is not a name at all rather than
	// emitting it. It is the switch that separates the languages whose callee is
	// an identifier from the one whose callee is a shell command word.
	DeclineNonName bool
	// NameExtra are runes that are legitimate name characters in THIS language
	// and nowhere else — `?` and `!` in Elixir and Ruby predicate and bang
	// method names.
	NameExtra string
	// ReceiverWrappers and ReceiverArgStop ARE ONE KNOB IN TWO FIELDS and must
	// be set together; TestCalleeProfileCoverage asserts the parity.
	//
	// ReceiverWrappers are the node kinds that hold a receiver the Calls query
	// never reads. Groovy's `o?.size()` parses as access_op(identifier, "?.",
	// function_call(...)) and the query matches the INNER function_call, so the
	// receiver sits in the PARENT the query never sees; Lua's chained call nests
	// the inner call as the outer call's first child and the query captures only
	// the trailing identifier. In both the emission is a BARE method name whose
	// receiver was thrown away, and no signal carried out of the span cut can
	// see it, because the cut never fired.
	ReceiverWrappers []string
	// ReceiverArgStop are the node kinds that END the ancestor walk. An
	// argument-position call sits inside an argument node whose parent is the
	// CALLER's call, which of course starts before the argument does — so a walk
	// that does not stop there climbs into the caller and reads it as an elided
	// receiver, deleting a legitimate call. The stop must be keyed on KIND: on a
	// real Lua tree the capture, its own call and the enclosing argument node
	// all start at the SAME byte, so no offset comparison can tell them apart.
	ReceiverArgStop []string
}

// calleeProfiles is an OVERRIDE TABLE, not a closed enumeration. A language
// with no row gets the zero calleeProfile and keeps every emission the
// un-normalized code produced, which is why a shell's `${CMD}`, `"$BIN"`,
// `./local.sh` and `/usr/bin/env` survive verbatim. It copies the idiom of
// langProfiles in the parser package's lang_profile.go, whose single read path
// profileFor supplies a default that is not the zero value for one field.
//
// A NO-ROW LANGUAGE IS NOT AN UNTOUCHED LANGUAGE. The span scan and the
// delimiter cut run unconditionally, and the scan's quote-awareness is a real,
// measured, beneficial difference for a shell: a command word written as
// `"${BASH_SOURCE[0]}"` used to be sliced at the `]` INSIDE the quoted
// expansion and emitted as the garbage `}"`, and now emits whole. Still
// unbindable, but truthful rather than mangled, and pinned by the fixture
// bash_bracket_command_word.
//
// EVERY ROW WAS EXECUTED THROUGH Chunker.ChunkFile, and a knob no probed shape
// at that grammar reached was REMOVED rather than documented — Java, Scala,
// Groovy, Kotlin and Swift carry no ElideLiteralBodies for that reason. The
// rationales are PROBE RESULTS over the shapes listed in each comment, never
// claims about what a whole grammar can express.
var calleeProfiles = map[Language]calleeProfile{
	// Composite-literal receivers, no chain operators. Go additionally needs the
	// quote-aware scan for its RAW string literals: a backtick string takes no
	// escapes, so `T{p: ` + "`C:\\`" + `}.M()` is balanced only for a scan that
	// applies escapes inside `'` and `"` and NOT inside backticks.
	LangGo:   {ElideLiteralBodies: true, DeclineNonName: true},
	LangRust: {ElideLiteralBodies: true, DeclineNonName: true},
	LangCPP:  {ElideLiteralBodies: true, DeclineNonName: true},

	// Optional chaining and non-null assertion, plus object/composite literal
	// receivers.
	LangJavaScript: {ChainOps: "?!", ElideLiteralBodies: true, DeclineNonName: true},
	LangTypeScript: {ChainOps: "?!", ElideLiteralBodies: true, DeclineNonName: true},
	LangTSX:        {ChainOps: "?!", ElideLiteralBodies: true, DeclineNonName: true},

	// C# reaches ChainOps through a nested conditional access: `o?.ToString()`
	// emits nothing at all, because the invocation's function: field is a
	// conditional_access_expression the Calls query never matches — but when the
	// conditional access nests INSIDE a member access the function: field IS a
	// member_access_expression, which the query captures WHOLE, so `a?.b.C()`
	// emits `a?.b.C` and `a!.E()` emits `a!.E`. Without ChainOps both are
	// DELETED outright; with it both repair to the byte-identical undecorated
	// spelling. Neither corpus exercises this row, so it is fixture-pinned:
	// csharp_optional_chain. `(a?.b).F()` is a different shape entirely — it
	// reaches the cut, comes out bare, and is declined as a chained tail.
	LangCSharp: {ChainOps: "?!", ElideLiteralBodies: true, DeclineNonName: true},

	// A TRAILING LAMBDA puts a balanced brace run straight into the composed
	// span — Kotlin's `listOf(1).map{it}.size()` emits `map{it}.size` and
	// Swift's `[1,2].map{ $0 }.count()` emits `map{$0}.count` — so the missing
	// ElideLiteralBodies here is a DELIBERATE OUTCOME rather than an absence of
	// shapes. With no elision the span is not a name and is declined, which is
	// TRUTHFUL; eliding would fabricate the qualifier `map`.
	LangKotlin: {ChainOps: "?!", DeclineNonName: true},
	LangSwift:  {ChainOps: "?!", DeclineNonName: true},

	// Ruby's safe-navigation operator is `&.`, and `&` alone is what may go in
	// this row: a `?` or `!` here destroys `x.empty?` and `x.save!`, which
	// ruby_predicate_and_bang_names catches by emitting nothing at all. The
	// grammar emits the INTERMEDIATE hop as its own callee, so `o&.a&.b`
	// produces both `o&.a` and `o&.a&.b` and the row repairs two edges, not one.
	// The block-pass form `arr.map(&:to_s)` is unaffected: its `&` sits in
	// ARGUMENT position and never enters a callee span.
	LangRuby: {ChainOps: "&", DeclineNonName: true, NameExtra: "?!"},

	// PHP is the ONLY row that sets ChainFollow, and the field exists for it:
	// `$o?->a->b()` emits `$o?->a->b`, whose `?` is followed by `-`, so a
	// followed-by-`.` rule could never fire. `$o?->m()` emits NOTHING at all
	// before and after — php_nullsafe_chain pins both dispositions.
	LangPHP: {ChainOps: "?", ChainFollow: "-", DeclineNonName: true},

	// THE WRAPPER-ELISION PRODUCERS. Groovy and Lua are the only two registered
	// languages whose emission LOSES the receiver, measured by running each
	// DeclineNonName language's safe-navigation shape through Chunker.ChunkFile
	// and checking whether the receiver text survives into the callee. Lua is
	// also the one language whose chained tail the cut-keyed rule cannot reach,
	// because its composed span is the trailing identifier alone and no
	// parenthesis ever enters it. Corpus coverage for Lua is ZERO — every Lua
	// span in either corpus is a fixture — so the fixtures carry the whole
	// burden.
	LangGroovy: {DeclineNonName: true,
		ReceiverWrappers: []string{"access_op"}, ReceiverArgStop: []string{"argument_list"}},
	LangLua: {DeclineNonName: true,
		ReceiverWrappers: []string{"function_call"}, ReceiverArgStop: []string{"function_arguments"}},

	// Java's double-brace initializer is DECLINED, not repaired: the cut fires
	// on the constructor's own `()` and the brace text lands in the tail, so no
	// balanced brace run at depth zero ever reaches an elision. Shapes probed at
	// Scala — `new Thing{}.norm`, `Pt(1,2).x.norm`, `m.keys.head` and
	// `List(1).map{x => x}.size` — put no brace-delimited receiver in a composed
	// span either.
	LangJava:   {DeclineNonName: true},
	LangScala:  {DeclineNonName: true},
	LangPython: {DeclineNonName: true},
	LangC:      {DeclineNonName: true},

	// Elixir's ChainOps is DELIBERATELY EMPTY and its NameExtra is what keeps
	// `Map.has_key?`, `File.read!` and `Enum.empty?` intact. This row and the
	// fixture elixir_predicate_and_bang_names are one change.
	LangElixir: {DeclineNonName: true, NameExtra: "?!"},

	// LangBash TAKES NO ROW, deliberately. A shell callee is a command word, not
	// a name: `${CMD}`, `"$BIN"`, `cmd-with-dash`, `.` and `/usr/bin/env` carry
	// braces, quotes, dots and slashes, and any gated knob breaks at least one
	// of them. It also means NEITHER decline fires on a shell script — the bare
	// command words a chained-tail census counts there are inert.
}

// calleeProfileNoRow names the languages that carry a non-empty Calls query and
// take NO row above, so the coverage test can tell a deliberate omission from a
// language nobody has considered.
var calleeProfileNoRow = []Language{LangBash}

// calleeProfileFor is the ONLY read path onto the table, and it is where
// ChainFollow's non-zero default is supplied.
func calleeProfileFor(lang Language) calleeProfile {
	p := calleeProfiles[lang]
	if p.ChainFollow == "" {
		p.ChainFollow = "."
	}
	return p
}

// calleeProfiledLanguages returns every language carrying a row. It exists so
// the coverage test can assert the declared/consumed partition without naming
// the table, keeping calleeProfileFor the single read path.
func calleeProfiledLanguages() []Language {
	out := make([]Language, 0, len(calleeProfiles))
	for lang := range calleeProfiles {
		out = append(out, lang)
	}
	return out
}
