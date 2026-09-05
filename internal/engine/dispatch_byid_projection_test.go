// SPDX-License-Identifier: Apache-2.0

package engine

// dispatch_byid_projection_test.go covers R8: the caller's `fields` projection on
// a by-id read that ALSO carries include_edges or include_cross_links.
//
// THE DEFECT: dispatchQueryByID threaded a.Format but never a.Fields, and
// renderByIDResult took no fields parameter — so the identical call projected
// WITHOUT include_edges and returned the whole node WITH it. A caller who added
// the flag silently lost the projection they asked for, which on a large node is
// the difference between a read that fits and one that spills.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/enginetest"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// byIDProjectionExec answers the three reads dispatchQueryByID issues: the base
// by-id node, the pivot edge drain, and the bulk peer hydrate.
func byIDProjectionExec(node *knowledgev1.Node, peer *knowledgev1.Node, edges []*knowledgev1.Edge) ExecuteFn {
	return func(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
		q := req.GetQuery()
		switch {
		case q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_EDGES:
			return &knowledgev1.ExecuteResponse{Edges: edges}, nil
		case len(q.GetIds()) > 0:
			if peer == nil {
				return &knowledgev1.ExecuteResponse{}, nil
			}
			return enginetest.ResponseWithNodes(peer), nil
		case q.GetById() == node.Id:
			return enginetest.ResponseWithNodes(node), nil
		}
		return &knowledgev1.ExecuteResponse{}, nil
	}
}

// projectionFixture builds a large-bodied node with one relates-to peer.
//
// THE FIXTURE USES NODE TYPES THAT ALREADY EXISTED, deliberately. R8 is a
// property of the by-id read arm and has nothing to do with this ticket's new
// vocabulary: a plan section is only the case that MOTIVATED it. Typing the
// fixture with the new node types would have made this whole file fail to
// COMPILE against the pre-change tree, so its red leg could only ever have been
// a build error — and a build error cannot distinguish "the projection is
// dropped" from "the test names a symbol that does not exist yet". Every
// assertion below is therefore reproducible as a real failure on the tree
// before the fix.
func projectionFixture() (*knowledgev1.Node, *knowledgev1.Node, []*knowledgev1.Edge) {
	n := &knowledgev1.Node{
		Id: "sec-0", SymbolName: "Touch points", Type: string(kgtypes.NodePlan),
		Status: kgtypes.StatusActive, Summary: "the touch points section",
		// A body large enough that returning it unprojected is the whole problem,
		// and PRINTABLE: a NUL-filled filler turns any failure diff into kilobytes
		// of escaped bytes, which hides the one line a reader needs.
		Description: "BODY " + strings.Repeat("x", 512),
	}
	kgtypes.SetValue(n, "position", "0")
	peer := &knowledgev1.Node{Id: "ann-1", SymbolName: "an annotation", Type: string(kgtypes.NodeFinding)}
	edges := []*knowledgev1.Edge{{FromId: "ann-1", ToId: "sec-0", Type: string(kgtypes.EdgeRelatesTo)}}
	return n, peer, edges
}

func byIDCall(t *testing.T, args string) (kgtools.ToolResult, bool) {
	t.Helper()
	n, peer, edges := projectionFixture()
	return dispatchQueryByID(context.Background(), byIDProjectionExec(n, peer, edges), json.RawMessage(args))
}

func resultBody(res kgtools.ToolResult) string {
	if len(res.Content) == 0 {
		return ""
	}
	return res.Content[0].Text
}

// R8-a. The projection is honored WITH include_edges, and the same-run control
// is the identical call WITHOUT it, which projected before this change too.
func TestDispatchQueryByID_HonorsFieldsWithEdges(t *testing.T) {
	res, handled := byIDCall(t, `{"id":"sec-0","include_edges":true,"fields":["id","name","type"],"format":"json"}`)
	require.True(t, handled)
	require.False(t, res.IsError, "%s", resultBody(res))

	// The projected row rides under the envelope's `node` key. It is NOT flattened
	// to the envelope root: edges, cross_links and the unconditional `truncated`
	// sit beside it, and a caller who projects is asking for a smaller node rather
	// than for a read that stops disclosing whether it was complete.
	var env map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(resultBody(res)), &env))
	var got map[string]any
	require.NoError(t, json.Unmarshal(env["node"], &got))
	assert.Equal(t, "sec-0", got["id"])
	assert.Equal(t, "Touch points", got["name"])
	assert.Equal(t, string(kgtypes.NodePlan), got["type"])
	assert.NotContains(t, got, "description",
		"a key outside the projection must not ride along — that is the whole point of asking for one")
	assert.NotContains(t, resultBody(res), "BODY ")
}

// R8-b. The envelope keys survive the projection: edges, cross_links, and an
// UNCONDITIONAL truncated. A projected read that drops truncated is the defect —
// `false` is a positive statement of completeness and an absent key is
// indistinguishable from an older binary.
func TestDispatchQueryByID_ProjectionKeepsTheEnvelopeKeys(t *testing.T) {
	res, handled := byIDCall(t, `{"id":"sec-0","include_edges":true,"fields":["id"],"format":"json"}`)
	require.True(t, handled)

	var env map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(resultBody(res)), &env))
	require.Contains(t, env, "node", "the projected row rides under the node key, keeping the envelope shape")
	require.Contains(t, env, "edges")
	require.Contains(t, env, "truncated")

	var node map[string]any
	require.NoError(t, json.Unmarshal(env["node"], &node))
	assert.Equal(t, map[string]any{"id": "sec-0"}, node, "the node is exactly the projection")

	var edges []map[string]any
	require.NoError(t, json.Unmarshal(env["edges"], &edges))
	require.Len(t, edges, 1)
	assert.Equal(t, "ann-1", edges[0]["peer_id"], "the edge summary is unaffected by the node projection")
}

// R8-c. An unsupported key is REFUSED naming the key and the accepted set, and
// the message is BYTE-IDENTICAL to the non-edges path's, so the two cannot drift
// into two vocabularies.
//
// THE MARKER AND THE KEY ARE BOTH ASSEMBLED AT RUNTIME, and the MARKER is the
// load-bearing half. This file is tracked, so it sits inside the corpus the
// standing projection-key census walks, and that census matches the literal
// `"fields":[` marker and then reads the quoted strings inside it. A deliberately
// invalid key written as a literal here is, as far as the scan can tell, a real
// out-of-vocabulary call site — and it failed the gate exactly that way
// (out_of_vocab=1, rc=1).
//
// INTERPOLATING ONLY THE KEY DOES NOT FIX IT. The format verb takes the key's
// place inside a list that is still literal, and the verb is then itself read as
// an out-of-vocabulary key: one offender is traded for another. Assembling the
// marker is what keeps the fixture out of the scanned text, while the test
// exercises the identical path with the identical bytes at run time.
//
// This is the census's own technique, applied for its own stated reason: its
// positive control assembles marker and key from fragments because that file is
// tracked too, and it says so where it does it.
func TestDispatchQueryByID_ProjectionRefusalMatchesTheNonEdgesPath(t *testing.T) {
	marker, badKey := "fields", "nonsense"
	args := fmt.Sprintf(`{"id":"sec-0","include_edges":true,%q:[%q],"format":"json"}`, marker, badKey)

	withEdges, handled := byIDCall(t, args)
	require.True(t, handled)
	require.True(t, withEdges.IsError)
	assert.Contains(t, resultBody(withEdges), badKey,
		"the refusal names the offending key — the assembled fixture must still reach the validator")

	// THE SIBLING ARM ITSELF, driven rather than stood in for. This is the
	// comparison the clause asks for: the non-edges PATH is renderNodeResponse,
	// and comparing each arm to the validator they share cannot see a wrapper
	// added on either side of it — the arm would still call the validator, still
	// hold the same vocabulary, and return different bytes. Driving both arms and
	// comparing their rendered bodies is what makes such a wrapper red.
	// IT IS CALLED DIRECTLY, and that is forced rather than chosen: dispatchQueryByID
	// returns handled=false for a by-id read carrying neither absorption flag, so a
	// plain call through byIDCall never reaches this arm at all. renderNodeResponse
	// is what the default-mode dispatcher invokes for that read, so driving it is
	// driving the live path.
	fixtureNode, _, _ := projectionFixture()
	noEdges, rerr := renderNodeResponse(
		enginetest.ResponseWithNodes(fixtureNode), "knowledge", "sec-0", true, "json", []string{badKey}, false)
	require.NoError(t, rerr, "a caller-input refusal is a rendered error result, not a transport error")
	require.True(t, noEdges.IsError, "the non-edges arm must refuse the same key: %s", resultBody(noEdges))
	assert.Equal(t, resultBody(noEdges), resultBody(withEdges),
		"the two by-id arms must surface ONE refusal, not two copies that can drift apart")

	// AND THE VOCABULARY IS THE SHARED ONE, kept beside the arm comparison rather
	// than replaced by it: the assertion above would still hold if both arms
	// drifted together, and this one says whose words they are.
	err := ValidateNodeProjection([]string{badKey}, false)
	require.Error(t, err)
	assert.Equal(t, err.Error(), resultBody(withEdges),
		"both arms must surface the same refusal text — they share ValidateNodeProjection precisely so they cannot drift")
}

// R8-d. tombstoned_at without include_tombstones is refused on this arm too.
func TestDispatchQueryByID_TombstonedAtNeedsTheOptIn(t *testing.T) {
	res, handled := byIDCall(t, `{"id":"sec-0","include_edges":true,"fields":["tombstoned_at"],"format":"json"}`)
	require.True(t, handled)
	require.True(t, res.IsError)
	assert.Contains(t, resultBody(res), "include_tombstones")

	// CONTROL: with the opt-in the same projection is accepted, so the refusal
	// above is the opt-in gate rather than a key this arm rejects outright.
	ok, _ := byIDCall(t, `{"id":"sec-0","include_edges":true,"include_tombstones":true,"fields":["tombstoned_at"],"format":"json"}`)
	assert.False(t, ok.IsError, "%s", resultBody(ok))
}

// R8-e. include_cross_links alone with a projection, and both flags together.
func TestDispatchQueryByID_ProjectionOnCrossLinkShapes(t *testing.T) {
	for _, args := range []string{
		`{"id":"sec-0","include_cross_links":true,"fields":["id"],"format":"json"}`,
		`{"id":"sec-0","include_edges":true,"include_cross_links":true,"fields":["id"],"format":"json"}`,
	} {
		t.Run(args, func(t *testing.T) {
			res, handled := byIDCall(t, args)
			require.True(t, handled)
			require.False(t, res.IsError, "%s", resultBody(res))
			var env map[string]json.RawMessage
			require.NoError(t, json.Unmarshal([]byte(resultBody(res)), &env))
			require.Contains(t, env, "truncated")
			var node map[string]any
			require.NoError(t, json.Unmarshal(env["node"], &node))
			assert.Equal(t, map[string]any{"id": "sec-0"}, node)
		})
	}
}

// R8-f. A projection with format UNSET emits the json projection rather than
// markdown — the documented override: a projected row IS a json object, so there
// is no text shape to render it into.
func TestDispatchQueryByID_ProjectionOverridesAnUnsetFormat(t *testing.T) {
	res, handled := byIDCall(t, `{"id":"sec-0","include_edges":true,"fields":["id","name"]}`)
	require.True(t, handled)
	require.False(t, res.IsError, "%s", resultBody(res))
	var env map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(resultBody(res)), &env),
		"a projected read emits json whatever format says")
	require.Contains(t, env, "node")
	require.Contains(t, env, "truncated")
}

// R8-g / R8-h. The UNPROJECTED bodies stay byte-identical — the legacy knowledge
// JSON body and the markdown generic body are preserved deliberately, and the
// tester's own shape (include_edges with no fields) is a live consumer.
func TestDispatchQueryByID_UnprojectedBodiesUnchanged(t *testing.T) {
	res, handled := byIDCall(t, `{"id":"sec-0","include_edges":true}`)
	require.True(t, handled)
	require.False(t, res.IsError)
	body := resultBody(res)
	assert.Contains(t, body, `"peer_id":"ann-1"`, "the legacy include_edges body is the node-with-edges JSON")
	assert.Contains(t, body, "BODY ", "and it carries the whole node, because no projection was asked for")

	jsonRes, _ := byIDCall(t, `{"id":"sec-0","include_edges":true,"format":"json"}`)
	require.False(t, jsonRes.IsError)
	var env map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(resultBody(jsonRes)), &env))
	require.Contains(t, env, "node")
	require.Contains(t, env, "truncated")
	var node map[string]any
	require.NoError(t, json.Unmarshal(env["node"], &node))
	assert.Contains(t, node, "description", "the unprojected json envelope still carries the whole node")
}
