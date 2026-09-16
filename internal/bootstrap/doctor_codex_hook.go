// SPDX-License-Identifier: Apache-2.0

// doctor_codex_hook.go — the `codex-hook` diagnostic check.
//
// WHAT IT CAN AND CANNOT SEE, stated up front because the difference is the
// whole point. This check reads ~/.codex/config.toml: it can say whether the
// hook is installed and whether its definition is current. It CANNOT say
// whether the hook ever fired — that is a fact about deliveries the daemon
// received, and the daemon reports it per call on manage(status)'s
// session_source / session_reason ("codex hook installed but not observed
// firing"). So an installed, current hook is reported OK with the trust step
// named in the detail, because an installed hook that nobody trusted looks
// exactly like this from disk and Codex prints nothing either way.

package bootstrap

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
)

// checkCodexHook reports the on-disk state of the knowledge session hook in
// ~/.codex/config.toml: absent → warn with the install remediation; present but
// not the current definition → warn with the update remediation; present and
// current → ok, naming the manual /hooks trust step the hook needs to fire.
func checkCodexHook(mcpPort int, mcpPortKnown bool) checkResult {
	home, err := bootstrapHomeDir()
	if err != nil {
		return checkResult{name: "codex-hook", status: statusWarn, msg: "cannot resolve home dir: " + err.Error()}
	}
	path := filepath.Join(home, ".codex", "config.toml")
	installed, found := installedCodexHookCommand(path)
	if !found {
		return checkResult{
			name: "codex-hook", status: statusWarn,
			msg:    "no knowledge session hook in ~/.codex/config.toml",
			detail: "run `knowledge install-codex-assets` to install it, then trust it in Codex with /hooks",
		}
	}
	// Compare the SHAPE at the port the installed command names — the port is a
	// per-install choice, so comparing at the default would call every
	// non-default install drifted — and then compare that port to the one this
	// daemon actually serves on, which is the arm that catches a hook posting
	// where nothing listens. See claudeSettingsResult for why it is two
	// questions.
	port, portKnown := hookURLPort(installed)
	if !portKnown {
		port = graphclient.DefaultMCPHTTPPort
		if mcpPortKnown {
			port = mcpPort
		}
	}
	if installed != codexHookCommand(port) {
		return checkResult{
			name: "codex-hook", status: statusWarn,
			msg:    "knowledge session hook in ~/.codex/config.toml is out of date",
			detail: "run `knowledge install-codex-assets` to update",
		}
	}
	// Both ports must be KNOWN: the installed one read off the command, the
	// served one supplied by a caller that actually knows it. See
	// claudeSettingsResult for why a default is not a substitute.
	if portKnown && mcpPortKnown && port != mcpPort {
		return checkResult{
			name: "codex-hook", status: statusWarn,
			msg: fmt.Sprintf("the session hook posts to port %d but this daemon serves MCP on %d — deliveries reach nothing and sessions resolve to none",
				port, mcpPort),
			detail: fmt.Sprintf("run `knowledge install-codex-assets --mcp-port %d`", mcpPort),
		}
	}
	return checkResult{
		name: "codex-hook", status: statusOK,
		msg:    fmt.Sprintf("session hook installed (daemon port %d)", port),
		detail: "codex skips an untrusted hook silently — if manage(status) reports session_source codex-meta, run /hooks in Codex and trust it",
	}
}

// installedCodexHookCommand returns the command string of the knowledge-managed
// PreToolUse entry in the codex config at path, and whether one is installed. A
// config that does not exist, does not parse, or holds no managed entry is a
// clean "not installed" rather than an error: this is a diagnostic.
func installedCodexHookCommand(path string) (string, bool) {
	root, ok := readCodexConfig(path, "codex-hook check")
	if !ok {
		return "", false
	}
	hooks, _ := root["hooks"].(map[string]any)
	entries, _ := hooks["PreToolUse"].([]any)
	for _, entry := range entries {
		if cmd, found := managedCodexCommand(entry); found {
			return cmd, true
		}
	}
	return "", false
}

// managedCodexCommand returns the marked command inside one decoded PreToolUse
// entry, when that entry is the knowledge-managed one.
func managedCodexCommand(entry any) (string, bool) {
	m, ok := entry.(map[string]any)
	if !ok {
		return "", false
	}
	handlers, _ := m["hooks"].([]any)
	for _, h := range handlers {
		hm, ok := h.(map[string]any)
		if !ok {
			continue
		}
		cmd, _ := hm["command"].(string)
		if strings.Contains(cmd, knowledgeHookMarkerPrefix) {
			return cmd, true
		}
	}
	return "", false
}
