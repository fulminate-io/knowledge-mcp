// SPDX-License-Identifier: Apache-2.0

package tools

// intercept_mutate_dispatch.go holds the arm-selection helpers InterceptMutate
// and handleInterceptMutateUpdate delegate to. Each is a branch of the dispatch
// decision tree lifted verbatim out of its caller so both callers stay inside
// the function-length and complexity gates; the routing order is unchanged.

import (
	"context"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// handleGraphPassthroughMutate claims a practice/transformers CRUD mutation that
// carries no link_graph. With no link_graph these are engine-reducible, lowering
// to a Target-routed MutationPlan (Target.Graph == practice/transformers), so
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
	if a.Graph != "practice" && a.Graph != "transformers" {
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
	ex, eerr := persistExecutor(gc)
	if eerr != nil {
		return true, errorResult("mutate(" + a.Operation + "): " + eerr.Error())
	}
	res, err := engine.Dispatch(ctx, ex.Execute, "mutate", params.Arguments)
	if err != nil {
		return true, errorResult("mutate(" + a.Operation + "): " + err.Error())
	}
	return true, res
}

// dispatchClientMutateCreate claims create=finding/research/rule. Other create
// types (criterion, generic knowledge nodes, etc.) fall through —
// InterceptAddCriterion fires earlier in the chain for criterion, and a generic
// create flows on to the engine create arm. Split out of InterceptMutate to keep
// its decision tree inside the complexity gate.
func dispatchClientMutateCreate(ctx context.Context, deps ClientDeps, a mutateArgs) (bool, kgtools.ToolResult) {
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
	if err := accountMutateParams(armCreateFallthrough, a); err != nil {
		return true, errorResult(err.Error())
	}
	return false, kgtools.ToolResult{}
}

// accountDefaultBucketMutate runs param accounting for the five operations that
// reach the dispatch default bucket and decline to an engine arm. Any other
// operation returns nil — see the default-branch comment for why gating them
// would be wrong.
func accountDefaultBucketMutate(a mutateArgs) error {
	switch a.Operation {
	case "create_batch":
		return accountMutateParams(armCreateBatch, a)
	case "upsert":
		return accountMutateParams(armUpsert, a)
	case "update_batch":
		return accountMutateParams(armUpdateBatchItems, a)
	case "bulk_update_metadata":
		return accountMutateParams(armBulkUpdateMetadata, a)
	case "unlink":
		return accountMutateParams(armUnlink, a)
	}
	return nil
}

// handleLocalOnlyMutateUpdate routes a SINGLE-id update on a node with no
// external backing, in precedence order: status=completed container rollup →
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
	// Claim closure-rollup for local-only container updates (status=completed on
	// project/ticket/plan/phase/step). The client owns the cascade. It writes
	// status only to descendants that are themselves one of those
	// five container types; every other type is held — the cascade never writes
	// the status of a node that records evidence — and named in the response
	// instead. The
	// cascadeToDescendants() term honors expand_to_descendants: when the caller
	// sets it false, the rollup arm declines and the explicit-false update falls
	// through to the typed-router/engine single-node path below (which writes
	// status=completed to the NAMED container only — a real single-node update,
	// not a no-op). Absent/true preserves the cascade.
	if a.Status == kgtypes.StatusCompleted && node != nil && isClientRollupContainer(kgtypes.NodeType(node.Type)) && a.cascadeToDescendants() {
		if err := accountMutateParams(armUpdateRollup, a); err != nil {
			return true, errorResult(err.Error())
		}
		return true, handleClientUpdateStatusRollup(ctx, gc, a, node)
	}
	// Per-type first-class-param routing: a typed knowledge node update
	// (criterion/rule/finding/...) routes its create-time params
	// (command/criterion_type/scope/enforcement/evidence/source) into metadata,
	// re-derives the summary, re-stamps a criterion's name, and loudly rejects
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
