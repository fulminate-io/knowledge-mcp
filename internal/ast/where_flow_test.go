// SPDX-License-Identifier: Apache-2.0

package ast

// where_flow_test.go covers the flows_to leaf: the registry binding, the
// unarmed-language error policy, and the bounded reachability walk.

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// TestFlowLeaf_GoArmIsAvailable is this plan's PREREQUISITE GATE: every later
// Go-based test in the flows_to work assumes a real Go arm behind flowArmFor,
// and would otherwise fail for a reason that looks like leaf breakage.
//
// It also pins the binding itself. flowArmFor is the ast package's only door to
// the flow-step registry, so a rename or signature change upstream surfaces
// here rather than as a compile error scattered across the leaf and evaluator.
func TestFlowLeaf_GoArmIsAvailable(t *testing.T) {
	arm, ok := flowArmFor(treesitter.LangGo)
	require.True(t, ok, "Go must be flow-armed: the flows_to leaf's Go tests and its "+
		"armed-vocabulary error message both depend on it")
	assert.NotNil(t, arm, "an armed language must hand back a usable resolver, not a nil one")

	// THE DISCRIMINATING LEG. Without it, a flowArmFor that returned (something,
	// true) unconditionally would satisfy the assertions above — and the whole
	// point of the accessor is that it can say NO, which is what the leaf's
	// loud-error policy keys on.
	_, unarmed := flowArmFor(treesitter.Language("no-such-language"))
	assert.False(t, unarmed, "an unregistered language must report unarmed, never a silent yes")
}

// flowsToTree is the minimal well-formed flows_to where-tree the validator
// tests drive. Spelled once so a test asserting a REFUSAL cannot accidentally
// be refused for a malformed leaf instead of the reason under test.
func flowsToTree() *WhereNode {
	return &WhereNode{FlowsTo: &FlowsToLeaf{From: "P", To: "ARG", Within: "$match"}}
}

// TestFlowLeaf_UnarmedLanguageErrorsLoudly pins the ticket's decided policy: a
// flow leaf on a language with no flow-step arm ERRORS, naming the language and
// the flow-armed vocabulary — it never quietly evaluates to false.
//
// THE SUBJECT LANGUAGE IS UNARMED BY CONSTRUCTION, NOT BY ASSUMPTION. Which
// languages carry arms is an open parameter owned upstream, so a test that
// hardcoded some language it believed unarmed would become vacuously passing
// the day that language gets an arm — asserting a refusal that no longer
// happens for the reason it claims. Capturing the real arm, unregistering it,
// and restoring it in cleanup makes the unarmed state a fact this test creates.
//
// The restore is mandatory rather than tidy: the upstream unregister DELETES
// the entry, so a test that took an arm out and did not put it back would
// silently disarm flow collection for every later test in the same binary.
func TestFlowLeaf_UnarmedLanguageErrorsLoudly(t *testing.T) {
	// CONTROL, in the same run: an armed language must return nil through the
	// identical call. Without it, "errors on unarmed" is satisfied by a
	// validator that errors on everything.
	require.NoError(t, ValidateWhereFlowArms(flowsToTree(), treesitter.LangGo),
		"an armed language must validate clean — otherwise the refusal below proves nothing")

	const subject = treesitter.LangTypeScript
	if arm, armed := flowArmFor(subject); armed {
		treesitter.UnregisterFlowSteps(subject)
		t.Cleanup(func() { treesitter.RegisterFlowSteps(subject, arm) })
	}

	err := ValidateWhereFlowArms(flowsToTree(), subject)
	require.Error(t, err, "a flows_to leaf on an unarmed language must ERROR, never evaluate to false")

	msg := err.Error()
	assert.Contains(t, msg, string(subject), "the error must name the offending language")
	assert.Contains(t, msg, string(treesitter.LangGo),
		"the error must list the armed vocabulary so the caller learns what DOES work")
	// php carries a flow arm upstream but is ast-denied, so advertising it would
	// send a caller to a language this tool refuses outright. The absence is only
	// meaningful because the presence assertion above fires on the same message.
	assert.NotContains(t, msg, "php",
		"the armed vocabulary is derived from ast's own registry, so an ast-denied "+
			"language must never be advertised")
}

// TestFlowLeaf_RequiredFieldsAndScopeErrors pins the three required fields at
// the PRE-WALK point. Step 3.1 adds the evaluator's half of the same rule under
// its own subtests; both points need their own gate because ast.Match is
// callable directly, without the tool handlers that run this validator.
func TestFlowLeaf_RequiredFieldsAndScopeErrors(t *testing.T) {
	cases := []struct {
		name string
		leaf *FlowsToLeaf
	}{
		{"empty_from_errors_pre_walk", &FlowsToLeaf{From: "", To: "ARG", Within: "$match"}},
		{"empty_to_errors_pre_walk", &FlowsToLeaf{From: "P", To: "", Within: "$match"}},
		{"empty_within_errors_pre_walk", &FlowsToLeaf{From: "P", To: "ARG", Within: ""}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateWhereFlowArms(&WhereNode{FlowsTo: tc.leaf}, treesitter.LangGo)
			require.Error(t, err, "a malformed flows_to leaf must be refused before the walk, "+
				"or it returns a clean zero over any corpus the pattern misses")
			// The field NAME must appear, so the caller can fix it without
			// bisecting their own payload.
			field := strings.TrimSuffix(strings.TrimPrefix(tc.name, "empty_"), "_errors_pre_walk")
			assert.Contains(t, err.Error(), field, "the refusal must name the missing field")
		})
	}

	t.Run("nested_under_a_composer_is_still_caught", func(t *testing.T) {
		// The recursion is the point: a leaf buried under all/any/not or inside a
		// sub-pattern's where is exactly as unanswerable as one at the top.
		nested := &WhereNode{All: []*WhereNode{
			{Not: &WhereNode{FlowsTo: &FlowsToLeaf{From: "P", To: "ARG"}}},
		}}
		require.Error(t, ValidateWhereFlowArms(nested, treesitter.LangGo),
			"a flows_to nested under composers must be validated too")
	})

	// The three subtests below drive the EVALUATOR, not the pre-walk validator.
	// Each keeps its driving call to evalFlowsTo/Match INSIDE its own t.Run body:
	// a PASS-line grep cannot see which function a subtest exercised, so driving
	// ValidateWhereFlowArms here would assert the wrong half of a two-point rule
	// and stay green while the library path went unguarded.

	t.Run("evaluator_errors_on_empty_field", func(t *testing.T) {
		sc := newOuterScope(treesitter.LangGo, nil, nil)
		_, err := evalFlowsTo(&FlowsToLeaf{From: "P", To: "ARG"}, sc)
		require.Error(t, err, "the EVALUATOR must refuse a malformed leaf too — ast.Match is "+
			"callable directly, so it cannot assume the pre-walk validator ran")
		assert.Contains(t, err.Error(), "within", "and must name the missing field")
	})

	t.Run("evaluator_errors_on_unarmed_language", func(t *testing.T) {
		// Unarmed BY CONSTRUCTION: take Go's real arm out, restore it on cleanup.
		arm, armed := flowArmFor(treesitter.LangGo)
		require.True(t, armed, "the fixture guarantee this test inverts")
		treesitter.UnregisterFlowSteps(treesitter.LangGo)
		t.Cleanup(func() { treesitter.RegisterFlowSteps(treesitter.LangGo, arm) })

		dir := fixtureRepo(t, map[string]string{
			"unarmed.go": "package p\n\nfunc carry(alpha string) { sink(alpha) }\n\nfunc sink(string) {}\n",
		})
		pat, perr := Parse("func $F($P $_) { $$$BODY }")
		require.NoError(t, perr)
		cp, cerr := Compile(pat, treesitter.LangGo, "")
		require.NoError(t, cerr)
		defer cp.Close()

		_, _, err := Match(context.Background(), dir, treesitter.LangGo, cp,
			&WhereNode{FlowsTo: &FlowsToLeaf{From: "P", To: "P", Within: "$match"}}, Scope{})
		require.Error(t, err, "an unarmed language must ERROR through the library path, "+
			"never quietly evaluate to false")
		assert.Contains(t, err.Error(), "no flow-step arm")
	})

	t.Run("endpoint_outside_within_errors", func(t *testing.T) {
		// A from-capture outside the named declaration can never be true, and
		// returning false would be indistinguishable from an honest "no flow".
		sc := newOuterScope(treesitter.LangGo, nil, nil)
		sc.captures.byName["OUT"] = Capture{Text: "x", StartByte: 5, EndByte: 6}
		sc.captures.byName["IN"] = Capture{Text: "y", StartByte: 120, EndByte: 121}
		sc.captures.byName["FN"] = Capture{Text: "f", StartByte: 100, EndByte: 200}

		_, err := evalFlowsTo(&FlowsToLeaf{From: "OUT", To: "IN", Within: "FN"}, sc)
		require.Error(t, err, "an endpoint outside the scope must error, not return false")
		assert.Contains(t, err.Error(), "from", "the error must name WHICH endpoint escaped")
		assert.Contains(t, err.Error(), "OUTSIDE")
	})

	t.Run("well_formed_armed_leaf_passes", func(t *testing.T) {
		// KNOWN POSITIVE for the whole table: without it every assertion above is
		// satisfied by a validator that refuses unconditionally.
		require.NoError(t, ValidateWhereFlowArms(flowsToTree(), treesitter.LangGo))
	})
}

// TestFlowLeaf_GoParamReachesSinkArg is the end-to-end gate on the REAL Go arm,
// and its two halves are not symmetric in value.
//
// THE NEGATIVE HALF IS THE LOAD-BEARING ONE. A leaf that is inert — never
// wired into the leaf evaluators, or wired and always returning true — passes the
// positive half trivially, because the pattern alone already matches. Only the
// non-flowing fixture can tell "the leaf filtered" from "the leaf did nothing",
// which is the same absence-vs-filter distinction the whole ticket is about.
//
// The two fixtures use DIFFERENT concrete identifiers so neither can satisfy
// the other's assertion by accident.
func TestFlowLeaf_GoParamReachesSinkArg(t *testing.T) {
	dir := fixtureRepo(t, map[string]string{
		"flow.go": "package p\n\n" +
			"var delta = \"d\"\n\n" +
			// alpha FLOWS: the parameter itself occupies the call argument.
			"func carry(alpha string) { sink(alpha) }\n\n" +
			// beta does NOT flow: the argument is an unrelated package var.
			"func drop(beta string) { sink(delta) }\n\n" +
			"func sink(string) {}\n",
	})

	pat, err := Parse("func $F($P string) { sink($ARG) }")
	require.NoError(t, err)
	cp, err := Compile(pat, treesitter.LangGo, "")
	require.NoError(t, err)
	defer cp.Close()

	// CONTROL: with no where-tree the pattern matches BOTH declarations. This is
	// what makes the filtered result below meaningful rather than a fixture that
	// only ever had one match.
	all, _, err := Match(context.Background(), dir, treesitter.LangGo, cp, nil, Scope{})
	require.NoError(t, err)
	require.Len(t, all, 2, "the bare pattern must match both carry and drop")

	filtered, _, err := Match(context.Background(), dir, treesitter.LangGo, cp,
		&WhereNode{FlowsTo: &FlowsToLeaf{From: "P", To: "ARG", Within: "$match"}}, Scope{})
	require.NoError(t, err)

	require.Len(t, filtered, 1, "exactly one declaration has the flow; got %d", len(filtered))
	kept := filtered[0].Captures["F"].Text
	assert.Equal(t, "carry", kept, "the FLOWING declaration is kept")
	assert.NotEqual(t, "drop", kept, "the NON-flowing declaration is dropped — this is the "+
		"assertion an inert leaf fails")
}

// TestFlowLeaf_CyclicStepsTerminate is the named catcher for the visited set.
//
// HONEST NOTE ON THE FAILURE MODE: omitting the visited set does NOT produce a
// clean assertion failure here — it produces a `go test` TIMEOUT, because the
// walk revisits the cycle forever. A reader debugging a timeout in this package
// should suspect this first.
func TestFlowLeaf_CyclicStepsTerminate(t *testing.T) {
	// Install a fake arm on an ast-registered language, capturing the production
	// arm first and restoring it on cleanup — an arm left swapped disarms the
	// feature for every later test in the same binary.
	const victim = treesitter.LangGo
	prod, armed := flowArmFor(victim)
	require.True(t, armed)
	t.Cleanup(func() { treesitter.RegisterFlowSteps(victim, prod) })

	treesitter.RegisterFlowSteps(victim, func(decl *sitter.Node, src []byte) []flowStep {
		// A -> B and B -> A over two real child nodes of the declaration: a cycle
		// the walk must not revisit.
		steps := prod(decl, src)
		if len(steps) < 2 {
			return steps
		}
		a, b := steps[0].Target, steps[1].Target
		if a == nil || b == nil {
			return steps
		}
		return []flowStep{
			{Kind: steps[0].Kind, Target: a, Sources: []*sitter.Node{b}},
			{Kind: steps[1].Kind, Target: b, Sources: []*sitter.Node{a}},
		}
	})

	dir := fixtureRepo(t, map[string]string{
		"cyc.go": "package p\n\nfunc loop(epsilon string) { zeta := epsilon; sink(zeta) }\n\nfunc sink(string) {}\n",
	})
	pat, err := Parse("func $F($P string) { $$$BODY }")
	require.NoError(t, err)
	cp, err := Compile(pat, treesitter.LangGo, "")
	require.NoError(t, err)
	defer cp.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _, _ = Match(context.Background(), dir, treesitter.LangGo, cp,
			&WhereNode{FlowsTo: &FlowsToLeaf{From: "P", To: "P", Within: "$match"}}, Scope{})
	}()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("the flow walk did not terminate on a cyclic step set — the visited set is missing")
	}
}

// TestFlowLeaf_ArmInvokedOncePerScope is the ONLY catcher for a missed memo
// propagation site. The memo lives on evalScope, and a scope constructor that
// allocated a fresh map instead of inheriting one would memoize nothing while
// every behavioral test above stayed green — the arm would simply run N times
// and return the same answer.
func TestFlowLeaf_ArmInvokedOncePerScope(t *testing.T) {
	const victim = treesitter.LangGo
	prod, armed := flowArmFor(victim)
	require.True(t, armed)
	t.Cleanup(func() { treesitter.RegisterFlowSteps(victim, prod) })

	var calls atomic.Int64
	treesitter.RegisterFlowSteps(victim, func(decl *sitter.Node, src []byte) []flowStep {
		calls.Add(1)
		return prod(decl, src)
	})

	// THREE call sites in ONE declaration: without the memo the arm runs once
	// per match, so the count tracks matches instead of declarations.
	dir := fixtureRepo(t, map[string]string{
		"multi.go": "package p\n\n" +
			"func many(theta string) { sink(theta); sink(theta); sink(theta) }\n\n" +
			"func sink(string) {}\n",
	})
	pat, err := Parse("sink($ARG)")
	require.NoError(t, err)
	cp, err := Compile(pat, treesitter.LangGo, "")
	require.NoError(t, err)
	defer cp.Close()

	matches, _, err := Match(context.Background(), dir, treesitter.LangGo, cp,
		&WhereNode{FlowsTo: &FlowsToLeaf{
			From: "ARG", To: "ARG",
			Within: "FN",
		}, InsidePattern: &SubPatternLeaf{
			Of: "$match", Pattern: "func $F($P string) { $$$B }", As: "FN",
		}}, Scope{})
	require.NoError(t, err)
	require.Len(t, matches, 3, "the fixture must produce THREE matches in one declaration, "+
		"or the count below cannot distinguish per-match from per-declaration")

	got := calls.Load()
	assert.Equal(t, int64(1), got,
		"the arm must run ONCE per declaration, not once per match; got %d calls for 3 matches", got)
}
