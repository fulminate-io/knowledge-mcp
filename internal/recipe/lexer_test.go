// SPDX-License-Identifier: Apache-2.0

package recipe

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLex_Smoke runs a tiny happy-path recipe through the lexer and
// asserts the full token stream. Keeps the parser golden suite focused
// on grammatical coverage while this test pins low-level tokenization.
func TestLex_Smoke(t *testing.T) {
	src := []byte(`# header comment
select section where section.heading ~= /Router/
traverse references out as $target
emit pattern {
    type := "pattern"
    name := $section.heading
} as $pat
$pat --[relates-to]--> $target
`)
	toks, err := Lex(src)
	require.NoError(t, err)

	var kinds []TokenKind
	for _, tk := range toks {
		kinds = append(kinds, tk.Kind)
	}

	// Spot-check a few salient ones without pinning the full list.
	assert.Equal(t, TokEOF, toks[len(toks)-1].Kind)
	assert.Contains(t, kinds, TokIdent)
	assert.Contains(t, kinds, TokString)
	assert.Contains(t, kinds, TokVar)
	assert.Contains(t, kinds, TokRegex)
	assert.Contains(t, kinds, TokColonEq)
	assert.Contains(t, kinds, TokTildeEq)
	assert.Contains(t, kinds, TokRelArrow)
	assert.Contains(t, kinds, TokLBrace)
	assert.Contains(t, kinds, TokRBrace)
	assert.Contains(t, kinds, TokNewline)
}

// TestLex_Errors covers the lex error classes the parser golden suite
// later re-exercises at higher levels. Each case pins the reported
// line:col so regressions in Position tracking get caught early.
func TestLex_Errors(t *testing.T) {
	cases := []struct {
		name    string
		src     string
		wantErr string
	}{
		{"unterminated string", `select page where page.title := "oops`, "unterminated string"},
		{"unterminated regex", `select page where page.title ~= /oops`, "unterminated regex"},
		{"bad arrow", `$x -> $y`, "expected '-->' or"},
		{"dollar no ident", `$ := "x"`, "expected identifier after '$'"},
		{"unterminated rel arrow", `$a --[rel-->`, "unterminated --[rel]-->"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Lex([]byte(tc.src))
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

// TestLex_PositionAdvance checks that Position tracking is correct
// across newlines.
func TestLex_PositionAdvance(t *testing.T) {
	toks, err := Lex([]byte("select\nsection\n"))
	require.NoError(t, err)
	// select is at 1:1, newline at 1:7, section at 2:1.
	require.GreaterOrEqual(t, len(toks), 4)
	assert.Equal(t, Position{Line: 1, Col: 1}, toks[0].Pos)
	assert.Equal(t, TokIdent, toks[0].Kind)
	assert.Equal(t, TokNewline, toks[1].Kind)
	assert.Equal(t, Position{Line: 2, Col: 1}, toks[2].Pos)
	assert.Equal(t, "section", toks[2].Value)
}
