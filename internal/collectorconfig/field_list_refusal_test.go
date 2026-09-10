// SPDX-License-Identifier: Apache-2.0

package collectorconfig

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// field_list_refusal_test.go — R2's loader half, one row per class per list per
// cascade level.
//
// EVERY ROW ASSERTS THE MESSAGE NAMES THE FILE, THE ENTRY AND THE FIELD, in that
// order, which is the shape every other refusal in this package follows. A
// refusal that said only "bad field list" would pass a test that checked for an
// error and would tell an operator nothing.
//
// THE REFUSAL IS A LOADER REFUSAL AND NOT A COMPOSER ONE. The composer's
// tolerance for an unrecognized field NAME is a separate, pinned contract (an
// unknown name resolves as a metadata key and contributes nothing); nothing here
// invents a closed vocabulary of admissible field names.

// listRefusalBody builds a stdio entry whose behavior block carries one raw JSON
// fragment, so a row can express `[]`, a blank entry or a duplicate exactly as an
// operator would have typed it.
func listRefusalBody(fragment string) string {
	return `{"collectors":{"tickets":{"type":"stdio","command":"/usr/local/bin/p","tool":"collect",` +
		fragment + `}}}`
}

func TestParseFile_FieldListRefusalSet(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want []string
	}{
		// EMPTY DECLARED LIST, all three lists, graph level.
		{"graph-level embed_fields declared empty",
			listRefusalBody(`"behavior":{"embed_fields":[]}`),
			[]string{"/scratch/collectors.json", "tickets", "embed_fields", "empty"}},
		{"graph-level summarize_fields declared empty",
			listRefusalBody(`"behavior":{"summarize_fields":[]}`),
			[]string{"/scratch/collectors.json", "tickets", "summarize_fields", "empty"}},
		{"graph-level bm25_fields declared empty",
			listRefusalBody(`"behavior":{"bm25_fields":[]}`),
			[]string{"/scratch/collectors.json", "tickets", "bm25_fields", "empty"}},

		// BLANK ENTRY.
		{"graph-level embed_fields with a blank entry",
			listRefusalBody(`"behavior":{"embed_fields":["summary","  "]}`),
			[]string{"/scratch/collectors.json", "tickets", "embed_fields", "blank"}},
		{"graph-level summarize_fields with an empty entry",
			listRefusalBody(`"behavior":{"summarize_fields":[""]}`),
			[]string{"/scratch/collectors.json", "tickets", "summarize_fields", "blank"}},

		// DUPLICATE ENTRY.
		{"graph-level embed_fields with a duplicate",
			listRefusalBody(`"behavior":{"embed_fields":["summary","content","summary"]}`),
			[]string{"/scratch/collectors.json", "tickets", "embed_fields", "summary", "twice"}},
		{"graph-level bm25_fields with a duplicate",
			listRefusalBody(`"behavior":{"bm25_fields":["content","content"]}`),
			[]string{"/scratch/collectors.json", "tickets", "bm25_fields", "content", "twice"}},

		// THE SECOND CASCADE LEVEL. NodeTypeOverride carries all three lists too, so
		// a refusal that reached only the graph level would leave half the surface
		// unguarded.
		{"node-type override embed_fields declared empty",
			listRefusalBody(`"node_types":{"ticket":{"embed_fields":[]}}`),
			[]string{"/scratch/collectors.json", "tickets", "ticket", "embed_fields", "empty"}},
		{"node-type override summarize_fields with a blank entry",
			listRefusalBody(`"node_types":{"ticket":{"summarize_fields":["summary","\t"]}}`),
			[]string{"/scratch/collectors.json", "tickets", "ticket", "summarize_fields", "blank"}},
		{"node-type override bm25_fields with a duplicate",
			listRefusalBody(`"node_types":{"ticket":{"bm25_fields":["summary","summary"]}}`),
			[]string{"/scratch/collectors.json", "tickets", "ticket", "bm25_fields", "summary", "twice"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseFile(noEnv, "/scratch/collectors.json", []byte(tc.body))
			require.Error(t, err, "bad input always errors; this entry was admitted")
			for _, want := range tc.want {
				assert.Contains(t, err.Error(), want,
					"the refusal must name the file, the entry and the field")
			}
		})
	}
}

// TestParseFile_FieldListsThatAreFineAreAdmitted is the known positive for the
// table above. Without it every row would be satisfied by a loader that refused
// every behavior block it was handed.
func TestParseFile_FieldListsThatAreFineAreAdmitted(t *testing.T) {
	got, err := parseFile(noEnv, "/scratch/collectors.json", []byte(listRefusalBody(
		`"behavior":{"embed_fields":["summary","content"],"summarize_fields":["content"],`+
			`"bm25_fields":["symbol_name"]},`+
			`"node_types":{"ticket":{"embed_fields":["summary"],"bm25_fields":["content","summary"]}}`)))
	require.NoError(t, err)
	e := got["tickets"]
	require.NotNil(t, e.Behavior)
	assert.Equal(t, []string{"summary", "content"}, e.Behavior.EmbedFields)
	assert.Equal(t, []string{"content"}, e.Behavior.SummarizeFields)
	assert.Equal(t, []string{"symbol_name"}, e.Behavior.Bm25Fields)
	assert.Equal(t, []string{"summary"}, e.NodeTypes["ticket"].EmbedFields)

	// AN ABSENT LIST IS NOT AN EMPTY ONE, and that distinction is what the whole
	// server-side default rests on. An entry with no behavior block at all, and an
	// entry whose block names only booleans, both pass.
	_, err = parseFile(noEnv, "/scratch/collectors.json", []byte(goodStdio))
	require.NoError(t, err)
	_, err = parseFile(noEnv, "/scratch/collectors.json", []byte(listRefusalBody(
		`"behavior":{"summarizable":true,"embeddable":false}`)))
	require.NoError(t, err)
}

// TestParseFile_FieldListRefusalIsNotAVocabularyCheck: an UNRECOGNIZED field name
// is admitted, because it resolves as a metadata key at compose time. R2 refuses
// malformed LISTS, never unfamiliar NAMES.
func TestParseFile_FieldListRefusalIsNotAVocabularyCheck(t *testing.T) {
	got, err := parseFile(noEnv, "/scratch/collectors.json", []byte(listRefusalBody(
		`"behavior":{"embed_fields":["summary","my_custom_metadata_key"]}`)))
	require.NoError(t, err,
		"an unfamiliar field name resolves as a metadata key on the server; refusing it here "+
			"would invent a closed vocabulary this config format does not have")
	assert.Equal(t, []string{"summary", "my_custom_metadata_key"}, got["tickets"].Behavior.EmbedFields)
}
