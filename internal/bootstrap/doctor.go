// SPDX-License-Identifier: Apache-2.0

// doctor.go — `knowledge doctor` diagnostic subcommand. Aggregates
// every install/runtime check into a single command so users can
// triage in one shot when something breaks. Output is human-readable
// (CLI mode carve-out — stdout is fair game). Exits 0 when no errors
// (warnings are fine), exit 1 when any check produced an error.
//
// Checks (in order):
//
//   - server         knowledge-server reachable + responding to Status?
//   - claude-cli     ~/.knowledge/config valid + cli_bin (if applicable)
//                    points at an executable file?
//   - voyage         VOYAGE_API_KEY set? (informational only — BM25-
//                    only mode is fully supported)
//   - fulminate      logged in to Fulminate Cloud? (informational —
//                    paid features unavailable when not logged in)
//   - claude-assets  ~/.claude/agents and ~/.claude/skills match the
//                    embedded version? (warns when stale or missing)
//   - config         ~/.knowledge/config parses + validates?
//
// Each check returns a (status, message) pair the formatter renders
// with a unicode glyph. No color codes — terminal-agnostic.

package bootstrap

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/auth"
	"github.com/fulminate-io/knowledge-mcp/internal/claudeassets"
	"github.com/fulminate-io/knowledge-mcp/internal/config"
	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
)

// checkStatus enumerates the three outcome classes a check can return.
// ok = green light. warn = something operator should know about but
// not blocking (e.g., "VOYAGE_API_KEY unset, BM25-only mode"). err =
// real problem that breaks functionality (e.g., "cli_bin path doesn't
// exist"). info = neutral/informational with no judgment (e.g., "not
// logged in to Fulminate" — fine if you don't use paid features).
type checkStatus int

const (
	statusOK checkStatus = iota
	statusInfo
	statusWarn
	statusErr
)

// checkResult is what each diagnostic check returns. Detail is an
// optional second-line follow-up (action hints like "run `knowledge
// install-claude-assets`"). Empty Detail means single-line output.
type checkResult struct {
	name   string      // "server", "claude-cli", etc.
	status checkStatus // ok / info / warn / err
	msg    string      // one-liner
	detail string      // optional extra line(s) under the main msg
}

// runDoctor is the CLI entry point. Walks every check, prints results,
// returns nil with exit-1 trapped via os.Exit when any check produced
// an error. Warnings + info don't affect exit code.
func runDoctor(args []string) error {
	fs := flag.NewFlagSet("knowledge doctor", flag.ContinueOnError)
	port := fs.Int("port", graphclient.DefaultPort, "TCP port the graph server should be listening on")
	configFile := fs.String("config-file", "", "Path to the TOML config file (default ~/.knowledge/config)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	checks := []checkResult{
		checkServer(*port),
		checkConfig(*configFile),
		checkClaudeCLI(*configFile),
		checkVoyage(),
		checkFulminateAuth(),
		checkClaudeAssets(),
	}

	fmt.Fprintln(os.Stdout, "knowledge doctor")
	fmt.Fprintln(os.Stdout, "================")
	fmt.Fprintln(os.Stdout)
	var errCount, warnCount int
	for _, c := range checks {
		fmt.Fprintf(os.Stdout, "  %s %-14s %s\n", glyphFor(c.status), c.name, c.msg)
		if c.detail != "" {
			fmt.Fprintf(os.Stdout, "                   %s\n", c.detail)
		}
		switch c.status {
		case statusErr:
			errCount++
		case statusWarn:
			warnCount++
		}
	}
	fmt.Fprintln(os.Stdout)
	fmt.Fprintf(os.Stdout, "Summary: %d errors, %d warnings\n", errCount, warnCount)
	if errCount > 0 {
		os.Exit(1)
	}
	return nil
}

// glyphFor returns the prefix glyph for a check status. Unicode-only;
// no ANSI color codes (so output works under launchd logs, file
// redirects, CI runners, etc.).
func glyphFor(s checkStatus) string {
	switch s {
	case statusOK:
		return "✓"
	case statusInfo:
		return "ⓘ"
	case statusWarn:
		return "⚠"
	case statusErr:
		return "✗"
	default:
		return "?"
	}
}

// checkServer dials the TCP loopback port + asks for Status. ok when
// the server is up and responding; info when it's not (might just
// not be started yet — `knowledge start` to bring it up).
func checkServer(port int) checkResult {
	gc := graphclient.NewGraphClient(port)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if !gc.HealthyCtx(ctx) {
		return checkResult{
			name:   "server",
			status: statusInfo,
			msg:    fmt.Sprintf("not running on port %d", port),
			detail: "run `knowledge start` to spawn it, or `brew services start knowledge` for a launchd-managed instance",
		}
	}
	status, err := gc.Status()
	if err != nil {
		return checkResult{
			name: "server", status: statusWarn,
			msg: fmt.Sprintf("listening on port %d but Status RPC failed: %v", port, err),
		}
	}
	pid, _ := status["pid"].(float64)
	nodes, _ := status["nodes"].(float64)
	edges, _ := status["edges"].(float64)
	return checkResult{
		name:   "server",
		status: statusOK,
		msg:    fmt.Sprintf("running on port %d (PID %d, %d nodes, %d edges)", port, int64(pid), int64(nodes), int64(edges)),
	}
}

// checkConfig loads + validates the config file for the summarizer
// consumer. Validation failure is a hard error since it'd block
// server startup too.
func checkConfig(configFile string) checkResult {
	path := configFile
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return checkResult{name: "config", status: statusErr, msg: "cannot resolve home dir: " + err.Error()}
		}
		path = filepath.Join(home, ".knowledge", "config")
	}
	cfg, err := config.Load(path)
	if err != nil {
		return checkResult{
			name: "config", status: statusErr,
			msg: fmt.Sprintf("%s: %v", path, err),
		}
	}
	if err := cfg.Validate([]config.Consumer{config.ConsumerSummarizer}); err != nil {
		return checkResult{
			name: "config", status: statusErr,
			msg:    path,
			detail: err.Error(),
		}
	}
	sec, _ := cfg.Resolve(config.ConsumerSummarizer)
	return checkResult{
		name: "config", status: statusOK,
		msg: fmt.Sprintf("%s valid (%s/%s)", path, sec.Provider, sec.Model),
	}
}

// checkClaudeCLI surfaces the cli_bin field state when the summarizer
// uses a CLI provider. For API providers, returns info ("not using
// claude-cli"). The config check above already validates cli_bin
// existence; this check exposes the actual path so users can copy-
// paste it for their own troubleshooting.
func checkClaudeCLI(configFile string) checkResult {
	path := configFile
	if path == "" {
		home, _ := os.UserHomeDir()
		path = filepath.Join(home, ".knowledge", "config")
	}
	cfg, err := config.Load(path)
	if err != nil {
		return checkResult{name: "claude-cli", status: statusInfo, msg: "config not loadable; see config check above"}
	}
	sec, err := cfg.Resolve(config.ConsumerSummarizer)
	if err != nil {
		return checkResult{name: "claude-cli", status: statusInfo, msg: "summarizer section not resolvable"}
	}
	if !sec.Provider.IsCLI() {
		return checkResult{name: "claude-cli", status: statusInfo, msg: "summarizer uses " + string(sec.Provider) + " (no CLI binary needed)"}
	}
	if sec.CLIBin == "" {
		return checkResult{
			name: "claude-cli", status: statusErr,
			msg:    "cli_bin not set in config",
			detail: "add `cli_bin = \"/absolute/path/to/claude\"` to [default] or [summarizer]",
		}
	}
	info, err := os.Stat(sec.CLIBin)
	if err != nil {
		return checkResult{name: "claude-cli", status: statusErr, msg: sec.CLIBin + " — " + err.Error()}
	}
	if info.IsDir() {
		return checkResult{name: "claude-cli", status: statusErr, msg: sec.CLIBin + " is a directory, not an executable"}
	}
	if info.Mode()&0o111 == 0 {
		return checkResult{name: "claude-cli", status: statusErr, msg: sec.CLIBin + " is not executable"}
	}
	return checkResult{name: "claude-cli", status: statusOK, msg: sec.CLIBin + " (executable)"}
}

// checkVoyage reports VOYAGE_API_KEY presence. Empty key is the
// documented BM25-only mode — info-level, not a warning.
func checkVoyage() checkResult {
	if os.Getenv("VOYAGE_API_KEY") == "" {
		return checkResult{
			name: "voyage", status: statusInfo,
			msg: "VOYAGE_API_KEY unset (BM25-only search mode)",
		}
	}
	return checkResult{
		name: "voyage", status: statusOK,
		msg: "VOYAGE_API_KEY set, vector search enabled",
	}
}

// checkFulminateAuth checks for a stored OAuth refresh token in the
// platform keychain. Not-logged-in is info, not warning — paid
// features being unavailable is a fully-supported state.
func checkFulminateAuth() checkResult {
	store, err := auth.NewStore()
	if err != nil {
		return checkResult{
			name: "fulminate", status: statusInfo,
			msg: "keychain unavailable: " + err.Error(),
		}
	}
	_, err = store.Get(context.Background(), auth.KeyRefreshToken)
	if errors.Is(err, auth.ErrNotFound) {
		return checkResult{
			name: "fulminate", status: statusInfo,
			msg:    "not logged in (paid features unavailable)",
			detail: "run `knowledge login` to authenticate",
		}
	}
	if err != nil {
		return checkResult{name: "fulminate", status: statusWarn, msg: "keychain read failed: " + err.Error()}
	}
	return checkResult{name: "fulminate", status: statusOK, msg: "logged in (refresh token present in keychain)"}
}

// checkClaudeAssets walks the embedded .claude/{agents,skills} tree
// and compares each file against the on-disk copy under ~/.claude/.
// Reports the count of missing or out-of-date files so the user
// knows whether to run `knowledge install-claude-assets`.
func checkClaudeAssets() checkResult {
	home, err := os.UserHomeDir()
	if err != nil {
		return checkResult{name: "claude-assets", status: statusWarn, msg: "cannot resolve home dir: " + err.Error()}
	}
	dest := filepath.Join(home, ".claude")

	var missing, drift []string
	err = fs.WalkDir(claudeassets.Files, ".", func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() {
			return walkErr
		}
		embedded, err := claudeassets.Files.ReadFile(p)
		if err != nil {
			return err
		}
		onDisk, err := os.ReadFile(filepath.Join(dest, p))
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				missing = append(missing, p)
				return nil
			}
			return err
		}
		if hashEqual(embedded, onDisk) {
			return nil
		}
		drift = append(drift, p)
		return nil
	})
	if err != nil {
		return checkResult{name: "claude-assets", status: statusErr, msg: "walk failed: " + err.Error()}
	}
	if len(missing) == 0 && len(drift) == 0 {
		return checkResult{name: "claude-assets", status: statusOK, msg: "in sync with embedded version"}
	}
	sort.Strings(missing)
	sort.Strings(drift)
	return checkResult{
		name:   "claude-assets",
		status: statusWarn,
		msg:    fmt.Sprintf("%d missing, %d out of date", len(missing), len(drift)),
		detail: "run `knowledge install-claude-assets` to update",
	}
}

// hashEqual returns true when the two byte slices have the same
// SHA-256 digest. Used by checkClaudeAssets to determine drift
// without doing a byte-by-byte compare on every file (the digest
// short-circuits unequal lengths quickly enough for this scale).
func hashEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	ha := sha256.Sum256(a)
	hb := sha256.Sum256(b)
	return hex.EncodeToString(ha[:]) == hex.EncodeToString(hb[:])
}
