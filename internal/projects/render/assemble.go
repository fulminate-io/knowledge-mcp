// SPDX-License-Identifier: Apache-2.0

package render

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/paging"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// Args is the typed shape Handle parses. Mirrors the server-side
// assembleArgs at cmd/knowledge-server/tools/tools_assemble.go:41.
// new_run uses bool here instead of the server's flexBool because
// the client intercept doesn't need to accept the legacy
// string/bool flexibility — JSON callers always send bool.
type Args struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Type       string `json:"type"`
	NewRun     bool   `json:"new_run"`
	RunSession string `json:"run_session"`
	Format     string `json:"format"`
}

// Handle is the top-level entry point for the client-side assemble
// intercept. Parses the args, resolves the node (by ID or
// type+name), and dispatches to the appropriate composite assembler
// based on node.Type.
//
// Ported from cmd/knowledge-server/tools/tools_assemble.go:50 with
// these changes per the relocation plan:
//   - No-args recovery branch REMOVED. The server's assembleTools
//     description still mentioned no-args recovery, but the body
//     never reachable in the client-side intercept (no snapshot
//     store-side access). Handle errors when id + type + name are
//     all empty.
//   - resolveAssembleNode walks knowledge first, then every loaded
//     practice graph (via the wire `query({graph:"practice"})` list)
//     instead of touching store.Store() directly.
//   - The switch routes 9 NodeTypes through 8 case arms (Agent + Skill
//     share one case): NodePlan → assemblePlan, NodeProject →
//     assembleProjectContainer, NodeTicket → assembleTicket,
//     NodeTestPlan → assembleTestPlan, NodeResearch → assembleResearch,
//     (NodeAgent | NodeSkill) → assembleInstruction, NodeDecision →
//     assembleDecision, NodePattern → assemblePatternIn, default →
//     assembleFallback. Mirrors the server-side dispatch exactly.
func Handle(ctx context.Context, gc GraphCaller, args json.RawMessage) kgtools.ToolResult {
	var a Args
	if err := json.Unmarshal(args, &a); err != nil {
		return kgtools.ErrorResult("invalid arguments: " + err.Error())
	}

	if a.ID == "" && a.Type == "" && a.Name == "" {
		return kgtools.ErrorResult("provide id, or both type and name for lookup")
	}

	// Resolve node: by ID or by type+name lookup.
	nodeID := a.ID
	if nodeID == "" {
		resolved, res := resolveAssembleByName(ctx, gc, a.Type, a.Name)
		if res != nil {
			return *res
		}
		nodeID = resolved
	}

	node, graphType, graphName, err := resolveAssembleNode(ctx, gc, nodeID)
	if err != nil {
		return kgtools.ErrorResult(fmt.Sprintf("no node with id %q in knowledge or any practice graph", nodeID))
	}

	if a.Format == "json" {
		return assembleJSON(ctx, gc, node)
	}

	switch kgtypes.NodeType(node.Type) {
	case kgtypes.NodePlan:
		return assemblePlan(ctx, gc, node)
	case kgtypes.NodeProject:
		return assembleProjectContainer(ctx, gc, node)
	case kgtypes.NodeTicket:
		return assembleTicket(ctx, gc, node)
	case kgtypes.NodeTestPlan:
		return assembleTestPlan(ctx, gc, node, a.NewRun, a.RunSession)
	case kgtypes.NodeResearch:
		return assembleResearch(ctx, gc, node)
	case kgtypes.NodeAgent, kgtypes.NodeSkill:
		return assembleInstruction(ctx, gc, node)
	case kgtypes.NodeDecision:
		return assembleDecision(ctx, gc, node)
	case kgtypes.NodePattern:
		return assemblePatternIn(ctx, gc, node, graphType, graphName)
	default:
		return assembleFallback(ctx, gc, node)
	}
}

// resolveAssembleByName performs a type+name lookup against the
// knowledge graph via the `query` MCP tool. Returns the matched
// node ID. Returns a non-nil *kgtools.ToolResult to short-circuit
// when the inputs are invalid or no match is found.
//
// Ported from cmd/knowledge-server/tools/tools_assemble.go:110 with
// the store.Match(NodeType) query replaced by a wire call to
// query({type:<typ>}). Multiple matches: the first is used; a warn
// is logged.
//
// THREE LEGS, each load-bearing for a different reason:
//
//   - The browse DRAINS bounded id-keyset pages. It used to be one browse
//     carrying no limit at all, which the compile path caps at
//     browseDefaultLimit — so assemble(type, name) searched only the first
//     page of that type before reporting the node nonexistent.
//   - A symbol_name EQ field_predicate narrows server-side. This is a PERF
//     requirement, not decoration: without it every by-name assemble drains
//     the entire node set of the type. The predicate rides the singular
//     type-browse arm, the only compile arm that also threads after_id.
//   - The client-side SymbolName scan below STAYS. It is the CORRECTNESS
//     leg: an old or predicate-blind server that ignores field_predicates
//     returns an arbitrary page, and the scan is what stops a wrong-named
//     node being resolved.
func resolveAssembleByName(ctx context.Context, gc GraphCaller, typ, name string) (string, *kgtools.ToolResult) {
	if typ == "" || name == "" {
		res := kgtools.ErrorResult("provide id, or both type and name for lookup")
		return "", &res
	}
	ex, eerr := asExecutor(gc)
	if eerr != nil {
		res := kgtools.ErrorResult("resolve by name: " + eerr.Error())
		return "", &res
	}
	candidates, derr := paging.DrainKeysetPages(func(afterID string) ([]*knowledgev1.Node, error) {
		raw, merr := json.Marshal(map[string]any{
			"type": typ,
			"field_predicates": []map[string]string{
				{"field": "symbol_name", "op": "eq", "value": name},
			},
			"limit": paging.BrowsePageSize,
			// SET on every page including the first, where the value is empty:
			// presence, not emptiness, is what selects the keyset browse.
			"after_id":   afterID,
			"skip_total": true,
		})
		if merr != nil {
			return nil, fmt.Errorf("marshal: %w", merr)
		}
		req, ok := engine.Compile("query", raw)
		if !ok {
			return nil, fmt.Errorf("query not reducible to an ExecuteRequest")
		}
		resp, rerr := ex.Execute(ctx, req)
		if rerr != nil {
			return nil, rerr
		}
		// query(type:) compiles to a type-browse whose typed Nodes carrier
		// (engine.DecodeNodes) carries the matched wire node payloads.
		return engine.DecodeNodes(resp)
	}, paging.BrowsePageSize)
	if derr != nil {
		res := kgtools.ErrorResult("resolve by name: " + derr.Error())
		return "", &res
	}

	var foundID string
	matches := 0
	for _, c := range candidates {
		if c.SymbolName != name {
			continue
		}
		if matches == 0 {
			foundID = c.Id
		}
		matches++
	}
	if foundID == "" {
		res := kgtools.ErrorResult(fmt.Sprintf("no %s node named %q found", typ, name))
		return "", &res
	}
	if matches > 1 {
		slog.Warn("render.Handle: multiple matches; using first",
			"type", typ, "name", name, "matches", matches, "picked_id", foundID)
	}
	return foundID, nil
}

// resolveAssembleNode looks up a node by ID, first in the knowledge
// graph and then across every loaded practice graph. Returns the
// node and the (graphType, graphName) tuple it was found in so
// callers (assemblePatternIn) can walk edges in the right graph.
//
// Ported from cmd/knowledge-server/tools/tools_assemble.go:145 with
// the direct store reads swapped for wire calls. Practice graph
// enumeration goes through `query({graph:"practice"})` (the list-
// graphs path); the result is a JSON list with each graph's
// language slug under the `Name` field.
func resolveAssembleNode(ctx context.Context, gc GraphCaller, nodeID string) (*knowledgev1.Node, string, string, error) {
	// Try knowledge graph first.
	node, err := FetchNode(ctx, gc, nodeID)
	if err == nil && node != nil {
		return node, "", "", nil
	}
	// Practice fallback: enumerate practice graphs and try each.
	langs := listPracticeGraphs(ctx, gc)
	for _, lang := range langs {
		pn, perr := FetchNodeIn(ctx, gc, nodeID, "practice", lang)
		if perr != nil || pn == nil {
			continue
		}
		return pn, "practice", lang, nil
	}
	return nil, "", "", fmt.Errorf("no node with id %q in knowledge or any practice graph", nodeID)
}

// listPracticeGraphs returns the language slugs of every loaded practice
// graph via the Execute carrier seam: a query(graph:practice, mode:modules)
// compiled to RETURN_MODE_GRAPH_NAMES (compileQuery emits the graph-names mode
// only for mode=="modules"), decoded via engine.DecodeGraphNames → []*knowledgev1.GraphInfo
// (we project .GetName()). Returns an empty slice on any failure — the caller falls
// through to the not-found error, the same outcome as "no practice graphs loaded."
func listPracticeGraphs(ctx context.Context, gc GraphCaller) []string {
	ex, err := asExecutor(gc)
	if err != nil {
		return nil
	}
	payload := struct {
		Graph string `json:"graph"`
		Mode  string `json:"mode"`
	}{Graph: "practice", Mode: "modules"}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil
	}
	req, ok := engine.Compile("query", raw)
	if !ok {
		return nil
	}
	resp, err := ex.Execute(ctx, req)
	if err != nil {
		return nil
	}
	infos, err := engine.DecodeGraphNames(resp)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(infos))
	for _, gi := range infos {
		if gi.Name != "" {
			out = append(out, gi.Name)
		}
	}
	return out
}
