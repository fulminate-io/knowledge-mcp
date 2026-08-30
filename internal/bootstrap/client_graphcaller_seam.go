// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
)

// graphClientCaller adapts a *graphclient.GraphClient to the narrow
// tools.GraphCaller interface so intercepts can forward tail-calls
// without depending on the concrete graph-client type. The base seam is Execute —
// every intercept read/write rides the Execute carrier, type-asserting this
// concrete value UP to render.Executor / Indexer / Syncer / topologyFetcher as
// needed.
type graphClientCaller struct {
	gc *graphclient.GraphClient
}

// Execute is the base GraphCaller seam: it exposes the wrapped *GraphClient's
// engine Execute so the carrier-backed internal wire helpers (PersistBatch,
// render.FetchNode / IterEdges, the project/plan/ticket intercepts, the
// thought/linker/pipeline wire helpers) decode raw ExecuteResponse carriers. The
// helpers type-assert this same concrete value to the render.Executor seam.
func (g graphClientCaller) Execute(ctx context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	return g.gc.Execute(ctx, req)
}

// Index exposes the wrapped *GraphClient's engine Index RPC so the client-side
// manage intercepts (set_metadata_overrides / delete_branch /
// list_branches) can drive the generic lifecycle ops without reaching for the
// concrete *GraphClient. This is the narrow Index
// seam the tools.Indexer type-assert upgrades to — like Execute above, it does
// NOT widen the Call-only tools.GraphCaller interface.
func (g graphClientCaller) Index(ctx context.Context, req *knowledgev1.IndexRequest) (*knowledgev1.IndexResponse, error) {
	return g.gc.Index(ctx, req)
}

// MetadataStats exposes the wrapped *GraphClient's engine MetadataStats RPC so
// the client-side promote_metadata composer can read the per-graph
// stats + override carriers off the GraphCaller without reaching for the
// concrete *GraphClient. Like Execute/Index, this is a narrow seam a tools-side
// interface type-asserts for; it does NOT widen the Call-only GraphCaller.
func (g graphClientCaller) MetadataStats(ctx context.Context, req *knowledgev1.MetadataStatsRequest) (*knowledgev1.MetadataStatsResponse, error) {
	return g.gc.MetadataStats(ctx, req)
}

// ExportGraph exposes the wrapped *GraphClient's engine ExportGraph RPC so the
// client-side push orchestration (InterceptSync) can fetch the serialized OSS
// graph bytes off the GraphCaller without reaching for the concrete
// *GraphClient. This is the narrow Exporter seam the tools.Exporter type-assert
// upgrades to — like Execute/Index/MetadataStats/Sync, it does NOT widen the
// Call-only tools.GraphCaller interface.
func (g graphClientCaller) ExportGraph(ctx context.Context, req *knowledgev1.ExportGraphRequest) (*knowledgev1.ExportGraphResponse, error) {
	return g.gc.ExportGraph(ctx, req)
}

// OverwriteGraph is the narrow Overwriter seam (sibling of ExportGraph), driven local-only by the pull arm off LocalGraphCaller.
func (g graphClientCaller) OverwriteGraph(ctx context.Context, req *knowledgev1.OverwriteGraphRequest) (*knowledgev1.OverwriteGraphResponse, error) {
	return g.gc.OverwriteGraph(ctx, req)
}
