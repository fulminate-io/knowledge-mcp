// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// swiftFlowDecl parses a swift fixture and runs the swift arm over the
// declaration whose text contains marker.
func swiftFlowDecl(t *testing.T, src, marker string) []FlowStep {
	t.Helper()
	root := parseFlowFixture(t, LangSwift, src)
	decl := findFlowDecl(t, root, src, marker, "function_declaration", "protocol_function_declaration")
	return swiftFlowSteps(decl, []byte(src))
}

// swiftParityFixture is the DEDICATED parity fixture: every call site passes at
// least one parameter-derived argument, which is what makes SET EQUALITY correct
// rather than red against correct work. It ends with the chained tail, the
// DECLINE DIRECTION.
const swiftParityFixture = `class S {
	func handle(_ p: String, _ q: Int) {
		helper(p)
		util.run(p)
		obj.method(q)
		a.b(p).c(p)
	}
}
`

// TestSwiftFlowSteps is SWIFT'S ONLY BEHAVIORAL GATE. Phase 9's other step is
// the registry census, which asserts an arm is REGISTERED and never what it
// EMITS — so without this file swift would ship the one behaviorally ungated
// arm of the fifteen.
func TestSwiftFlowSteps(t *testing.T) {
	t.Run("swift_steps", func(t *testing.T) {
		// THE LABELED CALL IS PART OF THE ASSERTION: `sink(value: a)` must record
		// argument ZERO, because a swift label is part of the callee's declared
		// signature rather than a reordering, and the ordinal is what Evidence
		// carries.
		//
		// THE TWO-PARAMETER FIXTURE IS WHAT MAKES THE POSITION ASSERTION
		// FALSIFIABLE. Collapse it to one and it passes against an arm hardcoding 0.
		const src = `class S {
	var cache: String = ""
	func store(_ p: String, _ q: Int) -> Int {
		let a = p
		sink(value: a)
		self.cache = p
		other.cache = p
		return q
	}
}
`
		steps := swiftFlowDecl(t, src, "func store")

		params := map[string]int{}
		var defines, callArgs, returns int
		var argIndex = -1
		for _, s := range steps {
			switch s.Kind {
			case StepParam:
				if s.Target != nil && !s.Receiver {
					params[s.Target.Content([]byte(src))] = s.Index
				}
			case StepDefine:
				if s.Target != nil && s.Target.Content([]byte(src)) == "a" {
					defines++
				}
			case StepCallArg:
				if s.Callee == "sink" {
					callArgs++
					argIndex = s.Index
				}
			case StepReturn:
				returns++
			}
		}
		assert.Equal(t, map[string]int{"p": 0, "q": 1}, params,
			"the INTERNAL name binds — the name a body reference uses — each at its own position")
		assert.Equal(t, 1, defines, "the let binding emits one step")
		require.Equal(t, 1, callArgs, "the labeled call emits one step")
		assert.Equal(t, 0, argIndex,
			"a LABELED argument still occupies ordinal position zero")
		assert.Equal(t, 1, returns, "the return emits one step")

		assertOneReceiverField(t, steps)
	})

	t.Run("source_ordered", func(t *testing.T) {
		const src = `class S {
	func ordered(_ p: String, _ q: Int) -> Int {
		let a = p
		helper(a)
		let b = q
		other(b)
		return q
	}
}
`
		steps := swiftFlowDecl(t, src, "func ordered")
		require.NotEmpty(t, steps, "control: the arm produced steps at all")

		prev, seen, counted := uint32(0), false, 0
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
			"control: more than one step carries a node, so the assertion above compared something")
	})

	t.Run("callee_matches_calls_edge", func(t *testing.T) {
		// THE CALLEE-SPELLING PARITY RULE. Swift's profile is
		// {ChainOps:"?!", DeclineNonName:true} and carries NO ElideLiteralBodies
		// deliberately, so a trailing-closure receiver is declined rather than
		// repaired — and a hand-derived spelling would diverge there first.
		armSet := calleeSetOf(swiftFlowDecl(t, swiftParityFixture, "func handle"))
		edgeSet := callsEdgeSetOf(t, "pkg/parity.swift", swiftParityFixture)

		assert.Equal(t, edgeSet, armSet,
			"the arm derives every callee through normalizeCallee, so its spellings are the "+
				"CALLS edge's")

		require.NotEmpty(t, armSet, "known-positive: the fixture produced callees at all")
		qualified := false
		for callee := range armSet {
			if strings.Contains(callee, ".") {
				qualified = true
			}
		}
		assert.True(t, qualified,
			"known-positive: at least one spelling is QUALIFIED, so two sets of bare names cannot "+
				"agree trivially")

		// THE DECLINE DIRECTION.
		assert.False(t, edgeSet["c"], "the chained tail emits NO CALLS edge")
		assert.False(t, armSet["c"], "and the arm emits no StepCallArg for it either")
		assert.True(t, edgeSet["a.b"],
			"control: the INNER call of the same chain does emit, so the absence above is the "+
				"decline rather than the whole chain being invisible")
	})
}
