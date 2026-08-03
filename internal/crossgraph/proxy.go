// SPDX-License-Identifier: Apache-2.0

package crossgraph

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/projects/render"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// proxyUpsertArgs is the mutate(upsert) wire shape for a cross-graph proxy. It
// carries the deterministic id + provenance source + display fields + the proxy's
// foreign_graph/foreign_id/language metadata, lowering through compileMutateUpsert
// → the engine MUTATION_KIND_UPSERT arm. `proxy` is on that arm's type allowlist,
// which is what keeps this path working: the create-time system-managed-type
// guard rejects type=proxy outright, so an allowlist that dropped proxy would
// hard-fail every cross-graph link.
type proxyUpsertArgs struct {
	Operation   string            `json:"operation"`
	Type        string            `json:"type"`
	ID          string            `json:"id"`
	Source      string            `json:"source"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Metadata    map[string]string `json:"metadata"`
}

// UpsertForeignProxy builds the deterministic cross-graph proxy for a located
// foreign node via the package-local BuildCrossGraphProxy (the SHARED builder
// both client and server proxy paths agree on, so ids/source/metadata are
// byte-identical) and UPSERTs it into targetGraph through the Execute seam (the
// type-blind MUTATION_KIND_UPSERT arm admits type=proxy). Returns the built proxy.
//
// The located source is the wire node (*knowledgev1.Node, from render.FetchNodeIn)
// and BuildCrossGraphProxy now takes/returns the proto carrier directly — the prior
// throwaway store-wrapper bridge is gone. gt (kgtypes.GraphType) is cast to string
// for the proto graph_type field. The returned proxy is the owned *knowledgev1.Node
// product the caller reads .Id off of.
func UpsertForeignProxy(ctx context.Context, ex render.Executor, targetGraph string, gt kgtypes.GraphType, name, nodeID string, src *knowledgev1.Node) (*knowledgev1.Node, error) {
	proxy, perr := BuildCrossGraphProxy(&knowledgev1.ProxyTarget{
		GraphType: string(gt),
		Name:      name,
		NodeId:    nodeID,
	}, src)
	if perr != nil {
		return nil, fmt.Errorf("build %s proxy: %w", gt, perr)
	}
	upsertArgs, merr := json.Marshal(proxyUpsertArgs{
		Operation:   "upsert",
		Type:        proxy.GetType(),
		ID:          proxy.GetId(),
		Source:      proxy.GetSource(),
		Name:        proxy.GetSymbolName(),
		Description: proxy.GetDescription(),
		Metadata:    nodeMetadataMap(proxy),
	})
	if merr != nil {
		return nil, fmt.Errorf("marshal %s proxy upsert: %w", gt, merr)
	}
	if _, uerr := executeMutateInGraph(ctx, ex, targetGraph, upsertArgs); uerr != nil {
		return nil, fmt.Errorf("upsert %s proxy: %w", gt, uerr)
	}
	return proxy, nil
}

// nodeMetadataMap extracts every key/value pair from the proxy node's metadata as
// a plain string→string map (nil when empty).
func nodeMetadataMap(n *knowledgev1.Node) map[string]string {
	md := n.GetMetadata()
	if len(md) == 0 {
		return nil
	}
	out := make(map[string]string, len(md))
	maps.Copy(out, md)
	return out
}

// executeMutateInGraph lowers a mutate JSON arg to a MutationPlan via
// engine.Compile and runs it through the Execute seam with an explicit
// targetGraph GraphSelector. Shared by the proxy UPSERT (here) — the final LINK
// builds its plan directly (in crossgraph.go) so it can carry EdgeSpec metadata.
func executeMutateInGraph(ctx context.Context, ex render.Executor, targetGraph string, args json.RawMessage) (*knowledgev1.ExecuteResponse, error) {
	req, ok := engine.Compile("mutate", args)
	if !ok {
		return nil, fmt.Errorf("crossgraph: mutate args not reducible to a MutationPlan")
	}
	// Override the envelope target so the proxy lands in the requested graph
	// (knowledge or linkage) regardless of what the compiled args defaulted to.
	req.Target = &knowledgev1.GraphSelector{Graph: targetGraph}
	return ex.Execute(ctx, req)
}
