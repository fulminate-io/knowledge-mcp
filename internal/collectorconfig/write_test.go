// SPDX-License-Identifier: Apache-2.0

package collectorconfig

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// write_test.go — the file half of add and remove, and the legacy rendering the
// migration path rests on.

// TestUpsertAndRemove_RoundTrip pins the ordinary lifecycle, including that the
// file it writes is one the LOADER accepts: a writer whose output the reader
// refuses is a command that reports success and breaks the next collect.
func TestUpsertAndRemove_RoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), ConfigDirName, FileName)

	require.NoError(t, Upsert(path, "tickets", Entry{
		Type: TransportStdio, Command: "/usr/local/bin/p", Tool: "collect",
		Env: map[string]string{"TOKEN": "t"},
	}))
	require.NoError(t, Upsert(path, "remote", Entry{
		Type: TransportHTTP, URL: "https://c.example/mcp", Tool: "collect_logs",
	}))

	loaded, err := loadFile(noEnv, path)
	require.NoError(t, err, "the writer's own output must load through the reader")
	require.Len(t, loaded, 2)
	assert.Equal(t, "/usr/local/bin/p", loaded["tickets"].Command)

	require.NoError(t, Remove(path, "tickets"))
	loaded, err = loadFile(noEnv, path)
	require.NoError(t, err)
	require.Len(t, loaded, 1)
	_, gone := loaded["tickets"]
	assert.False(t, gone)

	// REMOVING THE LAST ENTRY LEAVES A LOADABLE FILE, which is why an empty
	// collectors object is an empty scope rather than a refusal.
	require.NoError(t, Remove(path, "remote"))
	loaded, err = loadFile(noEnv, path)
	require.NoError(t, err)
	assert.Empty(t, loaded)
}

// TestRemove_AnAbsentNameIsAnError pins that removing something that is not
// there FAILS. Reporting success would tell an operator who misspelled a name
// that their collector was removed while it kept collecting.
func TestRemove_AnAbsentNameIsAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), ConfigDirName, FileName)
	require.NoError(t, Upsert(path, "tickets", Entry{Type: TransportStdio, Command: "/p", Tool: "collect"}))

	err := Remove(path, "boards")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "boards")
	assert.Contains(t, err.Error(), path)
}

// TestUpsert_ReadsBackUNEXPANDED is the row that keeps a credential out of the
// file. `add` rewrites the whole object, so the entries already in it are
// re-marshaled — and an entry read through the EXPANDING path would come back
// with its ${TOKEN} replaced by the token itself, baking the secret into the
// very file the reference exists to keep it out of.
func TestUpsert_ReadsBackUNEXPANDED(t *testing.T) {
	t.Setenv("FUL1794_WRITE_PATH_TOKEN", "super-secret")
	path := filepath.Join(t.TempDir(), ConfigDirName, FileName)
	require.NoError(t, Upsert(path, "remote", Entry{
		Type: TransportHTTP, URL: "https://c.example/mcp", Tool: "collect",
		Headers: map[string]string{"Authorization": "Bearer ${FUL1794_WRITE_PATH_TOKEN}"},
	}))

	// A SECOND write is what re-marshals the first entry, so this is the operation
	// under test rather than the initial write.
	require.NoError(t, Upsert(path, "tickets", Entry{Type: TransportStdio, Command: "/p", Tool: "collect"}))

	raw, err := os.ReadFile(path) //nolint:gosec // a t.TempDir() path.
	require.NoError(t, err)
	body := string(raw)
	assert.Contains(t, body, "${FUL1794_WRITE_PATH_TOKEN}", "the reference must survive a rewrite verbatim")
	assert.NotContains(t, body, "super-secret", "the resolved value must NEVER be written into the file")

	// CONTROL, same run: the loader DOES expand it when reading for a collect, so
	// the assertion above is about the write path and not about an expander that
	// never fires.
	loaded, err := loadFile(nil, path)
	require.NoError(t, err)
	assert.Equal(t, "Bearer super-secret", loaded["remote"].Headers["Authorization"])
}

// TestUpsert_RefusesBeforeWritingWhenTheExistingFileCannotBeRead pins that a
// write never silently discards entries it could not parse.
func TestUpsert_RefusesBeforeWritingWhenTheExistingFileCannotBeRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	require.NoError(t, os.WriteFile(path, []byte(`{"collectors":`), 0o600))

	err := Upsert(path, "tickets", Entry{Type: TransportStdio, Command: "/p", Tool: "collect"})
	require.Error(t, err, "a malformed existing file must refuse the write rather than being overwritten with one entry")

	raw, err := os.ReadFile(path) //nolint:gosec // a t.TempDir() path.
	require.NoError(t, err)
	assert.Equal(t, `{"collectors":`, string(raw), "the file must be untouched by a refused write")
}

// TestLegacyInvocation_SynthesizesAClassARecord pins the migration path's one
// piece of guidance: the exact command that turns a leftover catalog record into
// a config entry.
//
// THE ENV PLACEHOLDER IS ASSERTED SPECIFICALLY. Those records carried variable
// NAMES with no values, so emitting `-e NAME=` would hand the operator a command
// that sets the variable to the EMPTY STRING — a different input from an unset
// one, and one their provider would read as configured.
func TestLegacyInvocation_SynthesizesAClassARecord(t *testing.T) {
	stdio := &knowledgev1.GraphTypeDef{
		Name: "tickets",
		Collector: &knowledgev1.CollectorSpec{
			Tool: "collect",
			Provider: &knowledgev1.CollectorSpec_Stdio{Stdio: &knowledgev1.StdioProvider{
				Command: "/usr/local/bin/p", Args: []string{"--region", "us-east-1"}, Env: []string{"TOKEN"},
			}},
		},
	}
	label, convertible := LegacyClass(stdio)
	assert.Equal(t, LegacyConvertible, label)
	require.True(t, convertible)

	got := LegacyInvocation(stdio, ScopeUser)
	for _, want := range []string{
		"knowledge collector add", "-s user", "-t stdio", "--tool collect",
		"-e TOKEN=" + EnvValuePlaceholder, "tickets", "--", "/usr/local/bin/p", "--region", "us-east-1",
	} {
		assert.Contains(t, got, want)
	}
	assert.NotContains(t, got, "-e TOKEN= ",
		"an empty value is a DIFFERENT input from a placeholder the operator must fill in")

	httpDef := &knowledgev1.GraphTypeDef{
		Name: "remote",
		Collector: &knowledgev1.CollectorSpec{
			Tool:     "collect_logs",
			Provider: &knowledgev1.CollectorSpec_Http{Http: &knowledgev1.HttpProvider{Url: "https://c.example/mcp"}},
		},
	}
	got = LegacyInvocation(httpDef, ScopeProject)
	assert.Contains(t, got, "-s project")
	assert.Contains(t, got, "-t http")
	assert.Contains(t, got, "https://c.example/mcp")
	assert.NotContains(t, got, "--\n")
}

// TestLegacyInvocation_ClassBHasNoInvocation pins the other class: a pre-project
// exec-only record decodes with no tool and no provider, so there is nothing to
// synthesize.
//
// THAT RECORD IS ALREADY BROKEN BEFORE THIS CONTRACT, which is worth stating
// here so nobody reads its refusal as something the config file caused: the
// collect path already refuses a record that names no tool.
func TestLegacyInvocation_ClassBHasNoInvocation(t *testing.T) {
	classB := &knowledgev1.GraphTypeDef{Name: "legacy-exec", Collector: &knowledgev1.CollectorSpec{}}
	label, convertible := LegacyClass(classB)
	assert.Equal(t, LegacyUnconvertible, label)
	assert.False(t, convertible)
	assert.Empty(t, LegacyInvocation(classB, ScopeUser))

	noCollector := &knowledgev1.GraphTypeDef{Name: "behavior-only"}
	label, convertible = LegacyClass(noCollector)
	assert.Equal(t, LegacyUnconvertible, label)
	assert.False(t, convertible)
}
