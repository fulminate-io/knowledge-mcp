// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestFlowVocabularyLockstep pins the three flow edge types AND the flow
// Evidence prefix across the two CLIENT vocabularies.
//
// TWO CLIENT MIRRORS EXIST because no hand-written package is shared across the
// two binaries, so the chunker carries its own EdgeType vocabulary beside
// kgtypes'. Mirrors drift; this is what stops them.
//
// HONEST LABELS, because a guard claimed as a red-first assertion is a false
// statement nobody re-runs: this test is RED-FIRST. It is the ONLY gate on the
// two spellings — no compiler sees across the two packages, because package
// treesitter deliberately imports no kgtypes — so changing either side alone
// turns it red.
//
// THE TWO CHARACTERIZATION GUARDS ARE IN THE OTHER HALF.
// server_has_no_nontest_ref and calls_control_fires walk knowledge-server's
// tree and live in chunker_flow_vocab_server_test.go, which the sync script
// removes from the published tree; the mirror is the client module alone, and
// the control leg REQUIRES the server tree to carry the "CALLS" literal. Both
// halves run here on every staging run.
func TestFlowVocabularyLockstep(t *testing.T) {
	root := repoRoot(t)

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

		// MODULE-RELATIVE, paired with the module-root anchor above, for the
		// reason the sibling lockstep states: kgtypes sits under the module root
		// in both layouts, and only a module-relative spelling names it in both.
		body := readCensusFile(t, root, "internal/kgtypes/edge_types.go")
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
}
