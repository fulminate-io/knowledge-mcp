// SPDX-License-Identifier: Apache-2.0

// ast_degraded_zero_test.go — the degradation-aware zero warning, in both
// directions.
//
// WHY THE CLEAN-CORPUS ROWS ARE HALF THE TEST. A warning that is always present
// carries no information, and an absence assertion with no known-positive in the
// same run cannot tell a working gate from a dead probe. The two degraded rows
// prove the warning fires; the two clean rows prove it fires SELECTIVELY, and
// that the existing zero-result vocabulary is untouched when nothing degraded.
// Neither pair means anything without the other, so both run here.
//
// WHY THE FIXTURE IS ASSERTED BEFORE THE HINT IS. A fixture that silently stops
// degrading — a grammar update that starts accepting the broken file, a
// discovery rule that begins declining it — would turn the two warning rows into
// assertions about a clean corpus, and they would fail for a reason that reads
// like a bug in the warning. Each degraded row therefore asserts
// files_with_parse_errors and files_scanned FIRST. Note that a file discovery
// DECLINES never reaches the parser and so never degrades anything: the check
// that matters is files_scanned, not the file being present on disk.

package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// astDegradedFixture writes a two-file corpus: one file that parses cleanly and
// one that tree-sitter can only error-recover. It reuses wireDirtyFile, the
// package's existing deliberately-ungrammatical Go source, rather than inventing
// a second broken snippet.
func astDegradedFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "clean.go"), []byte(wireCleanFile), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "dirty.go"), []byte(wireDirtyFile), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module fix\n\ngo 1.21\n"), 0o600))
	return dir
}

// astDegradedMissPattern finds nothing in either fixture file, so a zero total
// is a genuine no-match over a scanned corpus rather than an artifact of an
// empty one.
const astDegradedMissPattern = "func ZZZNoSuchName() {}"

func TestAstDegradedZeroWarning(t *testing.T) {
	t.Run("match_zero_with_degraded_files_warns", func(t *testing.T) {
		deps := astTestDeps{rootDir: astDegradedFixture(t), rootDirSet: true}
		body, isErr, _ := callAst(t, deps, `{"operation":"match","language":"go","pattern":"`+astDegradedMissPattern+`"}`)
		require.False(t, isErr, "match failed: %s", body)

		var out struct {
			Total int    `json:"total"`
			Hint  string `json:"hint"`
			Stats struct {
				FilesScanned         int `json:"files_scanned"`
				FilesWithParseErrors int `json:"files_with_parse_errors"`
			} `json:"stats"`
		}
		require.NoError(t, json.Unmarshal([]byte(body), &out))

		// The fixture's own preconditions, asserted before anything about the
		// hint, so a corpus that stopped degrading cannot pass these rows.
		require.Positive(t, out.Stats.FilesWithParseErrors,
			"fixture must actually degrade, or this row silently becomes a clean-corpus assertion")
		require.Greater(t, out.Stats.FilesScanned, 1, "both fixture files must reach the parser")
		require.Equal(t, 0, out.Total, "the pattern must find nothing, or the zero path is never taken")

		assert.Contains(t, out.Hint, "did not fully parse", "a zero over a degraded corpus must say so")
		assert.Contains(t, out.Hint, "NOT evidence of absence")
		assert.Contains(t, out.Hint, "no matches",
			"the ordinary zero guidance must survive alongside the warning — the usual causes are still live")
	})

	t.Run("count_zero_with_degraded_files_warns", func(t *testing.T) {
		deps := astTestDeps{rootDir: astDegradedFixture(t), rootDirSet: true}
		body, isErr, _ := callAst(t, deps, `{"operation":"count","language":"go","pattern":"`+astDegradedMissPattern+`"}`)
		require.False(t, isErr, "count failed: %s", body)

		var out struct {
			Total                int    `json:"total"`
			Hint                 string `json:"hint"`
			FilesScanned         int    `json:"files_scanned"`
			FilesWithParseErrors int    `json:"files_with_parse_errors"`
		}
		require.NoError(t, json.Unmarshal([]byte(body), &out))

		require.Positive(t, out.FilesWithParseErrors,
			"fixture must actually degrade, or this row silently becomes a clean-corpus assertion")
		require.Greater(t, out.FilesScanned, 1, "both fixture files must reach the parser")
		require.Equal(t, 0, out.Total, "the pattern must find nothing, or the zero path is never taken")

		// This is the shape the count path had no hint for at all: the counters
		// were in the payload and nothing said what they implied about the zero.
		assert.Contains(t, out.Hint, "did not fully parse", "a count-shaped zero over a degraded corpus must warn")
		assert.Contains(t, out.Hint, "NOT evidence of absence")
	})

	t.Run("match_zero_clean_corpus_keeps_empty_result_hint", func(t *testing.T) {
		deps := astTestDeps{rootDir: astFixtureRepo(t), rootDirSet: true}
		body, isErr, _ := callAst(t, deps, `{"operation":"match","language":"go","pattern":"`+astDegradedMissPattern+`"}`)
		require.False(t, isErr, "match failed: %s", body)

		var out struct {
			Hint  string `json:"hint"`
			Stats struct {
				FilesScanned         int `json:"files_scanned"`
				FilesWithParseErrors int `json:"files_with_parse_errors"`
			} `json:"stats"`
		}
		require.NoError(t, json.Unmarshal([]byte(body), &out))

		require.Positive(t, out.Stats.FilesScanned, "files must be scanned, or this is the zero-scan case instead")
		require.Equal(t, 0, out.Stats.FilesWithParseErrors, "the control corpus must parse cleanly")

		assert.Contains(t, out.Hint, "no matches", "a clean-corpus zero keeps the ordinary guidance")
		assert.NotContains(t, out.Hint, "did not fully parse",
			"nothing degraded, so the warning must not fire — a warning present on every zero says nothing")
	})

	t.Run("count_scanned_clean_corpus_stays_hintless", func(t *testing.T) {
		deps := astTestDeps{rootDir: astFixtureRepo(t), rootDirSet: true}
		body, isErr, _ := callAst(t, deps, `{"operation":"count","language":"go","pattern":"`+astDegradedMissPattern+`"}`)
		require.False(t, isErr, "count failed: %s", body)

		var out struct {
			Total                int    `json:"total"`
			Hint                 string `json:"hint"`
			FilesScanned         int    `json:"files_scanned"`
			FilesWithParseErrors int    `json:"files_with_parse_errors"`
		}
		require.NoError(t, json.Unmarshal([]byte(body), &out))

		require.Positive(t, out.FilesScanned)
		require.Equal(t, 0, out.FilesWithParseErrors, "the control corpus must parse cleanly")
		require.Equal(t, 0, out.Total)

		// count gains NO general scanned-but-no-match hint. Widening it to hint
		// on every zero would turn a landed gate red, and the only route back to
		// green would be deleting that gate.
		assert.Empty(t, out.Hint, "a clean-corpus count zero stays hintless")
	})
}
