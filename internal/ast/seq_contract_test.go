// SPDX-License-Identifier: Apache-2.0

// seq_contract_test.go — the executable $$$SEQ per-position contract table.
// The rows live in seq_contract_rows_test.go; this file holds the row shape
// and the runner.
//
// WHAT THE TABLE ASSERTS. wantText / wantChildKinds are always the CONTRACT
// values, never the current behavior:
//
//   - $$$SEQ binds zero-or-more consecutive siblings.
//   - Children carries SEMANTIC SIBLINGS ONLY. Anonymous separators — the
//     commas between parameters or arguments, the semicolons between
//     statements — are EXCLUDED from Children.
//   - Text is the VERBATIM SOURCE SPAN from the first matched sibling's start
//     byte to the last one's end byte, so the separators ARE present in Text.
//     That is what makes a seq capture re-interpolate as valid source.
//   - Container delimiters — an argument list's parens, a block's braces —
//     are in NEITHER Text nor Children, because the container is not a
//     sibling.
//   - The empty sequence binds Text "" and no children.
//
// So for `func $N($$$P)` against `func two(a int, b string)`: Text is
// "a int, b string" (comma included) and Children is two parameter_declaration
// entries (comma excluded).
//
// INVERTED xfail SEMANTICS — this is what makes the table falsifiable in both
// directions. A row with xfail=="" asserts the engine PRODUCES the contract
// capture. A row with xfail!="" asserts the engine does NOT produce it. An
// xfail row that starts passing FAILS this test and forces the marker's
// removal, so a partial fix can never land unrecorded. The table shipped with
// nine markers over the regimes true sequence semantics had not reached yet;
// it now carries NONE, so every row below asserts the contract directly and
// the mechanism stands ready for the next row that outruns the engine.
//
// WHY EVERY BODY / PARAMETER POSITION CARRIES ARITY 0, 1 AND 2. The
// single-bind defect wears a disguise at arity 1: a degenerate bind of a
// one-statement body descends the single-child target block and reports
// exactly the capture a correct implementation would. Only arity 2 separates
// them, and only arity 0 proves the empty sequence. A probe that used a
// one-statement fixture recorded a false pass.
//
// PERF SHAPE: serial by design. Each row parses one small in-memory snippet
// and runs one walk; there is no shared expensive setup to amortize and no
// row is long enough to dominate. Parallelism would buy nothing and would
// cost a per-row tree-sitter parser, so none is used.

package ast

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// seqContractRow is one measured cell of the $$$SEQ contract.
//
// capture names the ONE seq placeholder the row asserts on. It exists
// because the mandated patterns bind two seq captures at once (a parameter
// position and a body position in the same pattern), and a row has to say
// which of them it is measuring without the reader having to infer it from
// the row name.
//
// lang duplicates cfg.Lang on purpose: it is the row's own declaration of
// what it believes it is testing, and the runner cross-checks the two so a
// copy-pasted row that kept the wrong LangConfig fails loudly instead of
// quietly measuring another grammar.
type seqContractRow struct {
	name           string
	lang           treesitter.Language
	cfg            LangConfig
	pattern        string
	source         string
	capture        string
	wantText       string
	wantChildKinds []string
	xfail          string
}

// TestSeqContractTable runs every contract cell. Contract rows must be
// satisfied; xfail rows must NOT be satisfied.
func TestSeqContractTable(t *testing.T) {
	for _, row := range seqContractRows {
		t.Run(row.name, func(t *testing.T) {
			if row.cfg.Lang != row.lang {
				t.Fatalf("row declares lang=%q but carries the LangConfig for %q", row.lang, row.cfg.Lang)
			}
			matches := runLongTailWalker(t, row.cfg, row.pattern, row.source)
			satisfied, observed := seqContractHolds(matches, row)
			switch {
			case row.xfail == "" && !satisfied:
				t.Errorf("contract row does not hold.\n"+
					"  pattern:  %q\n"+
					"  source:   %q\n"+
					"  capture:  $$$%s\n"+
					"  want:     text=%q kinds=%v\n"+
					"  observed: %s",
					row.pattern, row.source, row.capture, row.wantText, row.wantChildKinds, observed)
			case row.xfail != "" && satisfied:
				t.Errorf("xfail row now SATISFIES the contract — delete its xfail marker so the "+
					"row stands as a contract row.\n"+
					"  pattern: %q\n"+
					"  source:  %q\n"+
					"  xfail said: %s",
					row.pattern, row.source, row.xfail)
			case row.xfail != "":
				// Record what the engine ACTUALLY does at this position. An
				// xfail row is only as good as the reason attached to it, and
				// a reason nobody can check against an observation is how a
				// row ends up red for a reason other than the one claimed.
				t.Logf("xfail as declared: %s\n  want:     text=%q kinds=%v\n  observed: %s",
					row.xfail, row.wantText, row.wantChildKinds, observed)
			}
		})
	}
}

// seqContractHolds reports whether ANY match binds row.capture to exactly the
// contract text and child kinds, and returns a rendering of every observed
// binding for the failure message. A row with no match at all does not hold —
// which is the correct reading for the degenerate positions where the engine
// fails to match rather than mis-capturing.
func seqContractHolds(matches []walkerMatch, row seqContractRow) (bool, string) {
	if len(matches) == 0 {
		return false, "no match at all"
	}
	seen := make([]string, 0, len(matches))
	for _, m := range matches {
		capture, ok := m.captures[row.capture]
		if !ok {
			seen = append(seen, fmt.Sprintf("outer=%s <no $$$%s binding>", m.outer, row.capture))
			continue
		}
		kinds := seqChildKinds(capture)
		seen = append(seen, fmt.Sprintf("outer=%s text=%q kinds=%v", m.outer, capture.Text, kinds))
		if capture.Text == row.wantText && slices.Equal(kinds, row.wantChildKinds) {
			return true, strings.Join(seen, "; ")
		}
	}
	return false, strings.Join(seen, "; ")
}

// seqChildKinds projects a sequence capture's per-sibling views down to their
// tree-sitter kinds, in order.
func seqChildKinds(c Capture) []string {
	out := make([]string, 0, len(c.Children))
	for _, ch := range c.Children {
		out = append(out, ch.Kind)
	}
	return out
}
