// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"context"
	"testing"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/stretchr/testify/require"
)

// goRegularSymbolsNamed returns every REGULAR symbol id the Go grammar spells
// with one kind name.
//
// It returns the whole set rather than the first match, because the set is the
// subject: a kind name maps to several symbols and a table built from only one
// of them is wrong in a way no runtime observation on today's corpus reveals.
func goRegularSymbolsNamed(grammar *sitter.Language, name string) []sitter.Symbol {
	var out []sitter.Symbol
	for i := range int(grammar.SymbolCount()) {
		s := sitter.Symbol(uint16(i)) //nolint:gosec // i is bounded by SymbolCount
		if grammar.SymbolType(s) != sitter.SymbolTypeRegular {
			continue
		}
		if grammar.SymbolName(s) == name {
			out = append(out, s)
		}
	}
	return out
}

// TestSymbolClassesCoverEveryGrammarSpelling is the catcher for the
// first-match builder: it requires the class table to classify EVERY regular
// symbol the grammar spells with a covered kind name, not just the first.
//
// IT IS THE ONLY THING THAT CATCHES THAT DEFECT TODAY. Across a 1,430-file
// measurement only symbol 1 ever surfaces for "identifier", and 1 is the first
// of that name's three symbols, so a [0]-only builder returns the right answer
// at runtime on this corpus while leaving symbols 60 and 61 unclassified. The
// wrong answer appears the moment the grammar's own routing changes, which is
// too late to discover it — so the assertion is over the grammar's declared
// spellings rather than over observed nodes.
func TestSymbolClassesCoverEveryGrammarSpelling(t *testing.T) {
	grammar, ok := LanguageGrammar(LangGo)
	require.True(t, ok, "the Go grammar must be registered")
	require.NotNil(t, grammar)

	table := goKinds()
	require.NotEmpty(t, table,
		"the class table is empty: newSymbolClasses must return a populated table or panic, never a table that silently classifies nothing")

	coveredMulti := 0
	for name, code := range goKindNames {
		symbols := goRegularSymbolsNamed(grammar, name)
		// KNOWN-POSITIVE CONTROL per kind: a zero here means the class map
		// names a kind this grammar does not declare — a typo — and without
		// this check the per-symbol loop below would iterate nothing and pass.
		require.NotEmptyf(t, symbols,
			"the Go grammar declares no regular symbol named %q, so the class map names a kind that cannot exist", name)
		if len(symbols) > 1 {
			coveredMulti++
		}
		for _, s := range symbols {
			require.Equalf(t, code, table.class(s),
				"symbol %d is spelled %q but classifies as %d instead of %d — every symbol of a multiply-mapped name must carry the class, so a builder that stopped at the first match is caught here",
				s, name, table.class(s), code)
		}
	}

	// ANTI-VACUITY CONTROL. If no covered kind name maps to more than one
	// symbol, the loop above cannot distinguish a correct builder from a
	// first-match one and this test is quietly proving nothing. It FAILS LOUDLY
	// in that case rather than going vacuous — a re-vendored grammar that
	// flattened the multiplicity is a fact worth stopping on.
	require.Positivef(t, coveredMulti,
		"no kind name covered by the class map maps to more than one regular symbol, so this test can no longer distinguish a correct builder from a first-match one; the grammar's multiplicity changed and this control is the notification")

	grammarMulti := map[string]int{}
	for i := range int(grammar.SymbolCount()) {
		s := sitter.Symbol(uint16(i)) //nolint:gosec // i is bounded by SymbolCount
		if grammar.SymbolType(s) != sitter.SymbolTypeRegular {
			continue
		}
		grammarMulti[grammar.SymbolName(s)]++
	}
	multiNames := 0
	for _, n := range grammarMulti {
		if n > 1 {
			multiNames++
		}
	}
	require.Positivef(t, multiNames,
		"the Go grammar declares no multiply-mapped regular kind name at all, which contradicts the measurement this table's set semantics rest on")
	t.Logf("go grammar: %d symbols, %d distinct regular names, %d multiply-mapped, %d of them covered by the class map",
		grammar.SymbolCount(), len(grammarMulti), multiNames, coveredMulti)
}

// TestSymbolClassesPanicsRatherThanDegrading is the gate on newSymbolClasses'
// declared failure mode: it returns a NON-EMPTY table or panics, and never
// returns nil or a partial one.
//
// NOTHING ELSE IN THIS PACKAGE CAN FIRE THAT BRANCH, which is why it needs its
// own test. Every other caller reaches the builder through goKinds(), which
// passes LangGo and goKindNames — a language whose grammar is always registered
// in the test binary and a kind map whose every name the grammar declares. A
// regression that made the builder return nil on a bad input would therefore
// still hand back a full, correct table on every existing code path, and the
// whole suite would stay green while the guarantee was gone.
//
// WHY THE GUARANTEE IS WORTH A TEST AT ALL: goKinds memoizes with sync.Once, so
// a degraded table returned once is cached for the life of the process. class()
// would then answer goKindOther for every symbol, the arm would bind nothing,
// and every fixture-constructing unit test in this package would still pass,
// because they build their own inputs rather than reading the table.
//
// The third panic branch — a grammar reporting zero symbols — is deliberately
// NOT covered here and this is not an oversight: no registered grammar reports
// zero, and reaching it would need a fake *sitter.Language the vendored binding
// gives no way to construct. It is stated rather than silently skipped.
func TestSymbolClassesPanicsRatherThanDegrading(t *testing.T) {
	cases := []struct {
		name  string
		lang  Language
		kinds map[string]uint8
	}{
		{
			// A kind name the grammar does not declare is a typo in the class
			// map, not a grammar the table should quietly under-cover.
			name:  "kind name the grammar does not declare",
			lang:  LangGo,
			kinds: map[string]uint8{"no_such_node_kind_exists": goKindIdentifier},
		},
		{
			// LangUnknown is deliberately absent from the language registry, so
			// LanguageGrammar reports it missing. Building a class table for a
			// language with no grammar is a programming error.
			name:  "language with no registered grammar",
			lang:  LangUnknown,
			kinds: goKindNames,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Panics(t, func() {
				_ = newSymbolClasses(tc.lang, tc.kinds)
			}, "newSymbolClasses must panic rather than return a nil or degraded table")
		})
	}

	// KNOWN-POSITIVE CONTROL. Without it, a newSymbolClasses that panicked
	// unconditionally — on every input, valid or not — would satisfy both cases
	// above and this test would be asserting nothing about the discrimination
	// it exists to check.
	require.NotPanics(t, func() {
		table := newSymbolClasses(LangGo, goKindNames)
		require.NotEmpty(t, table, "the valid build must return a populated table")
	}, "the production inputs must build cleanly, or the cases above prove nothing")
}

// TestSymbolClassesToleratesErrorNodes is the catcher for an unbounded index in
// class().
//
// THE PRE-EXISTING SUITE IS NO SUBSTITUTE, and that was traced rather than
// assumed. TestChunkSyntaxError (chunker_test.go) uses `func incomplete( {`,
// which tree-sitter recovers with a MISSING ")" node carrying an in-range
// symbol — that fixture holds ZERO out-of-range symbols and cannot reach the
// panic. The bare `if` below produces a real ERROR node, symbol 65535 against a
// 217-entry table, sitting INSIDE the declaration subtree the qualifier walk
// descends.
func TestSymbolClassesToleratesErrorNodes(t *testing.T) {
	const src = `package p

func h() { if }
`
	c := NewChunker()
	t.Cleanup(c.Close)

	res, err := c.ChunkFile(context.Background(), "error_node_fixture.go", []byte(src))
	require.NoError(t, err)
	require.NotEmptyf(t, res.Chunks,
		"control: the error-tolerant parse produced no chunks, so this fixture would prove nothing about what the arms do with an ERROR node")
}

// TestF0KindTableMemo covers the generic per-language memo, with Go as its
// KNOWN-POSITIVE CONTROL.
//
// A generic helper with no production caller is a helper nothing proves, which
// is why goKinds is reimplemented on this type rather than left beside it: the
// go_control subtest reads the PRODUCTION path, so the memo cannot pass here
// while being wrong for the only language that uses it.
func TestF0KindTableMemo(t *testing.T) {
	t.Run("go_control", func(t *testing.T) {
		// The production accessor, not a locally-built instance: this is what
		// makes Go the helper's known-positive control rather than a fixture
		// that happens to resemble one.
		table := goKinds()
		require.NotEmpty(t, table,
			"the production Go table is empty, so every later assertion about classification would be vacuous")

		grammar, ok := LanguageGrammar(LangGo)
		require.True(t, ok, "the Go grammar must be registered")
		symbols := goRegularSymbolsNamed(grammar, "identifier")
		require.NotEmpty(t, symbols,
			"control: the grammar declares no regular symbol named \"identifier\", so the classification check below would iterate nothing")
		for _, s := range symbols {
			require.Equal(t, goKindIdentifier, table.class(s),
				"the memoized production table must classify every \"identifier\" symbol as goKindIdentifier")
		}
	})

	t.Run("second_call_returns_same_table", func(t *testing.T) {
		// A fresh instance rather than the production one, so this subtest
		// measures the memo's own behavior instead of an already-warm cache.
		tbl := kindTable{lang: LangGo, names: goKindNames}
		first := tbl.get()
		second := tbl.get()
		require.NotEmpty(t, first, "control: an empty first build would make the identity comparison below meaningless")
		require.Equal(t, &first[0], &second[0],
			"get() must return the memoized table rather than rebuilding it, so both calls must share one backing array")
	})

	t.Run("unknown_language_panics", func(t *testing.T) {
		// LangUnknown is deliberately absent from the language registry, so this
		// exercises the first of newSymbolClasses' two panic paths — no grammar
		// registered — through the memo rather than around it.
		tbl := kindTable{lang: LangUnknown, names: goKindNames}
		require.Panics(t, func() {
			_ = tbl.get()
		}, "get() must propagate newSymbolClasses' panic rather than memoizing a degraded table")
	})
}
