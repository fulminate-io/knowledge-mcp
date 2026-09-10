// SPDX-License-Identifier: Apache-2.0

// practice_hub_resolve.go — the gate that asks whether a `source_hub` NAMES A
// HUB, and the one bulk read every hub-scoped call pays.
//
// WHY IT IS A SEPARATE CONCERN FROM ITS FIVE SIBLINGS. Every other hub file
// answers a question about the PARAMETER: which arms take it, which targets it
// scopes, which families may name it, which bodies may repeat it, which arm may
// not drop it. Each of them takes the id on trust. This one answers the question
// about the NODE, and it is the coarsest sufficient carrier for it: one
// resolution, above every arm, rather than a row per arm that a new arm would
// eventually be added without.
//
// WHAT AN UNRESOLVED HUB COST. A write carrying a hub id that named nothing was
// accepted on every arm. The node landed carrying the id in its `source_hub`
// metadata AND a `sourced-from` edge to an id no node holds, so the two carriers
// requirement 2 makes one fact disagreed permanently and nothing could repair
// them: a hub-scoped browse counted the node as a member, a traverse from the
// node reached no hub, and a traverse from the hub could not resolve its own
// root. Bad input errors — and a hub id that names nothing is bad input on every
// arm that takes it.
//
// THE FOUR WAYS AN ID FAILS ARE FOUR MESSAGES, for practiceHubEndpointRefusal's
// reason: absent, deleted, some other node type, and a member id used as a hub
// send the caller to four different fixes. The last two are the same test — a
// hub is a node of type source — but they are not the same sentence, because a
// caller who typed a member id has made a different mistake from one who typed a
// pattern id by accident.
//
// THE HUB'S OWN VALIDITY IS NOT DECIDED HERE ANY MORE. Resolving the hub id —
// that it names a live `source` node keyed to nothing but itself — moved into the
// engine (practice_hub_guard.go), beneath every caller, because two callers reach
// the engine without passing this dispatch at all. What stays in this file is the
// TARGET and ENDPOINT scoping, which is a different rule about a different id
// set, and the payload-decidable refusals that need no read.

package tools

import (
	"context"
	"encoding/json"
	"fmt"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

// fetchPracticeNodesFor is the ONE seam every practice hub gate reads through. It
// issues a bulk by-ids read with tombstones, so a deleted node is a different
// answer from a missing one. The hub-resolution cache it once served from moved
// into the engine with the rule that needed it.
func fetchPracticeNodesFor(
	ctx context.Context, gc GraphCaller, _ mutateArgs, ids []string,
) (map[string]*knowledgev1.Node, bool, error) {
	return foundation.FetchNodesByIDs(ctx, gc, kgtypes.GraphPractice, "", ids, foundation.IncludeTombstones)
}

// guardPracticeHubHead runs every practice-hub gate that belongs above the
// dispatch tree, in the ONE order they are correct in, and returns the args
// carrying the read the arms below consume.
//
// THE ORDER IS THE CONTRACT, which is why it is one function rather than four
// call sites at the head. The two PAYLOAD-DECIDABLE refusals come first: a
// payload naming two different hubs at once, and a hub-scoped target arm naming
// no target, are wrong on their face, and hearing them after a round trip spends
// a read to say what the payload already said. The RESOLUTION comes next, because
// it is the first gate that must read. The body rule comes last, because it reads
// the resolution's cache rather than paying for a read of its own.
//
// THE EMPTY-ENDPOINT REFUSAL LEADS, because it is the cheapest and because its
// position is load-bearing in the other direction too: an empty `from` or `to`
// makes the cross-graph link composer DECLINE, and the call then lands on the
// generic link fall-through whose rejection reason points the caller at the
// practice link it is already making. Naming the empty endpoint here, above that
// composer, is what stops a message about the wrong fault.
//
// refusePracticeHubOffFamily is NOT here, and its absence is deliberate: it runs
// above the graph caller is even resolved, so it answers on a degraded client
// too. Its position is stated at its call site.
func guardPracticeHubHead(ctx context.Context, gc GraphCaller, a mutateArgs) (mutateArgs, error) {
	if err := refusePracticeMembershipEdgeWrite(a); err != nil {
		return a, err
	}
	if err := refusePracticeHubEmptyEndpoint(a); err != nil {
		return a, err
	}
	if err := refusePracticeHubOnAHub(a); err != nil {
		return a, err
	}
	if err := guardPracticeHubBodyHubs(a); err != nil {
		return a, err
	}
	if err := refusePracticeHubNoTarget(a); err != nil {
		return a, err
	}
	return a, refusePracticeHubBodyWithoutParam(ctx, gc, a)
}

// refusePracticeMembershipEdgeWrite refuses a practice link or unlink that names
// the membership relation itself, whether or not the call carries a hub.
//
// WHY THE RELATION IS NOT WRITABLE BY HAND. Membership is one fact on two
// carriers, and every arm that can write it writes BOTH or refuses: the create
// plan carries the `source_hub` key and the `sourced-from` edge together, and the
// by-hub delete removes the member entirely. An edge arm carries no metadata, so
// a caller naming this relation is asking for exactly half of the write — and the
// three shapes it produced are the three the reviews found live: a member's edge
// repointed to another hub while the key stayed, the edge removed while the key
// stayed, and a SECOND outgoing sourced-from edge from one member to another,
// which is a node grouped under a pattern rather than under a hub.
//
// IT SELF-FILTERS ON THE FAMILY AND THE OPERATION, NOT ON THE HUB. The hub gates
// beside it read the parameter; this one reads the RELATIONSHIP, and a caller
// who names no hub reaches the same half-write. That is why it is the one hub
// gate whose empty-hub case is not a return.
//
// AN ORDINARY RELATION IS UNTOUCHED: a practice link or unlink of any other
// relationship behaves exactly as it did, scoped or not.
func refusePracticeMembershipEdgeWrite(a mutateArgs) error {
	if a.Graph != string(kgtypes.GraphPractice) {
		return nil
	}
	if a.Operation != "link" && a.Operation != "unlink" {
		return nil
	}
	// EXACTLY, not case-insensitively. Folding an edge type merges two stored
	// families, which is its own defect class; the case variant a caller can send
	// is canonicalised later by the engine's edge-type resolve and is refused
	// there, at the second position, where the spelling is already the graph's
	// own. See engine/practice_membership_edge.go for both positions and why
	// there are two.
	if !engine.IsPracticeMembershipEdge(a.Relationship) {
		return nil
	}
	// ONE MESSAGE FOR BOTH POSITIONS, rendered from the engine package that owns
	// the rule, so the two cannot drift into telling a caller two different things.
	return engine.PracticeMembershipEdgeRefusal(a.Relationship)
}

// refusePracticeHubOnAHub refuses a create that would group a `source` node under
// another hub, which is the other half of the nesting rule the resolver states.
//
// WHY NESTING IS REFUSED RATHER THAN DEFINED. A hub is the identity of a
// collection. A collection that is itself a member makes "the hub this node is
// grouped under" a chain instead of a value, and every reader of the key resolves
// exactly one hop: the hub-scoped browse, the by-hub delete and the target gates
// would all report the immediate hub while a traverse over the edges walks the
// whole chain. Nothing in this surface expresses a nested collection, so a shape
// that produces one is refused at both ends — here, and at the resolver, which
// will not accept a nested node AS a hub.
func refusePracticeHubOnAHub(a mutateArgs) error {
	if a.SourceHub == "" || a.Graph != string(kgtypes.GraphPractice) {
		return nil
	}
	for _, role := range practiceHubCreatedSourceTypes(a) {
		return fmt.Errorf(
			"`%s`=%q would group `%s`, which is a %q node — a hub — under another hub. A hub is the identity "+
				"of a collection and is grouped under nothing: nesting one would make the hub a node belongs "+
				"to a chain rather than a value, and every reader of the %s key resolves one hop. Create the "+
				"hub without %s; nothing was written",
			practiceHubParamOnWrites, a.SourceHub, role, kgtypes.NodeSource,
			kgtypes.MetaKeySourceHub, practiceHubParamOnWrites)
	}
	return nil
}

// practiceHubCreatedSourceTypes names every payload path on a create shape whose
// body declares type `source`. It reads the batch bodies out of the raw payload
// at the same seam practiceHubBodyCarriersOf reads their metadata, because the
// tools-side mutateArgs has no field for `nodes`.
func practiceHubCreatedSourceTypes(a mutateArgs) []string {
	switch a.Operation {
	case "create":
		if a.Type == string(kgtypes.NodeSource) {
			return []string{"type"}
		}
		return nil
	case "create_batch":
	default:
		return nil
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(a.raw, &envelope); err != nil {
		return nil //nolint:nilerr // a payload that does not parse is the dispatcher's error to report
	}
	body, present := envelope["nodes"]
	if !present {
		return nil
	}
	var entries []struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(body, &entries); err != nil {
		return nil //nolint:nilerr // a malformed nodes[] is the dispatcher's error to report
	}
	out := make([]string, 0, 1)
	for i, entry := range entries {
		if entry.Type == string(kgtypes.NodeSource) {
			out = append(out, fmt.Sprintf("nodes[%d].type", i))
		}
	}
	return out
}
