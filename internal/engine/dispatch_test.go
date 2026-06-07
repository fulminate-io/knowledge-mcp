// SPDX-License-Identifier: Apache-2.0

package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"syscall"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/enginetest"
	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
)

// dispatchCounters records exec invocation counts for the Dispatch contract
// assertions. The legacy fallback CallFn was removed — there is no fallback
// path left to count, so the struct tracks exec calls only.
type dispatchCounters struct {
	execCalls int
}

func (d *dispatchCounters) exec(resp *knowledgev1.ExecuteResponse, err error) ExecuteFn {
	return func(_ context.Context, _ *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
		d.execCalls++
		return resp, err
	}
}

// TestDispatch_DenyOnOkFalse asserts the deny flip: a shape Compile does
// NOT recognize (here a specialized query mode) is DENIED with an explicit
// error naming the tool — exec NEVER runs, and there is no legacy fallback wire
// to forward to. This inverts the pre-cutover fall-through contract:
// the legacy ToolService.Call fallback is gone, so an uncompilable
// shape that reaches Dispatch is a genuine unrecognized request.
func TestDispatch_DenyOnOkFalse(t *testing.T) {
	d := &dispatchCounters{}
	out, err := Dispatch(context.Background(),
		d.exec(nil, errors.New("exec must not run")),
		"query", json.RawMessage(`{"mode":"stats"}`))
	require.NoError(t, err, "a deny is rendered as an error result, not returned as a Go error")
	assert.True(t, out.IsError, "an uncompilable shape is denied (IsError)")
	assert.Contains(t, out.Content[0].Text, "query", "the deny message names the offending tool")
	assert.Contains(t, out.Content[0].Text, "denied", "the deny message is legible")
	assert.Equal(t, 0, d.execCalls, "exec must NEVER run for a denied shape")
}

// TestDispatch_ExecOnceThenRenderOnOkTrue asserts a reducible shape calls exec
// EXACTLY ONCE then renders the response.
func TestDispatch_ExecOnceThenRenderOnOkTrue(t *testing.T) {
	d := &dispatchCounters{}
	// Canned search response so Render produces real output.
	results := []SearchResult{
		{Score: 0.9, Node: &knowledgev1.Node{Id: "n1", Type: "finding", SymbolName: "Hit"}},
	}
	resp := enginetest.SearchResponseWith(searchResultsToProtoForTest(results)...)

	out, derr := Dispatch(context.Background(),
		d.exec(resp, nil),
		"search", json.RawMessage(`{"query":"x","graph":"knowledge"}`))
	require.NoError(t, derr)
	assert.Equal(t, 1, d.execCalls, "exec runs EXACTLY once (bounded-constant)")
	assert.Contains(t, out.Content[0].Text, "[finding] Hit")
}

// TestDispatch_MapsInvalidArgument asserts a CodeInvalidArgument engine error is
// rendered as the validation message (not returned as a Go error). Uses a by-id
// update so the engine-error-rendering path is exercised: exec runs once and
// returns the CodeInvalidArgument, which Dispatch renders verbatim.
func TestDispatch_MapsInvalidArgument(t *testing.T) {
	d := &dispatchCounters{}
	engineErr := connect.NewError(connect.CodeInvalidArgument, errors.New("unknown set_fields key"))
	out, err := Dispatch(context.Background(),
		d.exec(nil, engineErr),
		"mutate", json.RawMessage(`{"operation":"update","id":"n1","status":"closed"}`))
	require.NoError(t, err, "engine validation error is rendered, not returned")
	assert.True(t, out.IsError)
	assert.Contains(t, out.Content[0].Text, "unknown set_fields key")
	assert.Equal(t, 1, d.execCalls)
}

// TestDispatch_CreateValidationErrorRelayedFromServer asserts the
// contract: with the client precheck deleted, a create that violates a
// create-body validator (type=document with no summary — an embed-only type that
// requires a Summary) is NO LONGER rejected client-side. It flows to Execute
// EXACTLY ONCE, the server engine's decodeCreate rejects it with a
// CodeInvalidArgument invalidMutation, and renderEngineError relays that message
// to the LLM verbatim (out.IsError, "summary is required"). This is the inverse
// of the prior zero-exec client-precheck test — it mirrors
// TestDispatch_MapsNotFound (which stubs a CodeNotFound exec error and asserts
// the relayed text).
func TestDispatch_CreateValidationErrorRelayedFromServer(t *testing.T) {
	d := &dispatchCounters{}
	// The server's invalidMutation surfaces across the wire as a
	// CodeInvalidArgument carrying the "summary is required" message.
	serverErr := connect.NewError(connect.CodeInvalidArgument,
		errors.New("planToMutation: mutate(create): summary is required and must be non-empty (search-optimized one-line summary)"))
	out, err := Dispatch(context.Background(),
		d.exec(nil, serverErr),
		"mutate", json.RawMessage(`{"operation":"create","type":"document","name":"X"}`))
	require.NoError(t, err, "the server validation error is rendered, not returned")
	assert.True(t, out.IsError, "the server validation failure surfaces as an error result")
	assert.Contains(t, out.Content[0].Text, "summary is required",
		"the server's mutate(create) summary-required message is relayed verbatim")
	assert.Equal(t, 1, d.execCalls, "the create flows to Execute EXACTLY once — validation is server-side now")
}

// TestDispatch_MapsNotFound asserts CodeNotFound surfaces as the not-found text.
func TestDispatch_MapsNotFound(t *testing.T) {
	d := &dispatchCounters{}
	engineErr := connect.NewError(connect.CodeNotFound, errors.New("node missing not found"))
	out, err := Dispatch(context.Background(),
		d.exec(nil, engineErr),
		"query", json.RawMessage(`{"id":"missing"}`))
	require.NoError(t, err)
	assert.True(t, out.IsError)
	assert.Contains(t, out.Content[0].Text, "node missing not found")
}

// TestDispatch_RenderQueryIDBareNode asserts the knowledge query-id arm renders
// a bare JSON node (handleGetNode shape — MarshalIndent), NOT {nodes:[]} and NOT
// the generic markdown.
func TestDispatch_RenderQueryIDBareNode(t *testing.T) {
	d := &dispatchCounters{}
	resp := enginetest.ResponseWithNode(&knowledgev1.Node{Id: "n1", SymbolName: "Doc", Type: "document"})
	out, derr := Dispatch(context.Background(),
		d.exec(resp, nil),
		"query", json.RawMessage(`{"id":"n1"}`))
	require.NoError(t, derr)
	text := out.Content[0].Text
	// Knowledge graph (graph absent → knowledge) → JSON node, not {nodes:[]}.
	var decoded knowledgev1.Node
	require.NoError(t, json.Unmarshal([]byte(text), &decoded))
	assert.Equal(t, "n1", decoded.Id)
	assert.Equal(t, "Doc", decoded.SymbolName)
}

// TestDispatch_RenderQueryIDsBulk asserts the ids[]-bulk arm renders {nodes:[]}.
func TestDispatch_RenderQueryIDsBulk(t *testing.T) {
	d := &dispatchCounters{}
	resp := enginetest.ResponseWithNodes(
		&knowledgev1.Node{Id: "a", SymbolName: "A"},
		&knowledgev1.Node{Id: "b", SymbolName: "B"},
	)
	out, derr := Dispatch(context.Background(),
		d.exec(resp, nil),
		"query", json.RawMessage(`{"ids":["a","b"]}`))
	require.NoError(t, derr)
	assert.Contains(t, out.Content[0].Text, `"label":"knowledge"`)
	assert.Contains(t, out.Content[0].Text, `"nodes"`)
}

// seqExec returns an ExecuteFn that hands back a different canned response per
// call (in order), recording the per-call request shape. It backs the
// bounded-constant Execute-count assertions on the dispatchQueryByID
// orchestration (exactly 3 Execute calls).
type seqExec struct {
	responses []*knowledgev1.ExecuteResponse
	reqs      []*knowledgev1.ExecuteRequest
	calls     int
}

func (s *seqExec) fn() ExecuteFn {
	return func(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
		s.reqs = append(s.reqs, req)
		i := s.calls
		s.calls++
		if i < len(s.responses) {
			return s.responses[i], nil
		}
		return &knowledgev1.ExecuteResponse{}, nil
	}
}

// TestDispatch_IncludeEdgesThreeExecCalls pins the
// bounded-constant invariant for query(id, include_edges): the client composes
// the edge summary in EXACTLY 3 Execute calls — (1) the bare node read, (2) the
// RETURN_MODE_EDGES read for raw edges, (3) ONE bulk ids[] peer hydrate — NOT a
// per-peer 2+N round-trip. The third call's plan carries the union of peer IDs
// in a single Ids[] field.
func TestDispatch_IncludeEdgesThreeExecCalls(t *testing.T) {
	nodeResp := enginetest.ResponseWithNode(&knowledgev1.Node{Id: "n1", SymbolName: "Hub", Type: "plan"})
	// Two raw edges → two distinct peers a, b (both directions).
	edgeProtos := edgesToProtoForTest([]knowledgev1.Edge{
		{FromId: "n1", ToId: "a", Type: "contains"},
		{FromId: "b", ToId: "n1", Type: "informed-by"},
	})
	peerResp := enginetest.ResponseWithNodes(
		&knowledgev1.Node{Id: "a", SymbolName: "Alpha", Type: "phase"},
		&knowledgev1.Node{Id: "b", SymbolName: "Beta", Type: "decision"},
	)

	s := &seqExec{responses: []*knowledgev1.ExecuteResponse{
		nodeResp,            // (1) bare node
		{Edges: edgeProtos}, // (2) RETURN_MODE_EDGES raw edges
		peerResp,            // (3) bulk peer hydrate
	}}

	out, err := Dispatch(context.Background(),
		s.fn(),
		"query", json.RawMessage(`{"id":"n1","include_edges":true,"graph":"knowledge"}`))
	require.NoError(t, err)

	assert.Equal(t, 3, s.calls, "include_edges composes in EXACTLY 3 Execute calls (no per-peer N+1)")
	// Call 2 is the RETURN_MODE_EDGES read.
	require.GreaterOrEqual(t, len(s.reqs), 3)
	assert.Equal(t, knowledgev1.ReturnMode_RETURN_MODE_EDGES, s.reqs[1].GetQuery().GetReturnMode(),
		"call 2 is the raw-edges read")
	// Call 3 is the SINGLE bulk peer hydrate carrying BOTH peer IDs in one Ids[].
	assert.ElementsMatch(t, []string{"a", "b"}, s.reqs[2].GetQuery().GetIds(),
		"call 3 bulk-hydrates BOTH peers in ONE Ids[] field (no N+1)")

	// The rendered knowledge node carries the edges (NodeWithEdges JSON shape).
	assert.Contains(t, out.Content[0].Text, `"edges"`)
	assert.Contains(t, out.Content[0].Text, "Alpha")
	assert.Contains(t, out.Content[0].Text, "Beta")
}

// TestDispatch_RenderMutateCreateIDs asserts the mutate-create arm renders the
// created IDs from ExecuteResponse.Ids. A valid embed-only create (type=document
// WITH summary + name) flows straight to Execute EXACTLY once and renders the
// created id — the exec-once assertion folds in the coverage of the former
// TestDispatch_PrecheckCreateValidPasses, which became behaviorally identical
// once the client precheck was deleted.
func TestDispatch_RenderMutateCreateIDs(t *testing.T) {
	d := &dispatchCounters{}
	resp := &knowledgev1.ExecuteResponse{Ids: []string{"created-1"}}
	out, err := Dispatch(context.Background(),
		d.exec(resp, nil),
		"mutate", json.RawMessage(`{"operation":"create","type":"document","name":"X","summary":"s"}`))
	require.NoError(t, err)
	assert.Contains(t, out.Content[0].Text, "Created → ID: created-1")
	assert.Equal(t, 1, d.execCalls, "a valid create flows to Execute EXACTLY once")
}

// TestDispatch_RenderDeleteTool drives the LLM-facing standalone `delete` tool
// END-TO-END through engine.Dispatch (NOT engine.Compile in isolation): a
// by-ids delete compiles (ok=true), exec runs EXACTLY once, and the engine.Render
// `case "delete"` arm renders the affected-count line "Deleted 3 node(s)"
// (renderMutationResponse with MUTATION_KIND_DELETE → mutationVerb "Deleted").
// This is the T2-1 regression guard: on the pre-fix tree Render had no "delete"
// arm so the default fired ("Render: unrenderable tool \"delete\"", IsError true)
// — a guard the Compile-only tests cannot catch because they bypass Dispatch/Render.
func TestDispatch_RenderDeleteTool(t *testing.T) {
	d := &dispatchCounters{}
	resp := &knowledgev1.ExecuteResponse{AffectedCount: 3}
	out, err := Dispatch(context.Background(),
		d.exec(resp, nil),
		"delete", json.RawMessage(`{"ids":["a","b","c"]}`))
	require.NoError(t, err)
	assert.False(t, out.IsError, "the standalone delete tool renders cleanly (not the unrenderable-tool error)")
	assert.Equal(t, "Deleted 3 node(s)", out.Content[0].Text)
	assert.Equal(t, 1, d.execCalls, "the delete tool compiles → exec runs EXACTLY once")
}

// TestDispatch_BareThoughtCreate_NoSummaryGate is the regression guard for the
// thought/charge carve-out (the mirror-inverse of
// TestDispatch_CreateValidationErrorRelayedFromServer, which proves a
// type:document w/o summary DOES surface "summary is required"). A bare direct
// LLM thought create with only content drives END-TO-END through Dispatch: the
// client no longer prechecks (the client precheck was deleted), Compile
// lowers it to a CREATE, exec runs EXACTLY once, and the render is
// "Created → ID: t-1" with NO "summary is required". The carve-out itself now
// lives SERVER-side (decodeCreate returns nil for type==thought before any
// summary gating); this client-side test proves the client adds NO gate of its
// own — a thought create flows through unrejected and renders cleanly.
func TestDispatch_BareThoughtCreate_NoSummaryGate(t *testing.T) {
	d := &dispatchCounters{}
	resp := &knowledgev1.ExecuteResponse{Ids: []string{"t-1"}}
	out, err := Dispatch(context.Background(),
		d.exec(resp, nil),
		"mutate", json.RawMessage(`{"operation":"create","type":"thought","content":"x"}`))
	require.NoError(t, err)
	assert.False(t, out.IsError, "a bare thought create must NOT be summary-gated")
	assert.Contains(t, out.Content[0].Text, "Created → ID: t-1")
	assert.NotContains(t, out.Content[0].Text, "summary is required")
	assert.Equal(t, 1, d.execCalls, "bare thought create compiles → exec runs once")
}

// TestDispatch_BareChargeCreate_NoSummaryGate is the type:charge sibling of the
// thought guard above — a bare charge create with content + no summary flows
// through Dispatch unrejected (no client gate) and renders the CREATE; the
// server-side carve-out admits it.
func TestDispatch_BareChargeCreate_NoSummaryGate(t *testing.T) {
	d := &dispatchCounters{}
	resp := &knowledgev1.ExecuteResponse{Ids: []string{"c-1"}}
	out, err := Dispatch(context.Background(),
		d.exec(resp, nil),
		"mutate", json.RawMessage(`{"operation":"create","type":"charge","content":"x"}`))
	require.NoError(t, err)
	assert.False(t, out.IsError, "a bare charge create must NOT be summary-gated")
	assert.Contains(t, out.Content[0].Text, "Created → ID: c-1")
	assert.NotContains(t, out.Content[0].Text, "summary is required")
	assert.Equal(t, 1, d.execCalls)
}

// TestDispatch_PrecheckQueryEmptyTextRequiresText is the GAP-B regression guard
// (CEO decision: REQUIRE TEXT). The text-required query mode (text) with EMPTY
// text used to fall through to the GENERIC post-cutover deny ("tool query is not a
// recognized engine-reducible shape"). The precheckQuery seam now intercepts it
// BEFORE Compile and returns the SPECIFIC requires-text validation error naming
// the mode — exec NEVER runs (bounded-constant: 0). This exercises the path
// END-TO-END through Dispatch, not precheckQuery in isolation, so the suite catches
// a future regression where the seam stops being invoked. (graph_reach surfaces
// the unknown-mode deny. recent is served entirely client-side and never reaches
// this engine path — both arms: text-bearing recent via composeKnowledgeSearch and
// bare empty-text recent via composeRecentBrowse, in intercept_search_knowledge.go.
// recent is not in reducibleTextRequiredModes — only "text" is — so it is not in
// this test's loop.)
func TestDispatch_PrecheckQueryEmptyTextRequiresText(t *testing.T) {
	for _, mode := range []string{"text"} {
		t.Run(mode, func(t *testing.T) {
			d := &dispatchCounters{}
			args := `{"mode":"` + mode + `"}` // no text field → empty.
			out, err := Dispatch(context.Background(),
				d.exec(nil, errors.New("exec must not run — precheck rejects empty text")),
				"query", json.RawMessage(args))
			require.NoError(t, err, "the requires-text validation error is rendered, not returned")
			assert.True(t, out.IsError, "empty-text %s is a validation failure (IsError)", mode)
			assert.Contains(t, out.Content[0].Text,
				`query mode "`+mode+`" requires a non-empty text query`,
				"the requires-text message names the mode and is legible")
			assert.NotContains(t, out.Content[0].Text, "not a recognized engine-reducible shape",
				"the LEGIBLE requires-text message replaces the generic deny")
			assert.Equal(t, 0, d.execCalls, "precheck failure issues NO Execute RPC (bounded-constant: 0)")
		})
	}
}

// TestDispatch_RendersNoBackend_AndUnreachable is the guard
// for the dispatcher's two new render branches in renderEngineError:
//
//   - graphclient.ErrNoBackend (bare or wrapped) → "no backend available"
//     message with the actionable `knowledge install` / `knowledge login` hints.
//   - Local-server-unreachable transport error (connect CodeUnavailable OR a
//     wrapped syscall.ECONNREFUSED) → "local server unreachable" message with
//     the `knowledge start` / `knowledge login` hints.
//
// Drives the path END-TO-END through Dispatch so the assertion catches a
// future regression where the renderEngineError branches stop being invoked.
// In every row exec runs EXACTLY ONCE (the engine call surfaces the error).
func TestDispatch_RendersNoBackend_AndUnreachable(t *testing.T) {
	cases := []struct {
		name        string
		err         error
		wantSubstrs []string
		notSubstrs  []string
	}{
		{
			name: "bare ErrNoBackend",
			err:  graphclient.ErrNoBackend,
			wantSubstrs: []string{
				"no backend available",
				"knowledge install",
				"knowledge login",
			},
			notSubstrs: []string{"engine:", "connect:"},
		},
		{
			name: "wrapped ErrNoBackend",
			err:  fmt.Errorf("router: %w", graphclient.ErrNoBackend),
			wantSubstrs: []string{
				"no backend available",
				"knowledge install",
				"knowledge login",
			},
			notSubstrs: []string{"engine:", "connect:"},
		},
		{
			name: "connect CodeUnavailable",
			err:  connect.NewError(connect.CodeUnavailable, errors.New("transport: dial 127.0.0.1:15022: connect: connection refused")),
			wantSubstrs: []string{
				"local server unreachable",
				"knowledge start",
				"knowledge login",
			},
			notSubstrs: []string{"engine:", "connect: connection refused"},
		},
		{
			name: "wrapped ECONNREFUSED via net.OpError",
			err:  &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED},
			wantSubstrs: []string{
				"local server unreachable",
				"knowledge start",
				"knowledge login",
			},
			notSubstrs: []string{"engine:", "dial tcp"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := &dispatchCounters{}
			out, err := Dispatch(context.Background(),
				d.exec(nil, tc.err),
				"search", json.RawMessage(`{"query":"x","graph":"knowledge"}`))
			require.NoError(t, err, "transport error is rendered, not returned")
			assert.True(t, out.IsError, "transport error surfaces as an error result")
			for _, want := range tc.wantSubstrs {
				assert.Contains(t, out.Content[0].Text, want,
					"%s: missing substring %q in rendered text %q", tc.name, want, out.Content[0].Text)
			}
			for _, notWant := range tc.notSubstrs {
				assert.NotContains(t, out.Content[0].Text, notWant,
					"%s: leaked raw text %q in rendered text %q", tc.name, notWant, out.Content[0].Text)
			}
			assert.Equal(t, 1, d.execCalls, "exec runs EXACTLY once (the engine call surfaces the error)")
		})
	}
}

// TestDispatch_PrecheckQueryNonTextRequiredModesUngated asserts the precheckQuery
// seam is SCOPED: it gates ONLY the text-required modes. An empty-text "modules"
// (catalog enumeration, no text input) compiles and runs; an empty-text default
// mode with a real shape (a type-browse) compiles and runs; and a text-required
// mode WITH text passes the precheck and runs. None surface the requires-text
// error — the seam is a gate on the empty-text-required violation only, not a
// blanket interception.
func TestDispatch_PrecheckQueryNonTextRequiredModesUngated(t *testing.T) {
	cases := []struct {
		name string
		args string
	}{
		{"modules empty text", `{"mode":"modules","graph":"code","repo":"r"}`},
		{"default type-browse empty text", `{"type":"finding"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := &dispatchCounters{}
			resp := enginetest.ResponseWithNodes()
			out, err := Dispatch(context.Background(),
				d.exec(resp, nil),
				"query", json.RawMessage(tc.args))
			require.NoError(t, err)
			assert.NotContains(t, out.Content[0].Text, "requires a non-empty text query",
				"%s must NOT trip the requires-text precheck", tc.name)
			assert.Equal(t, 1, d.execCalls, "%s compiles past the precheck → exec runs once", tc.name)
		})
	}
}
