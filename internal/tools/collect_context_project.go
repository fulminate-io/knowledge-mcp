// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"sort"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/externalcollector"
	"github.com/fulminate-io/knowledge-mcp/internal/graphsel"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/paging"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

// collect_context_project.go — the READ PRIMITIVES every family arm rides, and
// the PROJECTION from a fetched node to the declared field set.
//
// THE NODE READ IS THE COMPILED-IN LINKER'S OWN, WRITTEN BESIDE IT RATHER THAN
// INSIDE IT. The linker's helpers are unexported and, more to the point, they
// are on a hot path this contract must not join: the linker drains every code
// graph's whole file set on every post-collect run, and a context filter grafted
// into that helper would change what every builtin collect pays with nothing in
// the suite to say so. The shapes are the same, the ownership is not.
//
// THE EDGE READ IS foundation.FetchAllEdges AND IS NOT WRITTEN HERE AT ALL. The
// edge arm used to ride the ingest service's cloud-subgraph fetch, which
// returned one cloud graph whole and indexed its edges by endpoint in memory;
// that RPC and the cloud family it served are both gone. FetchAllEdges reads
// RETURN_MODE_EDGES through the SAME Execute carrier the node drain uses, takes
// the graph type as a parameter so it serves any registered family, and pages in
// bounded pivots. Its own doc states the two properties this path depends on: the
// pages are sequential and must NOT be parallelized, because they share the
// drain's dedup map; and a pivot no from_id band can divide is an ERROR naming
// the pivot rather than a short set, which this path propagates rather than
// degrading to a partial block — the same rule fillCollectContext states for
// every other unsatisfiable declaration.
//
// THE PROJECTION IS A SWITCH OVER DECLARED FIELD NAMES rather than a copy with
// omissions, and that direction matters: a copy-then-blank projection carries a
// new field the moment the fetch gains one, which is exactly the no-baseline
// property this contract exists to hold. A field nobody declared is never
// written here at all.

// contextDrainNodes reads EVERY node of one type in one graph, as bounded
// id-keyset pages.
//
// THE PAGE LIMIT IS SET AFTER THE REST OF THE PAYLOAD and it is positive, both
// deliberately: a non-positive limit is rewritten by the compiler to the small
// browse default, so a drain that omitted it would silently read one short page
// and report it as the whole set.
//
// THE INSTANCE KEY COMES FROM graphsel AND IS NOT SPELLED HERE. A code graph is
// addressed by `repo` and a registered custom graph by `name`; hard-coding
// either would read the wrong graph for the other family, and graphsel holds
// that switch exactly once for the whole client.
func contextDrainNodes(ctx context.Context, gc GraphCaller, family, graphName, nodeType string) ([]*knowledgev1.Node, error) {
	return paging.DrainKeysetPages(func(afterID string) ([]*knowledgev1.Node, error) {
		argMap := graphsel.ScopePayload(kgtypes.GraphType(family), graphName, false)
		argMap["type"] = nodeType
		argMap["limit"] = paging.BrowsePageSize
		argMap["after_id"] = afterID
		argMap["skip_total"] = true
		args, err := json.Marshal(argMap)
		if err != nil {
			return nil, fmt.Errorf("marshal node browse: %w", err)
		}
		req, ok := engine.Compile("query", args)
		if !ok {
			return nil, fmt.Errorf("the node-browse args do not reduce to an ExecuteRequest")
		}
		resp, err := gc.Execute(ctx, req)
		if err != nil {
			return nil, err
		}
		return engine.DecodeNodes(resp)
	}, paging.BrowsePageSize)
}

// contextDrainAllNodes reads EVERY node in one graph, whatever its type, as
// bounded id-keyset pages.
//
// IT IS A SIBLING OF contextDrainNodes RATHER THAN A PARAMETER ON IT, because
// the difference is not a value: the browse either carries a `type` key or it
// does not, and passing an empty type would be a browse for the node type named
// by the empty string. Everything else — the page size, the positive limit, the
// keyset cursor, the graphsel instance key — is the same and is stated there.
func contextDrainAllNodes(ctx context.Context, gc GraphCaller, family, graphName string) ([]*knowledgev1.Node, error) {
	return paging.DrainKeysetPages(func(afterID string) ([]*knowledgev1.Node, error) {
		argMap := graphsel.ScopePayload(kgtypes.GraphType(family), graphName, false)
		// THE EXPLICIT EVERY-TYPE KEY, not an omitted type. The query compiler
		// refuses a read that reduces to no recognized shape rather than serving
		// it as something the caller did not ask for, so "every node" is asked for
		// by name.
		argMap["all_types"] = true
		argMap["limit"] = paging.BrowsePageSize
		argMap["after_id"] = afterID
		argMap["skip_total"] = true
		args, err := json.Marshal(argMap)
		if err != nil {
			return nil, fmt.Errorf("marshal node browse: %w", err)
		}
		req, ok := engine.Compile("query", args)
		if !ok {
			return nil, fmt.Errorf("the node-browse args do not reduce to an ExecuteRequest")
		}
		resp, err := gc.Execute(ctx, req)
		if err != nil {
			return nil, err
		}
		return engine.DecodeNodes(resp)
	}, paging.BrowsePageSize)
}

// projectNodes narrows a fetched node set to the declared node types, the
// declared path basenames and the declared field set, in that order.
//
// THE TYPE FILTER IS SKIPPED ENTIRELY UNDER all_node_types, and it has to be
// rather than being satisfied by a wider list: the filter is a membership test
// over the DECLARED types, which is empty under the selector, so a declaration
// asking for every type would be projected down to nothing. The two selectors
// are mutually exclusive (FamilyDeclaration.validate refuses both together), so
// exactly one of these two readings applies to any admitted declaration.
func projectNodes(nodes []*knowledgev1.Node, family externalcollector.FamilyDeclaration) []externalcollector.ContextNode {
	out := make([]externalcollector.ContextNode, 0, len(nodes))
	for _, n := range nodes {
		if !family.AllNodeTypes && !slices.Contains(family.NodeTypes, n.GetType()) {
			continue
		}
		if len(family.PathBasenames) > 0 &&
			!slices.Contains(family.PathBasenames, filepath.Base(n.GetFilePath())) {
			continue
		}
		out = append(out, projectNode(n, family))
	}
	return out
}

// projectNode copies exactly the declared fields off one node.
//
// A DECLARED METADATA KEY THE NODE DOES NOT HOLD IS OMITTED rather than carried
// empty, which is the rule the environment allowlist states in its own words: an
// absent value and one set to the empty string are different inputs, and a
// module reading a declared key back as "" cannot tell which it got.
func projectNode(n *knowledgev1.Node, family externalcollector.FamilyDeclaration) externalcollector.ContextNode {
	var out externalcollector.ContextNode
	for _, field := range family.NodeFields {
		switch field {
		case externalcollector.ContextNodeFieldID:
			out.ID = n.GetId()
		case externalcollector.ContextNodeFieldType:
			out.Type = n.GetType()
		case externalcollector.ContextNodeFieldSymbolName:
			out.SymbolName = n.GetSymbolName()
		case externalcollector.ContextNodeFieldFilePath:
			out.FilePath = n.GetFilePath()
		case externalcollector.ContextNodeFieldContent:
			out.Content = n.GetContent()
		}
	}
	for _, key := range family.MetadataKeys {
		v := kgtypes.Value(n, key)
		if v == "" {
			continue
		}
		if out.Metadata == nil {
			out.Metadata = make(map[string]string, len(family.MetadataKeys))
		}
		out.Metadata[key] = v
	}
	return out
}

// contextGraphEdges reads the edges incident to one graph's projected nodes and
// projects each to the declared edge field set.
//
// THE PIVOT SET IS THE PROJECTED NODES' IDS, which is what makes this read
// bounded at all: FetchAllEdges pages over an id set, and the declaration's
// node_types is what says which ids those are, while the `id` node field is what
// puts an id on them.
//
// A DECLARATION MISSING EITHER RECEIVES NO EDGES, so both are refused upstream —
// see FamilyDeclaration.validate, which now enforces the pair rather than this
// comment asserting it. The claim used to be written here and was not true: the
// incoherence condition named node fields and metadata keys and omitted
// edge_fields entirely, so `{"aws":{"edge_fields":["from_id"]}}` validated,
// filled, and returned a graph with no edges and no error. Fields with nowhere
// to land are the same defect on either side, and they are now refused on the
// same rule.
//
// EVERY EDGE INCIDENT TO A CARRIED NODE REACHES THIS SET, in either direction:
// an edge whose other endpoint was not carried is still an edge the module can
// see one end of, and dropping it would silently narrow a dependency walk. An
// edge with NEITHER endpoint carried is unreachable here and is also unusable by
// any module, since it names nothing the module can see.
//
// THE DEDUPE IS BY THE TRIPLE AND NOT BY THE PROJECTION. Two distinct edges
// between the same pair differ only in their type once from_id and to_id are the
// declared fields, so deduping on the projected pair would collapse them into
// one; the drain's own union is by full edge, and this second pass exists only
// because an edge incident to two carried nodes is returned for each pivot.
func contextGraphEdges(
	ctx context.Context,
	gc GraphCaller,
	family, graphName string,
	nodes []externalcollector.ContextNode,
	decl externalcollector.FamilyDeclaration,
) ([]externalcollector.ContextEdge, error) {
	ids := contextEdgePivotIDs(nodes)
	if len(ids) == 0 {
		return nil, nil
	}
	edges, err := foundation.FetchAllEdges(ctx, gc, kgtypes.GraphType(family), graphName, ids, nil)
	if err != nil {
		return nil, err
	}
	type edgeKey struct{ from, to, typ string }
	seen := make(map[edgeKey]struct{}, len(edges))
	out := make([]externalcollector.ContextEdge, 0, len(edges))
	for i := range edges {
		e := &edges[i]
		k := edgeKey{from: e.GetFromId(), to: e.GetToId(), typ: e.GetType()}
		if _, dup := seen[k]; dup {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, projectEdge(e, decl))
	}
	return out, nil
}

// contextEdgePivotIDs is the pivot set the edge read pages over: the ids of the
// nodes already carried in this graph's slice, deduped and sorted.
//
// IT READS THE PROJECTED NODES RATHER THAN THE FETCHED ONES so the edge set
// cannot name a node the block does not carry. SORTED because the pivot pages
// are cut from this order, so an unsorted set would issue different page
// boundaries run to run for one unchanged store.
//
// A NODE PROJECTED WITHOUT ITS id CONTRIBUTES NO PIVOT, which is the honest
// answer rather than a defect to route around: a declaration asking for edges
// while declining to carry node ids has asked for endpoints it gave the module
// no way to resolve.
func contextEdgePivotIDs(nodes []externalcollector.ContextNode) []string {
	seen := make(map[string]struct{}, len(nodes))
	out := make([]string, 0, len(nodes))
	for _, n := range nodes {
		if n.ID == "" {
			continue
		}
		if _, dup := seen[n.ID]; dup {
			continue
		}
		seen[n.ID] = struct{}{}
		out = append(out, n.ID)
	}
	sort.Strings(out)
	return out
}

// projectEdge copies exactly the declared fields off one edge.
func projectEdge(e *knowledgev1.Edge, family externalcollector.FamilyDeclaration) externalcollector.ContextEdge {
	var out externalcollector.ContextEdge
	for _, field := range family.EdgeFields {
		switch field {
		case externalcollector.ContextEdgeFieldFromID:
			out.FromID = e.GetFromId()
		case externalcollector.ContextEdgeFieldToID:
			out.ToID = e.GetToId()
		}
	}
	return out
}
