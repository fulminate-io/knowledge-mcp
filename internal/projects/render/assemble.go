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

// Args is the typed shape Handle parses. Mirrors the arg shape of the RETIRED
// server-side assemble tool: cmd/knowledge-server/tools/ went away when
// assembly moved client-side, so neither that directory nor the assembleArgs
// in it exists in the tree today. Every "ported from" note below names that
// same retired tool.
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

	// Source is the practice SOURCE HUB the resolve is scoped to, spelled
	// `source` because the name is free on this tool.
	//
	// IT NARROWS A BY-ID READ, WHICH SOUNDS LIKE NOTHING AND IS NOT. An id either
	// is under the hub or is not, so the hub cannot change WHICH node comes back —
	// it decides whether one comes back at all. That is the difference between
	// assembling a node the caller believed belonged to a collection and being
	// told it does not, which matters most for exactly the case the hub exists
	// for: auditing what a failed or abandoned collection actually landed. It
	// also settles the legacy question, because a hub is a fact about the
	// COMBINED graph: a hub-scoped resolve never falls back to the pre-singleton
	// graphs, which have no hubs to belong to.
	Source string `json:"source"`

	// SectionStart and SectionEnd are the inclusive section range of a CHUNKED
	// plan: the paging primitive that lets a caller read a whole plan in bounded
	// pages instead of asking for one read that returns all of it.
	//
	// POINTERS because 0 is a legal index: a plain int could not tell
	// "section 0" from "no bound supplied", and an absent bound has a different
	// meaning from a zero one (from the first section, versus that one section).
	// An out-of-bounds, inverted or negative range ERRORS naming the bound —
	// never clamps, because a clamp hands a reader a page they did not ask for
	// with nothing in the result to say so.
	SectionStart *int `json:"section_start"`
	SectionEnd   *int `json:"section_end"`
}

// Handle is the top-level entry point for the client-side assemble
// intercept. Parses the args, resolves the node (by ID or
// type+name), and dispatches to the appropriate composite assembler
// based on node.Type.
//
// Ported from the retired server-side assemble tool with these
// changes per the relocation plan:
//   - No-args recovery branch REMOVED. That tool's description still
//     mentioned no-args recovery, but the body was never reachable
//     in the client-side intercept (no snapshot store-side access). Handle errors when id + type + name are
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

	node, graphType, err := resolveAssembleNode(ctx, gc, nodeID, a.Source)
	if err != nil {
		if a.Source != "" {
			// THE HUB-SCOPED MISS NAMES THE HUB. "not in any practice graph" would
			// be false here — the node may well exist, under a different hub — and
			// a caller told the wrong thing about why a read failed goes looking in
			// the wrong place.
			return kgtools.ErrorResult(fmt.Sprintf(
				"no node with id %q under source hub %q in the practice graph", nodeID, a.Source))
		}
		return kgtools.ErrorResult(fmt.Sprintf("no node with id %q in knowledge or the practice graph", nodeID))
	}

	// Every arm's result funnels through the one appendRenderedSize call below
	// rather than each arm appending for itself: the size is computable from the
	// finished ToolResult alone, so the single dispatch choke point is where it
	// belongs, and no arm can forget it. The truncation notice goes the other
	// way — it is appended inside each arm, because only the arm holds both its
	// result and its own truncation verdict.
	// A section range on a node that is NOT a chunked plan is bad input rather
	// than a harmless extra: silently ignoring it would hand the caller a result
	// they did not ask for, and the params-are-routed rule this package follows
	// says an unroutable param errors naming itself.
	if (a.SectionStart != nil || a.SectionEnd != nil) && kgtypes.NodeType(node.Type) != kgtypes.NodePlan {
		return kgtools.ErrorResult(fmt.Sprintf(
			"assemble: section_start/section_end apply to a plan, but %s is a %s — a section range pages a chunked plan's sections",
			node.Id, node.Type))
	}

	var res kgtools.ToolResult
	if a.Format == "json" {
		res = assembleJSON(ctx, gc, node, a.SectionStart, a.SectionEnd)
	} else {
		switch kgtypes.NodeType(node.Type) {
		case kgtypes.NodePlan:
			res = assemblePlan(ctx, gc, node, a.SectionStart, a.SectionEnd)
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
		case kgtypes.NodePlanSection:
			res = assembleSection(ctx, gc, node)
		case kgtypes.NodePattern:
			res = assemblePatternIn(ctx, gc, node, graphType)
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
	kgtypes.NodePlanSection,
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
// Ported from the retired server-side assemble tool with
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

// resolveAssembleNode looks up a node by ID, first in the knowledge graph and
// then in the practice graph. Returns the node and the graph TYPE it was found
// in, so callers (assemblePatternIn) can walk edges in the right graph.
//
// IT RETURNED A (graphType, graphName) TUPLE and now returns the type alone.
// The name was the pre-singleton practice graph a legacy probe had resolved in,
// and both families it can answer for hold exactly one graph — knowledge and
// the combined practice graph — so the second half of the tuple was always the
// empty string once the legacy fan-out went.
//
// Ported from the retired server-side assemble tool with the direct store reads
// swapped for wire calls.
func resolveAssembleNode(
	ctx context.Context, gc GraphCaller, nodeID, hub string,
) (*knowledgev1.Node, string, error) {
	// A HUB-SCOPED RESOLVE IS A PRACTICE READ AND NOTHING ELSE, so the knowledge
	// probe is skipped rather than run and discarded: a knowledge node carries no
	// source hub, so a hit there could only be a node the scope excludes.
	if hub == "" {
		// Try knowledge graph first.
		node, err := FetchNode(ctx, gc, nodeID)
		if err == nil && node != nil {
			return node, "", nil
		}
	}
	// PRACTICE FALLBACK: ONE PROBE FIRST, against the combined graph. The practice
	// family holds a single graph now, so the id resolves there or it does not,
	// and asking eight graphs about a corpus that lives in one is a round trip per
	// graph for an answer already in hand.
	if pn, perr := FetchNodeIn(ctx, gc, nodeID, "practice", ""); perr == nil && pn != nil {
		if hub != "" && pn.GetMetadata()[kgtypes.MetaKeySourceHub] != hub {
			// SCOPED OUT, AND THAT IS AN ANSWER RATHER THAN A FALL-THROUGH. The node
			// exists; it belongs to another hub. Returning it would serve a read the
			// caller explicitly narrowed, and continuing to the legacy fallback
			// would search graphs that have no hubs at all.
			return nil, "", fmt.Errorf(
				"node %q is not under source hub %q", nodeID, hub)
		}
		return pn, "practice", nil
	}

	// NOTHING FOLLOWS THE PRACTICE PROBE, AND THAT IS THE CHANGE. A LEGACY
	// FALLBACK used to sit here: it enumerated the practice catalog and probed
	// every pre-singleton graph CONCURRENTLY through the legacy `language`
	// selector, taking the lowest-indexed resolver so the answer did not depend on
	// which probe finished first. Those graphs are retired and `language`
	// addresses none of them, so every one of those probes would compose the same
	// selector the single probe above already sent — N round trips to re-ask one
	// question — and the enumeration, the errgroup, the per-index slots and the
	// local listPracticeGraphs helper went with it.
	//
	// THE HUB-SCOPED MESSAGE STAYS ITS OWN, because the two misses are different
	// facts: a scoped miss means the id is not under that hub, and an unscoped one
	// means it is in neither graph family.
	if hub != "" {
		return nil, "", fmt.Errorf(
			"no node with id %q under source hub %q in the practice graph", nodeID, hub)
	}
	return nil, "", fmt.Errorf("no node with id %q in knowledge or the practice graph", nodeID)
}
