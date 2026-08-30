// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// systemsFlowLang is one member of the family, with its own fixtures: three
// grammars cannot share one source text.
type systemsFlowLang struct {
	label   string
	lang    Language
	ext     string
	decl    string
	kinds   []string
	parity  string
	ordered string
}

// systemsFlowDecl parses a fixture with one language's grammar and runs that
// language's arm over the declaration whose text contains marker.
func systemsFlowDecl(t *testing.T, l systemsFlowLang, src, marker string) []FlowStep {
	t.Helper()
	root := parseFlowFixture(t, l.lang, src)
	decl := findFlowDecl(t, root, src, marker, l.kinds...)
	switch l.lang {
	case LangC:
		return cFlowSteps(decl, []byte(src))
	case LangCPP:
		return cppFlowSteps(decl, []byte(src))
	default:
		return rustFlowSteps(decl, []byte(src))
	}
}

// systemsFlowLangs is the family, in the LOCKED label order the criterion greps
// for.
//
// EVERY PARITY FIXTURE PASSES A PARAMETER-DERIVED ARGUMENT AT EVERY CALL SITE,
// which is what makes SET EQUALITY correct rather than red against correct work.
// THE CPP AND RUST FIXTURES CARRY A SEPARATOR-BEARING CALLEE — `ptr->run` and
// `util::run` — which is the family's own proof that a callee spelling can hold
// a `>` or a `:`, and therefore that no reader may classify a flow edge by
// scanning its Evidence for one.
var systemsFlowLangs = []systemsFlowLang{
	{
		label: "c", lang: LangC, ext: ".c", decl: "int handle",
		kinds: []string{"function_definition"},
		parity: `int handle(char *p, int q) {
	helper(p);
	s->run(p);
	obj.method(q);
	return q;
}
`,
		ordered: `int ordered(char *p, int q) {
	char *a = p;
	helper(a);
	int b = q;
	other(b);
	return b;
}
`,
	},
	{
		label: "cpp", lang: LangCPP, ext: ".cpp", decl: "int handle",
		kinds: []string{"function_definition"},
		parity: `struct S {
	int handle(char *p, int q) {
		helper(p);
		ptr->run(p);
		Ns::fn(p);
		a.b(p).c(p);
		return q;
	}
};
`,
		ordered: `struct S {
	int ordered(char *p, int q) {
		char *a = p;
		helper(a);
		int b = q;
		other(b);
		return b;
	}
};
`,
	},
	{
		label: "rust", lang: LangRust, ext: ".rs", decl: "fn handle",
		kinds: []string{"function_item"},
		parity: `impl S {
	fn handle(&self, p: String, q: i32) -> i32 {
		helper(p);
		util::run(&p);
		obj.method(q);
		a.b(p).c(p);
		return q;
	}
}
`,
		ordered: `impl S {
	fn ordered(&self, p: String, q: i32) -> i32 {
		let a = p;
		helper(a);
		let b = q;
		other(b);
		return b;
	}
}
`,
	},
}

// TestSystemsFlowSteps pins the c, cpp and rust arms.
func TestSystemsFlowSteps(t *testing.T) {
	t.Run("c_steps", func(t *testing.T) {
		const src = `int store(char *p, int q) {
	char *a = p;
	sink(a);
	s->cache = q;
	return q;
}
`
		steps := systemsFlowDecl(t, systemsFlowLangs[0], src, "int store")
		assertNominalShape(t, src, steps)
	})

	t.Run("cpp_steps", func(t *testing.T) {
		const src = `struct S {
	int cache;
	int store(char *p, int q) {
		char *a = p;
		sink(a);
		this->cache = q;
		other->cache = q;
		return q;
	}
};
`
		steps := systemsFlowDecl(t, systemsFlowLangs[1], src, "int store")
		assertNominalShape(t, src, steps)
		assertOneReceiverField(t, steps)
	})

	t.Run("rust_steps", func(t *testing.T) {
		const src = `impl S {
	fn store(&self, p: String, q: i32) -> i32 {
		let a = p;
		sink(a);
		self.cache = q;
		other.cache = q;
		return q;
	}
}
`
		steps := systemsFlowDecl(t, systemsFlowLangs[2], src, "fn store")
		assertNominalShape(t, src, steps)
		assertOneReceiverField(t, steps)
	})

	t.Run("addressof_unwraps", func(t *testing.T) {
		// THE NEGATIVE CONTROL IS THE ONE THAT MATTERS HERE. Over-collecting
		// Sources produces facts that are WRONG rather than missing, and every
		// positive assertion below would still pass against an arm that put every
		// identifier in the declaration into every Sources list. The second call
		// passes an unrelated local, so its Sources must NOT contain p.
		const src = `int store(char *p, int q) {
	char *local = 0;
	sink(&p);
	other(&local);
	return q;
}
`
		steps := systemsFlowDecl(t, systemsFlowLangs[0], src, "int store")
		assert.True(t, sourcesContain(steps, src, "sink", "p"),
			"the address-of unwraps to its operand: &p yields p's identifier, not the unary node "+
				"and not nothing")
		assert.False(t, sourcesContain(steps, src, "other", "p"),
			"negative control: the call taking an UNRELATED local does not carry p — an arm that "+
				"collected every identifier in the declaration would fail here and nowhere else")
		assert.True(t, sourcesContain(steps, src, "other", "local"),
			"control: that call does carry its own operand, so the negative above is scoping "+
				"rather than an arm that emitted nothing")
	})

	t.Run("deref_unwraps", func(t *testing.T) {
		const src = `int store(char *p, int q) {
	char *local = 0;
	sink(*p);
	other(*local);
	return q;
}
`
		steps := systemsFlowDecl(t, systemsFlowLangs[0], src, "int store")
		assert.True(t, sourcesContain(steps, src, "sink", "p"),
			"the dereference unwraps to its operand")
		assert.False(t, sourcesContain(steps, src, "other", "p"),
			"negative control: the call dereferencing an UNRELATED local does not carry p")
		assert.True(t, sourcesContain(steps, src, "other", "local"),
			"control: that call does carry its own operand")
	})

	t.Run("source_ordered", func(t *testing.T) {
		// NESTED PER LANGUAGE, DELIBERATELY. A flat loop under one t.Run emits ONE
		// PASS line whatever it iterated over, so a loop covering c alone would be
		// byte-identical in the runner's output to one covering all three.
		for _, l := range systemsFlowLangs {
			t.Run(l.label, func(t *testing.T) {
				steps := systemsFlowDecl(t, l, l.ordered, "ordered")
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
		// THE CALLEE-SPELLING PARITY RULE, nested per language and asserted INSIDE
		// the loop — one aggregate over the family is satisfied by a single
		// language carrying the whole set.
		for _, l := range systemsFlowLangs {
			t.Run(l.label, func(t *testing.T) {
				armSet := calleeSetOf(systemsFlowDecl(t, l, l.parity, l.decl))
				edgeSet := callsEdgeSetOf(t, "pkg/parity"+l.ext, l.parity)

				assert.Equal(t, edgeSet, armSet,
					"the arm derives every callee through normalizeCallee, so its spellings are "+
						"the CALLS edge's")

				require.NotEmpty(t, armSet, "known-positive: the fixture produced callees at all")
				qualified := false
				for callee := range armSet {
					if strings.ContainsAny(callee, ".:>") {
						qualified = true
					}
				}
				assert.True(t, qualified,
					"known-positive: at least one spelling is QUALIFIED — and in this family a "+
						"qualifier is written with `.`, `::` or `->`, every one of which is an "+
						"admitted separator")

				if l.lang == LangC {
					// C's fixture carries no chained tail: the language has no
					// method-call chain to write one with, so the decline direction
					// below is asserted for the two languages that DO.
					return
				}
				assert.False(t, edgeSet["c"], "the chained tail emits NO CALLS edge")
				assert.False(t, armSet["c"], "and the arm emits no StepCallArg for it either")
				assert.True(t, edgeSet["a.b"],
					"control: the INNER call of the same chain does emit, so the absence above is "+
						"the decline rather than the whole chain being invisible")
			})
		}
	})
}

// sourcesContain reports whether the StepCallArg for one callee reads an operand
// spelled name.
func sourcesContain(steps []FlowStep, src, callee, name string) bool {
	for i := range steps {
		if steps[i].Kind != StepCallArg || steps[i].Callee != callee {
			continue
		}
		for _, s := range steps[i].Sources {
			if s != nil && s.Content([]byte(src)) == name {
				return true
			}
		}
	}
	return false
}
