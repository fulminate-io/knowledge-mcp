// SPDX-License-Identifier: Apache-2.0

package recipe

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestParse_WhereTreePredicates drives the grammar change end to end: the new
// form parses, the retired form is refused with a repairable message, strict
// decode rejects a typo at BOTH levels, and the two shapes the lexer's
// look-behind must NOT capture still parse as they always did.
func TestParse_WhereTreePredicates(t *testing.T) {
	t.Run("select_where_tree", func(t *testing.T) {
		r, err := Parse([]byte(`select section where {
    "all": [
        {"kind": {"of": "node", "is": ["section", "block"]}},
        {"not": {"exists": {"of": "section.metadata.draft"}}}
    ]
}
emit pattern {
    name := section.symbol_name
}
`))
		require.NoError(t, err)
		sel, ok := r.Rules[0].(RuleSelect)
		require.True(t, ok, "the first rule is a select")
		require.NotNil(t, sel.Where)
		require.Len(t, sel.Where.All, 2)
		require.NotNil(t, sel.Where.All[0].Kind)
		assert.Equal(t, "node", sel.Where.All[0].Kind.Of)
		assert.Equal(t, []string{"section", "block"}, sel.Where.All[0].Kind.Is,
			"a list `is` decodes to every member")
		require.NotNil(t, sel.Where.All[1].Not)
		require.NotNil(t, sel.Where.All[1].Not.Exists)
		assert.Equal(t, "section.metadata.draft", sel.Where.All[1].Not.Exists.Of)
	})

	t.Run("filter_where_tree", func(t *testing.T) {
		r, err := Parse([]byte(`select section
filter {"any": [
    {"matches": {"of": "section.symbol_name", "regex": "^Ch"}},
    {"equals": {"of": "node.type", "value": "section"}}
]}
emit pattern {
    name := section.symbol_name
}
`))
		require.NoError(t, err)
		f, ok := r.Rules[1].(RuleFilter)
		require.True(t, ok, "the second rule is a filter")
		require.NotNil(t, f.Where, "a filter always carries a tree; the parser requires it")
		require.Len(t, f.Where.Any, 2)
		assert.Equal(t, "^Ch", f.Where.Any[0].Matches.Regex)
		assert.Equal(t, "section", f.Where.Any[1].Equals.Value)
	})

	t.Run("kind_leaf_accepts_a_bare_string", func(t *testing.T) {
		// The string-or-array unmarshaler's other arm; without it, `"is":"section"`
		// fails to decode and the two spellings are not interchangeable.
		r, err := Parse([]byte(`select section where {"kind": {"of": "node", "is": "section"}}
emit pattern {
    name := section.symbol_name
}
`))
		require.NoError(t, err)
		sel := r.Rules[0].(RuleSelect)
		assert.Equal(t, []string{"section"}, sel.Where.Kind.Is)
	})

	t.Run("legacy_select_where_refused", func(t *testing.T) {
		_, err := Parse([]byte("select section where section.symbol_name ~= /^Ch/\n"))
		require.Error(t, err)
		msg := err.Error()
		assert.Contains(t, msg, `must be a JSON where-tree in braces`)
		assert.Contains(t, msg, `got "section"`, "the offending text is named")
		assert.Contains(t, msg, "kind{of,is}", "and the grammar that replaced it")
		assert.Contains(t, msg, `help("recipes")`)
	})

	t.Run("legacy_filter_refused", func(t *testing.T) {
		_, err := Parse([]byte("select section\nfilter section.symbol_name ~= /^[A-Z]/\n"))
		require.Error(t, err)
		msg := err.Error()
		assert.Contains(t, msg, "parse error at 2:8:", "cited at the offending token")
		assert.Contains(t, msg, `predicate after 'filter' must be a JSON where-tree in braces`)
		assert.Contains(t, msg, `got "section"`)
		assert.Contains(t, msg, "ancestor{edge,where}")
	})

	t.Run("brace_after_emit_type_is_not_a_where_tree", func(t *testing.T) {
		// The wrong-but-compiling lexer treats EVERY brace as a span; this emit
		// block then vanishes into one raw token and the recipe stops parsing.
		r, err := Parse([]byte(`select section
emit pattern {
    type := "pattern"
    name := section.symbol_name
}
`))
		require.NoError(t, err)
		e, ok := r.Rules[1].(RuleEmit)
		require.True(t, ok)
		assert.Len(t, e.Fields, 2, "the emit block is still a field map, not a raw span")
	})

	t.Run("field_named_where_is_not_a_where_tree", func(t *testing.T) {
		// A look-behind keyed on the WORD alone, without checking that the next
		// byte is a brace, would misfire here.
		r, err := Parse([]byte(`select section
emit pattern {
    name := section.symbol_name
    where := "somewhere"
}
`))
		require.NoError(t, err)
		e := r.Rules[1].(RuleEmit)
		assert.Contains(t, e.Fields, "where")
	})

	t.Run("unterminated_where_tree", func(t *testing.T) {
		// The span opens on line 2 at column 8. An error citing EOF would leave the
		// author to find the start of the tree themselves.
		_, err := Parse([]byte("select section\nfilter {\"matches\": {\"of\": \"node.name\"\n"))
		require.Error(t, err)
		msg := err.Error()
		assert.Contains(t, msg, "unterminated where-tree")
		assert.Contains(t, msg, "'{' at 2:8", "the OPENING brace is cited, not EOF")
	})

	t.Run("unknown_composer_key", func(t *testing.T) {
		_, err := Parse([]byte(`select section
filter {"al": [{"exists": {"of": "node.name"}}]}
`))
		require.Error(t, err)
		msg := err.Error()
		assert.Contains(t, msg, `unknown field "al"`, "the offending key is named")
		assert.Contains(t, msg, "Composers: all, any, not.", "and the accepted set")
		assert.Contains(t, msg, "refused before any row was read")
	})

	t.Run("unknown_key_inside_a_leaf", func(t *testing.T) {
		// WITHOUT PER-LEAF STRICTNESS THIS PARSES CLEANLY and the typo vanishes,
		// which is the silent-authoring class the whole ticket exists to close.
		_, err := Parse([]byte(`select section
filter {"kind": {"of": "node", "is": "section", "typo": 1}}
`))
		require.Error(t, err)
		assert.Contains(t, err.Error(), `unknown field "typo"`)
	})

	t.Run("emit_without_name_or_identity_is_a_parse_error", func(t *testing.T) {
		// PARSE AND NOTHING ELSE: no sourceView, no Interpret. An implementation
		// that places this check behind Interpret returns no error here — and the
		// three documentation gates, which also call Parse alone, would ship a
		// broken example.
		_, err := Parse([]byte(`select section
emit pattern {
    summary := section.symbol_name
}
`))
		require.Error(t, err)
		msg := err.Error()
		assert.Contains(t, msg, "must set 'name' or 'identity'")
		assert.Contains(t, msg, "emit pattern")
	})

	t.Run("emit_with_only_identity_parses", func(t *testing.T) {
		// The control: at least one of the two is required, not both. A check
		// demanding both reds here against correct work.
		_, err := Parse([]byte(`select section
emit pattern {
    identity := section.symbol_name
    summary := section.summary
}
`))
		require.NoError(t, err)
	})

	t.Run("ancestor_sub_tree_head_scope", func(t *testing.T) {
		// Inside an ancestor sub-tree the row is a walked neighbor, so `node` is
		// the only legal bare head and the select type is not.
		_, err := Parse([]byte(`select section
filter {"ancestor": {"edge": "CONTAINS", "where": {"matches": {"of": "node.symbol_name", "regex": "^Part"}}}}
`))
		require.NoError(t, err, "node is legal inside the sub-tree")

		_, err = Parse([]byte(`select section
filter {"ancestor": {"edge": "CONTAINS", "where": {"matches": {"of": "section.symbol_name", "regex": "^Part"}}}}
`))
		require.Error(t, err, "the select type names the outer row, not the walked neighbor")
		assert.Contains(t, err.Error(), `unknown field head "section"`)
	})
}

// TestLex_WhereTreeSpan pins the one lexing case a naive depth counter gets
// wrong: a brace inside a JSON string literal is data, not structure.
func TestLex_WhereTreeSpan(t *testing.T) {
	t.Run("brace_inside_a_string_does_not_close_the_span", func(t *testing.T) {
		src := []byte(`select section
filter {"equals": {"of": "node.name", "value": "}"}}
traverse CONTAINS out
`)
		toks, err := Lex(src)
		require.NoError(t, err)

		var spans []Token
		for _, tk := range toks {
			if tk.Kind == TokWhereJSON {
				spans = append(spans, tk)
			}
		}
		require.Len(t, spans, 1, "exactly one where-tree span")
		assert.JSONEq(t, `{"equals": {"of": "node.name", "value": "}"}}`, spans[0].Value,
			"the span runs to the REAL closing brace, and carries its braces undecoded")

		// The stream continues normally afterwards, which a span that closed early
		// would break.
		var idents []string
		for _, tk := range toks {
			if tk.Kind == TokIdent {
				idents = append(idents, tk.Value)
			}
		}
		assert.Contains(t, idents, "traverse")
		assert.Contains(t, idents, "CONTAINS")
	})

	t.Run("escaped_quote_inside_a_string", func(t *testing.T) {
		src := []byte(`select section
filter {"matches": {"of": "node.name", "regex": "\"quoted\"}"}}
`)
		toks, err := Lex(src)
		require.NoError(t, err)
		var span string
		for _, tk := range toks {
			if tk.Kind == TokWhereJSON {
				span = tk.Value
			}
		}
		require.NotEmpty(t, span)
		assert.True(t, strings.HasSuffix(span, "}}"), "the span closed at its matching brace: %s", span)
		assert.Contains(t, span, `\"quoted\"`, "escapes reach the JSON decoder undecoded")
	})

	t.Run("unterminated_string_inside_the_span", func(t *testing.T) {
		_, err := Lex([]byte("select section\nfilter {\"equals\": {\"of\": \"node.name\n"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unterminated")
		assert.Contains(t, err.Error(), "2:8", "cited at the opening brace")
	})
}
