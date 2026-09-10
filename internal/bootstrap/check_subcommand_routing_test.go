// SPDX-License-Identifier: Apache-2.0

// check_subcommand_routing_test.go — the shell face reads the checks corpus
// THROUGH THE DAEMON.
//
// WHY THIS FILE EXISTS. The face used to construct a bare graphclient aimed at
// the local file-backed knowledge-server and run the analyzer in-process. On a
// machine whose data plane is cloud that face could not read the checks corpus
// at all: every check authored through an MCP tool call lands in the routed
// plane, and a local-store reader sees a different corpus — the failure is not
// even loud, because a partial read looks exactly like a complete one. These
// rows pin the routed shape behaviorally: the call ARRIVES at the daemon
// carrying the caller's scope, and an unreachable daemon is REFUSED rather than
// quietly answered from somewhere else.

package bootstrap

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/tools"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/corpusscan"
)

// fakeDaemonSessionID is the session the stub mints on initialize. A fixed
// value rather than a random one so a request that carried the WRONG header is
// distinguishable in a failure message from one that carried none.
const fakeDaemonSessionID = "check-run-test-session"

// fakeDaemon is an in-process stand-in for `knowledge serve`'s streamable-HTTP
// MCP endpoint. It mints a session on initialize, REQUIRES that session header
// on every later request exactly as the real handler does
// (graphclient/mcp_http.go handlePOST), records every tools/call it receives,
// and answers with whatever the test's responder returns — unless its fault says
// to answer wrongly instead, which for the two aborts means recording the call
// and then resetting the stream rather than answering it at all.
//
// IT IS DELIBERATELY NOT A GRAPH SERVER. The point of the routed face is that
// the CLI no longer speaks the graph protocol at all, so a stub that answered
// Connect health probes would let a reverted implementation pass.
type fakeDaemon struct {
	srv *httptest.Server

	mu        sync.Mutex
	calls     []kgtools.CallToolParams
	sessionOK []bool

	respond func(kgtools.CallToolParams) kgtools.ToolResult

	// fault is the one way THIS stub answers wrongly, zero when it answers
	// well. It is a field on the stub rather than a second stub type so every
	// row — the healthy control and each malformed answer — reaches the
	// production client through one handler.
	fault fakeDaemonFault
}

// startFakeDaemon stands the stub up on an ephemeral loopback port over h2c —
// cleartext HTTP/2, the transport the real daemon serves and the only one the
// client dials.
func startFakeDaemon(t *testing.T, respond func(kgtools.CallToolParams) kgtools.ToolResult) *fakeDaemon {
	t.Helper()
	d := &fakeDaemon{respond: respond}

	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", d.handle)
	d.srv = httptest.NewServer(h2c.NewHandler(mux, &http2.Server{}))
	t.Cleanup(func() { d.srv.CloseClientConnections(); d.srv.Close() })
	return d
}

// handle mirrors the real endpoint's method routing closely enough that a
// client which works here works there: initialize mints and returns the
// session header, everything else is looked up by it.
func (d *fakeDaemon) handle(w http.ResponseWriter, r *http.Request) {
	var req kgtools.JSONRPCRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	switch req.Method {
	case "initialize":
		if d.fault.handshakeStatus != 0 {
			http.Error(w, "the daemon refused the handshake", d.fault.handshakeStatus)
			return
		}
		if !d.fault.omitSessionHeader {
			w.Header().Set("Mcp-Session-Id", fakeDaemonSessionID)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-11-25","capabilities":{"tools":{}},"serverInfo":{"name":"knowledge","version":"stub"}}}`)
	case "tools/call":
		var params kgtools.CallToolParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			http.Error(w, "bad params", http.StatusBadRequest)
			return
		}
		d.mu.Lock()
		d.calls = append(d.calls, params)
		sessionOK := r.Header.Get("Mcp-Session-Id") == fakeDaemonSessionID
		d.sessionOK = append(d.sessionOK, sessionOK)
		d.mu.Unlock()
		// THE SESSION IS ENFORCED, not merely recorded, because the real
		// endpoint answers 404 without it (graphclient/mcp_http.go handlePOST).
		// A stub that recorded the header and answered anyway would let a client
		// that never carried one read as working here and 404 in production.
		if !sessionOK {
			http.Error(w, "a session header is required", http.StatusNotFound)
			return
		}
		if d.fault.toolStatus != 0 {
			http.Error(w, "the daemon refused the call", d.fault.toolStatus)
			return
		}
		// THE TWO ABORTS RESET THE STREAM rather than answering it, which is
		// what a daemon killed, wedged or cut off mid-answer looks like to this
		// client. http.ErrAbortHandler is the sanctioned spelling: net/http
		// suppresses the panic trace and the HTTP/2 layer sends RST_STREAM, so
		// the client sees a transport failure and not a response.
		if d.fault.abortBeforeHeaders {
			panic(http.ErrAbortHandler)
		}
		if d.fault.truncatedBody != "" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, d.fault.truncatedBody)
			// The flush is load-bearing: without it the headers and the partial
			// body would be buffered and discarded by the abort, and the row
			// would drive the abortBeforeHeaders arm instead of the read arm.
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			panic(http.ErrAbortHandler)
		}
		if d.fault.toolBody != "" {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, d.fault.toolBody)
			return
		}
		id := req.ID
		if d.fault.responseID != "" {
			id = json.RawMessage(d.fault.responseID)
		}
		writeStubResult(w, id, d.respond(params))
	default:
		// A notification (notifications/initialized) has no response body.
		w.WriteHeader(http.StatusAccepted)
	}
}

// writeStubResult marshals a tool result into the JSON-RPC envelope the daemon
// writes, through the SAME kgtools types the daemon uses, so the stub cannot
// drift into a shape the production decoder would never see.
func writeStubResult(w http.ResponseWriter, id json.RawMessage, res kgtools.ToolResult) {
	out, err := json.Marshal(&kgtools.JSONRPCResponse{JSONRPC: "2.0", ID: id, Result: res})
	if err != nil {
		http.Error(w, "marshal", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(out)
}

// port is the ephemeral port the stub bound.
func (d *fakeDaemon) port(t *testing.T) int { return portFromURL(t, d.srv.URL) }

// toolCalls returns the recorded calls.
func (d *fakeDaemon) toolCalls() []kgtools.CallToolParams {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]kgtools.CallToolParams(nil), d.calls...)
}

// stubVerdict renders a run result the way the tool does — the machine-readable
// token first, then the counters — so the CLI is parsing the shape it will
// actually be handed. The token comes from the tool's own exported constants
// rather than a literal.
func stubVerdict(token string) kgtools.ToolResult {
	return kgtools.ToolResult{Content: []kgtools.ContentBlock{{
		Type: "text",
		Text: fmt.Sprintf(
			"%s: %s  checks_flagged=1 sites_flagged=1 checks_refused=0 llm_only_not_executed=0 test_files_scanned=0 truncated=false\nwarning\tpkg/a.go:7\tchk-1\tno naked defer Close\n",
			corpusscan.AnalyzerName, token),
	}}}
}

// TestCheckRun_ReadsTheCorpusThroughTheDaemon is entry 1: the verb's checks read
// ARRIVES at the daemon, carrying every scope the caller named.
//
// WHAT MAKES IT FALSIFYING. A face that constructs its own graph client answers
// from wherever that client points and sends the daemon nothing at all, so the
// recorded-call assertion is empty. It is the arrival that is asserted, not a
// spelling in the source.
func TestCheckRun_ReadsTheCorpusThroughTheDaemon(t *testing.T) {
	daemon := startFakeDaemon(t, func(kgtools.CallToolParams) kgtools.ToolResult {
		return stubVerdict(tools.VerdictFlagged)
	})
	repo := t.TempDir()

	err := captureStdoutErr(t, func() error {
		return runCheckRun([]string{
			"--http-port", fmt.Sprint(daemon.port(t)),
			"--repo", repo,
			"--language", "go",
			"--files", "pkg/a.go",
			"chk-1",
		})
	})
	require.ErrorIs(t, err, errCheckFlagged,
		"the daemon's verdict must decide the exit status; a FLAGGED answer is the flagged sentinel")

	calls := daemon.toolCalls()
	require.Len(t, calls, 1, "the corpus read must reach the daemon exactly once")
	assert.Equal(t, "manage_checks", calls[0].Name)

	var args map[string]any
	require.NoError(t, json.Unmarshal(calls[0].Arguments, &args))
	assert.Equal(t, "run", args["operation"])
	assert.Equal(t, "go", args["language"])
	assert.Equal(t, repo, args["repo"],
		"the repo is resolved to an absolute tree BY THE CALLER — a bare name would resolve against the daemon's own root, not the tree the operator is standing in")
	assert.Equal(t, []any{"chk-1"}, args["ids"])
	assert.Equal(t, []any{"pkg/a.go"}, args["files"])
	_, compactSent := args["compact"]
	assert.False(t, compactSent,
		"an omitted --compact stays absent so the analyzer's own resolver picks the default; sending one here would be a second answer")

	d := daemon
	d.mu.Lock()
	defer d.mu.Unlock()
	require.Len(t, d.sessionOK, 1)
	assert.True(t, d.sessionOK[0],
		"the call must carry the session the daemon minted on initialize, or the real endpoint answers 404")
}

// TestCheckRun_ResolvesTheRepoBeforeItTravels is the row that makes the repo
// assertion above discriminate.
//
// WHY IT IS A SEPARATE ROW. An ABSOLUTE --repo resolves to itself, so a face
// that forwarded the caller's raw argument and a face that forwarded the
// resolved tree send identical bytes — the assertion passes either way. A BARE
// NAME is where they part: the resolver reads it against the CALLER's working
// directory, while the daemon would read the same name against its own root or
// the machine-local manifest and scan a different checkout entirely. That is not
// hypothetical; it is why a criterion naming a repo by name measures the main
// checkout from whichever worktree invokes it.
func TestCheckRun_ResolvesTheRepoBeforeItTravels(t *testing.T) {
	daemon := startFakeDaemon(t, func(kgtools.CallToolParams) kgtools.ToolResult {
		return stubVerdict(tools.VerdictClean)
	})

	// Stand the process in a directory whose BASENAME is the repo name, which is
	// the resolver's current-tree arm — no manifest, no HOME, nothing shared.
	t.Chdir(t.TempDir())
	wd, err := os.Getwd()
	require.NoError(t, err)

	runErr := captureStdoutErr(t, func() error {
		return runCheckRun([]string{
			"--http-port", fmt.Sprint(daemon.port(t)),
			"--repo", filepath.Base(wd),
			"--language", "go",
		})
	})
	require.NoError(t, runErr, "a CLEAN answer is exit 0")

	calls := daemon.toolCalls()
	require.Len(t, calls, 1)
	var args map[string]any
	require.NoError(t, json.Unmarshal(calls[0].Arguments, &args))

	assert.Equal(t, wd, args["repo"],
		"the RESOLVED tree must travel, not the name the caller typed")
	assert.NotEqual(t, filepath.Base(wd), args["repo"],
		"control: the two differ for this input, which is what makes the assertion above discriminate")
}

// TestCheckRun_RefusesWhenTheDaemonIsUnreachable is entry 2. The refusal must
// NAME the endpoint and the remedy, and it must be a refusal: a face that fell
// back to a local store on an unreachable daemon would report a verdict over a
// corpus nobody asked for, which is the whole defect being closed.
func TestCheckRun_RefusesWhenTheDaemonIsUnreachable(t *testing.T) {
	// Port 1 is privileged and nothing listens on it under test, so the dial
	// fails fast and deterministically.
	err := captureStdoutErr(t, func() error {
		return runCheckRun([]string{
			"--http-port", "1",
			"--repo", t.TempDir(),
			"--language", "go",
		})
	})
	require.Error(t, err, "an unreachable daemon must refuse, never answer")
	require.NotErrorIs(t, err, errCheckFlagged, "an unreachable daemon is not a verdict")
	require.NotErrorIs(t, err, errCheckInconclusive,
		"an unreachable daemon is a refusal to run, not an inconclusive run — those are different answers and a criterion reads them differently")

	msg := err.Error()
	assert.Contains(t, msg, "127.0.0.1:1/mcp", "the refusal must name the daemon endpoint it could not reach")
	assert.Contains(t, msg, "knowledge serve", "the refusal must name the fix")
	assert.NotContains(t, strings.ToLower(msg), "clean",
		"a refusal must never read as a clean corpus")
}

// captureStdoutErr runs fn with stdout captured, returning fn's error. The
// capture keeps the verdict body out of the test log; the error is what the
// rows above assert on.
func captureStdoutErr(t *testing.T, fn func() error) error {
	t.Helper()
	var err error
	_ = captureStdout(t, func() { err = fn() })
	return err
}
