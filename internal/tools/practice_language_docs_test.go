// SPDX-License-Identifier: Apache-2.0

package tools

// practice_language_docs_test.go — the rendered help topics and the tool-schema
// param descriptions carry no per-language practice graph and no legacy
// selector.
//
// IT RENDERS THE OUTPUT RATHER THAN READING THE CONSTANTS. help() answers from
// helpTopics through handleHelpClient, and the schemas answer through the
// ToolDef builders, so a sentence removed from one constant and left in a
// sibling that the same topic splices in would pass a constant-level assertion
// and still reach a caller. The census below drives the surfaces a caller
// actually reads.
//
// THE FORBIDDEN PHRASES ARE THE CLAIM, and each one is a sentence a caller could
// act on: that `language` selects a practice graph, that the pre-singleton
// graphs are reachable, or that a practice search requires a language. A bare
// occurrence of the word "language" is NOT the claim — the param is live for the
// corpus checks, for ast and for the topology analyzers — so the phrases are
// specific enough to name what went wrong.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// practiceLegacySelectorPhrases are the sentences that assert a per-language
// practice graph, in the spellings the surfaces actually use.
var practiceLegacySelectorPhrases = []string{
	"pre-singleton",
	"legacy selector",
	"legacy `language`",
	"READ-ONLY legacy",
	"read-only selector",
	"read-only legacy",
	"per-language practice",
	"LEGACY pre-singleton",
}

// renderHelpTopic returns the rendered body of one help topic.
func renderHelpTopic(t *testing.T, topic string) string {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"topic": topic})
	require.NoError(t, err)
	res := handleHelpClient(raw)
	require.Falsef(t, res.IsError, "help(%q) must render", topic)
	body := textBodyTools(res)
	require.NotEmptyf(t, body, "help(%q) rendered nothing, so this census measured nothing", topic)
	return body
}

// assertNoPhrase reports a hit as a short EXCERPT around the offending phrase
// rather than as the whole body. A help topic is thousands of characters, and a
// failure that dumps all of them buries the one line the reader has to fix.
func assertNoPhrase(t *testing.T, surface, body, phrase, why string) {
	t.Helper()
	at := strings.Index(body, phrase)
	if at < 0 {
		return
	}
	from := max(at-90, 0)
	to := min(at+len(phrase)+90, len(body))
	t.Errorf("%s carries %q — %s\n  ...%s...", surface, phrase, why, body[from:to])
}

// TestHelpTopics_NameNoLegacyPracticeSelector is the help half of requirement 5.
//
// IT CENSUSES EVERY TOPIC rather than the ones the requirement lists by name.
// The practice selector is described in the query, mutate, delete, traverse,
// assemble, search and sync bodies, and a list transcribed here would go green
// on the day a sentence moved between them.
func TestHelpTopics_NameNoLegacyPracticeSelector(t *testing.T) {
	require.NotEmpty(t, helpTopics, "the topic table is what this census walks")
	for topic := range helpTopics {
		t.Run(topic, func(t *testing.T) {
			body := renderHelpTopic(t, topic)
			for _, phrase := range practiceLegacySelectorPhrases {
				assertNoPhrase(t, "help(\""+topic+"\")", body, phrase,
					"it asserts a per-language practice graph")
			}
		})
	}
}

// TestHelpPractice_DoesNotRequireALanguageForSearch closes the one line that was
// already false before this change: a practice search requires no language.
//
// THE PHRASES HERE ARE NARROWER THAN THE CENSUS ABOVE ON PURPOSE. help("patterns")
// legitimately says there USED TO BE a graph per language slug — a deleted rule
// leaving its reasoning behind is this repo's own idiom — so a phrase matching
// the word "slug" would have to be disarmed rather than fixed. These two match
// the live instruction instead.
func TestHelpPractice_DoesNotRequireALanguageForSearch(t *testing.T) {
	for topic := range helpTopics {
		t.Run(topic, func(t *testing.T) {
			body := renderHelpTopic(t, topic)
			assertNoPhrase(t, "help(\""+topic+"\")", body, "Required for search",
				"a practice search requires no language")
			assertNoPhrase(t, "help(\""+topic+"\")", body, "omit to list graphs",
				"the practice family holds one graph, so there is no listing to omit a selector for")
		})
	}
}

// practiceSelectorSchemaParams are the six schema param descriptions
// requirement 5 names, as (tool, param) pairs resolved from the live ToolDefs.
func practiceSelectorSchemaParams(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, tc := range []struct {
		label string
		def   kgtools.MCPTool
		param string
	}{
		{"query.graph", QueryToolDef(), "graph"},
		{"query.language", QueryToolDef(), "language"},
		{"search.language", SearchToolDef(), "language"},
		{"traverse.language", TraverseToolDef(), "language"},
		{"mutate.language", MutateToolDef(), "language"},
		{"delete.language", DeleteToolDef(), "language"},
		{"assemble.source", AssembleToolDef(), "source"},
	} {
		prop, ok := tc.def.InputSchema.Properties[tc.param]
		require.Truef(t, ok, "%s: the schema must still declare %q", tc.label, tc.param)
		desc := schemaParamDescription(t, prop)
		require.NotEmptyf(t, desc, "%s: the description is empty, so this census measured nothing", tc.label)
		out[tc.label] = desc
	}
	return out
}

// TestToolSchemas_NameNoLegacyPracticeSelector is the schema half of
// requirement 5. The generated doc pages splice these descriptions verbatim, so
// this is the one assertion that covers both surfaces.
func TestToolSchemas_NameNoLegacyPracticeSelector(t *testing.T) {
	for label, desc := range practiceSelectorSchemaParams(t) {
		for _, phrase := range practiceLegacySelectorPhrases {
			assert.NotContainsf(t, desc, phrase,
				"%s still asserts a per-language practice graph", label)
		}
	}
}

// TestPracticeSelectorMessages_NameNoLegacyRead covers the refusal wordings a
// caller reads when a practice selector is wrong. They are prose too, and a
// refusal that sends the caller to a call that no longer exists is worse than a
// stale doc page.
func TestPracticeSelectorMessages_NameNoLegacyRead(t *testing.T) {
	for label, msg := range map[string]string{
		"practiceListGraphsUnrouted": practiceListGraphsUnrouted,
		"practiceStatsNoHubScope":    practiceStatsNoHubScope,
		"styleIndexPagingRejected":   styleIndexPagingRejected,
	} {
		assert.NotContainsf(t, msg, `language:"<lang>"`,
			"%s names a call that no longer works", label)
		assert.NotContainsf(t, msg, "pre-singleton",
			"%s speaks of graphs that are gone", label)
	}
}

// schemaParamDescription reads one property's description whatever concrete
// shape the schema map holds it under.
func schemaParamDescription(t *testing.T, prop any) string {
	t.Helper()
	raw, err := json.Marshal(prop)
	require.NoError(t, err)
	var decoded struct {
		Description string `json:"description"`
	}
	require.NoError(t, json.Unmarshal(raw, &decoded))
	return strings.TrimSpace(decoded.Description)
}
