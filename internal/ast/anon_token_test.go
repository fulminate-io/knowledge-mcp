// SPDX-License-Identifier: Apache-2.0

// anon_token_test.go — the anonymous-token discrimination table.
//
// WHAT IT ASSERTS. tree-sitter splits a node's children into NAMED children
// (the grammar's structural fields) and ANONYMOUS ones (operators, modifiers,
// declaration keywords, punctuation, channel direction, async/readonly/`?`).
// A matcher that compares only named children is blind to every anonymous
// token, in BOTH directions: an anonymous token in the PATTERN fails to
// constrain, and an anonymous token in the SOURCE fails to exclude. Each row
// below pins one position where that blindness produced a wrong answer, and
// asserts the discriminated result.
//
// WHY THE ROWS ASSERT A CAPTURE-TEXT SET AND NOT A MATCH COUNT. The test
// walkers run matchTree at every named node, so a count is inflated by nested
// matches and says nothing about WHICH targets matched. A row names one
// capture and declares the exact set of texts it must bind across the whole
// fixture — over-match and under-match are then both visible, and a row that
// stopped matching anything cannot pass.
//
// THE JSX ROW IS NOT A REPRO. jsx attribute discrimination is already correct
// (the attributed element carries an extra named child, so the bare pattern
// already fails to align). It is here as the must-not-regress control: making
// anonymous tokens visible must not turn a correct discrimination into an
// over-constraint that stops matching legitimate targets.
//
// PERF SHAPE: serial, mirroring seq_contract_test.go. Each row parses one
// small in-memory snippet and runs one walk; there is nothing to amortize.

package ast

import (
	"slices"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// anonTokenRow is one discrimination cell.
//
// capture names the placeholder whose bound texts identify the matched
// targets. wantTexts is the exact set (deduplicated, sorted) that capture must
// bind across every match in the fixture — an empty want means the pattern
// must match nothing, which is only ever used alongside a sibling row that
// proves the same fixture is reachable.
type anonTokenRow struct {
	name      string
	lang      treesitter.Language
	cfg       LangConfig
	pattern   string
	source    string
	capture   string
	wantTexts []string
}

// TestAnonTokenDiscrimination runs every discrimination cell.
func TestAnonTokenDiscrimination(t *testing.T) {
	for _, row := range anonTokenRows {
		t.Run(row.name, func(t *testing.T) {
			if row.cfg.Lang != row.lang {
				t.Fatalf("row declares lang=%q but carries the LangConfig for %q", row.lang, row.cfg.Lang)
			}
			matches := runLongTailWalker(t, row.cfg, row.pattern, row.source)
			got := capturedTexts(matches, row.capture)
			if !slices.Equal(got, row.wantTexts) {
				t.Errorf("$%s bound %v, want %v\n  pattern: %q\n  source:  %q",
					row.capture, got, row.wantTexts, row.pattern, row.source)
			}
		})
	}
}

// capturedTexts collects the deduplicated, sorted set of texts bound to
// capture across every match.
func capturedTexts(matches []walkerMatch, capture string) []string {
	seen := map[string]bool{}
	for _, m := range matches {
		if c, ok := m.captures[capture]; ok {
			seen[c.Text] = true
		}
	}
	out := make([]string, 0, len(seen))
	for text := range seen {
		out = append(out, text)
	}
	slices.Sort(out)
	return out
}

var anonTokenRows = []anonTokenRow{
	// ---- Go binary operators ----------------------------------------------
	// The operator is an anonymous child of binary_expression, so every
	// binary_expression shares one named-child shape. Distinct operands per
	// expression are what make the bound set name the matched target.
	{
		name:    "go_operator_plus",
		lang:    treesitter.LangGo,
		cfg:     goLangConfig,
		pattern: "$A + $B",
		source: "package p\n" +
			"func f() {\n" +
			"\t_ = plusL + plusR\n" +
			"\t_ = minusL - minusR\n" +
			"\t_ = mulL * mulR\n" +
			"\t_ = eqL == eqR\n" +
			"}\n",
		capture:   "A",
		wantTexts: []string{"plusL"},
	},

	// ---- Go nil checks ----------------------------------------------------
	// != and == differ only in an anonymous token; the two rows together are
	// what prove the hit sets are DIFFERENT rather than merely non-empty.
	{
		name:    "go_nil_check_ne",
		lang:    treesitter.LangGo,
		cfg:     goLangConfig,
		pattern: "if $E != nil { $$$B }",
		source: "package p\n" +
			"func f(neErr, eqErr error) {\n" +
			"\tif neErr != nil {\n\t\tone()\n\t}\n" +
			"\tif eqErr == nil {\n\t\ttwo()\n\t}\n" +
			"}\n",
		capture:   "E",
		wantTexts: []string{"neErr"},
	},
	{
		name:    "go_nil_check_eq",
		lang:    treesitter.LangGo,
		cfg:     goLangConfig,
		pattern: "if $E == nil { $$$B }",
		source: "package p\n" +
			"func f(neErr, eqErr error) {\n" +
			"\tif neErr != nil {\n\t\tone()\n\t}\n" +
			"\tif eqErr == nil {\n\t\ttwo()\n\t}\n" +
			"}\n",
		capture:   "E",
		wantTexts: []string{"eqErr"},
	},

	// ---- Go channel direction ---------------------------------------------
	// chan<- / <-chan / chan differ only in the anonymous direction token, and
	// the three rows share one fixture so each one's want set is the other
	// two's exclusion.
	{
		name:      "go_chan_send_only",
		lang:      treesitter.LangGo,
		cfg:       goLangConfig,
		pattern:   "func $N(c chan<- int) { $$$B }",
		source:    goChanFixture,
		capture:   "N",
		wantTexts: []string{"sendOnly"},
	},
	{
		name:      "go_chan_recv_only",
		lang:      treesitter.LangGo,
		cfg:       goLangConfig,
		pattern:   "func $N(c <-chan int) { $$$B }",
		source:    goChanFixture,
		capture:   "N",
		wantTexts: []string{"recvOnly"},
	},
	{
		name:      "go_chan_bidirectional",
		lang:      treesitter.LangGo,
		cfg:       goLangConfig,
		pattern:   "func $N(c chan int) { $$$B }",
		source:    goChanFixture,
		capture:   "N",
		wantTexts: []string{"bidi"},
	},

	// ---- Java modifiers ---------------------------------------------------
	// The trigger case: a `modifiers` node holding only keywords has zero
	// named children, so the leaf-content compare used to discriminate it by
	// accident. Add an annotation and `modifiers` gains a named child, the
	// content compare is skipped, and the pattern widens to every method.
	{
		name:    "java_annotated_modifiers",
		lang:    treesitter.LangJava,
		cfg:     javaLangConfig,
		pattern: "@Ann private void $N() { x(); }",
		source: "class C {\n" +
			"  @Ann private void alpha() { x(); }\n" +
			"  @Ann public void beta() { x(); }\n" +
			"  @Ann void gamma() { x(); }\n" +
			"  @Ann static final synchronized void delta() { x(); }\n" +
			"}\n",
		capture:   "N",
		wantTexts: []string{"alpha"},
	},

	// ---- Python async def -------------------------------------------------
	{
		name:    "python_async_def",
		lang:    treesitter.LangPython,
		cfg:     pythonLangConfig,
		pattern: "async def $N():\n    $$$B",
		source: "def plain():\n    pass\n\n" +
			"async def coro():\n    pass\n",
		capture:   "N",
		wantTexts: []string{"coro"},
	},

	// ---- Bash declaration keywords ----------------------------------------
	// local / export / declare / readonly are all declaration_command with the
	// keyword as an anonymous first child, so they share one named-child shape.
	{
		name:    "bash_local_keyword",
		lang:    treesitter.LangBash,
		cfg:     bashLangConfig,
		pattern: "local $X=$Y",
		source: "f() {\n" +
			"  local loc=1\n" +
			"  export exp=2\n" +
			"  declare dec=3\n" +
			"  readonly ro=4\n" +
			"}\n",
		capture:   "X",
		wantTexts: []string{"loc"},
	},

	// ---- TypeScript async, the widening direction -------------------------
	// The mirror of TestTypeScript_FunctionDeclaration: there a pattern
	// WITHOUT `async` must not match an async declaration; here a pattern
	// WITH `async` must not match a plain one. Both directions matter — an
	// engine could fix one by ignoring the token on the other side.
	{
		name:    "ts_async_pattern_excludes_plain",
		lang:    treesitter.LangTypeScript,
		cfg:     tsLangConfig,
		pattern: "async function $N($$$ARGS) { $$$BODY }",
		source: "async function asyncFn(id) { return id; }\n" +
			"function plainFn(x) { return x; }\n",
		capture:   "N",
		wantTexts: []string{"asyncFn"},
	},

	// ---- TypeScript interface property, the three-way ---------------------
	// readonly is an anonymous modifier and `?` an anonymous optionality
	// token, so all three property shapes share one named-child shape. The
	// write-side identity gate cannot stand in for these: under a
	// source-anchored splice an over-matching pattern still produces an empty
	// diff, so read-side blindness here would never surface downstream.
	{
		name:      "ts_interface_optional_property",
		lang:      treesitter.LangTypeScript,
		cfg:       tsLangConfig,
		pattern:   "interface $I { $N?: $T; }",
		source:    tsInterfaceFixture,
		capture:   "I",
		wantTexts: []string{"Optional"},
	},
	{
		name:      "ts_interface_plain_property",
		lang:      treesitter.LangTypeScript,
		cfg:       tsLangConfig,
		pattern:   "interface $I { $N: $T; }",
		source:    tsInterfaceFixture,
		capture:   "I",
		wantTexts: []string{"Plain"},
	},
	{
		name:      "ts_interface_readonly_property",
		lang:      treesitter.LangTypeScript,
		cfg:       tsLangConfig,
		pattern:   "interface $I { readonly $N: $T; }",
		source:    tsInterfaceFixture,
		capture:   "I",
		wantTexts: []string{"ReadOnly"},
	},

	// ---- JSX must-not-regress ---------------------------------------------
	// Green before this phase and green after. The attributed element carries
	// an extra named child so the bare pattern already fails to align; the row
	// exists to catch the opposite failure — a bare element that stops
	// matching because attribute punctuation became an over-constraint.
	{
		name:    "tsx_bare_div_still_matches",
		lang:    treesitter.LangTSX,
		cfg:     tsxLangConfig,
		pattern: "<div>{$C}</div>",
		source: "function App() {\n  return <div className=\"x\">{attributed}</div>;\n}\n" +
			"function Bare() {\n  return <div>{plain}</div>;\n}\n",
		capture:   "C",
		wantTexts: []string{"plain"},
	},
}

// goChanFixture carries all three channel directions so each direction row's
// want set is the other two rows' exclusion.
const goChanFixture = "package p\n" +
	"func sendOnly(c chan<- int) { use(c) }\n" +
	"func recvOnly(c <-chan int) { use(c) }\n" +
	"func bidi(c chan int) { use(c) }\n"

// tsInterfaceFixture carries the optional, plain and readonly property shapes
// in one file so the three interface rows partition it.
const tsInterfaceFixture = "interface Optional { o?: number; }\n" +
	"interface Plain { p: number; }\n" +
	"interface ReadOnly { readonly r: number; }\n"
