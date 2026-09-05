// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gorilla/websocket"

	"github.com/fulminate-io/knowledge-mcp/internal/clientver"
)

// tunnelHeaderRecorder captures the headers of every request it serves.
type tunnelHeaderRecorder struct {
	mu   sync.Mutex
	seen []http.Header
}

func (r *tunnelHeaderRecorder) record(h http.Header) {
	r.mu.Lock()
	r.seen = append(r.seen, h.Clone())
	r.mu.Unlock()
}

func (r *tunnelHeaderRecorder) first(t *testing.T) http.Header {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.seen) == 0 {
		t.Fatalf("the stub server observed no request at all, so this assertion measures nothing")
	}
	return r.seen[0]
}

// assertStamped checks BOTH client-identity headers against what clientver
// reports, rather than against literals — a hand-rolled pair at the call site
// that drifted from the shared implementation would still satisfy a literal
// comparison, and the census would not notice because the site stays listed.
func assertStamped(t *testing.T, leg string, h http.Header) {
	t.Helper()
	if got := h.Get(clientver.HeaderVersion); got != clientver.Version {
		t.Errorf("%s: %s = %q, want clientver.Version %q", leg, clientver.HeaderVersion, got, clientver.Version)
	}
	if got := h.Get(clientver.HeaderPlatform); got != clientver.Platform() {
		t.Errorf("%s: %s = %q, want clientver.Platform() %q", leg, clientver.HeaderPlatform, got, clientver.Platform())
	}
}

// TestTunnelRequests_StampClientVersionAndPlatform covers BOTH tunnel paths.
//
// The websocket half is the one a partial edit misses: the certificate POST is
// an obvious request, while the relay dial passed a NIL header map and names no
// endpoint constant in an argument, so only the shape half of the call-path
// census could see it at all.
func TestTunnelRequests_StampClientVersionAndPlatform(t *testing.T) {
	oldVer := clientver.Version
	t.Cleanup(func() { clientver.Version = oldVer })
	clientver.Version = "4.4.4-tunnel-test"

	t.Run("the certificate POST carries both headers beside its hand-rolled auth", func(t *testing.T) {
		rec := &tunnelHeaderRecorder{}
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rec.record(r.Header)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(connectResponse{Certificate: "cert-bytes"})
		}))
		t.Cleanup(srv.Close)

		cert, _, _, err := fetchCert(t.Context(), srv.Client(), srv.URL, "tok", "ssh-ed25519 AAAA", "dev")
		if err != nil {
			t.Fatalf("fetchCert: %v", err)
		}
		if cert != "cert-bytes" {
			t.Fatalf("certificate = %q", cert)
		}

		h := rec.first(t)
		assertStamped(t, "certificate POST", h)
		// The stamp sits BESIDE the headers this path already set; it must not
		// have displaced either.
		if got := h.Get("Authorization"); got != "Bearer tok" {
			t.Errorf("certificate POST lost its bearer: Authorization = %q", got)
		}
		if got := h.Get("Content-Type"); got != "application/json" {
			t.Errorf("certificate POST lost its content type: %q", got)
		}
	})

	t.Run("the websocket handshake carries both headers", func(t *testing.T) {
		rec := &tunnelHeaderRecorder{}
		upgrader := websocket.Upgrader{}
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rec.record(r.Header)
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				return
			}
			_ = conn.Close()
		}))
		t.Cleanup(srv.Close)

		wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
		// The dial is what is under test; the pump afterwards ends as soon as
		// the stub closes, and its error is not this test's subject.
		_ = proxyOverWS(t.Context(), wsURL, proxyHeader{}, strings.NewReader(""), &strings.Builder{})

		// THIS IS THE ASSERTION THAT FAILS IF THE DIAL STILL PASSES nil.
		assertStamped(t, "websocket handshake", rec.first(t))
	})

	t.Run("a 426 on the certificate POST names the remedy", func(t *testing.T) {
		clientver.ClearRefusal()
		t.Cleanup(clientver.ClearRefusal)

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUpgradeRequired)
			_, _ = w.Write([]byte(`{"minimum":"9.0.0","client_version":"4.4.4","upgrade_command":"knowledge install","reason":"below_minimum"}`))
		}))
		t.Cleanup(srv.Close)

		_, _, _, err := fetchCert(t.Context(), srv.Client(), srv.URL, "tok", "ssh-ed25519 AAAA", "dev")
		if err == nil {
			t.Fatalf("a 426 must fail the certificate fetch")
		}
		for _, want := range []string{"9.0.0", "4.4.4", "knowledge install"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("the refusal omits %q, so the user has no remedy: %v", want, err)
			}
		}
		if _, latched := clientver.CurrentRefusal(); !latched {
			t.Errorf("the tunnel refusal did not latch, so no status surface can report it")
		}
	})

	t.Run("a 426 on the websocket handshake names the remedy", func(t *testing.T) {
		clientver.ClearRefusal()
		t.Cleanup(clientver.ClearRefusal)

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUpgradeRequired)
			_, _ = w.Write([]byte(`{"minimum":"9.0.0","client_version":"4.4.4","upgrade_command":"knowledge install","reason":"below_minimum"}`))
		}))
		t.Cleanup(srv.Close)

		wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
		err := proxyOverWS(t.Context(), wsURL, proxyHeader{}, strings.NewReader(""), &strings.Builder{})
		if err == nil {
			t.Fatalf("a 426 handshake must fail the dial")
		}
		for _, want := range []string{"9.0.0", "knowledge install"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("the handshake refusal omits %q: %v", want, err)
			}
		}
	})

	// THE CONTROL for the refusal legs: an ordinary non-2xx that is NOT a
	// version refusal must keep its existing, already-actionable message rather
	// than being swallowed by the new arm.
	t.Run("a non-426 failure keeps its existing actionable error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusForbidden)
		}))
		t.Cleanup(srv.Close)

		_, _, _, err := fetchCert(t.Context(), srv.Client(), srv.URL, "tok", "ssh-ed25519 AAAA", "dev")
		if err == nil {
			t.Fatalf("a 403 must still fail")
		}
		if !strings.Contains(err.Error(), "no matching dev environment") {
			t.Errorf("the 403 arm lost its message: %v", err)
		}
	})
}
