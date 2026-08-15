// SPDX-License-Identifier: Apache-2.0

// Package tools — the seeded TWO-LAYER backend the manage(repair_edges) tests
// drive, plus the node/edge constructors they share. It lives beside the tests
// rather than inside them because the fixture outgrew the file: both files stay
// under lefthook's hard 500-line cap.

package tools

import (
	"context"
	"encoding/json"
	"slices"
	"sort"
	"testing"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/enginetest"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// repairEdgesLayerKey identifies ONE seeded graph layer: a repo, plus the BARE
// overlay name ("" for the base graph). A single flat graph cannot express the
// base/overlay split at all — through it a base-only repair and a correct one
// read identically — so the fixture is keyed by the layer every request resolves.
type repairEdgesLayerKey struct {
	Repo   string
	Branch string
}

// repairEdgesLayer is one layer's seeded contents.
type repairEdgesLayer struct {
	files   []*knowledgev1.Node
	symbols map[string]*knowledgev1.Node
	edges   []*knowledgev1.Edge
}

// repairEdgesFake is a seeded TWO-LAYER code-graph backend for the repair_edges
// arm. It serves the four reads the operation makes and records every plan it saw:
//
//   - the overlay-key catalog read (RETURN_MODE_GRAPH_NAMES + overlay_of),
//     serving whatever key FORM the case seeded — the cloud "base@overlay" key or
//     the OSS bare name;
//   - the file-node keyset browse — CURSOR-HONORING, so a single un-paged read
//     is distinguishable from a correct drain;
//   - the RETURN_MODE_EDGES pivot read — returns edges incident to the pivot set
//     in BOTH directions (the real carrier's shape) and honors the edge-type
//     filter, so a predicate widened past CONTAINS is observable;
//   - the by-ids hydrate.
//
// EVERY SERVED REQUEST'S RESOLVED (Repo, Branch) IS RECORDED. Without that
// recorder a repair sending Branch="agent@launch-fixes" and one sending
// Branch="launch-fixes" would be indistinguishable through a fake that merely
// fails to find data for a key: it would report zero, which is the very false
// green under test.
//
// Every mutation is RECORDED — which is what lets a preview test assert that
// nothing was mutated instead of merely that the text said so — and an UNLINK is
// really applied TO THE ADDRESSED LAYER ONLY, so an unlink aimed at the base
// cannot clean the overlay or vice versa, and an execute test's verify-after
// re-enumeration observes the effect of its own mutation.
type repairEdgesFake struct {
	layers      map[repairEdgesLayerKey]*repairEdgesLayer
	overlayKeys map[string][]string

	queryPlans  []*knowledgev1.QueryPlan
	mutations   []*knowledgev1.MutationPlan
	servedKeys  []repairEdgesLayerKey
	mutatedKeys []repairEdgesLayerKey
}

func newRepairEdgesFake() *repairEdgesFake {
	return &repairEdgesFake{
		layers:      map[repairEdgesLayerKey]*repairEdgesLayer{},
		overlayKeys: map[string][]string{},
	}
}

// layer returns the seeded layer for key, creating an empty one on demand. A read
// addressed at an UNSEEDED layer therefore serves nothing rather than falling back
// to another layer — the property that makes a mis-scoped read observable.
func (f *repairEdgesFake) layer(key repairEdgesLayerKey) *repairEdgesLayer {
	l, ok := f.layers[key]
	if !ok {
		l = &repairEdgesLayer{symbols: map[string]*knowledgev1.Node{}}
		f.layers[key] = l
	}
	return l
}

// repairEdgesKeyOf is the ONE derivation from a request's selector to a layer key,
// so every arm reads the same thing.
func repairEdgesKeyOf(sel *knowledgev1.GraphSelector) repairEdgesLayerKey {
	return repairEdgesLayerKey{Repo: sel.GetRepo(), Branch: sel.GetBranch()}
}

func (f *repairEdgesFake) Execute(
	_ context.Context, req *knowledgev1.ExecuteRequest,
) (*knowledgev1.ExecuteResponse, error) {
	key := repairEdgesKeyOf(req.GetTarget())
	if m := req.GetMutation(); m != nil {
		f.mutations = append(f.mutations, m)
		f.mutatedKeys = append(f.mutatedKeys, key)
		return &knowledgev1.ExecuteResponse{AffectedCount: f.applyUnlink(key, m)}, nil
	}
	q := req.GetQuery()
	f.queryPlans = append(f.queryPlans, q)
	// The catalog read is graph-scoped, not layer-scoped (its selector names no
	// repo), so it is deliberately NOT recorded in servedKeys: servedKeys is the
	// set of LAYERS the repair actually read or would have read.
	if q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_GRAPH_NAMES {
		return f.serveOverlayKeys(q), nil
	}
	f.servedKeys = append(f.servedKeys, key)
	switch {
	case q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_EDGES:
		return f.serveEdges(key, q), nil
	case q.GetSelection().GetNodeType() == string(kgtypes.NodeFile):
		return f.serveFileBrowse(key, q), nil
	default:
		return f.serveHydrate(key, q), nil
	}
}

// serveOverlayKeys serves the overlay_of RETURN_MODE_GRAPH_NAMES read with the
// seeded keys for the requested base, shaped like the in-tree precedent
// fakeGraphCaller.execOverlayKeys. The seeded FORM is per-case: the cloud
// "base@overlay" key or the OSS bare overlay name.
func (f *repairEdgesFake) serveOverlayKeys(q *knowledgev1.QueryPlan) *knowledgev1.ExecuteResponse {
	var infos []*knowledgev1.GraphInfo
	for _, key := range f.overlayKeys[q.GetOverlayOf()] {
		if key != "" {
			infos = append(infos, &knowledgev1.GraphInfo{Name: key})
		}
	}
	return &knowledgev1.ExecuteResponse{GraphNames: infos}
}

// applyUnlink REALLY removes the fanned edges FROM THE ADDRESSED LAYER, so the
// handler's verify-after re-enumeration observes the effect of its own mutation
// rather than a fake that always answers "done". Without it, a correct execute
// path and a no-op one both leave the fossil resident and read identically; and
// without the per-layer scoping, a base-only repair would appear to clean the
// overlay too.
func (f *repairEdgesFake) applyUnlink(key repairEdgesLayerKey, m *knowledgev1.MutationPlan) int64 {
	if m.GetKind() != knowledgev1.MutationPlan_MUTATION_KIND_UNLINK {
		return 0
	}
	l := f.layer(key)
	spec := m.GetEdgeSpec()
	sources := make(map[string]bool, len(m.GetSelection().GetIds()))
	for _, id := range m.GetSelection().GetIds() {
		sources[id] = true
	}
	kept := make([]*knowledgev1.Edge, 0, len(l.edges))
	var removed int64
	for _, e := range l.edges {
		if sources[e.GetFromId()] && e.GetToId() == spec.GetToId() && e.GetType() == spec.GetRelationship() {
			removed++
			continue
		}
		kept = append(kept, e)
	}
	l.edges = kept
	return removed
}

func (f *repairEdgesFake) serveFileBrowse(key repairEdgesLayerKey, q *knowledgev1.QueryPlan) *knowledgev1.ExecuteResponse {
	nodes := append([]*knowledgev1.Node(nil), f.layer(key).files...)
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].GetId() < nodes[j].GetId() })
	if cursor := q.GetAfterId(); cursor != "" {
		kept := nodes[:0]
		for _, n := range nodes {
			if n.GetId() > cursor {
				kept = append(kept, n)
			}
		}
		nodes = kept
	}
	if lim := int(q.GetLimit()); lim > 0 && len(nodes) > lim {
		nodes = nodes[:lim]
	}
	return enginetest.ResponseWithNodes(nodes...)
}

func (f *repairEdgesFake) serveEdges(key repairEdgesLayerKey, q *knowledgev1.QueryPlan) *knowledgev1.ExecuteResponse {
	pivot := make(map[string]bool, len(q.GetIds()))
	for _, id := range q.GetIds() {
		pivot[id] = true
	}
	wantTypes := q.GetSelection().GetEdgeTypes()
	edges := f.layer(key).edges
	out := make([]*knowledgev1.Edge, 0, len(edges))
	for _, e := range edges {
		if !pivot[e.GetFromId()] && !pivot[e.GetToId()] {
			continue
		}
		if len(wantTypes) > 0 && !slices.Contains(wantTypes, e.GetType()) {
			continue
		}
		out = append(out, e)
	}
	return &knowledgev1.ExecuteResponse{Edges: out}
}

func (f *repairEdgesFake) serveHydrate(key repairEdgesLayerKey, q *knowledgev1.QueryPlan) *knowledgev1.ExecuteResponse {
	symbols := f.layer(key).symbols
	out := make([]*knowledgev1.Node, 0, len(q.GetIds()))
	for _, id := range q.GetIds() {
		if n, ok := symbols[id]; ok {
			out = append(out, n)
		}
	}
	return enginetest.ResponseWithNodes(out...)
}

// repairEdgesCall drives InterceptManage(repair_edges) over the seeded fake.
func repairEdgesCall(t *testing.T, f *repairEdgesFake, args string) (bool, kgtools.ToolResult) {
	t.Helper()
	deps := interceptTestDeps{gc: f}
	return InterceptManage(opCtx(), deps, kgtools.CallToolParams{Name: "manage", Arguments: json.RawMessage(args)})
}

func repairFileNode(path string) *knowledgev1.Node {
	return &knowledgev1.Node{Id: path, Type: string(kgtypes.NodeFile), FilePath: path}
}

func repairSymbolNode(id, filePath string) *knowledgev1.Node {
	return &knowledgev1.Node{Id: id, Type: "function", FilePath: filePath}
}

func repairContainsEdge(from, to string) *knowledgev1.Edge {
	return &knowledgev1.Edge{FromId: from, ToId: to, Type: string(kgtypes.EdgeContains)}
}
