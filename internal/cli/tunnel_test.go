// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

// TestFetchCert_SendsPublicKeyWithBearer asserts fetchCert POSTs the ephemeral
// PUBLIC key (never the private key) with the bearer header and the env selector,
// and returns the cert on 200.
func TestFetchCert_SendsPublicKeyWithBearer(t *testing.T) {
	kp, err := generateEphemeralKey()
	if err != nil {
		t.Fatalf("generateEphemeralKey: %v", err)
	}

	var (
		gotAuth string
		gotBody []byte
		gotPath string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		_ = json.NewEncoder(w).Encode(connectResponse{
			Certificate:  "ssh-ed25519-cert-v01@openssh.com AAAAcert",
			RelayToken:   "relay.jwt.token",
			HostCAPubKey: "ssh-ed25519 AAAAhostca",
		})
	}))
	defer srv.Close()

	cert, relayToken, hostCAPubKey, err := fetchCert(context.Background(), srv.Client(), srv.URL, "tok-123", kp.authorizedKey, "api-dev")
	if err != nil {
		t.Fatalf("fetchCert: %v", err)
	}
	if cert == "" {
		t.Fatal("fetchCert returned an empty certificate")
	}
	if relayToken != "relay.jwt.token" {
		t.Errorf("relay_token = %q, want it surfaced from the 200 response body", relayToken)
	}
	if hostCAPubKey != "ssh-ed25519 AAAAhostca" {
		t.Errorf("host_ca_pubkey = %q, want it surfaced from the 200 response body", hostCAPubKey)
	}
	if gotPath != "/v1/dev-vm/connect" {
		t.Errorf("path = %q, want /v1/dev-vm/connect", gotPath)
	}
	if gotAuth != "Bearer tok-123" {
		t.Errorf("auth header = %q, want %q", gotAuth, "Bearer tok-123")
	}

	var sent connectRequest
	if err := json.Unmarshal(gotBody, &sent); err != nil {
		t.Fatalf("unmarshal sent body: %v", err)
	}
	if strings.TrimSpace(sent.SSHPublicKey) != strings.TrimSpace(kp.authorizedKey) {
		t.Errorf("sent ssh_public_key = %q, want the ephemeral public key", sent.SSHPublicKey)
	}
	if sent.Env != "api-dev" {
		t.Errorf("sent env = %q, want %q (the env selector must reach the server)", sent.Env, "api-dev")
	}
	// The private key must NEVER appear on the wire.
	if strings.Contains(string(gotBody), "PRIVATE KEY") || strings.Contains(string(gotBody), string(kp.privatePEM)) {
		t.Error("request body contains the private key — only the public key may be sent")
	}
}

// TestFetchCert_StatusMapping asserts each error status maps to a distinct,
// actionable message and that the 401 message references `knowledge login`.
func TestFetchCert_StatusMapping(t *testing.T) {
	cases := []struct {
		status  int
		wantSub string
	}{
		{http.StatusBadRequest, "400"},
		{http.StatusUnauthorized, "knowledge login"},
		{http.StatusForbidden, "dev environment"},
		{http.StatusServiceUnavailable, "unavailable"},
	}
	for _, tc := range cases {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
			}))
			defer srv.Close()

			_, _, _, err := fetchCert(context.Background(), srv.Client(), srv.URL, "tok", "ssh-ed25519 AAAA", "")
			if err == nil {
				t.Fatalf("status %d: expected an error", tc.status)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("status %d: error %q does not mention %q", tc.status, err.Error(), tc.wantSub)
			}
		})
	}

	// An unmapped status falls through to the default arm with the raw status.
	t.Run("default arm", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusTeapot)
			_, _ = w.Write([]byte("brewing"))
		}))
		defer srv.Close()
		_, _, _, err := fetchCert(context.Background(), srv.Client(), srv.URL, "tok", "ssh-ed25519 AAAA", "")
		if err == nil || !strings.Contains(err.Error(), "418") {
			t.Errorf("default arm error = %v, want it to carry the raw status 418", err)
		}
	})

	// Confirm the four mapped messages are actually distinct.
	msgs := map[string]bool{}
	for _, status := range []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden, http.StatusServiceUnavailable} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) }))
		_, _, _, err := fetchCert(context.Background(), srv.Client(), srv.URL, "tok", "ssh-ed25519 AAAA", "")
		srv.Close()
		if err != nil {
			msgs[err.Error()] = true
		}
	}
	if len(msgs) != 4 {
		t.Errorf("expected 4 distinct status messages, got %d: %v", len(msgs), msgs)
	}
}

// TestGenerateEphemeralKey_Parseable asserts the generated PEM private key and
// authorized-key line parse with the ssh library.
func TestGenerateEphemeralKey_Parseable(t *testing.T) {
	kp, err := generateEphemeralKey()
	if err != nil {
		t.Fatalf("generateEphemeralKey: %v", err)
	}
	if _, err := ssh.ParseRawPrivateKey(kp.privatePEM); err != nil {
		t.Errorf("private key PEM does not parse: %v", err)
	}
	if _, _, _, _, err := ssh.ParseAuthorizedKey([]byte(kp.authorizedKey)); err != nil {
		t.Errorf("authorized key line does not parse: %v", err)
	}
}

// TestTunnelSSHHost_StripsScheme asserts the host-derivation helper returns the
// endpoint host with the scheme stripped, for both the prod and dev endpoints,
// and that it is consistent with the build-tag-pinned CloudEndpoint.
func TestTunnelSSHHost_StripsScheme(t *testing.T) {
	cases := []struct{ endpoint, want string }{
		{"https://fulminate.io", "fulminate.io"},
		{"https://dev.fulminate.io", "dev.fulminate.io"},
	}
	for _, tc := range cases {
		got, err := tunnelSSHHost(tc.endpoint)
		if err != nil {
			t.Fatalf("tunnelSSHHost(%q): %v", tc.endpoint, err)
		}
		if got != tc.want {
			t.Errorf("tunnelSSHHost(%q) = %q, want %q", tc.endpoint, got, tc.want)
		}
	}

	// Consistency with the build-tag-pinned CloudEndpoint: the derived host is
	// CloudEndpoint with the scheme stripped and carries no scheme prefix.
	got, err := tunnelSSHHost(CloudEndpoint)
	if err != nil {
		t.Fatalf("tunnelSSHHost(CloudEndpoint): %v", err)
	}
	if strings.Contains(got, "://") {
		t.Errorf("derived host %q still carries a scheme", got)
	}
	if want := strings.TrimPrefix(CloudEndpoint, "https://"); got != want {
		t.Errorf("tunnelSSHHost(CloudEndpoint) = %q, want %q", got, want)
	}
}

// captureStdout swaps os.Stdout for a pipe, runs f, and returns everything f printed to
// stdout alongside f's error.
func captureStdout(t *testing.T, f func() error) (string, error) {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	runErr := f()
	_ = w.Close()
	os.Stdout = old
	out, _ := io.ReadAll(r)
	return string(out), runErr
}

// hostCertConnectServer stands in for the agent connect endpoint: it echoes a
// per-env relay_token (dev_env_id = "env-"+env) and a host_ca_pubkey chosen by
// hostCAFor(env), so a test can drive distinct envs and the fail-closed empty case.
func hostCertConnectServer(t *testing.T, hostCAFor func(env string) string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req connectRequest
		_ = json.Unmarshal(body, &req)
		_ = json.NewEncoder(w).Encode(connectResponse{
			Certificate:  "ssh-ed25519-cert-v01@openssh.com AAAAcert",
			RelayToken:   mintRelayToken(t, "env-"+req.Env),
			HostCAPubKey: hostCAFor(req.Env),
		})
	}))
}

// TestRunTunnel_PrintProxy_HostVerification exercises the full --print-proxy-command
// flow for TWO distinct envs: each writes its OWN known_hosts (distinct env_id
// @cert-authority anchors, no collision) and emits StrictHostKeyChecking + a
// HostKeyAlias pinned to that env's env_id, with the host_ca_pubkey threaded from the
// connect response.
func TestRunTunnel_PrintProxy_HostVerification(t *testing.T) {
	t.Setenv("KNOWLEDGE_AUTH_TOKEN", "tok") // StaticTokenSource path — no keychain
	home := t.TempDir()
	t.Setenv("HOME", home)

	srv := hostCertConnectServer(t, func(env string) string { return "ssh-ed25519 CA-" + env })
	defer srv.Close()

	runFor := func(env string) string {
		out, err := captureStdout(t, func() error {
			return runTunnel(context.Background(), srv.URL, env, tunnelOpts{printProxy: true})
		})
		if err != nil {
			t.Fatalf("runTunnel(%q, printProxy): %v", env, err)
		}
		return out
	}

	type envCase struct{ env, envID, hostCA string }
	cases := []envCase{
		{"api-dev", "env-api-dev", "ssh-ed25519 CA-api-dev"},
		{"worker-dev", "env-worker-dev", "ssh-ed25519 CA-worker-dev"},
	}

	knownHostsPaths := map[string]string{}
	for _, tc := range cases {
		out := runFor(tc.env)

		// StrictHostKeyChecking + a HostKeyAlias pinned to THIS env's env_id are emitted.
		if !strings.Contains(out, "StrictHostKeyChecking yes") {
			t.Errorf("env %q proxy output missing StrictHostKeyChecking yes:\n%s", tc.env, out)
		}
		if !strings.Contains(out, "HostKeyAlias "+tc.envID) {
			t.Errorf("env %q proxy output missing HostKeyAlias %q:\n%s", tc.env, tc.envID, out)
		}
		if !strings.Contains(out, "UserKnownHostsFile ") {
			t.Errorf("env %q proxy output missing UserKnownHostsFile:\n%s", tc.env, out)
		}

		// The FOUR transport directives the ws-bridge harness (TestDevVMConnectWSBridge, agent repo)
		// consumes from this print-proxy block MUST be present: ProxyCommand (the --proxy transport
		// leg), IdentityFile + CertificateFile (the ephemeral cert identity), and IdentitiesOnly yes
		// (so only that key is offered). These are emitted at tunnel.go's print-proxy branch alongside
		// the host-verification keys above; guarding them here keeps the harness's `ssh -F <cfg>` run
		// wired if the emitted block is ever refactored.
		if !strings.Contains(out, "ProxyCommand ") {
			t.Errorf("env %q proxy output missing ProxyCommand (the ws-bridge transport leg):\n%s", tc.env, out)
		}
		// The login user MUST be pinned to the fixed sshLoginUser: sshd rejects any other
		// name (the cert's sole principal). Without it, ssh would use the operator's local
		// username and fail with "name is not a listed principal".
		if !strings.Contains(out, "User "+sshLoginUser) {
			t.Errorf("env %q proxy output missing `User %s` (the fixed cert-principal login user):\n%s", tc.env, sshLoginUser, out)
		}
		if !strings.Contains(out, "IdentityFile ") {
			t.Errorf("env %q proxy output missing IdentityFile (the ephemeral private key):\n%s", tc.env, out)
		}
		if !strings.Contains(out, "CertificateFile ") {
			t.Errorf("env %q proxy output missing CertificateFile (the minted cert):\n%s", tc.env, out)
		}
		if !strings.Contains(out, "IdentitiesOnly yes") {
			t.Errorf("env %q proxy output missing IdentitiesOnly yes:\n%s", tc.env, out)
		}

		// The per-env known_hosts carries this env's @cert-authority anchor with the
		// threaded host_ca_pubkey.
		khPath := filepath.Join(home, ".knowledge", "ssh", connectionName(tc.env)+"-known_hosts")
		data, err := os.ReadFile(khPath)
		if err != nil {
			t.Fatalf("read known_hosts for %q: %v", tc.env, err)
		}
		want := "@cert-authority " + tc.envID + " " + tc.hostCA + "\n"
		if string(data) != want {
			t.Errorf("env %q known_hosts = %q, want %q", tc.env, data, want)
		}
		knownHostsPaths[tc.env] = khPath
	}

	// The two envs wrote DISTINCT known_hosts files — no collision.
	if knownHostsPaths["api-dev"] == knownHostsPaths["worker-dev"] {
		t.Fatalf("both envs wrote the same known_hosts path %q — env verification must not collide", knownHostsPaths["api-dev"])
	}
}

// TestRunTunnel_FailsClosedOnEmptyHostCAPubKey pins the fail-closed rule: an
// empty host_ca_pubkey (a server too old to certify its host key) makes the tunnel
// REFUSE to connect — never a silent unverified session — and writes no known_hosts.
func TestRunTunnel_FailsClosedOnEmptyHostCAPubKey(t *testing.T) {
	t.Setenv("KNOWLEDGE_AUTH_TOKEN", "tok")
	home := t.TempDir()
	t.Setenv("HOME", home)

	srv := hostCertConnectServer(t, func(string) string { return "" }) // old server: no host_ca_pubkey
	defer srv.Close()

	_, err := captureStdout(t, func() error {
		return runTunnel(context.Background(), srv.URL, "api-dev", tunnelOpts{printProxy: true})
	})
	if err == nil {
		t.Fatal("expected a fail-closed error when host_ca_pubkey is empty, got nil (would connect UNVERIFIED)")
	}
	if !strings.Contains(err.Error(), "host_ca_pubkey") && !strings.Contains(err.Error(), "host") {
		t.Errorf("error %q does not explain the missing host verification anchor", err.Error())
	}

	// No known_hosts (and no unverified session) may be produced on the fail-closed path.
	if _, statErr := os.Stat(filepath.Join(home, ".knowledge", "ssh", connectionName("api-dev")+"-known_hosts")); statErr == nil {
		t.Error("a known_hosts file was written on the fail-closed path — must write nothing")
	}
}
