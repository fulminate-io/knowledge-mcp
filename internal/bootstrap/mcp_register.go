// SPDX-License-Identifier: Apache-2.0

// mcp_register.go — shared helper that registers the knowledge MCP
// server with a client CLI (claude / codex) at install time. Both
// installers call this after priming their global-instruction file, so
// a brew/tarball install also wires the stdio MCP server up — no manual
// `claude mcp add` step.
//
// The body is generic over the client binary name + scope args: claude
// uses `mcp add -s user knowledge -- <abs>`, codex uses the scope-less
// `mcp add knowledge -- <abs>` (codex has no -s user flag). Registration
// is a best-effort idempotent upsert: `mcp remove knowledge` (ignore a
// not-found exit), then `mcp add ...`.
//
// NON-FATAL by contract: a missing client CLI, an unresolvable binary
// path, or a failing add command all log a slog.Warn and return nil so
// the asset install never aborts on MCP registration.
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
)

// mcpServerName is the registered name of the knowledge MCP server,
// shared by the remove + add commands so the upsert targets one entry.
const mcpServerName = "knowledge"

// execCommand is a stubbable alias for exec.Command. Production uses the
// real one; the argv-shape / --no-mcp tests put a recording fake client
// on PATH and exercise the real exec path, but the alias keeps the seam
// open for finer-grained stubbing if needed.
var execCommand = exec.Command

// registerKnowledgeMCP registers the knowledge MCP server with the
// client named clientBin (e.g. "claude" / "codex"). scopeArgs carries
// the client-specific scope flags inserted before the server name
// (claude: []string{"-s", "user"}; codex: nil). The registered command
// is the absolute path to the running stdio binary (resolved via the
// stubbable getExecutable), run with no extra args (stdio is the
// default transport for both clients).
//
// Behavior:
//   - Client CLI not on PATH → slog.Warn + return nil (NON-FATAL).
//   - getExecutable errors → slog.Warn + return nil (NON-FATAL).
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

	abs, err := getExecutable()
	if err != nil {
		slog.Warn("knowledge: could not resolve own executable path; skipping MCP registration",
			"client", clientBin, "error", err)
		return nil
	}

	removeArgs := []string{"mcp", "remove", mcpServerName}
	addArgs := append([]string{"mcp", "add"}, scopeArgs...)
	addArgs = append(addArgs, mcpServerName, "--", abs)

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
	fmt.Fprintf(os.Stdout, "  registered knowledge MCP server with %s (%s)\n", clientBin, abs)
	return nil
}
