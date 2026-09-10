// SPDX-License-Identifier: Apache-2.0

// practice_hub_upsert_carry.go — the one thing `source_hub` does on a practice
// write WITHOUT being named: a mutate(upsert) of an existing member carries that
// member's stored hub forward onto the body it writes.
//
// WHY THIS IS NOT A GATE LIKE ITS SIBLINGS. Every other file in this family
// answers "what does the PARAMETER mean on this arm" and refuses a call that
// misuses it. This one is about the parameter's ABSENCE, and the answer is not a
// refusal: a member being edited by an ordinary hub-less upsert must keep its
// membership, and refusing that call instead would make every field edit of a
// grouped node an error.
//
// THE MECHANISM IT EXISTS FOR. An UPSERT writes the body's metadata rather than
// merging into what is stored — it is the only arm with those semantics, since
// update, update_batch and bulk_update_metadata all merge per key. So a hub-less
// upsert of a grouped node replaced its metadata with the payload's and dropped
// the `source_hub` key, while the node→hub `sourced-from` edge, which no upsert
// plan can carry, survived untouched. The node then had its two membership
// carriers disagreeing: a hub-scoped browse, which predicates on the key,
// returned nothing for it while the unfiltered browse still returned it. Nothing
// refused and nothing read, so no caller could tell. Worse, the state was not
// recoverable through this surface: every hub-carrying arm refuses a node that
// carries no key, and the create the refusal names REPLACES an existing node
// whole.
//
// SO THE CLIENT PRESERVES IT. On a practice upsert whose key RESOLVES to a node
// carrying a hub, and whose payload names no hub of its own, the stored value is
// stamped onto the body before the plan is composed. Membership is then recorded
// on two carriers that no field edit can split.
//
// IT COSTS ONE READ AND IT CANNOT COST NONE. Knowing whether an upsert key names
// an existing member is exactly what the read answers, so the hub-less upsert
// pays the same single bulk read the hub-scoped one pays. What IS unchanged is
// the WRITE: with nothing to preserve — a new id, or a node under no hub — this
// path declines and the engine compiles the caller's own payload, byte for byte
// as before.
//
// AND IT NEVER OVERWRITES A TYPED KEY. A caller who wrote `source_hub` into the
// body has stated the membership, so this path leaves the key alone. What it may
// state is checked one gate earlier: refusePracticeHubBodyWithoutParam admits a
// body naming the hub the node is ALREADY under and refuses one naming any other,
// because a body key rides no `sourced-from` edge and would leave the node filed
// under one hub and linked to another.
//
// A MEMBER IS NEVER MOVED BETWEEN HUBS ON THIS SURFACE. No arm rewrites both
// carriers together, so there is no write that moves a node rather than splitting
// it, and the answer is to say so rather than to leave a route that half-does it.

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"

	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// claimPracticeHubUpsertCarry preserves a practice member's hub across a hub-less
// upsert, claiming the call in order to do it.
//
// WHY CLAIMING CHANGES NOTHING BUT THE PAYLOAD. The call it claims is one that
// would otherwise DECLINE to the client's own engine dispatch, and that dispatch
// is engine.Dispatch over the graph caller's Execute and Stats — the same two
// seams persistExecutor and statsFnOf hand back here. So the claimed write is the
// declined write with one metadata key added, rather than a second write path
// that could drift from it. It is the same re-dispatch handleGraphPassthroughMutate
// performs for every other practice CRUD arm.
//
// IT RUNS LAST, below every refusal on this arm. A call that is malformed on its
// face, carries a legacy selector, names an unroutable param or names a hub for a
// gate to scope has already been refused above; what reaches here is a write that
// is going to the engine, and the only question left is whether it would strip a
// membership on the way.
func claimPracticeHubUpsertCarry(ctx context.Context, gc GraphCaller, a mutateArgs) (bool, kgtools.ToolResult) {
	if !practiceUpsertOwesHubCarry(a) {
		return false, kgtools.ToolResult{}
	}
	stored, err := storedPracticeHubOf(ctx, gc, a, a.ID)
	if err != nil {
		return true, errorResult("mutate(upsert): " + err.Error())
	}
	if stored == "" {
		// NOTHING TO PRESERVE — a new id, or a node grouped under no hub. The call
		// declines exactly as it did before this file existed, so the plan the
		// engine compiles is the caller's own payload.
		return false, kgtools.ToolResult{}
	}
	args, serr := practiceHubStampedUpsertArgs(a, stored)
	if serr != nil {
		return true, errorResult("mutate(upsert): " + serr.Error())
	}
	ex, eerr := persistExecutor(gc)
	if eerr != nil {
		return true, errorResult("mutate(upsert): " + eerr.Error())
	}
	stats, sferr := statsFnOf(gc)
	if sferr != nil {
		return true, errorResult("mutate(upsert): " + sferr.Error())
	}
	res, derr := engine.Dispatch(ctx, ex.Execute, stats, "mutate", args)
	if derr != nil {
		return true, errorResult("mutate(upsert): " + derr.Error())
	}
	return true, res
}

// practiceUpsertOwesHubCarry reports whether this call is the shape whose
// membership has to be preserved. Every conjunct is payload-decidable, so a call
// that owes nothing pays no read.
//
// THE TYPE CONJUNCT IS NOT DEFENSIVE. compileMutateUpsert denies a body missing
// `id` or `type`, so such a payload can never write and there is no membership
// for a write to strip; buying a round trip to discover that would spend a read on
// a call that was already going to be denied.
//
// A CALL THAT NAMES A HUB IS THE OTHER PATH, not this one: the engine's own
// lowering stamps the parameter onto the body, and the target gate above has
// already checked the key resolves under exactly that hub.
func practiceUpsertOwesHubCarry(a mutateArgs) bool {
	if a.Operation != "upsert" || a.Graph != string(kgtypes.GraphPractice) {
		return false
	}
	if a.SourceHub != "" {
		return false
	}
	if _, typed := a.Metadata[kgtypes.MetaKeySourceHub]; typed {
		return false
	}
	return a.ID != "" && a.Type != ""
}

// storedPracticeHubOf reads the hub an upsert key is currently grouped under, or
// "" when the key resolves to nothing or the node is grouped under none.
//
// IT IS THE SAME BULK READ AND THE SAME VISIBILITY the hub guards pay:
// foundation.FetchNodesByIDs against the combined practice graph, WITH
// tombstones. A soft-deleted node still belongs to the hub it was grouped under,
// and answering that question on a different visibility rule than the guard beside
// it would let the two disagree about whether the node exists at all.
//
// EVERY FAILURE MODE IS AN ERROR, never a "" that reads as "no membership". A
// caller-less client, a failed read and a TRUNCATED read are three ways of not
// knowing what the node is grouped under, and treating any of them as "grouped
// under nothing" would write the body unstamped — which is precisely the silent
// strip this path exists to prevent, arrived at from the other direction.
func storedPracticeHubOf(ctx context.Context, gc GraphCaller, a mutateArgs, id string) (string, error) {
	if gc == nil {
		return "", fmt.Errorf(
			"this upsert rewrites `id`=%q whole, and the combined practice graph could not be read to see "+
				"which `%s` it is grouped under, so the write would strip it; nothing was written",
			id, kgtypes.MetaKeySourceHub)
	}
	resolved, truncated, err := fetchPracticeNodesFor(ctx, gc, a, []string{id})
	if err != nil {
		return "", fmt.Errorf(
			"this upsert rewrites `id`=%q whole, and the combined practice graph could not be read to see "+
				"which `%s` it is grouped under, so the write would strip it; nothing was written: %w",
			id, kgtypes.MetaKeySourceHub, err)
	}
	if truncated {
		// FetchNodesByIDs REPORTS truncation rather than acting on it. One id
		// cannot reach the server's row ceiling, so this is not a live failure
		// mode — but a caller that ignored the verdict would be treating a node it
		// never saw as ungrouped, and writing the strip on that basis.
		return "", fmt.Errorf(
			"the membership read for `id`=%q came back TRUNCATED, so this call did not see the node and cannot "+
				"tell one grouped under no `%s` from one it failed to read; nothing was written",
			id, kgtypes.MetaKeySourceHub)
	}
	node := resolved[id]
	if node == nil || node.GetId() == "" {
		return "", nil
	}
	return node.GetMetadata()[kgtypes.MetaKeySourceHub], nil
}

// practiceHubStampedUpsertArgs returns the caller's own payload with the stored
// hub added to its `metadata`.
//
// IT REWRITES THE RAW PAYLOAD RATHER THAN RE-ENCODING mutateArgs, for the reason
// that struct's own doc gives: unknown fields pass through via the verbatim
// forward, and a re-encode from the struct would silently strip every key it has
// no field for. The envelope is decoded one level, into per-key raw messages, so
// exactly one key is rewritten and every other byte the caller sent survives.
//
// THE METADATA MAP COMES FROM THE DECODED ARGS, not from a second decode of the
// raw body: mutateArgs already decoded that key as map[string]string, so this
// cannot disagree with what the engine will decode from the payload it is handed.
func practiceHubStampedUpsertArgs(a mutateArgs, hub string) (json.RawMessage, error) {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(a.raw, &envelope); err != nil {
		return nil, fmt.Errorf(
			"the stored `%s` could not be carried onto this upsert because its payload did not decode, and "+
				"writing it unstamped would strip the membership; nothing was written: %w",
			kgtypes.MetaKeySourceHub, err)
	}
	// SIZED FROM ONE LENGTH, NOT A SUM: a make() capacity is a hint —
	// the one hub key costs at most a growth — while a size built by
	// addition is a value the allocator has to take on trust.
	metadata := make(map[string]string, len(a.Metadata))
	maps.Copy(metadata, a.Metadata)
	metadata[kgtypes.MetaKeySourceHub] = hub
	encoded, err := json.Marshal(metadata)
	if err != nil {
		// Cannot fail: a map of strings to strings. Checked rather than discarded
		// because writing the payload unstamped is the defect this path closes.
		return nil, fmt.Errorf("the stored `%s` could not be encoded; nothing was written: %w",
			kgtypes.MetaKeySourceHub, err)
	}
	envelope["metadata"] = encoded
	out, err := json.Marshal(envelope)
	if err != nil {
		return nil, fmt.Errorf("the stamped upsert payload could not be encoded; nothing was written: %w", err)
	}
	return out, nil
}
