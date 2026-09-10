// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"encoding/json"
	"sort"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collectorconfig"
	"github.com/fulminate-io/knowledge-mcp/internal/externalcollector"
)

// collect_context_wire_test.go — WHAT REACHES THE PROVIDER, observed on the wire
// by a stub that echoes its own call arguments back as nodes.
//
// THE INSTRUMENT IS AN ECHO, and it is the only way to read what was SENT rather
// than what the filler believed it sent. Its in-tree precedent is the
// environment-allowlist report in externalcollector's stub provider, which
// reports the child's environment back the same way.
//
// EVERY ABSENCE ASSERTION HERE CARRIES A SAME-RUN POSITIVE CONTROL through the
// same instrument, the same field and the same path: `params` is present when a
// collect carries params and absent when it does not, so a zero on `context`
// reads as a decision rather than as a dead echo.

// argEchoProvider stands a provider up whose tool returns ONE NODE PER TOP-LEVEL
// CALL-ARGUMENT KEY, and records the whole argument document for the rows that
// assert on the block's content rather than on its presence.
func argEchoProvider(t *testing.T) (url string, seen *echoedArgs) {
	t.Helper()
	rec := &echoedArgs{}
	u := startCustomProvider(t, func(req *mcp.CallToolRequest) any {
		rec.record(t, req)
		nodes := make([]any, 0, len(rec.keys()))
		for _, k := range rec.keys() {
			nodes = append(nodes, map[string]any{"id": "arg:" + k, "type": "arg"})
		}
		return map[string]any{"nodes": nodes, "edges": []any{}, "walk_complete": true}
	})
	return u, rec
}

// echoedArgs holds the decoded call-argument document of the last call.
type echoedArgs struct{ doc map[string]any }

func (e *echoedArgs) record(t *testing.T, req *mcp.CallToolRequest) {
	t.Helper()
	e.doc = map[string]any{}
	if len(req.Params.Arguments) > 0 {
		require.NoError(t, json.Unmarshal(req.Params.Arguments, &e.doc))
	}
}

// keys returns the top-level argument keys in sorted order.
func (e *echoedArgs) keys() []string {
	out := make([]string, 0, len(e.doc))
	for k := range e.doc {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// contextBlock returns the decoded `context` argument, or nil when the key is
// absent. The two are DIFFERENT and the caller must distinguish them, which is
// why this returns the ok flag rather than a zero value.
func (e *echoedArgs) contextBlock(t *testing.T) (externalcollector.CollectContext, bool) {
	t.Helper()
	raw, ok := e.doc["context"]
	if !ok {
		return externalcollector.CollectContext{}, false
	}
	b, err := json.Marshal(raw)
	require.NoError(t, err)
	var block externalcollector.CollectContext
	require.NoError(t, json.Unmarshal(b, &block))
	return block, true
}

// declaringEntry builds a config entry that registers the stub family AND
// declares foreign-graph context.
func declaringEntry(url string, decl externalcollector.ContextDeclaration) namedEntry {
	e := namedCustomDef(customStubFamily, url)
	e.entry.Context = decl
	return e
}

// resourceDecl is a registered family's declared slice: the graph name, four
// node facts, and the two edge endpoints.
//
// THE FAMILY IS A REGISTERED GRAPH TYPE, not a builtin. That is the substance of
// the vocabulary change: the block's key set is whatever the operator registered,
// so every row below asks for a family under a name the fixture's registry holds
// rather than a name compiled into this module.
func resourceDecl() externalcollector.ContextDeclaration {
	return externalcollector.ContextDeclaration{
		"aws": {
			NodeTypes:    []string{"aws-resource"},
			NodeFields:   []string{"id", "type", "symbol_name"},
			MetadataKeys: []string{"resource_type"},
			EdgeFields:   []string{"from_id", "to_id"},
		},
	}
}

// awsDeps wires the echo provider, the registry holding `aws`, and the standing
// two-resource fixture.
func awsDeps(t *testing.T, url string, decl externalcollector.ContextDeclaration) *customDeps {
	t.Helper()
	deps := newCustomDeps(t, declaringEntry(url, decl))
	deps.crud = registeredCRUD("aws")
	deps.graphs = twoResourceCaller()
	return deps
}

// --- R2: a module declaring NOTHING receives no block ---

// TestCustomCollect_UndeclaredEntrySendsNoContextKey is the by-construction arm
// and the control for every other row in this file: with no declaration the
// argument document carries exactly the keys it carried before this contract
// grew, and `context` is ABSENT rather than empty or null.
func TestCustomCollect_UndeclaredEntrySendsNoContextKey(t *testing.T) {
	url, seen := argEchoProvider(t)
	deps := newCustomDeps(t, customDef(url))

	handled, res := callCollect(deps, `{"type":"jira","id":"board","params":{"project":"FUL"}}`)
	require.True(t, handled)
	require.False(t, res.IsError, resultText(res))

	assert.Equal(t, []string{"id", "params"}, seen.keys(),
		"an entry that declares no context sends the same arguments it always did")
	_, present := seen.contextBlock(t)
	assert.False(t, present, "the key must be ABSENT, not present and empty")
}

// TestCustomCollect_ParamlessUndeclaredEntrySendsIDAlone is the second half of
// the control: the paramless collect omits `params` entirely, which is what
// makes the presence of `params` above a signal rather than a constant.
func TestCustomCollect_ParamlessUndeclaredEntrySendsIDAlone(t *testing.T) {
	url, seen := argEchoProvider(t)
	deps := newCustomDeps(t, customDef(url))

	handled, res := callCollect(deps, `{"type":"jira","id":"board"}`)
	require.True(t, handled)
	require.False(t, res.IsError, resultText(res))

	assert.Equal(t, []string{"id"}, seen.keys(),
		"an absent optional is absent, never empty — the params precedent this block follows")
}

// --- R2: a declaring module receives the block, filled from the store ---

// TestCustomCollect_DeclaringEntryReceivesTheContextBlock is R2's core
// observation: the block reaches the provider on the wire, and its content is
// the declared slice projected out of what the client read.
func TestCustomCollect_DeclaringEntryReceivesTheContextBlock(t *testing.T) {
	url, seen := argEchoProvider(t)
	deps := awsDeps(t, url, resourceDecl())

	handled, res := callCollect(deps, `{"type":"jira","id":"board"}`)
	require.True(t, handled)
	require.False(t, res.IsError, resultText(res))

	assert.Equal(t, []string{"context", "id"}, seen.keys(),
		"a declaring entry's collect carries the block beside the collect id")

	block, present := seen.contextBlock(t)
	require.True(t, present, "the declared block must be on the wire")
	require.Len(t, block["aws"], 1, "one entry per aws graph in the declared slice")
	assert.Equal(t, "prod", block["aws"][0].GraphName)
	// AN UNDECLARED FAMILY IS ABSENT FROM THE MAP, not present and empty. That is
	// the observable form of "the block carries what was declared": a module can
	// tell "I did not ask" from "I asked and there was nothing".
	assert.NotContains(t, block, externalcollector.ContextFamilyCode,
		"an undeclared family contributes no key at all")

	require.Len(t, block["aws"][0].Nodes, 2)
	assert.Equal(t, externalcollector.ContextNode{
		ID:         "i-1",
		Type:       "aws-resource",
		SymbolName: "api-server",
		Metadata:   map[string]string{"resource_type": "ec2:instance"},
	}, block["aws"][0].Nodes[0], "exactly the declared fields, and nothing else")

	require.Len(t, block["aws"][0].Edges, 1)
	assert.Equal(t, externalcollector.ContextEdge{FromID: "i-1", ToID: "i-2"}, block["aws"][0].Edges[0])
}

// TestCustomCollect_TheProjectionIsTheDeclaredSliceAndNothingMore is the
// NO-BASELINE arm — the whole of "declared-only". The fetch returns symbol_name
// on every node; an entry that declares `id` alone must receive nodes carrying
// id and NOT symbol_name, or "exactly the declared slice" is untested and a
// projection that passed everything through would look identical.
func TestCustomCollect_TheProjectionIsTheDeclaredSliceAndNothingMore(t *testing.T) {
	url, seen := argEchoProvider(t)
	decl := externalcollector.ContextDeclaration{
		"aws": {
			NodeTypes:  []string{"aws-resource"},
			NodeFields: []string{"id"},
		},
	}
	deps := awsDeps(t, url, decl)

	handled, res := callCollect(deps, `{"type":"jira","id":"board"}`)
	require.True(t, handled)
	require.False(t, res.IsError, resultText(res))

	block, present := seen.contextBlock(t)
	require.True(t, present)
	require.Len(t, block["aws"], 1)
	require.Len(t, block["aws"][0].Nodes, 2)
	assert.Equal(t, externalcollector.ContextNode{ID: "i-1"}, block["aws"][0].Nodes[0],
		"the read carried type, symbol_name and metadata; the declaration named id alone")
	assert.Empty(t, block["aws"][0].Edges,
		"edge_fields was not declared, so no edge read was issued even though the graph holds one")
	assert.Empty(t, deps.graphs.(*contextGraphCaller).edgePivots(),
		"and the cost matches: an undeclared edge set costs no edge RPC at all")
}

// TestCustomCollect_NodeTypesSelectsWhichNodesEnterTheSlice pins the arm that
// makes the graph-names-only declaration a real declaration rather than a
// degenerate one: a family declared EMPTY yields the graph name and NO nodes.
//
// THE DECLARATION IS EMPTY RATHER THAN FIELDS-WITHOUT-TYPES, and the difference
// is the point: asking for node fields with no node type to carry them is
// refused by the validator as incoherent, so the legitimate way to ask for the
// graph names alone is to ask for nothing else.
func TestCustomCollect_NodeTypesSelectsWhichNodesEnterTheSlice(t *testing.T) {
	url, seen := argEchoProvider(t)
	decl := externalcollector.ContextDeclaration{"aws": {}}
	deps := awsDeps(t, url, decl)

	handled, res := callCollect(deps, `{"type":"jira","id":"board"}`)
	require.True(t, handled)
	require.False(t, res.IsError, resultText(res))

	block, present := seen.contextBlock(t)
	require.True(t, present)
	require.Len(t, block["aws"], 1)
	assert.Equal(t, "prod", block["aws"][0].GraphName, "the graph name is always carried")
	assert.Empty(t, block["aws"][0].Nodes, "no node type was declared, so no node enters the slice")
}

// TestCustomCollect_MetadataKeyAbsentFromANodeIsOmitted holds the rule the
// environment allowlist states and this block copies: an absent value and an
// empty one are different inputs, so a declared key the node does not hold is
// OMITTED rather than carried as "".
func TestCustomCollect_MetadataKeyAbsentFromANodeIsOmitted(t *testing.T) {
	url, seen := argEchoProvider(t)
	decl := externalcollector.ContextDeclaration{
		"aws": {
			NodeTypes:    []string{"aws-resource"},
			NodeFields:   []string{"id"},
			MetadataKeys: []string{"resource_type", "clientId"},
		},
	}
	deps := awsDeps(t, url, decl)

	handled, res := callCollect(deps, `{"type":"jira","id":"board"}`)
	require.True(t, handled)
	require.False(t, res.IsError, resultText(res))

	block, present := seen.contextBlock(t)
	require.True(t, present)
	require.Len(t, block["aws"][0].Nodes, 2)
	assert.Equal(t, map[string]string{"resource_type": "ec2:instance"}, block["aws"][0].Nodes[0].Metadata,
		"clientId is absent from this node, so the key is absent from the projection")
	// THE CONTROL for that absence, in the same run and through the same field:
	// the second node DOES hold clientId, so the key is not simply unreachable.
	assert.Equal(t, map[string]string{"resource_type": "ec2:instance", "clientId": "abc-123"},
		block["aws"][0].Nodes[1].Metadata)
	// AND THE THIRD KEY THAT WAS NEVER DECLARED: both nodes carry a description
	// and content the read returned, and neither reaches the block. Without this
	// leg the two assertions above hold for a projection that copied every key
	// the node had and happened to match.
	for _, n := range block["aws"][0].Nodes {
		assert.NotContains(t, n.Metadata, "description")
		assert.Empty(t, n.Content, "content was not declared")
	}
}

// --- R9(a): the block is sent unbounded ---

// TestCustomCollect_ALargeDeclaredBlockIsSentWhole is R9(a) stated as a
// property rather than a promise. NO SIZE ARM EXISTS ON THE FILL PATH, and this
// row is the one that goes red if anyone adds one.
func TestCustomCollect_ALargeDeclaredBlockIsSentWhole(t *testing.T) {
	url, seen := argEchoProvider(t)
	decl := externalcollector.ContextDeclaration{
		"aws": {
			NodeTypes:  []string{"aws-resource"},
			NodeFields: []string{"id", "content"},
		},
	}
	deps := newCustomDeps(t, declaringEntry(url, decl))
	deps.crud = registeredCRUD("aws")
	deps.graphs = bulkResourceCaller(2000, 4096)

	handled, res := callCollect(deps, `{"type":"jira","id":"board"}`)
	require.True(t, handled)
	require.False(t, res.IsError, resultText(res))

	block, present := seen.contextBlock(t)
	require.True(t, present, "a large declared block is sent, not refused")
	require.Len(t, block["aws"], 1)
	assert.Len(t, block["aws"][0].Nodes, 2000, "every declared node arrives; nothing is dropped or truncated")
	assert.Len(t, block["aws"][0].Nodes[1999].Content, 4096, "and each arrives whole")
}

// --- R1: the persisted record is unchanged by a declaration ---

// TestPersistedRecordCarriesNoContextDeclaration is R6's STRUCTURAL half, and it
// is cheaper and sharper than inferring the same fact from an empty proto diff:
// a wire that grew no message can still grow a payload.
func TestPersistedRecordCarriesNoContextDeclaration(t *testing.T) {
	entry := collectorconfig.Entry{
		Type:    collectorconfig.TransportHTTP,
		URL:     "http://example.invalid",
		Tool:    customStubTool,
		Context: resourceDecl(),
	}
	persisted := collectorconfig.Persisted("jira", entry)
	raw, err := json.Marshal(persisted)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "context",
		"the record the server sees carries no trace of the declaration")
	assert.NotContains(t, string(raw), "resource_type")

	// THE CONTROL: the same marshal DOES carry the behavior half, so the absence
	// above is a decision about the declaration rather than an empty record.
	assert.Contains(t, string(raw), "syncable")

	// AND THE RUNTIME RECORD DOES CARRY IT, which is what makes the split real
	// rather than the declaration simply being dropped everywhere.
	assert.Equal(t, resourceDecl(), collectorconfig.Runtime("jira", entry).Context)
}
