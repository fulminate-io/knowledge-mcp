// SPDX-License-Identifier: Apache-2.0

// intercept_manage_rebuild.go — T-GTB3 Phase 7 client-side rebuild_bm25 /
// rebuild_hnsw manage intercepts. Each drives the T-GTB1 GraphClient.Index RPC
// (op REBUILD_BM25 / REBUILD_HNSW) over the resolved graph target(s). Durability
// is SERVER-SIDE: the Index rebuild arms auto-persist via db.SaveGraph (T-GTB1b,
// guarded name!=""), so the client issues exactly ONE rebuild RPC per resolved
// graph and NO separate persist op. The which-graph-types policy is client-side:
// a single named graph is one RPC; an empty-name multi-graph type (e.g. all
// practice graphs) resolves the graph list client-side then fans one RPC per
// graph in PARALLEL (NumCPU-bounded pool, mirroring searchCodeMultiRepo) — never
// a serial N+1 loop.

package tools

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"time"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// rebuildOp pairs an Index rebuild operation with its human label for the ack.
type rebuildOp struct {
	op    knowledgev1.IndexRequest_IndexOp
	label string // "BM25" or "HNSW"
}

// handleClientRebuildBM25 / handleClientRebuildHNSW are the two manage entry
// points; both delegate to handleClientRebuild with the matching Index op.
func handleClientRebuildBM25(ctx context.Context, deps ClientDeps, a manageArgs) kgtools.ToolResult {
	return handleClientRebuild(ctx, deps, a, rebuildOp{op: knowledgev1.IndexRequest_INDEX_OP_REBUILD_BM25, label: "BM25"})
}

func handleClientRebuildHNSW(ctx context.Context, deps ClientDeps, a manageArgs) kgtools.ToolResult {
	return handleClientRebuild(ctx, deps, a, rebuildOp{op: knowledgev1.IndexRequest_INDEX_OP_REBUILD_HNSW, label: "HNSW"})
}

// handleClientRebuild resolves the rebuild target(s) for (graph, name) and fires
// one Index RPC per resolved graph (parallel fan-out for the empty-name
// multi-graph case). The server self-persists each rebuild (T-GTB1b) — no client
// persist op.
func handleClientRebuild(ctx context.Context, deps ClientDeps, a manageArgs, ro rebuildOp) kgtools.ToolResult {
	ix, err := manageIndexer(deps)
	if err != nil {
		return errorResult(fmt.Sprintf("manage(rebuild_%s): %s", strings.ToLower(ro.label), err.Error()))
	}
	targets, single, terr := resolveRebuildTargets(ctx, deps, a)
	if terr != nil {
		return errorResult(fmt.Sprintf("manage(rebuild_%s): %s", strings.ToLower(ro.label), terr.Error()))
	}
	if len(targets) == 0 {
		return textResult(fmt.Sprintf("No %s graphs found — nothing to rebuild.", a.Graph))
	}

	start := time.Now()
	if rerr := runRebuildFanOut(ctx, ix, targets, ro.op); rerr != nil {
		return errorResult(fmt.Sprintf("manage(rebuild_%s): %s", strings.ToLower(ro.label), rerr.Error()))
	}
	elapsed := time.Since(start).Round(time.Millisecond)
	return textResult(renderRebuildAck(ro.label, a.Graph, targets, single, elapsed))
}

// rebuildTarget is one graph the rebuild fans to. graphType "" / "knowledge" is
// the knowledge root; name "" on the root is the default instance.
type rebuildTarget struct {
	graphType string
	name      string
}

// resolveRebuildTargets enumerates the rebuild target(s) from (graph, name),
// mirroring the legacy handleRebuildBM25/handleRebuildHNSW per-graph-type policy
// (now client-side). Returns (targets, single, err) where single==true for a
// one-graph rebuild (named or knowledge-root), false for the empty-name
// multi-graph fan-out.
func resolveRebuildTargets(ctx context.Context, deps ClientDeps, a manageArgs) ([]rebuildTarget, bool, error) {
	switch a.Graph {
	case "", "knowledge":
		// Knowledge root: one rebuild, empty name (server skips persist — root
		// saver flushes it, rebuildKnowledgeBM25 parity).
		return []rebuildTarget{{graphType: "knowledge", name: ""}}, true, nil
	case "practice":
		if a.Name != "" {
			return []rebuildTarget{{graphType: "practice", name: a.Name}}, true, nil
		}
		names, err := listGraphNamesOfType(ctx, deps, "practice")
		if err != nil {
			return nil, false, err
		}
		return targetsFromNames("practice", names), false, nil
	case "cloud", "cicd":
		if a.Name == "" {
			return nil, false, fmt.Errorf("%s: name is required (%s account key)", a.Graph, a.Graph)
		}
		return []rebuildTarget{{graphType: a.Graph, name: a.Name}}, true, nil
	case "code":
		// injectManageRepo injects name for the code route; a single named code
		// graph is one rebuild.
		if a.Name == "" {
			return nil, false, fmt.Errorf("code: name is required (repo) — run from inside an indexed code repo or pass name:")
		}
		return []rebuildTarget{{graphType: "code", name: a.Name}}, true, nil
	default:
		return nil, false, fmt.Errorf("unknown graph type %q — use one of: code, practice, cloud, cicd, knowledge (default)", a.Graph)
	}
}

// targetsFromNames builds rebuild targets for a multi-graph fan-out.
func targetsFromNames(graphType string, names []string) []rebuildTarget {
	out := make([]rebuildTarget, 0, len(names))
	for _, n := range names {
		out = append(out, rebuildTarget{graphType: graphType, name: n})
	}
	return out
}

// runRebuildFanOut fires one Index rebuild RPC per target. A single target runs
// inline; multiple targets fan out concurrently over a NumCPU-bounded pool
// (mirroring searchCodeMultiRepo's WaitGroup + semaphore), NOT a serial loop.
// The first error wins; the rest are allowed to finish (best-effort, like the
// server's per-graph loop, but surfaced as a failure).
func runRebuildFanOut(ctx context.Context, ix Indexer, targets []rebuildTarget, op knowledgev1.IndexRequest_IndexOp) error {
	if len(targets) == 1 {
		return execRebuildOne(ctx, ix, targets[0], op)
	}
	var (
		mu     sync.Mutex
		firstE error
		wg     sync.WaitGroup
	)
	sem := make(chan struct{}, max(1, runtime.NumCPU()))
	for _, tgt := range targets {
		wg.Add(1)
		go func(tgt rebuildTarget) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if err := execRebuildOne(ctx, ix, tgt, op); err != nil {
				mu.Lock()
				if firstE == nil {
					firstE = err
				}
				mu.Unlock()
			}
		}(tgt)
	}
	wg.Wait()
	return firstE
}

// execRebuildOne fires ONE Index rebuild RPC for a single target.
func execRebuildOne(ctx context.Context, ix Indexer, tgt rebuildTarget, op knowledgev1.IndexRequest_IndexOp) error {
	_, err := ix.Index(ctx, &knowledgev1.IndexRequest{
		Target:    rebuildGraphSelector(tgt),
		Operation: op,
	})
	if err != nil {
		return fmt.Errorf("rebuild %s/%s: %w", tgt.graphType, tgt.name, err)
	}
	return nil
}

// rebuildGraphSelector builds the GraphSelector for a rebuild target. The
// knowledge root (empty name) maps to a selector with no name so the server
// skips the name!="" persist guard. Code routes via Repo+Name; practice via
// Language; cloud/cicd via Name.
func rebuildGraphSelector(tgt rebuildTarget) *knowledgev1.GraphSelector {
	switch tgt.graphType {
	case "", "knowledge":
		return &knowledgev1.GraphSelector{Graph: tgt.graphType}
	case "code":
		return &knowledgev1.GraphSelector{Graph: "code", Repo: tgt.name, Name: tgt.name}
	case "practice":
		return &knowledgev1.GraphSelector{Graph: "practice", Language: tgt.name}
	default:
		return &knowledgev1.GraphSelector{Graph: tgt.graphType, Name: tgt.name}
	}
}

// renderRebuildAck ports the legacy handleRebuild text shapes: the knowledge
// graph, a single named graph, or the "%d <type> graph(s)" multi-graph summary.
func renderRebuildAck(label, graph string, targets []rebuildTarget, single bool, elapsed time.Duration) string {
	if !single {
		return fmt.Sprintf("%s index rebuilt for %d %s graph(s) in %v", label, len(targets), graph, elapsed)
	}
	tgt := targets[0]
	switch tgt.graphType {
	case "", "knowledge":
		return fmt.Sprintf("%s index rebuilt for knowledge graph in %v", label, elapsed)
	default:
		return fmt.Sprintf("%s index rebuilt for %s graph %q in %v", label, tgt.graphType, tgt.name, elapsed)
	}
}
