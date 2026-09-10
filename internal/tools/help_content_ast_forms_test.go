// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// help_content_ast_forms_test.go pins the three where-tree forms help("ast")
// owes a WORKED EXAMPLE each: the namespaced sub-pattern capture, the
// value-precise flows_to destination, and the Go import-spec pattern.
//
// EACH FORM IS ASSERTED BY ITS OWN PHRASE PLUS ITS OWN EXAMPLE FRAGMENT, never
// by a count of three: a topic that documented one form three times would
// satisfy a count while teaching one thing. The example fragment is the half
// that matters — a reader who cannot copy a call has not been given a form.
//
// THE THIRD FORM SHIPS NO NEW SYNTAX, which is exactly why it is asserted here.
// flows_to's value-precise destination changes what a `to` capture REACHES and
// nothing about how one is spelled, so a reader cannot infer it from the
// grammar and the example is the only place it exists.
func TestHelpAst_DocumentsTheThreeWhereTreeFormsWithAWorkedExampleEach(t *testing.T) {
	// Resolved through the dispatch rather than read off the constant, so a
	// topic split across two files that failed to concatenate fails here.
	res := handleHelpClient(json.RawMessage(`{"topic":"ast"}`))
	require.False(t, res.IsError, "the ast topic must resolve: %s", res.Content[0].Text)
	body := res.Content[0].Text
	require.NotEmpty(t, body)

	for _, form := range []struct {
		name    string
		rule    string
		example string
	}{
		{
			name:    "the namespaced sub-pattern capture",
			rule:    `"<as>.<capture>" — a sub-pattern leaf's OWN capture`,
			example: `"to": "T.C"`,
		},
		{
			name:    "the value-precise flows_to destination",
			rule:    "flows_to destinations are VALUE-precise",
			example: `"pattern": "&$MP.CommandTransport{Command: $C, $$$REST}"`,
		},
		{
			name:    "the Go import-spec pattern",
			rule:    "Importing a package: the import-spec form (Go)",
			example: `"$$$P \"os/exec\""`,
		},
	} {
		t.Run(form.name, func(t *testing.T) {
			assert.Contains(t, body, form.rule, "the topic must state this form")
			assert.Contains(t, body, form.example, "the topic must carry a copyable example of this form")
		})
	}

	// THE TWO LIMITS EACH FORM OWES, because a form documented without its
	// limit is the one a reader ships wrong. The export does not backtrack and
	// a leaf with no `as` exports nothing; the import form does not fix the
	// kind-leaf residual.
	for _, limit := range []string{
		"A leaf with NO 'as' declares no namespace and exports nothing",
		"binds the FIRST matching candidate and does not backtrack",
		"still binds only the specs that carry a LOCAL NAME",
		"the walk is intra-declaration",
	} {
		assert.Contains(t, body, limit, "each form must ship with the limit a reader would otherwise discover in production")
	}

	// THE TWO-LEVEL CAPTURE REFERENCE, corrected by this ticket. The topic
	// shipped "$outer.outer.X" as the depth-two spelling and that reference does
	// not resolve — resolveCapture strips one WHOLE "$outer." token at a time.
	// The negative keeps the refuted wording from returning as an unqualified
	// claim; the positive keeps a later rewrite from satisfying the negative by
	// deleting the paragraph.
	assert.Contains(t, body, `"$outer.$outer.X" walks two levels`,
		"the topic must give the spelling that actually resolves")
	assert.Equal(t, 1, strings.Count(body, `"$outer.outer.X"`),
		`"$outer.outer.X" may appear only once, in the sentence saying it is NOT two levels`)
	assert.Contains(t, body, `("$outer.outer.X" is NOT two`,
		"the refuted spelling must be named as refuted rather than silently dropped")
}
