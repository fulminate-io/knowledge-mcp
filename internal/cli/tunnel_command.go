// SPDX-License-Identifier: Apache-2.0

// tunnel_command.go — the non-interactive single-command mode for `knowledge
// tunnel` (SSM-style). A remote command supplied via `--command "<cmd>"` or a
// `-- <cmd...>` passthrough is appended AFTER the ssh target with NO -t: ssh then
// requests no PTY, sshd sets SSH_ORIGINAL_COMMAND, and the dev-VM cloud-config
// ForceCommand runs it through its existing non-interactive branch (a bare
// `ctr task exec` with no -t) and propagates the exit code. Such an exec is
// ephemeral — it attaches to NO tmux session, so FULMINATE_SESSION is never
// threaded for it. runTunnel (tunnel.go) orchestrates; execSSH (tunnel_ssh.go)
// already passes stdio through and returns the *exec.ExitError carrying the
// remote status.

package cli

import (
	"context"
	"fmt"
)

// runRename renames a dev-VM tmux session via a one-shot `tmux rename-session -t <old>
// <new>` over the non-interactive branch, then — when the renamed session was this
// env's persisted default — follows the rename in the sidecar so a later no-flag
// connect reattaches the new name rather than minting a fresh default. Both names are
// allowlist-validated by parseRenameSpec before they reach the remote argv.
func runRename(ctx context.Context, dir, env, keyPath, certPath, target, knownHostsPath, proxyCmd, spec string) error {
	old, newName, err := parseRenameSpec(spec, dir, env)
	if err != nil {
		return err
	}
	if err := runTunnelCommand(ctx, keyPath, certPath, target, knownHostsPath, proxyCmd, []string{"tmux", "rename-session", "-t", old, newName}); err != nil {
		return err
	}
	if cur, ok := readSessionSidecar(dir, env); ok && cur == old {
		return writeSessionSidecar(dir, env, newName)
	}
	return nil
}

// resolveTunnelCommand reconciles the two ways a one-shot remote command is
// supplied and returns the command tokens to append after the ssh target (nil for
// the interactive path):
//
//   - --command "<cmd>"  → a single shell string:
//     `knowledge tunnel --command "cat ~/.knowledge/config" <env>`.
//   - -- <cmd...>         → the verbatim tokens after a standalone `--`:
//     `knowledge tunnel <env> -- cat ~/.knowledge/config`. dashCmd is exactly
//     those tokens (splitAtDoubleDash already stripped the `--` itself).
//
// Supplying BOTH forms is an error. A command present implies non-interactive.
func resolveTunnelCommand(command string, dashCmd []string) ([]string, error) {
	if command != "" && len(dashCmd) > 0 {
		return nil, fmt.Errorf("a remote command was given both via --command and as a `-- <cmd...>` passthrough — use one, not both")
	}
	if command != "" {
		return []string{command}, nil
	}
	if len(dashCmd) == 0 {
		return nil, nil
	}
	return dashCmd, nil
}

// runOneShot dispatches the non-interactive short-circuits that attach to NO tmux
// session — --rename (`tmux rename-session`, with a persisted-default follow),
// --list-sessions (`tmux ls`), --kill (`tmux kill-session -t <name>`, with the name
// allowlist-validated before it is spliced into the remote argv), and one-shot
// command mode (--command / `-- <cmd...>`). Each runs a single command over
// the ForceCommand's non-interactive branch with no FULMINATE_SESSION threaded. It
// returns handled=true when it ran one (the caller returns err); handled=false means
// none was requested and the caller falls through to the interactive session path.
// --print-proxy-command suppresses command mode (a one-shot command is not part of
// ~/.ssh/config), so that config block is emitted instead.
func runOneShot(ctx context.Context, dir, env, keyPath, certPath, target, knownHostsPath, proxyCmd string, opts tunnelOpts) (handled bool, err error) {
	switch {
	case opts.renameSession != "":
		return true, runRename(ctx, dir, env, keyPath, certPath, target, knownHostsPath, proxyCmd, opts.renameSession)
	case opts.listSessions:
		return true, runTunnelCommand(ctx, keyPath, certPath, target, knownHostsPath, proxyCmd, []string{"tmux", "ls"})
	case opts.killSession != "":
		if !sessionNameRE.MatchString(opts.killSession) {
			return true, fmt.Errorf("invalid session name %q: must match %s (letters/digits/_/- only, no leading hyphen, ≤64 chars)", opts.killSession, sessionNamePattern)
		}
		return true, runTunnelCommand(ctx, keyPath, certPath, target, knownHostsPath, proxyCmd, []string{"tmux", "kill-session", "-t", opts.killSession})
	case len(opts.command) > 0 && !opts.printProxy:
		return true, runTunnelCommand(ctx, keyPath, certPath, target, knownHostsPath, proxyCmd, opts.command)
	}
	return false, nil
}

// runTunnelCommand runs a one-shot remote command over the tunnel: it appends the
// command AFTER the ssh target with NO -t (so ssh requests no PTY, sshd sets
// SSH_ORIGINAL_COMMAND, and the dev-VM ForceCommand runs it non-interactively),
// keeps the host-cert verification options unchanged, and threads NO
// FULMINATE_SESSION (an ephemeral exec attaches to no tmux session). stdio is
// passed through and the remote exit code is surfaced by execSSH returning the
// *exec.ExitError, whose code RunSubcommand propagates as the process exit code.
func runTunnelCommand(ctx context.Context, keyPath, certPath, target, knownHostsPath, proxyCmd string, command []string) error {
	args := buildSSHArgs(keyPath, certPath, target, []string{
		"-o", "UserKnownHostsFile=" + knownHostsPath,
		"-o", "StrictHostKeyChecking=yes",
		"-o", "ProxyCommand=" + proxyCmd,
	})
	args = append(args, command...)
	return sshRunner(ctx, args)
}
