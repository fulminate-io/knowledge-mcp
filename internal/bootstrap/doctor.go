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
	"context"
	"flag"
	"fmt"
	"os"
	"time"

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

// doctorFlags holds the parsed flags for `knowledge doctor`.
type doctorFlags struct {
	port       int
	configFile string
	deep       bool
}

// registerDoctorFlags registers the `knowledge doctor` flags on fs, binding
// each into f. Pure register-only seam (no fs.Parse) — shared by runDoctor (the
// live CLI path) and the docs generator, which VisitAll's the FlagSet to render
// the flag table. Mirrors registerConfigFlags / registerLifecycleFlags /
// registerInstallFlags.
func registerDoctorFlags(fs *flag.FlagSet, f *doctorFlags) {
	fs.IntVar(&f.port, "port", graphclient.DefaultPort, "TCP port the graph server should be listening on")
	fs.StringVar(&f.configFile, "config-file", "", "Path to the TOML config file (default ~/.knowledge/config)")
	fs.BoolVar(&f.deep, "deep", false, "Exercise each configured provider's reachability/login (slower, makes network calls)")
}

// runDoctor is the CLI entry point. Walks every check, prints results,
// returns nil with exit-1 trapped via os.Exit when any check produced
// an error. Warnings + info don't affect exit code.
func runDoctor(args []string) error {
	fs := flag.NewFlagSet("knowledge doctor", flag.ContinueOnError)
	var f doctorFlags
	registerDoctorFlags(fs, &f)
	if err := fs.Parse(args); err != nil {
		return err
	}

	checks := defaultChecks(f.port, f.configFile)
	if f.deep {
		checks = append(checks, checkProvidersDeep(f.configFile))
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

// defaultChecks builds the NON-DEEP diagnostic check slice shared by the
// `knowledge doctor` CLI (runDoctor) and the MCP manage(status) doctor block
// (the bootstrap client's DoctorChecks). Ordering + content match the historical
// runDoctor sequence exactly so the CLI output is unchanged. The opt-in --deep
// reachability check (checkProvidersDeep) is DELIBERATELY excluded: it makes
// ~30s of provider network calls and must never run on a status poll — the CLI
// caller appends it separately behind its --deep flag.
//
// SCOPE: these are the CORE in-daemon checks. checkCodeStaleness is
// single-repo/cwd-based; run inside the daemon it typically reports "code index:
// last collected unknown" for the daemon's launch dir — it is included AS-IS as
// one general check (it never errors). Per-graph index freshness (multi-repo
// commits-behind) is deferred to a fast-follow ticket; no such machinery here.
//
// LIVENESS IS PROBED EXACTLY ONCE per run (probe once, no retries): one
// GraphClient, one bounded no-retry HealthService.Check, and the result is
// threaded into every check that needs the server. Before this, checkServer
// and checkCodeStaleness each dialed their own client, and on an install with
// a remote backend (no local server by design) each probe slept through the
// reconnect interceptor's retry ladder — ~13s of a ~14.5s manage(status).
func defaultChecks(port int, configFile string) []checkResult {
	gc := graphclient.NewGraphClient(port)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	healthy := gc.HealthyCtx(ctx)
	cancel()

	checks := []checkResult{
		checkServer(gc, port, healthy),
		checkCodeStaleness(gc, healthy),
		checkConfig(configFile),
	}
	checks = append(checks, checkConsumerCLIs(configFile)...)
	checks = append(checks,
		checkVoyage(configFile),
		checkFulminateAuth(),
		checkClaudeAssets(),
		checkClaudeMD(),
		checkClaudeSettings(),
	)
	return checks
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
