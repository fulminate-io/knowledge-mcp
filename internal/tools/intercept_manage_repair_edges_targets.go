// SPDX-License-Identifier: Apache-2.0

// intercept_manage_repair_edges_targets.go — overlay-aware target resolution for
// manage(repair_edges). A repair covers every graph the read surface can select
// for a targeted repo: the base code graph AND every branch overlay of it, each
// as one repairEdgesTarget.
//
// repairEdgesTarget.Branch IS ALWAYS THE BARE OVERLAY NAME ("launch-fixes") AND NEVER THE COMPOSED CATALOG KEY ("agent@launch-fixes"); an empty Branch means the BASE graph.
//
// ALL THREE SOURCES OF A BRANCH VALUE NORMALIZE THROUGH bareOverlayName — the CLOUD catalog key (reported "agent@launch-fixes"), the OSS catalog key (reported BARE as "launch-fixes"), and the OPERATOR-SUPPLIED branch: argument (which may arrive in EITHER form).
// The two backends genuinely disagree about the form: the local registry's
// listOverlays sets GraphInfo.Name to the bare overlay, while a remote-backed
// catalog reports the full base@overlay key. A resolver blind to either form
// silently repairs nothing on the backend that reports the other one.
//
// A WRONG Branch IS SILENT: the server's resolveCode composes sel.Repo+"@"+sel.Branch and, WHEN THAT SCOPE ERRORS, FALLS BACK TO THE BASE GRAPH WITH NO ERROR SURFACED, so a composed value ("agent@agent@launch-fixes") makes the repair re-scan the already-clean base and print "0 fossil(s) remaining" / "Repair COMPLETE" while the overlay stays contaminated.
//
// bareOverlayName mirrors the server-side sibling deleteOverlayIfPresent
// (cmd/knowledge-server/internal/bootstrap/engine_index.go), whose doc comment
// exists for precisely this hazard and whose own normalization is the same
// strings.TrimPrefix(branch, base+"@"). This file is the package's single home
// for overlay-name resolution: appendOverlayTargets and the code-search staleness
// footer call it too, so the normalization cannot diverge across consumers.

package tools

import (
	"context"
	"fmt"
	"sort"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/paging"
)

// repairEdgesTarget is ONE graph the repair acts on: a code repo, optionally
// narrowed to one of its branch overlays. Branch is the BARE overlay name; an
// empty Branch is the base graph.
type repairEdgesTarget struct {
	Repo   string
	Branch string
}

// Label renders the operator-visible GRAPH IDENTITY of one target: "code/agent"
// for a base target, "code/agent@launch-fixes" for an overlay target. It is the
// composed spelling on purpose — it names a graph, not an argument. The bare
// spelling belongs on the branch: argument the echoed invocation renders.
func (t repairEdgesTarget) Label() string {
	if t.Branch == "" {
		return "code/" + t.Repo
	}
	return "code/" + t.Repo + "@" + t.Branch
}

// bareOverlayName strips a leading base+"@" from an overlay key and returns the
// value unchanged otherwise, so the CLOUD form ("agent@launch-fixes"), the OSS
// form ("launch-fixes"), and an operator's argument in either spelling all reduce
// to the one bare name GraphSelector.Branch requires. A key already bare is
// returned as-is: THAT IS THE OSS FORM AND IT IS THE COMMON CASE ON THAT BACKEND.
func bareOverlayName(base, key string) string {
	return strings.TrimPrefix(key, base+"@")
}

// repairEdgesResolveTargets resolves the graphs to repair.
//
//   - branch set   -> EXACTLY that one overlay of name (name is REQUIRED), and no
//     enumeration: the operator named the overlay, so a catalog read would be both
//     wasted and wrong.
//   - name set     -> the base graph PLUS every branch overlay of that repo.
//   - name empty   -> every code graph PLUS every branch overlay of each.
//
// Overlays ride the default rather than an opt-in flag because a flag would leave
// the defect (a repair that cannot reach a graph the read surface selects by
// default) reachable by the default invocation.
//
// THE OPERATOR VALUE IS NORMALIZED, NOT FORWARDED. An operator who reads the
// per-layer report line "code/agent@launch-fixes" and passes
// branch:"agent@launch-fixes" is following what this tool's own output taught
// them; forwarding that verbatim composes agent@agent@launch-fixes and buys the
// silent base fallback described in the file header.
func repairEdgesResolveTargets(ctx context.Context, deps ClientDeps, a manageArgs) ([]repairEdgesTarget, error) {
	if a.Branch != "" {
		if a.Name == "" {
			return nil, fmt.Errorf(
				"branch:%q requires name:<repo> — a branch overlay belongs to exactly one repo, "+
					"so branch with an empty name names no graph", a.Branch)
		}
		return []repairEdgesTarget{{Repo: a.Name, Branch: bareOverlayName(a.Name, a.Branch)}}, nil
	}

	bases, err := repairEdgesBaseNames(ctx, deps, a.Name)
	if err != nil {
		return nil, err
	}
	targets := make([]repairEdgesTarget, 0, len(bases))
	for _, base := range bases {
		targets = append(targets, repairEdgesTarget{Repo: base})
		keys, kerr := listOverlayKeysOfBase(ctx, deps, string(kgtypes.GraphCode), base)
		if kerr != nil {
			return nil, kerr
		}
		for _, key := range keys {
			bare := bareOverlayName(base, key)
			if bare == "" {
				continue
			}
			// A key STILL carrying an "@" after normalization did not belong to
			// this base — the enumeration is base-scoped, so this is defensive.
			if left, _, ok := atSplit(bare); ok && left != base {
				continue
			}
			targets = append(targets, repairEdgesTarget{Repo: base, Branch: bare})
		}
	}
	return targets, nil
}

// repairEdgesBaseNames resolves the BASE code graph names to repair: a named repo
// is taken as-is with no catalog read; an empty name enumerates every code graph
// through the shared RETURN_MODE_GRAPH_NAMES catalog read, sorted so the report
// order is deterministic.
func repairEdgesBaseNames(ctx context.Context, deps ClientDeps, name string) ([]string, error) {
	if name != "" {
		return []string{name}, nil
	}
	infos, err := fetchGraphNamesOfType(ctx, deps.GraphCaller(), string(kgtypes.GraphCode))
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(infos))
	for _, gi := range infos {
		if n := gi.GetName(); n != "" {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	return names, nil
}

// repairEdgesSelector builds the GraphSelector for one target by DELEGATING to
// manageGraphSelector for the graph+repo discriminant and adding the overlay
// dimension on the dedicated Branch field — the same routing clearTarget uses for
// code, and never a base@branch composed onto Repo.
func repairEdgesSelector(t repairEdgesTarget) *knowledgev1.GraphSelector {
	sel := manageGraphSelector(string(kgtypes.GraphCode), t.Repo)
	sel.Branch = t.Branch
	return sel
}

// repairEdgesFetchContainsEdges reads the CONTAINS edges incident to the file-id
// pivot set in ONE pivot-paged bulk read, scoped to the SELECTOR the caller
// resolved — so an overlay target reads the overlay's edges and a base target
// reads the base's.
//
// IT DOES NOT USE foundation.FetchEdges, AND THAT IS THE POINT. FetchEdges builds
// its own target from (graphType, name) — wire.go graphTarget →
// graphsel.GraphSelectorFor, which sets only sel.Repo — so it has no way to carry
// GraphSelector.Branch and every read through it resolves the BASE graph. Under an
// overlay target that would browse the overlay's file nodes, read the base's
// edges, find no fossils, and print "Repair COMPLETE" over a still-contaminated
// overlay: the same false green this capability exists to kill, by another route.
// Giving FetchEdges a branch would change a signature 24 callers share, so the
// scoped read lives here instead — the same shape fetchAllLogEdges
// (tools_logs_wire_fetch_edges.go) already uses for the same reason.
//
// THE READ SHAPE IS UNCHANGED from what FetchEdges issued: the same
// paging.DrainPivotEdges drain, the same EdgePivotPageSize page size, the same
// CorrelationsEdgeScanCap as both the plan Limit and the drain's truncation cap
// (the Limit is what the server enforces, the cap is what the drain uses to notice
// it was enforced — one without the other never detects truncation), and the same
// abort-on-saturation contract, because a repair computed from a silently
// truncated edge set is a partial delete presented as a complete one.
func repairEdgesFetchContainsEdges(
	ctx context.Context, gc GraphCaller, target *knowledgev1.GraphSelector, fileIDs []string,
) ([]knowledgev1.Edge, error) {
	if gc == nil || len(fileIDs) == 0 {
		return nil, nil
	}
	return paging.DrainPivotEdges(fileIDs, paging.EdgePivotPageSize, engine.CorrelationsEdgeScanCap,
		func(idPage []string, fromIDGte, fromIDLt string) ([]knowledgev1.Edge, bool, error) {
			resp, err := gc.Execute(ctx, &knowledgev1.ExecuteRequest{
				Plan: &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{
					Ids:               idPage,
					Selection:         &knowledgev1.Selection{EdgeTypes: []string{string(kgtypes.EdgeContains)}},
					ReturnMode:        knowledgev1.ReturnMode_RETURN_MODE_EDGES,
					IncludeTombstones: true,
					Limit:             int32(engine.CorrelationsEdgeScanCap),
					EdgeFromBand:      paging.EdgeFromBandOrNil(fromIDGte, fromIDLt),
				}},
				Target: target,
			})
			if err != nil {
				return nil, false, fmt.Errorf("execute bulk edges: %w", err)
			}
			page, derr := engine.DecodeEdges(resp)
			if derr != nil {
				return nil, false, fmt.Errorf("decode bulk edges: %w", derr)
			}
			return page, resp.GetTruncated(), nil
		})
}
