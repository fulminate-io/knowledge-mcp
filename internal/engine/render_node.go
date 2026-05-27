// SPDX-License-Identifier: Apache-2.0

package engine

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// render_node.go mirrors the server-side single-node renderers
// (renderGenericNode / writeGenericNodeBody / writeGenericEdgeSummary /
// renderCrossLinks in cmd/knowledge-server/tools) client-side: those live on
// the wrong side of the import boundary (engine cannot import
// cmd/knowledge-server/tools), so the field order + markdown are reproduced
// here against the verified server functions. The include_edges /
// include_cross_links DATA arrives in ExecuteResponse.edges_json /
// cross_links_json (T2.4a absorption #8); this file renders it — it does NOT
// compute edges/cross-links client-side.

// nodeEdgeInfo mirrors srvtools.NodeEdgeInfo (tools_query_edges.go:14) — the
// per-edge row the engine marshals into ExecuteResponse.edges_json. JSON tags
// match the server struct so the decode round-trips.
type nodeEdgeInfo struct {
	PeerID       string `json:"peer_id"`
	PeerName     string `json:"peer_name"`
	PeerType     string `json:"peer_type"`
	Relationship string `json:"relationship"`
	Direction    string `json:"direction"` // "outgoing" or "incoming"
}

// crossLink mirrors srvtools.CrossLink (tools_query_linkage.go:165) — the
// cross-graph link row the engine marshals into
// ExecuteResponse.cross_links_json. PeerInfo carries the proto
// *knowledgev1.ProxyTarget (the wire-crossing proxy DATA type per the CEO
// proxy-family correction). JSON tags match the server struct.
type crossLink struct {
	EdgeType  kgtypes.EdgeType         `json:"edge_type"`
	Direction string                   `json:"direction"`
	Peer      *knowledgev1.Node        `json:"peer"`
	PeerInfo  *knowledgev1.ProxyTarget `json:"peer_info,omitempty"`
}

// renderNodeResponse renders a PLAIN single-node (by_id) response. The shape
// depends on the target graph, matching the two distinct legacy paths:
//   - KNOWLEDGE graph (handleGetNode): JSON — json.MarshalIndent(node).
//   - GENERIC cross-graph (renderGenericNode): markdown "## <label> node" + body.
//
// T-GTB2 site (d): the include_edges / include_cross_links shapes do NOT reach
// here — they are intercepted in dispatchQueryByID (dispatch_byid.go) and
// composed via multi-call orchestration BEFORE the generic Compile/exec/Render
// flow. This path therefore renders the BARE node only (no edge-summary /
// cross-link sections); renderKnowledgeNode / renderGenericNode still accept the
// composed sections (the intercept passes them in directly).
//
// isKnowledge selects the shape (the dispatcher passes it from the target graph).
//
// When fields is non-empty, the bare node is rendered as the projected JSON
// object (the ProjectNodeJSON grammar — id/name/type/status/description +
// metadata[.<key>]) for BOTH graph shapes, so the tool-wide `fields` projection
// reaches the single-id read instead of full-node hydration. An empty fields
// list preserves the legacy shapes (knowledge → MarshalIndent; generic →
// markdown body).
func renderNodeResponse(resp *knowledgev1.ExecuteResponse, label, nodeID string, isKnowledge bool, fields []string) (kgtools.ToolResult, error) {
	nodes, err := decodeNodes(resp)
	if err != nil {
		return kgtools.ToolResult{}, err
	}
	if len(nodes) == 0 {
		return errorResult(nodeNotFoundMsg(nodeID, label)), nil
	}
	n := nodes[0]
	if len(fields) > 0 {
		return jsonResult(ProjectNodeJSON(n, fields)), nil
	}
	if isKnowledge {
		return renderKnowledgeNode(n, nil, nil), nil
	}
	return renderGenericNode(n, label, nil, nil), nil
}

// nodeNotFoundMsg is the shared by-id not-found message, matching the legacy
// "node %s not found in %s graph" text both the plain render path and the
// dispatchQueryByID intercept surface.
func nodeNotFoundMsg(nodeID, label string) string {
	return fmt.Sprintf("node %s not found in %s graph", nodeID, label)
}

// renderKnowledgeNode reproduces the knowledge-graph query-id JSON shape
// (handleGetNode): plain → MarshalIndent(node); include_edges →
// jsonResult(NodeWithEdges); both with the cross-link markdown appended when
// include_cross_links returned rows.
func renderKnowledgeNode(n *knowledgev1.Node, edges []nodeEdgeInfo, links []crossLink) kgtools.ToolResult {
	var body string
	if len(edges) > 0 {
		// jsonResult(NodeWithEdges{Node, Edges}) — the include_edges JSON shape.
		nwe := nodeWithEdges{Node: n, Edges: edges}
		b, err := json.Marshal(nwe)
		if err != nil {
			return errorResult("marshal node with edges: " + err.Error())
		}
		body = string(b)
	} else {
		b, err := json.MarshalIndent(n, "", "  ")
		if err != nil {
			return errorResult("marshal node: " + err.Error())
		}
		body = string(b)
	}
	if len(links) > 0 {
		body += renderCrossLinkSection(links)
	}
	return kgtools.TextResult(body)
}

// RenderGenericNode is the exported no-edges/no-links cross-graph node render
// the cmd/knowledge/internal/tools per-graph composers use for a plain by-id
// read (e.g. a linkage node). Edge/cross-link enrichment stays on the
// dispatchQueryByID path; this is the bare "## <label> node" body.
func RenderGenericNode(n *knowledgev1.Node, label string) kgtools.ToolResult {
	return renderGenericNode(n, label, nil, nil)
}

// renderGenericNode reproduces the cross-graph markdown node render
// (renderGenericNode): "## <label> node" + body + edge summary + cross-links.
func renderGenericNode(n *knowledgev1.Node, label string, edges []nodeEdgeInfo, links []crossLink) kgtools.ToolResult {
	var sb strings.Builder
	fmt.Fprintf(&sb, "## %s node\n\n", label)
	writeNodeBody(&sb, n)
	if len(edges) > 0 {
		writeEdgeSummary(&sb, edges, n.Id)
	}
	if len(links) > 0 {
		sb.WriteString(renderCrossLinkSection(links))
	}
	return kgtools.TextResult(sb.String())
}

// nodeWithEdges mirrors srvtools.NodeWithEdges (tools_query_edges.go:24) — the
// include_edges knowledge-graph JSON shape {node, edges}. The dispatchQueryByID
// intercept (dispatch_byid.go) builds the []nodeEdgeInfo client-side and passes
// it into renderKnowledgeNode, which marshals this shape.
type nodeWithEdges struct {
	Node  *knowledgev1.Node `json:"node"`
	Edges []nodeEdgeInfo    `json:"edges,omitempty"`
}

// writeNodeBody mirrors writeGenericNodeBody (tools_query_dispatch.go:210):
// bold name + ID / Type / Status / Source lines + description + summary +
// content + metadata in stable key order.
func writeNodeBody(sb *strings.Builder, n *knowledgev1.Node) {
	name := n.SymbolName
	if name == "" {
		name = n.Id
	}
	fmt.Fprintf(sb, "**%s**\n", name)
	fmt.Fprintf(sb, "ID: %s\n", n.Id)
	if n.Type != "" {
		fmt.Fprintf(sb, "Type: %s\n", n.Type)
	}
	if n.Status != "" {
		fmt.Fprintf(sb, "Status: %s\n", n.Status)
	}
	if n.Source != "" {
		fmt.Fprintf(sb, "Source: %s\n", n.Source)
	}
	if n.Description != "" {
		fmt.Fprintf(sb, "\n%s\n", n.Description)
	}
	if n.Summary != "" && n.Summary != n.Description {
		fmt.Fprintf(sb, "\n**Summary:** %s\n", n.Summary)
	}
	if n.Content != "" {
		fmt.Fprintf(sb, "\n%s\n", n.Content)
	}
	writeNodeMetadata(sb, n)
}

// writeNodeMetadata mirrors the server writeNodeMetadata (tools_query_dispatch.go:240):
// emits the Metadata map in stable key order, skipping empty values, under a
// "### Metadata" header.
func writeNodeMetadata(sb *strings.Builder, n *knowledgev1.Node) {
	if len(n.Metadata) == 0 {
		return
	}
	keys := sortedKeys(n.Metadata)
	wrote := false
	for _, k := range keys {
		v := n.Metadata[k]
		if v == "" {
			continue
		}
		if !wrote {
			sb.WriteString("\n### Metadata\n")
			wrote = true
		}
		fmt.Fprintf(sb, "- %s: %s\n", k, v)
	}
}

// writeEdgeSummary mirrors writeGenericEdgeSummary (tools_query_dispatch.go:277)
// + writeEdgeTypeCountLine (tools_query_cloud.go:149). It aggregates the
// engine-returned per-edge rows into Outgoing/Incoming edge-type counts and
// renders the "### Edges" section + the traverse hint. The section is emitted
// only when there is at least one edge (the caller already gated on len>0).
func writeEdgeSummary(sb *strings.Builder, edges []nodeEdgeInfo, nodeID string) {
	out := map[string]int{}
	in := map[string]int{}
	for _, e := range edges {
		if e.Direction == "incoming" {
			in[e.Relationship]++
		} else {
			out[e.Relationship]++
		}
	}
	sb.WriteString("\n### Edges\n\n")
	writeEdgeTypeCountLine(sb, "Outgoing", out)
	writeEdgeTypeCountLine(sb, "Incoming", in)
	fmt.Fprintf(sb, "\nUse `traverse({ start: %q })` to see per-edge detail.\n", nodeID)
}

// writeEdgeTypeCountLine mirrors the server helper (tools_query_cloud.go:149):
// "- Outgoing: BACKS×3, USES×1" or "- Outgoing: (none)" for an empty side.
func writeEdgeTypeCountLine(sb *strings.Builder, label string, counts map[string]int) {
	if len(counts) == 0 {
		fmt.Fprintf(sb, "- %s: (none)\n", label)
		return
	}
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s×%d", k, counts[k]))
	}
	fmt.Fprintf(sb, "- %s: %s\n", label, strings.Join(parts, ", "))
}

// renderCrossLinkSection mirrors renderCrossLinks (tools_query_linkage.go:172):
// the "## Cross-Graph Links" section with one --edge--> peer [graph] line per
// link. The caller already gated on len>0.
func renderCrossLinkSection(links []crossLink) string {
	var sb strings.Builder
	sb.WriteString("\n\n## Cross-Graph Links\n\n")
	for _, cl := range links {
		peerLabel := cl.Peer.SymbolName
		if peerLabel == "" {
			peerLabel = cl.Peer.Id
		}
		graphLabel := proxyTargetLabel(cl.PeerInfo)
		if cl.Direction == "outgoing" {
			fmt.Fprintf(&sb, "- --%s--> %s %s\n", string(cl.EdgeType), peerLabel, graphLabel)
		} else {
			fmt.Fprintf(&sb, "- <--%s-- %s %s\n", string(cl.EdgeType), peerLabel, graphLabel)
		}
	}
	return sb.String()
}

// proxyTargetLabel mirrors the server helper (tools_query_linkage.go:244):
// "[graphType:name]" or "[graphType]" — empty for a nil target. Reads the proto
// ProxyTarget accessors (GetGraphType/GetName).
func proxyTargetLabel(info *knowledgev1.ProxyTarget) string {
	if info == nil {
		return ""
	}
	label := fmt.Sprintf("[%s", info.GetGraphType())
	if info.GetName() != "" {
		label += ":" + info.GetName()
	}
	label += "]"
	return label
}

// sortedKeys returns map keys in stable ascending order.
func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
