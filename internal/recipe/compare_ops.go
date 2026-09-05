// SPDX-License-Identifier: Apache-2.0

package recipe

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/ast"
)

// compare_ops.go — the compare leaf's operator vocabulary, its operand parse and
// its ordered comparison.
//
// THE VOCABULARY IS GENERATED, NOT HAND-WRITTEN. gen/knowledge/v1/engine.pb.go
// carries MetadataPredicate_Op_name and MetadataPredicate_Op_value, generated
// from MetadataPredicate.Op in proto/knowledge/v1/engine.proto. Resolving a
// spelling through that generated map rather than a hand-listed switch is what
// stops this vocabulary drifting the next time the proto gains an operator, and
// it is the discipline expr_static_validate.go's builtinTable already follows —
// pinned to its dispatch by a test rather than by care. It is also the ONLY
// cross-module contract this package may lean on: the repo admits no shared
// hand-written package between the client and server binaries, and a generated
// protobuf enum is exactly the shared parse that rule permits.
//
// MIRRORED SHAPE, NOT SHARED CODE.
// cmd/knowledge-server/internal/store/query_executor_match.go's metaCompare is
// the server's numeric-cast-else-text ordering over the same enum, and that file
// records that an ABSENT key never satisfies an ordered comparison. This leaf
// mirrors that shape; it does not import it. ONE DELIBERATE DIVERGENCE:
// metaCompare falls BACK to a lexical comparison when either side fails to
// parse, and this leaf does not — a non-numeric operand is an error naming the
// value, never coerced and never silently answered as text.
//
// Cost: everything here runs ONCE PER COMPARE LEAF PER RUN, from the validator's
// resolve pass, except compareOrdered, which runs per row and parses and
// allocates nothing. compareOpVocabulary allocates only when a refusal is being
// rendered.

// compareLiteralParses counts REAL numeric parses of a compare leaf's LITERAL
// operand by the validator's resolve pass — one per compare leaf per run, never
// one per row.
//
// That is enough for the property it exists to make measurable: a per-row
// re-parse of the same literal returns identical answers on every input, so no
// correctness test can see it; only this counter moves, by the ROW COUNT instead
// of the LEAF COUNT. Nothing the evaluator does increments it, because the
// evaluator parses no literal.
//
// replication: process-local by design — this is a client-side measurement aid
// with no replicated meaning. It counts parses within one process, a restart
// re-zeroes it, and no reader outside this process consults it.
var compareLiteralParses atomic.Int64

// declinedCompareOps are the generated enum members this leaf does NOT admit,
// each named with the leaf that serves that shape instead.
//
// THEY ARE DECLINED EXPLICITLY RATHER THAN BY OMISSION. A member left out of an
// admitted list is indistinguishable from a member nobody has noticed yet, so
// the pin that requires every generated member to be admitted-or-declined would
// have nothing to check against. Declining them by name also gives a refusal
// something useful to say: `exists` and `prefix` are real author intents this
// grammar already serves, just not here.
var declinedCompareOps = map[knowledgev1.MetadataPredicate_Op]string{
	knowledgev1.MetadataPredicate_OP_UNSPECIFIED: "not an operator",
	knowledgev1.MetadataPredicate_OP_EXISTS:      "use the `exists` leaf",
	knowledgev1.MetadataPredicate_OP_PREFIX:      "use the `matches` leaf with an anchored regex",
}

// compareOp resolves a recipe's operator spelling to its generated enum member.
//
// THE SPELLING IS MATCHED EXACTLY AND NOTHING IS FOLDED, which is the rule
// validate_source.go's header states for every other vocabulary in this package:
// a name that is not already lower case is refused rather than repaired, so `LT`
// and `Lt` reach the refusal — with suggestCompareOp's case-insensitive pass to
// word it — instead of being quietly accepted as `lt`.
func compareOp(name string) (knowledgev1.MetadataPredicate_Op, bool) {
	if name != strings.ToLower(name) {
		return knowledgev1.MetadataPredicate_OP_UNSPECIFIED, false
	}
	num, ok := knowledgev1.MetadataPredicate_Op_value["OP_"+strings.ToUpper(name)]
	if !ok {
		return knowledgev1.MetadataPredicate_OP_UNSPECIFIED, false
	}
	op := knowledgev1.MetadataPredicate_Op(num)
	if _, declined := declinedCompareOps[op]; declined {
		return knowledgev1.MetadataPredicate_OP_UNSPECIFIED, false
	}
	return op, true
}

// compareOpVocabulary returns the admitted spellings, sorted, derived from the
// same generated map minus the declined set — so a refusal can never list an
// operator compareOp would reject, and can never omit one it would accept.
func compareOpVocabulary() []string {
	out := make([]string, 0, len(knowledgev1.MetadataPredicate_Op_value))
	for name, num := range knowledgev1.MetadataPredicate_Op_value {
		if _, declined := declinedCompareOps[knowledgev1.MetadataPredicate_Op(num)]; declined {
			continue
		}
		out = append(out, strings.ToLower(strings.TrimPrefix(name, "OP_")))
	}
	sort.Strings(out)
	return out
}

// suggestCompareOp returns the near-miss clause a refusal appends, or "" when
// nothing plausible is close enough to name.
//
// THE CASE-INSENSITIVE PASS RUNS FIRST, and it is not redundant with the
// edit-distance pass: a case flip changes every byte it touches, so `LT` against
// `lt` scores an edit distance of 2 out of a 2-character name and no distance
// threshold can name it. It is the same ordering sourceCensus.suggest uses, for
// the same measured reason.
//
// NEITHER PASS MATCHING RETURNS "", so a refusal never invents advice. An
// operator that is real but declined gets the declined clause instead, which
// tells the author which leaf does serve their intent.
func suggestCompareOp(name string) string {
	admitted := compareOpVocabulary()
	for _, candidate := range admitted {
		if strings.EqualFold(candidate, name) {
			return fmt.Sprintf(
				"did you mean %q? operators are matched exactly, in lower case", candidate)
		}
	}
	if num, ok := knowledgev1.MetadataPredicate_Op_value["OP_"+strings.ToUpper(name)]; ok {
		if why, declined := declinedCompareOps[knowledgev1.MetadataPredicate_Op(num)]; declined {
			return fmt.Sprintf("%q is not an ordered comparison: %s", strings.ToLower(name), why)
		}
	}
	near := ast.ClosestVocabulary(name, admitted)
	if len(near) == 0 {
		return ""
	}
	return fmt.Sprintf("did you mean %s?", quoteJoin(near))
}

// parseNumericOperand parses one operand as a FINITE float64.
//
// NOTHING IS TRIMMED AND NOTHING IS COERCED. A value with surrounding whitespace
// or a trailing unit is text where a magnitude was expected, and the caller
// turns that into an error naming the offender rather than a silently repaired
// number or a zero.
//
// NaN AND THE INFINITIES ARE REFUSED, and a bare ParseFloat is why they have to
// be refused HERE rather than left to the caller: Go's ParseFloat accepts "NaN",
// "nan", "Inf", "+Inf", "-Inf" and "infinity" as valid float64 values, so
// without this guard they resolve and the run proceeds. That is bad input
// answered as a false predicate — the conflation this leaf's own doc comment
// says it prevents — and under `ne` it is worse than a zero-row answer: NaN
// compares false against everything, so `not-equal` admits the ENTIRE rowset
// from a meaningless threshold.
//
// It guards the ROW VALUE as well as the literal, because both operands come
// through here. A node stamped "NaN" is then an error naming the node rather
// than a row silently dropped from every ordered comparison.
func parseNumericOperand(s string) (float64, error) {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, err
	}
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, fmt.Errorf("%q is not a finite magnitude", s)
	}
	return f, nil
}

// compareOrdered applies one resolved operator to two parsed magnitudes.
//
// THE DEFAULT ARM REPORTS A VALIDATOR BUG RATHER THAN RETURNING FALSE. Every op
// reaching here came through compareOp, so an unhandled member means the
// admitted set and this dispatch have drifted apart — and a false would answer
// the author's question wrongly with zero rows, which is the exact silence this
// whole grammar exists to end. TestCompareOps_CoverEveryProtoOp is what keeps
// the arm unreachable in practice.
func compareOrdered(op knowledgev1.MetadataPredicate_Op, got, want float64) (bool, error) {
	switch op {
	case knowledgev1.MetadataPredicate_OP_EQ:
		return got == want, nil
	case knowledgev1.MetadataPredicate_OP_NE:
		return got != want, nil
	case knowledgev1.MetadataPredicate_OP_LT:
		return got < want, nil
	case knowledgev1.MetadataPredicate_OP_LTE:
		return got <= want, nil
	case knowledgev1.MetadataPredicate_OP_GT:
		return got > want, nil
	case knowledgev1.MetadataPredicate_OP_GTE:
		return got >= want, nil
	default:
		return false, fmt.Errorf(
			"recipe: compare leaf reached the row loop with operator %q, which compareOrdered does not apply — "+
				"a validator bug: compareOp admitted an operator this dispatch has no arm for. Admitted operators: %s",
			op.String(), joinPlain(compareOpVocabulary()))
	}
}
