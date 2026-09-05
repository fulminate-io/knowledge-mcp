// SPDX-License-Identifier: Apache-2.0

package graphclient

import (
	"bytes"
	"compress/gzip"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/auth"
	"github.com/fulminate-io/knowledge-mcp/internal/clientver"
)

// cloud_refusal_encoding_test.go covers the refusal body ARRIVING UNDER A
// CONTENT-ENCODING on the connect transport — the shape that turned the
// gateway's instructive 426 into a shrug.
//
// WHY THIS IS CONNECT-SPECIFIC, and why the sibling refusal tests could not see
// it: net/http decompresses a response transparently ONLY when it added
// Accept-Encoding itself. The connect protocol sets `Accept-Encoding: gzip` on
// every unary call, so on this transport net/http hands the client the bytes as
// they arrived and the classifier must undo the encoding for itself. The sync
// transport sets no such header, so its responses arrive already decompressed
// and this case cannot be constructed there at all.

// capturedRefusalBody is the EXACT 109-byte body the dev gateway returned to a
// v0.8.3-stamped client on 2026-09-04, byte-identical on the connect route and
// the sync route. It is quoted verbatim rather than composed from a map so this
// test fails if the wire shape it was written against ever changes.
const capturedRefusalBody = `{"minimum":"v0.8.4","client_version":"v0.8.3","upgrade_command":"knowledge install","reason":"below_minimum"}`

// gzipBytes compresses b the way an HTTP proxy would.
func gzipBytes(t *testing.T, b []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	zw := gzip.NewWriter(&out)
	_, err := zw.Write(b)
	require.NoError(t, err)
	require.NoError(t, zw.Close())
	return out.Bytes()
}

// encodedRefusalServer serves a 426 carrying body under the given
// Content-Encoding, and records the Accept-Encoding the client asked for.
type encodedRefusalServer struct {
	body     []byte
	encoding string
	// sawAcceptEncoding is what the CLIENT asked for, recorded so a case can
	// prove the request that invites a compressed answer is the one connect
	// actually sends — rather than assuming it.
	sawAcceptEncoding string
}

func (h *encodedRefusalServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.sawAcceptEncoding = r.Header.Get("Accept-Encoding")
	w.Header().Set("Content-Type", "application/json")
	if h.encoding != "" {
		w.Header().Set("Content-Encoding", h.encoding)
	}
	w.WriteHeader(http.StatusUpgradeRequired)
	// G705 (gosec XSS-via-taint) flags w.Write(field) conservatively: body is a
	// canned refusal payload fixed at construction, never request-derived.
	_, _ = w.Write(h.body) //nolint:gosec // G705 false positive: canned refusal bytes, not request-derived
}

// TestCloudRoundTripper_GzippedRefusalCarriesTheRemedy is the defect: the
// gateway's instructive refusal arrived gzipped on the connect transport and the
// client reported it as a body it could not read, losing the minimum and the
// upgrade command the gateway had composed for the user.
//
// THE ACCEPT-ENCODING ASSERTION IS PART OF THE CASE, not decoration. It is what
// makes the fixture faithful rather than invented: this transport ASKS for gzip,
// which is precisely why net/http leaves the body encoded and why a compressed
// refusal is reachable in production at all.
func TestCloudRoundTripper_GzippedRefusalCarriesTheRemedy(t *testing.T) {
	clientver.ClearRefusal()
	t.Cleanup(clientver.ClearRefusal)

	h := &encodedRefusalServer{body: gzipBytes(t, []byte(capturedRefusalBody)), encoding: "gzip"}
	srv := httptest.NewServer(h)
	t.Cleanup(func() { srv.CloseClientConnections(); srv.Close() })

	gc := closeIdleOnCleanup(t, NewCloudGraphClient(srv.URL, auth.StaticTokenSource{AccessToken: "tok"}))
	_, execErr := gc.Execute(opCtx(), &knowledgev1.ExecuteRequest{})
	require.Error(t, execErr, "a gateway refusal must fail the call, never degrade it")

	assert.Contains(t, h.sawAcceptEncoding, "gzip",
		"the connect protocol asks for gzip on every unary call, which is what turns OFF net/http's "+
			"transparent decompression and leaves the classifier holding encoded bytes")

	got, ok := clientver.CurrentRefusal()
	require.True(t, ok, "the refusal must latch so the status surfaces can report it")
	assert.Equal(t, clientver.ReasonBelowMinimum, got.Reason,
		"the GATEWAY's reason must survive the transport; reporting this client's own parse failure "+
			"instead sends the reader to the wrong repo")
	assert.Equal(t, "v0.8.4", got.Minimum, "the minimum is the fact a refused user needs")
	assert.Equal(t, "v0.8.3", got.ClientVersion, "the gateway echoes the version it judged")
	assert.Equal(t, "knowledge install", got.UpgradeCommand, "and the remedy it composed")
	assert.Empty(t, got.Diagnostic,
		"a refusal the gateway explained carries no local diagnostic — there is nothing on this side to explain")

	for _, want := range []string{"v0.8.4", "v0.8.3", "knowledge install", clientver.ReasonBelowMinimum} {
		assert.Contains(t, execErr.Error(), want,
			"the surfaced error must carry the remedy the gateway composed")
	}
}

// TestCloudRoundTripper_PlainRefusalStillCarriesTheRemedy is the same-run control
// for the case above: the identical bytes with NO Content-Encoding. Without it, a
// decoder that mangled every uncompressed body would still satisfy the gzip case.
func TestCloudRoundTripper_PlainRefusalStillCarriesTheRemedy(t *testing.T) {
	clientver.ClearRefusal()
	t.Cleanup(clientver.ClearRefusal)

	h := &encodedRefusalServer{body: []byte(capturedRefusalBody)}
	srv := httptest.NewServer(h)
	t.Cleanup(func() { srv.CloseClientConnections(); srv.Close() })

	gc := closeIdleOnCleanup(t, NewCloudGraphClient(srv.URL, auth.StaticTokenSource{AccessToken: "tok"}))
	_, execErr := gc.Execute(opCtx(), &knowledgev1.ExecuteRequest{})
	require.Error(t, execErr)

	got, ok := clientver.CurrentRefusal()
	require.True(t, ok)
	assert.Equal(t, clientver.ReasonBelowMinimum, got.Reason)
	assert.Equal(t, "v0.8.4", got.Minimum)
	assert.Equal(t, "knowledge install", got.UpgradeCommand)
}

// TestCloudRoundTripper_UndecodableEncodingIsALocalFault pins the arm that must
// stay a refusal without becoming a guess: an encoding this client cannot undo.
//
// IT MUST NOT PASS THE ENCODED BYTES THROUGH to the JSON parser. Doing so is the
// original defect one encoding later — the parse fails, the refusal is reported
// as unreadable, and nothing says why. The reason names THIS side, and the
// diagnostic names the transport, the encoding and what arrived.
func TestCloudRoundTripper_UndecodableEncodingIsALocalFault(t *testing.T) {
	clientver.ClearRefusal()
	t.Cleanup(clientver.ClearRefusal)

	// Brotli: a real Content-Encoding, and one this client has no decoder for.
	h := &encodedRefusalServer{body: []byte(capturedRefusalBody), encoding: "br"}
	srv := httptest.NewServer(h)
	t.Cleanup(func() { srv.CloseClientConnections(); srv.Close() })

	gc := closeIdleOnCleanup(t, NewCloudGraphClient(srv.URL, auth.StaticTokenSource{AccessToken: "tok"}))
	_, execErr := gc.Execute(opCtx(), &knowledgev1.ExecuteRequest{})
	require.Error(t, execErr, "an unreadable refusal is still a refusal, never an admission")

	got, ok := clientver.CurrentRefusal()
	require.True(t, ok)
	assert.Equal(t, clientver.ReasonRefusalBodyUnparseable, got.Reason,
		"the fault is on THIS side and the reason must say so, rather than borrowing the gateway's "+
			"name for an unparseable client version")
	for _, want := range []string{"connect", `"br"`} {
		assert.Contains(t, got.Diagnostic, want,
			"the diagnostic must name the transport that lost the body and the encoding it claimed: %q", got.Diagnostic)
	}
	assert.Contains(t, execErr.Error(), "could not read the refusal",
		"and the surfaced error must say this client could not read it, rather than reporting an empty "+
			"minimum as if it were an answer: %v", execErr)
}

// TestCloudRoundTripper_CorruptGzipIsALocalFault covers the other half of the
// decode arm: an encoding this client DOES support, carrying bytes that are not
// that encoding. A decoder that returned its input on a decompression failure
// would land back on the original defect.
func TestCloudRoundTripper_CorruptGzipIsALocalFault(t *testing.T) {
	clientver.ClearRefusal()
	t.Cleanup(clientver.ClearRefusal)

	h := &encodedRefusalServer{body: []byte(capturedRefusalBody), encoding: "gzip"}
	srv := httptest.NewServer(h)
	t.Cleanup(func() { srv.CloseClientConnections(); srv.Close() })

	gc := closeIdleOnCleanup(t, NewCloudGraphClient(srv.URL, auth.StaticTokenSource{AccessToken: "tok"}))
	_, execErr := gc.Execute(opCtx(), &knowledgev1.ExecuteRequest{})
	require.Error(t, execErr)

	got, ok := clientver.CurrentRefusal()
	require.True(t, ok)
	assert.Equal(t, clientver.ReasonRefusalBodyUnparseable, got.Reason)
	assert.Contains(t, got.Diagnostic, "gzip",
		"the diagnostic must name the encoding that failed: %q", got.Diagnostic)
	assert.Contains(t, got.Diagnostic, "minimum",
		"and quote what actually arrived, so a reader can see it was JSON mislabelled as gzip: %q", got.Diagnostic)
}
