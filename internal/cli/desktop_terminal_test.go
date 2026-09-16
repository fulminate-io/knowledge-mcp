// SPDX-License-Identifier: Apache-2.0
package cli

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/ssh"

	"github.com/fulminate-io/knowledge-mcp/internal/auth"
)

func TestDesktopTerminalMint(t *testing.T) {
	store := withMemoryStore(t)
	if err := store.Set(t.Context(), auth.KeyRefreshToken, "fixture-refresh"); err != nil {
		t.Fatal(err)
	}
	peer, _ := dashboardPeer(t)
	state := "running"
	envID := "env-native"
	mints := 0
	relayAccount := "account-A"
	relayEnv := "env-native"
	hostCAOverride := ""
	// certMutation re-mints the peer's capability certificate so a single
	// binding can be broken at a time; the production path never verifies the
	// certificate signature, so re-signing under a fixture CA isolates the
	// field under test instead of collapsing into a signature failure.
	var certMutation func(*ssh.Certificate)
	_, mutationKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	mutationCA, err := ssh.NewSignerFromKey(mutationKey)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(auth.AccountHeaderName) != "account-A" {
			t.Error("account not captured")
		}
		switch r.URL.Path {
		case "/v1/accounts/account-A/dev-vm/env-native":
			_ = json.NewEncoder(w).Encode(map[string]string{"env_id": envID, "name": "named-env", "state": state})
		case "/v1/dev-vm/connect":
			mints++
			response, err := http.Post(peer.URL+"/v1/dev-vm/connect", "application/json", r.Body)
			if err != nil {
				t.Error(err)
				return
			}
			defer response.Body.Close()
			var cap connectResponse
			decoder := json.NewDecoder(response.Body)
			decoder.DisallowUnknownFields()
			if decoder.Decode(&cap) != nil {
				t.Error("invalid fixture capability")
				return
			}
			if certMutation != nil {
				parsed, _, _, _, err := ssh.ParseAuthorizedKey([]byte(cap.Certificate))
				if err != nil {
					t.Error("fixture certificate unparsable", err)
					return
				}
				cert, isCert := parsed.(*ssh.Certificate)
				if !isCert {
					t.Error("fixture minted a bare key, so no certificate binding could be broken")
					return
				}
				certMutation(cert)
				if err := cert.SignCert(rand.Reader, mutationCA); err != nil {
					t.Error(err)
					return
				}
				cap.Certificate = string(ssh.MarshalAuthorizedKey(cert))
			}
			if hostCAOverride != "" {
				cap.HostCAPubKey = hostCAOverride
			}
			token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"dev_env_id": relayEnv, "account": relayAccount}).SignedString([]byte("fixture"))
			if err != nil {
				t.Error(err)
				return
			}
			cap.RelayToken = token
			_ = json.NewEncoder(w).Encode(cap)
		default:
			t.Error("unexpected path", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	old := nativeManagementTransport
	t.Cleanup(func() { nativeManagementTransport = old })
	nativeManagementTransport = func(auth.Store) *auth.Transport {
		return auth.NewSyncTransport(server.URL, auth.StaticTokenSource{AccessToken: "fixture-access"})
	}
	key, err := generateEphemeralKey()
	if err != nil {
		t.Fatal(err)
	}
	other, err := generateEphemeralKey()
	if err != nil {
		t.Fatal(err)
	}
	otherPublic, _, _, _, err := ssh.ParseAuthorizedKey([]byte(other.authorizedKey))
	if err != nil {
		t.Fatal(err)
	}
	req := desktopTerminalRequest{Account: "account-A", Environment: "env-native", PublicKey: key.authorizedKey}
	got := performDesktopTerminal(t.Context(), req)
	if got.Code != "" || got.Certificate == "" || got.RelayToken == "" || got.HostCA == "" || got.Account != "account-A" || got.Environment != "env-native" || mints != 1 {
		t.Fatalf("mint failed code=%s mints=%d", got.Code, mints)
	}
	state = "stopped"
	if got := performDesktopTerminal(t.Context(), req); got.Code != "environment_stopped" || mints != 1 {
		t.Fatal("stopped environment minted")
	}
	state = "running"
	envID = "wrong-env"
	if got := performDesktopTerminal(t.Context(), req); got.Code != "invalid_response" || mints != 1 {
		t.Fatal("mismatched environment minted")
	}
	req.PublicKey = "private-invalid"
	if got := performDesktopTerminal(t.Context(), req); got.Code != "invalid_request" || mints != 1 {
		t.Fatal("invalid key minted")
	}
	req.PublicKey = key.authorizedKey
	envID = "env-native"
	// refuse names the binding under test, the public code it must produce, and
	// the mint count the request is allowed to reach. Each of these cases fails
	// AFTER the capability is minted, so the count rises by one every time: a
	// case that leaves it unchanged never reached the binding it claims to test.
	wantMints := 1
	refuse := func(binding, code string) {
		t.Helper()
		wantMints++
		result := performDesktopTerminal(t.Context(), req)
		if result.Code != code {
			t.Errorf("%s: code=%q want %q", binding, result.Code, code)
		}
		if result.Certificate != "" || result.RelayToken != "" || result.HostCA != "" {
			t.Errorf("%s: refusal returned capability material", binding)
		}
		if mints != wantMints {
			t.Errorf("%s: mints=%d want %d", binding, mints, wantMints)
		}
	}
	relayAccount = "other-account"
	refuse("relay token account claim", "connection_refused")
	relayAccount = "account-A"

	relayEnv = "other-env"
	refuse("relay token dev_env_id claim", "connection_refused")
	relayEnv = "env-native"

	certMutation = func(cert *ssh.Certificate) { cert.Key = otherPublic }
	refuse("certificate bound to the requested public key", "invalid_response")

	certMutation = func(cert *ssh.Certificate) { cert.CertType = ssh.HostCert }
	refuse("certificate type", "invalid_response")

	certMutation = func(cert *ssh.Certificate) {
		cert.ValidBefore = uint64(time.Now().Add(time.Hour).Unix())
	}
	refuse("certificate validity window upper bound", "invalid_response")

	certMutation = func(cert *ssh.Certificate) {
		cert.ValidBefore = uint64(time.Now().Add(-time.Minute).Unix())
	}
	refuse("certificate already expired", "invalid_response")
	certMutation = nil

	hostCAOverride = "not-a-key"
	refuse("host CA public key", "invalid_response")
	hostCAOverride = ""

	// KNOWN POSITIVE: the same request succeeds once every knob is restored, so
	// the refusals above are the bindings and not a fixture the cases broke.
	if got := performDesktopTerminal(t.Context(), req); got.Code != "" || got.Certificate == "" || mints != wantMints+1 {
		t.Fatalf("restored fixture no longer mints code=%s mints=%d", got.Code, mints)
	}
}
