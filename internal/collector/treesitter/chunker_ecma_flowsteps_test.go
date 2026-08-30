// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ecmaFlowLangs is the family, with the label each nested subtest runs under.
//
// THE LABELS ARE LOCKED AND MATCH THE `_steps` SUBTEST PREFIXES so the two
// vocabularies cannot drift. The extension selects the language on the CALLS
// side, which goes through the real Chunker.
var ecmaFlowLangs = []struct {
	label string
	lang  Language
	ext   string
}{
	{"js", LangJavaScript, ".js"},
	{"ts", LangTypeScript, ".ts"},
	{"tsx", LangTSX, ".tsx"},
}

// ecmaFlowDecl parses a fixture with one language's grammar and runs that
// language's arm over the declaration whose text contains marker.
func ecmaFlowDecl(t *testing.T, lang Language, src, marker string) []FlowStep {
	t.Helper()
	root := parseFlowFixture(t, lang, src)
	decl := findFlowDecl(t, root, src, marker,
		"function_declaration", "method_definition", "class_declaration", "arrow_function")
	switch lang {
	case LangTypeScript:
		return tsFlowSteps(decl, []byte(src))
	case LangTSX:
		return tsxFlowSteps(decl, []byte(src))
	default:
		return jsFlowSteps(decl, []byte(src))
	}
}

// ecmaParityFixture is the DEDICATED parity fixture: EVERY call site passes at
// least one parameter-derived argument, which is what makes SET EQUALITY correct
// rather than red against correct work. It is plain ECMAScript, so the same
// bytes parse under all three grammars.
//
// The last statement is the family's own DECLINING SHAPE — an optional-chain
// tail — and it carries no `new` expression deliberately: the Calls query emits
// a constructor as a callee, which this arm declines by design, so a `new` here
// would break set equality for a reason that is not a defect.
const ecmaParityFixture = `function handler(p, q) {
	helper(p);
	util.run(p);
	obj.method(q);
	o.a(p)?.b(p);
}
`

// ecmaOrderFixture interleaves defines and calls so the ordering assertion has a
// real sequence to compare rather than a single step.
const ecmaOrderFixture = `function ordered(p, q) {
	const a = p;
	helper(a);
	const b = q;
	other(b);
	return a;
}
`

// TestECMAFlowSteps pins the three ECMAScript arms. EACH LANGUAGE GETS ITS OWN
// SUBTEST because they are three grammars: tsx numbers the same kind names
// differently from typescript, so a single shared assertion would pass on the
// base grammar and prove nothing about tsx.
func TestECMAFlowSteps(t *testing.T) {
	t.Run("js_steps", func(t *testing.T) {
		const src = `function f(a, b) {
	const x = a;
	sink(x);
	return b;
}
`
		steps := ecmaFlowDecl(t, LangJavaScript, src, "function f")
		assertECMAShape(t, src, steps)
	})

	t.Run("ts_steps", func(t *testing.T) {
		// THE ANNOTATED PARAMETERS ARE THE POINT: typescript wraps each name in a
		// required_parameter beside its annotation, so an arm reading the
		// parameter list's identifiers directly finds nothing at all here.
		const src = `function f(a: string, b: number): string {
	const x = a;
	sink(x);
	return b;
}
`
		steps := ecmaFlowDecl(t, LangTypeScript, src, "function f")
		assertECMAShape(t, src, steps)
	})

	t.Run("tsx_steps", func(t *testing.T) {
		const src = `function f(a: string, b: number): string {
	const x = a;
	sink(x);
	return b;
}
`
		steps := ecmaFlowDecl(t, LangTSX, src, "function f")
		assertECMAShape(t, src, steps)
	})

	t.Run("this_field_scoped", func(t *testing.T) {
		// BOTH WRITES ARE IN ONE FIXTURE so the negative cannot pass vacuously:
		// an arm emitting nothing at all would satisfy "no step for the non-this
		// write" while failing the positive beside it.
		const src = `class Server {
	store(v) {
		this.cache = v;
		other.cache = v;
	}
}
`
		steps := ecmaFlowDecl(t, LangJavaScript, src, "store(v)")
		var fields []FlowStep
		for _, s := range steps {
			if s.Kind == StepAssign && s.Field != "" {
				fields = append(fields, s)
			}
		}
		require.Len(t, fields, 1,
			"the this-qualified write emits; the write through another object does NOT, because "+
				"binding that owner needs a type lookup the chunker does not have")
		assert.Equal(t, "cache", fields[0].Field)
		assert.True(t, fields[0].Receiver)
	})

	t.Run("source_ordered", func(t *testing.T) {
		// NESTED PER LANGUAGE, DELIBERATELY. A flat loop under one t.Run emits ONE
		// PASS line whatever it iterated over, so a loop covering javascript alone
		// would be byte-identical in the runner's output to one covering all three
		// — and tsx is exactly the grammar this family's reasoning says a shared
		// assertion cannot speak for.
		for _, l := range ecmaFlowLangs {
			t.Run(l.label, func(t *testing.T) {
				steps := ecmaFlowDecl(t, l.lang, ecmaOrderFixture, "function ordered")
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
		// THE CALLEE-SPELLING PARITY RULE, and it matters most in THIS family:
		// all three languages carry {ChainOps:"?!", ElideLiteralBodies:true,
		// DeclineNonName:true}, so optional-chain and non-null-assertion spellings
		// are rewritten or declined before a CALLS edge ever sees them.
		//
		// NESTED PER LANGUAGE for the reason source_ordered gives above, and
		// ASSERTED INSIDE THE LOOP — one aggregate over the family is satisfied by
		// a single language carrying the whole set.
		for _, l := range ecmaFlowLangs {
			t.Run(l.label, func(t *testing.T) {
				armSet := calleeSetOf(ecmaFlowDecl(t, l.lang, ecmaParityFixture, "function handler"))
				edgeSet := callsEdgeSetOf(t, "pkg/parity"+l.ext, ecmaParityFixture)

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

				// THE DECLINE DIRECTION. The optional-chain tail names a method on
				// a receiver the emission threw away, so neither side records it.
				assert.False(t, edgeSet["b"], "the optional-chain tail emits NO CALLS edge")
				assert.False(t, armSet["b"], "and the arm emits no StepCallArg for it either")
				assert.True(t, edgeSet["o.a"],
					"control: the INNER call of the same chain does emit, so the absence above is "+
						"the decline rather than the whole chain being invisible")
			})
		}
	})
}

// ecmaShapeLocal is the local this family's `_steps` fixtures bind. It differs
// from the other families' only because "a" and "b" are already the two
// PARAMETER names here.
const ecmaShapeLocal = "x"

// assertECMAShape asserts the per-language `_steps` shape: two parameters in
// their own positions, one define, one call-arg and one return.
//
// THE TWO-PARAMETER FIXTURE IS WHAT MAKES THE POSITION ASSERTION FALSIFIABLE.
// Collapse it to one parameter and it passes against an arm that hardcodes 0.
func assertECMAShape(t *testing.T, src string, steps []FlowStep) {
	t.Helper()
	params := map[string]int{}
	var defines, callArgs, returns int
	for _, s := range steps {
		switch s.Kind {
		case StepParam:
			if s.Target != nil {
				params[s.Target.Content([]byte(src))] = s.Index
			}
		case StepDefine:
			if s.Target != nil && s.Target.Content([]byte(src)) == ecmaShapeLocal {
				defines++
			}
		case StepCallArg:
			if s.Callee == shapeCallee {
				callArgs++
			}
		case StepReturn:
			returns++
		}
	}
	assert.Equal(t, map[string]int{"a": 0, "b": 1}, params,
		"both parameters bind, each in its OWN position")
	assert.Equal(t, 1, defines, "the local define emits one step")
	assert.Equal(t, 1, callArgs, "the call emits one step for its parameter-derived argument")
	assert.Equal(t, 1, returns, "the return emits one step")
}
