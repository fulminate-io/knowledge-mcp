// SPDX-License-Identifier: Apache-2.0

package crossgraph

import (
	"context"
	"fmt"
	"time"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/projects/render"
)

// LinkRequest is the input to ResolveAndLink — the single cross-graph
// proxy-materialization entry point shared by tools' handleClientCrossGraphLink
// (knowledge target) and the linker's emitLink (linkage target). TargetGraph
// names the graph the proxies + the from→to edge land in ("knowledge" or
// "linkage"). The edge-metadata fields (Weight/Confidence/Method/Evidence/
// LastValidated) ride onto the final LINK's EdgeSpec AS-GIVEN (LastValidated is
// RFC3339; empty = unset); the interactive knowledge path leaves them zero.
type LinkRequest struct {
	From, To, Relationship string
	Graph, Language        string
	TargetGraph            string
	Weight, Confidence     float64
	Method, Evidence       string
	LastValidated          string

	// Stats is the seam the relationship is RESOLVED through, against the
	// target graph's own edge vocabulary. It is REQUIRED, not optional: without
	// it the composer cannot tell a new edge family from a mis-cased spelling
	// of an existing one, and guessing between those two is the defect this
	// resolution exists to remove. A nil Stats makes the link REFUSE loudly
	// rather than fall back to writing the caller's spelling unchecked.
	Stats engine.StatsFn
}

// ResolveAndLink is the SINGLE owner of the generic cross-graph resolve+link tail:
// enumerate the foreign graphs ONCE, resolve the FROM endpoint (knowledge raw-id
// or foreign-proxy id), materialize the TO proxy if foreign, and write the from→to
// LINK into req.TargetGraph carrying the edge metadata. Everything rides the
// client Execute seam (render.Executor / MUTATION_KIND_UPSERT + MUTATION_KIND_LINK
// with an explicit GraphSelector.Target == req.TargetGraph) — it NEVER issues a
// gc.Call("mutate"), so it never triggers the server legacy handleCrossGraphLink →
// ResolveOrProxy / proxyScanGraphTypes / RefreshProxyKeywords. The proxy
// materialization logic is exercised ONCE (this impl) regardless of caller.
//
// Returns (handled, result, err): handled=false means "fall through to legacy"
// (the FROM was unresolvable / the enumeration failed — the dangling-edge guard);
// handled=true with a non-error result means the link landed.
func ResolveAndLink(ctx context.Context, gc GraphCaller, ex render.Executor, req LinkRequest) (bool, kgtools.ToolResult, error) {
	if gc == nil || ex == nil {
		return false, kgtools.ToolResult{}, fmt.Errorf("crossgraph: graph client / Execute seam unavailable")
	}
	target := req.TargetGraph
	if target == "" {
		target = "knowledge"
	}

	// bestEffort: the LINKAGE-target path (linker + link_graph:linkage) reproduces
	// the server ResolveOrProxy's best-effort fallback — an endpoint that resolves
	// to no node anywhere is linked by its RAW id (e.g. the image linker's
	// to=<repo-name>, which is not a node). The KNOWLEDGE-target interactive path
	// instead GUARDS: an unresolvable endpoint returns handled=false so the caller
	// falls through to legacy (the dangling-edge guard — never link a raw knowledge
	// id with no node behind it).
	bestEffort := target != "knowledge"

	graphs, gerr := ListForeignGraphs(ctx, ex)
	if gerr != nil {
		// Enumeration failed → fall through to legacy (no partial proxy work).
		return false, kgtools.ToolResult{}, nil //nolint:nilerr // enumeration failure → legacy path
	}

	// Resolve FROM (knowledge raw-id or foreign-proxy id).
	fromID, ferr := resolveEndpoint(ctx, gc, ex, target, graphs, req.From, bestEffort)
	if ferr != nil {
		return true, kgtools.ErrorResult("crossgraph: resolve FROM: " + ferr.Error()), ferr
	}
	if fromID == "" {
		return false, kgtools.ToolResult{}, nil // guard: unresolvable knowledge FROM → legacy.
	}

	// Resolve TO.
	toID, terr := resolveEndpoint(ctx, gc, ex, target, graphs, req.To, bestEffort)
	if terr != nil {
		return true, kgtools.ErrorResult("crossgraph: resolve TO: " + terr.Error()), terr
	}
	if toID == "" {
		return false, kgtools.ToolResult{}, nil // guard: unresolvable knowledge TO → legacy.
	}

	// Resolve the relationship ONCE, against the TARGET graph's own edge
	// vocabulary, after the endpoints resolve and before the write. Assigning
	// it back onto req is what makes the write and the confirmation below share
	// a single spelling — a second resolution, or a render off the caller's
	// original spelling, could name something other than what was stored.
	//
	// This is the DECLARATION path: an unknown spelling is admitted as a new
	// edge family, which is what keeps a fresh linkage graph able to receive
	// its first BUILDS.
	res, rerr := engine.ResolveEdgeTypeDeclaration(ctx, req.Stats,
		&knowledgev1.GraphSelector{Graph: target}, []string{req.Relationship})
	if rerr != nil {
		return true, kgtools.ErrorResult("crossgraph: resolve relationship: " + rerr.Error()), rerr
	}
	req.Relationship = res.Types[0]

	if lerr := linkInGraph(ctx, ex, target, fromID, toID, req); lerr != nil {
		return true, kgtools.ErrorResult("crossgraph: link to foreign proxy failed: " + lerr.Error()), lerr
	}
	return true, kgtools.TextResult(fmt.Sprintf("Linked %s -[%s]-> %s in %s", fromID, req.Relationship, toID, target)), nil
}

// resolveEndpoint resolves one link endpoint to the id it should reference in the
// target graph — the client-composed mirror of the server's ResolveOrProxy:
//
//  1. id in knowledge → return id (the raw knowledge id is the endpoint).
//  2. id in a foreign graph → build + UPSERT its deterministic proxy into
//     targetGraph and return the proxy id (so the edge never dangles off a raw
//     foreign id).
//  3. id found nowhere:
//     - bestEffort (linkage path) → return id AS-IS (server ResolveOrProxy
//     best-effort: a non-node id like a repo-name links by its raw id).
//     - guard (knowledge path) → return "" so the caller falls through to legacy.
//
// A proxy build/UPSERT failure surfaces as a non-nil error.
func resolveEndpoint(ctx context.Context, gc GraphCaller, ex render.Executor, targetGraph string, graphs []ForeignGraph, id string, bestEffort bool) (string, error) {
	if known, ferr := render.FetchNodeIn(ctx, gc, id, "knowledge", ""); ferr == nil && known != nil {
		return id, nil
	}
	gt, name, node, found := LocateForeignNode(ctx, gc, graphs, id)
	if found {
		proxy, uerr := UpsertForeignProxy(ctx, ex, targetGraph, gt, name, id, node)
		if uerr != nil {
			return "", uerr
		}
		return proxy.Id, nil
	}
	if bestEffort {
		return id, nil // server ResolveOrProxy best-effort: link by raw id.
	}
	return "", nil // guard → legacy.
}

// linkInGraph writes the from→to LINK into targetGraph carrying the EdgeSpec
// edge-metadata AS-GIVEN. The plan is built DIRECTLY (not via engine.Compile) so
// it can set both the explicit Target and the metadata carriers. The edge type
// arrives already RESOLVED against the target graph's own edge vocabulary —
// ResolveAndLink does that once, above, and assigns it back onto req — so this
// writes req.Relationship verbatim. LastValidated (RFC3339) is parsed to unix
// nanos; an empty value is unset (0).
func linkInGraph(ctx context.Context, ex render.Executor, targetGraph, fromID, toID string, req LinkRequest) error {
	spec := &knowledgev1.EdgeSpec{
		Relationship: req.Relationship,
		ToId:         toID,
		Forward:      true,
		Weight:       req.Weight,
		Confidence:   req.Confidence,
		Method:       req.Method,
		Evidence:     req.Evidence,
	}
	if req.LastValidated != "" {
		t, perr := time.Parse(time.RFC3339, req.LastValidated)
		if perr != nil {
			return fmt.Errorf("parse last_validated %q: %w", req.LastValidated, perr)
		}
		spec.LastValidated = t.UnixNano()
	}
	plan := &knowledgev1.MutationPlan{
		Kind:      knowledgev1.MutationPlan_MUTATION_KIND_LINK,
		Selection: &knowledgev1.Selection{Ids: []string{fromID}},
		EdgeSpec:  spec,
	}
	_, err := ex.Execute(ctx, &knowledgev1.ExecuteRequest{
		Plan:   &knowledgev1.ExecuteRequest_Mutation{Mutation: plan},
		Target: &knowledgev1.GraphSelector{Graph: targetGraph},
	})
	return err
}
