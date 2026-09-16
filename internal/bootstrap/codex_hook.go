// SPDX-License-Identifier: Apache-2.0

// codex_hook.go — the Codex PreToolUse hook `install-codex-assets` writes, and
// the read-modify-write that installs it into ~/.codex/config.toml.
//
// WHY A `command` HANDLER AND NOT AN `http` ONE. Codex 0.154.0's hook handler
// vocabulary is command / mcp_tool / prompt / agent — there is NO http handler.
// The Claude shape (a hook that POSTs by itself) cannot be copied; the Codex
// insurance hook is a `command` handler whose process reads the hook JSON on
// stdin and POSTs it to the daemon's hook endpoint.
//
// THE HOOK IS INERT UNTIL THE USER TRUSTS IT, and nothing an installer writes
// can change that. Codex records trust against the hook definition's hash and
// skips an untrusted hook SILENTLY — no warning on stdout, stderr, the --json
// event stream or `codex doctor`. An installer-written `trusted_hash` is
// ignored; `--dangerously-bypass-hook-trust` is per-invocation only and is not
// something an installer may put in a user's command line; and codex-cli
// exposes no programmatic trust path at all (app-server carries a read-only
// hooks/list and no trust write). So `install-codex-assets` writes the hook AND
// PRINTS THE ONE MANUAL STEP, and the daemon reports the untrusted state as
// "codex hook installed but not observed firing" — because nothing in Codex
// will say it.
//
// NO SECRET IS IN THE COMMAND. It lands in the user's config.toml in plain
// text, so it carries none; the daemon's hook endpoint is loopback-only and
// unauthenticated by design, which is what makes that safe.

package bootstrap

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	toml "github.com/pelletier/go-toml/v2"
)

// codexHookMatcher is the PreToolUse matcher: every knowledge MCP tool, in the
// same mcp__<server>__<tool> matcher grammar Claude uses (Codex reports an MCP
// call's tool_name in that form). The session must be resolvable on ANY call,
// so the matcher is not narrowed to one tool.
const codexHookMatcher = "mcp__knowledge__.*"

// codexHookMarker is the sentinel that makes the install idempotent: the
// installer removes every PreToolUse entry whose command carries it before
// appending the current one, so a re-run refreshes rather than duplicates and a
// port change does not strand the old entry.
const codexHookMarker = knowledgeSessionHookMarker

// codexHookStatusMessage is what Codex shows while the hook runs.
const codexHookStatusMessage = "knowledge session hook"

// codexHookCommand returns the shell command the hook handler runs: read the
// hook JSON on stdin, POST it to the daemon's Codex hook endpoint on port.
//
// IT EXITS 0 WHATEVER HAPPENS, and that is a deliberate match to the harness
// contract rather than a silent degrade of ours. Claude's own http hook is
// non-blocking on connection failure by documented design ("Connection failure:
// non-blocking error, execution continues"), so a daemon that is down never
// blocks a Claude tool call; the Codex command hook is written to behave
// identically so the two harnesses do not differ on whether the user's work
// stops when the daemon is restarting. The daemon's OWN refusals are fatal and
// loud — that is where a failure is reported, and an undelivered hook surfaces
// as a `none`/inert resolution in manage(status), never as a guess.
func codexHookCommand(port int) string {
	return fmt.Sprintf(
		"sh -c ': %s; curl -sS --max-time 2 -X POST -H \"Content-Type: application/json\" "+
			"--data-binary @- %s >/dev/null 2>&1; exit 0'",
		codexHookMarker, daemonHookURL(port, "codex"))
}

// codexHookEntry builds the PreToolUse matcher-group entry as the generic map
// the TOML round-trip carries, so it is written as an inline [hooks] table
// alongside whatever else the user's config.toml holds.
func codexHookEntry(port int) map[string]any {
	return map[string]any{
		"matcher": codexHookMatcher,
		"hooks": []any{map[string]any{
			"type":          "command",
			"command":       codexHookCommand(port),
			"statusMessage": codexHookStatusMessage,
		}},
	}
}

// patchCodexSessionHook installs the session hook into ~/.codex/config.toml via
// a read-modify-write that PRESERVES every other entry and table, creating the
// file/table if absent. A pre-existing knowledge-managed entry is REPLACED (the
// marker identifies it); every user-authored PreToolUse entry survives in order.
// In dryRun it prints what it would write and writes nothing.
//
// A failure is NON-FATAL and warns, matching patchCodexToolTimeout's contract:
// the asset install must not abort because a config file could not be patched.
//
// IT RETURNS NOTHING, deliberately. Every path here is either a success or a
// warned skip, so an `error` result could only ever be nil — a vacuous result
// the caller would have to check and that would tell it nothing (owner
// 2026-09-11: "code that is not an error or can never be an error shouldnt
// really have an error return value"). The sibling patchCodexToolTimeout still
// carries one; this does not copy it.
func patchCodexSessionHook(port int, dryRun bool) {
	path, err := codexConfigPath()
	if err != nil {
		slog.Warn("knowledge: cannot resolve codex config path; skipping session-hook patch", "error", err)
		return
	}
	root, ok := readCodexConfig(path, "session-hook patch")
	if !ok {
		return
	}
	if !setCodexSessionHook(root, port, path) {
		return
	}

	out, merr := toml.Marshal(root)
	if merr != nil {
		slog.Warn("knowledge: cannot marshal codex config.toml; skipping session-hook patch",
			"path", path, "error", merr)
		return
	}
	if dryRun {
		fmt.Fprintf(os.Stdout, "  would write %s: hooks.PreToolUse knowledge session hook → daemon port %d\n", path, port)
		printCodexHookTrustStep(path)
		return
	}
	if !writeCodexConfig(path, out, "session-hook patch") {
		return
	}
	fmt.Fprintf(os.Stdout, "  wrote the knowledge session hook to %s (daemon port %d)\n", path, port)
	printCodexHookTrustStep(path)
}

// printCodexHookTrustStep prints the ONE MANUAL STEP that arms the hook. It is
// printed on the write path AND on --dry-run, because it is the only thing
// standing between a user and a hook that is installed, silent and doing
// nothing: Codex skips an untrusted hook without a word, and no installer-side
// write can grant the trust.
func printCodexHookTrustStep(path string) {
	fmt.Fprintf(os.Stdout,
		"  ACTION REQUIRED: the codex hook in %s is INERT until you trust it — open Codex, run /hooks, and trust the knowledge session hook\n",
		path)
}

// setCodexSessionHook replaces the knowledge-managed PreToolUse entry in the
// decoded config root, preserving every user entry in its original order and
// appending ours at the end when none was present. It reports whether the patch
// may be written.
//
// AN UNEXPECTED TYPE IS A REFUSAL, NOT A REPLACEMENT. `hooks` is expected to be
// a table and `hooks.PreToolUse` a list; a comma-ok assert that discards the
// mismatch would quietly overwrite whatever the user actually had there —
// `hooks = "x"`, or a `[[hooks]]` array-of-tables — and the user's own
// configuration is not ours to reinterpret. On a mismatch this warns naming the
// KEY and the TYPE OBSERVED and reports false, and the caller writes nothing:
// the same warn-and-skip contract readCodexConfig and patchCodexToolTimeout
// hold when the file cannot be parsed at all.
func setCodexSessionHook(root map[string]any, port int, path string) bool {
	raw, present := root["hooks"]
	hooks, ok := raw.(map[string]any)
	switch {
	case !present || raw == nil:
		hooks = map[string]any{}
		root["hooks"] = hooks
	case !ok:
		slog.Warn("knowledge: codex config.toml `hooks` is not a table; refusing to overwrite it — skipping session-hook patch",
			"path", path, "key", "hooks", "observed_type", fmt.Sprintf("%T", raw))
		return false
	}

	existing, ok := hooks["PreToolUse"].([]any)
	if rawPre, prePresent := hooks["PreToolUse"]; prePresent && rawPre != nil && !ok {
		slog.Warn("knowledge: codex config.toml `hooks.PreToolUse` is not a list; refusing to overwrite it — skipping session-hook patch",
			"path", path, "key", "hooks.PreToolUse", "observed_type", fmt.Sprintf("%T", rawPre))
		return false
	}

	kept := make([]any, 0, len(existing)+1)
	for _, entry := range existing {
		if !codexEntryIsManaged(entry) {
			kept = append(kept, entry)
		}
	}
	hooks["PreToolUse"] = append(kept, codexHookEntry(port))
	return true
}

// codexEntryIsManaged reports whether a decoded PreToolUse entry is the
// knowledge-managed one, by the marker its command carries. As on the Claude
// side the marker is the sentinel, not the matcher: a user may legitimately
// have their own entry on the same matcher, and it must survive.
func codexEntryIsManaged(entry any) bool {
	m, ok := entry.(map[string]any)
	if !ok {
		return false
	}
	handlers, _ := m["hooks"].([]any)
	for _, h := range handlers {
		hm, ok := h.(map[string]any)
		if !ok {
			continue
		}
		if cmd, _ := hm["command"].(string); strings.Contains(cmd, knowledgeHookMarkerPrefix) {
			return true
		}
	}
	return false
}

// readCodexConfig reads and decodes the codex config.toml into a generic map so
// unrelated tables and keys survive the round-trip verbatim. It reports false
// (having warned, naming what is being skipped) when the file exists but cannot
// be read or parsed; an ABSENT file is a success returning an empty root.
func readCodexConfig(path, what string) (map[string]any, bool) {
	root := map[string]any{}
	data, rerr := os.ReadFile(path) //nolint:gosec // ~/.codex/config.toml, resolved through bootstrapHomeDir
	switch {
	case rerr == nil:
		if uerr := toml.Unmarshal(data, &root); uerr != nil {
			slog.Warn("knowledge: codex config.toml is unparseable; skipping "+what, "path", path, "error", uerr)
			return nil, false
		}
	case !os.IsNotExist(rerr):
		slog.Warn("knowledge: cannot read codex config.toml; skipping "+what, "path", path, "error", rerr)
		return nil, false
	}
	return root, true
}

// writeCodexConfig creates the config dir if needed and writes out, warning and
// reporting false on either failure.
func writeCodexConfig(path string, out []byte, what string) bool {
	// 0750, not the 0755 the neighboring tool_timeout patch still carries: the
	// directory is the user's own ~/.codex and only their own Codex CLI, running
	// as them, traverses it. A new site should not need an exemption.
	if mkErr := os.MkdirAll(filepath.Dir(path), 0o750); mkErr != nil {
		slog.Warn("knowledge: cannot create codex config dir; skipping "+what, "path", path, "error", mkErr)
		return false
	}
	if werr := os.WriteFile(path, out, 0o600); werr != nil {
		slog.Warn("knowledge: cannot write codex config.toml; skipping "+what, "path", path, "error", werr)
		return false
	}
	return true
}
