// SPDX-License-Identifier: Apache-2.0

package tools

// fake_graph_caller_seed_test.go holds the scripted GraphCaller's seeded-node
// decode/encode helpers, the ONE Target-aware id resolver both its by-id and
// by-ids reads go through, and the seed constructor its tests drive it with.
//
// It is a sibling rather than part of fake_graph_caller_test.go for the same
// reason the graph-names and call-log helpers are already siblings: that file sits
// against the repo's per-file length ceiling, and a change to the fake cannot land
// while it is over.

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/enginetest"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// decodeSeededNode turns one seeded JSON body into a Node. Factored out of
// encodeNodeResult so the single-id and bulk paths share ONE decode — two copies
// would be free to drift on exactly the metadata round-trip the charge property
// fold depends on.
func decodeSeededNode(res kgtools.ToolResult) (*knowledgev1.Node, bool) {
	var body string
	if len(res.Content) > 0 {
		body = res.Content[0].Text
	}
	var n knowledgev1.Node
	if uerr := json.Unmarshal([]byte(body), &n); uerr != nil {
		return nil, false
	}
	return &n, true
}

// stampTombstone marks a decoded node with the tombstone the seed recorded, so a
// caller that READ with tombstones can still tell a live node from a deleted one.
//
// WITHOUT IT THE FLAG DECIDED ONLY VISIBILITY. seededNodeResult hides a
// tombstoned id from a tombstone-blind read, which is half the server's
// behaviour; the other half is that a tombstone-INCLUDING read serves the row
// with its TombstonedAt set. A guard that reads with tombstones and then decides
// on the stamp — the hub resolver does, because a deleted hub is a different
// refusal from a missing one — could otherwise not be driven at all.
func (f *fakeGraphCaller) stampTombstone(id string, n *knowledgev1.Node) *knowledgev1.Node {
	if n != nil && f.tombstonedIDs[id] {
		n.TombstonedAt = 1
	}
	return n
}

// encodeNodeResult decodes a seeded single-node JSON body into a knowledgev1.Node and
// re-emits it as the nodes_json carrier ([]knowledgev1.Node), the shape render.Fetch-
// NodeIn decodes. A malformed seed surfaces as not-found.
func (f *fakeGraphCaller) encodeNodeResult(id string, res kgtools.ToolResult) (*knowledgev1.ExecuteResponse, error) {
	n, decoded := decodeSeededNode(res)
	if !decoded {
		return &knowledgev1.ExecuteResponse{}, nil
	}
	return enginetest.ResponseWithNodes(f.stampTombstone(id, n)), nil
}

func nodeResultJSON(t *testing.T, id, typ string, metadata map[string]string) kgtools.ToolResult {
	t.Helper()
	payload := map[string]any{
		"id":       id,
		"type":     typ,
		"metadata": metadata,
	}
	b, err := json.MarshalIndent(payload, "", "  ")
	require.NoError(t, err)
	return kgtools.ToolResult{Content: []kgtools.ContentBlock{{Type: "text", Text: string(b)}}}
}

// seededNodeResult resolves ONE id against the request's Target through the
// three seeding maps, in precedence order, and reports whether the addressed
// graph holds it.
//
// IT IS A SHARED HELPER BECAUSE THE PLURAL READ ASKS THE SAME QUESTION. The
// single-id path owned this chain alone, and the bulk hydrate read the FLAT map
// only — so a fake that seeded a node into one graph answered a by-id read
// honestly and a by-ids read as though every graph held it. Any composer that
// resolved endpoints in bulk therefore measured a graph it had not addressed.
//
// A (type,name) or (type) key that IS configured and does NOT hold the id means
// "not in this graph", even when the flat map does hold it — that is the
// existing single-id rule, kept verbatim, and it is what lets a test say a node
// lives in practice and NOT in knowledge.
func (f *fakeGraphCaller) seededNodeResult(
	req *knowledgev1.ExecuteRequest, id string,
) (kgtools.ToolResult, bool) {
	// TOMBSTONE VISIBILITY, MODELED RATHER THAN IGNORED. The server drops
	// tombstoned rows from a read unless the plan asks for them, and this resolver
	// answered every id whatever the flag said — so a caller's IncludeTombstones /
	// ExcludeTombstones choice was unobservable in this package and could be
	// flipped with the whole suite still green. Seeding an id here makes the flag
	// decide the answer, which is what lets a row assert the choice.
	if f.tombstonedIDs[id] && !req.GetQuery().GetIncludeTombstones() {
		return kgtools.ToolResult{}, false
	}
	// Name-aware lookup first (when seeded): resolve only in the request's
	// (graphType,graphName).
	if f.queryResponsesByGraphName != nil {
		if byID, hasKey := f.queryResponsesByGraphName[targetGraphKey(req.GetTarget())]; hasKey {
			res, ok := byID[id]
			return res, ok
		}
	}
	// Graph-aware lookup next (when seeded): resolve only in the request's
	// Target graph. Empty Target → "knowledge".
	if f.queryResponsesByGraph != nil {
		graph := req.GetTarget().GetGraph()
		if graph == "" {
			graph = "knowledge"
		}
		if byID, hasGraph := f.queryResponsesByGraph[graph]; hasGraph {
			res, ok := byID[id]
			return res, ok
		}
	}
	res, ok := f.queryResponses[id]
	return res, ok
}
