// SPDX-License-Identifier: Apache-2.0

// ast_patterns_teststats_test.go — every WalkStats counter survives sibling-form
// alternation.
//
// WHY THIS FILE EXISTS. mergeWalkStats folds one alternation member's stats into
// the union with an explicit per-field policy, and NO COMPILER POINTS AT IT: a
// field added to WalkStats with no rule here is silently zero on every
// patterns:[...] call while every single-pattern call reports it correctly. That
// is the shape a reader is least likely to catch by eye, so the assertion is
// made against a KNOWN-POSITIVE CONTROL rather than against a literal — the
// single-pattern call over the same tree, whose number the alternation must
// reproduce.

package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/ast"
)

// statsReply decodes the walk stats off a count reply. count is used rather than
// match because its stats sit at the top level, and the counters under test are
// discovery-side facts that both ops share.
type statsReply struct {
	Total             int `json:"total"`
	FilesScanned      int `json:"files_scanned"`
	TestFilesScanned  int `json:"test_files_scanned"`
	TestFilesExcluded int `json:"test_files_excluded"`
}

// altStatsRepo writes one non-test and one test file, each holding both call
// shapes, so every alternation member walks the identical file set.
func altStatsRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "fix.go"), []byte(altFixture), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "fix_test.go"), []byte(altFixture), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module fix\n\ngo 1.21\n"), 0o600))
	return dir
}

// callAltStats runs one count call and decodes its stats.
func callAltStats(t *testing.T, dir string, args map[string]any) statsReply {
	t.Helper()
	args["language"] = "go"
	args["repo"] = dir
	args["operation"] = "count"
	encoded, err := json.Marshal(args)
	require.NoError(t, err)
	body, isErr, handled := callAst(t, astTestDeps{rootDir: dir}, string(encoded))
	require.True(t, handled)
	require.False(t, isErr, "body: %s", body)
	var out statsReply
	require.NoError(t, json.Unmarshal([]byte(body), &out), "body: %s", body)
	return out
}

func TestAstPatterns_TestFileCountersSurviveAlternation(t *testing.T) {
	const goodA = "alpha($$$X)"
	const goodB = "beta($$$X)"

	t.Run("tests included: the scanned counter is the single-pattern number", func(t *testing.T) {
		dir := altStatsRepo(t)
		single := callAltStats(t, dir, map[string]any{"pattern": goodA, "include_tests": true})
		require.Equal(t, 1, single.TestFilesScanned, "control: one member reports the counter correctly")

		alt := callAltStats(t, dir, map[string]any{"patterns": []string{goodA, goodB}, "include_tests": true})
		assert.Equal(t, single.TestFilesScanned, alt.TestFilesScanned,
			"every member rediscovers the identical file set, so the union reports that set's count and never zero")
		assert.Equal(t, single.FilesScanned, alt.FilesScanned, "the existing MAX rule is the shape the new one copies")
		assert.Equal(t, 4, alt.Total, "known-positive control: both members matched in both files")
	})

	t.Run("tests excluded: the excluded counter is the single-pattern number", func(t *testing.T) {
		dir := altStatsRepo(t)
		single := callAltStats(t, dir, map[string]any{"pattern": goodA})
		require.Equal(t, 1, single.TestFilesExcluded, "control: one member reports the counter correctly")

		alt := callAltStats(t, dir, map[string]any{"patterns": []string{goodA, goodB}})
		assert.Equal(t, single.TestFilesExcluded, alt.TestFilesExcluded,
			"the complement counter needs its own rule; a field with no rule reads zero here and nowhere else")
		assert.Equal(t, 0, alt.TestFilesScanned, "and its complement stays zero with the filter on")
		assert.Equal(t, 2, alt.Total, "known-positive control: both members matched in the non-test file only")
	})
}

// TestMergeWalkStats_EveryNumericFieldHasARule is the CLASS gate for the defect
// the two counters above exposed, generalized so the next field is covered
// without anyone remembering to cover it.
//
// THE CLASS. mergeWalkStats is a hand-kept per-field policy and every ast tool
// call folds through it — the alternation path AND the single-pattern path, both
// of which call it once per compiled pattern. A field added to WalkStats with no
// line in that function is therefore not merely wrong under alternation: it
// reads ZERO for every ast tool caller while the engine reports it correctly to
// everyone else. Nothing points at the omission: the struct compiles, the walk
// populates the field, the tool prints zero.
//
// WHY REFLECTION AND NOT A LIST. A hand-written list of fields to check is the
// same hand-kept artifact that failed in the first place, one level up. Walking
// the struct means a field added tomorrow is covered today, and the test names
// the field it caught rather than a count.
//
// SCOPE, stated: the numeric fields. The map and string fields carry
// first-walk-wins semantics rather than an arithmetic, and CleanHint has its own
// union helper with its own test; a value-preservation assertion over those
// would be asserting a different rule.
func TestMergeWalkStats_EveryNumericFieldHasARule(t *testing.T) {
	typ := reflect.TypeFor[ast.WalkStats]()
	checked := 0
	for i := range typ.NumField() {
		field := typ.Field(i)
		switch field.Type.Kind() {
		case reflect.Int, reflect.Int64:
		default:
			continue
		}
		t.Run(field.Name, func(t *testing.T) {
			checked++
			// One member carries a distinct non-zero value for this field; the
			// running union starts empty, exactly as a fold does.
			//
			// THE INPUT MUST BE A LEGAL WalkStats OR THE GATE MEASURES ITSELF.
			// FilesSkipped is the exact sum of its three by-cause counters and
			// the fold moves them as ONE GROUP, keyed on the total — precisely so
			// one pass's total can never be paired with another's breakdown. A
			// probe that set a cause while leaving the total at zero would hand
			// the fold a value the walk cannot produce and read the correct
			// refusal to merge it as a missing rule.
			src := ast.WalkStats{}
			reflect.ValueOf(&src).Elem().Field(i).SetInt(7)
			src.FilesSkipped = src.SkippedRead + src.SkippedParseError + src.SkippedParseLimit
			if src.FilesSkipped == 0 {
				reflect.ValueOf(&src).Elem().Field(i).SetInt(7)
			}
			var dst ast.WalkStats
			mergeWalkStats(&dst, src)

			got := reflect.ValueOf(dst).Field(i).Int()
			assert.NotZerof(t, got,
				"WalkStats.%s survives no fold: mergeWalkStats has no rule for it, so every ast tool call reports zero "+
					"for a counter the engine populated. Add its rule beside the others.", field.Name)
		})
	}
	require.Positive(t, checked, "the walk must have found numeric fields, or this gate is asserting over an empty set")
}
