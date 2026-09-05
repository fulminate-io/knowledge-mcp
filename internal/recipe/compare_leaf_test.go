// SPDX-License-Identifier: Apache-2.0

package recipe

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// compareFixture is FOUR `block` nodes carrying the three populations a numeric
// leaf has to tell apart, and it is built around the layout signals the pdf and
// web raw collectors stamp:
//
//   - font_ratio_to_body 1.6 / 1.15 / 1 — three distinct magnitudes, so an
//     operator that is off by one boundary (gt where gte was meant) selects a
//     different set rather than the same one.
//   - ONE node with the key ABSENT — the false-predicate population. An
//     implementation coercing absent to 0 lands it in every `lt` set, which is
//     exactly what the `lt 1000` subtest below reads.
//   - line_count on ALL FOUR, stamped so its thresholds cut the fixture into a
//     DIFFERENT population from any font-ratio threshold. A leaf that ignored
//     `of` and answered from one hard-wired key would pass the font-ratio cases
//     and fail these.
//   - chrome_repeat_shaped "true"/"false" — a censused key holding TEXT, which is
//     neither absence: it is the row-read parse error.
//
// The key NAMES are the ones the pdf raw layout-signal emitter stamps; this
// fixture stamps them itself, so these tests depend on the names and not on that
// emitter's code.
func compareFixture(t *testing.T) *sourceView {
	t.Helper()
	f := &fakeGraphCaller{
		nodes: []*knowledgev1.Node{
			{Id: "b1", Type: "block", SymbolName: "heading", Metadata: map[string]string{
				"font_ratio_to_body": "1.6", "line_count": "10", "chrome_repeat_shaped": "true"}},
			{Id: "b2", Type: "block", SymbolName: "subheading", Metadata: map[string]string{
				"font_ratio_to_body": "1.15", "line_count": "3", "chrome_repeat_shaped": "false"}},
			{Id: "b3", Type: "block", SymbolName: "body", Metadata: map[string]string{
				"font_ratio_to_body": "1", "line_count": "1", "chrome_repeat_shaped": "false"}},
			{Id: "b4", Type: "block", SymbolName: "unstamped", Metadata: map[string]string{
				"line_count": "7", "chrome_repeat_shaped": "true"}},
		},
	}
	sv, err := loadSourceView(context.Background(), f, kgtypes.GraphPDFRaw, "doc")
	require.NoError(t, err)
	return sv
}

// compareBody wraps one where-tree filter in a complete, parseable recipe, so
// every subtest below drives the SAME path a saved recipe takes: parse, then the
// validator's resolve pass, then the row loop.
func compareBody(filter string) string {
	return fmt.Sprintf("select block\nfilter %s\nemit pattern {\n    name := node.symbol_name\n}", filter)
}

// compareRun resolves a filter through the real validator and evaluates it over
// every fixture node, returning the ids that matched.
//
// IT GOES THROUGH validateAgainstSource RATHER THAN RESOLVING BY HAND, because
// the property under test is that the operator and operand are settled by the
// validator before any row exists. A helper that populated the map itself would
// pass against an evaluator that resolved lazily.
func compareRun(t *testing.T, sv *sourceView, filter string) ([]string, error) {
	t.Helper()
	r, err := Parse([]byte(compareBody(filter)))
	require.NoError(t, err, "the body must PARSE — these subtests are about semantics, not grammar")

	compiled, compares, err := validateAgainstSource(r, sv)
	if err != nil {
		return nil, err
	}
	env := newEnv()
	env.whereRegexes, env.whereCompares = compiled, compares

	var tree *WhereNode
	for _, rule := range r.Rules {
		if fr, ok := rule.(RuleFilter); ok {
			tree = fr.Where
		}
	}
	require.NotNil(t, tree, "the body must carry a filter rule")

	var out []string
	for _, id := range []string{"b1", "b2", "b3", "b4"} { // sorted, so the result is stable
		n, ok := sv.nodeByID(id)
		require.True(t, ok)
		row := Row{NodeID: id, Node: n, Vars: map[string]string{}}
		matched, err := evalWhereTree(context.Background(), env, &row, tree, sv)
		if err != nil {
			return nil, err
		}
		if matched {
			out = append(out, id)
		}
	}
	return out, nil
}

// compareSurvivors is compareRun for the subtests that must not error, asserting
// a SET rather than a count — an evaluator keeping the right NUMBER of the wrong
// rows still fails.
func compareSurvivors(t *testing.T, sv *sourceView, filter string) []string {
	t.Helper()
	got, err := compareRun(t, sv, filter)
	require.NoError(t, err)
	return got
}

// compareRefusal is compareRun for the subtests that must be refused BEFORE the
// walk, returning the refusal text.
func compareRefusal(t *testing.T, sv *sourceView, filter string) string {
	t.Helper()
	_, err := compareRun(t, sv, filter)
	require.Error(t, err, "the recipe must be refused")
	return err.Error()
}

func TestCompareLeaf_Behaviour(t *testing.T) {
	sv := compareFixture(t)

	t.Run("admits_every_operator", func(t *testing.T) {
		// All six admitted operators over the SAME key, each selecting a different
		// set. The boundary value 1.15 is stamped on b2 on purpose: gt and gte
		// disagree about it, and lt and lte disagree about it, so an off-by-one
		// operator table cannot answer all four correctly.
		for _, tc := range []struct {
			op, value string
			want      []string
		}{
			{"gt", "1.15", []string{"b1"}},
			{"gte", "1.15", []string{"b1", "b2"}},
			{"lt", "1.15", []string{"b3"}},
			{"lte", "1.15", []string{"b2", "b3"}},
			{"ne", "1.15", []string{"b1", "b3"}},
			// THE NUMERIC EQ, and the one case that separates this leaf from the
			// `equals` leaf that already exists: the row stores the string "1" and
			// the recipe asks for "1.0". A string equality answers NO MATCH; a
			// numeric comparison answers b3. An implementation that reached for
			// string equality passes every other case here and fails this one.
			{"eq", "1.0", []string{"b3"}},
		} {
			t.Run(tc.op, func(t *testing.T) {
				got := compareSurvivors(t, sv, fmt.Sprintf(
					`{"compare": {"of": "node.metadata.font_ratio_to_body", "op": %q, "value": %q}}`, tc.op, tc.value))
				assert.Equal(t, tc.want, got)
			})
		}

		// A DIFFERENT KEY SELECTING A DIFFERENT POPULATION. Every set above is a
		// subset of the three font-ratio-stamped nodes; this one includes b4, which
		// carries no font ratio at all. A leaf that ignored `of` and answered from
		// one hard-wired key passes everything above and fails here.
		got := compareSurvivors(t, sv,
			`{"compare": {"of": "node.metadata.line_count", "op": "gt", "value": "5"}}`)
		assert.Equal(t, []string{"b1", "b4"}, got)
	})

	t.Run("absent_value_does_not_match_and_is_not_an_error", func(t *testing.T) {
		// THE FALSE-PREDICATE HALF. b4 does not carry font_ratio_to_body. The key
		// IS in the source graph's vocabulary — three other nodes stamp it — so
		// this is not bad input; the recipe named something real and this row does
		// not have it.
		//
		// THE THRESHOLD IS DELIBERATELY ENORMOUS. Every stamped node is below 1000,
		// so an implementation that coerced an absent value to 0 would put b4 in
		// this set alongside the other three. Nothing is defaulted, nothing is
		// compared, and the run does not stop.
		got := compareSurvivors(t, sv,
			`{"compare": {"of": "node.metadata.font_ratio_to_body", "op": "lt", "value": "1000"}}`)
		assert.Equal(t, []string{"b1", "b2", "b3"}, got)
		assert.NotContains(t, got, "b4", "an absent value is not coerced to zero into the lt set")

		// THE `exists` CONTROL — the discriminator this DSL gives an author who
		// wants the other reading. It selects exactly the same three rows, which is
		// what makes the claim above "b4 was excluded for absence" rather than
		// "b4 was excluded for some other reason".
		ctrl := compareSurvivors(t, sv, `{"exists": {"of": "node.metadata.font_ratio_to_body"}}`)
		assert.Equal(t, got, ctrl, "absence, not the comparison, is what excluded b4")
	})

	t.Run("refuses_a_non_numeric_literal_before_the_walk", func(t *testing.T) {
		msg := compareRefusal(t, sv,
			`{"compare": {"of": "node.metadata.font_ratio_to_body", "op": "gt", "value": "big"}}`)
		assert.Contains(t, msg, `"big"`, "the offending literal")
		assert.Contains(t, msg, "not a number")
		assert.Contains(t, msg, "nothing is coerced or trimmed")
		assert.Contains(t, msg, "before the walk")
	})

	t.Run("refuses_a_non_finite_literal_before_the_walk", func(t *testing.T) {
		// NaN AND THE INFINITIES PARSE. strconv.ParseFloat accepts "NaN", "nan",
		// "Inf", "+Inf", "-Inf" and "infinity" as valid float64 values, so a bare
		// ParseFloat resolves them and the run proceeds — which is bad input
		// answered as a false predicate, the exact conflation this leaf exists to
		// prevent. The `ne` case is the dangerous direction: a NaN literal under
		// `ne` compares false against every row, so `not-equal` admits the WHOLE
		// rowset from a meaningless threshold.
		for _, literal := range []string{"NaN", "nan", "Inf", "+Inf", "-Inf", "infinity"} {
			t.Run(literal, func(t *testing.T) {
				msg := compareRefusal(t, sv, fmt.Sprintf(
					`{"compare": {"of": "node.metadata.font_ratio_to_body", "op": "ne", "value": %q}}`, literal))
				assert.Contains(t, msg, fmt.Sprintf("%q", literal), "the offending literal")
				assert.Contains(t, msg, "is not a finite magnitude")
				assert.Contains(t, msg, "before the walk")
			})
		}
	})

	t.Run("refuses_an_unknown_operator_before_the_walk", func(t *testing.T) {
		msg := compareRefusal(t, sv,
			`{"compare": {"of": "node.metadata.font_ratio_to_body", "op": "gtt", "value": "1"}}`)
		assert.Contains(t, msg, `"gtt"`, "the offending spelling")
		// The edit-distance pass names every candidate within its threshold, and
		// the vocabulary it scores is sorted, so the clause is byte-stable.
		assert.Contains(t, msg, `did you mean "gt" or "gte" or "lt"?`, "the near-miss, from the edit-distance pass")
		assert.Contains(t, msg, "eq, gt, gte, lt, lte, ne", "the admitted vocabulary")
		assert.Contains(t, msg, "before the walk")

		// AN UPPER-CASE SPELLING IS REFUSED, NOT FOLDED, and the near-miss is
		// worded by the case-insensitive pass — a case flip changes every byte, so
		// no edit-distance threshold can name it.
		cased := compareRefusal(t, sv,
			`{"compare": {"of": "node.metadata.font_ratio_to_body", "op": "GT", "value": "1"}}`)
		assert.Contains(t, cased, `"GT"`)
		assert.Contains(t, cased, `did you mean "gt"?`)
		assert.Contains(t, cased, "matched exactly")
	})

	t.Run("refuses_a_non_numeric_operator_naming_the_leaf_that_serves_it", func(t *testing.T) {
		// `exists` and `prefix` are REAL members of the generated enum and real
		// author intents — they are just not ordered comparisons. The refusal names
		// the leaf that does serve each, rather than reporting them as typos.
		existsMsg := compareRefusal(t, sv,
			`{"compare": {"of": "node.metadata.font_ratio_to_body", "op": "exists", "value": "1"}}`)
		assert.Contains(t, existsMsg, "not an ordered comparison")
		assert.Contains(t, existsMsg, "use the `exists` leaf")

		prefixMsg := compareRefusal(t, sv,
			`{"compare": {"of": "node.metadata.font_ratio_to_body", "op": "prefix", "value": "1"}}`)
		assert.Contains(t, prefixMsg, "not an ordered comparison")
		assert.Contains(t, prefixMsg, "use the `matches` leaf with an anchored regex")
	})

	t.Run("refuses_an_of_key_the_source_graph_does_not_carry", func(t *testing.T) {
		// THE BAD-INPUT HALF, and the carrier that makes it reachable: the compare
		// leaf's `of` has to be routed into whereNodeOwnPaths, which the metadata
		// census reads. Omit that one append and this key reads empty on every row
		// — the silent class the validator exists to end — while every other
		// subtest here still passes.
		msg := compareRefusal(t, sv,
			`{"compare": {"of": "node.metadata.font_ratio_to_bodyy", "op": "gt", "value": "1"}}`)
		assert.Contains(t, msg, `"font_ratio_to_bodyy"`, "the offending key")
		assert.Contains(t, msg, "metadata key")
		assert.Contains(t, msg, `did you mean "font_ratio_to_body"?`)
		assert.Contains(t, msg, "before the walk")
	})

	t.Run("errors_on_non_numeric_text_read_off_a_row", func(t *testing.T) {
		// NEITHER ABSENCE. chrome_repeat_shaped is censused and stamped on every
		// row, and it holds "true"/"false": something wrote text where the recipe
		// expects a magnitude. That is an error naming the node and the value, not
		// a false predicate and not a coercion to zero.
		_, err := compareRun(t, sv,
			`{"compare": {"of": "node.metadata.chrome_repeat_shaped", "op": "gt", "value": "0"}}`)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "chrome_repeat_shaped", "the leaf's field")
		assert.Contains(t, err.Error(), `"true"`, "the offending value")
		assert.Contains(t, err.Error(), `"b1"`, "the node it was read off")
		assert.Contains(t, err.Error(), "not a number")
	})
}

// TestCompareOps_CoverEveryProtoOp pins the operator table to the GENERATED enum
// rather than to care.
//
// TWO LEGS, and they fail on opposite mistakes. Leg 1 requires every generated
// member to be admitted-or-declined, so the next operator added to
// MetadataPredicate.Op in the proto cannot land as an unnoticed silent gap. Leg 2
// requires every ADMITTED member to be one compareOrdered actually has an arm
// for, so an operator cannot be advertised in a refusal message and then reach
// the row loop's default arm.
func TestCompareOps_CoverEveryProtoOp(t *testing.T) {
	for name, num := range knowledgev1.MetadataPredicate_Op_value {
		op := knowledgev1.MetadataPredicate_Op(num)
		spelling := lowerOpSpelling(name)
		_, declined := declinedCompareOps[op]
		resolved, admitted := compareOp(spelling)

		require.NotEqual(t, declined, admitted,
			"generated operator %q must be either admitted or explicitly declined, and not both", name)
		if !admitted {
			continue
		}
		require.Equal(t, op, resolved, "the admitted spelling %q must resolve to its own enum member", spelling)

		// LEG 2: compareOrdered has a real arm for it. The default arm returns an
		// error, so an advertised-but-unhandled operator fails here rather than at
		// a user's row loop.
		_, err := compareOrdered(resolved, 1, 0)
		require.NoError(t, err, "admitted operator %q must be one compareOrdered applies", spelling)
		assert.Contains(t, compareOpVocabulary(), spelling,
			"an admitted operator must appear in the vocabulary a refusal renders")
	}

	// And the declined set is not merely unlisted: it is refused BY compareOp.
	for op := range declinedCompareOps {
		_, ok := compareOp(lowerOpSpelling(knowledgev1.MetadataPredicate_Op_name[int32(op)]))
		assert.False(t, ok, "declined operator %v must not resolve", op)
	}

	// The default arm itself is reachable only through a member outside the
	// admitted set, and it reports a validator bug rather than answering false.
	_, err := compareOrdered(knowledgev1.MetadataPredicate_OP_EXISTS, 1, 0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "validator bug")
}

// lowerOpSpelling turns a generated enum name into the recipe spelling for it.
func lowerOpSpelling(name string) string {
	return strings.ToLower(strings.TrimPrefix(name, "OP_"))
}

// TestCompareLeaf_LiteralResolvedOncePerRun is the PERFORMANCE observable: a
// compare leaf's LITERAL operand is parsed ONCE per run by the validator's
// resolve pass, not once per row.
//
// WHY IT NEEDS A COUNTER AT ALL. A per-row re-parse of the same literal returns
// identical answers on every input, so no correctness assertion in this file can
// see it — only the cost differs.
//
// THE BODY CARRIES TWO COMPARE LEAVES, AND THE COMPLIANT VALUE IS EXACTLY 2 —
// one resolve per leaf per run. The separation the gate rests on is that the
// compliant value is a single exact number while every violating placement reads
// strictly above it: the first leaf is evaluated on all four rows and excludes
// one, so the second is evaluated on three, and a per-row re-parse inside the
// evaluator reads 2 + 4 + 3 = 9. A one-leaf body would not distinguish a
// resolve-per-leaf implementation from a resolve-once-per-run one.
func TestCompareLeaf_LiteralResolvedOncePerRun(t *testing.T) {
	sv := compareFixture(t)
	before := compareLiteralParses.Load()

	got := compareSurvivors(t, sv, `{"all": [
		{"compare": {"of": "node.metadata.line_count", "op": "gt", "value": "1"}},
		{"compare": {"of": "node.metadata.font_ratio_to_body", "op": "gte", "value": "1.15"}}
	]}`)
	require.Equal(t, []string{"b1", "b2"}, got, "the run must actually have walked the rows")

	assert.Equal(t, int64(2), compareLiteralParses.Load()-before,
		"two compare leaves, each resolved once per run — a per-row re-parse reads 9 on this body")
}
