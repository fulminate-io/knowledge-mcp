// SPDX-License-Identifier: Apache-2.0

package tools

// manage_checks_include_tests_test.go — the run knob AS THE CALLER MEETS IT.
//
// Every row goes through InterceptManageChecks with a real JSON payload, because
// the two halves that can break independently are both on that path: the schema
// must declare the param (or rejectUndeclaredParams refuses the call before any
// scan), and the args struct must read it (or it is accepted and dropped). A
// test calling the analyzer directly would see neither.

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// checksRunTestFileTree is the run fixture tree plus a TEST file holding one
// more site for the fmt check, so the site count alone says whether the walk
// reached test code: 2 without it, 3 with it.
func checksRunTestFileTree(t *testing.T) string {
	t.Helper()
	root := checksRunFixtureTree(t)
	require.NoError(t, os.WriteFile(filepath.Join(root, "sites_test.go"), []byte(
		"package runfixture\n\nfunc gamma() { fmt.Println(3) }\n"), 0o600))
	return root
}

// registeredTestFileRepo records the tree under a repo name and returns it.
func registeredTestFileRepo(t *testing.T) string {
	t.Helper()
	root := checksRunTestFileTree(t)
	m := withTestManifest(t)
	require.NoError(t, m.Record("runfixture", root))
	return root
}

// TestManageChecks_RunIncludeTestsIsTriState pins all three input states at the
// MCP face, with the site count as the observable.
func TestManageChecks_RunIncludeTestsIsTriState(t *testing.T) {
	registeredTestFileRepo(t)

	for _, tc := range []struct {
		name  string
		extra map[string]any
		want  string
	}{
		{"absent", nil, "sites_flagged=2"},
		{"explicit false", map[string]any{"include_tests": false}, "sites_flagged=2"},
		{"explicit true", map[string]any{"include_tests": true}, "sites_flagged=3"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := driveChecksRun(t, runChecksArgs(t, "runfixture", tc.extra))
			require.False(t, res.IsError, "the run must succeed: %s", res.Content[0].Text)
			assert.Contains(t, res.Content[0].Text, tc.want,
				"the tree holds two non-test sites and one test-file site")
		})
	}
}

// TestManageChecks_RunVerdictLineCarriesTheTestFileCount is R3 at this face, and
// its knob-off leg is the falsifying one: the five existing counters and the
// token are exactly what they were before this feature existed.
func TestManageChecks_RunVerdictLineCarriesTheTestFileCount(t *testing.T) {
	registeredTestFileRepo(t)

	off := driveChecksRun(t, runChecksArgs(t, "runfixture", nil)).Content[0].Text
	assert.Contains(t, off, "corpus_scan: FLAGGED  checks_flagged=2 sites_flagged=2 checks_refused=0 llm_only_not_executed=0 test_files_scanned=0 truncated=false",
		"a run that reached no test file reports zero, and every counter beside it is unmoved")

	on := driveChecksRun(t, runChecksArgs(t, "runfixture", map[string]any{"include_tests": true})).Content[0].Text
	assert.Contains(t, on, "test_files_scanned=1",
		"a run that reached test code says so, which is the whole point of the counter")
	assert.Contains(t, on, "sites_flagged=3", "and the disclosure is not counted as a site")
}

// TestManageChecks_RunIncludeTestsRefusesAnUnsupportedLanguage relays the
// analyzer's refusal verbatim. The control is the same call with the flag
// OMITTED, which must not be refused for the language's sake.
func TestManageChecks_RunIncludeTestsRefusesAnUnsupportedLanguage(t *testing.T) {
	registeredTestFileRepo(t)

	args := func(extra map[string]any) json.RawMessage {
		body := map[string]any{"operation": OpChecksRun, "repo": "runfixture", "language": "rust"}
		maps.Copy(body, extra)
		raw, err := json.Marshal(body)
		require.NoError(t, err)
		return raw
	}

	for _, v := range []bool{true, false} {
		res := driveChecksRun(t, args(map[string]any{"include_tests": v}))
		require.True(t, res.IsError, "include_tests=%v for a language with no test-file convention must be refused", v)
		body := res.Content[0].Text
		assert.Contains(t, body, "rust", "the refusal names the offending language")
		assert.Contains(t, body, "go", "and lists the languages that do carry a convention")
	}

	omitted := driveChecksRun(t, args(nil))
	assert.NotContains(t, omitted.Content[0].Text, "include_tests",
		"an omitted flag misleads nobody and must never be refused for the language")
}

// TestManageChecks_NewParamsAreDeclaredAndRead is the schema/args seam. Both
// halves are asserted because either alone is silently wrong: an undeclared
// param is refused before any scan by rejectUndeclaredParams, and a declared one
// with no struct field is accepted and dropped.
func TestManageChecks_NewParamsAreDeclaredAndRead(t *testing.T) {
	props := ManageChecksToolDef().InputSchema.Properties
	for _, name := range []string{"include_tests", "applies_to_tests"} {
		prop, ok := props[name]
		require.True(t, ok, "the schema must declare %s", name)
		assert.NotEmpty(t, prop.Description, "%s must carry a description", name)
	}

	// READ, not merely declared: the call is claimed and answered rather than
	// refused for carrying an unknown key.
	registeredTestFileRepo(t)
	res := driveChecksRun(t, runChecksArgs(t, "runfixture", map[string]any{"include_tests": true}))
	require.False(t, res.IsError, "a declared param must not be refused: %s", res.Content[0].Text)

	// THE FALSIFYING CONTROL: a param the schema does NOT declare is refused, so
	// the pass above is the parity guard working rather than a guard that admits
	// everything.
	bogus := driveChecksRun(t, runChecksArgs(t, "runfixture", map[string]any{"include_test": true}))
	require.True(t, bogus.IsError, "an undeclared param must be refused before anything is scanned")
	assert.Contains(t, bogus.Content[0].Text, "include_test")
}

// TestManageChecks_RunTestFileCountAgreesWithTheAstTool is R4's agreement leg,
// run as ONE test over ONE tree so the two instruments cannot be compared across
// different corpora.
//
// The corpus runner and the ast tool are two callers into the same walk, and the
// number each reports is the number a reader will compare when deciding whether
// a scan reached their test code. They agree here or the counter means two
// different things depending on who asked. Both sides are the real
// instruments — the analyzer through its MCP face, the ast tool through its own.
func TestManageChecks_RunTestFileCountAgreesWithTheAstTool(t *testing.T) {
	root := registeredTestFileRepo(t)

	// THE AST TOOL'S OWN CENSUS over the same tree, at the same flag value.
	astBody, isErr, handled := callAst(t, astTestDeps{rootDir: root},
		`{"operation":"count","language":"go","repo":`+jsonString(root)+`,"pattern":"fmt.Println($X)","include_tests":true}`)
	require.True(t, handled)
	require.False(t, isErr, "the census must succeed: %s", astBody)
	var census struct {
		TestFilesScanned int `json:"test_files_scanned"`
	}
	require.NoError(t, json.Unmarshal([]byte(astBody), &census))
	require.Positive(t, census.TestFilesScanned,
		"the fixture tree must hold a test file, or the agreement below is between two zeros")

	// THE RUN over the same tree, at the same flag value.
	body := driveChecksRun(t, runChecksArgs(t, "runfixture", map[string]any{"include_tests": true})).Content[0].Text
	assert.Contains(t, body, fmt.Sprintf("test_files_scanned=%d", census.TestFilesScanned),
		"the runner and the ast tool walk the same files and must report the same test-file count")

	// THE FALSIFYING CONTROL, same pair at the other flag value: both report
	// zero, so the agreement above is not satisfied by two instruments that
	// always print the same constant.
	offBody, _, _ := callAst(t, astTestDeps{rootDir: root},
		`{"operation":"count","language":"go","repo":`+jsonString(root)+`,"pattern":"fmt.Println($X)"}`)
	var offCensus struct {
		TestFilesScanned int `json:"test_files_scanned"`
	}
	require.NoError(t, json.Unmarshal([]byte(offBody), &offCensus))
	assert.Zero(t, offCensus.TestFilesScanned)
	assert.Contains(t, driveChecksRun(t, runChecksArgs(t, "runfixture", nil)).Content[0].Text, "test_files_scanned=0")
}

// jsonString quotes s as a JSON string literal, so a Windows-style or
// space-carrying temp path cannot break the hand-built payloads above.
func jsonString(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(b)
}
