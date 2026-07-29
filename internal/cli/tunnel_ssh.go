// SPDX-License-Identifier: Apache-2.0

// tunnel_ssh.go — the on-disk identity materialization + ssh-invocation mechanics
// for `knowledge tunnel`. It writes the ephemeral key / cert / known_hosts under
// ~/.knowledge/ssh, builds the ssh argv (identity + cert + extra options + target),
// and runs ssh (via the injectable sshRunner). runTunnel (tunnel.go) orchestrates
// these; the FULMINATE_SESSION session-name model lives in tunnel_session.go.

package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// connectionName is the on-disk identity basename for an env (defaults when env
// is empty).
func connectionName(env string) string {
	if env == "" {
		return "dev"
	}
	return "dev-" + env
}

// sshSessionDir returns ~/.knowledge/ssh, created at 0700. Follows the inline
// os.UserHomeDir + filepath.Join(home, ".knowledge", ...) pattern every other
// ~/.knowledge consumer in this binary uses (there is no central helper).
func sshSessionDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	sshDir := filepath.Join(home, ".knowledge", "ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		return "", fmt.Errorf("create %s: %w", sshDir, err)
	}
	return sshDir, nil
}

// writeIdentityFiles writes the ephemeral private key and the returned cert to
// dir at 0600 (owner-only), in the OpenSSH-expected <name> / <name>-cert.pub
// layout, and returns their paths.
func writeIdentityFiles(dir, name string, kp *ephemeralKeyPair, cert string) (keyPath, certPath string, err error) {
	keyPath = filepath.Join(dir, name)
	certPath = filepath.Join(dir, name+"-cert.pub")
	if err = os.WriteFile(keyPath, kp.privatePEM, 0o600); err != nil {
		return "", "", fmt.Errorf("write private key: %w", err)
	}
	if err = os.WriteFile(certPath, []byte(cert), 0o600); err != nil {
		return "", "", fmt.Errorf("write certificate: %w", err)
	}
	return keyPath, certPath, nil
}

// writeKnownHosts writes a per-invocation OpenSSH known_hosts file to dir at 0600
// containing exactly one `@cert-authority <env_id> <host-ca-pubkey>` line — the trust
// anchor that makes ssh accept the VM's sshd host CERTIFICATE iff it is signed by this
// deploy's host-CA AND carries the env_id principal. Keying the marker line on env_id
// (not a shared apiHost) is what stops two different environments' host keys colliding in
// a shared known_hosts. The file is per-env (named off connectionName) so distinct envs
// never share a file. hostCAPubKey is the server's HostCAPubKey (an OpenSSH authorized-key
// line); its trailing whitespace is trimmed so the marker line is well-formed.
func writeKnownHosts(dir, name, envID, hostCAPubKey string) (knownHostsPath string, err error) {
	knownHostsPath = filepath.Join(dir, name+"-known_hosts")
	line := fmt.Sprintf("@cert-authority %s %s\n", envID, strings.TrimSpace(hostCAPubKey))
	if err = os.WriteFile(knownHostsPath, []byte(line), 0o600); err != nil {
		return "", fmt.Errorf("write known_hosts: %w", err)
	}
	return knownHostsPath, nil
}

// buildSSHArgs builds the ssh argv for a cert-based connection: the ephemeral
// identity (-i), its CertificateFile, IdentitiesOnly so only this key is
// offered, then any extra args, then the target host.
func buildSSHArgs(keyPath, certPath, target string, extra []string) []string {
	args := []string{
		"-i", keyPath,
		"-o", "CertificateFile=" + certPath,
		"-o", "IdentitiesOnly=yes",
	}
	args = append(args, extra...)
	args = append(args, target)
	return args
}

// sshSelf resolves the path to THIS binary for the emitted ProxyCommand line,
// falling back to the bare name "knowledge" (resolved off PATH) when os.Executable
// is unavailable — so a pasted ~/.ssh/config line is robust.
func sshSelf() string {
	if exe, err := os.Executable(); err == nil && exe != "" {
		return exe
	}
	return "knowledge"
}

// proxyCommandArg builds the ProxyCommand invocation: `<self> tunnel --proxy [env]`.
// The env arg is omitted when empty (connect to the oldest environment).
func proxyCommandArg(self, env string) string {
	if env == "" {
		return self + " tunnel --proxy"
	}
	return self + " tunnel --proxy " + env
}

// sshRunner runs the assembled ssh argv. It is a package var (default execSSH) so
// tests can capture the argv — including the threaded SetEnv=FULMINATE_SESSION — of
// a full runTunnel call without spawning a real ssh process.
var sshRunner = execSSH

// execSSH runs ssh with the given argv, wiring the current stdio so the
// resulting shell / editor Remote-SSH channel is interactive.
func execSSH(ctx context.Context, args []string) error {
	cmd := exec.CommandContext(ctx, "ssh", args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
