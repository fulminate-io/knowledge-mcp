// SPDX-License-Identifier: Apache-2.0

package graphclient

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/auth"
	"github.com/fulminate-io/knowledge-mcp/internal/clientver"
)

// refusalReasons is the reason table the connect transport must latch: the four
// originals, the two the gateway adds for unproven states, and one this repo
// has never heard of. The last is the load-bearing row — the vocabulary lives
// in the gateway's repo and can grow without a release here.
var refusalReasons = []string{
	clientver.ReasonBelowMinimum,
	clientver.ReasonHeaderAbsent,
	clientver.ReasonUnparseable,
	clientver.ReasonDevStampNotAllowlisted,
	clientver.ReasonUnverified,
	clientver.ReasonUnprovable,
	"quantum_flux_not_allowlisted",
}

// plainGatewayRefusal serves a 426 the way the AGENT GATEWAY does: a plain HTTP
// response with a JSON body, emitted by a proxy that never spoke connect's wire
// format. It is deliberately NOT a connect-encoded error — that difference is
// the whole reason the classification lives in the round-tripper, and a stub
// that returned a connect error would test a path the real gateway never takes.
type plainGatewayRefusal struct {
	body []byte
}

func (h *plainGatewayRefusal) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUpgradeRequired)
	// G705 (gosec XSS-via-taint) flags w.Write(field) conservatively: body is a
	// canned refusal payload fixed at construction, never request-derived, and
	// it is served under application/json by an httptest server to exercise the
	// round-tripper's classification. No untrusted source, no XSS surface.
	_, _ = w.Write(h.body) //nolint:gosec // G705 false positive: canned refusal JSON, not request-derived
}

// TestCloudRoundTripper_LatchesMinimumVersionRefusal drives the connect cloud
// transport against a gateway refusal arriving as a NON-CONNECT-PROTOCOL
// response, and asserts the call fails instructively and the refusal latches
// for every reason the gateway can emit.
func TestCloudRoundTripper_LatchesMinimumVersionRefusal(t *testing.T) {
	for _, reason := range refusalReasons {
		t.Run(reason, func(t *testing.T) {
			clientver.ClearRefusal()

			body, err := json.Marshal(map[string]string{
				"minimum":         "2.0.0",
				"client_version":  "1.0.0",
				"platform":        "linux-amd64",
				"upgrade_command": "knowledge install",
				"reason":          reason,
			})
			require.NoError(t, err)

			h := &plainGatewayRefusal{body: body}
			srv := httptest.NewServer(h)
			t.Cleanup(func() { srv.CloseClientConnections(); srv.Close() })

			// Driven through NewCloudGraphClient, not a hand-built
			// round-tripper: the constructor is what installs the account
			// selection, and a literal without one would take an early return
			// and observe nothing — a false red that invites widening a
			// production guard to accommodate a test shortcut.
			gc := closeIdleOnCleanup(t, NewCloudGraphClient(srv.URL, auth.StaticTokenSource{AccessToken: "tok"}))

			_, execErr := gc.Execute(opCtx(), &knowledgev1.ExecuteRequest{})
			require.Error(t, execErr, "a gateway refusal must fail the call, never degrade it")
			for _, want := range []string{"2.0.0", "1.0.0", "knowledge install"} {
				assert.Contains(t, execErr.Error(), want,
					"the surfaced error must carry the remedy; connect-go alone reports this refusal with an EMPTY message")
			}

			got, ok := clientver.CurrentRefusal()
			require.True(t, ok, "the refusal must latch so the status surfaces can report it")
			assert.Equal(t, reason, got.Reason,
				"an unrecognized reason is latched VERBATIM, never dropped and never coerced to a known member")
			assert.Equal(t, "2.0.0", got.Minimum)
			assert.Equal(t, "1.0.0", got.ClientVersion)
			assert.Equal(t, "knowledge install", got.UpgradeCommand)
			assert.False(t, got.At.IsZero())

			clientver.ClearRefusal()
			_, stillSet := clientver.CurrentRefusal()
			assert.False(t, stillSet, "ClearRefusal must empty the latch")
		})
	}
}

// TestCloudRoundTripper_UnparseableRefusalStillLatches proves the connect side
// treats a 426 with an unreadable body as a refusal rather than swallowing it.
func TestCloudRoundTripper_UnparseableRefusalStillLatches(t *testing.T) {
	clientver.ClearRefusal()
	h := &plainGatewayRefusal{body: []byte(`<html>gateway said no</html>`)}
	srv := httptest.NewServer(h)
	t.Cleanup(func() { srv.CloseClientConnections(); srv.Close() })

	gc := closeIdleOnCleanup(t, NewCloudGraphClient(srv.URL, auth.StaticTokenSource{AccessToken: "tok"}))
	_, err := gc.Execute(opCtx(), &knowledgev1.ExecuteRequest{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "could not read the refusal",
		"the error should say this client could not read the body rather than report an empty minimum as an answer: %v", err)

	got, ok := clientver.CurrentRefusal()
	require.True(t, ok, "an unparseable 426 is still a refusal, never an admission")
	assert.Equal(t, clientver.ReasonRefusalBodyUnparseable, got.Reason,
		"the LOCAL reason, not the gateway's version_unparseable — the two mean opposite things")
	assert.Contains(t, got.Diagnostic, "connect", "the diagnostic names the transport that lost the body")
	clientver.ClearRefusal()
}

// TestCloudRoundTripper_NonRefusalStatusDoesNotLatch is the discriminating
// control for the connect leg: the same client, the same handler shape, a
// different status. Without it, the latch assertions above are equally
// consistent with a classifier that latches on any non-2xx.
func TestCloudRoundTripper_NonRefusalStatusDoesNotLatch(t *testing.T) {
	clientver.ClearRefusal()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"account_forbidden"}`))
	}))
	t.Cleanup(func() { srv.CloseClientConnections(); srv.Close() })

	gc := closeIdleOnCleanup(t, NewCloudGraphClient(srv.URL, auth.StaticTokenSource{AccessToken: "tok"}))
	_, _ = gc.Execute(opCtx(), &knowledgev1.ExecuteRequest{})

	_, latched := clientver.CurrentRefusal()
	assert.False(t, latched, "a 403 account rejection must not latch a VERSION refusal")
}

// trackedBody records whether it was read to EOF and whether it was closed.
type trackedBody struct {
	rest   []byte
	atEOF  bool
	closed bool
}

func (b *trackedBody) Read(p []byte) (int, error) {
	if len(b.rest) == 0 {
		b.atEOF = true
		return 0, io.EOF
	}
	n := copy(p, b.rest)
	b.rest = b.rest[n:]
	return n, nil
}

func (b *trackedBody) Close() error { b.closed = true; return nil }

// TestClassifyGatewayRejection_DrainsAndClosesTheBody pins the connection-leak
// property directly, because it is invisible from the caller's side.
//
// io.LimitReader stops at the cap WITHOUT reaching EOF, so a rejection body
// larger than MaxErrorBodyBytes would leave the original body unread and
// unclosed and the underlying connection would never be released. The body here
// is deliberately LARGER than the cap so the LimitReader genuinely stops short
// — a body under the cap would reach EOF on its own and the assertion would
// pass against an implementation that never drained.
func TestClassifyGatewayRejection_DrainsAndClosesTheBody(t *testing.T) {
	clientver.ClearRefusal()
	t.Cleanup(clientver.ClearRefusal)

	payload := append([]byte(`{"reason":"below_minimum","minimum":"2.0.0","upgrade_command":"knowledge install","padding":"`),
		bytes.Repeat([]byte("x"), auth.MaxErrorBodyBytes*2)...)
	payload = append(payload, []byte(`"}`)...)
	require.Greater(t, len(payload), auth.MaxErrorBodyBytes,
		"the fixture must exceed the cap or the LimitReader would reach EOF unaided and the drain assertion would be vacuous")

	body := &trackedBody{rest: payload}
	req, err := http.NewRequest(http.MethodPost, "https://example.invalid/knowledge.v1.EngineService/Execute", nil)
	require.NoError(t, err)
	resp := &http.Response{StatusCode: http.StatusUpgradeRequired, Body: body, Request: req}

	b := &bearerRoundTripper{}
	refusal := b.classifyGatewayRejection(t.Context(), resp)

	require.Error(t, refusal, "a 426 must surface as an error even with no account selection installed")
	assert.True(t, body.atEOF, "the original body must be drained to EOF or the connection is never released")
	assert.True(t, body.closed, "the original body must be closed")
}
