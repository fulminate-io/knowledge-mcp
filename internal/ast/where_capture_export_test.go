// SPDX-License-Identifier: Apache-2.0

// where_capture_export_test.go — the namespaced sub-pattern capture export.
//
// A sub-pattern leaf carrying `as:"T"` over a pattern that binds `$C` writes
// "T.C" into the calling scope, so a SIBLING leaf can name the sub-pattern's
// capture. These rows cover the reference form's input classes: declared,
// declared-namespace-unknown-capture, undeclared namespace, no `as` at all,
// the `$outer.` chain, the collision the namespace exists to prevent, the
// first-candidate rule it inherits, the `not` interaction it inherits, and the
// per-match scope isolation.
//
// Every target is an inline string constant, as the rest of this suite's
// where-tree rows are: the fixtures travel with the test.

package ast

import (
	"errors"
	"strings"
	"testing"
)

// exportTwoCloses is the two-call target most rows below run against: two
// matches of `$X.Close()`, so a row can assert what the SECOND match sees.
const exportTwoCloses = "package main\nfunc f() { x.Close(); y.Close() }"

func TestExport_NamespacedReferenceResolvesFromASiblingLeaf(t *testing.T) {
	// Row 1. The declared form: a contains_pattern binding $R under as:"T",
	// then a sibling leaf naming "T.R". Before the export this reference was
	// refused as unresolvable.
	got, err := runWhere(t, "$X.Close()", exportTwoCloses,
		`{"all": [
			{"contains_pattern": {"of": "$match", "pattern": "$R.Close()", "as": "T"}},
			{"equals": {"of": "T.R", "value": "x"}}
		]}`)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != 1 {
		t.Errorf("matches = %d, want 1 (only the x.Close() match has T.R == \"x\")", got)
	}
}

func TestExport_NamespacedReferenceAlsoCarriesNodeIdentity(t *testing.T) {
	// The export writes nodeByName as well as byName, so same_node across the
	// namespace works. Without the node half this leaf would fall back to a
	// byte-span comparison and pass for the wrong reason, so the row asserts
	// the discriminating direction too: T.R and the OUTER X are the same node
	// here, and T.R against a different capture is not.
	got, err := runWhere(t, "$X.Close()", exportTwoCloses,
		`{"all": [
			{"contains_pattern": {"of": "$match", "pattern": "$R.Close()", "as": "T"}},
			{"same_node": {"captures": ["X", "T.R"]}}
		]}`)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != 2 {
		t.Errorf("matches = %d, want 2 (the sub-pattern binds the same receiver node the outer pattern did)", got)
	}
}

func TestExport_DeclaredNamespaceUnknownCaptureIsRefusedByName(t *testing.T) {
	// Row 2. `as:"T"` is declared but the sub-pattern binds no $NOPE, so the
	// reference is refused and the message names the reference the caller
	// wrote — not the bare capture and not the namespace.
	_, err := runWhere(t, "$X.Close()", exportTwoCloses,
		`{"all": [
			{"contains_pattern": {"of": "$match", "pattern": "$R.Close()", "as": "T"}},
			{"equals": {"of": "T.NOPE", "value": "x"}}
		]}`)
	if err == nil {
		t.Fatal("expected unresolved-capture error, got nil")
	}
	if !errors.Is(err, errCaptureUnresolved) {
		t.Errorf("err = %v; want errors.Is errCaptureUnresolved", err)
	}
	if !strings.Contains(err.Error(), "T.NOPE") {
		t.Errorf("err message = %v; want it to name %q", err, "T.NOPE")
	}
}

func TestExport_UndeclaredNamespaceIsRefusedByName(t *testing.T) {
	// Row 3. No leaf declares as:"Q", so "Q.R" names nothing at all.
	_, err := runWhere(t, "$X.Close()", exportTwoCloses,
		`{"all": [
			{"contains_pattern": {"of": "$match", "pattern": "$R.Close()", "as": "T"}},
			{"equals": {"of": "Q.R", "value": "x"}}
		]}`)
	if err == nil {
		t.Fatal("expected unresolved-capture error, got nil")
	}
	if !errors.Is(err, errCaptureUnresolved) {
		t.Errorf("err = %v; want errors.Is errCaptureUnresolved", err)
	}
	if !strings.Contains(err.Error(), "Q.R") {
		t.Errorf("err message = %v; want it to name %q", err, "Q.R")
	}
}

func TestExport_LeafWithNoAsExportsNothing(t *testing.T) {
	// Row 4. THE SCOPING PROOF. A sub-pattern leaf that declares no namespace
	// exports nothing, so its capture is unreachable by ANY spelling — a bare
	// "R" is still refused. Without this row the export could be leaking every
	// sub-pattern capture flat into the caller's scope and rows 1-3 would all
	// still pass.
	_, err := runWhere(t, "$X.Close()", exportTwoCloses,
		`{"all": [
			{"contains_pattern": {"of": "$match", "pattern": "$R.Close()"}},
			{"equals": {"of": "R", "value": "x"}}
		]}`)
	if err == nil {
		t.Fatal("expected unresolved-capture error, got nil")
	}
	if !errors.Is(err, errCaptureUnresolved) {
		t.Errorf("err = %v; want errors.Is errCaptureUnresolved", err)
	}
	if !strings.Contains(err.Error(), `"R"`) {
		t.Errorf("err message = %v; want it to name the bare capture R", err)
	}
}

func TestExport_OuterChainReachesTheNamespaceFromANestedWhere(t *testing.T) {
	// Row 5. A namespaced key written at the OUTER level, referenced from
	// inside a nested sub-pattern's own where as "$outer.T.R". resolveCapture
	// strips one `$outer.` and finds the literal key in the parent scope, so
	// the new form composes with the existing chain for free.
	got, err := runWhere(t, "$X.Close()", exportTwoCloses,
		`{"all": [
			{"contains_pattern": {"of": "$match", "pattern": "$R.Close()", "as": "T"}},
			{"contains_pattern": {"of": "$match", "pattern": "$S.Close()",
				"where": {"same_text": {"captures": ["S", "$outer.T.R"]}}}}
		]}`)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != 2 {
		t.Errorf("matches = %d, want 2", got)
	}
}

func TestExport_OuterChainRefusesADepthTheScopeDoesNotHave(t *testing.T) {
	// The control on the row above: one `$outer.` resolves, two do not, so the
	// chain is really being walked rather than the prefix being ignored. Note
	// the depth-two spelling is `$outer.$outer.` — one token per level — which
	// TestWhere_OuterDepthTwoSpellingIsRepeatedTokens pins.
	_, err := runWhere(t, "$X.Close()", exportTwoCloses,
		`{"all": [
			{"contains_pattern": {"of": "$match", "pattern": "$R.Close()", "as": "T"}},
			{"contains_pattern": {"of": "$match", "pattern": "$S.Close()",
				"where": {"same_text": {"captures": ["S", "$outer.$outer.T.R"]}}}}
		]}`)
	if err == nil {
		t.Fatal("expected unresolved-capture error, got nil")
	}
	if !errors.Is(err, errCaptureUnresolved) {
		t.Errorf("err = %v; want errors.Is errCaptureUnresolved", err)
	}
}

func TestExport_NamespaceDoesNotCollideWithAnOuterCaptureOfTheSameName(t *testing.T) {
	// Row 6. THE MUTATION ROW for the namespacing decision. The outer pattern
	// binds $R and the sub-pattern also binds $R; "R" and "T.R" are distinct
	// bindings and neither overwrites the other. Make the export flat instead
	// of namespaced and this test goes red, because the sub-pattern's R would
	// clobber the outer one.
	target := "package main\nfunc f() { outer.Wrap(inner.Close()) }"
	got, err := runWhere(t, "$R.Wrap($$$A)", target,
		`{"all": [
			{"equals": {"of": "R", "value": "outer"}},
			{"contains_pattern": {"of": "$match", "pattern": "$R.Close()", "as": "T"}},
			{"equals": {"of": "T.R", "value": "inner"}},
			{"equals": {"of": "R", "value": "outer"}}
		]}`)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != 1 {
		t.Errorf("matches = %d, want 1 (R stays \"outer\" after the sub-pattern bound its own R)", got)
	}
}

func TestExport_BindsTheFirstCandidateAndTheSameOneTheHandleBinds(t *testing.T) {
	// Row 7. Two descendants the sub-pattern can express. evalSubPattern
	// returns on the FIRST, so the exported captures are the first
	// candidate's — the export does NOT lift the no-backtracking limit that
	// help("manage_checks") trap 6 documents.
	//
	// The two facts are asserted TOGETHER so they cannot drift: T.R equals the
	// first receiver AND the `as` handle T is the same node the sub-pattern's
	// own $match bound.
	target := "package main\nfunc f() {\n\ta.Close()\n\tb.Close()\n}"
	got, err := runWhere(t, "func $F() { $$$B }", target,
		`{"all": [
			{"contains_pattern": {"of": "$match", "pattern": "$R.Close()", "as": "T"}},
			{"equals": {"of": "T.R", "value": "a"}},
			{"same_node": {"captures": ["T", "T.$match"]}}
		]}`)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != 1 {
		t.Errorf("matches = %d, want 1 (first candidate is a.Close(), and T == T.$match)", got)
	}
}

func TestExport_SecondCandidateIsNotReachedByASiblingLeaf(t *testing.T) {
	// The discriminating half of row 7: naming the SECOND candidate's receiver
	// from a sibling leaf finds nothing, because the leaf bound the first and
	// does not backtrack. If a later change makes evaluation candidate-major
	// this row goes red deliberately.
	target := "package main\nfunc f() {\n\ta.Close()\n\tb.Close()\n}"
	got, err := runWhere(t, "func $F() { $$$B }", target,
		`{"all": [
			{"contains_pattern": {"of": "$match", "pattern": "$R.Close()", "as": "T"}},
			{"equals": {"of": "T.R", "value": "b"}}
		]}`)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != 0 {
		t.Errorf("matches = %d, want 0 (the leaf binds the first candidate only)", got)
	}
}

func TestExport_NotWrapperBindsOnInnerMatchExactlyAsTheHandleDoes(t *testing.T) {
	// Row 8. The inherited smell documented on SubPatternLeaf.As: inside a
	// `not`, the binding still fires on inner-match and the outer verdict
	// flips. The namespaced export inherits that exactly — it neither fixes it
	// nor makes it worse — so a sibling-of-`not` leaf can read T.R for the
	// match whose inner leaf succeeded.
	got, err := runWhere(t, "$X.Close()", exportTwoCloses,
		`{"all": [
			{"not": {"contains_pattern": {"of": "$match", "pattern": "y.Close()", "as": "T"}}},
			{"contains_pattern": {"of": "$match", "pattern": "$R.Close()", "as": "U"}},
			{"equals": {"of": "U.R", "value": "x"}}
		]}`)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != 1 {
		t.Errorf("matches = %d, want 1", got)
	}
}

func TestExport_NamespaceDoesNotSurviveIntoTheNextMatchsScope(t *testing.T) {
	// Row 22. WRITE-THEN-RE-READ across matches. Each match gets its own
	// captures via withMatchCaptures, so a key written while evaluating the
	// FIRST match must be absent when the second is evaluated. The `any`
	// composer is what makes the second match reach the reading leaf at all:
	// the binding leaf returns false there, so the sibling runs and must be
	// refused rather than resolving a stale node.
	//
	// The mutation: hoist the export into a scope shared across matches and
	// this row goes red, because T.R would still be readable.
	target := "package main\nfunc f() {\n\ta.Close()\n\tb.Close()\n}"
	got, err := runWhere(t, "$X.Close()", target,
		`{"any": [
			{"contains_pattern": {"of": "$match", "pattern": "a.Close()", "as": "T"}},
			{"equals": {"of": "T.$match", "value": "a.Close()"}}
		]}`)
	if !errors.Is(err, errCaptureUnresolved) {
		t.Fatalf("err = %v; want errors.Is errCaptureUnresolved on the second match", err)
	}
	if got != 1 {
		t.Errorf("matches = %d, want 1 (only the first match binds T)", got)
	}
}

func TestExport_RebindingOneHandleClearsThePreviousNamespace(t *testing.T) {
	// Two leaves share the handle "T" over patterns binding DIFFERENT capture
	// names. After the second leaf binds, the first leaf's exported key must be
	// gone rather than readable as a stale node — a stale capture read as a
	// fresh one is the worst of the available failures, so the clear turns it
	// into the ordinary refusal.
	target := "package main\nfunc f() {\n\ta.Close()\n\tsink(v)\n}"
	_, err := runWhere(t, "func $F() { $$$B }", target,
		`{"all": [
			{"contains_pattern": {"of": "$match", "pattern": "$R.Close()", "as": "T"}},
			{"contains_pattern": {"of": "$match", "pattern": "sink($V)", "as": "T"}},
			{"equals": {"of": "T.R", "value": "a"}}
		]}`)
	if err == nil {
		t.Fatal("expected unresolved-capture error for the cleared key T.R, got nil")
	}
	if !errors.Is(err, errCaptureUnresolved) {
		t.Errorf("err = %v; want errors.Is errCaptureUnresolved", err)
	}
}

func TestWhere_OuterDepthTwoSpellingIsRepeatedTokens(t *testing.T) {
	// A DEFECT FOUND WHILE BUILDING THE EXPORT, PINNED HERE BECAUSE NOTHING
	// OBSERVED IT. Four shipped surfaces documented the two-level capture
	// reference as "$outer.outer.X". resolveCapture strips the LITERAL prefix
	// "$outer." one occurrence at a time, so "$outer.outer.X" strips once and
	// then looks up a capture named "outer.X" one level up — which is not a
	// two-level walk and never resolved. The working spelling is one token per
	// level: "$outer.$outer.X".
	//
	// The documentation was corrected to the working spelling rather than
	// resolveCapture taught to accept the documented one, and the reason is the
	// namespaced export this ticket adds: "outer.X" is now a LEGAL capture key
	// (a leaf declaring `as:"outer"` over a pattern binding $X writes exactly
	// it), so accepting "$outer.outer.X" as a depth-two walk would make one
	// spelling mean two different things depending on what a sibling leaf
	// happens to be named. Both directions are asserted in one run so the pair
	// discriminates.
	target := "package main\nfunc f() { x.Close() }"
	tree := func(ref string) string {
		return `{"contains_pattern": {"of": "$match", "pattern": "$R.Close()", "where": {
			"contains_pattern": {"of": "$match", "pattern": "$S", "where": {
				"same_text": {"captures": ["S", "` + ref + `"]}}}}}}`
	}

	got, err := runWhere(t, "$X.Close()", target, tree("$outer.$outer.X"))
	if err != nil {
		t.Fatalf("$outer.$outer.X: err: %v", err)
	}
	if got != 1 {
		t.Errorf("$outer.$outer.X matches = %d, want 1", got)
	}

	_, err = runWhere(t, "$X.Close()", target, tree("$outer.outer.X"))
	if !errors.Is(err, errCaptureUnresolved) {
		t.Errorf("$outer.outer.X err = %v; want errors.Is errCaptureUnresolved — "+
			"if this spelling now resolves, the docs corrected by this ticket are wrong again", err)
	}
}
