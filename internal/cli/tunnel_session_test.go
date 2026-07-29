// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSessionNamePattern_LockstepWithCloudConfig guards the session-name allowlist
// against the dev-VM cloud-config ForceCommand's regex (separate repos, no shared
// package): if they drift, the client would accept a name the VM rejects (silent
// s-$$ fallback) or vice versa.
func TestSessionNamePattern_LockstepWithCloudConfig(t *testing.T) {
	const want = `^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`
	if sessionNamePattern != want {
		t.Errorf("sessionNamePattern = %q, want %q (lockstep with the dev-VM cloud-config ForceCommand allowlist)", sessionNamePattern, want)
	}
}

// TestGenerateSessionName_MatchesAllowlist asserts every minted default name passes
// the allowlist the VM validates against (so a generated default never degrades to
// the s-$$ fallback on the VM side).
func TestGenerateSessionName_MatchesAllowlist(t *testing.T) {
	for range 50 {
		n, err := generateSessionName()
		if err != nil {
			t.Fatalf("generateSessionName: %v", err)
		}
		if !sessionNameRE.MatchString(n) {
			t.Fatalf("generated name %q does not match the allowlist %s", n, sessionNamePattern)
		}
	}
}

// TestReadSessionSidecar_InvalidRegenerates asserts a corrupt/hand-edited sidecar
// (a value failing the allowlist) is treated as absent, so resolveSessionName
// regenerates a fresh default and signals persist rather than delivering a bad name.
func TestReadSessionSidecar_InvalidRegenerates(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(sessionSidecarPath(dir, "api-dev"), []byte("-bad name\n"), 0o600); err != nil {
		t.Fatalf("seed corrupt sidecar: %v", err)
	}
	if _, ok := readSessionSidecar(dir, "api-dev"); ok {
		t.Error("readSessionSidecar accepted an allowlist-invalid persisted name")
	}
	name, persist, err := resolveSessionName(dir, "api-dev", "", false)
	if err != nil {
		t.Fatalf("resolveSessionName: %v", err)
	}
	if !persist {
		t.Error("resolveSessionName must persist a freshly regenerated default")
	}
	if !sessionNameRE.MatchString(name) {
		t.Errorf("regenerated name %q does not match the allowlist", name)
	}
}

// TestRunTunnel_SessionModel exercises the persisted-per-env-default session model
// end to end via the direct-exec path (sshRunner is stubbed to capture the argv, so
// no ssh spawns): a first no-flag connect generates + persists a name and threads it
// as `-o SetEnv=FULMINATE_SESSION=<name>`; a second no-flag connect reattaches the
// identical persisted name; --new rotates the persisted default; --reuse/--session
// pins a name WITHOUT overwriting the default; an invalid --session errors; and
// --list-sessions runs a one-shot `tmux ls` with NO FULMINATE_SESSION threaded.
func TestRunTunnel_SessionModel(t *testing.T) {
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
	sidecar := filepath.Join(home, ".knowledge", "ssh", connectionName(env)+".session")

	run := func(opts tunnelOpts) error {
		gotArgs = nil
		return runTunnel(context.Background(), srv.URL, env, opts)
	}
	argvHasSetEnv := func(name string) bool {
		return strings.Contains(strings.Join(gotArgs, " "), "SetEnv=FULMINATE_SESSION="+name)
	}
	readSidecar := func() string {
		data, err := os.ReadFile(sidecar)
		if err != nil {
			t.Fatalf("read sidecar: %v", err)
		}
		return strings.TrimSpace(string(data))
	}

	// (1) First no-flag connect: generate + persist + thread SetEnv into the argv.
	if err := run(tunnelOpts{}); err != nil {
		t.Fatalf("first connect: %v", err)
	}
	first := readSidecar()
	if !sessionNameRE.MatchString(first) {
		t.Errorf("persisted default %q does not match the allowlist %s", first, sessionNamePattern)
	}
	if !argvHasSetEnv(first) {
		t.Errorf("direct-exec argv missing -o SetEnv=FULMINATE_SESSION=%s: %v", first, gotArgs)
	}

	// (2) Second no-flag connect: reattach the identical persisted name (no rotation).
	if err := run(tunnelOpts{}); err != nil {
		t.Fatalf("second connect: %v", err)
	}
	if !argvHasSetEnv(first) {
		t.Errorf("reattach did not reuse the persisted name %s: %v", first, gotArgs)
	}
	if got := readSidecar(); got != first {
		t.Errorf("no-flag reattach changed the sidecar: %q -> %q", first, got)
	}

	// (3) --new: rotate the persisted default to a DIFFERENT allowlist-valid name.
	if err := run(tunnelOpts{newSession: true}); err != nil {
		t.Fatalf("--new: %v", err)
	}
	rotated := readSidecar()
	if rotated == first {
		t.Errorf("--new did not rotate the persisted default (still %s)", first)
	}
	if !sessionNameRE.MatchString(rotated) {
		t.Errorf("rotated name %q does not match the allowlist", rotated)
	}
	if !argvHasSetEnv(rotated) {
		t.Errorf("--new argv missing SetEnv=%s: %v", rotated, gotArgs)
	}

	// (4) --reuse/--session: pin a specific name, DO NOT overwrite the persisted default.
	if err := run(tunnelOpts{pinnedSession: "shared-pair"}); err != nil {
		t.Fatalf("--reuse: %v", err)
	}
	if !argvHasSetEnv("shared-pair") {
		t.Errorf("--reuse argv missing SetEnv=shared-pair: %v", gotArgs)
	}
	if got := readSidecar(); got != rotated {
		t.Errorf("--reuse overwrote the persisted default: want %s, got %s", rotated, got)
	}

	// (5) An invalid --session value errors before connecting.
	if err := run(tunnelOpts{pinnedSession: "-bad; rm -rf /"}); err == nil {
		t.Error("invalid --session value did not error")
	}

	// (6) --list-sessions: one-shot `tmux ls`, NO FULMINATE_SESSION threaded.
	if err := run(tunnelOpts{listSessions: true}); err != nil {
		t.Fatalf("--list-sessions: %v", err)
	}
	if strings.Contains(strings.Join(gotArgs, " "), "FULMINATE_SESSION") {
		t.Errorf("--list-sessions must not thread FULMINATE_SESSION: %v", gotArgs)
	}
	if n := len(gotArgs); n < 2 || gotArgs[n-2] != "tmux" || gotArgs[n-1] != "ls" {
		t.Errorf("--list-sessions argv must end with the remote `tmux ls`: %v", gotArgs)
	}
}

// TestRunTunnel_OriginDefaultAndKill pins the origin-based naming and the --kill
// lifecycle control: a first no-flag connect persists the human-recognizable "cli"
// default (not a random handle); --new rotates to a fresh but still-recognizable
// "cli-<hex>"; --kill runs a one-shot `tmux kill-session -t <name>` with NO
// FULMINATE_SESSION threaded; and an allowlist-invalid --kill value errors before
// reaching the VM.
func TestRunTunnel_OriginDefaultAndKill(t *testing.T) {
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
	sidecar := filepath.Join(home, ".knowledge", "ssh", connectionName(env)+".session")
	run := func(opts tunnelOpts) error { gotArgs = nil; return runTunnel(context.Background(), srv.URL, env, opts) }

	// (1) First no-flag connect persists the legible client default (cli-<host>).
	if err := run(tunnelOpts{}); err != nil {
		t.Fatalf("first connect: %v", err)
	}
	data, err := os.ReadFile(sidecar)
	if err != nil {
		t.Fatalf("read sidecar: %v", err)
	}
	def := cliDefaultSessionName()
	if got := strings.TrimSpace(string(data)); got != def {
		t.Errorf("first connect persisted %q, want the client default %q", got, def)
	}
	if !strings.HasPrefix(def, sessionOriginCLI) || !sessionNameRE.MatchString(def) {
		t.Errorf("client default %q must be a %q-prefixed allowlist-valid name", def, sessionOriginCLI)
	}

	// (2) --new rotates to a fresh, human-readable name: the client default plus a
	// memorable word (cli-<host>-<word>), NOT hex, and distinct from the plain default.
	if err := run(tunnelOpts{newSession: true}); err != nil {
		t.Fatalf("--new: %v", err)
	}
	data, _ = os.ReadFile(sidecar)
	rotated := strings.TrimSpace(string(data))
	if rotated == def || !strings.HasPrefix(rotated, def+"-") {
		t.Errorf("--new produced %q, want a fresh %q-<word> name", rotated, def)
	}
	if !sessionNameRE.MatchString(rotated) {
		t.Errorf("--new name %q does not match the allowlist", rotated)
	}

	// (3) --kill: one-shot `tmux kill-session -t <name>`, no FULMINATE_SESSION.
	if err := run(tunnelOpts{killSession: "harness"}); err != nil {
		t.Fatalf("--kill: %v", err)
	}
	joined := strings.Join(gotArgs, " ")
	if strings.Contains(joined, "FULMINATE_SESSION") {
		t.Errorf("--kill must not thread FULMINATE_SESSION: %v", gotArgs)
	}
	if n := len(gotArgs); n < 4 || gotArgs[n-4] != "tmux" || gotArgs[n-3] != "kill-session" || gotArgs[n-2] != "-t" || gotArgs[n-1] != "harness" {
		t.Errorf("--kill argv must end with `tmux kill-session -t harness`: %v", gotArgs)
	}

	// (4) An allowlist-invalid --kill value errors before connecting.
	if err := run(tunnelOpts{killSession: "-bad; rm -rf /"}); err == nil {
		t.Error("invalid --kill value did not error")
	}
}

// TestRunTunnel_Rename exercises --rename: a bare "<new>" renames the env's persisted
// default and the sidecar FOLLOWS the rename (so a later no-flag connect reattaches the
// new name); an "<old>=<new>" form renames a specific session without touching the
// default unless old IS the default; and both names are allowlist-validated. The remote
// argv is a one-shot `tmux rename-session -t <old> <new>` with no FULMINATE_SESSION.
func TestRunTunnel_Rename(t *testing.T) {
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
	sidecar := filepath.Join(home, ".knowledge", "ssh", connectionName(env)+".session")
	run := func(opts tunnelOpts) error { gotArgs = nil; return runTunnel(context.Background(), srv.URL, env, opts) }
	readSidecar := func() string { data, _ := os.ReadFile(sidecar); return strings.TrimSpace(string(data)) }

	// Seed a persisted default via a first no-flag connect.
	if err := run(tunnelOpts{}); err != nil {
		t.Fatalf("seed connect: %v", err)
	}
	def := readSidecar()

	// (1) Bare --rename <new>: renames the default; sidecar follows to the new name.
	if err := run(tunnelOpts{renameSession: "feature-x"}); err != nil {
		t.Fatalf("--rename <new>: %v", err)
	}
	joined := strings.Join(gotArgs, " ")
	if strings.Contains(joined, "FULMINATE_SESSION") {
		t.Errorf("--rename must not thread FULMINATE_SESSION: %v", gotArgs)
	}
	if n := len(gotArgs); n < 5 || gotArgs[n-5] != "tmux" || gotArgs[n-4] != "rename-session" || gotArgs[n-3] != "-t" || gotArgs[n-2] != def || gotArgs[n-1] != "feature-x" {
		t.Errorf("--rename argv must end with `tmux rename-session -t %s feature-x`: %v", def, gotArgs)
	}
	if got := readSidecar(); got != "feature-x" {
		t.Errorf("sidecar did not follow the rename of the default: want feature-x, got %q", got)
	}

	// (2) --rename <old>=<new> for a NON-default session leaves the default untouched.
	if err := run(tunnelOpts{renameSession: "other-sess=renamed"}); err != nil {
		t.Fatalf("--rename old=new: %v", err)
	}
	if n := len(gotArgs); gotArgs[n-2] != "other-sess" || gotArgs[n-1] != "renamed" {
		t.Errorf("--rename old=new argv must target `other-sess renamed`: %v", gotArgs)
	}
	if got := readSidecar(); got != "feature-x" {
		t.Errorf("renaming a non-default session must not change the default: want feature-x, got %q", got)
	}

	// (3) An allowlist-invalid new name errors before connecting.
	if err := run(tunnelOpts{renameSession: "-bad; rm -rf /"}); err == nil {
		t.Error("invalid --rename new name did not error")
	}
}

// TestRunTunnel_PrintProxy_EmitsSetEnv asserts the --print-proxy-command block emits
// a `SetEnv FULMINATE_SESSION=<name>` line matching the persisted per-env default, so
// a pasted ~/.ssh/config carries the session name to the VM.
func TestRunTunnel_PrintProxy_EmitsSetEnv(t *testing.T) {
	t.Setenv("KNOWLEDGE_AUTH_TOKEN", "tok")
	home := t.TempDir()
	t.Setenv("HOME", home)

	srv := hostCertConnectServer(t, func(env string) string { return "ssh-ed25519 CA-" + env })
	defer srv.Close()

	out, err := captureStdout(t, func() error {
		return runTunnel(context.Background(), srv.URL, "api-dev", tunnelOpts{printProxy: true})
	})
	if err != nil {
		t.Fatalf("runTunnel printProxy: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(home, ".knowledge", "ssh", connectionName("api-dev")+".session"))
	if err != nil {
		t.Fatalf("read persisted sidecar: %v", err)
	}
	name := strings.TrimSpace(string(data))
	if !strings.Contains(out, "SetEnv FULMINATE_SESSION="+name) {
		t.Errorf("print-proxy block missing `SetEnv FULMINATE_SESSION=%s`:\n%s", name, out)
	}
}
