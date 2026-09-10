// SPDX-License-Identifier: Apache-2.0

// practice_membership_edge.go — the practice membership relation's refusal, and
// the SECOND of the two positions it is stated at.
//
// WHY TWO POSITIONS, AND WHY BOTH COMPARE EXACTLY. The rule is that a practice
// link or unlink may not name `sourced-from`: membership is the `source_hub`
// metadata key AND that edge together, an edge arm carries only the edge, and
// half a write is the split every earlier round closed a route to.
//
// The tools layer states it first, above the whole dispatch tree, because that is
// where a caller gets a legible message before the cross-graph link composer
// turns an edge fault into a different one. But the tools layer sees the caller's
// RAW spelling, and the relationship is canonicalised later: resolveArgsEdgeTypes
// adopts an existing stored spelling on a unique case-insensitive match, so a
// caller sending `SOURCED-FROM` reaches the write as `sourced-from`. That was
// measured, not reasoned — on a tree without this second position the variant
// linked and the two carriers split.
//
// A CASE-INSENSITIVE COMPARISON WOULD HAVE COVERED BOTH FROM ONE POSITION, and it
// is not what this does. Case-folding an edge type merges two stored families,
// which is a defect class the corpus checks bind; the answer is not to fold at
// the top but to state the rule again where the spelling is ALREADY canonical.
// So both positions compare exactly, and neither has to guess about casing.
//
// ONE MESSAGE, ONE PLACE. Both positions render this package's sentence, so the
// two cannot drift into telling a caller two different things about one rule.

package engine

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// PracticeMembershipEdgeRefusal renders the refusal both positions share.
func PracticeMembershipEdgeRefusal(relationship string) error {
	return fmt.Errorf(
		"`relationship`=%q is the practice MEMBERSHIP edge, and it is not written by hand: a node's hub is "+
			"recorded on two carriers at once — the %s metadata key and this edge — and this arm carries only "+
			"the edge, so the write would leave the two disagreeing. A node is grouped by mutate(create, "+
			"graph:\"practice\", %s:...), which writes both in one plan, and a whole collection is removed by "+
			"mutate(delete, graph:\"practice\", %s:...). Use another relationship to relate two practice nodes; "+
			"nothing was written",
		relationship, kgtypes.MetaKeySourceHub, kgtypes.MetaKeySourceHub, kgtypes.MetaKeySourceHub)
}

// IsPracticeMembershipEdge reports whether a relationship is EXACTLY the practice
// membership relation. Exact by design: see the file header.
func IsPracticeMembershipEdge(relationship string) bool {
	return relationship == sourceHubEdgeType
}

// refusePracticeMembershipEdgeArgs is the engine-side position, run on the
// CANONICALISED arguments — after resolveArgsEdgeTypes has rewritten the
// relationship to the graph's own stored spelling — so the case variant the
// resolver adopts is refused here even though the tools layer saw a different
// string.
//
// IT DECODES ONLY WHAT IT DECIDES ON. A malformed payload is the generic flow's
// error to report, not this gate's, so it stands aside rather than preempting it.
func refusePracticeMembershipEdgeArgs(args json.RawMessage) error {
	var a struct {
		Operation    string `json:"operation"`
		Graph        string `json:"graph"`
		Relationship string `json:"relationship"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return nil //nolint:nilerr // a payload that does not parse is the dispatcher's error to report
	}
	if a.Graph != string(kgtypes.GraphPractice) {
		return nil
	}
	if a.Operation != "link" && a.Operation != "unlink" {
		return nil
	}
	if !IsPracticeMembershipEdge(a.Relationship) {
		return nil
	}
	return PracticeMembershipEdgeRefusal(a.Relationship)
}

// prepareMutateArgs is the mutate branch's pre-Compile step: it CANONICALISES the
// relationship against the target graph's own edge vocabulary and then applies
// the rules that can only be decided on the canonical spelling.
//
// THE ORDER IS THE WHOLE POINT. resolveArgsEdgeTypes adopts an existing stored
// spelling on a unique case-insensitive match, so a caller's `SOURCED-FROM`
// becomes `sourced-from` here — and the membership rule, which compares exactly
// because folding an edge type merges two stored families, is stated after it
// rather than before.
func prepareMutateArgs(
	ctx context.Context, stats StatsFn, tool string, args json.RawMessage,
) (json.RawMessage, string, error) {
	next, notice, rerr := resolveArgsEdgeTypes(ctx, stats, tool, args)
	if rerr != nil {
		return nil, "", rerr
	}
	if merr := refusePracticeMembershipEdgeArgs(next); merr != nil {
		return nil, "", merr
	}
	return next, notice, nil
}

// guardPracticeDispatch is the Dispatch-side call of the hub rules, shared by the
// mutate and delete branches so the standalone delete tool runs the same
// resolution the mutate arms do. See practice_hub_guard.go.
func guardPracticeDispatch(ctx context.Context, exec ExecuteFn, args json.RawMessage) error {
	return GuardPracticeWrite(ctx, exec, args)
}
