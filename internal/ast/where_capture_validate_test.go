// SPDX-License-Identifier: Apache-2.0

// where_capture_validate_test.go — the pre-walk capture-reference refusal.
//
// THE SILENCE THIS REMOVES, pinned by the first two rows as a pair rather than
// asserted in prose: a where-tree naming a capture nothing declares errors
// LOUDLY over a corpus the outer pattern hits and returns a CLEAN ZERO over one
// it misses, because capture references resolve only inside evalWhere. The
// second reading is indistinguishable from a correct search that found nothing,
// and on a stored corpus check it is the answer the caller reads.

package ast

import (
	"errors"
	"strings"
	"testing"
)

// mustWhere parses a where-tree for a validator row, failing the test rather
// than the validator when the JSON itself is wrong.
func mustWhere(t *testing.T, whereJSON string) *WhereNode {
	t.Helper()
	w, err := ParseWhere([]byte(whereJSON))
	if err != nil {
		t.Fatalf("ParseWhere(%s): %v", whereJSON, err)
	}
	return w
}

const undeclaredRefWhere = `{"all": [
	{"contains_pattern": {"of": "$match", "pattern": "$R.Close()"}},
	{"equals": {"of": "T.R", "value": "x"}}
]}`

func TestCaptureRefs_TheSilentArmIsSilentWithoutTheValidator(t *testing.T) {
	// Row (a), first half — THE RED. The same where-tree over a target the
	// outer pattern MISSES returns zero matches and no error at all. This is
	// the observation the pre-walk refusal exists to convert into an error;
	// the row is here so a later reader can see that the silence is real
	// rather than take the validator's word for it.
	got, err := runWhere(t, "$X.Close()", "package main\nfunc f() { g() }", undeclaredRefWhere)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != 0 {
		t.Fatalf("matches = %d, want 0", got)
	}
	// And the same-run KNOWN POSITIVE (row (b)): over a target the pattern DOES
	// match, the identical reference errors. One tree, two corpora, two
	// different answers — which is exactly why the walk cannot be the gate.
	_, err = runWhere(t, "$X.Close()", exportTwoCloses, undeclaredRefWhere)
	if !errors.Is(err, errCaptureUnresolved) {
		t.Fatalf("err = %v; want errors.Is errCaptureUnresolved on the matching corpus", err)
	}
}

func TestCaptureRefs_UndeclaredReferenceIsRefusedBeforeTheWalk(t *testing.T) {
	// Row (a), second half — THE GREEN. The validator refuses the same tree
	// with no corpus at all, and names the reference the caller wrote.
	pat, err := Parse("$X.Close()")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	err = ValidateWhereCaptureRefs(mustWhere(t, undeclaredRefWhere), pat)
	if err == nil {
		t.Fatal("expected a refusal, got nil")
	}
	if !errors.Is(err, errCaptureUndeclared) {
		t.Errorf("err = %v; want errors.Is errCaptureUndeclared", err)
	}
	for _, want := range []string{`"T.R"`, "equals", "of"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err message = %v; want substring %q", err, want)
		}
	}
}

func TestCaptureRefs_DeclaredNamespaceIsAcceptedEvenWhenTheWalkWouldMatchNothing(t *testing.T) {
	// Row (c). THE CONTROL THAT KEEPS THE REFUSAL FROM FIRING ON EVERYTHING.
	// `as:"T"` is declared and the sub-pattern binds $R, so "T.R" is a name
	// the tree can bind — whether any corpus contains it is not this
	// function's question, exactly as ValidateWhereKinds validates a kind
	// against the grammar and never against the corpus.
	pat, err := Parse("$X.Close()")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	where := mustWhere(t, `{"all": [
		{"contains_pattern": {"of": "$match", "pattern": "$R.Close()", "as": "T"}},
		{"equals": {"of": "T.R", "value": "x"}},
		{"equals": {"of": "T.$match", "value": "x.Close()"}},
		{"equals": {"of": "T", "value": "x.Close()"}}
	]}`)
	if err := ValidateWhereCaptureRefs(where, pat); err != nil {
		t.Errorf("ValidateWhereCaptureRefs = %v, want nil", err)
	}
}

func TestCaptureRefs_MatchAndEveryOuterDepthPassTheValidator(t *testing.T) {
	// Row (d). "$match" is declared in every scope, and the `$outer.` chain
	// resolves against the same scope chain resolveCapture walks — including
	// the namespaced key one level up, which is the form R1 adds.
	pat, err := Parse("$X.Close()")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	where := mustWhere(t, `{"all": [
		{"kind": {"of": "$match", "is": "call_expression"}},
		{"contains_pattern": {"of": "$match", "pattern": "$R.Close()", "as": "T"}},
		{"contains_pattern": {"of": "$match", "pattern": "$S.Close()", "where": {"all": [
			{"same_text": {"captures": ["S", "$outer.X"]}},
			{"same_text": {"captures": ["S", "$outer.T.R"]}},
			{"kind": {"of": "$match", "is": "call_expression"}},
			{"contains_pattern": {"of": "$match", "pattern": "$U", "where": {
				"same_text": {"captures": ["U", "$outer.$outer.T.R"]}}}}
		]}}}
	]}`)
	if err := ValidateWhereCaptureRefs(where, pat); err != nil {
		t.Errorf("ValidateWhereCaptureRefs = %v, want nil", err)
	}
}

func TestCaptureRefs_OuterDepthBeyondTheChainIsRefused(t *testing.T) {
	// The discriminating half of row (d): a depth the chain does not have is
	// refused, so "every $outer. depth passes" is not the validator ignoring
	// the prefix.
	pat, err := Parse("$X.Close()")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	err = ValidateWhereCaptureRefs(mustWhere(t,
		`{"same_node": {"captures": ["X", "$outer.X"]}}`), pat)
	if !errors.Is(err, errCaptureUndeclared) {
		t.Errorf("err = %v; want errors.Is errCaptureUndeclared", err)
	}
}

func TestCaptureRefs_EveryMemberOfAnAlternationIsValidated(t *testing.T) {
	// A patterns[] alternation shares ONE where-tree, so a reference declared
	// by the first member and not the second is a run-time error the moment
	// the second matches. Validating only the first would leave exactly the
	// silence this function removes.
	first, err := Parse("$X.Close()")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	second, err := Parse("$Y.Shutdown()")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	where := mustWhere(t, `{"equals": {"of": "X", "value": "x"}}`)
	if err := ValidateWhereCaptureRefs(where, first); err != nil {
		t.Fatalf("first member alone should pass: %v", err)
	}
	err = ValidateWhereCaptureRefs(where, first, second)
	if !errors.Is(err, errCaptureUndeclared) {
		t.Errorf("err = %v; want errors.Is errCaptureUndeclared naming X, undeclared in the second member", err)
	}
}

func TestCaptureRefs_EmptyReferenceIsRefused(t *testing.T) {
	// An omitted `of` names nothing for exactly the reason a misspelled one
	// does. The leaf-specific validators give a better message where one
	// exists, which is why ValidateWhereFlowArms runs first at every call site
	// carrying both; this is the backstop for the leaves that have none.
	pat, err := Parse("$X.Close()")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	err = ValidateWhereCaptureRefs(mustWhere(t, `{"kind": {"is": "identifier"}}`), pat)
	if err == nil {
		t.Fatal("expected a refusal for an empty `of`, got nil")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Errorf("err = %v; want it to name the empty reference", err)
	}
}

func TestCaptureRefs_NilTreeIsNoFilter(t *testing.T) {
	// Same nil semantics as ValidateWhereKinds and ValidateWhereFlowArms.
	pat, err := Parse("$X.Close()")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if err := ValidateWhereCaptureRefs(nil, pat); err != nil {
		t.Errorf("ValidateWhereCaptureRefs(nil) = %v, want nil", err)
	}
}

func TestCaptureRefs_RefusalNamesTheVocabularyItWouldAccept(t *testing.T) {
	// The message is the discovery surface for the namespaced form, so it must
	// show the COMPOSITE keys rather than only the bare handles — a caller who
	// wrote "T.C" and meant "T.R" learns the name from the refusal.
	pat, err := Parse("$X.Close()")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	err = ValidateWhereCaptureRefs(mustWhere(t, `{"all": [
		{"contains_pattern": {"of": "$match", "pattern": "$R.Close()", "as": "T"}},
		{"equals": {"of": "T.C", "value": "x"}}
	]}`), pat)
	if err == nil {
		t.Fatal("expected a refusal, got nil")
	}
	for _, want := range []string{"T.R", "T.$match", "$match", "X"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err message = %v; want the declared vocabulary to include %q", err, want)
		}
	}
}
