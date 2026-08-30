// SPDX-License-Identifier: Apache-2.0

// Package render — the fixture's gc.Call arm.
//
// fakeGcFixture answers two seams. Execute (in testutil_test.go) is the carrier
// path every wire-fetch helper takes; Call is the older JSON `query` path a few
// render surfaces still use. They are split across two files because together
// they exceed the repo's 500-line cap on a source file, which test files are not
// exempt from.

package render

import (
	"encoding/json"
	"sort"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// handleQuery dispatches the three query shapes render/ uses:
//
//   - {graph:"practice"} with no id → list practice graphs.
//   - {id:..., include_edges:true} → return {node, edges:[]}.
//   - {id:...} → return the bare wire-node JSON.
//   - {type:...} (no id) → return matching nodes from knowledge.
func (g *fakeGcFixture) handleQuery(args json.RawMessage) kgtools.ToolResult {
	var req struct {
		ID           string `json:"id"`
		IncludeEdges bool   `json:"include_edges"`
		Graph        string `json:"graph"`
		Language     string `json:"language"`
		Name         string `json:"name"`
		Type         string `json:"type"`
		Format       string `json:"format"`
	}
	_ = json.Unmarshal(args, &req)

	// List-practice-graphs path: query({graph:"practice"}).
	if req.Graph == "practice" && req.ID == "" {
		names := make([]string, 0, len(g.f.practiceGraphs))
		for n := range g.f.practiceGraphs {
			names = append(names, n)
		}
		sort.Strings(names)
		type entry struct {
			Name string `json:"name"`
		}
		entries := make([]entry, len(names))
		for i, n := range names {
			entries[i] = entry{Name: n}
		}
		body, err := json.Marshal(struct {
			Graphs []entry `json:"graphs"`
		}{Graphs: entries})
		if err != nil {
			return kgtools.ErrorResult("marshal: " + err.Error())
		}
		return kgtools.TextResult(string(body))
	}

	// Determine target graph (knowledge default, practice via lang/name).
	graphType := req.Graph
	graphName := req.Name
	if graphType == "practice" && req.Language != "" {
		graphName = req.Language
	}
	k := graphKey(graphType, graphName)

	if req.IncludeEdges && req.ID != "" {
		// query(id:, include_edges:true) → {edges:[...]}.
		return g.renderEdgesResponse(k, req.ID)
	}

	if req.ID != "" {
		nodes := g.f.nodes[k]
		if nodes == nil {
			return kgtools.ErrorResult("not found")
		}
		n, ok := nodes[req.ID]
		if !ok {
			return kgtools.ErrorResult("not found")
		}
		return g.renderNodeJSON(n)
	}

	// query(type:) — only knowledge graph; return matching nodes as
	// a flat array of {id, symbol_name, type}.
	if req.Type != "" {
		knowledge := g.f.nodes[graphKey("", "")]
		type listRow struct {
			ID         string `json:"id"`
			SymbolName string `json:"symbol_name"`
			Type       string `json:"type"`
		}
		var rows []listRow
		for _, n := range knowledge {
			if n.Type != req.Type {
				continue
			}
			rows = append(rows, listRow{ID: n.Id, SymbolName: n.SymbolName, Type: n.Type})
		}
		sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
		body, err := json.Marshal(rows)
		if err != nil {
			return kgtools.ErrorResult("marshal: " + err.Error())
		}
		return kgtools.TextResult(string(body))
	}

	return kgtools.ErrorResult("fakeGcFixture: empty query")
}

// renderNodeJSON marshals a wire node into the bare on-the-wire
// shape FetchNode parses. Fields are flat (id, type, symbol_name,
// description, summary, content, status, keywords, source) plus a
// metadata map for inline key/value pairs.
func (g *fakeGcFixture) renderNodeJSON(n *knowledgev1.Node) kgtools.ToolResult {
	type payload struct {
		ID          string            `json:"id"`
		Type        string            `json:"type"`
		SymbolName  string            `json:"symbol_name"`
		Description string            `json:"description"`
		Summary     string            `json:"summary"`
		Content     string            `json:"content"`
		Status      string            `json:"status"`
		Keywords    string            `json:"keywords"`
		Source      string            `json:"source"`
		Metadata    map[string]string `json:"metadata"`
	}
	p := payload{
		ID:          n.Id,
		Type:        n.Type,
		SymbolName:  n.SymbolName,
		Description: n.Description,
		Summary:     n.Summary,
		Content:     n.Content,
		Status:      n.Status,
		Keywords:    n.Keywords,
		Source:      n.Source,
		Metadata:    n.Metadata,
	}
	body, err := json.Marshal(p)
	if err != nil {
		return kgtools.ErrorResult("marshal: " + err.Error())
	}
	return kgtools.TextResult(string(body))
}

// renderEdgesResponse marshals the (outgoing + incoming) edges for
// nodeID in graph k into the {edges:[]} wire shape IterEdges parses.
func (g *fakeGcFixture) renderEdgesResponse(k, nodeID string) kgtools.ToolResult {
	type row struct {
		PeerID       string `json:"peer_id"`
		Relationship string `json:"relationship"`
		Direction    string `json:"direction"`
	}
	var rows []row
	bucket := g.f.edges[k]
	for i := range bucket {
		e := &bucket[i]
		if e.FromId == nodeID {
			rows = append(rows, row{
				PeerID: e.ToId, Relationship: e.Type, Direction: "outgoing",
			})
		}
		if e.ToId == nodeID {
			rows = append(rows, row{
				PeerID: e.FromId, Relationship: e.Type, Direction: "incoming",
			})
		}
	}
	body, err := json.Marshal(struct {
		Edges []row `json:"edges"`
	}{Edges: rows})
	if err != nil {
		return kgtools.ErrorResult("marshal: " + err.Error())
	}
	return kgtools.TextResult(string(body))
}
