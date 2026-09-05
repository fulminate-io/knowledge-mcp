// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestImplementsVocabularyLockstep pins the IMPLEMENTS edge type across the two
// CLIENT vocabularies.
//
// TWO CLIENT MIRRORS EXIST because no hand-written package is shared across the
// two binaries, so the chunker carries its own EdgeType vocabulary beside
// kgtypes'. Mirrors drift; this is what stops them. It is the ONLY gate on the
// two spellings — no compiler sees across the two packages, because package
// treesitter deliberately imports no kgtypes — so it is RED-FIRST rather than a
// characterization guard.
//
// THE SERVER-SUBJECT LEGS ARE IN THE OTHER HALF. The two subtests that walk
// knowledge-server's tree (its absence of a non-test reference, and the CALLS
// known-positive that proves that absence is real) live in
// chunker_implements_vocab_server_test.go, which the sync script removes from
// the published tree: the mirror is the client module alone, and the control
// leg REQUIRES the server tree to carry the "CALLS" literal, so it cannot pass
// where there is no server tree. Both halves run here on every staging run.
func TestImplementsVocabularyLockstep(t *testing.T) {
	root := repoRoot(t)

	t.Run("client_mirrors_agree", func(t *testing.T) {
		// The chunker's own constant is the one under test here; the kgtypes side
		// is read from source because this package cannot import it without a
		// dependency it does not otherwise need.
		assert.Equal(t, EdgeImplements, EdgeType("IMPLEMENTS"),
			"the chunker's mirror carries the wire literal verbatim")

		// MODULE-RELATIVE, paired with the module-root anchor above: kgtypes is
		// cmd/knowledge/internal/kgtypes here and internal/kgtypes in the
		// published mirror, and one spelling relative to the module root names
		// it in both. A repo-relative spelling would fail in BOTH layouts once
		// the anchor moved, since the module root already carries the
		// cmd/knowledge prefix.
		body := readCensusFile(t, root, "internal/kgtypes/edge_types.go")
		assert.Contains(t, body, `EdgeImplements EdgeType = "IMPLEMENTS"`,
			"kgtypes declares the same literal — the producer's vocabulary and the chunker's "+
				"must not diverge")
		assert.Contains(t, body, "EdgeMethodMethodSet",
			"and the method-set carrier's prefix lives beside the other Edge.Method values, "+
				"where a reader finds it without importing the producer")
	})
}

// readCensusFile reads one file under root, named relative to it.
func readCensusFile(t *testing.T, root, rel string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel))) //nolint:gosec // this repo's own tree
	require.NoError(t, err, "reading %s", rel)
	return string(body)
}
