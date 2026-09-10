// SPDX-License-Identifier: Apache-2.0

package bootstrap

// check_daemon_route_test.go — EVERY WAY THE DAEMON CAN ANSWER WRONGLY.
//
// WHY THIS FILE EXISTS. The routed face has one happy path and, beside it, every
// refusal return check_daemon_route.go contains: each one is a way the answer
// can be unusable, and a refusal nothing observes is a refusal that can be
// deleted without a red — several of them turn a corpus that was never read into
// exit 0, which is the vacuous green this whole surface exists to prevent.
//
// THE COUNT IS NOT WRITTEN DOWN HERE, ON PURPOSE. Two rounds of this work stated
// it in prose and both statements were short of the truth within a commit.
// check_daemon_route_census_test.go parses the routing file, counts its refusal
// returns, and requires every one of them to be either observed by a test named
// below or declared unreachable with its reason — so the class is kept by a test
// rather than by this paragraph, and a new arm cannot land silent.
//
// EACH ROW IS DRIVEN THROUGH THE REAL VERB, runCheckRun, against the fakeDaemon
// in check_subcommand_routing_test.go, so what is asserted is the behavior a
// caller gets and not a spelling in the source. Every row asserts the same three
// properties: the run is an error, the error is neither verdict sentinel, and
// the exit status is not 0 — plus the words that make the refusal actionable,
// which are the endpoint it tried and what came back from it.

import (
	"context"
	"fmt"
	"net"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/tools"
)

// fakeDaemonFault is the one way a stub daemon answers wrongly. The zero value
// is a daemon that answers well, which is what every other row in this package
// uses.
type fakeDaemonFault struct {
	// handshakeStatus answers initialize with this HTTP status instead of a
	// session.
	handshakeStatus int
	// omitSessionHeader answers initialize 200 and well-formed, but mints no
	// Mcp-Session-Id.
	omitSessionHeader bool
	// toolStatus answers tools/call with this HTTP status instead of a result.
	toolStatus int
	// toolBody is written VERBATIM as the tools/call body, which is how a
	// non-MCP answer is expressed — no JSON-RPC envelope at all.
	toolBody string
	// abortBeforeHeaders aborts the tools/call stream before any response
	// header is written, which is how a request that does not COMPLETE is
	// expressed: the client's Do returns an error and there is no response to
	// read a status off.
	abortBeforeHeaders bool
	// truncatedBody writes this much of a body under a 200, flushes it, then
	// aborts the stream. The headers and the status arrive, so the client gets
	// past the status check and fails while READING — a different arm from
	// both the abort above and the undecodable body below.
	truncatedBody string
	// responseID replaces the JSON-RPC id the stub echoes on tools/call. The
	// result itself stays well-formed and CLEAN, so the id is the ONLY thing
	// separating this answer from the healthy control.
	responseID string
}

// startFaultyDaemon stands a stub up that answers wrongly in exactly one way.
//
// ITS RESPONDER IS A CLEAN VERDICT ON PURPOSE. Every fault either short-circuits
// before the responder or, in responseID's case, changes one field of what the
// responder produced — so a production arm that stopped refusing would fall
// through to a perfectly readable CLEAN answer and exit 0. That is what makes
// each row below discriminate: the alternative to the refusal is not another
// error, it is a green gate over a corpus that was never read.
func startFaultyDaemon(t *testing.T, fault fakeDaemonFault) *fakeDaemon {
	t.Helper()
	d := startFakeDaemon(t, func(kgtools.CallToolParams) kgtools.ToolResult {
		return stubVerdict(tools.VerdictClean)
	})
	d.fault = fault
	return d
}

// runCheckRunAgainst drives the real verb at a stub's port with the smallest
// legal invocation.
func runCheckRunAgainst(t *testing.T, daemon *fakeDaemon) error {
	t.Helper()
	repo := t.TempDir()
	return captureStdoutErr(t, func() error {
		return runCheckRun([]string{
			"--http-port", fmt.Sprint(daemon.port(t)),
			"--repo", repo,
			"--language", "go",
		})
	})
}

// requireDaemonRefusal asserts the three properties every unusable answer must
// have, then the words the refusal must carry.
func requireDaemonRefusal(t *testing.T, err error, wants ...string) {
	t.Helper()
	require.Error(t, err, "an answer this face cannot read is a refusal, never a verdict")
	require.NotErrorIs(t, err, errCheckFlagged, "an unusable answer is not a flagged corpus")
	require.NotErrorIs(t, err, errCheckInconclusive,
		"an unusable answer is a refusal to run, not an inconclusive run — a criterion reads those differently")
	code, _ := subcommandExit(err)
	assert.NotEqual(t, 0, code, "the one exit status a refusal must never take is 0")
	assert.Equal(t, 1, code, "an unreadable daemon answer is an ordinary failure, not a verdict code")
	assert.NotContains(t, strings.ToLower(err.Error()), "corpus_scan: clean",
		"a refusal must never read as a clean corpus")
	for _, want := range wants {
		assert.Contains(t, err.Error(), want)
	}
}

// endpointOf renders the endpoint string a refusal must name for a stub.
func endpointOf(t *testing.T, daemon *fakeDaemon) string {
	t.Helper()
	return fmt.Sprintf("127.0.0.1:%d/mcp", daemon.port(t))
}

// TestCheckRun_RefusesAnEnvelopeCarryingAnRPCError observes decodeToolText's
// envelope.Error arm, and it is the SKEW ARM, and the one
// with a first-hand instance: a client newer than the daemon sends a parameter
// the running binary's schema does not declare, and the daemon refuses the call
// by name in the JSON-RPC envelope's error object. Relaying that as a verdict
// would report a corpus state from a scan that never ran.
func TestCheckRun_RefusesAnEnvelopeCarryingAnRPCError(t *testing.T) {
	const daemonSaid = `manage_checks: unknown parameter "compact"`
	daemon := startFaultyDaemon(t, fakeDaemonFault{
		toolBody: fmt.Sprintf(`{"jsonrpc":"2.0","id":2,"error":{"code":-32602,"message":%q}}`, daemonSaid),
	})

	err := runCheckRunAgainst(t, daemon)
	requireDaemonRefusal(t, err,
		endpointOf(t, daemon),
		daemonSaid, // the daemon's own words survive: they name the offending key
		"-32602",
		"manage_checks",
	)
}

// TestCheckRun_RefusesAToolResultFlaggedAsAnError observes decodeToolText's
// Result.IsError arm: the transport succeeded and
// the envelope is well-formed, but the TOOL refused. Its text is what names the
// offending value, so it is relayed rather than reworded.
func TestCheckRun_RefusesAToolResultFlaggedAsAnError(t *testing.T) {
	const toolSaid = "manage_checks run: no check matches id chk-nope"
	daemon := startFakeDaemon(t, func(kgtools.CallToolParams) kgtools.ToolResult {
		return kgtools.ToolResult{
			IsError: true,
			Content: []kgtools.ContentBlock{{Type: "text", Text: toolSaid}},
		}
	})

	err := runCheckRunAgainst(t, daemon)
	requireDaemonRefusal(t, err, endpointOf(t, daemon), toolSaid)
}

// TestCheckRun_RefusesAnAnswerWithNoContent observes decodeToolText's
// nil-or-empty Result.Content arm, and covers both spellings of "there is
// nothing to read": a result whose content list is empty, and an envelope with
// no result at all. They are one production arm and two input classes, and a
// face that let either through would classify the empty string.
func TestCheckRun_RefusesAnAnswerWithNoContent(t *testing.T) {
	t.Run("an empty content list", func(t *testing.T) {
		daemon := startFakeDaemon(t, func(kgtools.CallToolParams) kgtools.ToolResult {
			return kgtools.ToolResult{Content: nil}
		})
		requireDaemonRefusal(t, runCheckRunAgainst(t, daemon),
			endpointOf(t, daemon), "no content", "manage_checks")
	})

	t.Run("no result member at all", func(t *testing.T) {
		daemon := startFaultyDaemon(t, fakeDaemonFault{toolBody: `{"jsonrpc":"2.0","id":2}`})
		requireDaemonRefusal(t, runCheckRunAgainst(t, daemon),
			endpointOf(t, daemon), "no content", "manage_checks")
	})
}

// TestCheckRun_RefusesABodyItCannotDecode observes decodeToolText's
// json.Unmarshal arm: something is listening on the port
// and answering 200, but it is not speaking MCP.
//
// THE BODY IS DELIBERATELY A VALID VERDICT LINE. A face that fell back to
// treating an undecodable body as text would parse this one as CLEAN and exit 0
// over a corpus it never read — so the row's alternative to the refusal is a
// green gate, not another error.
func TestCheckRun_RefusesABodyItCannotDecode(t *testing.T) {
	daemon := startFaultyDaemon(t, fakeDaemonFault{
		toolBody: "corpus_scan: CLEAN  checks_flagged=0 sites_flagged=0 checks_refused=0 " +
			"llm_only_not_executed=0 test_files_scanned=0 truncated=false\n",
	})

	err := runCheckRunAgainst(t, daemon)
	requireDaemonRefusal(t, err, endpointOf(t, daemon), "could not decode", "manage_checks")
}

// TestCheckRun_RefusesARefusedHandshake observes openDaemonSession's non-2xx
// arm: the daemon is listening but will not
// open a session, so the corpus was never reached.
func TestCheckRun_RefusesARefusedHandshake(t *testing.T) {
	daemon := startFaultyDaemon(t, fakeDaemonFault{handshakeStatus: 503})

	err := runCheckRunAgainst(t, daemon)
	requireDaemonRefusal(t, err, endpointOf(t, daemon), "handshake", "503")

	assert.Empty(t, daemon.toolCalls(),
		"control: the run must not have reached the tool call, which is what makes this the handshake arm")
}

// TestCheckRun_RefusesAHandshakeThatMintsNoSession observes openDaemonSession's
// empty-session arm: a 200 handshake with no
// Mcp-Session-Id. The real endpoint answers 404 to every later request without
// that header, so continuing would turn a missing session into an obscure
// not-found rather than the thing that actually went wrong.
func TestCheckRun_RefusesAHandshakeThatMintsNoSession(t *testing.T) {
	daemon := startFaultyDaemon(t, fakeDaemonFault{omitSessionHeader: true})

	err := runCheckRunAgainst(t, daemon)
	requireDaemonRefusal(t, err, endpointOf(t, daemon), mcpSessionHeader)

	assert.Empty(t, daemon.toolCalls(),
		"the refusal is at the handshake: a client that carried on would be answered 404 by the real endpoint")
}

// TestCheckRun_RefusesANon2xxToolCall observes callDaemonTool's non-2xx arm: the
// session opened and the call was made,
// and the daemon answered with a status instead of a result.
func TestCheckRun_RefusesANon2xxToolCall(t *testing.T) {
	daemon := startFaultyDaemon(t, fakeDaemonFault{toolStatus: 500})

	err := runCheckRunAgainst(t, daemon)
	requireDaemonRefusal(t, err, endpointOf(t, daemon), "500", "manage_checks")

	require.Len(t, daemon.toolCalls(), 1,
		"control: the call DID reach the daemon, which is what separates this arm from the handshake ones")
}

// TestCheckRun_AHealthyDaemonIsTheControl is the same-run known positive for
// every row in this file: through the SAME stub, the SAME verb and the SAME helper, a
// well-formed answer produces the verdict's own exit status. Without it the
// refusals could be a harness that fails whatever it is given.
func TestCheckRun_AHealthyDaemonIsTheControl(t *testing.T) {
	clean := startFakeDaemon(t, func(kgtools.CallToolParams) kgtools.ToolResult {
		return stubVerdict(tools.VerdictClean)
	})
	require.NoError(t, runCheckRunAgainst(t, clean), "a CLEAN answer through this harness is exit 0")

	flagged := startFakeDaemon(t, func(kgtools.CallToolParams) kgtools.ToolResult {
		return stubVerdict(tools.VerdictFlagged)
	})
	require.ErrorIs(t, runCheckRunAgainst(t, flagged), errCheckFlagged,
		"and a FLAGGED answer is the flagged sentinel, so the harness carries a verdict when there is one")
}

// TestCheckRun_RefusesACallThatDidNotComplete observes callDaemonTool's
// transport-failure arm: the session opened, the call was sent, and the answer
// never arrived — the daemon was killed, wedged or cut off mid-request. There is
// no status and no body to read, so the only thing this face can say is that no
// verdict was produced.
func TestCheckRun_RefusesACallThatDidNotComplete(t *testing.T) {
	daemon := startFaultyDaemon(t, fakeDaemonFault{abortBeforeHeaders: true})

	err := runCheckRunAgainst(t, daemon)
	requireDaemonRefusal(t, err, endpointOf(t, daemon), "did not complete", "manage_checks")

	require.Len(t, daemon.toolCalls(), 1,
		"control: the call DID reach the daemon, which is what separates this arm from the handshake ones")
}

// TestCheckRun_RefusesAResponseItCannotFinishReading observes callDaemonTool's
// io.ReadAll arm: headers and a 200 arrived, so every status check passed, and
// the body was cut off partway through.
//
// THE PARTIAL BODY IS DELIBERATELY THE PREFIX OF A CLEAN VERDICT. A face that
// treated a short read as "what arrived is the answer" would hand this to the
// decoder, and the bytes it would hand over begin with a corpus_scan CLEAN line
// — so the row's alternative to the refusal is a green gate over a scan whose
// result nobody ever received in full.
func TestCheckRun_RefusesAResponseItCannotFinishReading(t *testing.T) {
	daemon := startFaultyDaemon(t, fakeDaemonFault{
		truncatedBody: `{"jsonrpc":"2.0","id":2,"result":{"content":[{"type":"text",` +
			`"text":"corpus_scan: CLEAN  checks_flagged=0 sites_flagged=0`,
	})

	err := runCheckRunAgainst(t, daemon)
	requireDaemonRefusal(t, err, endpointOf(t, daemon), "read the manage_checks response")

	require.Len(t, daemon.toolCalls(), 1,
		"control: the call DID reach the daemon and was answered — this arm is the READ of that answer")
}

// TestCheckRun_RefusesAnAnswerCarryingAnotherRequestsID observes decodeToolText's
// id-correlation arm. A JSON-RPC response is an answer to ONE request, named by
// the id it carries; a body whose id is not the one this face sent is some other
// call's answer, a proxy's, or a daemon that lost track of the request, and
// classifying its verdict would report on a scan this invocation never asked
// for. Bad input errors; it is never coerced into an answer.
func TestCheckRun_RefusesAnAnswerCarryingAnotherRequestsID(t *testing.T) {
	t.Run("an id that is not the one sent", func(t *testing.T) {
		daemon := startFaultyDaemon(t, fakeDaemonFault{responseID: "99"})

		err := runCheckRunAgainst(t, daemon)
		requireDaemonRefusal(t, err, endpointOf(t, daemon),
			"JSON-RPC id 99",
			"sent id "+checkDaemonToolCallID,
			"manage_checks")
	})

	// THE CONTROL runs the SAME field through the SAME stub with the id this
	// face actually sends. Without it the row above could be a stub that breaks
	// whenever responseID is set at all, rather than an assertion about the id.
	t.Run("control: the id that was sent", func(t *testing.T) {
		daemon := startFaultyDaemon(t, fakeDaemonFault{responseID: checkDaemonToolCallID})
		require.NoError(t, runCheckRunAgainst(t, daemon),
			"the same override carrying the id this face sent is the healthy answer, so the id is the only difference")
	})
}

// TestPostDaemon_RefusesAnEndpointItCannotBuildARequestFor observes postDaemon's
// one refusal that is not a transport failure: the request itself cannot be
// built. Every other row in this file drives the face end to end, and none of
// them can reach this one — the endpoint the face dials is rendered by
// checkDaemonEndpoint from a port number and is always well formed — so the leg
// is driven directly, with an endpoint carrying a byte no URL may hold.
//
// IT IS THE TEST THE CENSUS ROW NAMES for the error http.NewRequestWithContext
// returns. Without it that pass-through is a line that could be swallowed, and
// a caller would get a nil response and no reason.
func TestPostDaemon_RefusesAnEndpointItCannotBuildARequestFor(t *testing.T) {
	client, release := newDaemonMCPClient()
	defer release()

	resp, err := postDaemon(context.Background(), client, "http://127.0.0.1:1/\x7f", "", []byte("{}"))
	if resp != nil {
		_ = resp.Body.Close()
	}
	require.Error(t, err, "an endpoint no request can be built for is a refusal, never a silent nil error")
	assert.Nil(t, resp, "a request that was never built has no response to hand back")
	assert.Contains(t, err.Error(), "invalid control character in URL",
		"the refusal must carry what net/url objected to, or a caller cannot act on it")
}

// TestNewDaemonMCPClient_KeepsTheDialFailureOnTheChain observes the dialer's own
// pass-through, which is the one refusal in this file that a mutation survived
// when it was observed only through the end-to-end unreachable-daemon row: swallow
// the dial error there and the call still fails, so the outer refusal still names
// the endpoint and that row still passes.
//
// WHAT DISCRIMINATES IS THE CAUSE, not the failure. openDaemonSession wraps the
// transport error with %w precisely so a caller can tell "refused" from "timed
// out" (the reason is written at that arm), and the dial failure only reaches the
// chain because the dialer hands it back instead of returning a nil conn. So this
// asserts the chain still carries the dial operation.
func TestNewDaemonMCPClient_KeepsTheDialFailureOnTheChain(t *testing.T) {
	client, release := newDaemonMCPClient()
	defer release()

	// Port 1 is privileged and nothing listens on it under test, so the dial
	// fails fast and deterministically.
	resp, err := postDaemon(context.Background(), client, checkDaemonEndpoint(1), "", []byte("{}"))
	if resp != nil {
		_ = resp.Body.Close()
	}
	require.Error(t, err, "a dial to a port nothing listens on is a failure, never a response")

	var opErr *net.OpError
	require.ErrorAs(t, err, &opErr,
		"the dial failure must stay on the chain as a *net.OpError, or a caller cannot tell refused from timed out")
	assert.Equal(t, "dial", opErr.Op,
		"the operation on the chain must be the dial itself, not a later read off a connection that was never made")
}
