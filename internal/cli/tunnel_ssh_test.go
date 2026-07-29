// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"os"
	"strings"
	"testing"
)

// TestWriteIdentityFiles_Mode0600 asserts the key + cert are written owner-only
// in the OpenSSH <name> / <name>-cert.pub layout.
func TestWriteIdentityFiles_Mode0600(t *testing.T) {
	kp, err := generateEphemeralKey()
	if err != nil {
		t.Fatalf("generateEphemeralKey: %v", err)
	}
	dir := t.TempDir()
	keyPath, certPath, err := writeIdentityFiles(dir, "dev-api-dev", kp, "ssh-ed25519-cert AAAA")
	if err != nil {
		t.Fatalf("writeIdentityFiles: %v", err)
	}
	for _, p := range []string{keyPath, certPath} {
		info, statErr := os.Stat(p)
		if statErr != nil {
			t.Fatalf("stat %s: %v", p, statErr)
		}
		if mode := info.Mode().Perm(); mode != 0o600 {
			t.Errorf("%s mode = %o, want 0600", p, mode)
		}
	}
	if !strings.HasSuffix(certPath, "-cert.pub") {
		t.Errorf("cert path = %q, want a -cert.pub suffix (OpenSSH layout)", certPath)
	}
}

// TestBuildSSHArgs_IdentityAndCert asserts the argv carries -i <key>,
// CertificateFile=<cert>, IdentitiesOnly=yes, and targets the host last.
func TestBuildSSHArgs_IdentityAndCert(t *testing.T) {
	args := buildSSHArgs("/keys/dev", "/keys/dev-cert.pub", "relay.example.com", nil)
	joined := strings.Join(args, " ")

	if !strings.Contains(joined, "-i /keys/dev") {
		t.Errorf("argv missing -i identity: %v", args)
	}
	if !strings.Contains(joined, "CertificateFile=/keys/dev-cert.pub") {
		t.Errorf("argv missing CertificateFile: %v", args)
	}
	if !strings.Contains(joined, "IdentitiesOnly=yes") {
		t.Errorf("argv missing IdentitiesOnly=yes: %v", args)
	}
	if args[len(args)-1] != "relay.example.com" {
		t.Errorf("target host must be last, got %v", args)
	}
}

// TestProxyCommandArg asserts the emitted ProxyCommand references THIS binary in
// `tunnel --proxy <env>` form (never a bare `ssh <host>`), with the env omitted
// when empty.
func TestProxyCommandArg(t *testing.T) {
	got := proxyCommandArg("/opt/knowledge", "api-dev")
	if got != "/opt/knowledge tunnel --proxy api-dev" {
		t.Errorf("proxyCommandArg = %q, want the tunnel --proxy form", got)
	}
	if strings.Contains(got, "ssh ") {
		t.Errorf("ProxyCommand must NOT be a bare ssh invocation: %q", got)
	}
	if empty := proxyCommandArg("knowledge", ""); empty != "knowledge tunnel --proxy" {
		t.Errorf("empty-env ProxyCommand = %q, want no trailing env", empty)
	}
}

// TestBuildSSHArgs_InjectsProxyCommand asserts the direct exec path routes ssh
// through the relay ws by injecting -o ProxyCommand=<self> tunnel --proxy <env>.
func TestBuildSSHArgs_InjectsProxyCommand(t *testing.T) {
	proxyCmd := proxyCommandArg("/opt/knowledge", "api-dev")
	args := buildSSHArgs("/keys/dev", "/keys/dev-cert.pub", "relay.example.com",
		[]string{"-o", "ProxyCommand=" + proxyCmd})
	joined := strings.Join(args, " ")

	if !strings.Contains(joined, "-o ProxyCommand=/opt/knowledge tunnel --proxy api-dev") {
		t.Errorf("ssh argv missing the ProxyCommand option: %v", args)
	}
	if args[len(args)-1] != "relay.example.com" {
		t.Errorf("target host must remain last, got %v", args)
	}
}

// TestSSHLoginUser_FixedPrincipal drift-guards the cross-repo login-user contract:
// sshLoginUser MUST equal the agent's minted cert principal (devssh.SSHLoginUser) + the
// dev-VM cloud-config user (separate repos, no shared package); drift => sshd rejects.
func TestSSHLoginUser_FixedPrincipal(t *testing.T) {
	if sshLoginUser != "fulminate" {
		t.Errorf("sshLoginUser = %q, want %q (must match the minted cert principal + cloud-config login user)", sshLoginUser, "fulminate")
	}
}

// TestBuildSSHArgs_LoginUserTarget: a user-qualified target (fulminate@<env_id>) is the final argv element.
func TestBuildSSHArgs_LoginUserTarget(t *testing.T) {
	target := sshLoginUser + "@env-api-dev"
	args := buildSSHArgs("/keys/dev", "/keys/dev-cert.pub", target, nil)
	if args[len(args)-1] != target || !strings.HasPrefix(args[len(args)-1], "fulminate@") {
		t.Errorf("ssh target must be last and authenticate as fulminate@, got %v", args)
	}
}

// TestConnectionName asserts the on-disk basename defaults when env is empty and
// is prefixed otherwise.
func TestConnectionName(t *testing.T) {
	if got := connectionName(""); got != "dev" {
		t.Errorf("connectionName(\"\") = %q, want dev", got)
	}
	if got := connectionName("api-dev"); got != "dev-api-dev" {
		t.Errorf("connectionName(api-dev) = %q, want dev-api-dev", got)
	}
}

// TestWriteKnownHosts_PerEnvNoCollision asserts two DISTINCT envs each write a
// separate known_hosts file whose sole line is `@cert-authority <env_id> <host-ca>` —
// distinct env_id principals, no shared-host collision (the whole point of keying the
// anchor on env_id rather than a shared apiHost). The host_ca_pubkey is threaded
// verbatim (trailing whitespace trimmed), and both files are owner-only (0600).
func TestWriteKnownHosts_PerEnvNoCollision(t *testing.T) {
	dir := t.TempDir()

	const (
		envA    = "env-aaaa-1111"
		envB    = "env-bbbb-2222"
		hostCAA = "ssh-ed25519 AAAAhostcaA"
		hostCAB = "ssh-ed25519 BBBBhostcaB"
	)

	pathA, err := writeKnownHosts(dir, connectionName("api-dev"), envA, hostCAA+"\n")
	if err != nil {
		t.Fatalf("writeKnownHosts(A): %v", err)
	}
	pathB, err := writeKnownHosts(dir, connectionName("worker-dev"), envB, hostCAB+"\n")
	if err != nil {
		t.Fatalf("writeKnownHosts(B): %v", err)
	}

	// Distinct per-env files — env A and env B never share a known_hosts (no collision).
	if pathA == pathB {
		t.Fatalf("env A and env B wrote the SAME known_hosts path %q — must be per-env (collision)", pathA)
	}

	wantA := "@cert-authority " + envA + " " + hostCAA + "\n"
	wantB := "@cert-authority " + envB + " " + hostCAB + "\n"
	for _, tc := range []struct {
		path, want, otherEnv string
	}{
		{pathA, wantA, envB},
		{pathB, wantB, envA},
	} {
		data, readErr := os.ReadFile(tc.path)
		if readErr != nil {
			t.Fatalf("read %s: %v", tc.path, readErr)
		}
		if string(data) != tc.want {
			t.Errorf("known_hosts %s = %q, want %q (@cert-authority keyed on its OWN env_id)", tc.path, data, tc.want)
		}
		// The OTHER env's principal must not appear — distinct anchors, no collision.
		if strings.Contains(string(data), tc.otherEnv) {
			t.Errorf("known_hosts %s leaked the other env's principal %q — env anchors must not collide", tc.path, tc.otherEnv)
		}
		info, statErr := os.Stat(tc.path)
		if statErr != nil {
			t.Fatalf("stat %s: %v", tc.path, statErr)
		}
		if mode := info.Mode().Perm(); mode != 0o600 {
			t.Errorf("%s mode = %o, want 0600", tc.path, mode)
		}
	}
}
