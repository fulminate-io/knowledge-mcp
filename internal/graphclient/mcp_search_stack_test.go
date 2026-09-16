// SPDX-License-Identifier: Apache-2.0

package graphclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/auth"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// mcp_search_stack_test.go holds the harness that drives tool calls over the
// real MCP HTTP endpoint. Split out of router_test_helpers_test.go for the
// repo's file-length cap; same package, nothing but the location changed.

// mcpSearchStackPort is the port the EnsureServer message names. Nothing binds
// it: it is deliberately not 15022 or 15023, so no test can be read as having
// contacted the developer's own daemon or server.
const mcpSearchStackPort = 14901

// mcpSearchStack drives tool calls over the REAL MCP HTTP endpoint — the
// JSON-RPC mux an MCP client posts to — against a real Router with a counting
// engine behind each storage leg. A search reaches the BindSearch preflight
// only through that endpoint, so this is the harness that observes what a user
// observes: the preflight, the storage binding, the EnsureServer gate and the
// dispatch, end to end.
type mcpSearchStack struct {
	endpoint  string
	session   string
	local     *countingEngine
	cloud     *countingEngine
	byAccount *accountRoutedEngine
	stopLocal func()

	// accountHeader is what every tool call of this view sends as the inbound
	// account header: nil sends none at all, and each element is Added, so a
	// case can drive the present-but-empty and sent-twice classes as well as a
	// well-formed id.
	accountHeader []string
}

// as returns a VIEW of the stack whose tool calls carry the inbound account
// header with the given values — the same server and the same MCP session, so a
// test can alternate header-bound and unbound calls on one connection the way a
// browser page does.
func (s *mcpSearchStack) as(values ...string) *mcpSearchStack {
	view := *s
	view.accountHeader = values
	return &view
}

// newSignedInMCPSearchStack is the cloud user who also runs a local store: a
// selected cloud account, machine auth, and a healthy engine on each leg.
func newSignedInMCPSearchStack(t *testing.T) *mcpSearchStack {
	t.Helper()
	selectCloudAccountForTest(t, "probe-account")
	localURL, local, stopLocal := startHealthyCountingEngine(t)
	cloudURL, cloud, _ := startHealthyCountingEngine(t)
	localClient := closeIdleOnCleanup(t, NewGraphClientForURL(localURL))
	r := NewRouterWithMachineAuth(localClient, cloudURL, auth.StaticTokenSource{}, nil, true)
	t.Cleanup(r.Close)
	return newMCPSearchStack(t, r, localClient, local, cloud, stopLocal)
}

// newAccountRoutedMCPSearchStack is the signed-in fixture whose CLOUD leg is
// the account-routed endpoint: it counts Execute by the account the request
// stamped, so a test reads WHICH account a header-bound call was answered for
// rather than only what the daemon reported about itself. The selection is
// accountA for every case that uses it — the account a header-bound request is
// NOT for — so the fixture names it rather than taking it as a parameter.
func newAccountRoutedMCPSearchStack(t *testing.T) *mcpSearchStack {
	t.Helper()
	selectCloudAccountForTest(t, accountA)
	localURL, local, stopLocal := startHealthyCountingEngine(t)
	cloudURL, routed := startAccountRoutedEngine(t)
	localClient := closeIdleOnCleanup(t, NewGraphClientForURL(localURL))
	r := NewRouterWithMachineAuth(localClient, cloudURL, auth.StaticTokenSource{}, nil, true)
	t.Cleanup(r.Close)
	s := newMCPSearchStack(t, r, localClient, local, nil, stopLocal)
	s.byAccount = routed
	return s
}

// newLoggedOutMCPSearchStack is the OSS client: no machine auth, no auth state
// and no cloud URL, so Router.LoggedIn is false and every op is local.
func newLoggedOutMCPSearchStack(t *testing.T) *mcpSearchStack {
	t.Helper()
	localURL, local, stopLocal := startHealthyCountingEngine(t)
	localClient := closeIdleOnCleanup(t, NewGraphClientForURL(localURL))
	r := NewRouterWithMachineAuth(localClient, "", auth.StaticTokenSource{}, nil, false)
	t.Cleanup(r.Close)
	return newMCPSearchStack(t, r, localClient, local, nil, stopLocal)
}

func newMCPSearchStack(
	t *testing.T,
	r *Router,
	localClient *GraphClient,
	local, cloud *countingEngine,
	stopLocal func(),
) *mcpSearchStack {
	t.Helper()
	m := NewMCPClient(MCPClientConfig{
		BindStorage: r.BindStorage,
		BindSearch:  r.BindSearch,
		Client:      localClient,
		Port:        mcpSearchStackPort,
		Version:     "test",
		LoggedIn:    r.LoggedIn,
		Dispatch: func(ctx context.Context, tool string, _ json.RawMessage) (kgtools.ToolResult, error) {
			destination, _ := StorageDestination(ctx)
			// THE CATALOG READ'S ANSWER IS SURFACED, not discarded. It is the
			// read production resolves a graph's embed identity from
			// (tools/query_embed_identity.go via fetchGraphNamesOfType), so
			// rendering what came back is how a test over this endpoint can
			// observe that the identity arrived — and from which leg.
			resp, err := r.Execute(ctx, graphNamesQuery())
			if err != nil {
				return kgtools.ToolResult{}, err
			}
			accountID, accountSource, accountErr := auth.RequestAccount(ctx)
			account, source := accountID, string(accountSource)
			reason := RequestAccountReason(ctx)
			if accountErr != nil {
				account, source, reason = "", AccountSourceNone, accountErr.Error()
			}
			// The reference is built the way every production kgref producer
			// builds one — from the bound destination, after BindStorage — so a
			// test reading it is reading whether the binding reached a producer,
			// not whether this stub remembered the account.
			ref, refErr := Reference{Destination: destination, Graph: "knowledge", ID: "n1"}.Encode()
			if refErr != nil {
				ref = "(unencodable: " + refErr.Error() + ")"
			}
			return kgtools.TextResult(fmt.Sprintf(
				"OK tool=%s destination=%+v search_destinations=%+v embed_identities=%s account=%s account_source=%s account_reason=%s ref=%s",
				tool, destination, SearchDestinations(ctx),
				renderEmbedIdentities(resp.GetGraphNames()), account, source, reason, ref,
			)), nil
		},
	})
	srv := httptest.NewServer(NewHTTPServer(m, 0, nil).mux())
	t.Cleanup(srv.Close)
	s := &mcpSearchStack{endpoint: srv.URL + "/mcp", local: local, cloud: cloud, stopLocal: stopLocal}
	s.session = s.initialize(t)
	return s
}

// renderEmbedIdentities renders a catalog's embed identities in a shape whose
// EMPTINESS IS EXPLICIT: an empty list renders "[]" rather than nothing, so a
// test asserting that no identity arrived is reading a rendered absence rather
// than matching against a blank the request itself could have produced.
func renderEmbedIdentities(infos []*knowledgev1.GraphInfo) string {
	parts := make([]string, 0, len(infos))
	for _, gi := range infos {
		id := gi.GetEmbedIdentity()
		if id == nil {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s=%s/%s/%d/%s",
			gi.GetName(), id.GetProvider(), id.GetModel(), id.GetDimension(), id.GetDtype()))
	}
	return "[" + strings.Join(parts, " ") + "]"
}

func (s *mcpSearchStack) post(t *testing.T, body map[string]any, session string) (string, http.Header) {
	t.Helper()
	out, header, _ := s.postStatus(t, body, session)
	return out, header
}

// postStatus is post with the HTTP status code, which a transport-level refusal
// (a malformed account header) is reported as rather than as a JSON-RPC body.
func (s *mcpSearchStack) postStatus(t *testing.T, body map[string]any, session string) (string, http.Header, int) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, s.endpoint, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if session != "" {
		req.Header.Set("Mcp-Session-Id", session)
	}
	for _, value := range s.accountHeader {
		req.Header.Add(auth.AccountHeaderName, value)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(out), resp.Header, resp.StatusCode
}

// initialize performs the MCP handshake and returns the minted session id,
// which every later tools/call must carry.
func (s *mcpSearchStack) initialize(t *testing.T) string {
	t.Helper()
	_, header := s.post(t, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{
			"protocolVersion": "2025-06-18",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "test", "version": "1"},
		},
	}, "")
	session := header.Get("Mcp-Session-Id")
	if session == "" {
		t.Fatal("initialize minted no Mcp-Session-Id")
	}
	return session
}

// callResult drives a real tools/call and returns the JSON-RPC result object
// VERBATIM. Requirement 3 is an equality between two whole envelopes, so the
// bytes are what a test compares; callText below reads one field out of them.
func (s *mcpSearchStack) callResult(t *testing.T, tool string, args map[string]any) json.RawMessage {
	t.Helper()
	body, _ := s.post(t, map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "tools/call",
		"params": map[string]any{"name": tool, "arguments": args},
	}, s.session)
	var envelope struct {
		Result json.RawMessage `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &envelope); err != nil {
		t.Fatalf("tools/call %s: unmarshal %q: %v", tool, body, err)
	}
	if len(envelope.Error) > 0 {
		t.Fatalf("tools/call %s: JSON-RPC error %s", tool, envelope.Error)
	}
	return envelope.Result
}

// callText returns the first content block's text and whether the result was
// flagged an error, decoded from the same envelope callResult returns.
func (s *mcpSearchStack) callText(t *testing.T, tool string, args map[string]any) (string, bool) {
	t.Helper()
	var result struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	raw := s.callResult(t, tool, args)
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("tools/call %s: unmarshal result %s: %v", tool, raw, err)
	}
	if len(result.Content) == 0 {
		t.Fatalf("tools/call %s: result %s carries no content block", tool, raw)
	}
	return result.Content[0].Text, result.IsError
}
