// SPDX-License-Identifier: Apache-2.0

// capture_ref_refusal_test.go — the STORED-CHECK arm of the pre-walk
// capture-reference refusal.
//
// WHY THIS ARM NEEDS ITS OWN TEST RATHER THAN RIDING THE ast PACKAGE'S. The ast
// tool handlers refuse an undeclared capture before they walk, but a stored
// check never goes through them: it is validated at admission and executed by
// the corpus scan. Both of those paths would otherwise walk the whole corpus
// and report the clean zero a correct check that found nothing reports — and on
// a check whose job IS to report an absence, that zero is the answer the caller
// reads.

package corpus

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// undeclaredCaptureWhere names "T.X" while the sub-pattern leaf declares no
// `as`, so nothing in the tree can ever bind it. It is one character away from
// a tree that WOULD bind — adding `"as":"T"` — which is what makes the refusal
// worth having rather than a typo any reader would spot.
const undeclaredCaptureWhere = `{"all":[
	{"contains_pattern":{"of":"$match","pattern":"$Y.Close()"}},
	{"matches":{"of":"T.Y","regex":"^db$"}}
]}`

// declaredCaptureWhere is the SAME tree with the declaration present. It is the
// same-run known positive: without it, a refusal that fired on every where-tree
// carrying a sub-pattern would pass the row above.
const declaredCaptureWhere = `{"all":[
	{"contains_pattern":{"of":"$match","pattern":"$Y.Close()","as":"T"}},
	{"matches":{"of":"T.Y","regex":"^db$"}}
]}`

func TestValidateFixtures_RefusesAWhereTreeNamingACaptureNothingDeclares(t *testing.T) {
	err := ValidateFixtures(context.Background(), astCheck(undeclaredCaptureWhere),
		badFixture(badBody), goodFixture(goodBody))
	if err == nil {
		t.Fatal("a stored check naming a capture nothing declares must be refused, not walked to a silent zero")
	}
	if !errors.Is(err, ErrFixtureValidation) {
		t.Errorf("want ErrFixtureValidation, got %v", err)
	}
	if !strings.Contains(err.Error(), "T.Y") {
		t.Errorf("refusal %q does not name the reference the author wrote", err.Error())
	}
}

func TestValidateFixtures_AcceptsTheSameTreeOnceTheHandleIsDeclared(t *testing.T) {
	// The same-run known positive for the row above: adding `as:"T"` makes the
	// identical reference legal, and the pair then admits normally (fires on
	// the bad body, silent on the good one, which differ in the receiver name
	// alone).
	err := ValidateFixtures(context.Background(), astCheck(declaredCaptureWhere),
		badFixture(badBody), goodFixture(goodBody))
	if err != nil {
		t.Fatalf("the declared form must admit: %v", err)
	}
}

func TestValidateCheck_RefusesAnUndeclaredCaptureAtAdmission(t *testing.T) {
	// The second stored-check site: admission validation, which runs before any
	// fixture is materialized. A check that cannot be executed must never enter
	// the graph in the first place.
	err := validateAstBody(astCheck(undeclaredCaptureWhere))
	if err == nil {
		t.Fatal("admission must refuse a where-tree naming a capture nothing declares")
	}
	if !strings.Contains(err.Error(), "T.Y") {
		t.Errorf("refusal %q does not name the reference the author wrote", err.Error())
	}
	if err := validateAstBody(astCheck(declaredCaptureWhere)); err != nil {
		t.Errorf("the declared form must pass admission: %v", err)
	}
}
