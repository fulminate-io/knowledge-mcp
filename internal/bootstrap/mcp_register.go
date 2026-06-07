// SPDX-License-Identifier: Apache-2.0

// mcp_register.go — shared helper that registers the knowledge MCP
// server with a client CLI (claude / codex) at install time. Both
// installers call this after priming their global-instruction file, so
// a brew/tarball install also wires the daemon MCP endpoint up — no
// manual `claude mcp add` step.
//
// The registered transport is the `knowledge serve` daemon's loopback
// streamable-HTTP endpoint (daemonMCPURL), NOT a per-session stdio
// `knowledge` child: claude uses `mcp add -s user --transport http
// knowledge <url>`, codex registers the same loopback url via its own
// url-form (handled by the codex install path). Registration is a
// best-effort idempotent upsert: `mcp remove knowledge` (ignore a
// not-found exit), then `mcp add ...`.
//
// NON-FATAL by contract: a missing client CLI or a failing add command
// log a slog.Warn and return nil so the asset install never aborts on
// MCP registration.
//
// CLI mode, not MCP mode: writing the dry-run argv to stdout is
// legitimate here (mirrors the install_*_assets.go subcommands).

package bootstrap

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
)

// mcpServerName is the registered name of the knowledge MCP server,
// shared by the remove + add commands so the upsert targets one entry.
const mcpServerName = "knowledge"

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

// httpAddTail returns the `mcp add` argv tail (after the scope flags)
// that registers mcpServerName as a streamable-HTTP server at url, in the
// client-specific flag shape:
//
//   - claude: `--transport http knowledge <url>`
//     (claude mcp add [-s user] --transport http <name> <url>)
//   - codex:  `knowledge --url <url>`
//     (codex mcp add <name> --url <url>; codex has no --transport flag and
//     names the streamable-HTTP target with --url)
//
// Both CLIs share the `mcp add` verb + the loopback daemon url; only the
// transport-flag spelling differs, so one registration path covers both.
func httpAddTail(clientBin, url string) []string {
	if clientBin == "codex" {
		return []string{mcpServerName, "--url", url}
	}
	return []string{"--transport", "http", mcpServerName, url}
}

// registerKnowledgeMCP registers the knowledge MCP server with the
// client named clientBin ("claude" / "codex"). scopeArgs carries the
// client-specific scope flags inserted before the transport flags
// (claude: []string{"-s", "user"}; codex: nil — codex has no -s user
// flag). The registered server is the `knowledge serve` daemon's loopback
// streamable-HTTP endpoint (daemonMCPURL) — no stdio child, no
// own-executable resolution. The add-argv transport shape is
// client-specific (see httpAddTail): claude `--transport http <name>
// <url>`, codex `<name> --url <url>`.
//
// Behavior:
//   - Client CLI not on PATH → slog.Warn + return nil (NON-FATAL).
//   - dryRun → print the exact remove + add argv to stdout, run nothing.
//   - else → best-effort `mcp remove knowledge` (ignore failure), then
//     `mcp add ...`; an add failure logs a warn and returns nil.
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
	addArgs := append([]string{"mcp", "add"}, scopeArgs...)
	addArgs = append(addArgs, httpAddTail(clientBin, url)...)

	if dryRun {
		fmt.Fprintf(os.Stdout, "  would run: %s %s\n", clientBin, strings.Join(removeArgs, " "))
		fmt.Fprintf(os.Stdout, "  would run: %s %s\n", clientBin, strings.Join(addArgs, " "))
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
	return nil
}
