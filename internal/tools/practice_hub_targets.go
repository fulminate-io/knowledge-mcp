// SPDX-License-Identifier: Apache-2.0

// practice_hub_targets.go — the `source_hub` selector on the four practice
// TARGET arms: mutate(update), mutate(update_batch), mutate(bulk_update_metadata)
// and mutate(upsert).
//
// WHAT THE SELECTOR MEANS HERE. These four arms name EXISTING nodes by id, so
// the hub does on them what it does on link and unlink: it SCOPES A RESOLUTION.
// The operation happens WITHIN the hub — every target id must already be grouped
// under it, or the call is refused naming the hub, the target and its actual hub
// or absence, with nothing written. It never GROUPS a target the way a create
// does, and it is never a membership move: a node belongs to exactly one hub and
// changing that is an explicit metadata write, not a side effect of an update.
//
// OMITTED MEANS THE WHOLE GRAPH, byte-for-byte as before this file existed. Every
// path returns before any read on an empty hub, so a hub-less update, batch or
// upsert issues the same plan and the same number of reads it always has.
//
// UPSERT IS ONE OF THE FOUR, NOT AN EXCEPTION TO THEM. An upsert whose key
// resolves is scoped exactly as the other three arms are. An upsert whose key
// does NOT resolve is REFUSED, naming mutate(create) as the arm that groups —
// see practiceHubUpsertUnresolvedRefusal for the executed store behaviour that
// rules out doing it here.
//
// IT REFUSES RATHER THAN FILTERING, on the same rule the edge arms follow: a
// caller that names a hub has told us which corpus it means, and writing to a
// target outside it — or dropping the target silently — is the coercion this repo
// refuses. Bad input errors.

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// practiceHubTarget is ONE write target: the id, plus the payload path that
// named it. The role is carried rather than derived because a refusal has to
// send the caller to the exact key it typed — `id`, `ids[2]`, `items[0].id` and
// `updates[1].id` are four different edits, and "one of your targets" is none of
// them.
type practiceHubTarget struct{ role, id string }

// practiceHubTargetsOf reads the write targets out of a payload, per operation.
//
// IT READS items[] AND updates[] OUT OF THE RAW PAYLOAD because the tools-side
// mutateArgs has no carrier for either — they are engine-side shapes, and the
// guard runs before the engine sees the call. That is the same seam
// guardUpdateBatchItemKeys reads at, for the same reason.
//
// A PAYLOAD THAT DOES NOT PARSE IS THE DISPATCHER'S ERROR, not this guard's: it
// stands aside with an empty set rather than preempting the dispatcher with a
// duplicate message about the same malformed json.
func practiceHubTargetsOf(a mutateArgs) []practiceHubTarget {
	switch a.Operation {
	case "update":
		// The singular id NORMALIZES the call to the single-target path, which is
		// the same precedence compileMutateByIDUpdate applies: an update carrying
		// both writes the one node.
		if a.ID != "" {
			return []practiceHubTarget{{role: "id", id: a.ID}}
		}
		out := make([]practiceHubTarget, 0, len(a.IDs))
		for i, id := range a.IDs {
			out = append(out, practiceHubTarget{role: fmt.Sprintf("ids[%d]", i), id: id})
		}
		return out
	case "upsert":
		if a.ID == "" {
			return nil
		}
		return []practiceHubTarget{{role: "id", id: a.ID}}
	case "update_batch":
		return practiceHubItemTargets(a.raw, "items")
	case "bulk_update_metadata":
		return practiceHubItemTargets(a.raw, "updates")
	}
	return nil
}

// practiceHubItemTargets pulls the per-item ids out of one array key of the raw
// payload. update_batch and bulk_update_metadata differ only in the key's name
// and in what else each item carries, and the hub scopes neither of those.
func practiceHubItemTargets(raw json.RawMessage, key string) []practiceHubTarget {
	// TWO DECODES, and the first one is not ceremony: the payload's other keys are
	// scalars, so decoding the whole object straight into a per-key array shape
	// FAILS on "operation" long before it reaches the array — and a failed decode
	// here reads as "this call names no targets", which is a refusal of a
	// perfectly good batch. The RawMessage pass takes the one key this guard is
	// about and leaves every other key undecoded.
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil //nolint:nilerr // a payload that does not parse is the dispatcher's error to report
	}
	body, present := envelope[key]
	if !present {
		return nil
	}
	var items []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &items); err != nil {
		return nil //nolint:nilerr // a malformed items[] is the dispatcher's error to report
	}
	out := make([]practiceHubTarget, 0, len(items))
	for i, it := range items {
		out = append(out, practiceHubTarget{role: fmt.Sprintf("%s[%d].id", key, i), id: it.ID})
	}
	return out
}

// guardPracticeHubScopesTargets is the ONE gate the three pure-update arms call,
// and it is one function for the same reason guardPracticeHubScopesEndpoints is:
// the arms are the population a reviewer enumerates to know the rule holds, and
// two spellings of one rule are how they drift.
//
// IT SELF-FILTERS ON THE PARAM AND THE FAMILY, so a caller can put it in front of
// a shared arm without that arm knowing which graph it serves. The family half is
// now a PRECONDITION rather than a decision: refusePracticeHubOffFamily refuses a
// hub on every family but practice at the head of InterceptMutate, so no payload
// reaches this gate off practice. It is kept because both call sites sit on arms
// that carry many families, and a guard that resolved ids in the practice graph
// for a foreign payload would refuse that write for not being under a hub it
// never claimed — a wrong refusal paid for with a wrong read.
// TestPracticeHubGuards_SelfFilterOnTheFamily observes it, since the dispatch no
// longer can.
//
// UPSERT COMES HERE TOO, and it is the same rule: its key is its target. The one
// thing it does differently is the message an UNRESOLVED key earns, because on an
// upsert that shape is the caller reaching for the create half rather than
// mis-typing an id, and it is sent to the arm that can serve it.
func guardPracticeHubScopesTargets(ctx context.Context, gc GraphCaller, a mutateArgs) error {
	if a.SourceHub == "" || a.Graph != string(kgtypes.GraphPractice) {
		return nil
	}
	// THE OPERATION FILTER IS PART OF THE GATE, not a caller obligation. Both call
	// sites sit on arms that carry many operations — the passthrough carries four
	// and the fallthrough effectively the whole vocabulary — so a gate that
	// trusted its caller to have filtered would refuse a create for not already
	// being under its own hub the first time someone moved the call. The four
	// operations below are exactly the ones that name EXISTING nodes by id.
	switch a.Operation {
	case "update", "update_batch", "bulk_update_metadata", "upsert":
	default:
		return nil
	}
	// A RESTATED PRECONDITION, not a second decision: refusePracticeHubNoTarget
	// runs at the head of InterceptMutate, above every arm, because the fault is
	// decidable from the payload and must be heard without a round trip. It is
	// kept here so this guard stays correct standing alone — a caller that wires
	// it somewhere else inherits the refusal rather than reading an empty target
	// list as nothing to check.
	targets := practiceHubTargetsOf(a)
	if err := refusePracticeHubNoTarget(a); err != nil {
		return err
	}
	resolved, err := resolvePracticeHubTargets(ctx, gc, a, targets)
	if err != nil {
		return err
	}
	for _, t := range targets {
		node := resolved[t.id]
		if a.Operation == "upsert" && (node == nil || node.GetId() == "") {
			return practiceHubUpsertUnresolvedRefusal(a.SourceHub, t.id)
		}
		if rerr := practiceHubEndpointRefusal(a.SourceHub, t.role, t.id, node); rerr != nil {
			return rerr
		}
	}
	return nil
}

// refusePracticeHubNoTarget refuses a hub-scoped call on a TARGET arm that names
// nothing for the hub to scope. It is a SEPARATE function for the reason
// refusePracticeHubEmptyEndpoint is: it has two call sites with different
// reaches. The head of InterceptMutate calls it before the hub resolution pays
// its read, because a payload that is wrong on its face must hear so for free;
// the target guard calls it again so that guard stands alone.
func refusePracticeHubNoTarget(a mutateArgs) error {
	if a.SourceHub == "" || a.Graph != string(kgtypes.GraphPractice) {
		return nil
	}
	switch a.Operation {
	case "update", "update_batch", "bulk_update_metadata", "upsert":
	default:
		return nil
	}
	if len(practiceHubTargetsOf(a)) > 0 {
		return nil
	}
	return practiceHubNoTargetRefusal(a)
}

// practiceHubNoTargetRefusal renders the refusal for a hub-scoped call that names
// nothing for the hub to scope. Upsert gets its own sentence because its target
// is not "the nodes to write" but the upsert KEY, and a message telling a caller
// to name the nodes when the field it left out is `id` names the wrong edit.
func practiceHubNoTargetRefusal(a mutateArgs) error {
	if a.Operation == "upsert" {
		return fmt.Errorf(
			"`%s`=%q scopes this upsert to the practice nodes grouped under that hub, and the call names no `id` "+
				"for it to scope — `id` is the upsert key; nothing was written",
			practiceHubParamOnWrites, a.SourceHub)
	}
	return fmt.Errorf(
		"`%s`=%q scopes this write to the practice nodes grouped under that hub, and this %s names no target "+
			"for it to scope; name the nodes to write, or drop %s to address the whole practice graph; "+
			"nothing was written",
		practiceHubParamOnWrites, a.SourceHub, a.Operation, practiceHubParamOnWrites)
}

// resolvePracticeHubTargets is the ONE bulk read every target arm pays, however
// many ids the payload names: foundation.FetchNodesByIDs pages internally, so a
// thousand-item update_batch costs a bounded drain rather than a read per item.
//
// IT READS WITH TOMBSTONES, matching guardPracticeHubScopesEndpoints and the
// sibling by-id probes on the same ids. A soft-deleted node still belongs to the
// hub it was grouped under — the question here is membership, not liveness — and
// answering it on a different visibility rule than the arms beside it would let
// the two disagree about whether a target exists at all.
//
// EVERY FAILURE MODE IS A REFUSAL, never a pass. A missing graph caller, a failed
// read and a TRUNCATED read are three different ways of not knowing which hub a
// target belongs to, and a guard that cannot see its targets has not checked
// them.
func resolvePracticeHubTargets(
	ctx context.Context, gc GraphCaller, a mutateArgs, targets []practiceHubTarget,
) (map[string]*knowledgev1.Node, error) {
	if gc == nil {
		return nil, fmt.Errorf(
			"`%s`=%q scopes this write to the practice nodes grouped under that hub, and the graph could not be "+
				"read to resolve %s; nothing was written",
			practiceHubParamOnWrites, a.SourceHub, practiceHubTargetList(targets))
	}
	ids := make([]string, 0, len(targets))
	seen := make(map[string]bool, len(targets))
	for _, t := range targets {
		if seen[t.id] {
			continue
		}
		seen[t.id] = true
		ids = append(ids, t.id)
	}
	resolved, truncated, err := fetchPracticeNodesFor(ctx, gc, a, ids)
	if err != nil {
		return nil, fmt.Errorf(
			"`%s`=%q scopes this write to the practice nodes grouped under that hub, and the combined practice "+
				"graph could not be read to resolve its targets, so the scope could not be checked; nothing was "+
				"written: %w",
			practiceHubParamOnWrites, a.SourceHub, err)
	}
	if truncated {
		// FetchNodesByIDs REPORTS truncation rather than acting on it, and a
		// caller that ignored the verdict would be treating a target it never saw
		// as absent. Unlike the two-endpoint edge arms, this one CAN reach the
		// ceiling: a batch names as many targets as the caller sends.
		return nil, fmt.Errorf(
			"`%s`=%q: the target read came back TRUNCATED, so this call did not see every target and cannot tell "+
				"one outside the hub from one it failed to read; nothing was written",
			practiceHubParamOnWrites, a.SourceHub)
	}
	return resolved, nil
}

// practiceHubTargetList renders the target roles for the could-not-read refusal,
// which is the one message that names no single offender — nothing was resolved,
// so every target is equally unchecked.
func practiceHubTargetList(targets []practiceHubTarget) string {
	parts := make([]string, 0, len(targets))
	for _, t := range targets {
		parts = append(parts, fmt.Sprintf("`%s`=%q", t.role, t.id))
	}
	return strings.Join(parts, ", ")
}

// practiceHubUpsertUnresolvedRefusal renders the refusal a hub-carrying upsert
// earns when its key resolves to nothing, and it sends the caller to
// mutate(create), which is the arm that groups.
//
// WHY THIS IS A REFUSAL AND NOT A GROUPED WRITE. Grouping a node under a hub is
// TWO recordings that must land together: the `source_hub` metadata key, which
// every hub-scoped browse, delete and member resolution predicates on, and the
// node→hub `sourced-from` edge, which a traverse walks. The engine's UPSERT
// lowering can carry the first — it writes the body's metadata — but NOT the
// second: the server's UPSERT decode refuses a plan carrying edges ("UPSERT is
// node-only — link separately"). So the only plan that can group is the CREATE
// plan, and this arm once re-dispatched the caller's payload as a create to get
// it.
//
// THE STORE BEHAVIOUR THAT RULES THAT OUT, executed rather than reasoned. The
// half was chosen from a read, so an id inserted between that read and the write
// met a create — and a create on an id that already exists does NOT fail. It
// returns Created, leaves the node count unchanged, REPLACES the node whole
// (clearing content, status and every metadata key the second payload omits) and
// appends a SECOND hub edge; reproduced on an isolated client against both
// binaries built from this branch, and measured independently a day earlier on a
// scratch daemon. The cloud plane overwrites too: its node upsert tail is an ON
// CONFLICT DO UPDATE. So that window was a silent whole-node clobber of somebody
// else's node — the silent-drop class this ticket exists to close, at a worse
// grade — and a comment in this file asserted the opposite without ever running
// it.
//
// WHAT A CALLER DOES INSTEAD is one call, not a workaround: mutate(create) with
// the same hub emits both recordings in one plan and is the documented grouping
// arm. Refusing keeps one meaning per param and leaves no half-grouped state,
// which the alternatives — a two-plan upsert-then-link, or stamping the key and
// dropping the edge — both create.
func practiceHubUpsertUnresolvedRefusal(hub, id string) error {
	return fmt.Errorf(
		"`%s`=%q scopes this upsert to the practice nodes grouped under that hub, and `id`=%q resolves to no "+
			"node in the combined practice graph, so there is nothing under the hub to upsert. An upsert cannot "+
			"GROUP a new node: grouping writes the %s metadata key AND the %s edge to the hub together, and only "+
			"a create plan carries both. Create it under the hub instead — mutate(create, graph:\"practice\", "+
			"source_hub:%q, ...) — or drop %s to upsert against the whole practice graph; nothing was written",
		practiceHubParamOnWrites, hub, id, kgtypes.MetaKeySourceHub, kgtypes.EdgeSourcedFrom, hub,
		practiceHubParamOnWrites)
}

// THE CELL THIS FILE ONCE LEFT OPEN, and where it was closed: `source_hub` named
// on the update / update_batch / bulk_update_metadata / upsert arms of a family
// that is NOT practice. It was left open here because the passthrough arm also
// serves graph:"checks", where the family-blind create lowering stamped the hub
// key and emitted the sourced-from edge, so refusing the param everywhere would
// have changed a shipped family's behaviour on a disposition nobody had decided.
//
// THAT DISPOSITION IS NOW DECIDED, and the cell is closed OUTSIDE this file:
// refusePracticeHubOffFamily runs once at the head of InterceptMutate and refuses
// the param on every family but practice, on every operation, checks included.
// The checks stamping needed no separate removal — it was never a checks arm, it
// was the shared lowering being reached with a foreign graph, and practice is now
// the only family that can reach it. The FAMILY x OPERATION grid that holds the
// whole rule is mutate_family_arm_hub_test.go.
