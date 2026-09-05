// SPDX-License-Identifier: Apache-2.0

// Package tools — InterceptQueryPlanTree dispatches client-side
// query(mode:plan_tree) calls to a single subtree traversal, then
// assembles the rendered text or json client-side from the flat
// node+edge result via render.BuildChildIndex. The root node is fetched
// once for the exact not-found error and the root row; the rest of the
// subtree rides one traversal RPC, so the cost is O(depth) RPCs rather
// than O(nodes).

package tools

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/projects/render"
)

// InterceptQueryPlanTree claims query(mode:"plan_tree") and renders
// the tree client-side. Returns (handled, result). When the call is
// not a plan_tree query, returns (false, _) so the chain continues.
func InterceptQueryPlanTree(ctx context.Context, deps ClientDeps, params kgtools.CallToolParams) (bool, kgtools.ToolResult) {
	if params.Name != "query" {
		return false, kgtools.ToolResult{}
	}
	var a queryArgs
	if err := json.Unmarshal(params.Arguments, &a); err != nil {
		// Bad args — server will surface the canonical invalid-args
		// error; do not pre-empt.
		return false, kgtools.ToolResult{}
	}
	if a.Mode != "plan_tree" {
		return false, kgtools.ToolResult{}
	}
	if a.ID == "" {
		// Mirror tools_query_shortcuts.go:55-57 error text exactly so
		// callers cannot distinguish server-side vs client-side
		// rejection.
		return true, errorResult("plan_tree mode requires 'id' parameter (the root plan/project/ticket ID)")
	}

	if err := accountQueryParams(armPlanTree, params.Arguments); err != nil {
		return true, errorResult(err.Error())
	}

	// ONCE PER RESPONSE, ahead of the per-node walk — never per node, and ahead of
	// every read, so an unsupported key is refused before the traversal is paid for.
	// The validator and the accepted-key vocabulary are engine's, shared with the
	// by-id and ids-hydrate arms: a plan_tree-local key list would give the tool two
	// vocabularies that drift, and this one is rendered into the refusal callers read.
	// The opt-in is FALSE, and that is a statement about this arm rather than the
	// value the compiler made cheapest: plan_tree REJECTS include_tombstones, so a
	// plan_tree read can hold no tombstoned row for tombstoned_at to project.
	if err := engine.ValidateNodeProjection(a.Fields, false); err != nil {
		return true, errorResult(err.Error())
	}

	gc := deps.GraphCaller()
	if gc == nil {
		return true, errorResult("plan_tree: graph caller unavailable")
	}

	// Mirror tools_query_shortcuts.go:58-61: depth defaults to 10,
	// honored via a.Limit when caller supplied one.
	depth := 10
	if d := int(a.Limit); d > 0 {
		depth = d
	}

	// Mirror tools_walk.go:38-44 edge-types fallback. queryArgs's
	// singular EdgeType field (json:"edge_type") is the on-wire mirror
	// of the server-side struct — do NOT introduce an EdgeTypes alias.
	edgeTypes := []kgtypes.EdgeType{kgtypes.EdgeKGContains}
	if len(a.EdgeType) > 0 {
		edgeTypes = make([]kgtypes.EdgeType, len(a.EdgeType))
		for i, et := range a.EdgeType {
			edgeTypes[i] = kgtypes.EdgeType(et)
		}
	}

	node, err := render.FetchNode(ctx, gc, a.ID)
	if err != nil {
		return true, errorResult("plan_tree: " + err.Error())
	}
	if node == nil {
		return true, errorResult("node " + a.ID + " not found")
	}

	// One subtree traversal serves BOTH formats: it returns the whole
	// descendant node set plus the subtree's contains edges, from which
	// the parent→child index is assembled client-side with no per-node
	// fetch. edgeTypes[0] is the single structure edge type (the wire
	// EdgeType field is singular — see the fallback comment above).
	nodes, structureEdges, truncated, terr := render.TraverseDescendantsWithEdges(ctx, gc, a.ID, edgeTypes[0], depth)
	if terr != nil {
		return true, errorResult("plan_tree: " + terr.Error())
	}
	childIndex, _ := render.BuildChildIndex(a.ID, nodes, structureEdges)

	// A PROJECTION IS A JSON ENVELOPE, and supplying `fields` therefore selects the
	// json render whatever `format` says — the same override both by-id arms carry
	// (render_node.go, render_misc.go). A projected row is a field map, and the text
	// tree has no way to express one.
	if a.Format == "json" || len(a.Fields) > 0 {
		return true, renderPlanTreeJSON(ctx, gc, node, a.Fields, depth, nodes, childIndex, truncated)
	}

	// Text path needs depends-on ordering. Fetch every node's depends-on
	// edge in one batched RPC. A failed fetch degrades to no ordering
	// (best-effort, mirroring the per-node firstDependsOn that ignored
	// its error), rendering children in structure-edge order rather than
	// erroring the whole tree.
	allIDs := make([]string, 0, len(nodes)+1)
	allIDs = append(allIDs, a.ID)
	for _, n := range nodes {
		allIDs = append(allIDs, n.Id)
	}
	dependsOn, derr := render.FetchDependsOnEdges(ctx, gc, allIDs)
	if derr != nil {
		dependsOn = map[string]string{}
	}

	annotationLines, annotationsTruncated, annotationErr := planTreeAnnotationLines(ctx, gc, nodes)
	truncated = truncated || annotationsTruncated

	var sb strings.Builder
	render.RenderTreeFromIndex(&sb, node, 0, depth, childIndex, dependsOn, annotationLines)
	// TWO DISCLOSURES, TWO CAUSES — the same split the two assemble arms carry.
	// The truncation notice speaks for a server row ceiling; a failed annotation
	// read gets its own, because that notice's text names a cause that did not
	// occur and a `limit` remedy this arm's caller cannot apply to it.
	out := render.AppendTruncationNotice(kgtools.TextResult(sb.String()), truncated, len(nodes))
	return true, render.AppendAnnotationReadFailureNotice(out, annotationErr)
}

// buildPlanTreeJSON renders the recursive
// {id, name, type, status, description, updated_at, children} payload
// the server's handleWalk emits for format=json, reading children from a prebuilt
// parent→child index (render.BuildChildIndex) instead of a per-node
// edge+node fetch. The whole subtree is fetched in one traversal up
// front, so this recursion issues zero RPCs.
//
// Children-key contract: the children key is present only when the
// index has children for node.Id; a node with no indexed children
// returns a row WITHOUT a children key. (The previous per-node port
// emitted "children":[] in one corner case — a parent whose every
// child edge dangled to a node that failed to fetch. That case can
// only arise from a contains edge to a hard-deleted, never-tombstoned
// node; the index path produces no entry for such a parent and omits
// the key, which is the accepted contract since the dangling target
// renders nowhere either way. A tombstoned child never reaches here:
// its structure edge is dropped server-side before the index is built.)
//
// When the caller supplied a `fields` projection, each row is that projection
// instead of the fixed key set — see planTreeRow. The children key is unaffected:
// it describes the TREE rather than the node, so it rides every row either way,
// and a projection that dropped it would turn a tree read into a flat one.
func buildPlanTreeJSON(
	node *knowledgev1.Node,
	depth, maxDepth int,
	childIndex map[string][]*knowledgev1.Node,
	fields []string,
	annotations map[string]render.SectionAnnotationCounts,
) map[string]any {
	row := planTreeRow(node, fields)
	// THE ANNOTATIONS KEY RIDES EVERY ROW THAT HAS ONE, PROJECTION OR NOT, on
	// exactly the rule the children key already follows one line below: it
	// describes the READ rather than the node, so it is not a projectable node
	// field and a projection that dropped it would turn a reviewed plan into an
	// unreviewed-looking one. It is omitted entirely for a node with no
	// annotations, so every plan written before annotations existed emits the
	// bytes it always did — which is what keeps the two plan_tree goldens green.
	if counts, ok := annotations[node.Id]; ok {
		row["annotations"] = counts
	}
	if depth >= maxDepth {
		return row
	}
	children := childIndex[node.Id]
	if len(children) == 0 {
		return row
	}
	rows := make([]map[string]any, 0, len(children))
	for _, child := range children {
		rows = append(rows, buildPlanTreeJSON(child, depth+1, maxDepth, childIndex, fields, annotations))
	}
	row["children"] = rows
	return row
}

// renderPlanTreeJSON renders the json (and projected) plan_tree envelope.
//
// SPLIT OUT OF InterceptQueryPlanTree, which crossed the statement gate when the
// annotation read reached this branch. The file's own precedent is to move a
// decision tree out rather than raise the limit, and this branch is a natural
// unit: it builds one payload, discloses two independent verdicts on it, and
// returns.
//
// THE ANNOTATION READ RUNS FOR BOTH FORMATS, not only for text. It was wired into
// the text branch alone, so query(mode:"plan_tree", format:"json") and the same
// call under a projection reported NO review state on a plan whose text render
// carried the per-section line — the json arm being the text arm minus a feature,
// which is the shape this branch hit three times before it was swept.
//
// THE truncated KEY GOES ON THE ENVELOPE ROOT, never inside buildPlanTreeJSON.
// Truncation is a property of the READ, not of a node: a leaf row asserting
// truncated:false says nothing about anything, and per-row emission inflates
// exactly the large-tree payloads where truncation matters most. The file already
// draws this distinction the other way for updated_at, which IS per-row because a
// timestamp genuinely belongs to a node. It is emitted UNCONDITIONALLY, the same
// contract every sibling envelope carries: an absent key is indistinguishable
// from an old binary. The prose notice rides alongside — the two artifacts answer
// different questions.
func renderPlanTreeJSON(
	ctx context.Context,
	gc GraphCaller,
	node *knowledgev1.Node,
	fields []string,
	depth int,
	nodes []*knowledgev1.Node,
	childIndex map[string][]*knowledgev1.Node,
	truncated bool,
) kgtools.ToolResult {
	annotations, annotationsTruncated, annotationErr := planTreeAnnotationCounts(ctx, gc, nodes)
	payload := buildPlanTreeJSON(node, 0, depth, childIndex, fields, annotations)
	truncated = truncated || annotationsTruncated
	payload["truncated"] = truncated
	out := render.AppendTruncationNotice(jsonResult(payload), truncated, len(nodes))
	return render.AppendAnnotationReadFailureNotice(out, annotationErr)
}

// planTreeAnnotationCounts reads the annotations on a tree's sections and
// projects them into the per-node count-and-kinds shape the json rows carry.
//
// IT SHARES THE READ AND THE DEGRADE RULE with the text arm's own helper: the
// same FetchSectionAnnotations call, the same log-and-return-the-error on
// failure, so the two formats cannot report different review state or disclose a
// failure differently.
func planTreeAnnotationCounts(
	ctx context.Context, gc GraphCaller, nodes []*knowledgev1.Node,
) (map[string]render.SectionAnnotationCounts, bool, error) {
	sectionIDs := make([]string, 0, len(nodes))
	for _, n := range nodes {
		if kgtypes.NodeType(n.GetType()) == kgtypes.NodePlanSection {
			sectionIDs = append(sectionIDs, n.Id)
		}
	}
	annotations, truncated, err := render.FetchSectionAnnotations(ctx, gc, sectionIDs)
	if err != nil {
		slog.Warn("plan_tree json: annotation read failed; rendering without annotation state", "error", err)
		return nil, truncated, err
	}
	return render.AnnotationCounts(annotations), truncated, nil
}

// planTreeRow builds one node's json row: the full fixed key set when no
// projection was supplied, otherwise the caller's projection through
// engine.ProjectNodeJSON — the SAME projector the by-id and ids-hydrate arms use,
// so the three cannot drift into three vocabularies.
func planTreeRow(node *knowledgev1.Node, fields []string) map[string]any {
	if len(fields) > 0 {
		return engine.ProjectNodeJSON(node, fields)
	}
	row := map[string]any{
		"id":          node.Id,
		"name":        node.SymbolName,
		"type":        node.Type,
		"status":      node.Status,
		"description": node.Description,
	}
	// Read-time provenance, set before the leaf returns below so it
	// reaches every row and not just the internal ones. Raw unix nanos,
	// key omitted when zero — the by-id convention at
	// intercept_query_examine.go:299-304.
	if node.UpdatedAt != 0 {
		row["updated_at"] = node.UpdatedAt
	}
	return row
}

// planTreeAnnotationLines reads the reviewer annotations on a sectioned plan's
// sections and returns the per-node line map RenderTreeFromIndex takes, plus the
// read's truncation verdict.
//
// A sectioned plan's annotations hang off its sections by relates-to, which the
// contains traversal cannot see, so they need their own read. TWO EXTRA WIRE
// CALLS, AND ONLY WHEN THE TREE HAS SECTIONS: a phase-and-step plan yields an
// empty section set and the read short-circuits on it without issuing anything,
// so no existing tree pays for this.
//
// A FAILED READ DEGRADES TO NO LINES AND RETURNS THE ERROR. Returning no lines
// alone would render a plan under review as one with no review on it, so the
// caller is told; what changed is WHICH disclosure says so.
//
// THIS WAS THE THIRD DEGRADE ARM AND IT WAS MISSED. The two assemble arms were
// swept when their notice was split out; this one still folded a failed
// annotation read into the truncation verdict, so a caller was told the server
// row ceiling engaged and to retry with a smaller `limit` — a cause that did not
// occur and a remedy that does not address it. Worse, the error was DISCARDED
// with not even a log line, so an operator had nothing to investigate. Both are
// fixed here: the error rides out to its own notice, and it is logged.
func planTreeAnnotationLines(
	ctx context.Context, gc GraphCaller, nodes []*knowledgev1.Node,
) (map[string]string, bool, error) {
	sectionIDs := make([]string, 0, len(nodes))
	for _, n := range nodes {
		if kgtypes.NodeType(n.GetType()) == kgtypes.NodePlanSection {
			sectionIDs = append(sectionIDs, n.Id)
		}
	}
	annotations, truncated, err := render.FetchSectionAnnotations(ctx, gc, sectionIDs)
	if err != nil {
		slog.Warn("plan_tree: annotation read failed; rendering without annotation lines", "error", err)
		return nil, truncated, err
	}
	return render.AnnotationLines(annotations), truncated, nil
}
