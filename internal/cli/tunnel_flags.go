// SPDX-License-Identifier: Apache-2.0

// tunnel_flags.go — argument parsing for `knowledge tunnel`. It owns two things
// Go's stdlib `flag` does not give for free: (1) local flags that may appear in
// ANY order relative to the <env> positional (stdlib `flag` stops parsing at the
// first non-flag arg, so `tunnel <env> --new` would otherwise leave `--new`
// unparsed — and, worse, leak it into the ssh argv), and (2) a standalone `--`
// that cleanly separates the flag side from a verbatim remote command. TunnelCmd
// (tunnel.go) wires these into the connect flow.

package cli

import (
	"flag"
	"fmt"
	"os"
)

// tunnelFlags holds pointers to every `knowledge tunnel` flag value, populated by
// newTunnelFlagSet so TunnelCmd and the tests parse against the identical set.
type tunnelFlags struct {
	printProxy   *bool
	proxy        *bool
	newSession   *bool
	reuse        *string
	session      *string
	listSessions *bool
	kill         *string
	rename       *string
	command      *string
}

// newTunnelFlagSet builds the tunnel FlagSet and its value bindings. Kept separate
// from TunnelCmd so the interspersed-parse behavior can be exercised in a test
// against the real flag definitions, not a stand-in.
func newTunnelFlagSet() (*flag.FlagSet, *tunnelFlags) {
	fs := flag.NewFlagSet("tunnel", flag.ContinueOnError)
	fs.SetOutput(os.Stdout)
	fs.Usage = func() { fmt.Fprint(os.Stdout, tunnelUsage) }
	tf := &tunnelFlags{
		printProxy: fs.Bool("print-proxy-command", false,
			"print a ~/.ssh/config ProxyCommand line instead of connecting"),
		proxy: fs.Bool("proxy", false,
			"act as the SSH ProxyCommand transport: dial the relay ws and pipe stdin/stdout (used internally by the emitted ProxyCommand line)"),
		newSession: fs.Bool("new", false,
			"rotate to a fresh dev session, overwriting the persisted per-env default"),
		reuse: fs.String("reuse", "",
			"attach a specific existing session by name (does not change the persisted default)"),
		session: fs.String("session", "",
			"alias for --reuse: attach a specific session by name"),
		listSessions: fs.Bool("list-sessions", false,
			"list the dev env's existing tmux sessions (names usable with --reuse) and exit"),
		kill: fs.String("kill", "",
			"kill (destroy) the named tmux session in the dev env and exit"),
		rename: fs.String("rename", "",
			"rename a session and exit: --rename <new> renames the env's default, --rename <old>=<new> renames a specific session"),
		command: fs.String("command", "",
			"run a single remote command non-interactively (no PTY) and exit with its status; give this flag before the env name, or pass `-- <cmd...>` after it"),
	}
	return fs, tf
}

// splitAtDoubleDash splits args at the FIRST standalone "--": the tokens before it
// (subject to flag parsing — local flags in any order plus the <env> positional)
// and the tokens after it (the verbatim remote command). This boundary is what
// disambiguates a local flag (`tunnel <env> --new`) from a remote command's own
// flags (`tunnel <env> -- ls -la`): everything past `--` is never flag-parsed. A
// trailing `--` with nothing after yields an empty (non-nil) command; no `--` at
// all yields a nil command and all args on the flag side.
func splitAtDoubleDash(args []string) (flagArgs, cmd []string) {
	for i, a := range args {
		if a == "--" {
			return args[:i], args[i+1:]
		}
	}
	return args, nil
}

// parseTunnelFlags parses the flag side allowing flags to appear in ANY order
// relative to the <env> positional. Go's `flag` stops at the first non-flag arg,
// so it permutes: parse until a positional, set that aside, then resume parsing
// the remainder. The first positional is the env name (extra positionals are
// ignored); flag.ErrHelp is returned verbatim for --help so the caller can exit 0.
func parseTunnelFlags(fs *flag.FlagSet, args []string) (env string, err error) {
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return "", err
		}
		if fs.NArg() == 0 {
			break
		}
		positional = append(positional, fs.Arg(0))
		args = fs.Args()[1:]
	}
	if len(positional) > 0 {
		env = positional[0]
	}
	return env, nil
}
