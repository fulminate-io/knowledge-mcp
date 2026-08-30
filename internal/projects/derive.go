// SPDX-License-Identifier: Apache-2.0

package projects

import (
	"strings"
)

// DeriveCriterionName produces the auto-derived NAME for a criterion node from
// its description. It is the single source for the Name==Description convention
// the three name-derivation sites share (BuildCriterionNode, upsertCriterionNode,
// and the per-type update router's criterion re-stamp), so the persisted name
// cannot drift between them.
//
// IT CLAMPS TO THE FIRST LINE. A criterion's description is routinely a
// multi-line block — the assertion, then the command, then the reasoning — and
// carrying the whole block into the name made the name unusable as a label
// wherever a name is rendered as one. Authors were already working around it by
// leading the description with a one-line claim so the first line read as the
// pass condition; this makes that the behavior instead of a convention nobody
// stated. A single-line description is returned byte-identical, which is why
// every existing single-line derivation is unchanged.
//
// THE ALTERNATIVE, AND WHY IT IS NOT THIS. The other direction is to accept a
// caller-supplied `name` on a criterion update. It was rejected on blast radius,
// not taste: update and create must agree about whether a criterion may carry an
// independent name (intercept_mutate_update.go states that rule), so opening
// update forces the criterion CREATE arm open too — armCriterionCreate's rejected
// set, justifyCriterionDerived, and criterionCreateArgs, which deliberately
// carries no Name field. The clamp changes no contract and removes no rejection.
//
// Trailing whitespace on the first line is trimmed; leading blank lines are
// skipped so a description that opens with a newline still yields a name.
func DeriveCriterionName(description string) string {
	for line := range strings.SplitSeq(description, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return trimmed
		}
	}
	// Whitespace-only (or empty) description: return it verbatim rather than
	// inventing a name. The callers' own validators own the empty-description
	// rejection; silently substituting something here would hide it.
	return description
}
