// SPDX-License-Identifier: Apache-2.0

package engine

import (
	"fmt"
	"sort"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// render_stats.go renders the read-only Stats / MetadataStats carriers
// client-side. Post-T5.5 the GraphStats / MetadataStats / OverrideConfig
// carriers are TYPED proto messages consumed DIRECTLY — callers read
// resp.GetGraphStats() / resp.GetMetadataStats() / resp.GetOverrideConfig() and
// pass the proto pointers straight into the renderers (no store-side decode step;
// the prior graphStatsFromProto / DecodeMetadataStats / DecodeOverrideConfig
// decoders are gone with the pkg/store dependency). Topology findings are no
// longer decoded here: the analyzers now run client-side and produce
// foundation.Finding directly (the Topology RPC carrier + its decode are gone),
// which also breaks the engine<->topology import cycle.

// RenderStatsBreakdown renders a consistent Markdown stats body for the given
// proto GraphStats — a direct port of the server formatStatsBreakdown
// (cmd/knowledge-server/tools/tools_query_stats.go). The caller passes the typed
// resp.GetGraphStats() pointer straight in; this emits the scalar block followed
// by the sorted-by-count-desc Nodes-by-Type / Edges-by-Type tables. Shared across
// every graph type's stats mode (knowledge, cloud, cicd, practice, linkage, logs,
// code). The proto getters are nil-safe (nil stats → all-zero scalar block).
func RenderStatsBreakdown(stats *knowledgev1.GraphStats) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Nodes: %d\n", stats.GetNodeCount())
	fmt.Fprintf(&sb, "Edges: %d\n", stats.GetEdgeCount())
	fmt.Fprintf(&sb, "Vectors: %d\n", stats.GetVectorCount())
	fmt.Fprintf(&sb, "Binary vectors: %d\n", stats.GetBinaryVectorCount())
	fmt.Fprintf(&sb, "Text documents: %d\n", stats.GetTextDocCount())
	fmt.Fprintf(&sb, "BM25: %v\n", stats.GetHasBm25())
	fmt.Fprintf(&sb, "HNSW: %v\n", stats.GetHasHnsw())

	writeTypeBreakdown(&sb, "Nodes", stats.GetNodesByType())
	writeTypeBreakdown(&sb, "Edges", stats.GetEdgesByType())
	return sb.String()
}

// writeTypeBreakdown renders "### <Nodes|Edges> by Type" followed by the
// sorted-by-count-desc table over the proto map<string,int64> by-type carrier.
// No-op when the map is empty so empty graphs don't produce an empty section
// header. Port of the server writeNodeTypeBreakdown / writeEdgeTypeBreakdown
// (unified — the two former helpers differed only in the section label and the
// open-vocabulary key type, both now a plain string proto-map key).
func writeTypeBreakdown(sb *strings.Builder, kind string, counts map[string]int64) {
	if len(counts) == 0 {
		return
	}
	type row struct {
		name  string
		count int64
	}
	rows := make([]row, 0, len(counts))
	for name, n := range counts {
		rows = append(rows, row{name: name, count: n})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].count != rows[j].count {
			return rows[i].count > rows[j].count
		}
		return rows[i].name < rows[j].name
	})
	fmt.Fprintf(sb, "\n### %s by Type\n", kind)
	for _, r := range rows {
		fmt.Fprintf(sb, "- %s: %d\n", r.name, r.count)
	}
}

// RenderSampleNames writes a "### Sample names by type" section listing the
// supplied per-type node samples, ordered by descending node-type count to
// mirror the breakdown. Unlike the server appendSampleNames (which did its own
// db.Query), this renderer is PURE: the composer supplies the already-fetched
// samples map (bounded by node-type count via a Match(type).Limit(N) per type,
// dozens of fetches, not N+1 over nodes). No-op when stats has no node types.
// Each sample falls back to the node ID when SymbolName is empty. Ranges the
// proto map<string,int64> by-type carrier, casting the key to kgtypes.NodeType
// for the samples lookup.
func RenderSampleNames(sb *strings.Builder, stats *knowledgev1.GraphStats, samples map[kgtypes.NodeType][]*knowledgev1.Node) {
	nodesByType := stats.GetNodesByType()
	if len(nodesByType) == 0 {
		return
	}
	type row struct {
		nt    kgtypes.NodeType
		count int64
	}
	rows := make([]row, 0, len(nodesByType))
	for nt, n := range nodesByType {
		rows = append(rows, row{nt: kgtypes.NodeType(nt), count: n})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].count != rows[j].count {
			return rows[i].count > rows[j].count
		}
		return rows[i].nt < rows[j].nt
	})
	sb.WriteString("\n### Sample names by type\n")
	for _, r := range rows {
		nodes := samples[r.nt]
		if len(nodes) == 0 {
			continue
		}
		names := make([]string, 0, len(nodes))
		for _, n := range nodes {
			name := n.SymbolName
			if name == "" {
				name = n.Id
			}
			names = append(names, name)
		}
		fmt.Fprintf(sb, "- %s: %s\n", r.nt, strings.Join(names, ", "))
	}
}
