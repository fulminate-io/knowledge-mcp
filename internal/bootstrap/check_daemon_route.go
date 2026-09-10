// SPDX-License-Identifier: Apache-2.0

// check_daemon_route.go — the one transport `knowledge check run` uses to reach
// the checks corpus: an MCP tools/call to the running `knowledge serve` daemon.
//
// WHY THE DAEMON AND NOT A GRAPH CLIENT. A graph client built here would be a
// Connect client aimed at the LOCAL file-backed knowledge-server, with no
// Router: it can never reach the cloud data plane, whatever the login state.
// Checks authored through any MCP tool call land in the routed plane, so a
// local-store reader answers from a DIFFERENT corpus than the one the author
// wrote to — and the failure is not loud, because a partial read looks exactly
// like a complete one. The daemon already holds the login-aware Router that
// every MCP tool call rides, so the honest way for a shell face to read that
// corpus is to ask the daemon, which is also how `knowledge version` reads the
// daemon's own version (version_subcommand.go).
//
// IT IS THE SAME ANALYZER EITHER WAY. The daemon answers manage_checks with the
// registered corpus_scan analyzer over the tree this call names, so routing
// moves WHERE the corpus is read from and nothing about WHAT is scanned.

package bootstrap

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// The two budgets. They are separate because the two legs answer different
// questions: the handshake asks "is the daemon there", which is a loopback
// round trip and must fail fast so an unreachable daemon is reported in
// seconds; the scan asks the daemon to walk a tree, which on a whole repository
// is tens of seconds of real work. One budget would either make the reachability
// answer slow or cut a legitimate scan off mid-walk.
const (
	checkDaemonHandshakeBudget = 5 * time.Second
	checkDaemonScanBudget      = 10 * time.Minute
)

// mcpSessionHeader is the streamable-HTTP session header the daemon mints on
// initialize and REQUIRES on every later request (it answers 404 without it).
const mcpSessionHeader = "Mcp-Session-Id"

// checkDaemonToolCallID is the JSON-RPC id this face puts on its one tools/call.
// It is a constant rather than a literal at the send site because the id is a
// CORRELATION: the answer is only this call's answer if it carries this id back,
// so the value the request is stamped with and the value the reply is checked
// against must be the same one, by construction.
const checkDaemonToolCallID = "2"

// mcpProtocolVersion is the version this face asks for. The daemon echoes a
// version it supports, and nothing in this face reads the echoed value — of the
// handshake it keeps only the session header, and of the tools/call answer only
// the JSON-RPC id and the tool result — so the constant matters solely as a
// well-formed handshake.
const mcpProtocolVersion = "2025-11-25"

// checkDaemonEndpoint renders the loopback MCP endpoint for a port. It is one
// function so the address in a refusal message and the address actually dialed
// are the same string — a refusal naming an endpoint the code did not try would
// send a reader to the wrong place.
func checkDaemonEndpoint(port int) string {
	return fmt.Sprintf("http://127.0.0.1:%d/mcp", port)
}

// runChecksOnDaemon performs the manage_checks call against the daemon and
// returns the rendered result text verbatim.
//
// EVERY FAILURE IS A REFUSAL, never a fallback. There is no second corpus to
// read from: a face that answered from a local store when the daemon was down
// would report a verdict over a corpus the caller never asked about, which is
// the exact defect routing this face closes.
func runChecksOnDaemon(port int, args map[string]any) (string, error) {
	endpoint := checkDaemonEndpoint(port)
	client, release := newDaemonMCPClient()
	defer release()

	session, err := openDaemonSession(client, endpoint)
	if err != nil {
		return "", err
	}
	return callDaemonTool(client, endpoint, session, "manage_checks", args)
}

// openDaemonSession performs the MCP initialize handshake and returns the
// session id the daemon minted.
//
// THE HANDSHAKE IS THE REACHABILITY GATE. There is no separate health probe:
// the first thing this face does is the first thing it needs, so a daemon that
// is not up is reported by the call that would have read the corpus rather than
// by a probe that could disagree with it.
//
// The initialized notification is deliberately not sent: the endpoint gates
// every non-initialize request on the session header alone (mcp_http.go
// handlePOST), so a notification would add a round trip and gate nothing.
func openDaemonSession(client *http.Client, endpoint string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), checkDaemonHandshakeBudget)
	defer cancel()

	body := fmt.Appendf(nil,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":%q,"capabilities":{},"clientInfo":{"name":"knowledge check run","version":%q}}}`,
		mcpProtocolVersion, Version)

	resp, err := postDaemon(ctx, client, endpoint, "", body)
	if err != nil {
		// %w, not %v: the dial failure is the CAUSE and stays interrogable —
		// a caller distinguishing "refused" from "timed out" reads it off the
		// chain, and rendering it as text would silently take that away while
		// the printed line looked identical.
		return "", fmt.Errorf(
			"check run: the knowledge daemon is not reachable at %s, so the checks corpus was not read and no scan ran (%w) — "+
				"start it with `knowledge serve`, or name a running daemon with --http-port", endpoint, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf(
			"check run: the knowledge daemon at %s refused the MCP handshake with HTTP %d, so the checks corpus was not read",
			endpoint, resp.StatusCode)
	}
	session := resp.Header.Get(mcpSessionHeader)
	if session == "" {
		return "", fmt.Errorf(
			"check run: the knowledge daemon at %s answered the handshake without an %s header, so no session could be opened and the checks corpus was not read",
			endpoint, mcpSessionHeader)
	}
	return session, nil
}

// callDaemonTool issues one tools/call and returns the text of the result.
func callDaemonTool(client *http.Client, endpoint, session, tool string, args map[string]any) (string, error) {
	encodedArgs, err := json.Marshal(args)
	if err != nil {
		return "", fmt.Errorf("check run: encode the %s arguments: %w", tool, err)
	}
	params, err := json.Marshal(kgtools.CallToolParams{Name: tool, Arguments: encodedArgs})
	if err != nil {
		return "", fmt.Errorf("check run: encode the %s call: %w", tool, err)
	}
	body, err := json.Marshal(kgtools.JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage(checkDaemonToolCallID),
		Method:  "tools/call",
		Params:  params,
	})
	if err != nil {
		return "", fmt.Errorf("check run: encode the %s request: %w", tool, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), checkDaemonScanBudget)
	defer cancel()

	resp, err := postDaemon(ctx, client, endpoint, session, body)
	if err != nil {
		return "", fmt.Errorf(
			"check run: the %s call to the knowledge daemon at %s did not complete, so no verdict was produced: %w", tool, endpoint, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf(
			"check run: the knowledge daemon at %s answered the %s call with HTTP %d, so no verdict was produced",
			endpoint, tool, resp.StatusCode)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("check run: read the %s response from %s: %w", tool, endpoint, err)
	}
	return decodeToolText(raw, tool, endpoint, checkDaemonToolCallID)
}

// decodeToolText unwraps the JSON-RPC envelope and the tool result, returning
// the result's text. wantID is the JSON-RPC id the request was sent with.
//
// THE ID IS CHECKED BEFORE ANYTHING ELSE IN THE ENVELOPE IS READ. A JSON-RPC
// response answers ONE request, named by the id it carries back; a body whose id
// is not the one this face sent is another call's answer, an interposed proxy's,
// or a daemon that lost track of the request — and nothing in it describes the
// scan that was asked for, its error text included. Reading the error arm first
// would relay a message about some other call as though it were this one's
// refusal, and reading the result arm first would classify some other call's
// verdict. Bad input errors here rather than being coerced into an answer.
//
// A TOOL-LEVEL ERROR IS RELAYED VERBATIM. The tool's refusals name the offending
// value and the accepted vocabulary; re-wording them here would drop what makes
// them actionable, and swallowing one would turn a refused scan into a silent
// pass. The endpoint is PREFIXED to it rather than substituted for it: the
// daemon that refused is not necessarily the one the reader assumes, and on a
// client newer than the daemon this arm is where the version skew surfaces.
func decodeToolText(raw []byte, tool, endpoint, wantID string) (string, error) {
	var envelope struct {
		ID     json.RawMessage     `json:"id"`
		Result *kgtools.ToolResult `json:"result"`
		Error  *kgtools.RPCError   `json:"error"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return "", fmt.Errorf("check run: the knowledge daemon at %s answered the %s call with a body this client could not decode: %w", endpoint, tool, err)
	}
	if gotID := strings.TrimSpace(string(envelope.ID)); gotID != wantID {
		return "", fmt.Errorf(
			"check run: the knowledge daemon at %s answered the %s call with JSON-RPC id %s, but this client sent id %s, so the body is not this call's answer and no verdict was produced",
			endpoint, tool, renderRPCID(gotID), wantID)
	}
	if envelope.Error != nil {
		return "", fmt.Errorf("check run: the knowledge daemon at %s refused the %s call: %s (code %d)", endpoint, tool, envelope.Error.Message, envelope.Error.Code)
	}
	if envelope.Result == nil || len(envelope.Result.Content) == 0 {
		return "", fmt.Errorf("check run: the knowledge daemon at %s answered the %s call with no content, so there is no verdict to report", endpoint, tool)
	}
	var text strings.Builder
	for _, block := range envelope.Result.Content {
		text.WriteString(block.Text)
	}
	if envelope.Result.IsError {
		return "", fmt.Errorf("check run: the knowledge daemon at %s refused the %s call: %s",
			endpoint, tool, strings.TrimSpace(text.String()))
	}
	return text.String(), nil
}

// renderRPCID spells an id for a refusal message. An id that is absent and one
// that is the JSON literal null are both rendered as words rather than as
// nothing at all, so the refusal never reads as though it named an id and the
// reader can tell the two apart.
func renderRPCID(id string) string {
	switch id {
	case "":
		return "none at all"
	case "null":
		return "null"
	default:
		return id
	}
}

// postDaemon issues one JSON-RPC POST, carrying the session header when there
// is one.
func postDaemon(ctx context.Context, client *http.Client, endpoint, session string, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if session != "" {
		req.Header.Set(mcpSessionHeader, session)
	}
	return client.Do(req)
}

// newDaemonMCPClient builds the h2c (cleartext HTTP/2) client the daemon's /mcp
// endpoint requires, and the release func that tears its connections down.
// HTTP/1.1 OFF, unencrypted HTTP/2 ON, so the request reaches the daemon's
// h2c handler. No client-level timeout: each leg carries its own context
// deadline, which is what distinguishes an unreachable daemon from a long scan.
//
// RELEASE OWNS THE DIALED CONNECTIONS rather than calling CloseIdleConnections,
// for the reason the version probe documents: a pool-level release only reaches
// connections the pool considers idle, so a connection still carrying an aborted
// stream when a deadline fires is skipped — and with no IdleConnTimeout nothing
// reaps it afterwards. Holding the dialed conns makes teardown unconditional,
// which is correct for a client that serves one command and exits.
func newDaemonMCPClient() (client *http.Client, release func()) {
	var (
		mu     sync.Mutex
		dialed []net.Conn
	)

	t := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			var d net.Dialer
			conn, err := d.DialContext(ctx, network, addr)
			if err != nil {
				return nil, err
			}
			mu.Lock()
			dialed = append(dialed, conn)
			mu.Unlock()
			return conn, nil
		},
	}
	t.Protocols = new(http.Protocols)
	t.Protocols.SetHTTP1(false)
	t.Protocols.SetUnencryptedHTTP2(true)

	return &http.Client{Transport: t}, func() {
		mu.Lock()
		defer mu.Unlock()
		for _, conn := range dialed {
			_ = conn.Close()
		}
		dialed = nil
	}
}
