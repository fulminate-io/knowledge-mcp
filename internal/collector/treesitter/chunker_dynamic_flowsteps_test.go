// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// dynamicFlowLang is one member of the family, with its own fixtures.
type dynamicFlowLang struct {
	label   string
	lang    Language
	ext     string
	decl    string
	kinds   []string
	parity  string
	ordered string
}

// dynamicFlowDecl parses a fixture with one language's grammar and runs that
// language's arm over the declaration whose text contains marker.
func dynamicFlowDecl(t *testing.T, l dynamicFlowLang, src, marker string) []FlowStep {
	t.Helper()
	root := parseFlowFixture(t, l.lang, src)
	decl := findFlowDecl(t, root, src, marker, l.kinds...)
	if l.lang == LangPython {
		return pythonFlowSteps(decl, []byte(src))
	}
	return rubyFlowSteps(decl, []byte(src))
}

// dynamicFlowLangs is the family, in the LOCKED label order the criterion greps
// for.
//
// RUBY'S PARITY FIXTURE CARRIES AN INSTANCE-VARIABLE RECEIVER CALL, and that is
// mandatory rather than illustrative: `@logger.info(p)` composes the CALLS
// spelling `@logger.info`, an ORDINARY RESOLVED callee whose own spelling holds
// an `@`. It is the shape that forced the unresolved-callee test to be
// structural rather than a scan of Evidence for that character.
var dynamicFlowLangs = []dynamicFlowLang{
	{
		label: "python", lang: LangPython, ext: ".py", decl: "def handle",
		kinds: []string{"function_definition"},
		parity: `class S:
    def handle(self, p, q):
        helper(p)
        util.run(p)
        obj.method(q)
        a.b(p).c(p)
`,
		ordered: `class S:
    def ordered(self, p, q):
        a = p
        helper(a)
        b = q
        other(b)
        return a
`,
	},
	{
		label: "ruby", lang: LangRuby, ext: ".rb", decl: "def handle",
		kinds: []string{"method"},
		parity: `class S
  def handle(p, q)
    helper(p)
    util.run(p)
    @logger.info(p)
    a.b(p).c(p)
  end
end
`,
		ordered: `class S
  def ordered(p, q)
    a = p
    helper(a)
    b = q
    other(b)
    return a
  end
end
`,
	},
}

// TestDynamicFlowSteps pins the python and ruby arms.
func TestDynamicFlowSteps(t *testing.T) {
	t.Run("python_steps", func(t *testing.T) {
		const src = `class S:
    def store(self, p, q):
        a = p
        sink(a)
        self.cache = p
        other.cache = p
        return q
`
		steps := dynamicFlowDecl(t, dynamicFlowLangs[0], src, "def store")
		assertNominalShape(t, src, steps)
		assertOneReceiverField(t, steps)
	})

	t.Run("ruby_steps", func(t *testing.T) {
		// RUBY WRITES A RECEIVER FIELD TWO WAYS, so BOTH are in this fixture and
		// both must record. An arm covering only one of them would pass a fixture
		// that happened to use the other.
		const src = `class S
  def store(p, q)
    a = p
    sink(a)
    @cache = p
    self.other = p
    return q
  end
end
`
		steps := dynamicFlowDecl(t, dynamicFlowLangs[1], src, "def store")
		assertNominalShape(t, src, steps)

		fields := map[string]bool{}
		for _, s := range steps {
			if s.Kind == StepAssign && s.Field != "" {
				fields[s.Field] = true
				assert.True(t, s.Receiver, "a ruby field write is always on the receiver")
			}
		}
		assert.Equal(t, map[string]bool{"@cache": true, "other": true}, fields,
			"both shapes record, and the instance variable keeps its `@` sigil AS WRITTEN — no "+
				"identifier normalization happens here or downstream")
	})

	t.Run("self_is_recv_not_p0", func(t *testing.T) {
		// THE CARRIER ASSERTION. If this cannot be made to pass, the FlowSource
		// design is wrong and must CHANGE rather than be worked around: a receiver
		// that consumed position zero would make every FLOWS_TO_ARG fact about a
		// python method name the wrong argument index, silently, with every other
		// subtest here green.
		const src = `class S:
    def store(self, v):
        self.cache = v
        return v
`
		steps := dynamicFlowDecl(t, dynamicFlowLangs[0], src, "def store")
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
		require.NotNil(t, recv, "self is a StepParam marked Receiver")
		require.NotNil(t, recv.Target)
		assert.Equal(t, "self", recv.Target.Content([]byte(src)))
		require.NotNil(t, zero, "and position zero is a DIFFERENT step")
		require.NotNil(t, zero.Target)
		assert.Equal(t, "v", zero.Target.Content([]byte(src)),
			"v is parameter ZERO — the position a caller writes it at — not one")

		// The field write is the reason the distinction matters: it renders
		// `flow:recv>f:cache`, byte-identical to a Go method's.
		assertOneReceiverField(t, steps)
	})

	t.Run("param_forms_bind", func(t *testing.T) {
		// EVERY BINDING FORM THE PYTHON QUALIFIER ARM COVERS IS COVERED HERE. A
		// form that arm binds and this one drops is a silent per-form hole.
		//
		// THE KEYWORD SEPARATOR IS THE CATCHER: a bare `*` binds no name, so an arm
		// that let it consume a position would give `r` index 3 instead of 2 and
		// every other assertion here would still pass.
		const src = `class S:
    def store(self, a, b: str, c = 1, *, r = 2):
        sink(a)
`
		steps := dynamicFlowDecl(t, dynamicFlowLangs[0], src, "def store")
		got := map[string]int{}
		for _, s := range steps {
			if s.Kind == StepParam && !s.Receiver && s.Target != nil {
				got[s.Target.Content([]byte(src))] = s.Index
			}
		}
		assert.Equal(t, map[string]int{"a": 0, "b": 1, "c": 2, "r": 3}, got,
			"plain, annotated, defaulted and keyword-only forms all bind, each at its own "+
				"position, and the bare `*` separator consumes none")
	})

	t.Run("source_ordered", func(t *testing.T) {
		// NESTED PER LANGUAGE, DELIBERATELY. A flat loop under one t.Run emits ONE
		// PASS line whatever it iterated over, so a loop covering python alone
		// would be byte-identical in the runner's output to one covering both —
		// and ruby is the grammar this family exists to prove.
		for _, l := range dynamicFlowLangs {
			t.Run(l.label, func(t *testing.T) {
				steps := dynamicFlowDecl(t, l, l.ordered, "ordered")
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
		}
	})

	t.Run("callee_matches_calls_edge", func(t *testing.T) {
		// THE CALLEE-SPELLING PARITY RULE, and THIS FAMILY IS WHERE IT WAS PROVEN
		// NECESSARY. Nested per language and asserted INSIDE the loop.
		for _, l := range dynamicFlowLangs {
			t.Run(l.label, func(t *testing.T) {
				armSet := calleeSetOf(dynamicFlowDecl(t, l, l.parity, l.decl))
				edgeSet := callsEdgeSetOf(t, "pkg/parity"+l.ext, l.parity)

				assert.Equal(t, edgeSet, armSet,
					"the arm derives every callee through normalizeCallee, so its spellings are "+
						"the CALLS edge's")

				require.NotEmpty(t, armSet, "known-positive: the fixture produced callees at all")
				qualified := false
				for callee := range armSet {
					if strings.Contains(callee, ".") {
						qualified = true
					}
				}
				assert.True(t, qualified,
					"known-positive: at least one spelling is QUALIFIED, so two sets of bare names "+
						"cannot agree trivially")

				if l.lang == LangRuby {
					assert.True(t, armSet["@logger.info"],
						"THE PROOF CASE: an instance-variable receiver composes an ORDINARY "+
							"RESOLVED callee whose own spelling carries an `@`, which is why no "+
							"reader may classify a flow edge by scanning Evidence for one")
				}

				// THE DECLINE DIRECTION.
				assert.False(t, edgeSet["c"], "the chained tail emits NO CALLS edge")
				assert.False(t, armSet["c"], "and the arm emits no StepCallArg for it either")
				assert.True(t, edgeSet["a.b"],
					"control: the INNER call of the same chain does emit, so the absence above is "+
						"the decline rather than the whole chain being invisible")
			})
		}
	})
}
