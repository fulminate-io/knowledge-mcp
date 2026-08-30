// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFlowVocabularyLockstep pins the three flow edge types AND the flow
// Evidence prefix across the two CLIENT vocabularies, and against the server
// module.
//
// TWO CLIENT MIRRORS EXIST because no hand-written package is shared across the
// two binaries, so the chunker carries its own EdgeType vocabulary beside
// kgtypes'. Mirrors drift; this is what stops them.
//
// HONEST LABELS, because a guard claimed as a red-first assertion is a false
// statement nobody re-runs:
//
//   - client_mirrors_agree is RED-FIRST. It is the ONLY gate on the two
//     spellings — no compiler sees across the two packages, because package
//     treesitter deliberately imports no kgtypes. Change either side alone and
//     this subtest goes red.
//   - server_has_no_nontest_ref and calls_control_fires are CHARACTERIZATION
//     GUARDS. They pass in every state of a correct tree; what they catch is a
//     later change that quietly gives the server a stake in this vocabulary.
func TestFlowVocabularyLockstep(t *testing.T) {
	root := repoRootForCensus(t)

	t.Run("client_mirrors_agree", func(t *testing.T) {
		// The chunker's own constants are the ones under test here; the kgtypes
		// side is read from source because this package cannot import it without
		// a dependency it does not otherwise need.
		//
		// FOUR PAIRS, NOT THREE. EdgeEvidenceFlowPrefix is mirrored for a reason
		// the three edge types do not share: chunker_emit.go renders every flow
		// edge's Evidence from the treesitter copy, while external consumers
		// parse it against the kgtypes declaration. Those two drifting apart is
		// precisely what this leg exists to catch.
		assert.Equal(t, EdgeFlowsToReturn, EdgeType("FLOWS_TO_RETURN"),
			"the chunker's mirror carries the wire literal verbatim")
		assert.Equal(t, EdgeFlowsToArg, EdgeType("FLOWS_TO_ARG"),
			"the chunker's mirror carries the wire literal verbatim")
		assert.Equal(t, EdgeFlowsToField, EdgeType("FLOWS_TO_FIELD"),
			"the chunker's mirror carries the wire literal verbatim")
		assert.Equal(t, EdgeEvidenceFlowPrefix, "flow:",
			"the chunker's Evidence prefix carries the wire literal verbatim")

		body := readCensusFile(t, root, "cmd/knowledge/internal/kgtypes/edge_types.go")
		for _, decl := range []string{
			`EdgeFlowsToReturn EdgeType = "FLOWS_TO_RETURN"`,
			`EdgeFlowsToArg    EdgeType = "FLOWS_TO_ARG"`,
			`EdgeFlowsToField  EdgeType = "FLOWS_TO_FIELD"`,
			`EdgeEvidenceFlowPrefix = "flow:"`,
		} {
			assert.Contains(t, body, decl,
				"kgtypes declares the same literal — the producer's vocabulary and the "+
					"chunker's must not diverge")
		}
	})

	t.Run("server_has_no_nontest_ref", func(t *testing.T) {
		// THE SCOPE IS NON-TEST FILES, for the reason
		// TestImplementsVocabularyLockstep's own scoped leg gives: the server's
		// edge-type block is a vocabulary rather than a validator, and its decode
		// path converts every requested edge type with a bare cast and applies no
		// allowlist. So an empty result here is the structural proof that this
		// ticket needs no server change at all.
		//
		// Deliberately case-sensitive, matching the sibling: the lowercase word
		// appears in prose across many further files.
		for _, literal := range []string{"FLOWS_TO_RETURN", "FLOWS_TO_ARG", "FLOWS_TO_FIELD"} {
			hits := serverNonTestFilesNaming(t, root, literal)
			assert.Empty(t, hits,
				"no server non-test file needs the %s constant; adding one to a server "+
					"vocabulary the client never reads would be ceremony", literal)
		}
	})

	t.Run("calls_control_fires", func(t *testing.T) {
		// THE ONLY THING SEPARATING A REAL ABSENCE FROM A PROBE THAT MATCHES
		// NOTHING. If the walk, the path or the filter broke, this fires.
		hits := serverNonTestFilesNaming(t, root, `"CALLS"`)
		require.NotEmpty(t, hits,
			"control: the same walk DOES find the CALLS literal in server non-test files, so the "+
				"empty flow results above are a real absence rather than a broken probe")
	})
}
