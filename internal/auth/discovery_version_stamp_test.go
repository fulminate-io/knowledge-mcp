// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/clientver"
)

// legRecorder captures the headers one discovery leg was served with.
type legRecorder struct {
	mu     sync.Mutex
	served bool
	hdr    http.Header
}

func (r *legRecorder) record(h http.Header) {
	r.mu.Lock()
	r.served, r.hdr = true, h.Clone()
	r.mu.Unlock()
}

func (r *legRecorder) snapshot(t *testing.T, leg string) http.Header {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.served {
		t.Fatalf("the %s leg was never served, so any assertion about its headers measures nothing", leg)
	}
	return r.hdr
}

// TestDiscovery_StampsClientVersionOnTheFulminateLeg drives the real two-step
// discovery chain against TWO distinct stub servers and asserts the two legs in
// OPPOSITE directions.
//
// Both halves are required and they fail in opposite directions: stamping
// inside the shared helper passes the first and fails the second, and leaving
// the helper untouched fails the first. That opposition is the whole point —
// one helper serves both legs, and only one of the two targets is ours.
func TestDiscovery_StampsClientVersionOnTheFulminateLeg(t *testing.T) {
	oldVer := clientver.Version
	t.Cleanup(func() { clientver.Version = oldVer })
	clientver.Version = "5.5.5-discovery-test"

	newChain := func(t *testing.T, authStatus int) (fulminate, authKit *legRecorder, endpoint string, allowed map[string]struct{}) {
		t.Helper()
		fulminate, authKit = &legRecorder{}, &legRecorder{}

		// TLS stubs on BOTH legs: validateAuthServerHost requires https for the
		// discovered authorization server, and that guard is production
		// behavior this test must not weaken to make a fixture convenient.
		authSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authKit.record(r.Header)
			if authStatus != http.StatusOK {
				w.WriteHeader(authStatus)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(AuthorizationServerMetadata{
				Issuer:                "https://issuer.example.invalid",
				AuthorizationEndpoint: "https://issuer.example.invalid/authorize",
				TokenEndpoint:         "https://issuer.example.invalid/token",
			})
		}))
		t.Cleanup(authSrv.Close)

		fulSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fulminate.record(r.Header)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(ProtectedResourceMetadata{
				Resource:             "https://api.example.invalid",
				AuthorizationServers: []string{authSrv.URL},
			})
		}))
		t.Cleanup(fulSrv.Close)

		// Point the package's OAuth client at the stubs' own trust roots. Both
		// stubs share the httptest CA, so one client serves both legs.
		prevClient := oauthHTTPClient
		oauthHTTPClient = authSrv.Client()
		t.Cleanup(func() { oauthHTTPClient = prevClient })

		u, err := url.Parse(authSrv.URL)
		if err != nil {
			t.Fatalf("parse auth server url: %v", err)
		}
		// The allowlist keys on HOSTNAME, not host:port.
		return fulminate, authKit, fulSrv.URL, map[string]struct{}{u.Hostname(): {}}
	}

	t.Run("the fulminate leg is stamped and the AuthKit leg is NOT", func(t *testing.T) {
		ful, authKit, endpoint, allowed := newChain(t, http.StatusOK)

		if _, err := Discover(t.Context(), endpoint, allowed); err != nil {
			t.Fatalf("Discover: %v", err)
		}

		// OURS — stamped, and asserted against what clientver reports rather
		// than a literal, so a hand-rolled pair that drifted still fails.
		fh := ful.snapshot(t, "protected-resource")
		if got := fh.Get(clientver.HeaderVersion); got != clientver.Version {
			t.Errorf("protected-resource leg: %s = %q, want %q", clientver.HeaderVersion, got, clientver.Version)
		}
		if got := fh.Get(clientver.HeaderPlatform); got != clientver.Platform() {
			t.Errorf("protected-resource leg: %s = %q, want %q", clientver.HeaderPlatform, got, clientver.Platform())
		}

		// A THIRD PARTY — NEITHER header. This is the direction that fails if
		// the stamp is put inside the shared helper, and it is the one that
		// matters: a client-version header must never reach an authorization
		// server we do not operate.
		ah := authKit.snapshot(t, "authorization-server")
		for _, h := range []string{clientver.HeaderVersion, clientver.HeaderPlatform} {
			if got := ah.Get(h); got != "" {
				t.Errorf("the AuthKit authorization-server leg carries %s = %q; a client-version header must NEVER reach a third-party authorization server", h, got)
			}
		}
		// Belt and braces on the same claim: no vendor-prefixed header at all.
		for name := range ah {
			if strings.HasPrefix(http.CanonicalHeaderKey(name), "X-Knowledge-") {
				t.Errorf("the AuthKit leg carries a vendor header %q", name)
			}
		}
		// The header the helper already set must survive on BOTH legs.
		if got := fh.Get("Accept"); got != "application/json" {
			t.Errorf("protected-resource leg lost its Accept header: %q", got)
		}
		if got := ah.Get("Accept"); got != "application/json" {
			t.Errorf("authorization-server leg lost its Accept header: %q", got)
		}
	})

	t.Run("a 426 on the fulminate leg names the remedy", func(t *testing.T) {
		clientver.ClearRefusal()
		t.Cleanup(clientver.ClearRefusal)

		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUpgradeRequired)
			_, _ = w.Write([]byte(`{"minimum":"9.0.0","client_version":"5.5.5","upgrade_command":"knowledge install","reason":"below_minimum"}`))
		}))
		t.Cleanup(srv.Close)
		prevClient := oauthHTTPClient
		oauthHTTPClient = srv.Client()
		t.Cleanup(func() { oauthHTTPClient = prevClient })

		_, err := Discover(t.Context(), srv.URL, map[string]struct{}{})
		if err == nil {
			t.Fatalf("a 426 on discovery must fail")
		}
		for _, want := range []string{"9.0.0", "knowledge install"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("the discovery refusal omits %q, so the user has no remedy: %v", want, err)
			}
		}
		if _, latched := clientver.CurrentRefusal(); !latched {
			t.Errorf("the discovery refusal did not latch")
		}
	})

	// THE CONTROL for the refusal arm: an ordinary upstream failure on the
	// third-party leg keeps its existing message and is NOT rerouted through
	// the version classifier, which never runs on a leg we do not stamp.
	t.Run("a non-2xx on the AuthKit leg keeps its plain upstream error", func(t *testing.T) {
		clientver.ClearRefusal()
		t.Cleanup(clientver.ClearRefusal)

		_, _, endpoint, allowed := newChain(t, http.StatusInternalServerError)
		_, err := Discover(t.Context(), endpoint, allowed)
		if err == nil {
			t.Fatalf("a 500 on the authorization-server leg must fail")
		}
		if !strings.Contains(err.Error(), "upstream status 500") {
			t.Errorf("the AuthKit leg lost its plain upstream error: %v", err)
		}
		if _, latched := clientver.CurrentRefusal(); latched {
			t.Errorf("a third-party upstream failure latched a version refusal")
		}
	})
}
