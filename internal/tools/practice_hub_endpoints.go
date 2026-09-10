// SPDX-License-Identifier: Apache-2.0

// practice_hub_endpoints.go — the `source_hub` selector on the two practice EDGE
// arms, mutate(link) and mutate(unlink).
//
// WHAT THE SELECTOR MEANS HERE, AND WHY IT IS NOT THE CREATE'S MEANING. On a
// create the hub GROUPS the written node: it stamps the hub id onto the node's
// metadata (withSourceHub) and draws the node→hub `sourced-from` edge
// (sourceHubEdges), both in the engine's create lowering. An
// EDGE belongs to no hub, so there is nothing for a link or an unlink to group,
// and this arm does the other thing the same word means across this surface — it
// SCOPES A RESOLUTION. `source` on assemble scopes a by-id resolve and answers
// "that id is not under this hub" rather than serving it; a link names two ids,
// and the same scoping applied to both of them is what `source_hub` does here:
// both endpoints must be practice nodes grouped under the named hub, or the
// write is refused naming which endpoint failed and why.
//
// OMITTED MEANS THE WHOLE GRAPH, exactly as it did before this arm existed. The
// selector adds a narrowing; it never adds a default. Every path below returns
// nil immediately on an empty hub, so a link or unlink that names none behaves
// byte-for-byte as it always has, down to the number of reads it issues.
//
// IT REFUSES RATHER THAN FILTERING. A hub-scoped link whose `to` sits under
// another hub is a caller asking for a write it has also told us it does not
// want; writing it anyway is the silent coercion this repo refuses, and dropping
// it silently would be worse still. Bad input errors.

package tools

import (
	"context"
	"fmt"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// guardPracticeHubScopesEndpoints is the ONE gate both edge arms call, and it is
// deliberately one function rather than a check per arm: the arms are the
// population a reviewer has to enumerate to know the rule holds, and the two
// spellings of one rule are how they drift. It mirrors
// refusePracticeLanguageOnWrite, which the same two arms already share.
//
// IT SELF-FILTERS ON BOTH THE PARAM AND THE FAMILY, exactly as
// guardPracticeHubScopesTargets does. An empty `source_hub` is the whole-graph
// reading and returns before any read; a foreign family returns too, because it
// has ALREADY been refused — refusePracticeHubOffFamily now runs once at the head
// of InterceptMutate, above every arm, so by the time any arm-level gate is
// reached the family is practice. This filter is therefore a precondition
// restated, not a second refusal: two spellings of one rule are how they drift,
// and the reachable one is the head gate.
//
// THE ENDPOINT RESOLVE IS ONE READ FOR BOTH IDS. foundation.FetchNodesByIDs
// hydrates from and to in a single bounded Execute, so the scoping costs one
// round trip regardless of how the arm downstream probes. It reads WITH
// tombstones because render.FetchNodeIn — the probe the link arm's own claim
// path uses on these same two ids — reads with them: the question here is which
// hub an endpoint belongs to, and answering it on a different visibility rule
// than the sibling probe would let the two disagree about whether an endpoint
// exists at all.
func guardPracticeHubScopesEndpoints(ctx context.Context, gc GraphCaller, a mutateArgs) error {
	if a.SourceHub == "" || a.Graph != string(kgtypes.GraphPractice) {
		return nil
	}
	if err := refusePracticeHubEmptyEndpoint(a); err != nil {
		return err
	}
	if gc == nil {
		return fmt.Errorf(
			"`%s`=%q scopes this write to the practice nodes grouped under that hub, and the graph could not be "+
				"read to resolve %q and %q; nothing was written",
			practiceHubParamOnWrites, a.SourceHub, a.From, a.To)
	}
	endpoints, truncated, err := fetchPracticeNodesFor(ctx, gc, a, []string{a.From, a.To})
	if err != nil {
		return fmt.Errorf(
			"`%s`=%q scopes this write to the practice nodes grouped under that hub, and the combined practice "+
				"graph could not be read to resolve its endpoints, so the scope could not be checked; nothing "+
				"was written: %w",
			practiceHubParamOnWrites, a.SourceHub, err)
	}
	if truncated {
		// Two ids cannot reach the server's row ceiling, so this arm is not a
		// live failure mode — it is here because FetchNodesByIDs REPORTS
		// truncation rather than acting on it, and a caller that ignored the
		// verdict would be treating an endpoint it never saw as absent. The
		// helper reports; this caller decides, and its decision is to refuse.
		return fmt.Errorf(
			"`%s`=%q: the endpoint read came back TRUNCATED, so this call did not see both endpoints and cannot "+
				"tell one outside the hub from one it failed to read; nothing was written",
			practiceHubParamOnWrites, a.SourceHub)
	}
	for _, ep := range []struct{ role, id string }{{"from", a.From}, {"to", a.To}} {
		if rerr := practiceHubEndpointRefusal(a.SourceHub, ep.role, ep.id, endpoints[ep.id]); rerr != nil {
			return rerr
		}
	}
	return nil
}

// refusePracticeHubOffFamily refuses a `source_hub` named on ANY family but
// practice, on EVERY mutate operation. It has ONE production call site — the head
// of InterceptMutate, above every routing branch — and that is the point of it:
// the arms are the population a reviewer would otherwise have to enumerate to
// know the rule holds, and a rule spelled once per arm is a rule with one arm
// missing. Placing it above the dispatch tree makes every arm downstream of it by
// construction rather than by an author remembering.
//
// WHY EVERY FAMILY AND NOT JUST THE EDGE ONES. `source_hub` is a practice-family
// parameter: it GROUPS on a create, SCOPES the targets or endpoints of the other
// write arms, and SELECTS on a delete. Every one of those meanings is about
// practice source hubs, and no other family has any. On a foreign family the
// param can only reach nothing — and on some of them it reached WORSE than
// nothing before this gate: the engine's create lowering is family-blind, so a
// checks or code create stamped a practice hub key and a sourced-from edge onto a
// node of a family that has no hubs to point at.
//
// A REFUSAL RATHER THAN A DROP, on the rule that bad input errors: the caller
// asked for a narrowing this family cannot express, and serving the unnarrowed
// write is a different write from the one it asked for.
//
// IT IS PAYLOAD-DECIDABLE, which is why it sits with the other payload-decidable
// refusals at the head rather than beside the gates that pay a read. A call that
// is wrong on its face hears so without a round trip.
func refusePracticeHubOffFamily(a mutateArgs) error {
	if a.SourceHub == "" || a.Graph == string(kgtypes.GraphPractice) {
		return nil
	}
	// ONE TEXT, RENDERED FROM THE ENGINE. This position exists for its PLACE — it
	// speaks above the dispatch tree, before a graph caller is even resolved, so a
	// call that is wrong on its face hears so on a degraded client too. The
	// sentence is the engine's, because the same rule runs there for the callers
	// that never reach this dispatch, and two spellings of one refusal are two
	// things that drift. The engine's own arm reads BOTH hub spellings; this one
	// reads the only spelling a mutate payload can carry.
	return engine.PracticeHubOffFamilyRefusal(practiceHubParamOnWrites, a.SourceHub, a.Graph, a.Operation)
}

// refusePracticeHubEmptyEndpoint refuses a hub-scoped practice edge write whose
// `from` or `to` is EMPTY, and it is a SEPARATE function for the same reason
// refusePracticeHubOffFamily is: it has two call sites with different reaches.
// The guard above calls it before paying for a read, and the LINK block calls it
// alone, ahead of the cross-graph composer — because an empty endpoint makes that
// composer decline, and the call then lands on the generic link fall-through
// whose rejection reason sends the caller to `mutate(link, graph:"practice",
// source_hub:...)`, which is the call it just made. A message that names the
// call the caller is already making names nothing.
//
// THE FAULT IT NAMES IS THE EMPTY ENDPOINT, which is the edit the caller has to
// make. An empty id resolves to no node in any graph, so it can be under no hub
// and no amount of re-reading the hub documentation fixes it.
func refusePracticeHubEmptyEndpoint(a mutateArgs) error {
	if a.SourceHub == "" || a.Graph != string(kgtypes.GraphPractice) {
		return nil
	}
	// THE OPERATION FILTER IS THE GUARD'S OWN, not its caller's, because one call
	// site sits at the top of the dispatch where every operation passes. Only the
	// two EDGE arms name from and to; on every other operation an empty pair is
	// the normal shape, and refusing it there would refuse most of the surface.
	if a.Operation != "link" && a.Operation != "unlink" {
		return nil
	}
	for _, ep := range []struct{ role, id string }{{"from", a.From}, {"to", a.To}} {
		if ep.id != "" {
			continue
		}
		return fmt.Errorf(
			"`%s`=%q scopes this write to the practice nodes grouped under that hub, and `%s` is EMPTY — an "+
				"empty endpoint names no node, so it can be under no hub. Name both `from` and `to`; nothing "+
				"was written",
			practiceHubParamOnWrites, a.SourceHub, ep.role)
	}
	return nil
}

// practiceHubEndpointRefusal renders the refusal for ONE endpoint, or nil when
// that endpoint is grouped under the named hub.
//
// THE THREE FAILURES ARE THREE MESSAGES, not one. "Not under this hub" collapses
// a node that is not in the graph at all, a node that belongs to no hub, and a
// node that belongs to a DIFFERENT one — and those three send the caller to
// three different fixes: check the id, group the node, or name the other hub.
// Each message therefore states the hub asked for and the endpoint's own actual
// hub or the specific absence, which is what the caller needs to act.
func practiceHubEndpointRefusal(hub, role, id string, node *knowledgev1.Node) error {
	if node == nil || node.GetId() == "" {
		return fmt.Errorf(
			"`%s`=%q scopes this write to the practice nodes grouped under that hub, and `%s`=%q resolves to no "+
				"node in the combined practice graph at all, so it is under no hub; nothing was written",
			practiceHubParamOnWrites, hub, role, id)
	}
	actual, present := node.GetMetadata()[kgtypes.MetaKeySourceHub]
	switch {
	case !present || actual == "":
		return fmt.Errorf(
			"`%s`=%q scopes this write to the practice nodes grouped under that hub, and `%s`=%q carries no %s "+
				"metadata key at all — it is grouped under NO hub. Group it first (a create takes %s, and the "+
				"node also carries a %s edge to its hub), or drop %s to address the whole practice graph; "+
				"nothing was written",
			practiceHubParamOnWrites, hub, role, id, kgtypes.MetaKeySourceHub,
			practiceHubParamOnWrites, kgtypes.EdgeSourcedFrom, practiceHubParamOnWrites)
	case actual != hub:
		return fmt.Errorf(
			"`%s`=%q scopes this write to the practice nodes grouped under that hub, and `%s`=%q is grouped "+
				"under %q instead. A node belongs to exactly one hub: name that hub, or drop %s to address the "+
				"whole practice graph; nothing was written",
			practiceHubParamOnWrites, hub, role, id, actual, practiceHubParamOnWrites)
	}
	return nil
}
