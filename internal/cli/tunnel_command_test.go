// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestResolveTunnelCommand covers the two one-shot-command forms, the both-set
// error, and the interactive (no command) path. dashCmd is the verbatim tokens
// after a standalone `--` (splitAtDoubleDash has already stripped the `--`).
func TestResolveTunnelCommand(t *testing.T) {
	cases := []struct {
		name    string
		command string
		dashCmd []string
		want    []string
		wantErr bool
	}{
		{name: "interactive: no command", command: "", dashCmd: nil, want: nil},
		{name: "interactive: empty dashCmd (bare --)", command: "", dashCmd: []string{}, want: nil},
		{name: "--command flag", command: "cat ~/.knowledge/config", dashCmd: nil, want: []string{"cat ~/.knowledge/config"}},
		{name: "-- passthrough", command: "", dashCmd: []string{"cat", "~/.knowledge/config"}, want: []string{"cat", "~/.knowledge/config"}},
		{name: "-- passthrough carrying dashed remote flags", command: "", dashCmd: []string{"ls", "-la"}, want: []string{"ls", "-la"}},
		{name: "both set errors", command: "echo hi", dashCmd: []string{"echo", "bye"}, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveTunnelCommand(tc.command, tc.dashCmd)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("resolveTunnelCommand(%q, %v) = %v, want an error", tc.command, tc.dashCmd, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveTunnelCommand(%q, %v): unexpected error %v", tc.command, tc.dashCmd, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("resolveTunnelCommand(%q, %v) = %v, want %v", tc.command, tc.dashCmd, got, tc.want)
			}
		})
	}
}

// TestRunTunnel_CommandMode_ArgvShape asserts the one-shot command path appends the
// command AFTER the ssh target, passes NO -t (so ssh requests no PTY and the VM's
// ForceCommand takes its non-interactive branch), threads NO FULMINATE_SESSION, keeps
// the host-cert verification options, and persists no session sidecar.
func TestRunTunnel_CommandMode_ArgvShape(t *testing.T) {
	t.Setenv("KNOWLEDGE_AUTH_TOKEN", "tok")
	home := t.TempDir()
	t.Setenv("HOME", home)

	srv := hostCertConnectServer(t, func(env string) string { return "ssh-ed25519 CA-" + env })
	defer srv.Close()

	var gotArgs []string
	orig := sshRunner
	sshRunner = func(_ context.Context, args []string) error { gotArgs = args; return nil }
	t.Cleanup(func() { sshRunner = orig })

	const env = "api-dev"
	command := []string{"cat", "~/.knowledge/config"}
	if err := runTunnel(context.Background(), srv.URL, env, tunnelOpts{command: command}); err != nil {
		t.Fatalf("runTunnel command mode: %v", err)
	}

	joined := strings.Join(gotArgs, " ")

	// No PTY request: -t must never appear (it would force a PTY and the interactive
	// ForceCommand branch, defeating the non-interactive exec).
	for _, a := range gotArgs {
		if a == "-t" {
			t.Fatalf("command-mode argv must not carry -t (no PTY): %v", gotArgs)
		}
	}
	// No tmux session is threaded for a one-shot exec.
	if strings.Contains(joined, "FULMINATE_SESSION") {
		t.Errorf("command-mode argv must not thread FULMINATE_SESSION: %v", gotArgs)
	}
	// Host-cert verification options survive unchanged.
	if !strings.Contains(joined, "StrictHostKeyChecking=yes") || !strings.Contains(joined, "UserKnownHostsFile=") || !strings.Contains(joined, "ProxyCommand=") {
		t.Errorf("command-mode argv missing host-verification / proxy options: %v", gotArgs)
	}

	// The command is appended AFTER the ssh target (fulminate@<env_id>), in order.
	target := sshLoginUser + "@env-" + env
	ti := -1
	for i, a := range gotArgs {
		if a == target {
			ti = i
			break
		}
	}
	if ti == -1 {
		t.Fatalf("ssh target %q not found in argv: %v", target, gotArgs)
	}
	after := gotArgs[ti+1:]
	if !reflect.DeepEqual(after, command) {
		t.Errorf("command must be appended after the target %q: got trailing %v, want %v (full argv %v)", target, after, command, gotArgs)
	}

	// A one-shot command persists no per-env session sidecar (it skips the session model).
	if _, statErr := os.Stat(filepath.Join(home, ".knowledge", "ssh", connectionName(env)+".session")); statErr == nil {
		t.Error("command mode wrote a session sidecar — a one-shot exec must not touch the session model")
	}
}

// TestRunTunnel_CommandMode_ExitErrorPropagates asserts runTunnel returns the
// ssh *exec.ExitError verbatim (the carrier of the remote exit status) so the
// bootstrap boundary can surface the exact code.
func TestRunTunnel_CommandMode_ExitErrorPropagates(t *testing.T) {
	t.Setenv("KNOWLEDGE_AUTH_TOKEN", "tok")
	t.Setenv("HOME", t.TempDir())

	srv := hostCertConnectServer(t, func(env string) string { return "ssh-ed25519 CA-" + env })
	defer srv.Close()

	// A real *exec.ExitError with a known non-zero code (mirrors ssh forwarding a
	// remote command's status).
	exitErr := exec.Command("sh", "-c", "exit 7").Run()
	if _, ok := errors.AsType[*exec.ExitError](exitErr); !ok {
		t.Fatalf("setup: expected an *exec.ExitError, got %T", exitErr)
	}

	orig := sshRunner
	sshRunner = func(_ context.Context, _ []string) error { return exitErr }
	t.Cleanup(func() { sshRunner = orig })

	err := runTunnel(context.Background(), srv.URL, "api-dev", tunnelOpts{command: []string{"false"}})
	var got *exec.ExitError
	if !errors.As(err, &got) {
		t.Fatalf("runTunnel returned %v (%T), want an *exec.ExitError", err, err)
	}
	if got.ExitCode() != 7 {
		t.Errorf("propagated exit code = %d, want 7", got.ExitCode())
	}
}

// TestRunTunnel_CommandMode_PrintProxyWins asserts --print-proxy-command takes
// precedence over a supplied command: the config block is emitted (a one-shot
// command is not part of ~/.ssh/config) and the command is NOT executed.
func TestRunTunnel_CommandMode_PrintProxyWins(t *testing.T) {
	t.Setenv("KNOWLEDGE_AUTH_TOKEN", "tok")
	t.Setenv("HOME", t.TempDir())

	srv := hostCertConnectServer(t, func(env string) string { return "ssh-ed25519 CA-" + env })
	defer srv.Close()

	var ran bool
	orig := sshRunner
	sshRunner = func(_ context.Context, _ []string) error { ran = true; return nil }
	t.Cleanup(func() { sshRunner = orig })

	out, err := captureStdout(t, func() error {
		return runTunnel(context.Background(), srv.URL, "api-dev", tunnelOpts{printProxy: true, command: []string{"rm-nothing"}})
	})
	if err != nil {
		t.Fatalf("runTunnel printProxy+command: %v", err)
	}
	if ran {
		t.Error("--print-proxy-command must not execute the one-shot command (config only)")
	}
	if !strings.Contains(out, "ProxyCommand ") {
		t.Errorf("print-proxy block missing ProxyCommand:\n%s", out)
	}
	if strings.Contains(out, "rm-nothing") {
		t.Errorf("the one-shot command must not appear in the emitted ssh config:\n%s", out)
	}
}
