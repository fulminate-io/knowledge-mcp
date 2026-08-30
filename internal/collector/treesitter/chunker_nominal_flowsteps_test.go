// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// nominalFlowLang is one member of the family, with everything the two looping
// subtests need to exercise it end to end.
type nominalFlowLang struct {
	label   string
	lang    Language
	ext     string
	decl    string   // the marker identifying the declaration under test
	kinds   []string // the node kinds that declaration can be spelled as
	parity  string
	ordered string
}

// nominalFlowDecl parses a fixture with one language's grammar and runs that
// language's arm over the declaration whose text contains marker.
func nominalFlowDecl(t *testing.T, l nominalFlowLang, src, marker string) []FlowStep {
	t.Helper()
	root := parseFlowFixture(t, l.lang, src)
	decl := findFlowDecl(t, root, src, marker, l.kinds...)
	switch l.lang {
	case LangJava:
		return javaFlowSteps(decl, []byte(src))
	case LangKotlin:
		return kotlinFlowSteps(decl, []byte(src))
	case LangScala:
		return scalaFlowSteps(decl, []byte(src))
	case LangGroovy:
		return groovyFlowSteps(decl, []byte(src))
	default:
		return csharpFlowSteps(decl, []byte(src))
	}
}

// nominalFlowLangs is the family, in the LOCKED label order the criterion greps
// for. Each carries its own parity and ordering fixtures, because five grammars
// cannot share one source text.
//
// EVERY PARITY FIXTURE PASSES A PARAMETER-DERIVED ARGUMENT AT EVERY CALL SITE,
// which is what makes SET EQUALITY correct rather than red against correct work:
// over a general fixture an all-constant call yields a CALLS edge and no flow
// step by design, so the arm's set would be a proper subset.
//
// EVERY PARITY FIXTURE ALSO ENDS WITH THE CHAINED TAIL `a.b(p).c(p)`, which is
// the DECLINE DIRECTION. The cut fires in all five languages, and what survives
// it names a method on a receiver the emission threw away.
var nominalFlowLangs = []nominalFlowLang{
	{
		label: "java", lang: LangJava, ext: ".java", decl: "String handle",
		kinds: []string{"method_declaration", "constructor_declaration"},
		parity: `class S {
	String handle(String p, String q) {
		helper(p);
		util.run(p);
		obj.method(q);
		a.b(p).c(p);
		return p;
	}
}
`,
		ordered: `class S {
	String ordered(String p, String q) {
		String a = p;
		helper(a);
		String b = q;
		other(b);
		return a;
	}
}
`,
	},
	{
		label: "kotlin", lang: LangKotlin, ext: ".kt", decl: "fun handle",
		kinds: []string{"function_declaration"},
		parity: `class S {
	fun handle(p: String, q: String): String {
		helper(p)
		util.run(p)
		obj.method(q)
		a.b(p).c(p)
		return p
	}
}
`,
		ordered: `class S {
	fun ordered(p: String, q: String): String {
		val a = p
		helper(a)
		val b = q
		other(b)
		return a
	}
}
`,
	},
	{
		label: "scala", lang: LangScala, ext: ".scala", decl: "def handle",
		kinds: []string{"function_definition"},
		parity: `class S {
	def handle(p: String, q: String): String = {
		helper(p)
		util.run(p)
		obj.method(q)
		a.b(p).c(p)
		return p
	}
}
`,
		ordered: `class S {
	def ordered(p: String, q: String): String = {
		val a = p
		helper(a)
		val b = q
		other(b)
		return a
	}
}
`,
	},
	{
		label: "groovy", lang: LangGroovy, ext: ".groovy", decl: "def handle",
		kinds: []string{"function_definition", "function_declaration"},
		parity: `class S {
	def handle(p, q) {
		helper(p)
		util.run(p)
		obj.method(q)
		a.b(p).c(p)
		return p
	}
}
`,
		ordered: `class S {
	def ordered(p, q) {
		def a = p
		helper(a)
		def b = q
		other(b)
		return a
	}
}
`,
	},
	{
		label: "csharp", lang: LangCSharp, ext: ".cs", decl: "string Handle",
		kinds: []string{"method_declaration", "constructor_declaration"},
		parity: `class S {
	string Handle(string p, string q) {
		helper(p);
		util.run(p);
		obj.method(q);
		a.b(p).c(p);
		return p;
	}
}
`,
		ordered: `class S {
	string Ordered(string p, string q) {
		string a = p;
		helper(a);
		string b = q;
		other(b);
		return a;
	}
}
`,
	},
}

// TestNominalFlowSteps pins the five nominal-static arms. ONE SUBTEST PER
// LANGUAGE, not one shared fixture: these are five grammars and a shared fixture
// proves whichever one it happens to parse.
func TestNominalFlowSteps(t *testing.T) {
	t.Run("java_steps", func(t *testing.T) {
		const src = `class S {
	private String cache;
	String store(String p, String q) {
		String a = p;
		sink(a);
		this.cache = p;
		other.cache = p;
		return q;
	}
}
`
		steps := nominalFlowDecl(t, nominalFlowLangs[0], src, "String store")
		assertNominalShape(t, src, steps)
		assertOneReceiverField(t, steps)
	})

	t.Run("kotlin_steps", func(t *testing.T) {
		const src = `class S {
	var cache: String = ""
	fun store(p: String, q: String): String {
		val a = p
		sink(a)
		this.cache = p
		other.cache = p
		return q
	}
}
`
		steps := nominalFlowDecl(t, nominalFlowLangs[1], src, "fun store")
		assertNominalShape(t, src, steps)
		assertOneReceiverField(t, steps)
	})

	t.Run("scala_steps", func(t *testing.T) {
		const src = `class S {
	var cache: String = ""
	def store(p: String, q: String): String = {
		val a = p
		sink(a)
		this.cache = p
		other.cache = p
		return q
	}
}
`
		steps := nominalFlowDecl(t, nominalFlowLangs[2], src, "def store")
		assertNominalShape(t, src, steps)
		assertOneReceiverField(t, steps)
	})

	t.Run("groovy_steps", func(t *testing.T) {
		// GROOVY'S DECLINE IS PART OF THE ASSERTION. The vendored grammar parses
		// `this.cache = p` into an ERROR node beside a bare assignment, so this arm
		// records NO receiver-field write for groovy — a grammar boundary, not
		// breakage. The parsing sibling in the same fixture is what proves the arm
		// is not simply inert: without it, "no field step" would be satisfied by an
		// arm that produced nothing at all.
		const src = `class S {
	def cache
	def store(p, q) {
		def a = p
		sink(a)
		this.cache = p
		return q
	}
}
`
		steps := nominalFlowDecl(t, nominalFlowLangs[3], src, "def store")
		assertNominalShape(t, src, steps)

		var fields int
		for _, s := range steps {
			if s.Kind == StepAssign && s.Field != "" {
				fields++
			}
		}
		assert.Zero(t, fields,
			"the groovy grammar cannot parse a this-qualified write, so the arm records none — "+
				"and the parsing sibling asserted above proves this is the decline rather than an "+
				"inert arm")
	})

	t.Run("csharp_steps", func(t *testing.T) {
		const src = `class S {
	string cache;
	string Store(string p, string q) {
		string a = p;
		sink(a);
		this.cache = p;
		other.cache = p;
		return q;
	}
}
`
		steps := nominalFlowDecl(t, nominalFlowLangs[4], src, "string Store")
		assertNominalShape(t, src, steps)
		assertOneReceiverField(t, steps)
	})

	t.Run("source_ordered", func(t *testing.T) {
		// NESTED PER LANGUAGE, DELIBERATELY. A flat loop under one t.Run emits ONE
		// PASS line whatever it iterated over, so a loop covering java alone would
		// be byte-identical in the runner's output to one covering all five.
		for _, l := range nominalFlowLangs {
			t.Run(l.label, func(t *testing.T) {
				marker := "ordered"
				if l.lang == LangCSharp {
					marker = "Ordered"
				}
				steps := nominalFlowDecl(t, l, l.ordered, marker)
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
		// THE CALLEE-SPELLING PARITY RULE. All five rows rewrite or decline the
		// spelling before a CALLS edge carries it, so a hand-derived spelling would
		// bind a different declaration than the sibling CALLS edge does.
		//
		// NESTED PER LANGUAGE and ASSERTED INSIDE THE LOOP — one aggregate over the
		// family is satisfied by a single language carrying the whole set.
		for _, l := range nominalFlowLangs {
			t.Run(l.label, func(t *testing.T) {
				armSet := calleeSetOf(nominalFlowDecl(t, l, l.parity, l.decl))
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

// The names every `_steps` fixture in every family uses. They are CONSTANTS
// rather than parameters because the fixtures are deliberately uniform: what
// varies between languages is the SYNTAX of a parameter list, a local binding
// and a call, never the identifiers they are written with. Holding the names
// fixed is what makes twelve grammars comparable through one assertion.
const (
	shapeParam0 = "p"
	shapeParam1 = "q"
	shapeLocal  = "a"
	shapeCallee = "sink"
)

// assertNominalShape asserts the per-language `_steps` shape: two parameters in
// their own positions, one local define, and one call-arg for the callee.
//
// THE TWO-PARAMETER FIXTURE IS WHAT MAKES THE POSITION ASSERTION FALSIFIABLE.
// Collapse it to one parameter and it passes against an arm that hardcodes 0.
func assertNominalShape(t *testing.T, src string, steps []FlowStep) {
	t.Helper()
	params := map[string]int{}
	var defines, callArgs int
	for _, s := range steps {
		switch s.Kind {
		case StepParam:
			// THE RECEIVER IS EXCLUDED, and it must be: a receiver has no POSITION
			// — python's `self` and Go's method receiver both carry Index 0 with
			// Receiver set, so folding them in here would collide with the real
			// parameter zero and make this assertion unwritable.
			if s.Target != nil && !s.Receiver {
				params[s.Target.Content([]byte(src))] = s.Index
			}
		case StepDefine:
			if s.Target != nil && s.Target.Content([]byte(src)) == shapeLocal {
				defines++
			}
		case StepCallArg:
			if s.Callee == shapeCallee {
				callArgs++
			}
		}
	}
	assert.Equal(t, map[string]int{shapeParam0: 0, shapeParam1: 1}, params,
		"both parameters bind, each in its OWN position")
	assert.Equal(t, 1, defines, "the local define emits one step")
	assert.Equal(t, 1, callArgs, "the call emits one step for its parameter-derived argument")
}

// receiverFieldName is the field every receiver-write fixture in this family
// writes into. It is a constant rather than a parameter because the fixtures are
// deliberately uniform: what varies between languages is the SYNTAX of the
// write, never the name it targets.
const receiverFieldName = "cache"

// assertOneReceiverField asserts exactly one receiver-field write, into
// receiverFieldName.
//
// The fixture writes through the receiver AND through another object, so a
// single step here proves the scoping rather than the mere presence of a
// field arm.
func assertOneReceiverField(t *testing.T, steps []FlowStep) {
	t.Helper()
	var fields []FlowStep
	for _, s := range steps {
		if s.Kind == StepAssign && s.Field != "" {
			fields = append(fields, s)
		}
	}
	require.Len(t, fields, 1,
		"the receiver's field write emits; the write through another object does NOT")
	assert.Equal(t, receiverFieldName, fields[0].Field)
	assert.True(t, fields[0].Receiver)
}
