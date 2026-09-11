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
// The rule it pins: an arm consumes `language` when a graph it serves actually
// READS the param, and ignores it when the param is advertised on the arm's
// schema but selects nothing there. NO family is language-ADDRESSED any more —
// practice was the last one and its eight instance-keyed graphs are retired — so
// the only reader left is the CHECKS node body, where `language` is the check's
// corpus language. The knowledge family addresses one graph and carries no
// instance field, so every arm accounted below the knowledge-graph guard builds
// a Target with no language on it at all: the param is accepted and projected
// away, which is deliberate ignoring, not consumption. Rejecting it there would
// be a false rejection, because the schema advertises the param for the families
// whose bodies read it.
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
	// Reachable with a graph whose WRITE BODY reads the param, so it really does
	// route: on the checks graph `language` is the check node's corpus language,
	// and both arms below serve checks writes outside the knowledge-graph guard.
	//
	// THE RULE CHANGED WHEN PRACTICE STOPPED BEING LANGUAGE-ADDRESSED. It used to
	// be "reachable with a language-addressed graph", and practice was the only
	// one; the eight instance-keyed practice graphs were retired and `language`
	// addresses no graph on any family, so what is left is the checks body's own
	// reading of it. That is why the two LINK arms moved out of this set: an edge
	// arm carries no node body, so there is no corpus language for it to read.
	wantConsumed := map[armID]bool{
		armGraphPassthrough:        true,
		armNonKnowledgeFallthrough: true,
	}
	// The remaining arms, named rather than derived from the two sets above: a
	// length computed off the other tables would still balance if the same arm
	// fell out of two of them at once.
	wantRejected := map[armID]bool{
		armLinkCrossGraph:  true,
		armLinkFallthrough: true,
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
