// SPDX-License-Identifier: Apache-2.0

package graphclient

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/clientver"
)

// headerCaptureHandler records a full copy of every request's headers and
// returns 401 once when armed, so one test can inspect the FIRST dispatch and
// the 401-refresh RETRY separately. cloudCaptureHandler records only the
// Authorization value, which cannot answer this question.
type headerCaptureHandler struct {
	mu       sync.Mutex
	seen     []http.Header
	pending4 int
}

func (h *headerCaptureHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	h.seen = append(h.seen, r.Header.Clone())
	retry := h.pending4 > 0
	if retry {
		h.pending4--
	}
	h.mu.Unlock()

	if retry {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`unauthorized`))
		return
	}
	w.Header().Set("Content-Type", "application/proto")
	w.WriteHeader(http.StatusOK)
}

func (h *headerCaptureHandler) observed() []http.Header {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]http.Header, len(h.seen))
	copy(out, h.seen)
	return out
}

// TestCloudRoundTripper_StampsClientVersionAndPlatform proves the connect cloud
// transport carries the client's build identity on EVERY dispatch — the first
// send and the 401-refresh retry alike.
//
// The retry leg is the one that matters: an implementation that stamped in
// RoundTrip before the first dispatch, rather than inside the per-attempt
// clone, would compile, pass a single-request test, and silently ship an
// unstamped retry. Under a gateway that refuses an unstamped request, that
// retry is not a missing observability field — it is a dead request.
func TestCloudRoundTripper_StampsClientVersionAndPlatform(t *testing.T) {
	oldVer := clientver.Version
	t.Cleanup(func() { clientver.Version = oldVer })
	clientver.Version = "9.9.9-stamp-test"

	h := &headerCaptureHandler{pending4: 1}
	srv := httptest.NewServer(h)
	t.Cleanup(func() { srv.CloseClientConnections(); srv.Close() })

	src := &cloudRefresher{current: "tok-stale"}
	gc := closeIdleOnCleanup(t, NewCloudGraphClient(srv.URL, src))

	// The response payload never decodes; the assertion is on what the server
	// observed, exactly as the sibling bearer tests in this package do.
	_, _ = gc.Execute(opCtx(), &knowledgev1.ExecuteRequest{})

	got := h.observed()
	require.GreaterOrEqual(t, len(got), 2,
		"expected the first dispatch plus the 401-refresh retry, got %d request(s)", len(got))

	for i, hdr := range got[:2] {
		leg := "first dispatch"
		if i == 1 {
			leg = "401-refresh retry"
		}
		assert.Equal(t, clientver.Version, hdr.Get(clientver.HeaderVersion),
			"%s must carry %s", leg, clientver.HeaderVersion)
		assert.Equal(t, clientver.Platform(), hdr.Get(clientver.HeaderPlatform),
			"%s must carry %s", leg, clientver.HeaderPlatform)
	}

	// KNOWN-POSITIVE CONTROL for the value, not merely the key: the header must
	// carry the package var's value, so a hardcoded constant or an empty string
	// is distinguishable from a real stamp.
	assert.NotEqual(t, "dev", got[0].Get(clientver.HeaderVersion),
		"the header must reflect clientver.Version (overridden to %q here), not a compiled-in default",
		clientver.Version)

	// The stamp must not have displaced the credential it sits beside.
	assert.Equal(t, "Bearer tok-stale", got[0].Get("Authorization"))
	assert.Equal(t, "Bearer tok-rotated", got[1].Get("Authorization"))
}

// TestCloudRoundTripper_StampReplacesACallerSuppliedVersion proves the stamp is
// authoritative rather than additive: a request that already carried a version
// header leaves with exactly one, holding this client's value. Header.Add
// semantics would ship two values and let a caller forge the older one.
func TestCloudRoundTripper_StampReplacesACallerSuppliedVersion(t *testing.T) {
	oldVer := clientver.Version
	t.Cleanup(func() { clientver.Version = oldVer })
	clientver.Version = "2.0.0"

	req, err := http.NewRequest(http.MethodPost, "https://example.invalid/x", nil)
	require.NoError(t, err)
	req.Header.Set(clientver.HeaderVersion, "0.0.1-forged")

	clone := cloneRequestWithBearer(req, nil, "tok")

	assert.Equal(t, []string{"2.0.0"}, clone.Header.Values(clientver.HeaderVersion),
		"the stamp replaces a caller-supplied value instead of appending beside it")
}
