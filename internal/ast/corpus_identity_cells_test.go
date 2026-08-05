// SPDX-License-Identifier: Apache-2.0

// corpus_identity_cells_test.go — the census cell table: which fixture repo
// and which bounded subtree each registered language is measured against,
// which probes run there, and the verdict each probe is currently expected to
// produce. The invariant, the census format and the runner all live in
// corpus_identity_test.go.
//
// wantVerdict IS THE ANCHOR, AND EVERY VALUE BELOW IS MEASURED, NOT CHOSEN.
// Each one records what the engine does today, so this table is green on
// landing and red the moment behavior moves in either direction. A cell that
// starts satisfying the invariant is exactly as red as one that stops, until
// its wantVerdict is flipped — and that flip, visible in the diff, IS the
// record of the fix. The counts stood at 25 verdictViolation, 16 verdictOK and
// 2 verdictSkip against the frozen baseline artifact; they read 20 / 21 / 2
// once the anonymous-token-aware matcher stopped five single-line probes from
// matching the constructs whose rewrite corrupted them, 7 / 34 / 2 once
// replacement became source-anchored, 3 / 38 / 2 once $$$SEQ bound siblings
// rather than containers, 0 / 41 / 2 once the splice consumed a template token
// repeating one the match dropped, 0 / 42 / 1 once Elm compiled at all, and back
// to 0 / 41 / 2 once the declined-file guard demoted the two cells that splice
// nothing — bash's zsh-syntax `echo` row and groovy's unparsed multi-line `if`
// row — from a hollow OK to a reasoned SKIP. Those twenty cells flipped
// with their match counts UNCHANGED to the unit — measured cell by cell against
// a census taken at the previous commit — which is what distinguishes a real
// fix from a probe that went green by matching less.
//
// NO verdictViolation REMAINS, and that is the acceptance invariant met rather
// than an expectation lowered: while any cell declared a VIOLATION as expected,
// this table was asserting that a corruption SURVIVES. The last three to flip
// were the C, Java and PHP probes, every one spelled `if ($C) { $$$B; }` — with
// a trailing SEMICOLON inside the pattern's block. That semicolon interposes the
// statement wrapper the sequence is promoted through, so matching drops it along
// with the wrapper and it earns no alignment entry; the identity template still
// carried it and re-emitted it beside a capture whose statements already end in
// one, turning `g(a);` into `g(a);;`. The read path could not repair it — the
// capture is correct and the $$$SEQ contract table pins it that way (its C rows
// require `int a;` including the semicolon) — so the fix is on the write side:
// RawMatch.DroppedSpans records what a promotion threw away and splice.go
// consumes a template token that repeats one instead of emitting it.
//
// THE DECLINED-FILE GUARD MAKES THE CENSUS HONEST ABOUT WHAT IT DID NOT MEASURE.
// evaluateIdentity used to reach OK off an empty diff, but a file that was
// refused (overlap), rejected (the edit broke it) or already ungrammatical
// produces no diff, so a cell that spliced NOTHING read as a clean OK. The guard
// now reads all three decline lists, demotes a zero-spliced cell to a reasoned
// SKIP, treats any rejected file as a VIOLATION, and appends a spliced=/
// spliced_matches=/refused=/rejected=/preexisting= accounting to every row that
// declined something — so an OK now means every file actually spliced round-trips
// byte-identically, and the census can no longer hide a cell that measured little.
//
// THE ONLY LEGITIMATE RESIDUE IS A REASONED SKIP, and there are two, both grammar
// gaps the guard now surfaces rather than hides: bash's single-line `echo` row
// (its matches sit only in oh-my-zsh's zsh-syntax files, none of which the bash
// grammar parses) and groovy's multi-line `if` row (every match under its
// integTest subtree is in an unparsed file). Each carries the runner's own
// measured zero-spliced reason. Elm's multi-line row, once a skip, now evaluates
// on the `$N $P = $E` respelling; cpp was re-scoped off its unparsed header
// library onto tests/src/ so both its probes evaluate again. A skip with an empty
// reason is a cell hidden rather than fixed, and a criterion forbids it.
//
// The baseline file itself is never edited to follow — it is frozen so the
// evaluated-cell floor and the overwrite alarm keep working.
//
// EVERY REGISTERED LANGUAGE HAS A CELL. An absent cell and a passing cell must
// never look alike, so a language with no usable fixture or no reachable
// position is recorded as an explicit SKIP with its reason, never omitted.
//
// EVERY PREFIX IS BOUNDED AND ENDS IN A SLASH. Several fixture repos are
// hundreds of megabytes; an unscoped walk makes the harness unusable. The
// trailing slash matters because prefix filtering is a bare string-prefix test
// with no separator boundary.

package ast

import (
	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

var identityCells = []identityCell{
	{
		// The multi-line probe is the function-body sequence position. It is
		// spelled with a newline-separated body rather than a `;`-terminated
		// one because the matcher compares anonymous tokens: a pattern
		// carrying `;` requires the target to carry one too, and no function
		// under this prefix is written that way, which left the probe
		// matching nothing. The newline-separated spelling measures the same
		// position and finds nine multi-line matches here.
		lang:   treesitter.LangBash,
		repo:   "bash-ohmyzsh",
		prefix: "lib/",
		patterns: []identityPattern{
			// DECLARED SKIP. `echo $X` matches only inside oh-my-zsh's zsh-syntax
			// files, all of which the bash grammar leaves ungrammatical (measured:
			// every candidate a pre-existing parse failure, zero spliced), so the
			// declined-file guard reports a reasoned SKIP rather than a hollow OK.
			// Neither remedy applies: re-scoping the cell would drag the working
			// multi-line sibling below off lib/, and no single-line respell lands
			// in the few files here the bash grammar can parse. The multi-line
			// sibling keeps this cell measuring.
			{pattern: "echo $X", shape: shapeSingleLine, wantVerdict: verdictSkip},
			{pattern: "function $N() {\n  $$$B\n}", shape: shapeMultiLine, wantVerdict: verdictOK},
		},
	},
	{
		lang:   treesitter.LangC,
		repo:   "c-redis",
		prefix: "src/",
		patterns: []identityPattern{
			{pattern: "return $X;", shape: shapeSingleLine, wantVerdict: verdictOK},
			{pattern: "if ($C) { $$$B; }", shape: shapeMultiLine, wantVerdict: verdictOK},
		},
	},
	{
		lang: treesitter.LangCPP,
		repo: "cpp-json",
		// RE-SCOPED from include/ to tests/src/. nlohmann's include/ is the
		// template-heavy header library and the cpp grammar leaves nearly all of
		// it ungrammatical, so BOTH probes spliced zero there — the cell had been
		// measuring nothing while the census hid it behind an empty-diff OK. The
		// declined-file guard exposed that as a SKIP; re-scoping to the test
		// sources, where real .cpp translation units parse, lifts both probes back
		// to an evaluated identity round-trip (return splices several files clean,
		// if splices at least one), so the cell measures the reflow invariant on
		// real cpp again instead of certifying an unparsed subtree.
		prefix: "tests/src/",
		patterns: []identityPattern{
			{pattern: "return $X;", shape: shapeSingleLine, wantVerdict: verdictOK},
			{pattern: "if ($C) { $$$B; }", shape: shapeMultiLine, wantVerdict: verdictOK},
		},
	},
	{
		lang: treesitter.LangCSharp,
		repo: "cs-roslyn",
		// C# gets statement-expression probes rather than a block-sequence
		// one: its grammar admits only invocation, assignment, increment,
		// new and await as statement expressions, so a bare seq-placeholder
		// identifier inside a block never parses under any wrapper. One
		// pattern serves both shapes here, which is why the shape is part of
		// a row's key.
		prefix: "src/Compilers/Core/Portable/Syntax/",
		patterns: []identityPattern{
			{pattern: "$A = $B;", shape: shapeSingleLine, wantVerdict: verdictOK},
			{pattern: "$A = $B;", shape: shapeMultiLine, wantVerdict: verdictOK},
		},
	},
	{
		lang:   treesitter.LangElixir,
		repo:   "ex-phoenix",
		prefix: "lib/",
		patterns: []identityPattern{
			{pattern: "$F($X)", shape: shapeSingleLine, wantVerdict: verdictOK},
			{pattern: "def $N($$$P) do\n  $$$B\nend", shape: shapeMultiLine, wantVerdict: verdictOK},
		},
	},
	{
		// Elm was the one registered language with NO evaluable cell, and the
		// reason was structural rather than a missing fixture: the shared
		// reserved placeholder prefix begins with an underscore, which no Elm
		// identifier may, so every substituted pattern parsed as an
		// anything-pattern instead of a name and nothing compiled under either
		// context wrapper. elmLangConfig now carries its own lowercase reserved
		// prefix, and the single-line row is evaluated like every other
		// language's: it compiles under the expression wrapper to a
		// function_call_expr and round-trips 270 matches identically.
		//
		// THE MULTI-LINE ROW IS RESPELLED, and the spelling it landed on is
		// load-bearing rather than incidental. The row previously probed
		// `let $N = $E`, which measured nothing: Elm has no top-level `let`, so
		// the grammar took the keyword as the declared NAME and `$N` as its
		// parameter, leaving a function_declaration_left with two children
		// where a real let binding has one. It matched none of this subtree's
		// let bindings, which do exist and are multi-line.
		//
		// WHY `$N $P = $E` AND NOT `$N = $E`. The obvious respell — a binding
		// with no keyword — SELF-NESTS: a top-level declaration and the
		// let-bound declarations inside it both match, so the matches overlap
		// and the splice refuses the whole file. Measured over reactor/src/ via
		// a dry-run identity replace: `$N = $E` finds 151 matches but refuses 6
		// of 7 files and splices only NotFound.elm (2 matches), while
		// `$N $P = $E` — a declaration with exactly one parameter, which a
		// let-bound simple value does not have — finds 33 matches, refuses
		// nothing, and splices all 33 across 5 files with every diff empty.
		//
		// THE HARNESS NOW DISCLOSES REFUSALS, but the spelling still has to carry
		// the difference. Since the declined-file guard landed, evaluateIdentity
		// reads res.RefusedFiles / res.RejectedFiles / res.PreexistingParseFailures
		// and every row accounts for what it declined, so the self-nesting spelling
		// would no longer read as a silent clean OK — it would ship OK carrying
		// `refused=6`. It is still the WRONG probe: it measures ONE file
		// (NotFound.elm) while its row advertises a three-figure match count, which
		// is this file's own "a cell that exercised almost nothing is not a clean
		// pass" hazard reappearing through refusals instead of through zero matches.
		// Any probe in any language that self-nests has the same problem — do not
		// pick one.
		lang:   treesitter.LangElm,
		repo:   "elm-compiler",
		prefix: "reactor/src/",
		patterns: []identityPattern{
			{pattern: "$F $X", shape: shapeSingleLine, wantVerdict: verdictOK},
			{pattern: "$N $P = $E", shape: shapeMultiLine, wantVerdict: verdictOK},
		},
	},
	{
		lang:   treesitter.LangGo,
		repo:   "go-kubernetes",
		prefix: "pkg/scheduler/",
		patterns: []identityPattern{
			{pattern: "$F($X)", shape: shapeSingleLine, wantVerdict: verdictOK},
			{pattern: "if $C { $$$B }", shape: shapeMultiLine, wantVerdict: verdictOK},
		},
	},
	{
		lang:   treesitter.LangGroovy,
		repo:   "groovy-gradle",
		prefix: "subprojects/core/src/integTest/groovy/org/gradle/api/tasks/",
		patterns: []identityPattern{
			{pattern: "$F($X)", shape: shapeSingleLine, wantVerdict: verdictOK},
			// DECLARED SKIP. Every multi-line `if` under this integTest subtree
			// sits in a file the groovy grammar cannot parse (measured: all
			// candidates pre-existing parse failures, zero spliced), so the guard
			// reports a reasoned SKIP rather than a hollow OK off an empty diff.
			// The single-line sibling above still splices real files and keeps the
			// cell measuring; re-scoping the whole cell to chase this one probe
			// would move that working measurement, and no multi-line respell lands
			// in the handful of files here the grammar parses.
			{pattern: "if ($C) { $$$B }", shape: shapeMultiLine, wantVerdict: verdictSkip},
		},
	},
	{
		lang:   treesitter.LangJava,
		repo:   "java-spring",
		prefix: "core/spring-boot/src/main/java/org/springframework/boot/context/",
		patterns: []identityPattern{
			{pattern: "$F($X);", shape: shapeSingleLine, wantVerdict: verdictOK},
			{pattern: "if ($C) { $$$B; }", shape: shapeMultiLine, wantVerdict: verdictOK},
		},
	},
	{
		lang:   treesitter.LangJavaScript,
		repo:   "js-react",
		prefix: "packages/react/src/",
		patterns: []identityPattern{
			{pattern: "$F($X)", shape: shapeSingleLine, wantVerdict: verdictOK},
			{pattern: "if ($C) { $$$B }", shape: shapeMultiLine, wantVerdict: verdictOK},
		},
	},
	{
		lang:   treesitter.LangKotlin,
		repo:   "kt-okhttp",
		prefix: "okhttp/src/commonJvmAndroid/kotlin/",
		patterns: []identityPattern{
			{pattern: "$F($X)", shape: shapeSingleLine, wantVerdict: verdictOK},
			{pattern: "if ($C) { $$$B }", shape: shapeMultiLine, wantVerdict: verdictOK},
		},
	},
	{
		// THE FROZEN BASELINE'S TWO LUA ROWS ARE CONTAMINATED, and the file is
		// deliberately NOT edited to follow. identity_baseline.txt line 23
		// (`$F($X)` single-line matches=79 OK) and line 24 (`if $C then/$$$B/end`
		// multi-line matches=30 VIOLATION) were both noise, not measurements:
		// they were measured under the lua scanner race — the vendored grammar's
		// external scanner keeps its lexer state in process-global C variables, so
		// concurrent lua parses corrupted each other. Only lua is affected; the
		// baseline's numbers for every other language stand.
		//
		// This cell now reads matches=108 OK and matches=69 OK for the same two
		// probes. That jump is NOT an engine improvement between the two runs — it
		// is the difference between a raced measurement and a serialized one, now
		// that Parser.Parse holds every lua parse behind luaParseMu. So the
		// header's twenty-cell "flipped with match counts UNCHANGED to the unit"
		// argument — the thing that distinguishes a real fix from a probe that
		// went green by matching less — does NOT hold for these two lua rows, and
		// was never claimed to.
		//
		// The baseline stays byte-identical because it does two jobs that only
		// work while it is never edited: it is the evaluated-cell floor the
		// regenerated census is compared against, and it is the overwrite alarm
		// that a separate gate asserts is untouched. Editing it — even to add a
		// line — would move the counts those gates read.
		lang:   treesitter.LangLua,
		repo:   "lua-openresty",
		prefix: "t/lib/",
		patterns: []identityPattern{
			{pattern: "$F($X)", shape: shapeSingleLine, wantVerdict: verdictOK},
			{pattern: "if $C then\n$$$B\nend", shape: shapeMultiLine, wantVerdict: verdictOK},
		},
	},
	{
		lang:   treesitter.LangOCaml,
		repo:   "ocaml-dune",
		prefix: "src/dune_lang/",
		patterns: []identityPattern{
			{pattern: "$F $X", shape: shapeSingleLine, wantVerdict: verdictOK},
			{pattern: "let $N = $E", shape: shapeMultiLine, wantVerdict: verdictOK},
		},
	},
	{
		lang:   treesitter.LangPython,
		repo:   "py-django",
		prefix: "django/core/",
		patterns: []identityPattern{
			{pattern: "$F($X)", shape: shapeSingleLine, wantVerdict: verdictOK},
			{pattern: "def $N():\n    $$$B", shape: shapeMultiLine, wantVerdict: verdictOK},
		},
	},
	{
		lang:   treesitter.LangRuby,
		repo:   "rb-rails",
		prefix: "activesupport/lib/",
		patterns: []identityPattern{
			{pattern: "$F($X)", shape: shapeSingleLine, wantVerdict: verdictOK},
			{pattern: "def $N\n  $$$B\nend", shape: shapeMultiLine, wantVerdict: verdictOK},
		},
	},
	{
		lang:   treesitter.LangRust,
		repo:   "rust-tokio",
		prefix: "tokio/src/",
		patterns: []identityPattern{
			{pattern: "$F($X)", shape: shapeSingleLine, wantVerdict: verdictOK},
			{pattern: "if $C { $$$B }", shape: shapeMultiLine, wantVerdict: verdictOK},
		},
	},
	{
		lang:   treesitter.LangScala,
		repo:   "scala-akka",
		prefix: "akka-actor/src/main/",
		patterns: []identityPattern{
			{pattern: "$F($X)", shape: shapeSingleLine, wantVerdict: verdictOK},
			{pattern: "if ($C) { $$$B }", shape: shapeMultiLine, wantVerdict: verdictOK},
		},
	},
	{
		lang:   treesitter.LangSwift,
		repo:   "swift-vapor",
		prefix: "Sources/",
		patterns: []identityPattern{
			{pattern: "$F($X)", shape: shapeSingleLine, wantVerdict: verdictOK},
			{pattern: "if $C { $$$B }", shape: shapeMultiLine, wantVerdict: verdictOK},
		},
	},
	{
		// .tsx rides a separate grammar from .ts, so its cell is a separate
		// fixture subtree rather than a second probe on the TypeScript one.
		lang:   treesitter.LangTSX,
		repo:   "js-react",
		prefix: "fixtures/flight-parcel/src/",
		patterns: []identityPattern{
			{pattern: "$F($X)", shape: shapeSingleLine, wantVerdict: verdictOK},
			{pattern: "if ($C) { $$$B }", shape: shapeMultiLine, wantVerdict: verdictOK},
		},
	},
	{
		// The interface-property probes are MANDATORY and must stay evaluated.
		// The class-body member position is what is unreachable under the
		// current context wrappers; the interface-property position is not,
		// and it reproduces a live corruption — an identity template there
		// silently turns an optional property into a required one. A
		// TypeScript cell that were only a SKIP would hide exactly that.
		lang:   treesitter.LangTypeScript,
		repo:   "ts-vscode",
		prefix: "src/vs/base/common/",
		patterns: []identityPattern{
			{pattern: "$F($X)", shape: shapeSingleLine, wantVerdict: verdictOK},
			{pattern: "interface $I { $N?: $T; }", shape: shapeMultiLine, wantVerdict: verdictOK},
			{pattern: "interface $I { $N: $T; }", shape: shapeMultiLine, wantVerdict: verdictOK},
		},
	},
}
