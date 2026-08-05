// SPDX-License-Identifier: Apache-2.0

// ast_count_wire_test.go — the wire-shape gate for operation=count. Sibling to
// ast_replace_wire_test.go, scoped to the count family this plan's body-free
// counting change created.
//
// The point of the plan's count change is that NOTHING on the wire moves: the
// handler stops rebuilding by_file/by_kind from a retained []RawMatch and reads
// a pre-aggregated tally instead, and the caller must not be able to tell. This
// gate proves that by comparing the live response's top-level key set against
// the set frozen BEFORE the change (testdata/ast_count_response_keys.txt).
// Consuming the frozen artifact rather than hardcoding the post-change keys is
// what keeps the gate honest — a hardcoded set would assert the new behavior
// against itself.

package tools

import (
	"encoding/json"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// readCountKeyArtifact loads the frozen count-response key set, one key per
// line, sorted.
func readCountKeyArtifact(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile("testdata/ast_count_response_keys.txt")
	require.NoError(t, err, "the frozen count-response key artifact must exist")
	var keys []string
	for line := range strings.SplitSeq(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		keys = append(keys, line)
	}
	require.NotEmpty(t, keys, "the frozen artifact must carry keys")
	sort.Strings(keys)
	return keys
}

// TestHandleAstCount_ResponseKeySetUnchanged drives a matching count over the
// context fixture (java `$T $N = $V;` matches two constructs, so FilesScanned>0
// and every member compiles — neither conditional key hint nor pattern_errors
// is present) and asserts its top-level key set is exactly the frozen set. Any
// key added, dropped or renamed by the count-path change fails here.
func TestHandleAstCount_ResponseKeySetUnchanged(t *testing.T) {
	dir := astContextFixtureRepo(t)
	deps := astTestDeps{rootDir: dir, rootDirSet: true}

	body, isErr, handled := callAst(t, deps, `{"operation":"count","language":"java","pattern":"$T $N = $V;"}`)
	require.True(t, handled)
	require.False(t, isErr, "count failed: %s", body)

	var resp map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(body), &resp))

	// Guard the premise: a matching pattern with neither conditional key, so the
	// captured set is the unconditional one the artifact froze.
	require.NotContains(t, resp, "hint", "fixture must scan files so the zero-scan hint is absent")
	require.NotContains(t, resp, "pattern_errors", "fixture pattern must compile so pattern_errors is absent")

	got := make([]string, 0, len(resp))
	for k := range resp {
		got = append(got, k)
	}
	sort.Strings(got)

	want := readCountKeyArtifact(t)
	assert.Equal(t, want, got,
		"count response key set drifted from testdata/ast_count_response_keys.txt — the count-path change must not touch the wire")
}
