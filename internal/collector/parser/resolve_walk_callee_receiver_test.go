// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// TestCalleeReceiverSemantics is the DISCRIMINATING half of the callee-receiver
// work. The chunker fixtures prove what TEXT is emitted; they cannot prove the
// emission is CORRECT, because correctness is a claim about which declaration
// the emitted callee binds to. These controls settle it at the layer where a
// callee becomes an edge.
//
// EVERY SUBTEST CARRIES A DECOY, and the decoy is what makes it non-vacuous:
// without a same-named declaration in scope, a mangled callee binds to nothing
// on the unfixed tree too and the subtest would pass for the wrong reason. Each
// assertion is over the caller's FULL CALLS set by EQUALITY, never containment,
// so a fix that merely ADDS the right edge beside the fabricated one fails.
//
// THE `direct` AND `argpos` HALVES ARE CHARACTERIZATION GUARDS — green before
// and after — and they are what stops the fix being implemented as a blanket
// deletion of bare or qualified names.
func TestCalleeReceiverSemantics(t *testing.T) {
	t.Run("composite_literal_receiver_binds_the_type_not_a_decoy", func(t *testing.T) {
		res := populateFixture(t, []fixtureFile{{path: "svc/svc.go", src: receiverGoLiteralSrc}})

		assert.ElementsMatch(t, []string{"svc/svc.go:Format.Build"},
			edgesFrom(res, kgtypes.EdgeCalls, "svc/svc.go:viaLiteral"),
			"a composite-literal receiver must bind its TYPE and not a same-named decoy")

		// The literal receiver's edge must be BOUND, exactly as the typed
		// binding's is — not one member of a dynamic group at half confidence,
		// which is what the unfixed tree produces for both Format.Build and the
		// fabricated Other.Build.
		//
		// CONFIDENCE IS COMPARED AGAINST THE TYPED BINDING'S; METHOD IS NOT.
		// Every bound edge now stamps the RULE THAT RESOLVED IT on Method, so a
		// qualified `Format.Build` and a typed binding's `f.Build` legitimately
		// land at different rungs while both being equally bound. Comparing the
		// rung NAMES would assert a coincidence of call SHAPE rather than the
		// property under test, so the Method leg asserts what the property
		// actually is: a named rung rather than a dynamic group.
		lit := oneCallEdge(t, res, "svc/svc.go:viaLiteral")
		direct := oneCallEdge(t, res, "svc/svc.go:direct")
		assert.InDelta(t, direct.Confidence, lit.Confidence, 1e-9,
			"the literal-receiver edge must be as bound as the typed-binding edge")
		assert.NotEqual(t, kgtypes.EdgeMethodDynamic, lit.Method,
			"the literal-receiver edge must resolve at a named rung, not dynamically")
		assert.NotEmpty(t, lit.Method,
			"a bound edge names the rule that resolved it")
	})

	t.Run("literal_receiver_emits_no_edge_rather_than_a_same_named_local", func(t *testing.T) {
		res := populateFixture(t, []fixtureFile{{path: "web/a.js", src: receiverJSRegexSrc}})

		require.Empty(t, edgesFrom(res, kgtypes.EdgeCalls, "web/a.js:run"),
			"a regex-literal receiver must emit no edge rather than binding a same-named local")
	})

	t.Run("optional_chain_binds_the_container_not_a_decoy", func(t *testing.T) {
		res := populateFixture(t, []fixtureFile{{path: "web/c.ts", src: receiverTSOptionalSrc}})

		assert.ElementsMatch(t, []string{"web/c.ts:Mon.start"},
			edgesFrom(res, kgtypes.EdgeCalls, "web/c.ts:go"),
			"dropping the optional-chain operator must bind the named container alone")
	})

	t.Run("non_null_assertion_binds_the_container_not_a_decoy", func(t *testing.T) {
		res := populateFixture(t, []fixtureFile{{path: "web/d.ts", src: receiverTSBangSrc}})

		assert.ElementsMatch(t, []string{"web/d.ts:Mon.stop"},
			edgesFrom(res, kgtypes.EdgeCalls, "web/d.ts:go"),
			"dropping the non-null assertion must bind the named container alone")
	})

	// THE REMAINING FIVE SHARE ONE SHAPE: a decoy declaration, an `elided`
	// caller whose emission drops the receiver, and a `direct` caller making the
	// SAME call legitimately. The second assertion in each is what stops the fix
	// being a blanket deletion.

	t.Run("groovy_optional_receiver_emits_no_edge_rather_than_binding_a_sibling", func(t *testing.T) {
		res := populateFixture(t, []fixtureFile{{path: "app/R.groovy", src: receiverGroovySrc}})

		require.Empty(t, edgesFrom(res, kgtypes.EdgeCalls, "app/R.groovy:R.elided"),
			"a receiver the grammar elided must emit no edge rather than binding a sibling")
		assert.ElementsMatch(t, []string{"app/R.groovy:R.size"},
			edgesFrom(res, kgtypes.EdgeCalls, "app/R.groovy:R.direct"),
			"a genuinely unqualified call must still bind its sibling")
	})

	t.Run("javascript_chained_optional_emits_no_edge_rather_than_binding_a_local", func(t *testing.T) {
		res := populateFixture(t, []fixtureFile{{path: "web/a.js", src: receiverJSChainedOptionalSrc}})

		require.Empty(t, edgesFrom(res, kgtypes.EdgeCalls, "web/a.js:elided"),
			"a chained optional tail must emit no edge rather than binding a module-scope local")
		assert.ElementsMatch(t, []string{"web/a.js:getAttribute"},
			edgesFrom(res, kgtypes.EdgeCalls, "web/a.js:direct"),
			"a genuinely unqualified call must still bind its module-scope local")
	})

	// UNDECORATED, and that is the point: no chain operator appears anywhere in
	// the elided caller, and it fabricates exactly as the decorated one does.
	// This is why the rule keys on the cut having fired rather than on the
	// presence of an operator.
	t.Run("chained_tail_emits_no_edge_rather_than_binding_a_local", func(t *testing.T) {
		res := populateFixture(t, []fixtureFile{{path: "web/a.js", src: receiverJSChainedTailSrc}})

		require.Empty(t, edgesFrom(res, kgtypes.EdgeCalls, "web/a.js:elided"),
			"an undecorated chained tail must emit no edge rather than binding a module-scope local")
		assert.ElementsMatch(t, []string{"web/a.js:setAttribute"},
			edgesFrom(res, kgtypes.EdgeCalls, "web/a.js:direct"),
			"a genuinely unqualified call must still bind its module-scope local")
	})

	// The bracket half of the same cut, in a language whose landed fixture
	// already carries a subscripted receiver.
	t.Run("subscript_tail_emits_no_edge_rather_than_binding_a_local", func(t *testing.T) {
		res := populateFixture(t, []fixtureFile{{path: "svc/a.py", src: receiverPySubscriptSrc}})

		require.Empty(t, edgesFrom(res, kgtypes.EdgeCalls, "svc/a.py:elided"),
			"a subscripted-receiver tail must emit no edge rather than binding a module-scope local")
		assert.ElementsMatch(t, []string{"svc/a.py:method"},
			edgesFrom(res, kgtypes.EdgeCalls, "svc/a.py:direct"),
			"a genuinely unqualified call must still bind its module-scope local")
	})

	// THE argpos ASSERTION IS THE POINT AND IT IS A CHARACTERIZATION GUARD,
	// green before and after. Lua's receiver-wrapper node kind is the SAME kind
	// that encloses an argument-position call, so an ancestor walk that does not
	// END at the argument node climbs out of the argument list, reads the
	// CALLER's start byte as an elided receiver, and deletes `inner` — a
	// legitimate call, with nothing else in the repository to notice, because
	// the corpora hold no Lua outside fixtures.
	t.Run("lua_wrapper_tail_declines_while_argument_position_survives", func(t *testing.T) {
		res := populateFixture(t, []fixtureFile{{path: "app/m.lua", src: receiverLuaSrc}})

		require.Empty(t, edgesFrom(res, kgtypes.EdgeCalls, "app/m.lua:elided"),
			"lua's bare wrapper tail must emit no edge rather than binding a file-scope local")
		assert.ElementsMatch(t, []string{"app/m.lua:inner", "app/m.lua:outer"},
			edgesFrom(res, kgtypes.EdgeCalls, "app/m.lua:argpos"),
			"BOTH argument-position calls must survive the wrapper decline")
		assert.ElementsMatch(t, []string{"app/m.lua:tail"},
			edgesFrom(res, kgtypes.EdgeCalls, "app/m.lua:direct"),
			"a genuinely unqualified call must be untouched")
	})
}

// oneCallEdge returns the single CALLS edge leaving from, failing if there is
// not exactly one.
func oneCallEdge(t *testing.T, res PopulateResult, from string) *knowledgev1.Edge {
	t.Helper()
	var out []*knowledgev1.Edge
	for _, e := range res.Edges {
		if kgtypes.EdgeType(e.Type) == kgtypes.EdgeCalls && e.FromId == from {
			out = append(out, e)
		}
	}
	require.Len(t, out, 1, "expected exactly one CALLS edge from %s", from)
	return out[0]
}

// Format and Other each declare Build, so a callee reduced to the bare method
// name binds BOTH. Baseline: viaLiteral emits two edges at confidence 0.50, one
// of them wholly fabricated.
const receiverGoLiteralSrc = `package svc

type Format struct{}

func (Format) Build() {}

type Other struct{}

func (Other) Build() {}

func direct(f Format) {
	f.Build()
}

func viaLiteral() {
	Format{}.Build()
}
`

// THE LOCAL DECOY IS THE WHOLE POINT: without a same-named module-scope
// declaration the unfixed tree emits nothing here either, and the subtest would
// be vacuous. Baseline: run binds test at confidence 1.00.
const receiverJSRegexSrc = `export function test(s) {
  return s;
}

export function run(s) {
  return /^\s*$/.test(s);
}
`

const receiverTSOptionalSrc = `class Mon {
  static start() {}
}
class Other {
  static start() {}
}
function go() {
  Mon?.start();
}
`

const receiverTSBangSrc = `class Mon {
  static stop() {}
}
class Other {
  static stop() {}
}
function go() {
  Mon!.stop();
}
`

// Baseline: BOTH callers emit app/R.groovy:R.size as a BOUND edge at the same
// rung — byte-identical, which is precisely the defect.
const receiverGroovySrc = `class R {
    def size() { }
    def elided(o) {
        o?.size()
    }
    def direct() {
        size()
    }
}
`

const receiverJSChainedOptionalSrc = `export function getAttribute(x) {
  return x;
}

export function elided(o) {
  return o.get(1)?.getAttribute('x');
}

export function direct() {
  return getAttribute(1);
}
`

const receiverJSChainedTailSrc = `export function setAttribute(y) {
  return y;
}

export function elided(o) {
  return o.get(1).setAttribute('y');
}

export function direct() {
  return setAttribute(1);
}
`

const receiverPySubscriptSrc = `def method():
    return 1

def elided(d):
    return d['k'].method()

def direct():
    return method()
`

const receiverLuaSrc = `function tail(x)
  return x
end

function inner(x)
  return x
end

function outer(x)
  return x
end

function elided(a)
  return a.b(1).tail(2)
end

function argpos()
  return outer(inner(1))
end

function direct()
  return tail(3)
end
`
