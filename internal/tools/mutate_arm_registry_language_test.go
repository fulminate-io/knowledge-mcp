// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestMutateArmRegistry_LanguageClassifiedByGraphReachability asserts the
// `language` classification as a PARTITION over every arm rather than by
// sampling the arms an edit happened to touch.
//
// The rule it pins: an arm consumes `language` when it is reachable with a
// LANGUAGE-ADDRESSED graph, and ignores it when it is not. The knowledge family
// addresses one graph and carries no instance field, so every arm accounted
// below the knowledge-graph guard builds a Target with no language on it at all
// — the param is accepted and projected away, which is deliberate ignoring, not
// consumption. Rejecting it there would be a false rejection, because the schema
// advertises the param for the name-addressed families.
//
// SET EQUALITY IN ALL THREE LEGS, not containment and not a count. A count is
// satisfied by classifying the wrong arms; containment is satisfied by an
// over-reach that also converted a practice-reachable arm. The consumed leg is
// what makes this a classification rather than a sweep, and asserting the
// rejected class as well is what closes the partition — with only the first two
// legs, a new arm that classified `language` in NO set would leave both
// unchanged and pass.
//
// The tables below are not a second source of truth that rots: they sit beside
// the registry they describe, so a future arm reddens this test in the same edit
// that adds it — which is the moment to decide which class its language cell
// belongs in.
func TestMutateArmRegistry_LanguageClassifiedByGraphReachability(t *testing.T) {
	// Reachable ONLY with the knowledge family, whose resolver reads no instance
	// field: language selects nothing, so it is ignored with a justification.
	wantIgnored := map[armID]bool{
		armCreateContextLinked: true,
		armCreateFallthrough:   true,
		armCreateBatch:         true,
		armUpsert:              true,
		armUpdateTyped:         true,
		armUpdateFallthrough:   true,
		armUpdateBatchIDs:      true,
		armUpdateBatchItems:    true,
		armBulkUpdateMetadata:  true,
		armDelete:              true,
		armUnlink:              true,
	}
	// Reachable with a language-addressed graph (practice), so the param really
	// does route: the link block and the passthrough/non-knowledge arms all run
	// before or outside the knowledge-graph guard.
	wantConsumed := map[armID]bool{
		armLinkCrossGraph:          true,
		armLinkFallthrough:         true,
		armGraphPassthrough:        true,
		armNonKnowledgeFallthrough: true,
	}
	// The remaining arms, named rather than derived from the two sets above: a
	// length computed off the other tables would still balance if the same arm
	// fell out of two of them at once.
	wantRejected := map[armID]bool{
		armCriterionCreate: true,
		armCreateFinding:   true,
		armCreateResearch:  true,
		armCreateRule:      true,
		armUpdateBackend:   true,
		armUpdateRollup:    true,
		armAnswer:          true,
	}

	gotIgnored := map[armID]bool{}
	gotConsumed := map[armID]bool{}
	gotRejected := map[armID]bool{}
	for arm := range mutateArmRegistry {
		class, declared := paramClassFor(arm, "language")
		if !declared {
			t.Errorf("arm %s classifies language in NO set — the partition requires every arm to name it", arm)
			continue
		}
		switch class {
		case classDeliberatelyIgnored:
			gotIgnored[arm] = true
		case classConsumed:
			gotConsumed[arm] = true
		case classRejected:
			gotRejected[arm] = true
		}
	}

	assert.Equal(t, wantIgnored, gotIgnored,
		"the deliberately-ignored set must be exactly the knowledge-only arms")
	assert.Equal(t, wantConsumed, gotConsumed,
		"the consumed set must be exactly the arms a language-addressed graph can reach")

	// The third class closes the partition: every arm lands in exactly one of the
	// three named sets, and the loop above has already failed any arm that landed
	// in none of them.
	assert.Equal(t, wantRejected, gotRejected,
		"the rejected set must be exactly the remaining arms")
	assert.Len(t, mutateArmRegistry, len(wantIgnored)+len(wantConsumed)+len(wantRejected),
		"the three named sets must account for every registered arm")
}
