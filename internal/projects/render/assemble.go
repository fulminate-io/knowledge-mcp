// SPDX-License-Identifier: Apache-2.0

package render

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"golang.org/x/sync/errgroup"

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
//
// THE SWITCH BELOW AND handleDispatchNodeTypes MUST BE KEPT IN STEP. A Go
// switch's case set cannot be read reflectively, so the wire-cost gate compares
// its coverage against that slice rather than against the switch itself. Adding
// a case here without adding its type there leaves the new arm ungated.
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

	// Every arm's result funnels through the one appendRenderedSize call below
	// rather than each arm appending for itself: the size is computable from the
	// finished ToolResult alone, so the single dispatch choke point is where it
	// belongs, and no arm can forget it. The truncation notice goes the other
	// way — it is appended inside each arm, because only the arm holds both its
	// result and its own truncation verdict.
	var res kgtools.ToolResult
	if a.Format == "json" {
		res = assembleJSON(ctx, gc, node)
	} else {
		switch kgtypes.NodeType(node.Type) {
		case kgtypes.NodePlan:
			res = assemblePlan(ctx, gc, node)
		case kgtypes.NodeProject:
			res = assembleProjectContainer(ctx, gc, node)
		case kgtypes.NodeTicket:
			res = assembleTicket(ctx, gc, node)
		case kgtypes.NodeTestPlan:
			res = assembleTestPlan(ctx, gc, node, a.NewRun, a.RunSession)
		case kgtypes.NodeResearch:
			res = assembleResearch(ctx, gc, node)
		case kgtypes.NodeAgent, kgtypes.NodeSkill:
			res = assembleInstruction(ctx, gc, node)
		case kgtypes.NodeDecision:
			res = assembleDecision(ctx, gc, node)
		case kgtypes.NodePattern:
			res = assemblePatternIn(ctx, gc, node, graphType, graphName)
		default:
			res = assembleFallback(ctx, gc, node)
		}
	}
	return appendRenderedSize(res)
}

// handleDispatchNodeTypes names every node type Handle's switch routes to a
// dedicated arm. It exists because a Go switch's case set is not readable at
// run time: the wire-cost gate needs a list it can compare its table against,
// and this is that list. It is declared beside Handle so the two are read
// together, and Handle's doc comment names it as the thing to keep in step.
//
// The default (fallback) arm is deliberately absent — it has no node type, and
// the gate covers it with an explicit row for an unrecognized type instead.
var handleDispatchNodeTypes = []kgtypes.NodeType{
	kgtypes.NodePlan,
	kgtypes.NodeProject,
	kgtypes.NodeTicket,
	kgtypes.NodeTestPlan,
	kgtypes.NodeResearch,
	kgtypes.NodeAgent,
	kgtypes.NodeSkill,
	kgtypes.NodeDecision,
	kgtypes.NodePattern,
}

// appendRenderedSize adds the trailing disclosure naming what the assembled
// result costs a caller's context.
//
// A SEPARATE trailing content block, never concatenated into the payload, for
// the same reason AppendTruncationNotice gives: blocks are delivered as an
// array, so a format=json payload stays in its own block and remains
// independently parseable.
//
// BYTES, NOT A TOKEN ESTIMATE. Bytes are measured off the string in hand; a
// token count would be a derived number presented as a fact.
//
// The count covers every text block already on the result, which includes a
// truncation notice when one was appended — that notice is part of what the
// caller receives. It never counts itself: the size block is built from the
// total and appended afterwards.
func appendRenderedSize(res kgtools.ToolResult) kgtools.ToolResult {
	total := 0
	for _, b := range res.Content {
		total += len(b.Text)
	}
	res.Content = append(res.Content, kgtools.ContentBlock{
		Type: "text",
		Text: fmt.Sprintf("%d rendered bytes.", total),
	})
	return res
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
	// Practice fallback: enumerate practice graphs and probe them CONCURRENTLY.
	// The probes are independent — each targets a different graph and none
	// informs the next — so probing them serially costs one full round trip per
	// loaded graph before the answer is known, on a path that is already the
	// slow one.
	langs := listPracticeGraphs(ctx, gc)
	if len(langs) == 0 {
		return nil, "", "", fmt.Errorf("no node with id %q in knowledge or any practice graph", nodeID)
	}

	// RESULTS GO IN A PER-INDEX SLOT, NOT A SHARED APPEND. Probes finish in
	// arbitrary order, and the serial loop this replaces returned the FIRST
	// graph in listPracticeGraphs order that resolved. Picking the lowest
	// populated slot after Wait preserves that exactly. In practice an id
	// resolves in at most one graph — but "in practice" is not a contract, and
	// a resolver that answers differently on different calls is a worse defect
	// than a slow one.
	found := make([]*knowledgev1.Node, len(langs))

	g, gctx := errgroup.WithContext(ctx)
	// These are network-bound probes, not CPU work, so the bound is a small
	// constant rather than GOMAXPROCS: eight in-flight reads is enough to
	// collapse the latency of every practice graph this corpus loads, without
	// opening an unbounded fan-out if the number of graphs grows.
	g.SetLimit(min(len(langs), 8))
	for i, lang := range langs {
		g.Go(func() error {
			// A PROBE FAILURE IS NOT A RESOLUTION FAILURE. An error against
			// graph A says nothing about graph B, and the serial loop already
			// treated a per-graph error as "not here" and continued. Returning
			// the error here would cancel the sibling probes through gctx and
			// turn one unreachable graph into a global not-found.
			pn, perr := FetchNodeIn(gctx, gc, nodeID, "practice", lang)
			if perr == nil && pn != nil {
				found[i] = pn
			}
			return nil
		})
	}
	_ = g.Wait() // every probe returns nil; failures are recorded as empty slots

	for i, pn := range found {
		if pn != nil {
			return pn, "practice", langs[i], nil
		}
	}
	// Unchanged: this message covers both "probed and absent everywhere" and
	// "every probe failed", which is the same outcome for a caller.
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
