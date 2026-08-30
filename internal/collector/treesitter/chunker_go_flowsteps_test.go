// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"context"
	"strings"
	"testing"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// parseFlowFixture parses one in-memory fixture with a language's own grammar
// and returns its root node.
//
// IT PARSES DIRECTLY RATHER THAN GOING THROUGH THE Chunker because a flow-step
// arm takes a DECLARATION NODE, and Result carries chunks and edges rather than
// the tree they came from. Every family's arm test shares this helper.
func parseFlowFixture(t *testing.T, lang Language, src string) *sitter.Node {
	t.Helper()
	grammar, ok := LanguageGrammar(lang)
	require.True(t, ok, "control: a grammar is registered for %s", lang)

	parser := sitter.NewParser()
	parser.SetLanguage(grammar)
	tree, err := parser.ParseCtx(context.Background(), nil, []byte(src))
	require.NoError(t, err)
	t.Cleanup(func() {
		tree.Close()
		parser.Close()
	})
	return tree.RootNode()
}

// findFlowDecl returns the first node in the tree whose kind is one of kinds
// and whose source text contains marker.
//
// The marker is a substring rather than a parsed name so one helper serves
// fifteen grammars, none of which spell a declaration's name node the same way.
func findFlowDecl(t *testing.T, root *sitter.Node, src, marker string, kinds ...string) *sitter.Node {
	t.Helper()
	want := make(map[string]bool, len(kinds))
	for _, k := range kinds {
		want[k] = true
	}
	var found *sitter.Node
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if found != nil {
			return
		}
		if want[n.Type()] && strings.Contains(n.Content([]byte(src)), marker) {
			found = n
			return
		}
		for i := range int(n.NamedChildCount()) {
			walk(n.NamedChild(i))
		}
	}
	walk(root)
	require.NotNil(t, found, "control: the fixture declares a %v containing %q", kinds, marker)
	return found
}

// goFlowDecl parses a Go fixture and runs the Go arm over the declaration whose
// text contains marker.
func goFlowDecl(t *testing.T, src, marker string) []FlowStep {
	t.Helper()
	root := parseFlowFixture(t, LangGo, src)
	decl := findFlowDecl(t, root, src, marker, "function_declaration", "method_declaration")
	return goFlowSteps(decl, []byte(src))
}

// calleeSetOf collects the StepCallArg callee spellings a step slice carries.
func calleeSetOf(steps []FlowStep) map[string]bool {
	out := map[string]bool{}
	for i := range steps {
		if steps[i].Kind == StepCallArg {
			out[steps[i].Callee] = true
		}
	}
	return out
}

// callsEdgeSetOf chunks a fixture through the real Chunker and collects the
// callee spellings its CALLS edges carry.
func callsEdgeSetOf(t *testing.T, path, src string) map[string]bool {
	t.Helper()
	res := chunkQualFixture(t, path, src)
	out := map[string]bool{}
	for i := range res.Edges {
		if res.Edges[i].Type == EdgeCalls {
			out[res.Edges[i].ToID] = true
		}
	}
	return out
}

// stepStartByte returns the earliest source byte a step names, and whether it
// names one at all.
//
// A StepParam for an UNNAMED parameter names no node — that is the whole point
// of the nil Target — so ordering is asserted over the steps that do carry one.
func stepStartByte(step *FlowStep) (uint32, bool) {
	best, ok := uint32(0), false
	if step.Target != nil {
		best, ok = step.Target.StartByte(), true
	}
	for _, s := range step.Sources {
		if s == nil {
			continue
		}
		if b := s.StartByte(); !ok || b < best {
			best, ok = b, true
		}
	}
	return best, ok
}

// TestGoFlowSteps pins the Go reference arm, which the other fourteen are
// written against.
func TestGoFlowSteps(t *testing.T) {
	t.Run("steps_are_source_ordered", func(t *testing.T) {
		// THE CONTRACT FlowStep DOCUMENTS. The closure engine is order-sensitive
		// — a rebind clears — so an arm returning query order rather than source
		// order produces wrong facts silently, with every other subtest green.
		const src = `package fixture

func ordered(p string, q string) (string, string) {
	a := p
	helper(a)
	b := q
	other(b)
	return a, b
}
`
		steps := goFlowDecl(t, src, "func ordered")
		require.NotEmpty(t, steps, "control: the arm produced steps at all")

		prev, seen := uint32(0), false
		counted := 0
		for i := range steps {
			at, ok := stepStartByte(&steps[i])
			if !ok {
				continue
			}
			counted++
			if seen {
				assert.GreaterOrEqual(t, at, prev,
					"step %d starts before its predecessor — the slice is not source-ordered", i)
			}
			prev, seen = at, true
		}
		require.Greater(t, counted, 1,
			"control: more than one step carries a node, so the ordering assertion above compared something")
	})

	t.Run("param_positions", func(t *testing.T) {
		// THE MULTI-PARAMETER FIXTURES ARE WHAT MAKE THIS FALSIFIABLE. Collapse
		// them to one parameter and the subtest passes against an arm that
		// hardcodes index 0. Do not simplify them.
		const src = `package fixture

func blank(_ string, b string) {}

func flattened(a, b string, c int) {}
`
		blank := goFlowDecl(t, src, "func blank")
		var blankParams []FlowStep
		for _, s := range blank {
			if s.Kind == StepParam {
				blankParams = append(blankParams, s)
			}
		}
		require.Len(t, blankParams, 2, "both positions are held, the blank one included")
		assert.Nil(t, blankParams[0].Target, "the blank identifier binds no name")
		assert.Equal(t, 0, blankParams[0].Index, "but it still occupies position 0")
		require.NotNil(t, blankParams[1].Target)
		assert.Equal(t, "b", blankParams[1].Target.Content([]byte(src)))
		assert.Equal(t, 1, blankParams[1].Index,
			"b is parameter ONE — an arm that dropped the blank would call it zero")

		flat := goFlowDecl(t, src, "func flattened")
		got := map[string]int{}
		for _, s := range flat {
			if s.Kind == StepParam && s.Target != nil {
				got[s.Target.Content([]byte(src))] = s.Index
			}
		}
		assert.Equal(t, map[string]int{"a": 0, "b": 1, "c": 2}, got,
			"a shared type declares two names and BOTH occupy their own position")
	})

	t.Run("receiver_is_marked", func(t *testing.T) {
		const src = `package fixture

func (s *Server) Handle(p string) {}
`
		steps := goFlowDecl(t, src, "func (s *Server) Handle")
		var recv, zero *FlowStep
		for i := range steps {
			if steps[i].Kind != StepParam {
				continue
			}
			if steps[i].Receiver {
				recv = &steps[i]
				continue
			}
			if steps[i].Index == 0 {
				zero = &steps[i]
			}
		}
		require.NotNil(t, recv, "the receiver is a StepParam")
		require.NotNil(t, zero, "and parameter zero is a DIFFERENT step")
		require.NotNil(t, recv.Target)
		require.NotNil(t, zero.Target)
		assert.Equal(t, "s", recv.Target.Content([]byte(src)))
		assert.Equal(t, "p", zero.Target.Content([]byte(src)),
			"the receiver does not consume position zero — p does")
	})

	t.Run("callee_matches_calls_edge", func(t *testing.T) {
		// THE CALLEE-SPELLING PARITY RULE. A FLOWS_TO_ARG edge carries this
		// spelling as its endpoint and resolves it against the same reference
		// site the sibling CALLS edge uses, so a one-character divergence binds a
		// different declaration silently.
		//
		// THE FIXTURE IS DEDICATED, and that is what makes SET EQUALITY correct
		// rather than red against correct work: every call site here passes at
		// least one parameter-derived argument. Over a general fixture an
		// all-constant call yields a CALLS edge and no flow step BY DESIGN, so
		// the arm's set would be a proper subset.
		//
		// THE CATCHER IS THE QUALIFIED-SPELLING CONTROL. Drop it and an arm that
		// emits nothing at all passes against an empty CALLS set.
		const src = `package fixture

func handler(p string, q string) {
	helper(p)
	exec.Command(p)
	obj.Method(q)
	a.b(p).c(p)
}
`
		armSet := calleeSetOf(goFlowDecl(t, src, "func handler"))
		edgeSet := callsEdgeSetOf(t, "pkg/parity.go", src)

		assert.Equal(t, edgeSet, armSet,
			"the arm derives every callee through normalizeCallee, so its spellings are the CALLS edge's")

		require.NotEmpty(t, armSet, "known-positive: the fixture produced callees at all")
		qualified := false
		for callee := range armSet {
			if strings.Contains(callee, ".") {
				qualified = true
			}
		}
		assert.True(t, qualified,
			"known-positive: at least one spelling is QUALIFIED, so two sets of bare names cannot agree trivially")

		// THE DECLINE DIRECTION, where a hand-derived spelling diverges first.
		// The cut at the chained tail fires in every language; what survives it
		// names a method on a receiver the emission threw away.
		assert.False(t, edgeSet["c"], "the chained tail emits NO CALLS edge")
		assert.False(t, armSet["c"], "and the arm emits no StepCallArg for it either")
		assert.True(t, edgeSet["a.b"],
			"control: the INNER call of the same chain does emit, so the absence above is the decline "+
				"rather than the whole chain being invisible")
	})

	t.Run("field_write_scoped", func(t *testing.T) {
		const src = `package fixture

func (s *Server) Set(v string) {
	s.cache = v
	other.cache = v
}
`
		steps := goFlowDecl(t, src, "func (s *Server) Set")
		var fields []FlowStep
		for _, st := range steps {
			if st.Kind == StepAssign && st.Field != "" {
				fields = append(fields, st)
			}
		}
		require.Len(t, fields, 1,
			"the receiver's field write emits; the write onto another operand does NOT, because "+
				"binding that owner needs a type lookup the chunker does not have")
		assert.Equal(t, "cache", fields[0].Field)
		assert.True(t, fields[0].Receiver, "and it is marked as a receiver field")
	})
}
