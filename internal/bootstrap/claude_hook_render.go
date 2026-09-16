// SPDX-License-Identifier: Apache-2.0

// claude_hook_render.go — rendering the embedded Claude hook asset at the port
// this install's daemon actually serves on, and reading that port back out of a
// settings.json the installer already wrote.
//
// The session hook POSTs to the daemon, so its url names a port. The port is a
// per-install choice (--mcp-port, the same value the MCP registration uses), so
// the asset ships with a PLACEHOLDER rather than a literal and the installer
// renders it. That makes the asset bytes no longer a constant, which has one
// consequence worth stating plainly: the doctor's drift check cannot compare
// against a default-port rendering, or every user on a non-default port would
// carry a permanent warning. It reads the port back off the file it is checking
// (claudeHookPortFromSettings) and re-renders at THAT port, so drift means the
// SHAPE changed — the port is a parameter, not drift.

package bootstrap

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// claudeHookPortPlaceholder is the token in assets.ClaudeHooks that the
// resolved --mcp-port replaces.
const claudeHookPortPlaceholder = "{{KNOWLEDGE_MCP_PORT}}"

// errNoHookPortPlaceholder is the sentinel the missing-placeholder refusal
// wraps, so a caller (and a test) recognizes the CONDITION with errors.Is
// instead of matching the rendered message. Message text is not a contract; the
// sentinel is.
var errNoHookPortPlaceholder = fmt.Errorf("embedded claude hook asset carries no %s placeholder to render the daemon port into",
	claudeHookPortPlaceholder)

// renderClaudeHooks returns the embedded hook asset with the port placeholder
// replaced by port. It FAILS LOUD when the asset carries no placeholder: a
// rendered-asset pipeline whose input stopped needing rendering would silently
// write whatever literal port an editor left behind, and every install would
// then point its session hook at one machine's port.
func renderClaudeHooks(asset []byte, port int) ([]byte, error) {
	if err := validateMCPPort(port); err != nil {
		return nil, err
	}
	if !bytes.Contains(asset, []byte(claudeHookPortPlaceholder)) {
		return nil, errNoHookPortPlaceholder
	}
	return bytes.ReplaceAll(asset, []byte(claudeHookPortPlaceholder), []byte(strconv.Itoa(port))), nil
}

// claudeHookPortFromSettings reports the daemon port named by the session
// hook's url in an already-written settings.json, and whether one was found.
//
// A miss (no file content, no managed session entry, a url that does not parse,
// a port that is not a number) is reported as such rather than defaulted: the
// caller decides what an unreadable port means, and for the drift check it
// means "compare at the default", which is the honest answer when there is no
// installed entry to have chosen a port.
func claudeHookPortFromSettings(data []byte) (int, bool) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		return 0, false
	}
	var hooks map[string]json.RawMessage
	if err := json.Unmarshal(top["hooks"], &hooks); err != nil {
		return 0, false
	}
	var entries []json.RawMessage
	if err := json.Unmarshal(hooks["PreToolUse"], &entries); err != nil {
		return 0, false
	}
	for _, raw := range entries {
		if port, ok := sessionHookPort(raw); ok {
			return port, true
		}
	}
	return 0, false
}

// sessionHookPort reports the port in a single PreToolUse entry's session-hook
// url, when that entry is the knowledge-managed session hook.
func sessionHookPort(raw json.RawMessage) (int, bool) {
	var entry preToolUseEntry
	if err := json.Unmarshal(raw, &entry); err != nil {
		return 0, false
	}
	for _, h := range entry.Hooks {
		if !h.carriesManagedMarker() || h.URL == "" {
			continue
		}
		if port, ok := hookURLPort(h.URL); ok {
			return port, true
		}
	}
	return 0, false
}

// hookURLPort reads the daemon port out of a hook endpoint url of the form
// http://127.0.0.1:<port>/hook/<harness>, and reports whether it found one. It
// is the ONE port reader both installers' drift checks share — the Claude hook
// carries its url in a settings.json field, the Codex hook inside a shell
// command string, and the same three-line read serves both.
//
// It does not use net/url deliberately: the shape is fixed and known (this
// package WRITES it), and a whole url parse would buy nothing a prefix scan
// does not already give. See the note in the implementation report about the
// census test this package carries, whose fixture picks an import that must
// stay test-only.
func hookURLPort(rawURL string) (int, bool) {
	_, rest, found := strings.Cut(rawURL, "http://127.0.0.1:")
	if !found {
		return 0, false
	}
	digits, _, found := strings.Cut(rest, "/")
	if !found {
		return 0, false
	}
	port, err := strconv.Atoi(digits)
	if err != nil {
		return 0, false
	}
	return port, true
}
