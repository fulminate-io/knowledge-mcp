// SPDX-License-Identifier: Apache-2.0

// mcp_register.go — shared helper that registers the knowledge MCP
// server with a client CLI (claude / codex) at install time. Both
// installers call this after priming their global-instruction file, so
// a brew/tarball install also wires the daemon MCP endpoint up — no
// manual `claude mcp add` step.
//
// The registered transport is the `knowledge serve` daemon's loopback
// streamable-HTTP endpoint (daemonMCPURL), NOT a per-session stdio
// `knowledge` child. Both registrations also set a generous per-server
// tool-call timeout so legitimately-long ops (collect, large reads) are
// not cut off by the client's ~60s default:
//
//   - claude: `mcp add-json -s user knowledge '<json>'`, where the JSON
//     is an http server config carrying the per-server "timeout" field in
//     MILLISECONDS (mcpToolTimeoutMs). `claude mcp add` has no timeout
//     flag; only add-json accepts a full config including timeout.
//   - codex: `mcp add knowledge --url <url>` (codex has no timeout flag),
//     followed by a read-modify-write of ~/.codex/config.toml that sets
//     mcp_servers.knowledge.tool_timeout_sec in SECONDS (mcpToolTimeoutSec).
//     The patch preserves every other entry/table in the file.
//
// Registration is a best-effort idempotent upsert: `mcp remove knowledge`
// (ignore a not-found exit), then the client-specific add.
//
// NON-FATAL by contract: a missing client CLI, a failing add command, or
// a failing config patch log a slog.Warn and return nil so the asset
// install never aborts on MCP registration.
//
// CLI mode, not MCP mode: writing the dry-run argv to stdout is
// legitimate here (mirrors the install_*_assets.go subcommands).

package bootstrap

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	toml "github.com/pelletier/go-toml/v2"

	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
)

// mcpServerName is the registered name of the knowledge MCP server,
// shared by the remove + add commands so the upsert targets one entry.
const mcpServerName = "knowledge"

// mcpToolTimeoutMs is the per-server tool-call timeout (MILLISECONDS) set
// on the claude registration via the add-json "timeout" field. It is
// generous on purpose: a legitimately-long op (collect, large reads) on
// the Claude↔local-daemon hop was being cut off by the client's ~60s
// default, surfacing false "operation timed out" errors.
const mcpToolTimeoutMs = 180000

// mcpToolTimeoutSec is the same timeout expressed in SECONDS for the
// codex config.toml tool_timeout_sec field. Derived from mcpToolTimeoutMs
// so the two clients stay consistent (180000 ms == 180 s).
const mcpToolTimeoutSec = mcpToolTimeoutMs / 1000

// daemonMCPURL returns the loopback streamable-HTTP MCP endpoint the
// `knowledge serve` daemon mounts (/mcp) on its default port. Editors
// register against this URL instead of spawning a per-session stdio
// `knowledge` child. The port is graphclient.DefaultMCPHTTPPort — the
// daemon's default --http-port — and must match the path A's daemon mounts
// (graphclient.HTTPServer serves /mcp on 127.0.0.1:<port>).
func daemonMCPURL() string {
	return fmt.Sprintf("http://127.0.0.1:%d/mcp", graphclient.DefaultMCPHTTPPort)
}

// execCommand is a stubbable alias for exec.Command. Production uses the
// real one; the argv-shape / --no-mcp tests put a recording fake client
// on PATH and exercise the real exec path, but the alias keeps the seam
// open for finer-grained stubbing if needed.
var execCommand = exec.Command

// claudeServerJSON marshals the claude add-json server config for the
// knowledge MCP server: a streamable-HTTP server at url carrying the
// per-server "timeout" (MILLISECONDS). Built with encoding/json so the
// quoting is correct regardless of url contents.
func claudeServerJSON(url string) (string, error) {
	cfg := struct {
		Type    string `json:"type"`
		URL     string `json:"url"`
		Timeout int    `json:"timeout"`
	}{Type: "http", URL: url, Timeout: mcpToolTimeoutMs}
	b, err := json.Marshal(cfg)
	if err != nil {
		return "", fmt.Errorf("marshal claude mcp server config: %w", err)
	}
	return string(b), nil
}

// addArgvFor returns the full `mcp add...` argv (verb + scope flags +
// client-specific tail) that registers mcpServerName at url:
//
//   - claude: `mcp add-json [-s user] knowledge '<json>'` — the JSON
//     config carries the per-server timeout (claude mcp add has no
//     timeout flag; only add-json takes a full config).
//   - codex:  `mcp add knowledge --url <url>` — codex names the
//     streamable-HTTP target with --url and has no timeout flag; the
//     timeout is applied separately via the config.toml patch.
//
// scopeArgs carries the client-specific scope flags (claude: ["-s",
// "user"]; codex: nil).
func addArgvFor(clientBin string, scopeArgs []string, url string) ([]string, error) {
	if clientBin == "codex" {
		argv := append([]string{"mcp", "add"}, scopeArgs...)
		return append(argv, mcpServerName, "--url", url), nil
	}
	serverJSON, err := claudeServerJSON(url)
	if err != nil {
		return nil, err
	}
	argv := append([]string{"mcp", "add-json"}, scopeArgs...)
	return append(argv, mcpServerName, serverJSON), nil
}

// codexConfigPath returns the path to the codex config.toml the
// tool_timeout_sec patch targets (~/.codex/config.toml).
func codexConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".codex", "config.toml"), nil
}

// patchCodexToolTimeout sets mcp_servers.knowledge.tool_timeout_sec in the
// codex config.toml via a read-modify-write that PRESERVES every other
// entry/table in the file, creating the file/table if absent. Only
// tool_timeout_sec is set — startup_timeout_sec and all other keys are
// left untouched. In dryRun it prints what it would write and writes
// nothing. A failure is NON-FATAL: warn and return nil.
func patchCodexToolTimeout(dryRun bool) error {
	path, err := codexConfigPath()
	if err != nil {
		slog.Warn("knowledge: cannot resolve codex config path; skipping tool_timeout_sec patch", "error", err)
		return nil
	}

	// Read-modify-write into a generic map so unrelated tables/keys survive
	// the round-trip verbatim.
	root := map[string]any{}
	if data, rerr := os.ReadFile(path); rerr == nil {
		if uerr := toml.Unmarshal(data, &root); uerr != nil {
			slog.Warn("knowledge: codex config.toml is unparseable; skipping tool_timeout_sec patch",
				"path", path, "error", uerr)
			return nil
		}
	} else if !os.IsNotExist(rerr) {
		slog.Warn("knowledge: cannot read codex config.toml; skipping tool_timeout_sec patch",
			"path", path, "error", rerr)
		return nil
	}

	servers, _ := root["mcp_servers"].(map[string]any)
	if servers == nil {
		servers = map[string]any{}
		root["mcp_servers"] = servers
	}
	server, _ := servers[mcpServerName].(map[string]any)
	if server == nil {
		server = map[string]any{}
		servers[mcpServerName] = server
	}
	server["tool_timeout_sec"] = mcpToolTimeoutSec

	out, merr := toml.Marshal(root)
	if merr != nil {
		slog.Warn("knowledge: cannot marshal codex config.toml; skipping tool_timeout_sec patch",
			"path", path, "error", merr)
		return nil
	}

	if dryRun {
		fmt.Fprintf(os.Stdout, "  would write %s: mcp_servers.%s.tool_timeout_sec = %d\n",
			path, mcpServerName, mcpToolTimeoutSec)
		return nil
	}

	if mkErr := os.MkdirAll(filepath.Dir(path), 0o755); mkErr != nil { //nolint:gosec // ~/.codex is a user config dir
		slog.Warn("knowledge: cannot create codex config dir; skipping tool_timeout_sec patch",
			"path", path, "error", mkErr)
		return nil
	}
	if werr := os.WriteFile(path, out, 0o600); werr != nil {
		slog.Warn("knowledge: cannot write codex config.toml; skipping tool_timeout_sec patch",
			"path", path, "error", werr)
		return nil
	}
	fmt.Fprintf(os.Stdout, "  set mcp_servers.%s.tool_timeout_sec = %d in %s\n",
		mcpServerName, mcpToolTimeoutSec, path)
	return nil
}

// registerKnowledgeMCP registers the knowledge MCP server with the
// client named clientBin ("claude" / "codex"). scopeArgs carries the
// client-specific scope flags inserted before the add tail (claude:
// []string{"-s", "user"}; codex: nil — codex has no -s user flag). The
// registered server is the `knowledge serve` daemon's loopback
// streamable-HTTP endpoint (daemonMCPURL) — no stdio child, no
// own-executable resolution. Both registrations set a generous per-server
// tool-call timeout so long ops are not cut off by the client default:
// claude carries it in the add-json "timeout" field (ms), codex applies
// it via the config.toml tool_timeout_sec patch (sec). See addArgvFor and
// patchCodexToolTimeout.
//
// Behavior:
//   - Client CLI not on PATH → slog.Warn + return nil (NON-FATAL).
//   - dryRun → print the exact remove + add argv to stdout, and (codex)
//     print the config.toml patch it would write; run/write nothing.
//   - else → best-effort `mcp remove knowledge` (ignore failure), then
//     the client-specific `mcp add...`; an add failure logs a warn and
//     returns nil. For codex, also patch ~/.codex/config.toml.
func registerKnowledgeMCP(clientBin string, scopeArgs []string, dryRun bool) error {
	if _, err := exec.LookPath(clientBin); err != nil {
		// NON-FATAL by contract: a missing client CLI must not abort the
		// asset install — warn and return nil so the caller continues.
		slog.Warn("knowledge: client CLI not found; skipping MCP registration",
			"client", clientBin,
			"hint", fmt.Sprintf("install the %s CLI then re-run knowledge install-%s-assets", clientBin, clientBin))
		return nil //nolint:nilerr // missing-CLI is a deliberate non-fatal skip, not an error to propagate
	}

	url := daemonMCPURL()
	removeArgs := []string{"mcp", "remove", mcpServerName}
	addArgs, err := addArgvFor(clientBin, scopeArgs, url)
	if err != nil {
		slog.Warn("knowledge: cannot build MCP add command; skipping registration",
			"client", clientBin, "error", err)
		return nil
	}

	if dryRun {
		fmt.Fprintf(os.Stdout, "  would run: %s %s\n", clientBin, strings.Join(removeArgs, " "))
		fmt.Fprintf(os.Stdout, "  would run: %s %s\n", clientBin, strings.Join(addArgs, " "))
		if clientBin == "codex" {
			return patchCodexToolTimeout(dryRun)
		}
		return nil
	}

	// Best-effort remove so the add is an idempotent upsert. A
	// not-found / non-zero exit is expected on first install — ignore it.
	_ = execCommand(clientBin, removeArgs...).Run()

	if err := execCommand(clientBin, addArgs...).Run(); err != nil {
		slog.Warn("knowledge: MCP registration failed; register manually with `<client> mcp add`",
			"client", clientBin, "error", err)
		return nil
	}
	fmt.Fprintf(os.Stdout, "  registered knowledge MCP server with %s (%s)\n", clientBin, url)

	// codex has no timeout flag on `mcp add`; apply the per-server
	// tool-call timeout via the config.toml patch (non-fatal).
	if clientBin == "codex" {
		return patchCodexToolTimeout(dryRun)
	}
	return nil
}
