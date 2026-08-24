// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestImplementsVocabularyLockstep pins the IMPLEMENTS edge type across the two
// CLIENT vocabularies and against the server module.
//
// TWO CLIENT MIRRORS EXIST because no hand-written package is shared across the
// two binaries, so the chunker carries its own EdgeType vocabulary beside
// kgtypes'. Mirrors drift; this is what stops them.
func TestImplementsVocabularyLockstep(t *testing.T) {
	root := repoRootForCensus(t)

	t.Run("client_mirrors_agree", func(t *testing.T) {
		// The chunker's own constant is the one under test here; the kgtypes side
		// is read from source because this package cannot import it without a
		// dependency it does not otherwise need.
		assert.Equal(t, EdgeImplements, EdgeType("IMPLEMENTS"),
			"the chunker's mirror carries the wire literal verbatim")

		body := readCensusFile(t, root, "cmd/knowledge/internal/kgtypes/edge_types.go")
		assert.Contains(t, body, `EdgeImplements EdgeType = "IMPLEMENTS"`,
			"kgtypes declares the same literal — the producer's vocabulary and the chunker's "+
				"must not diverge")
		assert.Contains(t, body, "EdgeMethodMethodSet",
			"and the method-set carrier's prefix lives beside the other Edge.Method values, "+
				"where a reader finds it without importing the producer")
	})

	t.Run("server_has_no_nontest_ref", func(t *testing.T) {
		// THE SCOPE IS NON-TEST FILES, AND THE SCOPING IS LOAD-BEARING RATHER THAN
		// INCIDENTAL. The server module ALREADY names IMPLEMENTS in several
		// traversal test fixtures, so an unscoped "no server reference" assertion
		// could only ever pass while this work was undone — it would fail against
		// correct code. What the scoped leg actually asserts is that no server
		// NON-TEST file needs the constant: the server's edge-type block is a
		// vocabulary rather than a validator, and its decode path converts every
		// requested edge type with a bare cast and applies no allowlist.
		//
		// It is deliberately NOT case-insensitive: "implements" is the lowercase
		// KNOWLEDGE-graph edge type, and many further files carry the word in prose.
		hits := serverNonTestFilesNaming(t, root, "IMPLEMENTS")
		assert.Empty(t, hits,
			"no server non-test file needs the IMPLEMENTS constant; adding one to a server "+
				"vocabulary the client never reads would be ceremony")
	})

	t.Run("calls_control_fires", func(t *testing.T) {
		// THE ONLY THING SEPARATING A REAL ABSENCE FROM A PROBE THAT MATCHES
		// NOTHING. If the walk, the path or the filter broke, this fires.
		hits := serverNonTestFilesNaming(t, root, `"CALLS"`)
		require.NotEmpty(t, hits,
			"control: the same walk DOES find the CALLS literal in server non-test files, so the "+
				"empty IMPLEMENTS result above is a real absence rather than a broken probe")
	})
}

// readCensusFile reads one repo-relative file.
func readCensusFile(t *testing.T, root, rel string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel))) //nolint:gosec // this repo's own tree
	require.NoError(t, err, "reading %s", rel)
	return string(body)
}

// serverNonTestFilesNaming returns the server-module non-test .go files
// containing a literal.
func serverNonTestFilesNaming(t *testing.T, root, literal string) []string {
	t.Helper()
	var hits []string
	walkRoot := filepath.Join(root, "cmd", "knowledge-server")
	require.DirExists(t, walkRoot, "control: the server module tree exists")
	err := filepath.WalkDir(walkRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		body, readErr := os.ReadFile(path) //nolint:gosec // walks this repo's own source tree
		if readErr != nil {
			return readErr
		}
		if strings.Contains(string(body), literal) {
			rel, _ := filepath.Rel(root, path)
			hits = append(hits, filepath.ToSlash(rel))
		}
		return nil
	})
	require.NoError(t, err)
	return hits
}
