// SPDX-License-Identifier: Apache-2.0

// doctor.go — `knowledge doctor` diagnostic subcommand. Aggregates
// every install/runtime check into a single command so users can
// triage in one shot when something breaks. Output is human-readable
// (CLI mode carve-out — stdout is fair game). Exits 0 when no errors
// (warnings are fine), exit 1 when any check produced an error.
//
// The driver (runDoctor + glyphFor + the checkResult shape) lives
// here; the individual checks live in doctor_checks.go and the opt-in
// --deep reachability check in doctor_deep.go. Each check returns a
// (status, message) pair the formatter renders with a unicode glyph.
// No color codes — terminal-agnostic.

package bootstrap

import (
	"flag"
	"fmt"
	"os"

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
	deep := fs.Bool("deep", false, "Exercise each configured provider's reachability/login (slower, makes network calls)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	checks := []checkResult{
		checkServer(*port),
		checkCodeStaleness(*port),
		checkConfig(*configFile),
	}
	checks = append(checks, checkConsumerCLIs(*configFile)...)
	checks = append(checks,
		checkVoyage(*configFile),
		checkFulminateAuth(),
		checkClaudeAssets(),
	)
	if *deep {
		checks = append(checks, checkProvidersDeep(*configFile))
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
