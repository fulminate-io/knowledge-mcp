// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// calleeLiteralFixtures covers the callee shapes whose RECEIVER THE SYNTAX DOES
// NOT NAME — a composite literal, a string or regex literal, an optional-chain
// or non-null-assertion operator, a parenthesized expression, or a receiver the
// grammar hands over in a wrapper node the Calls query never reads.
//
// Every `src` here was executed through Chunker.ChunkFile against the unfixed
// tree, and every `absent` entry is a spelling that emission ACTUALLY produced,
// never an imagined one. They live in their own file because
// queries_capture_test.go is close to the repository's 500-line-per-file block
// and this table roughly triples the fixture count; calleeFixtures() appends
// them, exactly as it already appends the constructor table.
//
// The last three entries are CHARACTERIZATION GUARDS rather than reproductions:
// they are green against the unfixed tree and must stay green, and they are the
// only thing in the repository that fails if the normalization is applied
// globally instead of per language. See calleeProtectionFixtures.
func calleeLiteralFixtures() []calleeFixture {
	return append(calleeReceiverFixtures(), calleeProtectionFixtures()...)
}

// calleeLiteralFixtureNames is the locked name list. It is stated as a literal
// rather than derived from the table so that DELETING a fixture is a test
// failure rather than a silent narrowing of coverage.
var calleeLiteralFixtureNames = []string{
	"go_composite_literal_receiver",
	"go_degraded_new_paren",
	"go_raw_string_literal_receiver",
	"rust_string_literal_receiver",
	"rust_struct_literal_receiver",
	"cpp_brace_literal_receiver",
	"csharp_object_initializer_receiver",
	"csharp_optional_chain",
	"csharp_paren_receiver",
	"java_double_brace_receiver",
	"python_string_literal_receiver",
	"javascript_regex_literal_receiver",
	"javascript_optional_chain",
	"typescript_non_null_assertion",
	"tsx_optional_chain",
	"kotlin_optional_chain",
	"kotlin_non_null_assertion",
	"kotlin_trailing_lambda_chain",
	"swift_optional_chain",
	"swift_non_null_assertion",
	"groovy_optional_receiver",
	"ruby_safe_navigation",
	"php_nullsafe_chain",
	"bash_bracket_command_word",
	"elixir_predicate_and_bang_names",
	"ruby_predicate_and_bang_names",
	"bash_command_words",
}

// TestCalleeLiteralFixtureCoverage proves the TABLE still holds every locked
// fixture. It is the necessary companion to TestQualifiedCalleeCapture passing:
// that test proves every fixture IN the table passed, and this one proves the
// table was not quietly shortened. Neither alone proves a named fixture both
// exists and passes.
func TestCalleeLiteralFixtureCoverage(t *testing.T) {
	got := make(map[string]calleeFixture, len(calleeLiteralFixtureNames))
	for _, f := range calleeLiteralFixtures() {
		name := f.subtestName()
		require.NotContains(t, got, name, "duplicate fixture name %q", name)
		got[name] = f
	}

	for _, name := range calleeLiteralFixtureNames {
		f, ok := got[name]
		require.True(t, ok, "locked fixture %q is missing from the table", name)
		// A fixture asserting neither a want nor an absent pins nothing at
		// all, which is how a table stays the right LENGTH while losing its
		// meaning.
		assert.NotEmpty(t, append(append([]string{}, f.want...), f.absent...),
			"fixture %q states neither a want nor an absent", name)
	}
	assert.Len(t, got, len(calleeLiteralFixtureNames),
		"the table carries a fixture the locked list does not name")
}

// calleeReceiverFixtures are the RED-FIRST reproductions: each one's `absent`
// list is what the unfixed tree emits today.
func calleeReceiverFixtures() []calleeFixture {
	return []calleeFixture{
		// THE FOURTH CALL IS THE BRACE-DEPTH GATE and the third is not. The
		// third literal body holds no parenthesis at all, so the delimiter cut
		// never fires on it and it pins the ELISION only. The fourth carries
		// `unsafe.Slice(x, 2)` INSIDE the literal, so a depth-BLIND cut takes
		// that closing paren as its cut point and slices the type name off —
		// which is exactly what `,Depth:1}.Build` is. `unsafe.Slice` is in want
		// because that argument-position call is its own edge and must survive:
		// its span carries no delimiter and it is qualified.
		{lang: LangGo, name: "go_composite_literal_receiver", path: "a/lit.go",
			src: "package lit\n\nimport (\n\t\"unsafe\"\n\n\t\"pgx\"\n\t\"prototext\"\n\t\"protoimpl\"\n)\n\ntype F struct{}\n\nfunc (F) Build() {}\n\nfunc call(m, x any) {\n\tF{}.Build()\n\tpgx.Identifier{\"a\", \"b\"}.Sanitize()\n\tprototext.MarshalOptions{Multiline: true, Indent: \"\"}.Format(m)\n\tprotoimpl.TypeBuilder{GoTypes: unsafe.Slice(x, 2), Depth: 1}.Build()\n}\n",
			want: []string{"F.Build", "pgx.Identifier.Sanitize", "prototext.MarshalOptions.Format",
				"protoimpl.TypeBuilder.Build", "unsafe.Slice"},
			absent: []string{"F{}.Build", "pgx.Identifier{\"a\",\"b\"}.Sanitize",
				"prototext.MarshalOptions{Multiline:true,Indent:\"\"}.Format", ",Depth:1}.Build"}},

		// The grammar produces an ERROR node here, so the recovered selector
		// spans an opener with no closer and the cut finds no closing delimiter
		// to cut after. `new` survives because its OWN span carries no
		// delimiter, so the cut never fires on it.
		{lang: LangGo, name: "go_degraded_new_paren", path: "a/deg.go",
			src:    "package deg\n\nfunc call(c any) {\n\tnew(c.X.String())\n}\n",
			want:   []string{"new"},
			absent: []string{"new(c.X.String"}},

		// THE RAW-STRING CATCHER. A backtick string takes no escapes, so a scan
		// that consumes the byte after every backslash swallows the closing
		// delimiter, leaves the span unbalanced and DECLINES this callee — which
		// is not garbage but is not right either. Only `T.M` distinguishes a
		// correct scan from a merely-not-garbage one.
		{lang: LangGo, name: "go_raw_string_literal_receiver", path: "a/raw.go",
			src:    "package raw\n\ntype T struct{ p string }\n\nfunc (T) M() {}\n\nfunc call() {\n\tT{p: `C:\\`}.M()\n}\n",
			want:   []string{"T.M"},
			absent: []string{"T{p:`C:\\`}.M"}},

		// The bare `len` must be absent too, or the fixture passes against the
		// wrong rung of the resolution ladder: a bare method name for a receiver
		// the syntax does not name is what fabricates a confident edge.
		{lang: LangRust, name: "rust_string_literal_receiver", path: "a/s.rs",
			src:    "fn call(v: String) {\n    \"s\".len();\n    v.len();\n}\n",
			want:   []string{"v.len"},
			absent: []string{"\"s\".len", "len"}},

		{lang: LangRust, name: "rust_struct_literal_receiver", path: "a/p.rs",
			src:    "struct P{x:i32}\nimpl P { fn n(&self) {} }\nfn call() {\n    P{x:1}.n();\n}\n",
			want:   []string{"P.n"},
			absent: []string{"P{x:1}.n"}},

		{lang: LangCPP, name: "cpp_brace_literal_receiver", path: "a/p.cpp",
			src:    "struct Point { int x; int y; double norm(); };\nvoid call() {\n    Point{1,2}.norm();\n    plain(3);\n}\n",
			want:   []string{"Point.norm", "plain"},
			absent: []string{"Point{1,2}.norm"}},

		// THE `newD` SPELLING IS DELIBERATE AND IS NOT THIS TICKET'S TO FIX:
		// the whitespace strip joins `new D` into `newD` on the composed span,
		// which is pre-existing behavior.
		{lang: LangCSharp, name: "csharp_object_initializer_receiver", path: "a/D.cs",
			src:    "class D {\n    public int X;\n    public void Go() {}\n}\nclass R {\n    void Run() {\n        new D{X=1}.Go();\n        plain(3);\n    }\n}\n",
			want:   []string{"newD.Go", "plain"},
			absent: []string{"newD{X=1}.Go"}},

		// Both operator runs are followed by `.`, the default follow set, so
		// they repair exactly as TypeScript's do, and both repaired spellings
		// are byte-identical to the undecorated ones AND QUALIFIED — so the
		// chained-tail decline leaves them alone.
		{lang: LangCSharp, name: "csharp_optional_chain", path: "a/O.cs",
			src:    "class R {\n    void Go(dynamic a) {\n        a?.b.C();\n        a!.E();\n        plain(1);\n    }\n}\n",
			want:   []string{"a.b.C", "a.E", "plain"},
			absent: []string{"a?.b.C", "a!.E"}},

		// THE LAST TWO ABSENTS ARE THE DISCRIMINATOR. The parenthesized receiver
		// is DECLINED, not unwrapped: an interim design emitted `a.b.F` and
		// `x.y.G`, so without those two entries this fixture would pass against
		// either treatment and pin neither. The UNDECORATED call is required
		// too — `(x.y).G()` carries no chain operator at all and fabricates
		// exactly as the decorated one does, out of the identical cut.
		{lang: LangCSharp, name: "csharp_paren_receiver", path: "a/P.cs",
			src:    "class R {\n    void Go(dynamic a, dynamic x) {\n        (a?.b).F();\n        (x.y).G();\n        plain(1);\n    }\n}\n",
			want:   []string{"plain"},
			absent: []string{"F", "G", "a.b.F", "x.y.G"}},

		// THE CALL IS NOT REPAIRED, IT IS DECLINED: the cut fires on the
		// constructor's own `()` and the brace text lands in the tail, so no
		// balanced brace run at depth zero ever reaches the elision.
		{lang: LangJava, name: "java_double_brace_receiver", path: "a/H.java",
			src:    "class H {\n    void go() {\n        new java.util.HashMap<String,String>(){{ put(\"a\",\"b\"); }}.get(\"a\");\n        plain(3);\n    }\n}\n",
			want:   []string{"put"},
			absent: []string{";}}.get"}},

		{lang: LangPython, name: "python_string_literal_receiver", path: "a/j.py",
			src:    "def call(xs):\n    plain(3)\n    return \",\".join(xs)\n",
			want:   []string{"plain"},
			absent: []string{"\",\".join", "join"}},

		// THREE DIFFERENT MANGLINGS FROM ONE CLASS: the first regex holds no
		// delimiter so the span survives whole, the second closes a group so the
		// cut fires inside the literal, and the third closes a character class
		// so the bracket half of the same cut fires. All three are unbindable
		// and the bare `test` must not appear either.
		{lang: LangJavaScript, name: "javascript_regex_literal_receiver", path: "a/re.js",
			src:    "function call(s) {\n  plain(3);\n  /^\\s*$/.test(s);\n  /^(a|b)$/.test(s);\n  /[abc]/.test(s);\n}\n",
			want:   []string{"plain"},
			absent: []string{"/^\\s*$/.test", "$/.test", "/.test", "test"}},

		// THE THIRD CASE IS A DECLINE, NOT A REPAIR. `?.getAttribute` binds a
		// same-named module-scope local as a DYNAMIC edge at confidence 1.00 and
		// the bare spelling binds the same local as a BOUND edge, so "repairing"
		// it would UPGRADE a fabrication into the graph's strongest claim.
		{lang: LangJavaScript, name: "javascript_optional_chain", path: "a/oc.js",
			src:    "function call(o) {\n  o?.dispose();\n  this._opts?.onAdd();\n  o.get(1)?.getAttribute('x');\n}\n",
			want:   []string{"o.dispose", "this._opts.onAdd"},
			absent: []string{"?.getAttribute", "getAttribute"}},

		{lang: LangTypeScript, name: "typescript_non_null_assertion", path: "a/nn.ts",
			src:    "function call(items: any, before: any) {\n  items.enum!.join(',');\n  before!.getTime();\n}\n",
			want:   []string{"items.enum.join", "before.getTime"},
			absent: []string{"items.enum!.join", "before!.getTime"}},

		{lang: LangTSX, name: "tsx_optional_chain", path: "a/oc.tsx",
			src:    "function call(o: any) {\n  o?.dispose();\n}\n",
			want:   []string{"o.dispose"},
			absent: []string{"o?.dispose"}},

		{lang: LangKotlin, name: "kotlin_optional_chain", path: "a/O.kt",
			src:    "fun call(o: Any?) {\n    o?.length()\n}\n",
			want:   []string{"o.length"},
			absent: []string{"o?.length"}},

		// THE RUN CATCHER. Under a single-rune drop `o!!.length` becomes
		// `o!.length`, which is still not a name and is then declined outright,
		// so `o!.length` must be absent as well as the raw spelling. With both
		// calls in one function the correct build emits ONE edge at weight 2.
		{lang: LangKotlin, name: "kotlin_non_null_assertion", path: "a/N.kt",
			src:    "fun call(o: Any?) {\n    o?.length()\n    o!!.length()\n}\n",
			want:   []string{"o.length"},
			absent: []string{"o!!.length", "o!.length"}},

		// BOTH ABSENTS ARE MEASURED CONSEQUENCES OF DIFFERENT RULES: the
		// brace-carrying span is declined by nameability, because Kotlin takes
		// no literal-body elision and eliding would fabricate the qualifier
		// `map`; while `map` itself is a CUT TAIL from `listOf(1).map` and is
		// declined as a bare chained tail. `listOf` survives because its own
		// span carries no delimiter.
		{lang: LangKotlin, name: "kotlin_trailing_lambda_chain", path: "a/L.kt",
			src:    "fun call() {\n    listOf(1).map{it}.size()\n    plain(3)\n}\n",
			want:   []string{"listOf", "plain"},
			absent: []string{"map{it}.size", "map"}},

		{lang: LangSwift, name: "swift_optional_chain", path: "a/O.swift",
			src:    "func call(o: Any?) {\n    o?.count()\n}\n",
			want:   []string{"o.count"},
			absent: []string{"o?.count"}},

		{lang: LangSwift, name: "swift_non_null_assertion", path: "a/N.swift",
			src:    "func call(o: Any?) {\n    o!.count()\n}\n",
			want:   []string{"o.count"},
			absent: []string{"o!.count"}},

		// THE DECLARED SIBLING `size()` IS LOAD-BEARING and must not be tidied
		// away: with it in scope the stranded bare `size` binds to it as a BOUND
		// edge, which is the fabrication this fixture pins. `plain` is in want so
		// the non-empty assertion cannot be satisfied by a fixture that emits
		// nothing at all. Do NOT also assert on `o?.a.b()`: that emits the
		// QUALIFIED `a.b`, which both declines exclude by design.
		{lang: LangGroovy, name: "groovy_optional_receiver", path: "a/G.groovy",
			src:    "class G {\n    def size() { }\n    def go(o) {\n        o?.size()\n        plain(3)\n    }\n}\n",
			want:   []string{"plain"},
			absent: []string{"size"}},

		// THE INTERMEDIATE HOP IS ITS OWN CALLEE: this grammar emits `o&.a`
		// alongside the full `o&.a&.b`, so the two-hop shape produces TWO edges
		// and a want list missing `o.a` would be silent about half the repair.
		// `arr.map(&:to_s)` IS THE COLLATERAL-DAMAGE CONTROL — the block-pass
		// `&` sits in ARGUMENT position, and its presence in want proves the
		// operator drop did not reach into it.
		{lang: LangRuby, name: "ruby_safe_navigation", path: "a/sn.rb",
			src:    "def call(o, arr)\n  o&.size\n  o&.a&.b\n  arr.map(&:to_s)\n  plain(1)\nend\n",
			want:   []string{"o.size", "o.a", "o.a.b", "arr.map", "plain"},
			absent: []string{"o&.size", "o&.a", "o&.a&.b"}},

		// THE SINGLE-HOP CASE EMITS NOTHING BEFORE AND AFTER, and it is in the
		// src rather than omitted so a later reader does not assume it was
		// regressed here; `absent: m` pins that it does not start emitting a
		// bare name either. The `?` here is followed by `-`, never by `.`, which
		// is why the operator drop needed a per-language follow set.
		{lang: LangPHP, name: "php_nullsafe_chain", path: "a/ns.php",
			src:    "<?php\nfunction call($o) {\n    $o?->m();\n    $o?->a->b();\n    $o->m2();\n    plain(2);\n}\n",
			want:   []string{"$o->a->b", "$o->m2", "plain"},
			absent: []string{"$o?->a->b", "m"}},

		// RED-FIRST, NOT A CHARACTERIZATION GUARD. Shell has no profile row, but
		// the delimiter cut is UNCONDITIONAL, so its new quote-awareness changes
		// this emission: the old `}"` is the command word sliced at the `]`
		// INSIDE a quoted expansion, and the new output is the command word as
		// written — still unbindable, but truthful rather than mangled. THE SRC
		// IS SYNTHETIC; the shape is what matters.
		{lang: LangBash, name: "bash_bracket_command_word", path: "a/b.sh",
			allowDelims: true,
			src:         "run_it() {\n  \"${BASH_SOURCE[0]}\" --check\n  \"${kube[@]}\" get pods\n}\n",
			want:        []string{"\"${BASH_SOURCE[0]}\"", "\"${kube[@]}\""},
			absent:      []string{"}\""}},
	}
}

// calleeProtectionFixtures are CHARACTERIZATION GUARDS, labeled honestly:
// every want below is what the CURRENT tree emits, they are green before the
// normalization lands and must stay green after it, and nothing here is
// red-first. Their whole value is in the failure direction — they are the only
// thing in the repository that goes red if the normalization is applied without
// a per-language opt-in.
//
// A candidate applying the whole guard GLOBALLY measured clean on both corpora
// and kept the entire treesitter and parser suites green, while LOSING Elixir's
// `Map.has_key?`, `File.read!` and `Enum.empty?`, losing EVERY one of Ruby's
// `x.empty?`, `x.save!` and `x.nil?` (that source emitted no callees at all),
// and corrupting the shell `${CMD}` into `$` while dropping `"$BIN"`,
// `./local.sh` and `/usr/bin/env`. Corpus scale is not corpus coverage; only a
// fixture chosen from the language's GRAMMAR closes this.
//
// THE NAMED CATCHER PER FIXTURE, which specific omission each one fails on:
//
//	elixir_predicate_and_bang_names fails if Elixir is given a NON-EMPTY
//	ChainOps, or loses NameExtra "?!". Elixir's ChainOps stays empty precisely
//	because any `?` or `!` in it destroys these names.
//
//	ruby_predicate_and_bang_names fails if Ruby's ChainOps contains `?` or `!`,
//	or loses NameExtra "?!". It does NOT fail on Ruby's ACTUAL ChainOps, which
//	is `&`: the drop looks for a run of `&` followed by `.`, and none of these
//	three names contains a `&` at all. Under a GLOBAL guard this source emits no
//	callees whatsoever, so the non-empty assertion fires first.
//
//	bash_command_words fails if shell is given any gated knob at all.
//
// A NOTE FOR A LATER READER: these three assert callees containing `?`, `!`,
// `{`, `}`, `"`, `$` and `/`. The blanket loop in TestQualifiedCalleeCapture
// forbids `(`, `)`, `[`, `]`, newlines and surrounding whitespace — nothing
// else — so all three pass untouched. Do NOT widen those assertions.
func calleeProtectionFixtures() []calleeFixture {
	return []calleeFixture{
		// These are legitimate Elixir names, not chain operators.
		{lang: LangElixir, name: "elixir_predicate_and_bang_names", path: "a/p.ex",
			src:  "defmodule P do\n  def go(m) do\n    Map.has_key?(m, :a)\n    File.read!(\"p\")\n    Enum.empty?()\n  end\nend\n",
			want: []string{"Map.has_key?", "File.read!", "Enum.empty?"}},

		// Ruby is ALSO a composed-span grammar — its Calls query binds a
		// receiver capture and a method capture that are joined into one span —
		// so this fixture is simultaneously the protection case and a
		// composed-span case. NONE of these three is a chained call, so the
		// chained-tail decline never reaches them.
		{lang: LangRuby, name: "ruby_predicate_and_bang_names", path: "a/p.rb",
			src:  "class C\n  def go(x)\n    x.empty?\n    x.save!\n    x.nil?\n  end\nend\n",
			want: []string{"x.empty?", "x.save!", "x.nil?"}},

		// Braces, quotes, dots and slashes are ordinary characters in a shell
		// command word and the chunker cannot tell a mangled one from a real
		// one, which is why shell takes no gated knob and why the corpus census
		// LOGS it rather than asserting on it. NONE of these five shapes carries
		// a bracket, which is exactly why they are byte-identical before and
		// after and why the bracket case needed its own red-first fixture.
		{lang: LangBash, name: "bash_command_words", path: "a/c.sh",
			src:  "run_it() {\n  ${CMD} arg\n  \"$BIN\" arg\n  cmd-with-dash\n  . ./lib.sh\n  /usr/bin/env ls\n}\n",
			want: []string{"${CMD}", "\"$BIN\"", "cmd-with-dash", ".", "/usr/bin/env"}},
	}
}
