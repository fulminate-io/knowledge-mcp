// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// WireClient is the narrow surface the pipeline uses to issue RPCs against
// the graph server. *server.GraphClient satisfies this interface naturally
// (it has both PipelineScan and Execute); the package exposes only the two
// methods to keep the test seam small. Tests inject a fake WireClient with a
// call counter + canned response map.
//
// Execute is the engine seam: writeBatchUpdates compiles its update_batch
// into a MUTATION_KIND_UPDATE_ITEMS MutationPlan (engine.Compile) and runs it
// through Execute, so the per-item batch write rides the engine arm rather than
// the legacy mutate(update_batch) handler. PipelineScan is the dedicated
// index-gap-discovery RPC (T-GTB4) — gap discovery is NOT engine.Compile-
// reducible (it needs the server's per-axis dirty-gen state), so it rides its
// own typed EngineService.PipelineScan RPC rather than the Execute seam.
type WireClient interface {
	PipelineScan(ctx context.Context, req *knowledgev1.PipelineScanRequest) (*knowledgev1.PipelineScanResponse, error)
	Execute(ctx context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error)
}

// scanGaps issues a PipelineScan RPC for one (gt, name, axis, limit).
// Returns (items, dirty_gen, err). Empty items + nil err is the common
// case; the caller treats nil err as "scan succeeded; act on items".
// lastSeenGen is the dirty_gen the caller received from its previous
// scan against this (gt, name, axis); when non-zero AND equal to the
// server's current gen, the server short-circuits and returns empty
// items without iterating the node map.
func scanGaps(ctx context.Context, c WireClient, gt kgtypes.GraphType, name, axis string, limit int, lastSeenGen uint64) ([]*knowledgev1.PipelineScanItem, uint64, error) {
	resp, err := c.PipelineScan(ctx, &knowledgev1.PipelineScanRequest{
		GraphType:   string(gt),
		GraphName:   name,
		Axis:        axis,
		Limit:       int32(limit),
		LastSeenGen: lastSeenGen,
	})
	if err != nil {
		return nil, 0, fmt.Errorf("pipeline.rpc: PipelineScan %s/%s (%s): %w", gt, name, axis, err)
	}
	return resp.GetItems(), resp.GetDirtyGen(), nil
}

// pipelineEligibleGraphTypes is the set of graph types the LLM pipeline drains —
// mirrored verbatim from the server's pipelineEligibleGraphTypes
// (tools_pipeline_scan.go:87). The eligible-type filter is a client concern (the
// pipeline already owns which types it summarizes/embeds), so it lives here.
var pipelineEligibleGraphTypes = []kgtypes.GraphType{
	kgtypes.GraphKnowledge,
	kgtypes.GraphCode,
	kgtypes.GraphPractice,
	kgtypes.GraphCloud,
	kgtypes.GraphCICD,
	kgtypes.GraphTransformers,
}

// listLoadedGraphs returns every (gt, name) pair the pipeline should drain,
// composed CLIENT-SIDE over the generic RETURN_MODE_GRAPH_NAMES read (T-GTB6 —
// pure all-types graph enumeration, not pipeline floor). It reproduces the
// server's old handlePipelineListGraphs: seed the explicit {knowledge, default}
// entry (ListGraphsLite(GraphKnowledge) enumerates only the situation-overlay
// subdir, so the root knowledge graph would otherwise be missed), then one
// query(mode:modules) Execute per eligible type, appending each decoded
// GraphInfo.Name. Used by the refresh goroutine, which dedupes by graphKey —
// set membership, not order, is the invariant. Mirrors the D6 in-package
// engine.Compile+Execute+DecodeGraphNames template.
func listLoadedGraphs(ctx context.Context, c WireClient) ([]GraphRef, error) {
	out := []GraphRef{{GraphType: kgtypes.GraphKnowledge, GraphName: "default"}}
	for _, gt := range pipelineEligibleGraphTypes {
		rawArgs, err := json.Marshal(map[string]any{
			"graph":  string(gt),
			"mode":   "modules",
			"format": "json",
		})
		if err != nil {
			return nil, fmt.Errorf("pipeline.rpc: marshal list-graphs args (%s): %w", gt, err)
		}
		req, ok := engine.Compile("query", rawArgs)
		if !ok {
			return nil, fmt.Errorf("pipeline.rpc: list-graphs query did not compile (%s)", gt)
		}
		resp, err := c.Execute(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("pipeline.rpc: list-graphs (%s): %w", gt, err)
		}
		infos, derr := engine.DecodeGraphNames(resp)
		if derr != nil {
			return nil, fmt.Errorf("pipeline.rpc: list-graphs decode (%s): %w", gt, derr)
		}
		for _, info := range infos {
			if info.Name == "" {
				continue
			}
			out = append(out, GraphRef{GraphType: gt, GraphName: info.Name})
		}
	}
	return out, nil
}

// GraphRef is one (gt, name) pair returned by listLoadedGraphs. Public so
// the refresh goroutine can pass it back into RegisterGraph / build a diff
// set without exposing the wire-shape entry type.
type GraphRef struct {
	GraphType kgtypes.GraphType
	GraphName string
}

// updateBatchItem is one row in a mutate(update_batch) call. Mirrors the
// server-side mutateUpdateBatchItem shape exactly.
type updateBatchItem struct {
	ID           string            `json:"id"`
	Summary      *string           `json:"summary,omitempty"`
	Keywords     *string           `json:"keywords,omitempty"`
	BinaryVector []byte            `json:"binary_vector,omitempty"`
	Metadata     map[string]string `json:"metadata,omitempty"`
}

// updateBatchArgs is the wrapped arguments for mutate(operation:"update_batch").
// Graph/Repo/Account/Name route the write to the right backing DB —
// without them, mutate defaults to the knowledge graph and code-graph
// summary/embed writes silently land in the wrong place. Same routing
// shape as fetchNodes; mirrors server's graphSelectorFromArgs.
type updateBatchArgs struct {
	Operation string            `json:"operation"`
	Graph     string            `json:"graph,omitempty"`
	Repo      string            `json:"repo,omitempty"`
	Account   string            `json:"account,omitempty"`
	Name      string            `json:"name,omitempty"`
	Items     []updateBatchItem `json:"items"`
}

// writeBatchUpdates issues exactly ONE mutate(update_batch) call per
// invocation. Empty items is a no-op (no RPC fired).
//
// graphType + graphName REQUIRED for the same reason fetchNodes requires
// them — the server's mutate dispatcher needs to resolve the right
// backing DB or the write lands on the knowledge graph default and the
// pipeline silently never makes progress.
//
// Load-bearing perf criterion: this function MUST issue exactly 1 RPC per
// call. The integration test asserts the wire-client call counter equals
// the number of write groups, NOT the number of items.
func writeBatchUpdates(ctx context.Context, c WireClient, gt kgtypes.GraphType, graphName string, items []updateBatchItem) error {
	if len(items) == 0 {
		return nil
	}
	args := updateBatchArgs{Operation: "update_batch", Graph: string(gt), Items: items}
	switch gt {
	case kgtypes.GraphCode:
		args.Repo = graphName
	case kgtypes.GraphCloud, kgtypes.GraphCICD:
		args.Account = graphName
	default:
		if graphName != "" && graphName != "default" {
			args.Name = graphName
		}
	}
	// Compile the update_batch to a MUTATION_KIND_UPDATE_ITEMS MutationPlan and
	// run it through the Execute seam (the same engine.Compile+Execute shape
	// wire_persist.executeMutate uses) — NOT the legacy gc.Call(mutate) path.
	// This is the change that makes the legacy handleMutateUpdateBatch deletable
	// (T-GTB4). Still EXACTLY ONE Execute RPC per write group: N heterogeneous
	// items ride one plan in one Execute (no N+1).
	rawArgs, err := json.Marshal(args)
	if err != nil {
		return fmt.Errorf("pipeline.rpc: marshal update_batch args: %w", err)
	}
	req, ok := engine.Compile("mutate", rawArgs)
	if !ok {
		// compileMutateUpdateBatch must reduce a non-empty update_batch; a false
		// here is a wiring bug, not a routing decision.
		return fmt.Errorf("pipeline.rpc: update_batch did not compile to a MutationPlan (wiring bug)")
	}
	if _, err := c.Execute(ctx, req); err != nil {
		// Backend-tagged items reject the whole batch; log the offender
		// count so the operator sees the count without re-reading every
		// item.
		slog.Warn("pipeline.rpc: mutate(update_batch) failed",
			"items", len(items), "error", err)
		return err
	}
	return nil
}
