// SPDX-License-Identifier: Apache-2.0

package recipe

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// validate_emit_type_test.go — an emit naming a node type the combined practice
// graph does not enroll is refused BEFORE THE WALK, in the one error this
// validator issues, with every offending emit named.
//
// WHY IT IS ITS OWN FILE AND NOT A ROW IN THE SOURCE-CENSUS TESTS. Every other
// refusal there is checked against the SOURCE graph the recipe reads; this one
// is checked against the TARGET vocabulary a landing writes into. Same
// collection point, same single message, different answer key — and the sibling
// file states in its own words that target types are not the source census's
// business, which stays true.

// unenrolledEmitType is a type nothing enrolls — the shape a recipe author
// reaches for when they invent a type for their own collection. It is spelled as
// a literal so this file states its input rather than asking the vocabulary
// under test to produce one.
const unenrolledEmitType = "widget_of_nowhere"

func TestValidateAgainstSource_UnenrolledEmitTypeIsRefusedBeforeTheWalk(t *testing.T) {
	sv := validatorFixture(t)

	t.Run("the refusal names the emit, the type and the admitted vocabulary", func(t *testing.T) {
		msg := refusalFor(t, sv, "select section\nemit "+unenrolledEmitType+" {\n    name := node.symbol_name\n}")
		assert.Contains(t, msg, "emit:", "the site")
		assert.Contains(t, msg, `"`+unenrolledEmitType+`"`, "the offending value")
		assert.Contains(t, msg, "combined practice graph", "what it was checked against")
		assert.Contains(t, msg, "TARGET vocabulary", "and that it is NOT the source graph, which the message also names")
		assert.Contains(t, msg, "refused before the walk", "and why it was not answered with rows")
		assert.Contains(t, msg, `"pattern"`, "the admitted set, so the repair is in the message")
		assert.Contains(t, msg, `"idiom"`)
		assert.Contains(t, msg, "at 2:1", "positioned at the offending emit, not at the recipe")
	})

	t.Run("nothing was interpreted and nothing was fetched", func(t *testing.T) {
		// The refusal is BEFORE the walk, so the run reports no rows and issues no
		// Execute beyond the source load — which is what "before the walk" means
		// operationally rather than as a phrase in a message.
		f := &fakeGraphCaller{
			nodes: []*knowledgev1.Node{
				{Id: "s1", Type: "section", SymbolName: "Chapter One"},
				{Id: "p1", Type: "paragraph", SymbolName: "para"},
			},
			edges: []*knowledgev1.Edge{svEdge("s1", "p1", "CONTAINS")},
		}
		loaded, err := loadSourceView(context.Background(), f, kgtypes.GraphPDFRaw, "doc")
		require.NoError(t, err)
		afterLoad := f.calls

		r, perr := Parse([]byte("select section\nemit " + unenrolledEmitType + " {\n    name := node.symbol_name\n}"))
		require.NoError(t, perr)
		res, ierr := Interpret(context.Background(), r, loaded, recipeTargetSpec(), "eip", Options{})
		require.Error(t, ierr)
		assert.Equal(t, afterLoad, f.calls, "a refused recipe issues no Execute beyond the load")
		if res != nil {
			assert.Zero(t, res.Stats.NodesEmitted, "no row was interpreted into a node")
			assert.Empty(t, res.Nodes, "and nothing was emitted")
		}
	})

	t.Run("TWO offending emits are reported TOGETHER, ordered by position", func(t *testing.T) {
		body := "select section\nemit " + unenrolledEmitType + " {\n    name := node.symbol_name\n}\n" +
			"emit another_unenrolled_type {\n    name := node.symbol_name\n}"
		msg := refusalFor(t, sv, body)
		assert.Contains(t, msg, unenrolledEmitType, "the first offender")
		assert.Contains(t, msg, "another_unenrolled_type", "and the second — a first-error-wins validator names one")
		assert.Less(t, strings.Index(msg, unenrolledEmitType), strings.Index(msg, "another_unenrolled_type"),
			"ordered by position, so the message reads the same on every run")

		// THE ORDER IS STABLE, driven rather than asserted once: the violations come
		// out of a walk over a rule list, but the message that names them is sorted,
		// and a run-to-run reordering is exactly what the collect-then-sort design
		// exists to prevent.
		for range 20 {
			require.Equal(t, msg, refusalFor(t, sv, body))
		}
	})

	t.Run("an offending emit is reported ALONGSIDE a source violation", func(t *testing.T) {
		// The two checks have different answer keys and one message. A recipe with
		// one of each must produce BOTH, or an author repairs one and re-runs to
		// discover the other.
		msg := refusalFor(t, sv, "select sectionn\nemit "+unenrolledEmitType+" {\n    name := node.symbol_name\n}")
		assert.Contains(t, msg, `"sectionn"`, "the source violation")
		assert.Contains(t, msg, `"`+unenrolledEmitType+`"`, "and the target one, in the same error")
	})
}

// TestValidateAgainstSource_EnrolledEmitTypesAreAccepted is the near-miss
// control. Every practice type a landing actually writes must still run — a rule
// that admitted only the two types the refusal message happens to name would
// satisfy every row above and break every collection.
func TestValidateAgainstSource_EnrolledEmitTypesAreAccepted(t *testing.T) {
	sv := validatorFixture(t)
	for _, typ := range []kgtypes.NodeType{
		kgtypes.NodePattern, kgtypes.NodeIdiom, kgtypes.NodeUseCase,
		kgtypes.NodeExample, kgtypes.NodeReference, kgtypes.NodeDocument,
	} {
		t.Run(string(typ), func(t *testing.T) {
			r, err := Parse([]byte("select section\nemit " + string(typ) + " {\n    name := node.symbol_name\n}"))
			require.NoError(t, err)
			_, err = Interpret(context.Background(), r, sv, recipeTargetSpec(), "eip", Options{})
			require.NoError(t, err, "%q is enrolled and must run exactly as before", typ)
		})
	}
}

// TestParseEmit_ANonLiteralNodeTypeIsAlreadyARefusal is the OBSERVED half of the
// computed-emit-type requirement, pinned rather than built: the grammar admits a
// bare identifier after `emit` and nothing else, so a computed type is a PARSE
// error and never reaches the validator at all.
//
// WHY IT NEEDS A PIN AT ALL. The behavior is a property of expectIdent, one
// refactor away from becoming a fall-through that accepts an expression — at
// which point the type would be decidable only per row, where a miss is silent.
// That is the same class as the non-literal edge-type argument the DSL already
// refuses by name.
//
// THE CONTROL IS WHAT MAKES IT NON-VACUOUS: a bare identifier still parses and
// still carries its type verbatim, so these rows are the grammar refusing three
// shapes rather than the parser refusing everything.
func TestParseEmit_ANonLiteralNodeTypeIsAlreadyARefusal(t *testing.T) {
	for _, tc := range []struct{ name, body, want string }{
		{"a bind reference", "select section\nemit $t {\n    name := node.symbol_name\n}",
			`expected target node type after 'emit'`},
		{"an interpolation", "select section\nemit ${node.type} {\n    name := node.symbol_name\n}",
			`expected identifier after '$'`},
		{"a quoted string", "select section\nemit \"widget\" {\n    name := node.symbol_name\n}",
			`expected target node type after 'emit'`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.body))
			require.Error(t, err, "a computed emit type must be refused at PARSE time")
			assert.Contains(t, err.Error(), tc.want)
		})
	}

	t.Run("CONTROL: a bare identifier parses and carries its type verbatim", func(t *testing.T) {
		r, err := Parse([]byte("select section\nemit " + unenrolledEmitType + " {\n    name := node.symbol_name\n}"))
		require.NoError(t, err, "the grammar admits a bare identifier — the rows above are not the parser refusing everything")
		var emits int
		for _, rule := range r.Rules {
			if e, ok := rule.(RuleEmit); ok {
				emits++
				assert.Equal(t, unenrolledEmitType, e.NodeType,
					"the parsed type is the identifier verbatim, which is what the validator then checks")
			}
		}
		require.Equal(t, 1, emits, "the control must actually have reached an emit rule")
	})
}
