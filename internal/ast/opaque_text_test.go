// SPDX-License-Identifier: Apache-2.0

// opaque_text_test.go — the regression gate for the span-gap literal defect:
// an inlined string literal written into a pattern must CONSTRAIN, so a
// pattern's literal value can never behave as a wildcard.
//
// THE DEFECT THIS PINS. matchNode text-compares only nodes with no children of
// any kind. Several grammars parse a literal into a node that HAS children (its
// delimiters, its escape sequences) while the literal's own content bytes sit in
// a byte-span gap no child covers — so the matcher recursed past the content and
// never read it. Every one of those content bytes was a wildcard, and a replace
// then substituted the template's literal over a target whose literal differed:
// a wrong rewrite that parses fine, which the re-parse gate cannot catch and the
// dry-run diff renders as though intended.
//
// THE CONTROL SHAPE, and why all four legs are here rather than just the first.
// A test whose pass condition is a ZERO is indistinguishable from a probe
// pointed at nothing, so the nonsense-literal leg is never asserted alone:
//   - inline nonsense literal must count 0 (the defect: it counted every call),
//   - inline REAL literal must count exactly 1 (the known positive proving the
//     zero above is a refusal and not a dead pattern),
//   - capture + where over the same nonsense regex must count 0 (the where path
//     was always correct — it is the workaround the defect's ticket documented,
//     and it must stay correct),
//   - capture + where over regex "." must count every call (the known positive
//     for the where leg).

package ast

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// opaqueGoFixture carries three fmt.Errorf calls whose format literals all
// differ. Every leg below counts against a fixture-derived constant rather than
// a number derived the same way the measurement is, so a walk that silently
// stopped seeing calls fails the known-positive legs.
const opaqueGoFixture = `package p

import "fmt"

func a(e error) error { return fmt.Errorf("alpha %w", e) }
func b(e error) error { return fmt.Errorf("beta %w", e) }
func c(e error) error { return fmt.Errorf("gamma %w", e) }
`

// opaqueGoFixtureCalls is the fixture-derived constant: the number of
// fmt.Errorf calls written into opaqueGoFixture above.
const opaqueGoFixtureCalls = 3

// TestInlineLiteral_ValueConstrains is the four-leg control. The first leg is
// the defect's own reproduction: before the fix it matched all three calls.
func TestInlineLiteral_ValueConstrains(t *testing.T) {
	t.Run("inline_nonsense_literal_matches_nothing", func(t *testing.T) {
		matches := runLongTailWalker(t, goLangConfig,
			`fmt.Errorf("zzz-appears-nowhere-zzz", $$$ARGS)`, opaqueGoFixture)
		assert.Empty(t, matches,
			"a literal that appears nowhere in the fixture must constrain the match to zero; matching here means the literal's content bytes were never compared")
	})

	t.Run("inline_real_literal_matches_exactly_its_own_call", func(t *testing.T) {
		matches := runLongTailWalker(t, goLangConfig,
			`fmt.Errorf("beta %w", $$$ARGS)`, opaqueGoFixture)
		require.Len(t, matches, 1,
			"known positive: the literal that IS in the fixture must match its own call and only its own call — without this the zero above could be a dead pattern")
	})

	t.Run("capture_plus_where_nonsense_regex_matches_nothing", func(t *testing.T) {
		got, err := runWhere(t, `fmt.Errorf($FMT, $$$ARGS)`, opaqueGoFixture,
			`{"matches": {"of": "FMT", "regex": "zzz-appears-nowhere-zzz"}}`)
		require.NoError(t, err)
		assert.Zero(t, got, "the where path constrains the captured literal and must stay correct")
	})

	t.Run("capture_plus_where_dot_regex_matches_every_call", func(t *testing.T) {
		got, err := runWhere(t, `fmt.Errorf($FMT, $$$ARGS)`, opaqueGoFixture,
			`{"matches": {"of": "FMT", "regex": "."}}`)
		require.NoError(t, err)
		assert.Equal(t, opaqueGoFixtureCalls, got,
			"known positive for the where leg: a regex matching anything must reach every call, so the zero above is a true zero")
	})
}

// TestInlineLiteral_ReplaceLeavesADifferingLiteralAlone reproduces the incident
// shape the defect was reported from: a replace sweep keyed on one literal
// rewrote a call whose literal was DIFFERENT, because the pattern matched it as
// a wildcard and the template then substituted its own literal over the target's.
//
// WHY THIS NEEDS ITS OWN TEST RATHER THAN RESTING ON THE MATCH-SIDE ONES. The
// re-parse gate cannot catch this class: the wrong rewrite is grammatical, so it
// parses cleanly and the dry-run diff renders it as though intended. Match-side
// coverage proves the matcher no longer over-matches; only driving the real
// replace end to end proves nothing downstream re-widens it.
//
// The apply is REAL, not a dry run, and the assertions are made against the bytes
// on disk — a dry run would leave the file untouched whatever the matcher did,
// which is the one outcome this test must not be able to confuse with success.
func TestInlineLiteral_ReplaceLeavesADifferingLiteralAlone(t *testing.T) {
	const target = `package p

func a() { seed("checks/go", 1) }
func b() { seed("practice/go", 2) }
`
	dir := fixtureRepo(t, map[string]string{"seed.go": target})

	pat, err := Parse(`seed("checks/go", $N)`)
	require.NoError(t, err)
	cp, err := Compile(pat, treesitter.LangGo, "")
	require.NoError(t, err)
	defer cp.Close()

	raws, _, err := Match(context.Background(), dir, treesitter.LangGo, cp, nil, Scope{})
	require.NoError(t, err)
	require.Len(t, raws, 1,
		"known positive: the pattern must reach the checks/go call and ONLY it — two matches is the defect, and zero would make the disk assertions below pass vacuously over an untouched file")

	res, err := ApplyReplace(context.Background(), dir, treesitter.LangGo, raws, `seed("checks", $N)`, false, nil)
	require.NoError(t, err)
	require.True(t, res.Applied)
	assert.Equal(t, 1, res.MatchesReplaced, "exactly one call may be rewritten")

	onDisk, err := os.ReadFile(filepath.Join(dir, "seed.go")) //nolint:gosec // test fixture path
	require.NoError(t, err)
	got := string(onDisk)

	assert.Contains(t, got, `seed("checks", 1)`,
		"the call whose literal the pattern named must be rewritten")
	assert.Contains(t, got, `seed("practice/go", 2)`,
		"the call whose literal DIFFERS must survive byte for byte — rewriting it is the silent corruption this test exists for")
	assert.Equal(t, 1, strings.Count(got, `seed("checks", `),
		"only one call may carry the replacement literal")
}

// TestPlaceholderInsideOpaqueText_IsRefusedAtCompile pins that a placeholder
// written INSIDE a span-gap literal fails the compile with a message naming the
// placeholder, rather than compiling to a pattern that can never match.
//
// THIS IS THE OTHER HALF OF THE FIX, and without it the change would swap one
// silent wrong answer for another. Before, `zz("$X")` matched every zz call with
// a string argument and bound nothing; a whole-text comparison alone would make
// it match NOTHING while still reporting a successful compile — a zero
// indistinguishable from a pattern that correctly found no sites.
func TestPlaceholderInsideOpaqueText_IsRefusedAtCompile(t *testing.T) {
	t.Run("go_placeholder_inside_a_string_literal", func(t *testing.T) {
		pat, err := Parse(`zz("$X")`)
		require.NoError(t, err, "the DSL parses it; the refusal belongs to the compile")
		cp, err := Compile(pat, treesitter.LangGo, "")
		if cp != nil {
			cp.Close()
		}
		require.Error(t, err)
		assert.Contains(t, err.Error(), "$X", "the refusal must name the placeholder as the caller spelled it")
		assert.Contains(t, err.Error(), "interpreted_string_literal", "the refusal must name the kind that cannot host it")
		assert.Contains(t, err.Error(), "where-tree", "the refusal must name the way forward")
	})

	t.Run("the_same_shape_without_the_placeholder_compiles", func(t *testing.T) {
		// KNOWN POSITIVE. Without it the assertions above would pass just as well
		// against a compile that rejected every pattern carrying a string literal.
		pat, err := Parse(`zz("literal")`)
		require.NoError(t, err)
		cp, err := Compile(pat, treesitter.LangGo, "")
		require.NoError(t, err, "an inlined literal with no placeholder inside it must still compile")
		cp.Close()
	})

	t.Run("a_placeholder_that_IS_the_whole_content_node_still_compiles_and_matches", func(t *testing.T) {
		// Python surfaces a string's content as its own string_content node, so
		// `zz("$X")` there is a placeholder POSITION rather than a placeholder
		// buried inside a literal, and matchNode binds it before the opaque
		// comparison is reached. Refusing it would remove a capability this change
		// has no reason to touch.
		pat, err := Parse(`zz("$X")`)
		require.NoError(t, err)
		cp, err := Compile(pat, treesitter.LangPython, "")
		require.NoError(t, err, "a placeholder occupying a whole content node is a bindable position, not a fragment inside a literal")
		defer cp.Close()

		dir := fixtureRepo(t, map[string]string{"m.py": "zz(\"alpha\")\n"})
		raws, _, err := Match(context.Background(), dir, treesitter.LangPython, cp, nil, Scope{})
		require.NoError(t, err)
		require.Len(t, raws, 1,
			"and it must still MATCH — a compile that succeeds over a pattern that can never match is exactly the silent zero this refusal exists to prevent")
	})
}
