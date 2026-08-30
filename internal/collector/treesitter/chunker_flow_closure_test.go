// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"testing"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// flowIdentSrc is the ONLY parsing this file does, and it exists solely to mint
// real identifier nodes for hand-built steps.
//
// THE ENGINE TEST DRIVES HAND-BUILT FlowStep SLICES RATHER THAN A GRAMMAR, and
// that split is the point: it pins closure semantics with no language in the
// way, and it is the test the other fourteen arms inherit for free. A step
// sequence assembled here can express an ordering no Go source could produce,
// which is exactly what order_is_load_bearing needs.
const flowIdentSrc = `package fixture

func names() {
	_, _ = p, q
	_, _ = a, b
	_, _ = untainted, other
}
`

// flowIdents returns one identifier node per distinct name in flowIdentSrc.
func flowIdents(t *testing.T) map[string]*sitter.Node {
	t.Helper()
	root := parseFlowFixture(t, LangGo, flowIdentSrc)
	out := map[string]*sitter.Node{}
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if n.Type() == "identifier" {
			if name := n.Content([]byte(flowIdentSrc)); out[name] == nil {
				out[name] = n
			}
		}
		for i := range int(n.NamedChildCount()) {
			walk(n.NamedChild(i))
		}
	}
	walk(root)
	for _, name := range []string{"p", "q", "a", "b", "untainted", "other"} {
		require.NotNil(t, out[name], "control: the fixture mints an identifier node for %q", name)
	}
	return out
}

// closureOf runs the engine over hand-built steps against the identifier
// fixture's source.
func closureOf(steps []FlowStep) []ParamFlow {
	return flowClosure(steps, []byte(flowIdentSrc))
}

// TestFlowClosure pins the semantics of closure itself — the single definition
// every language arm relies on and none of them may reimplement.
func TestFlowClosure(t *testing.T) {
	n := flowIdents(t)

	t.Run("alias_propagates", func(t *testing.T) {
		// RULING 1: closure runs through local aliases. `a := p; sink(a)` RECORDS.
		// Direct-occurrence-only was rejected, because it is what makes the
		// ABSENCE of a fact near-meaningful rather than meaningless.
		got := closureOf([]FlowStep{
			{Kind: StepParam, Target: n["p"], Index: 0},
			{Kind: StepDefine, Target: n["a"], Sources: []*sitter.Node{n["p"]}},
			{Kind: StepCallArg, Callee: "exec.Command", Index: 0, Sources: []*sitter.Node{n["a"]}},
		})
		assert.Equal(t, []ParamFlow{{
			Source: FlowSource{ParamIndex: 0},
			Kind:   FlowToArg,
			Callee: "exec.Command",
		}}, got, "the alias carries the parameter into the argument")
	})

	t.Run("rebind_clears", func(t *testing.T) {
		// THE POSITIVE CONTROL IS NOT OPTIONAL. Without it this subtest passes
		// against an engine that emits nothing at all, which is the single most
		// plausible wrong implementation.
		cleared := closureOf([]FlowStep{
			{Kind: StepParam, Target: n["p"], Index: 0},
			{Kind: StepDefine, Target: n["a"], Sources: []*sitter.Node{n["p"]}},
			{Kind: StepAssign, Target: n["a"], Sources: []*sitter.Node{n["untainted"]}},
			{Kind: StepCallArg, Callee: "sink", Index: 0, Sources: []*sitter.Node{n["a"]}},
		})
		assert.Empty(t, cleared,
			"the rebind from an untainted source DELETES the binding, so the later use records nothing")

		recorded := closureOf([]FlowStep{
			{Kind: StepParam, Target: n["p"], Index: 0},
			{Kind: StepDefine, Target: n["a"], Sources: []*sitter.Node{n["untainted"]}},
			{Kind: StepAssign, Target: n["a"], Sources: []*sitter.Node{n["p"]}},
			{Kind: StepCallArg, Callee: "sink", Index: 0, Sources: []*sitter.Node{n["a"]}},
		})
		require.Len(t, recorded, 1,
			"positive control: the SAME shape with the two bindings swapped DOES record, so the "+
				"empty result above is the delete rather than an engine that emits nothing")
		assert.Equal(t, "sink", recorded[0].Callee)
	})

	t.Run("shadow_declines", func(t *testing.T) {
		// SHADOWING IS THE SAME DELETE, not a separate conflicted set — which is
		// strictly better than declining the whole parameter, because the OUTER
		// uses survive.
		got := closureOf([]FlowStep{
			{Kind: StepParam, Target: n["p"], Index: 0},
			{Kind: StepDefine, Target: n["a"], Sources: []*sitter.Node{n["p"]}},
			{Kind: StepCallArg, Callee: "outerSink", Index: 0, Sources: []*sitter.Node{n["a"]}},
			{Kind: StepDefine, Target: n["a"], Sources: []*sitter.Node{n["untainted"]}},
			{Kind: StepCallArg, Callee: "innerSink", Index: 0, Sources: []*sitter.Node{n["a"]}},
		})
		require.Len(t, got, 1, "exactly one of the two uses records")
		assert.Equal(t, "outerSink", got[0].Callee,
			"the use BEFORE the shadow survives; the one after it does not")
	})

	t.Run("self_reference_no_cycle", func(t *testing.T) {
		// `a = a + p` reads taint[a] in the union BEFORE the assignment writes
		// it, so one forward pass suffices. There is no fixpoint iteration and
		// none is needed.
		got := closureOf([]FlowStep{
			{Kind: StepParam, Target: n["p"], Index: 0},
			{Kind: StepDefine, Target: n["a"], Sources: []*sitter.Node{n["untainted"]}},
			{Kind: StepAssign, Target: n["a"], Sources: []*sitter.Node{n["a"], n["p"]}},
			{Kind: StepReturn, Index: 0, Sources: []*sitter.Node{n["a"]}},
		})
		assert.Equal(t, []ParamFlow{{
			Source: FlowSource{ParamIndex: 0},
			Kind:   FlowToReturn,
		}}, got, "the self-reference resolves in one pass and the parameter reaches the result")
	})

	t.Run("order_is_load_bearing", func(t *testing.T) {
		// THE SAME STEP SET, REVERSED, MUST PRODUCE A DIFFERENT RESULT. An engine
		// that ignored order — collecting every source and every sink and
		// crossing them — passes every other subtest here and fails this one.
		forward := []FlowStep{
			{Kind: StepParam, Target: n["p"], Index: 0},
			{Kind: StepDefine, Target: n["a"], Sources: []*sitter.Node{n["p"]}},
			{Kind: StepCallArg, Callee: "sink", Index: 0, Sources: []*sitter.Node{n["a"]}},
		}
		reversed := make([]FlowStep, len(forward))
		for i, step := range forward {
			reversed[len(forward)-1-i] = step
		}

		require.Len(t, closureOf(forward), 1,
			"known-positive: in source order the parameter reaches the sink")
		assert.Empty(t, closureOf(reversed),
			"reversed, the sink is read before anything binds it — the engine reads ORDER")
	})
}
