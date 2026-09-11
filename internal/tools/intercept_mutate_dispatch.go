// SPDX-License-Identifier: Apache-2.0

package tools

// intercept_mutate_dispatch.go holds the arm-selection helpers InterceptMutate
// and handleInterceptMutateUpdate delegate to. Each is a branch of the dispatch
// decision tree lifted verbatim out of its caller so both callers stay inside
// the function-length and complexity gates; the routing order is unchanged.

import (
	"context"
	"fmt"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/backends/dispatch"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// handleGraphPassthroughMutate claims a practice/checks CRUD mutation that
// carries no link_graph. With no link_graph these are engine-reducible, lowering
// to a Target-routed MutationPlan (Target.Graph == practice/checks), so
// they route through engine.Dispatch: a reducible op compiles→Execute→render and
// a non-reducible one falls back to legacy. The `link` op is intentionally
// excluded — the cross-graph link composer upstream owns that decision tree.
// Returns (false,_) for every other shape; the caller then applies the
// knowledge-graph guard. Split out of InterceptMutate to keep its decision tree
// inside the complexity gate.
func handleGraphPassthroughMutate(
	ctx context.Context,
	gc GraphCaller,
	a mutateArgs,
	params kgtools.CallToolParams,
) (bool, kgtools.ToolResult) {
	if a.Graph != "practice" && a.Graph != checksGraphSelector {
		return false, kgtools.ToolResult{}
	}
	if a.LinkGraph != "" {
		return false, kgtools.ToolResult{}
	}
	switch a.Operation {
	case "create", "create_batch", "update", "delete":
	default:
		return false, kgtools.ToolResult{}
	}
	if err := accountMutateParams(armGraphPassthrough, a); err != nil {
		return true, errorResult(err.Error())
	}
	// THE `language` REFUSAL IS NOT HERE ANY MORE. It used to sit on this arm,
	// which every practice create, create_batch, update and delete passes
	// through, with the link and unlink arms gated at their own entry points —
	// four spellings of one rule. It now runs ONCE at the head of InterceptMutate,
	// above every routing branch, so no arm can be reached carrying the param.
	// THE HUB SELECTOR ON THE UPDATE ARM, which is this arm's half of the target
	// class. It is scoped to `update` because the hub means three different things
	// across this one arm's four operations: create and create_batch GROUP the
	// node they write (the engine's create lowering stamps the key and emits the
	// sourced-from edge), delete uses it as a SELECTION axis (compileDelete lowers
	// it to a metadata predicate), and only update names existing targets for it
	// to scope. Running it on the other three would refuse a create for not
	// already being under the hub it is being grouped into.
	//
	// It sits AFTER the two payload-decidable refusals above because it pays a
	// READ: a call malformed on its face should hear so for free rather than after
	// a round trip.
	if a.Operation == "update" {
		if err := guardPracticeHubScopesTargets(ctx, gc, a); err != nil {
			return true, errorResult("mutate(update): " + err.Error())
		}
	}
	// No fixture, no admission: a check-carrying CHECKS-graph write is validated
	// against its own examples before anything reaches the store. Covers create,
	// create_batch and update here; delete carries no metadata and skips.
	//
	// checks is in this arm's graph set above precisely so this call can fire.
	// The guard self-filters on graph=="checks", so admitting only practice and
	// checks here would leave it permanently inert on the one path it
	// exists to protect, while still looking like a live gate.
	if err := guardCorpusCheckWrite(ctx, gc, a); err != nil {
		return true, errorResult(err.Error())
	}
	ex, eerr := persistExecutor(gc)
	if eerr != nil {
		return true, errorResult("mutate(" + a.Operation + "): " + eerr.Error())
	}
	// The Execute seam comes from persistExecutor, but the STATS seam comes from
	// gc, which is in scope on the same path: the executor is the persistence
	// wrapper, and the vocabulary read is a plain per-call routed RPC.
	stats, serr := statsFnOf(gc)
	if serr != nil {
		return true, errorResult("mutate(" + a.Operation + "): " + serr.Error())
	}
	res, err := engine.Dispatch(ctx, ex.Execute, stats, "mutate", params.Arguments)
	if err != nil {
		return true, errorResult("mutate(" + a.Operation + "): " + err.Error())
	}
	return true, res
}

// dispatchClientMutateCreate claims create=finding/research/rule by type, then
// claims ANY OTHER typed create that carries a context-link param:
// every knowledge-graph create routes the context-link trio, so a ticket_id,
// session or links on a generic type is born-linked rather than refused. A
// create carrying none of the three still falls through to the engine create
// arm, and a criterion create never reaches here at all: InterceptAddCriterion
// fires earlier in the chain. Split out of InterceptMutate to keep its decision
// tree inside the complexity gate.
func dispatchClientMutateCreate(ctx context.Context, deps ClientDeps, a mutateArgs) (bool, kgtools.ToolResult) {
	// ABOVE the type switch so ONE call covers both arms a plan_annotation can
	// take: the context-linked arm, which is how `links:[<section>]` attaches it,
	// and the fallthrough. A guard placed inside either arm would leave the other
	// ungated while still looking like a live gate.
	if err := guardPlanAnnotationCreate(a); err != nil {
		return true, errorResult(err.Error())
	}
	switch a.Type {
	case "finding":
		if err := accountMutateParams(armCreateFinding, a); err != nil {
			return true, errorResult(err.Error())
		}
		return true, handleClientMutateCreateFinding(ctx, deps, a)
	case "research":
		if err := accountMutateParams(armCreateResearch, a); err != nil {
			return true, errorResult(err.Error())
		}
		return true, handleClientMutateCreateResearch(ctx, deps, a)
	case "rule":
		if err := accountMutateParams(armCreateRule, a); err != nil {
			return true, errorResult(err.Error())
		}
		return true, handleClientMutateCreateRule(ctx, deps, a)
	}
	// The type-blind claim. `a.Type != ""` preserves today's behaviour for a
	// malformed type-less create: it keeps falling to armCreateFallthrough, whose
	// accounting rejects the trio exactly as it does now, and the engine's create
	// lowering declines an empty type anyway.
	if a.Type != "" && contextParamsSupplied(a) {
		if err := accountMutateParams(armCreateContextLinked, a); err != nil {
			return true, errorResult(err.Error())
		}
		return true, handleClientMutateCreateContextLinked(ctx, deps, a)
	}
	if err := accountMutateParams(armCreateFallthrough, a); err != nil {
		return true, errorResult(err.Error())
	}
	return false, kgtools.ToolResult{}
}

// accountDefaultBucketMutate runs param accounting for the five operations that
// reach the dispatch default bucket and decline to an engine arm. Any other
// operation returns nil — see the default-branch comment for why gating them
// would be wrong.
//
// create_batch also runs the step↔criterion pair gate, which is a PAYLOAD-shape
// check rather than a param-classification one: the accounting table answers
// "is this param routed", and a one-directional criterion attachment is a
// well-formed edges[] the engine will happily write and plan_tree will not show.
// It runs AFTER the accounting so a rejected param keeps its own message; see
// mutate_criterion_pair.go for the convention and what the check can decide.
func accountDefaultBucketMutate(a mutateArgs) error {
	switch a.Operation {
	case "create_batch":
		if err := accountMutateParams(armCreateBatch, a); err != nil {
			return err
		}
		if err := guardCreateBatchCriterionPair(a.raw); err != nil {
			return err
		}
		return guardCreateBatchPlanAnnotations(a.raw)
	case "upsert":
		return accountMutateParams(armUpsert, a)
	case "update_batch":
		if err := accountMutateParams(armUpdateBatchItems, a); err != nil {
			return err
		}
		// The second payload-shape check at this seam, beside create_batch's pair.
		// The accounting table answers "is this top-level param routed"; a
		// well-formed params set can still carry an items[] entry whose key
		// nothing reads, and that is what this refuses.
		return guardUpdateBatchItemKeys(a.raw)
	case "bulk_update_metadata":
		return accountMutateParams(armBulkUpdateMetadata, a)
	case "unlink":
		return accountMutateParams(armUnlink, a)
	}
	return nil
}

// handleLocalOnlyMutateUpdate routes a SINGLE-id update on a node with no
// external backing, in precedence order: terminal-status container rollup →
// per-type first-class-param router → generic engine dispatch fall-through.
// Each later arm fires only when the earlier ones decline. Split out of
// handleInterceptMutateUpdate to keep both inside the function-length gate.
func handleLocalOnlyMutateUpdate(
	ctx context.Context,
	deps ClientDeps,
	gc GraphCaller,
	a mutateArgs,
	node *knowledgev1.Node,
) (bool, kgtools.ToolResult) {
	// Claim closure-rollup for local-only container updates carrying a TERMINAL
	// status on project/ticket/plan/phase/step/test_plan/test_step. The client owns
	// the cascade. It writes the MAPPED descendant status only to descendants that
	// are themselves one of those seven container types; every other type is held —
	// the cascade never writes the status of a node that records evidence — and
	// named in the response instead.
	//
	// The single predicate absorbs all four conjuncts this guard used to test —
	// node-present, container-type, the expand_to_descendants opt-out and the
	// status test — and the tracker-backed arm consults the SAME one, so the two
	// arms cannot drift on which updates cascade. An empty return means "do not
	// cascade": the explicit-false update then falls through to the
	// typed-router/engine single-node path below, which writes the caller's status
	// to the NAMED container only — a real single-node update, not a no-op.
	if cascadeStatus := cascadeStatusForContainerUpdate(a, node); cascadeStatus != "" {
		if err := accountMutateParams(armUpdateRollup, a); err != nil {
			return true, errorResult(err.Error())
		}
		return true, handleClientUpdateStatusRollup(ctx, gc, a, node, cascadeStatus)
	}
	// Per-type first-class-param routing: a typed knowledge node update
	// (criterion/rule/finding/...) routes its create-time params
	// (command/criterion_type/scope/enforcement/evidence/source) into metadata,
	// re-derives the summary for a rule or finding, re-stamps a criterion's name,
	// and loudly rejects
	// params unroutable for the type. Fires AFTER the backend + rollup arms so it
	// claims only non-backend non-rollup typed updates.
	if claimed, res := handleClientMutateUpdateTyped(ctx, deps, a, node); claimed {
		return true, res
	}
	// Non-backend non-rollup update the typed router did not claim — return
	// (false,_) to route through the cloud-aware engine dispatch.
	if err := accountMutateParams(armUpdateFallthrough, a); err != nil {
		return true, errorResult(err.Error())
	}
	return false, kgtools.ToolResult{}
}

// handleBackendMutateUpdate routes a SINGLE-id update on a node that carries an
// external backing: the Linear write-through, then the knowledge-graph forward,
// then the terminal-status cascade. It is the sibling of
// handleLocalOnlyMutateUpdate above, split out of handleInterceptMutateUpdate for
// the same reason and with the routing order unchanged.
func handleBackendMutateUpdate(
	ctx context.Context,
	deps ClientDeps,
	gc GraphCaller,
	a mutateArgs,
	node *knowledgev1.Node,
	backendName string,
	params kgtools.CallToolParams,
) (bool, kgtools.ToolResult) {
	if err := accountMutateParams(armUpdateBackend, a); err != nil {
		return true, errorResult(err.Error())
	}
	// Clear-to-blank has no meaning on a tracker-backed node: the work item lives
	// in an external tracker whose status vocabulary has no blank state, so the
	// write could not be represented there even though it is legal locally.
	// Rejected BEFORE any tracker write, so the item is left untouched.
	if statusExplicitlySupplied(a.raw) && a.Status == "" {
		return true, errorResult(fmt.Sprintf(
			"mutate(update): status cannot be cleared to blank — node %s is backed by the %s tracker, "+
				"which has no blank state; set an explicit status instead",
			a.ID, backendName,
		))
	}
	backend := deps.BackendResolver().ByName(backendName)
	if backend == nil {
		return true, errorResult(fmt.Sprintf(
			"mutate(update): backend %q recorded on node %s but not currently configured",
			backendName, a.ID,
		))
	}

	updateArgs := dispatch.UpdateArgs{NodeID: a.ID}
	if a.Name != "" {
		v := a.Name
		updateArgs.Name = &v
	}
	if a.Description != "" {
		v := a.Description
		updateArgs.Description = &v
	}
	if a.Status != "" {
		v := a.Status
		updateArgs.Status = &v
	}
	if pri, ok := a.Metadata["priority"]; ok && pri != "" {
		v := parsePriority(pri)
		updateArgs.Priority = &v
	}
	if labels, ok := a.Metadata["labels"]; ok && labels != "" {
		v := labels
		updateArgs.Labels = &v
	}

	if err := dispatch.Update(ctx, node, backendName, backend, updateArgs); err != nil {
		return true, errorResult("mutate(update): " + err.Error())
	}

	// Build the forwarded args from a FRESH map (NOT in-place mutation
	// of caller's metadata). stripBackendPrivateMetadata is pure. The
	// knowledge-graph forward runs AFTER the backend dispatch.Update above,
	// routed through the login-aware Execute carrier seam (by-id UPDATE, cloud
	// when logged in) — the ordering + the desync message below are preserved
	// byte-for-byte.
	forwardedArgs := marshalForwardedMutateUpdateArgs(a, backendName)
	if _, err := executeMutate(ctx, gc, forwardedArgs); err != nil {
		return true, errorResult(fmt.Sprintf(
			"Linear update succeeded for %s, but local update failed: %v; the next manual update will not be a no-op until local catches up",
			a.ID, err,
		))
	}
	// The terminal-status cascade, on the SAME shared predicate the local arm
	// consults. Before this the tracker-backed arm returned here unconditionally,
	// so a container carrying an external backing never reached the cascade at ANY
	// status — the completed rollup included. Its live descendants stayed live
	// under a closed or cancelled work item.
	//
	// It runs AFTER both writes because the root's own status rides the local
	// forward above; cascadeToLiveDescendants writes only the descendants. The
	// partial state a failure here leaves, and why re-issuing is safe, are stated
	// at cascadeBackendFailureResult.
	if cascadeStatus := cascadeStatusForContainerUpdate(a, node); cascadeStatus != "" {
		summary, cerr := cascadeToLiveDescendants(ctx, gc, a, cascadeStatus)
		if cerr != nil {
			return true, cascadeBackendFailureResult(backendName, a.ID, cerr)
		}
		return true, textResult(fmt.Sprintf(
			"mutate(update): backend %q + local update succeeded for %s", backendName, a.ID) + summary)
	}
	// Surface a thin success message — the local mutate's result would
	// usually be empty for an update path, and the user typically cares
	// only that both halves landed.
	_ = params // reserved for future passthrough use
	return true, textResult(fmt.Sprintf("mutate(update): backend %q + local update succeeded for %s", backendName, a.ID))
}

// guardNonKnowledgeMutate is the FOREIGN-GRAPH GUARD: every mutate whose graph
// is neither empty nor knowledge passes through it, and it either refuses the
// call or declines it to the engine.
//
// LIFTED OUT OF InterceptMutate rather than inlined, on that function's own
// precedent: it holds a decision tree the cognitive-complexity gate bounds, and
// the file's rule is to move a branch out rather than raise the limit. The four
// gates below run in a fixed order and each one's position is load-bearing, so
// they are read together here instead of interleaved with the knowledge-graph
// routing they have nothing to do with.
//
// The (false, empty) return is the DECLINE, and it is the common case: a foreign
// graph the client has no model of routes on to the engine unchanged.
func guardNonKnowledgeMutate(
	ctx context.Context, gc GraphCaller, a mutateArgs,
) (bool, kgtools.ToolResult) {
	// THE `language` REFUSAL IS NOT HERE ANY MORE EITHER. This gate carried it for
	// the arms that DECLINE rather than claim — upsert, unlink, update_batch,
	// bulk_update_metadata, and a link the cross-graph composer declined — because
	// without it they were accounted as consumed and then compiled into a target
	// with no language on it at all, so the write landed in the combined graph
	// while the caller believed it had addressed practice/go. That silent drop is
	// still refused, one line at the head of InterceptMutate, which covers these
	// arms and the claiming ones with one spelling.
	// Backend-backed nodes only live in the knowledge graph.
	//
	// The link conjunct is load-bearing: the link block upstream does NOT return
	// when the cross-graph composer declines, so a declined link carrying a
	// non-knowledge graph reaches here too. It was already accounted upstream
	// under its own arm, and accounting it a second time under a different spec
	// would reject a call neither arm rejects on its own.
	if err := accountNonKnowledgeMutate(a); err != nil {
		return true, errorResult(err.Error())
	}
	// THE HUB SELECTOR ON THE UNLINK ARM, which is the other half of requirement
	// 3's edge pair. An unlink never reaches an unlink-specific client arm at
	// all: handleGraphPassthroughMutate serves create/create_batch/update/delete
	// only, so every practice unlink declines to here — and this arm's spec
	// consumes the whole schema, which is exactly why `source_hub` was accepted
	// and then dropped on this path before the gate below existed.
	//
	// IT IS SCOPED TO unlink BECAUSE IT SCOPES ENDPOINTS. upsert, update_batch
	// and bulk_update_metadata name no endpoints for it to resolve, and running
	// this gate on them would refuse a hub against a pair of empty ids. They are
	// NOT left ungated: they name TARGETS instead, and the target gate below is
	// the same rule applied to the ids they do name.
	//
	// It sits AFTER the accounting, unlike the language refusal above, because it
	// pays a READ: a call carrying a param no arm routes should hear that for
	// free rather than after a round trip.
	if a.Operation == "unlink" {
		if err := guardPracticeHubScopesEndpoints(ctx, gc, a); err != nil {
			return true, errorResult("mutate(unlink): " + err.Error())
		}
	}
	// THE HUB SELECTOR ON THE THREE TARGET ARMS THAT DECLINE HERE, which is the
	// other half of the class the update arm above closes at the passthrough.
	// update_batch, bulk_update_metadata and upsert all name EXISTING nodes by id
	// and all reach this arm, whose spec consumes the whole schema — which is
	// exactly why `source_hub` was accepted and then dropped on all three before
	// these gates existed. They are gated here rather than at the passthrough
	// because the passthrough never claims them.
	//
	// UPSERT IS A GUARD LIKE THE OTHER TWO, and this arm issues no write of its
	// own for it. An upsert whose key resolves is scoped and declines to the
	// engine exactly as it always has; an upsert whose key does NOT resolve is
	// refused naming mutate(create) as the arm that groups, because only a create
	// plan can carry the hub key and the sourced-from edge together and a create
	// re-dispatched here would REPLACE the node whole if the id appeared between
	// the guard read and the write. See practiceHubUpsertUnresolvedRefusal.
	if err := guardPracticeHubScopesTargets(ctx, gc, a); err != nil {
		return true, errorResult("mutate(" + a.Operation + "): " + err.Error())
	}
	// Param accounting keeps FIRST refusal above, so a rejected param keeps its
	// own specific message; this one claims only the calls accounting let
	// through. It refuses an instance-addressed graph whose selector is empty,
	// naming the value and the vocabulary rather than spending a server round
	// trip on "graph selector invalid". Singleton families return nil, so the
	// corpus-check guard below still runs for checks.
	if err := requireGraphInstanceSelector(a); err != nil {
		return true, errorResult(err.Error())
	}
	// The check gate's second call site, and the one that closes the upsert
	// bypass plus the two batch-update shapes — none of which reach the
	// passthrough arm upstream. It self-filters on graph=="checks", so practice
	// and every foreign graph pass straight through.
	if err := guardCorpusCheckWrite(ctx, gc, a); err != nil {
		return true, errorResult(err.Error())
	}
	// THE MEMBERSHIP AN UPSERT WOULD OTHERWISE STRIP, and the one thing on this
	// arm that is about the hub param's ABSENCE rather than its use. An upsert
	// WRITES the body's metadata whole, so a hub-less one of a grouped node used
	// to drop its `source_hub` key while the `sourced-from` edge survived. This
	// carries the stored value forward onto the body.
	//
	// IT RUNS LAST, below every refusal above it: what reaches this line is a call
	// that is going to the engine, and claiming it here re-dispatches it through
	// the same engine.Dispatch the decline would have reached, with one metadata
	// key added. See practice_hub_upsert_carry.go.
	if claimed, res := claimPracticeHubUpsertCarry(ctx, gc, a); claimed {
		return true, res
	}
	return false, kgtools.ToolResult{}
}
