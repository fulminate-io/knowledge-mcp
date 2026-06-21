// SPDX-License-Identifier: Apache-2.0

package recipe

import (
	"context"
	"fmt"
	"sync"
	"time"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/collector"
	"github.com/fulminate-io/knowledge-mcp/internal/collectorwire"
	"github.com/fulminate-io/knowledge-mcp/internal/graphsel"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

// RunRecipe is the single client-side entry point for a recipe transform — the
// collapsed replacement for the former server-side transform orchestration +
// Interpret + registry. There is NO transform interface, NO registry, and NO
// init(): the `collect` tool calls this plain exported function directly.
//
// The whole run is bounded: 2 source-read RPCs (loadSourceView) + (0 or 1 force
// read + 1 delete) + (0 or 1 WriteResult). No per-row RPC.
//
// expectedSourceType, when non-empty, is validated against the recipe's
// source_graph_type metadata: a mismatch (e.g. a `collect type=pdf` against a
// web-source recipe) returns a typed error naming BOTH types before any source
// read. Pass "" to skip the check (callers that already know the type matches).
//
// Sequence (mirrors Transform, re-seamed onto the client wire):
//  1. ParseSourceManifest(opts.SourceManifest) → sourceSlug, recipeName.
//  2. loadRecipeByName → the recipe node; parseWithCache → the AST.
//  3. read source_graph_type / target_graph_type / target_name from the recipe
//     node metadata → TargetSpec; validate expectedSourceType.
//  4. loadSourceView over (source_graph_type, sourceGraphName).
//  5. if opts.Force: forceDeleteBySource over the target BEFORE interpreting.
//  6. Interpret → in-memory Result.
//  7. if !opts.DryRun: writeResult ships the Result through the Sink.
//
// Stats (including a DryRun's projected counts) are returned in the Result for
// the caller (the collect tool) to surface; unlike the server, RunRecipe does
// NOT write last_dry_run_stats back to the recipe node.
func RunRecipe(
	ctx context.Context,
	caller foundation.GraphCaller,
	sink collector.Sink,
	sourceGraphName string,
	expectedSourceType kgtypes.GraphType,
	opts Options,
) (*Result, error) {
	start := time.Now()
	sourceSlug, recipeName, err := ParseSourceManifest(opts.SourceManifest)
	if err != nil {
		return nil, fmt.Errorf("recipe: parse manifest: %w", err)
	}
	recipeNode, err := loadRecipeByName(ctx, caller, recipeName)
	if err != nil {
		return nil, err
	}
	ast, err := parseWithCache(recipeNode)
	if err != nil {
		return nil, err
	}

	sourceGTStr := kgtypes.Value(recipeNode, "source_graph_type")
	targetGTStr := kgtypes.Value(recipeNode, "target_graph_type")
	targetName := kgtypes.Value(recipeNode, "target_name")
	if sourceGTStr == "" || targetGTStr == "" || targetName == "" {
		return nil, fmt.Errorf(
			"recipe %q: missing required metadata (source_graph_type=%q, target_graph_type=%q, target_name=%q)",
			recipeName, sourceGTStr, targetGTStr, targetName,
		)
	}
	if expectedSourceType != "" && string(expectedSourceType) != sourceGTStr {
		return nil, fmt.Errorf(
			"recipe %q: collect type %q does not match the recipe's source_graph_type %q",
			recipeName, expectedSourceType, sourceGTStr,
		)
	}
	target := TargetSpec{GraphType: kgtypes.GraphType(targetGTStr), Name: targetName}

	sv, err := loadSourceView(ctx, caller, kgtypes.GraphType(sourceGTStr), sourceGraphName)
	if err != nil {
		return nil, err
	}

	var forceDeleted int
	if opts.Force {
		forceDeleted, err = forceDeleteBySource(ctx, caller, target, sourceSlug, collectEmitTypes(ast))
		if err != nil {
			return nil, fmt.Errorf("recipe: force cleanup: %w", err)
		}
	}

	result, err := Interpret(ctx, ast, sv, target, sourceSlug, opts)
	if err != nil {
		return result, err
	}
	result.Stats.ForceDeleted = forceDeleted
	result.Stats.ElapsedMillis = time.Since(start).Milliseconds()

	if !opts.DryRun {
		if werr := writeResult(ctx, sink, target, result); werr != nil {
			return result, werr
		}
	}
	return result, nil
}

// RecipesBucketName is the GraphTransformers graph name that holds every recipe
// node. v1 keeps a single flat bucket keyed by recipe SymbolName; callers must
// use this constant rather than hard-coding the string so future partitioning
// lands in one place.
const RecipesBucketName = "recipes"

// astCache memoizes parsed recipes across RunRecipe invocations. The cache key
// is (recipeNodeID, UpdatedAt) so edits to a recipe body invalidate the cached
// AST on the next run automatically.
var astCache sync.Map // astCacheKey → *Recipe

type astCacheKey struct {
	id        string
	updatedAt int64
}

// loadRecipeByName fetches every "recipe" node in the GraphTransformers/recipes
// bucket over the wire (one Execute via FetchNodesByType) and returns the first
// whose SymbolName matches name. v1 keeps recipes in a single flat bucket; name
// collision is the user's responsibility. A miss returns a clear not-found error
// listing the available recipe names.
func loadRecipeByName(ctx context.Context, caller foundation.GraphCaller, name string) (*knowledgev1.Node, error) {
	nodes, err := foundation.FetchNodesByType(ctx, caller, kgtypes.GraphTransformers, RecipesBucketName, kgtypes.NodeType("recipe"))
	if err != nil {
		return nil, fmt.Errorf("recipe: list recipes: %w", err)
	}
	for _, n := range nodes {
		if n != nil && n.SymbolName == name {
			return n, nil
		}
	}
	var available []string
	for _, n := range nodes {
		if n != nil {
			available = append(available, n.SymbolName)
		}
	}
	return nil, fmt.Errorf("recipe %q not found (available: %v)", name, available)
}

// parseWithCache returns a cached *Recipe for node, parsing on miss. Cache keyed
// by (ID, UpdatedAt) so any change to the recipe body (which bumps UpdatedAt)
// forces a re-parse automatically.
func parseWithCache(node *knowledgev1.Node) (*Recipe, error) {
	key := astCacheKey{id: node.Id, updatedAt: node.UpdatedAt}
	if v, ok := astCache.Load(key); ok {
		ast, ok := v.(*Recipe)
		if !ok {
			return nil, fmt.Errorf("parseWithCache: cache entry for %q has unexpected type %T", node.SymbolName, v)
		}
		return ast, nil
	}
	ast, err := Parse([]byte(node.Content))
	if err != nil {
		return nil, fmt.Errorf("recipe %q parse: %w", node.SymbolName, err)
	}
	astCache.Store(key, ast)
	return ast, nil
}

// writeResult ships an interpreted Result to the target practice graph through
// the collector Sink — the shipLogsResult analog. It builds a single
// collectorwire.CollectResult{GraphType: practice, GraphName: <target_name>}
// carrying the emitted nodes plus the structural (link-rule) edges AND the
// translated-from lineage edges, then makes one WriteResult call keyed by the
// "recipe" collector name. The server reassembles the chunked nodes + proto
// edges from the standard collect wire.
//
// DryRun is handled by the CALLER (RunRecipe skips writeResult entirely on a
// dry run); writeResult always writes when invoked.
func writeResult(ctx context.Context, sink collector.Sink, target TargetSpec, res *Result) error {
	if sink == nil {
		return fmt.Errorf("recipe: writeResult: sink unavailable")
	}
	// Structure edges (link rules) first, then the translated-from lineage
	// edges — both ride the CollectResult.Edges carrier as kgwire.BatchEdge.
	edges := make([]kgwire.BatchEdge, 0, len(res.Edges)+len(res.Lineage))
	edges = append(edges, res.Edges...)
	edges = append(edges, res.Lineage...)

	wireResult := &collectorwire.CollectResult{
		GraphType: target.GraphType,
		GraphName: target.Name,
		Nodes:     res.Nodes,
		Edges:     edges,
	}
	if err := sink.WriteResult(ctx, "recipe", wireResult); err != nil {
		return fmt.Errorf("recipe: write target %s/%s: %w", target.GraphType, target.Name, err)
	}
	return nil
}

// forceDeleteBySource removes every node in the target graph whose outgoing
// translated-from edge carries the given sourceSlug, scoped to emitTypes. This
// is the client-side counterpart of the server's transformer.ForceDeleteBySource,
// re-targeted off the in-process store onto the wire:
//
//  1. FetchNodesByType (one Execute per emit type) lists the candidate target
//     nodes — only the types the recipe could have emitted are examined, never
//     unrelated nodes in the same target graph.
//  2. FetchEdges (one Execute) reads the candidates' translated-from edges as
//     []knowledgev1.Edge. A node is doomed when ANY of its OUTGOING
//     translated-from edges carries Evidence JSON whose source == sourceSlug.
//     NOTE: this READ edge (knowledgev1.Edge, with .Evidence) is distinct from
//     the kgwire.BatchEdge emission carrier — they are not unified.
//  3. ONE MutationPlan{DELETE, Selection{Ids: doomed}, HardDelete: true} removes
//     them in a single Execute — not N per-id deletes. HardDelete is REQUIRED:
//     DELETE defaults to a soft tombstone, and a tombstone would collide with
//     the StableID the run is about to re-emit on a Force re-run.
//
// Returns the number of nodes deleted. Force on sourceSlug=A never touches
// emissions from sourceSlug=B, even into the same target graph (the Evidence
// source match is exact).
func forceDeleteBySource(
	ctx context.Context,
	caller foundation.GraphCaller,
	target TargetSpec,
	sourceSlug string,
	emitTypes []string,
) (int, error) {
	if sourceSlug == "" {
		return 0, nil
	}
	doomed, err := collectDoomedIDs(ctx, caller, target, sourceSlug, emitTypes)
	if err != nil {
		return 0, err
	}
	if len(doomed) == 0 {
		return 0, nil
	}
	_, err = caller.Execute(ctx, &knowledgev1.ExecuteRequest{
		Plan: &knowledgev1.ExecuteRequest_Mutation{Mutation: &knowledgev1.MutationPlan{
			Kind:       knowledgev1.MutationPlan_MUTATION_KIND_DELETE,
			Selection:  &knowledgev1.Selection{Ids: doomed},
			HardDelete: true,
		}},
		Target: graphsel.GraphSelectorFor(target.GraphType, target.Name, false),
	})
	if err != nil {
		return 0, fmt.Errorf("recipe: force-delete %d nodes from %s/%s: %w", len(doomed), target.GraphType, target.Name, err)
	}
	return len(doomed), nil
}

// collectDoomedIDs lists the emit-type nodes in the target graph and returns the
// IDs of every node with at least one OUTGOING translated-from edge whose
// Evidence source matches sourceSlug. emitTypes scopes the node listing; an
// empty slice examines no nodes (a recipe with no emits cannot have written
// anything to force-delete).
func collectDoomedIDs(
	ctx context.Context,
	caller foundation.GraphCaller,
	target TargetSpec,
	sourceSlug string,
	emitTypes []string,
) ([]string, error) {
	var ids []string
	for _, nt := range emitTypes {
		nodes, err := foundation.FetchNodesByType(ctx, caller, target.GraphType, target.Name, kgtypes.NodeType(nt))
		if err != nil {
			return nil, fmt.Errorf("recipe: list target %q nodes: %w", nt, err)
		}
		for _, n := range nodes {
			if n != nil {
				ids = append(ids, n.Id)
			}
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	edges, err := foundation.FetchEdges(ctx, caller, target.GraphType, target.Name, ids, []kgtypes.EdgeType{kgtypes.EdgeTranslatedFrom})
	if err != nil {
		return nil, fmt.Errorf("recipe: read target translated-from edges: %w", err)
	}
	// A node is doomed when it has an OUTGOING translated-from edge (FromId ==
	// the node) whose Evidence source matches the slug. Dedupe so multiple
	// matching edges on one node still produce a single delete.
	doomedSet := make(map[string]struct{})
	for i := range edges {
		e := &edges[i]
		if SourceFromEvidence(e.Evidence) == sourceSlug {
			doomedSet[e.FromId] = struct{}{}
		}
	}
	// Preserve the node listing order for deterministic delete payloads.
	var doomed []string
	for _, id := range ids {
		if _, ok := doomedSet[id]; ok {
			doomed = append(doomed, id)
			delete(doomedSet, id)
		}
	}
	return doomed, nil
}
