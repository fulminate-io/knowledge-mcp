// SPDX-License-Identifier: Apache-2.0

// practice_hub_bodies.go — the `source_hub` collision gate: a practice write
// whose payload names the hub TWICE, once as the call's parameter and once as a
// `source_hub` metadata key inside a body, and names two different hubs.
//
// WHY IT IS ITS OWN GATE. The sibling hub files each answer "what does this ARM
// do with the parameter" — group on the creates, scope on the target arms, scope
// the endpoints on the edge arms, select on delete. This one answers a question
// about the PAYLOAD instead, and the answer is the same on every arm that can
// carry a body: two spellings of one fact that disagree are bad input. Written
// per-arm it would close whichever body a fixture happened to carry and leave
// the rest open, which is the state it was written to end.
//
// WHAT THE OPEN STATE COST. A create_batch carrying `source_hub`=B with a body
// whose metadata named A was ACCEPTED: the node landed carrying A in its
// metadata and a `sourced-from` edge to B, so its two hub carriers disagreed and
// a hub-scoped browse — which predicates on the metadata key — filed it under a
// hub it is not edged to. Requirement 2 needs the two carriers to agree, and
// nothing observed that they did not.
//
// IT REFUSES RATHER THAN PICKING A WINNER. Preferring the body would silently
// contradict the edge the call's parameter draws; preferring the parameter would
// silently discard a key the caller typed. Bad input errors, naming the payload
// path, the body's hub and the call's, so the caller edits the one they did not
// mean.
//
// IT IS PAYLOAD-DECIDABLE AND PAYS NO READ, which is why it sits ABOVE the two
// gates that resolve ids: a call that is wrong on its face hears so without a
// round trip.

package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// THE HUB-LESS BODY RULES LIVE HERE TOO, beside the gate that compares a body's
// hub with the CALL'S. They are the same question asked of a payload that names
// no hub at the call: a body may repeat a membership, never change one. Keeping
// the pair in one file is what stops a later edit from tightening one spelling
// of the rule and leaving the other.

// practiceHubBodyCarrier is ONE body in a payload that names a hub: the path the
// caller typed, the body's own id when it has one, and the hub it names.
//
// THE ROLE IS CARRIED RATHER THAN DERIVED, for the reason practiceHubTarget
// carries its own: `metadata`, `nodes[1].metadata` and `items[0].metadata` are
// three different edits, and a refusal that said "a body" would name none of
// them. The ID IS CARRIED BESIDE IT because a batch composed by a driver has
// indexes that are positions in a generated list — the id is the row its author
// can find.
//
// Only bodies that PRESENT the key become carriers. A body with no hub key has
// no second spelling to disagree with, and the create lowering stamps the call's
// hub onto it exactly as it always has.
type practiceHubBodyCarrier struct {
	role string
	id   string
	hub  string
}

// practiceHubBodyCarriersOf reads every metadata carrier a write payload holds.
//
// THE TOP-LEVEL `metadata` IS A CARRIER ON EVERY ARM, including the batch shapes
// that do not route it: a caller who typed a hub there meant it, and a param
// that contradicts the call is bad input whether or not this arm would have
// written it.
//
// THE PER-BODY CARRIERS ARE READ OUT OF THE RAW PAYLOAD because the tools-side
// mutateArgs has no field for `nodes`, `items` or `updates` — they are
// engine-side shapes, and this gate runs before the engine sees the call. That
// is the same seam practiceHubTargetsOf reads its per-item ids at, for the same
// reason.
func practiceHubBodyCarriersOf(a mutateArgs) []practiceHubBodyCarrier {
	carriers := make([]practiceHubBodyCarrier, 0, 1)
	if hub, ok := a.Metadata[kgtypes.MetaKeySourceHub]; ok {
		carriers = append(carriers, practiceHubBodyCarrier{
			role: "metadata." + kgtypes.MetaKeySourceHub, id: a.ID, hub: hub,
		})
	}
	switch a.Operation {
	case "create_batch":
		return append(carriers, practiceHubArrayBodyCarriers(a.raw, "nodes")...)
	case "update_batch":
		return append(carriers, practiceHubArrayBodyCarriers(a.raw, "items")...)
	case "bulk_update_metadata":
		return append(carriers, practiceHubArrayBodyCarriers(a.raw, "updates")...)
	}
	return carriers
}

// practiceHubArrayBodyCarriers pulls the per-body metadata out of one array key
// of the raw payload. The three array-carrying operations differ only in the
// key's name and in what else each entry holds, and the hub key sits in the same
// place in all three.
//
// TWO DECODES, for practiceHubItemTargets' reason: the payload's other keys are
// scalars, so decoding the whole object straight into a per-key array shape
// fails on `operation` long before it reaches the array. A PAYLOAD THAT DOES NOT
// PARSE IS THE DISPATCHER'S ERROR — this gate stands aside with no carriers
// rather than preempting it with a second message about the same malformed json.
func practiceHubArrayBodyCarriers(raw json.RawMessage, key string) []practiceHubBodyCarrier {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil //nolint:nilerr // a payload that does not parse is the dispatcher's error to report
	}
	body, present := envelope[key]
	if !present {
		return nil
	}
	var entries []struct {
		ID       string            `json:"id"`
		Metadata map[string]string `json:"metadata"`
	}
	if err := json.Unmarshal(body, &entries); err != nil {
		return nil //nolint:nilerr // a malformed body array is the dispatcher's error to report
	}
	out := make([]practiceHubBodyCarrier, 0, len(entries))
	for i, entry := range entries {
		hub, ok := entry.Metadata[kgtypes.MetaKeySourceHub]
		if !ok {
			continue
		}
		out = append(out, practiceHubBodyCarrier{
			role: fmt.Sprintf("%s[%d].metadata.%s", key, i, kgtypes.MetaKeySourceHub),
			id:   entry.ID,
			hub:  hub,
		})
	}
	return out
}

// guardPracticeHubBodyHubs is the gate itself, and it is ONE function for the
// reason its two siblings are: the arms are the population a reviewer enumerates
// to know the rule holds, and two spellings of one rule are how they drift.
//
// IT SELF-FILTERS ON THE PARAM AND THE FAMILY, so a caller can put it in front
// of a shared arm without that arm knowing which graph it serves. An empty
// `source_hub` is the whole-graph call, which has no second spelling to
// disagree with; an off-family call has already been refused by
// refusePracticeHubOffFamily at the head of InterceptMutate, and the family test
// is kept here because both call sites sit on arms that carry many families.
//
// THE FIRST DISAGREEING BODY IS THE MESSAGE, not a list: a caller fixes one edit
// at a time, and the next call re-runs the whole gate.
func guardPracticeHubBodyHubs(a mutateArgs) error {
	if a.SourceHub == "" || a.Graph != string(kgtypes.GraphPractice) {
		return nil
	}
	for _, carrier := range practiceHubBodyCarriersOf(a) {
		if carrier.hub == a.SourceHub {
			// THE SAME HUB IS NOT A CONFLICT, it is a second spelling of one fact.
			// The create lowering leaves an explicit key alone, so an agreeing body
			// lands byte-identically to one that named no hub at all.
			continue
		}
		return practiceHubBodyConflictRefusal(a.SourceHub, carrier)
	}
	return nil
}

// practiceHubBodyConflictRefusal renders the refusal. It names the payload path
// the caller has to edit, the hub the body asked for, the hub the call asked
// for, and — when the body carries one — the body's own id.
func practiceHubBodyConflictRefusal(callHub string, carrier practiceHubBodyCarrier) error {
	named := ""
	if carrier.id != "" {
		named = fmt.Sprintf(" (the body's `id` is %q)", carrier.id)
	}
	return fmt.Errorf(
		"`%s`=%q and `%s`=%q name different hubs%s - a node belongs to exactly one, and writing both would "+
			"leave its metadata contradicting its %s edge; drop whichever you did not mean; nothing was written",
		practiceHubParamOnWrites, callHub, carrier.role, carrier.hub, named, kgtypes.EdgeSourcedFrom)
}

// practiceHubBodyArms is the set of operations whose BODY metadata reaches a
// written node. It is the same population guardPracticeHubBodyHubs states, and
// the reason the rule below stops there: link, unlink, answer and delete write no
// body, so a hub key in their payload reaches nothing for this gate to compare.
func practiceHubBodyArms(op string) bool {
	switch op {
	case "create", "create_batch", "update", "update_batch", "bulk_update_metadata", "upsert":
		return true
	}
	return false
}

// refusePracticeHubBodyWithoutParam is the rule for a body that names a hub when
// the CALL names none, and it closes the last route to a split membership.
//
// WHY A BODY KEY IS NOT A MEMBERSHIP WRITE. Grouping is TWO recordings that must
// agree: the `source_hub` metadata key and the node→hub `sourced-from` edge. Only
// the create plan carries both. A body key alone therefore writes one of them:
// on a create it lands the key with NO edge, and on an update or an upsert it
// REPOINTS the key while the edge stays on the hub the node was grouped under.
// Both states are the one requirement 2 forbids, and both were reachable — the
// second was documented as the way to move a node between hubs, which it is not:
// it splits the node rather than moving it.
//
// SO THE RULE IS THE SAME ONE guardPracticeHubBodyHubs APPLIES to a call that
// names a hub: the body may repeat a fact, never change one. A body naming the
// hub the node is ALREADY grouped under is a second spelling and proceeds
// untouched; a body naming any other hub is refused naming both. On a create
// there is no stored hub to agree with, so the caller is sent to the parameter,
// which is the only thing that emits the edge.
//
// MOVING A MEMBER BETWEEN HUBS IS NOT AN OPERATION THIS SURFACE OFFERS. There is
// no arm that rewrites both carriers together, so there is no way to move one
// without splitting it, and the honest answer is to say so rather than to leave a
// route that half-does it.
//
// IT READS THE MEMBERSHIP ITSELF: the hub's own validity moved into the engine,
// so the one read this gate needs — what the named nodes are already grouped
// under — is issued here through fetchPracticeNodesFor.
func refusePracticeHubBodyWithoutParam(ctx context.Context, gc GraphCaller, a mutateArgs) error {
	if a.SourceHub != "" || a.Graph != string(kgtypes.GraphPractice) || !practiceHubBodyArms(a.Operation) {
		return nil
	}
	if a.Operation == "create" || a.Operation == "create_batch" {
		// THE CREATE SIDE IS THE ENGINE'S RULE, rendered here. A body key is legal
		// when the payload carries the membership edge for it — which is what the
		// recipe landing's own batch does — and when the body IS the hub, keyed to
		// its own id. Stating that a second time here is how this gate came to
		// refuse the shipped landing's shape; it calls the one implementation now.
		return engine.GuardPracticePayload(a.raw)
	}
	for _, carrier := range practiceHubBodyCarriersOf(a) {
		if carrier.hub == "" {
			continue
		}
		targets := practiceHubBodyCarrierTargets(a, carrier)
		resolved, truncated, rerr := fetchPracticeNodesFor(ctx, gc, a, targets)
		if rerr != nil || truncated {
			return practiceHubBodyUnreadableRefusal(carrier, rerr, truncated)
		}
		for _, id := range targets {
			stored := ""
			if node := resolved[id]; node != nil {
				stored = node.GetMetadata()[kgtypes.MetaKeySourceHub]
			}
			if stored == carrier.hub {
				continue
			}
			return practiceHubBodyMoveRefusal(carrier, id, stored)
		}
	}
	return nil
}

// practiceHubBodyCarrierTargets names the nodes ONE body carrier would write. A
// per-body carrier names its own id; the top-level `metadata` map applies to
// every target the call names, which on an ids[] update is more than one.
func practiceHubBodyCarrierTargets(a mutateArgs, carrier practiceHubBodyCarrier) []string {
	if carrier.id != "" {
		return []string{carrier.id}
	}
	targets := practiceHubTargetsOf(a)
	ids := make([]string, 0, len(targets))
	for _, t := range targets {
		ids = append(ids, t.id)
	}
	return ids
}

// practiceHubBodyMoveRefusal renders the refusal for a body that would repoint an
// existing member's key away from the hub its edge still points at.
func practiceHubBodyMoveRefusal(carrier practiceHubBodyCarrier, id, stored string) error {
	grouped := fmt.Sprintf("is grouped under %q", stored)
	if stored == "" {
		grouped = "is grouped under NO hub"
	}
	return fmt.Errorf(
		"`%s`=%q would write that hub onto `%s`, which %s — and the write carries no %s edge, so the node "+
			"would end up filed under one hub by its metadata and linked to another. A practice node is not "+
			"MOVED between hubs on this surface: no arm rewrites both carriers together. Name the hub it is "+
			"already under, or drop the key; nothing was written",
		carrier.role, carrier.hub, id, grouped, kgtypes.EdgeSourcedFrom)
}

// practiceHubBodyUnreadableRefusal is this gate's every-failure-is-a-refusal arm:
// a body that would repoint a membership is admitted only against the hub the
// node is stored under, and a gate that could not read that has not checked it.
func practiceHubBodyUnreadableRefusal(carrier practiceHubBodyCarrier, err error, truncated bool) error {
	if truncated {
		return fmt.Errorf(
			"`%s`=%q would write that hub onto an existing node, and the membership read came back TRUNCATED, "+
				"so this call did not see what the node is grouped under; nothing was written",
			carrier.role, carrier.hub)
	}
	return fmt.Errorf(
		"`%s`=%q would write that hub onto an existing node, and the combined practice graph could not be read "+
			"to see what it is grouped under; nothing was written: %w",
		carrier.role, carrier.hub, err)
}
