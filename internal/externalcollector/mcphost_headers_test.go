// SPDX-License-Identifier: Apache-2.0

package externalcollector

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mcphost_headers_test.go — R3: what an http provider receives, and what it must
// never receive.
//
// EVERY ASSERTION IS ACROSS EVERY REQUEST THE COLLECT MADE. A header attached to
// the handshake alone, or lost after it, would satisfy a check that looked at one
// request; the recorder holds them all for exactly that reason.

// TestRunMCP_HTTPSendsTheEntrysHeaders is R3's positive arm.
func TestRunMCP_HTTPSendsTheEntrysHeaders(t *testing.T) {
	url, recorder := startHTTPStub(t, stubModeConforming)
	entryHeaders := map[string]string{
		"Authorization": "Bearer ful1794-operator-token",
		"X-Tenant":      "acme",
	}
	_, _, err := RunMCP(context.Background(), httpDefHeaders(url, entryHeaders), nil, "board", nil)
	require.NoError(t, err)

	seen := recorder.all()
	require.GreaterOrEqual(t, len(seen), 2, "the collect must have made at least the handshake and the call")
	for i, h := range seen {
		assert.Equal(t, "Bearer ful1794-operator-token", h.Get("Authorization"),
			"request %d did not carry the entry's Authorization header", i)
		assert.Equal(t, "acme", h.Get("X-Tenant"), "request %d did not carry the entry's X-Tenant header", i)
	}
}

// TestRunMCP_HTTPSendsNoHeaderTheEntryDidNotName is R3's negative arm, and it is
// the row the retired "v1 sends no Authorization header" test became.
//
// THE CLAIM CHANGED SHAPE, NOT STRENGTH. It used to be that no Authorization
// header is ever sent; it is now that no header the ENTRY did not name is ever
// sent, which is the same guarantee about the daemon and a weaker one about the
// operator, who may now configure their own. The plant is what makes it real: an
// Authorization value sits in the daemon's OWN environment under the name a
// credential-composing implementation would reach for, and it must not appear.
func TestRunMCP_HTTPSendsNoHeaderTheEntryDidNotName(t *testing.T) {
	t.Setenv("KNOWLEDGE_COLLECTOR_TOKEN", daemonSideSecret)
	t.Setenv("HTTP_PROXY", "http://user:pw@proxy.invalid:3128")

	// THE BASELINE IS MEASURED AND THEN CHECKED AGAINST A PINNED LIST, and BOTH
	// halves are load-bearing.
	//
	// MEASURING ALONE IS NOT ENOUGH, and the first version of this test proved
	// it: a header the daemon composes UNCONDITIONALLY appears in the baseline
	// too, so subtracting the baseline cancels it out and the run stays green.
	// Measured — a `maps.Copy(req.Header, http.Header{"X-Tok": ...os.Getenv...})`
	// planted in boundedTransport.RoundTrip passed a baseline-only assertion.
	// A subject that supplies its own answer key proves nothing.
	//
	// PINNING ALONE IS NOT ENOUGH EITHER: a hand list silently widens the day the
	// SDK adds a header. So the list is a TRIPWIRE ON THE BASELINE rather than
	// the assertion about the entry's run — an unexpected baseline name fails
	// loudly and names itself, and the author then decides whether it is the
	// SDK's or ours. The names below are my own run at this commit; mcp-method is
	// the one the reviewer's hand list did not have.
	baseURL, baseRecorder := startHTTPStub(t, stubModeConforming)
	_, _, err := RunMCP(context.Background(), httpDef(baseURL), nil, "board", nil)
	require.NoError(t, err)
	protocol := headerNameSet(baseRecorder.all())
	require.Equal(t, protocolHeaderNames, sortedNames(protocol),
		"a collect whose entry names NO headers must carry the protocol's own and nothing else; "+
			"an unexpected name here is either a header this daemon composed or one the SDK started sending, and the two are not the same finding")

	url, recorder := startHTTPStub(t, stubModeConforming)
	_, _, err = RunMCP(context.Background(), httpDefHeaders(url, map[string]string{"X-Tenant": "acme"}), nil, "board", nil)
	require.NoError(t, err)

	seen := recorder.all()
	require.GreaterOrEqual(t, len(seen), 2, "the collect must have made at least the handshake and the call")
	for i, h := range seen {
		// THE KNOWN-POSITIVE, in the same run: the entry's own header IS present,
		// so the set assertion below is read through an instrument that works.
		require.Equal(t, "acme", h.Get("X-Tenant"), "request %d lost the entry's own header", i)
	}

	// THE ASSERTION IS ON THE SET, NOT ON TWO NAMES. The property is "no header
	// the ENTRY did not name appears on any request", and checking Authorization
	// and Proxy-Authorization alone admits a daemon-composed header under every
	// other name — measured: a maps.Copy adding X-Tok from the daemon's
	// environment left this whole suite green.
	want := make(map[string]struct{}, len(protocolHeaderNames)+1)
	for _, name := range protocolHeaderNames {
		want[name] = struct{}{}
	}
	want["x-tenant"] = struct{}{}
	assert.Equal(t, sortedNames(want), sortedNames(headerNameSet(seen)),
		"every header name on the wire must be the protocol's own or one the ENTRY named")

	// THE NAME SET LEAVES ONE HOLE, AND THIS CLOSES IT. The eight protocol names
	// are in the expected set by construction, so a daemon value smuggled out
	// under one of them — User-Agent, Accept-Encoding — passes the assertion
	// above. Measured: both did. So the VALUES are checked too, against the
	// daemon-side secret this test already plants.
	//
	// THE KNOWN-POSITIVE IS THE ENTRY'S OWN VALUE: the loop can see values at
	// all, so a clean sweep is evidence rather than an empty walk.
	sawEntryValue := false
	for i, h := range seen {
		for name, values := range h {
			for _, value := range values {
				assert.NotContains(t, value, daemonSideSecret,
					"request %d carried the daemon's own secret in header %q", i, name)
				if value == "acme" {
					sawEntryValue = true
				}
			}
		}
	}
	require.True(t, sawEntryValue, "the loop never saw the entry's own header value, so the sweep above walked nothing")
}

// daemonSideSecret is the value this test plants in the daemon's own
// environment. It is a constant so the name-set assertion and the value sweep
// are provably about the same string.
const daemonSideSecret = "ful1794-daemon-side-secret"

// protocolHeaderNames are the header names a collect carries when its entry
// names none: the MCP SDK's and net/http's own. Read from a run at this commit,
// sorted, and asserted against the baseline so a change on either side — the
// SDK's or ours — fails by name rather than widening the set this test subtracts.
var protocolHeaderNames = []string{
	"accept", "accept-encoding", "content-length", "content-type",
	"mcp-method", "mcp-protocol-version", "mcp-session-id", "user-agent",
}

// headerNameSet is the union of header names across every recorded request,
// lower-cased. The UNION rather than a per-request set: a header the daemon
// composed on one request of three is the defect, and an intersection would
// hide it.
func headerNameSet(seen []http.Header) map[string]struct{} {
	out := map[string]struct{}{}
	for _, h := range seen {
		for name := range h {
			out[strings.ToLower(name)] = struct{}{}
		}
	}
	return out
}

// sortedNames renders a name set for a comparison and for a failure message.
func sortedNames(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for name := range set {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// TestProviderTransport_HTTPBuildsNoCommand is R3's third clause: an http
// provider is DIALED, never spawned.
//
// THE OBSERVABLE IS THE TRANSPORT'S TYPE, taken from the one function that
// decides it, with the stdio arm in the same run as the known-positive. Counting
// this process's children would be the more direct instrument and a far worse
// one: the child is reaped before RunMCP returns, so the count is back to its
// starting value by the time anything could read it, and a probe that always
// reads zero has no discriminating power at all.
func TestProviderTransport_HTTPBuildsNoCommand(t *testing.T) {
	url, _ := startHTTPStub(t, stubModeConforming)

	httpTr, err := providerTransport(context.Background(), httpDef(url).Def.GetCollector(), nil)
	require.NoError(t, err)
	_, isCommand := httpTr.(*mcp.CommandTransport)
	assert.False(t, isCommand, "an http entry must not build a command transport — nothing is spawned for it")
	_, isStreamable := httpTr.(*mcp.StreamableClientTransport)
	assert.True(t, isStreamable, "an http entry dials a streamable-HTTP endpoint")

	// THE CONTROL: the same function on the stdio arm DOES build a command,
	// without which the assertion above is satisfied by any implementation.
	stdioTr, err := providerTransport(context.Background(), stdioDef(t, stubModeConforming).Def.GetCollector(), nil)
	require.NoError(t, err)
	cmdTr, isCommand := stdioTr.(*mcp.CommandTransport)
	require.True(t, isCommand, "a stdio entry builds a command transport")
	assert.NotNil(t, cmdTr.Command, "and that transport carries the child it will spawn")
}
