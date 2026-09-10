// SPDX-License-Identifier: Apache-2.0

package collectorconfig

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// load_test.go — the refusal set, one row per class.
//
// EVERY ROW ASSERTS THE MESSAGE NAMES THE FILE, THE ENTRY AND THE FIELD, not
// merely that an error came back. A loader that refused everything with one
// sentence would pass a test that only checked for an error, and an operator
// holding a fifty-line file would be told nothing about which line to fix.

// noEnv is the lookup for a file with no ${VAR} references, so a refusal row
// cannot pass because the process environment happened to hold something.
func noEnv(string) (string, bool) { return "", false }

// mustParse loads bytes through the whole pipeline and requires success —
// including that no entry carried its OWN refusal, which the file-level error
// return no longer reports.
func mustParse(t *testing.T, body string) map[string]Entry {
	t.Helper()
	out, err := parseFile(noEnv, "/scratch/collectors.json", []byte(body))
	require.NoError(t, err)
	entries := make(map[string]Entry, len(out))
	for name, row := range out {
		require.NoErrorf(t, row.Unresolved, "entry %q did not resolve", name)
		entries[name] = row.Entry
	}
	return entries
}

// goodStdio is the reference entry every refusal row bends.
const goodStdio = `{"collectors":{"tickets":{"type":"stdio","command":"/usr/local/bin/p","tool":"collect"}}}`

func TestParseFile_AdmitsTheReferenceEntries(t *testing.T) {
	// THE KNOWN POSITIVE for the whole table below: without it, every refusal row
	// would be satisfied by a loader that refused every file it was handed.
	got := mustParse(t, `{"collectors":{
      "tickets": {"type":"stdio","command":"/usr/local/bin/p","args":["--serve"],
                  "env":{"TOKEN":"t"},"tool":"collect"},
      "remote":  {"type":"http","url":"https://c.example/mcp",
                  "headers":{"Authorization":"Bearer x"},"tool":"collect_logs"}
    }}`)
	require.Len(t, got, 2)
	assert.Equal(t, "/usr/local/bin/p", got["tickets"].Command)
	assert.Equal(t, map[string]string{"TOKEN": "t"}, got["tickets"].Env)
	assert.Equal(t, "https://c.example/mcp", got["remote"].URL)
	assert.Equal(t, "collect_logs", got["remote"].Tool)
}

func TestParseFile_RefusalSet(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want []string
	}{
		{"invalid JSON", `{"collectors":`, []string{"/scratch/collectors.json", "does not decode"}},
		{"trailing content after the object", goodStdio + `{"collectors":{}}`, []string{"trailing content"}},
		{"an unknown key at the top level", `{"collectors":{},"servers":{}}`, []string{"servers"}},
		{
			"an unknown key inside an entry",
			`{"collectors":{"tickets":{"type":"stdio","command":"/p","tool":"c","binary_path":"/p"}}}`,
			[]string{"tickets", "binary_path"},
		},
		{
			"a retired exec key inside an entry",
			`{"collectors":{"tickets":{"type":"stdio","command":"/p","tool":"c","param_transport":"stdin"}}}`,
			[]string{"tickets", "param_transport"},
		},
		{
			"an unknown key inside behavior",
			`{"collectors":{"tickets":{"type":"stdio","command":"/p","tool":"c","behavior":{"searchable":true}}}}`,
			[]string{"tickets", "searchable"},
		},
		{
			"an unknown key inside a node_types override",
			`{"collectors":{"tickets":{"type":"stdio","command":"/p","tool":"c","node_types":{"issue":{"indexable":true}}}}}`,
			[]string{"tickets", "indexable"},
		},
		{"a missing type", `{"collectors":{"tickets":{"command":"/p","tool":"c"}}}`, []string{"tickets", "type is required"}},
		{"an unknown type", `{"collectors":{"tickets":{"type":"sse","url":"https://x/","tool":"c"}}}`, []string{"tickets", `"sse"`}},
		{"a stdio entry with no command", `{"collectors":{"tickets":{"type":"stdio","tool":"c"}}}`, []string{"tickets", "command is required"}},
		{"an http entry with no url", `{"collectors":{"remote":{"type":"http","tool":"c"}}}`, []string{"remote", "url is required"}},
		{
			"an http entry whose url does not parse",
			`{"collectors":{"remote":{"type":"http","url":"://not a url","tool":"c"}}}`,
			[]string{"remote", "does not parse"},
		},
		{
			"a stdio entry carrying url",
			`{"collectors":{"tickets":{"type":"stdio","command":"/p","url":"https://x/","tool":"c"}}}`,
			[]string{"tickets", "url belongs to a \"http\" entry"},
		},
		{
			"a stdio entry carrying headers",
			`{"collectors":{"tickets":{"type":"stdio","command":"/p","headers":{"A":"b"},"tool":"c"}}}`,
			[]string{"tickets", "headers belong to a \"http\" entry"},
		},
		{
			"an http entry carrying command",
			`{"collectors":{"remote":{"type":"http","url":"https://x/","command":"/p","tool":"c"}}}`,
			[]string{"remote", "command belongs to a \"stdio\" entry"},
		},
		{
			"an http entry carrying env",
			`{"collectors":{"remote":{"type":"http","url":"https://x/","env":{"A":"b"},"tool":"c"}}}`,
			[]string{"remote", "env belongs to a \"stdio\" entry"},
		},
		{
			"a non-string env value",
			`{"collectors":{"tickets":{"type":"stdio","command":"/p","tool":"c","env":{"N":7}}}}`,
			[]string{"tickets", "cannot unmarshal"},
		},
		{
			"a non-string header value",
			`{"collectors":{"remote":{"type":"http","url":"https://x/","tool":"c","headers":{"A":true}}}}`,
			[]string{"remote", "cannot unmarshal"},
		},
		{"a missing tool", `{"collectors":{"tickets":{"type":"stdio","command":"/p"}}}`, []string{"tickets", "tool is required"}},
		{
			"a non-boolean where a behavior boolean belongs",
			`{"collectors":{"tickets":{"type":"stdio","command":"/p","tool":"c","behavior":{"syncable":"yes"}}}}`,
			[]string{"tickets", "cannot unmarshal"},
		},
		{
			"an empty env variable name",
			`{"collectors":{"tickets":{"type":"stdio","command":"/p","tool":"c","env":{"":"v"}}}}`,
			[]string{"tickets", "empty variable name"},
		},
		{
			"an env key carrying an = ",
			`{"collectors":{"tickets":{"type":"stdio","command":"/p","tool":"c","env":{"A=B":"v"}}}}`,
			[]string{"tickets", "the KEY is the variable name"},
		},
		{
			"an empty node-type key",
			`{"collectors":{"tickets":{"type":"stdio","command":"/p","tool":"c","node_types":{"":{}}}}}`,
			[]string{"tickets", "empty node-type key"},
		},
		{"an empty entry name", `{"collectors":{"":{"type":"stdio","command":"/p","tool":"c"}}}`, []string{"empty name"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseFile(noEnv, "/scratch/collectors.json", []byte(tc.body))
			require.Error(t, err, "this shape must be refused, never skipped or defaulted")
			for _, want := range tc.want {
				assert.Contains(t, err.Error(), want, "the refusal must name it")
			}
		})
	}
}

// TestParseFile_DuplicateEntryNameIsRefused is its own row because the strict
// decode CANNOT see it: JSON permits a repeated key and Go keeps the last one
// silently, so a passing strict decode is not evidence about duplicates. The
// known-negative below is what proves the second walk is looking at the right
// object.
func TestParseFile_DuplicateEntryNameIsRefused(t *testing.T) {
	_, err := parseFile(noEnv, "/scratch/collectors.json", []byte(
		`{"collectors":{
        "tickets":{"type":"stdio","command":"/a","tool":"c"},
        "tickets":{"type":"stdio","command":"/b","tool":"c"}}}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "tickets")
	assert.Contains(t, err.Error(), "twice")

	// KNOWN NEGATIVE: two DIFFERENT names are not a duplicate, so the walk is not
	// simply refusing every file with two entries.
	got := mustParse(t, `{"collectors":{
        "tickets":{"type":"stdio","command":"/a","tool":"c"},
        "boards":{"type":"stdio","command":"/b","tool":"c"}}}`)
	assert.Len(t, got, 2)
}

// TestLoadFile_MissingIsAnEmptyScopeAndUnreadableIsAnError is the pair the whole
// contract turns on. They are asserted together because the difference is the
// point: an absent file is an operator who installed no collectors in that
// scope, and an unreadable one is an operator whose registrations cannot be
// determined. Reporting the second as the first silently unregisters everything
// they wrote.
func TestLoadFile_MissingIsAnEmptyScopeAndUnreadableIsAnError(t *testing.T) {
	dir := t.TempDir()

	absent := filepath.Join(dir, "nowhere", FileName)
	got, err := loadFile(noEnv, absent)
	require.NoError(t, err, "an absent file is an empty scope, not a failure")
	assert.Empty(t, got)

	empty := filepath.Join(dir, "empty.json")
	require.NoError(t, os.WriteFile(empty, nil, 0o600))
	got, err = loadFile(noEnv, empty)
	require.NoError(t, err, "an empty file is an empty scope: `collector remove` of the last entry leaves one")
	assert.Empty(t, got)

	malformed := filepath.Join(dir, "malformed.json")
	require.NoError(t, os.WriteFile(malformed, []byte(`{"collectors":`), 0o600))
	_, err = loadFile(noEnv, malformed)
	require.Error(t, err, "a file whose CONTENTS cannot be parsed is an ERROR, never an empty scope")
	assert.Contains(t, err.Error(), malformed)

	// THE FOURTH ARM IS A DIFFERENT BRANCH FROM THE THIRD, and that is the whole
	// reason it is here. The malformed case above reaches parseFile: the READ
	// succeeded. This one never gets that far — os.ReadFile itself fails — and
	// nothing else in this repository drives that branch, so replacing it with an
	// empty scope left every suite green while restoring exactly the degradation
	// this file's own doc comment forbids.
	unreadable := filepath.Join(dir, "unreadable.json")
	require.NoError(t, os.WriteFile(unreadable, []byte(`{"collectors":{}}`), 0o600))
	require.NoError(t, os.Chmod(unreadable, 0o000))
	t.Cleanup(func() { _ = os.Chmod(unreadable, 0o600) })
	if _, probe := os.ReadFile(unreadable); probe == nil { //nolint:gosec // the probe IS the eligibility check.
		// ROOT READS ANYTHING, so on a root runner this arm would assert a
		// property the mode bits cannot produce. Skipping is honest; asserting
		// would be a green that means nothing.
		t.Skip("this process can read a 0o000 file (running as root?), so the read-error branch is unreachable here")
	}
	_, err = loadFile(noEnv, unreadable)
	require.Error(t, err, "a file that exists and cannot be READ is an ERROR, never an empty scope")
	assert.Contains(t, err.Error(), unreadable)
	assert.Contains(t, err.Error(), "cannot be read")
}
