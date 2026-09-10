// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"bytes"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collectorconfig"
)

// collector_describe_test.go — `knowledge collector add` AGAINST THE DESCRIBE
// TOOL: the refusals, the fill, and the three-class environment policy.
//
// Every expectation about what was WRITTEN is derived from the declaration the
// provider served (declaredValue / declaredStrings), never from a literal copy
// of it beside the assertion: a row that retyped the declaration would pass
// while the fill dropped a field, because both halves would have been typed by
// the same hand at the same moment.

// addAgainst runs one add against a provider built with the given describe
// options and returns the command's output and the user-scope file path.
//
// IT TAKES NO ARGV. Every row here varies what the PROVIDER declares rather than
// what the operator typed; the rows that vary the argv are the behavior-flag
// suite and the stdio environment rows below, which have their own helpers.
func addAgainst(t *testing.T, stub ...describeStubOption) (string, string, error) {
	t.Helper()
	userPath := useTempHome(t)
	useScratchCwd(t)
	url := startContractProvider(t, true, stub...)
	full := []string{"-t", "http", "--tool", contractStubTool}
	full = append(full, "tickets", url)
	var out bytes.Buffer
	err := runCollectorAdd(full, &out)
	return out.String(), userPath, err
}

// addOverStdio runs one add whose provider is THIS TEST BINARY re-execed as a
// stdio collector, which is the only transport whose entry carries an env block
// — the block is the child's whole environment, and the loader refuses one on an
// http entry by name.
func addOverStdio(t *testing.T, argv []string) (string, string, error) {
	t.Helper()
	userPath := useTempHome(t)
	useScratchCwd(t)
	self, err := os.Executable()
	require.NoError(t, err)
	full := []string{"-t", "stdio", "--tool", contractStubTool, "-e", cliStubModeEnv + "=1"}
	full = append(full, argv...)
	full = append(full, "tickets", "--", self, cliStubArgvMarker)
	var out bytes.Buffer
	runErr := runCollectorAdd(full, &out)
	return out.String(), userPath, runErr
}

// loadedEntry reads back the entry one add wrote.
func loadedEntry(t *testing.T, userPath string) collectorconfig.Entry {
	t.Helper()
	se, found, err := collectorconfig.Loader{UserPath: userPath}.Resolve("tickets")
	require.NoError(t, err)
	require.True(t, found, "the add wrote no entry")
	return se.Entry
}

// TestCollectorAdd_RefusesAProviderThatDoesNotServeDescribe is the requirement
// itself: describe is REQUIRED, so a provider that does not serve it is refused
// BY NAME and nothing is written.
//
// THE REFUSAL TEXT IS ASSERTED, not merely the error's presence. Every provider
// written before this tool existed lands here, and its author's next move has to
// come out of the message: what is missing, what it must return, and which
// checked-in file describes it.
func TestCollectorAdd_RefusesAProviderThatDoesNotServeDescribe(t *testing.T) {
	out, userPath, err := addAgainst(t, withoutDescribe())
	require.Error(t, err)

	msg := err.Error()
	assert.Contains(t, msg, `"describe"`, "the refusal names the missing tool")
	assert.Contains(t, msg, "REQUIRED")
	assert.Contains(t, msg, "collector_describe.schema.json", "and points at the checked-in schema file")
	assert.Contains(t, msg, "node and edge types", "and says what the tool must return")
	assert.Contains(t, msg, "was NOT written")

	_, err = os.Stat(userPath)
	assert.True(t, os.IsNotExist(err), "a refused provider must leave NO file written")
	assert.Empty(t, out, "and print no success line")
}

// TestCollectorAdd_RefusesADescribeToolWhoseSchemaDoesNotSatisfyTheContract is
// the schema arm: a provider serves describe, and what it advertises does not
// declare the vocabulary the ingest refusal reads.
func TestCollectorAdd_RefusesADescribeToolWhoseSchemaDoesNotSatisfyTheContract(t *testing.T) {
	_, userPath, err := addAgainst(t, withDescribeSchema(func(out map[string]any) {
		out["required"] = []any{"behavior", "edge_types", "environment"}
		delete(out["properties"].(map[string]any), "node_types")
	}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "node_types", "the refusal names the PATH that falls short")
	assert.Contains(t, err.Error(), "describe")
	_, statErr := os.Stat(userPath)
	assert.True(t, os.IsNotExist(statErr), "nothing is written")
}

// TestCollectorAdd_RefusesADeclarationThatViolatesTheSchemaAtCallTime is the
// CALL-time arm, which asks a different question from the listing-time one: a
// provider may advertise a perfect schema and return something else.
func TestCollectorAdd_RefusesADeclarationThatViolatesTheSchemaAtCallTime(t *testing.T) {
	for _, tc := range []struct {
		name       string
		bend       func(decl map[string]any)
		wantErrHas string
	}{
		{"no behavior", func(d map[string]any) { delete(d, "behavior") }, "behavior"},
		{"no node vocabulary", func(d map[string]any) { delete(d, "node_types") }, "node_types"},
		{"an environment row with no class", func(d map[string]any) {
			d["environment"] = []any{map[string]any{"name": "STUB_HOME"}}
		}, "class"},
		{"an environment row carrying a VALUE", func(d map[string]any) {
			d["environment"] = []any{map[string]any{"name": "STUB_TOKEN", "class": "secret", "value": "s3cret"}}
		}, "value"},
		{"a context declaring a reason", func(d map[string]any) {
			d["context"] = map[string]any{"code": map[string]any{"node_types": []any{"file"}, "reason": "why"}}
		}, "reason"},
		{"a context this client cannot supply", func(d map[string]any) {
			d["context"] = map[string]any{"code": map[string]any{"node_fields": []any{"summary"}}}
		}, "summary"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, userPath, err := addAgainst(t, withDeclaration(tc.bend))
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErrHas)
			_, statErr := os.Stat(userPath)
			assert.True(t, os.IsNotExist(statErr), "a refused declaration writes nothing")
		})
	}
}

// TestCollectorAdd_FillsTheEntryFromTheDeclaration is R2's central row: every
// declared half reaches the written entry, compared against the declaration the
// provider served.
func TestCollectorAdd_FillsTheEntryFromTheDeclaration(t *testing.T) {
	_, userPath, err := addAgainst(t)
	require.NoError(t, err)
	entry := loadedEntry(t, userPath)

	require.NotNil(t, entry.Vocabulary, "the vocabulary must be PRESENT, which is what tells a declared-empty one from a family registered before describe existed")
	assert.Equal(t, declaredStrings(t, "node_types"), entry.Vocabulary.NodeTypes)
	assert.Equal(t, declaredStrings(t, "edge_types"), entry.Vocabulary.EdgeTypes)

	require.NotNil(t, entry.Behavior)
	assert.Equal(t, declaredStrings(t, "behavior", "embed_fields"), entry.Behavior.EmbedFields)
	assert.Equal(t, declaredStrings(t, "behavior", "summarize_fields"), entry.Behavior.SummarizeFields)
	assert.Equal(t, declaredStrings(t, "behavior", "bm25_fields"), entry.Behavior.Bm25Fields)

	declaredOverrides, ok := declaredValue(t, "node_type_overrides").(map[string]any)
	require.True(t, ok)
	require.Len(t, entry.NodeTypes, len(declaredOverrides))
	for nt := range declaredOverrides {
		got, present := entry.NodeTypes[nt]
		require.True(t, present, "the declared override for %q must reach the entry", nt)
		require.NotNil(t, got.Summarizable)
		assert.Equal(t, declaredValue(t, "node_type_overrides", nt, "summarizable"), *got.Summarizable)
	}

	declaredContext, ok := declaredValue(t, "context").(map[string]any)
	require.True(t, ok)
	require.Len(t, entry.Context, len(declaredContext))
	assert.Equal(t, declaredStrings(t, "context", "code", "node_types"), entry.Context["code"].NodeTypes)
	assert.Equal(t, declaredStrings(t, "context", "code", "node_fields"), entry.Context["code"].NodeFields)

	declaredEnvRows, ok := declaredValue(t, "environment").([]any)
	require.True(t, ok)
	require.Len(t, entry.EnvDeclaration, len(declaredEnvRows))
	for i, raw := range declaredEnvRows {
		row := raw.(map[string]any)
		assert.Equal(t, row["name"], entry.EnvDeclaration[i].Name)
		assert.Equal(t, row["class"], entry.EnvDeclaration[i].Class)
	}
}

// TestCollectorAdd_PrintsTheSuggestionAndDoesNotApplyIt is the spend rule's own
// row. The collector suggests both LLM axes ON; the operator gave no flag; the
// entry must record neither, and the operator must be TOLD what was suggested.
func TestCollectorAdd_PrintsTheSuggestionAndDoesNotApplyIt(t *testing.T) {
	out, userPath, err := addAgainst(t)
	require.NoError(t, err)

	assert.Contains(t, out, "SUGGESTS", "an operator must see what the collector's author thinks is worth paying for")
	assert.Contains(t, out, "summarizable=true")
	assert.Contains(t, out, "embeddable=true")
	assert.Contains(t, out, "not applied")

	entry := loadedEntry(t, userPath)
	require.NotNil(t, entry.Behavior)
	assert.Nil(t, entry.Behavior.Summarizable, "the suggestion must not reach the entry")
	assert.Nil(t, entry.Behavior.Embeddable, "the suggestion must not reach the entry")
}

// TestCollectorAdd_IsByteIdenticalOnASecondAdd pins idempotence on the BYTES. A
// map's iteration order is unspecified in Go, and every half the declaration
// fills is built from one, so this row is what a fill that did not sort would
// fail.
func TestCollectorAdd_IsByteIdenticalOnASecondAdd(t *testing.T) {
	t.Setenv("STUB_REGION", "eu-west-2")
	t.Setenv("STUB_HOME", "/home/stub")
	t.Setenv("STUB_TOKEN", "s3cret-value")

	// The STDIO transport, because it is the one whose entry carries every half
	// this fill writes: the env block, the declaration, the vocabulary and the
	// context. Six adds, then a seventh compared byte for byte.
	var userPath string
	var first string
	for i := range 7 {
		out, path, err := addOverStdio(t, nil)
		require.NoError(t, err, "add %d: %s", i, out)
		raw, readErr := os.ReadFile(path)
		require.NoError(t, readErr)
		if i == 0 {
			first, userPath = string(raw), path
			continue
		}
		assert.Equal(t, first, string(raw), "add %d wrote different bytes than the first", i)
		_ = userPath
	}
}

// TestCollectorAdd_AppliesTheThreeClassEnvironmentPolicy is R2's environment
// row. The classes come from the DECLARATION and the values from the shell the
// add runs in, on the same policy the installer applies:
//
//	path      a literal when set
//	selector  a literal when set, the key omitted entirely when unset
//	secret    NOTHING, in any state
func TestCollectorAdd_AppliesTheThreeClassEnvironmentPolicy(t *testing.T) {
	t.Run("every class set", func(t *testing.T) {
		t.Setenv("STUB_HOME", "/home/stub")
		t.Setenv("STUB_REGION", "eu-west-2")
		t.Setenv("STUB_TOKEN", "s3cret-value")

		out, userPath, err := addOverStdio(t, nil)
		require.NoError(t, err, out)
		entry := loadedEntry(t, userPath)

		assert.Equal(t, "/home/stub", entry.Env["STUB_HOME"], "a path literal is written")
		assert.Equal(t, "eu-west-2", entry.Env["STUB_REGION"], "a selector literal is written")
		assert.NotContains(t, entry.Env, "STUB_TOKEN", "a secret name is written in NO state")

		raw, err := os.ReadFile(userPath)
		require.NoError(t, err)
		assert.NotContains(t, string(raw), "s3cret-value", "and its value reaches no byte of the file")
		assert.NotContains(t, string(raw), "${STUB_TOKEN}", "and not a reference either — a reference arrives present-and-empty")
		assert.NotContains(t, out, "s3cret-value", "nor the add's own output")
		assert.Contains(t, out, "STUB_TOKEN", "the NAME is reported as skipped, so the absence is a decision the operator can see")
	})

	t.Run("selector unset omits the key entirely", func(t *testing.T) {
		t.Setenv("STUB_HOME", "/home/stub")
		os.Unsetenv("STUB_REGION")
		os.Unsetenv("STUB_TOKEN")

		out, userPath, err := addOverStdio(t, nil)
		require.NoError(t, err, out)
		entry := loadedEntry(t, userPath)
		assert.NotContains(t, entry.Env, "STUB_REGION",
			"an empty selector selects the thing named by the empty string, so an unset one is omitted rather than written blank")
		// The declaration itself is printed back with the entry, names and all, so
		// the assertion is on the SKIPPED-CREDENTIALS line rather than on the name:
		// nothing was withheld here, so there is nothing to report.
		assert.NotContains(t, out, "NONE of them was written",
			"an unset secret is not reported as skipped: nothing was withheld")
	})

	t.Run("an operator flag wins over the derived value", func(t *testing.T) {
		t.Setenv("STUB_REGION", "eu-west-2")
		_, userPath, err := addOverStdio(t, []string{"-e", "STUB_REGION=us-east-1"})
		require.NoError(t, err)
		assert.Equal(t, "us-east-1", loadedEntry(t, userPath).Env["STUB_REGION"])
	})

	t.Run("an operator may write a declared secret themselves", func(t *testing.T) {
		t.Setenv("STUB_TOKEN", "from-the-shell")
		_, userPath, err := addOverStdio(t, []string{"-e", "STUB_TOKEN=typed-on-purpose"})
		require.NoError(t, err)
		assert.Equal(t, "typed-on-purpose", loadedEntry(t, userPath).Env["STUB_TOKEN"],
			"someone who typed the key knows what it costs; this path exists to keep them from typing the other twenty")
	})
}

// TestCollectorAdd_TheDeclarationCarriesNoValueToTheEntry is the credential
// row's other half: whatever the provider says, no value from a declaration can
// become an env value, because the declaration has nowhere to carry one and the
// decode refuses the attempt.
func TestCollectorAdd_TheDeclarationCarriesNoValueToTheEntry(t *testing.T) {
	// THE VARIABLE IS SET, which is the only state in which the secret arm can
	// go wrong. Asserting its absence with the variable UNSET measures an arm the
	// policy never reaches, and passes against an implementation that writes
	// every secret it finds.
	t.Setenv("STUB_TOKEN", "s3cret-value")

	// OVER STDIO, because that is the transport whose entry HAS an env block: an
	// http provider runs in a process this client did not spawn, so no block is
	// derived for it and the arm under test never runs. A row on the http
	// transport would assert the absence of a key nothing could have written.
	out, userPath, err := addOverStdio(t, nil)
	require.NoError(t, err, out)
	raw, err := os.ReadFile(userPath)
	require.NoError(t, err)

	entry := loadedEntry(t, userPath)
	declaredSecret := false
	for _, row := range entry.EnvDeclaration {
		if row.Name == "STUB_TOKEN" {
			declaredSecret = row.Class == "secret"
		}
	}
	assert.True(t, declaredSecret,
		"the NAME is declared with its class, which is what tells an operator the absence was a decision")
	assert.NotContains(t, entry.Env, "STUB_TOKEN", "and it reaches no env key")

	// ON THE BYTES, not only on the decoded entry: a value can reach the file
	// through a path the decode normalizes away, and the file is what an operator
	// reads and what syncs.
	assert.NotContains(t, string(raw), "s3cret-value", "no secret value reaches any byte of the entry")
	assert.NotContains(t, string(raw), "${STUB_TOKEN", "and no reference to it either — a reference arrives present-and-empty")
}
