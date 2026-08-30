// SPDX-License-Identifier: Apache-2.0

// doc_flags.go — the docs generator's read-only window onto the client CLI
// flag surface. DocFlagSets composes the SAME register seams the live CLI paths
// use (registerConfigFlags, registerLifecycleFlags, registerInstallFlags,
// registerDoctorFlags, registerInstallClaudeFlags, registerInstallCodexFlags,
// registerTriggerFlags, registerStatusFlags, plus the serve daemon's local
// --http-port), so every flag's name / default / usage the generator renders is
// the real one — const-backed defaults (graphclient.DefaultPort,
// DefaultMCPHTTPPort, ...) resolve to their literal values via flag.Flag.DefValue
// with nothing hardcoded.
//
// Returning the constructed *flag.FlagSet (never parsed) lets the client-side
// docgen package VisitAll each set. This is the only seam docgen needs from
// bootstrap; the rendering + splicing logic stays in docgen.

package bootstrap

import (
	"flag"

	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
)

// DocFlagSet pairs a stable block name (the docs/guides binaries.md generated
// marker id) with the FlagSet whose flags the generator renders into it.
type DocFlagSet struct {
	// BlockName is the named-marker id in binaries.md (e.g. "flags-serve").
	BlockName string
	// FlagSet is a freshly-built, UNPARSED FlagSet carrying the subcommand's
	// flags via the real register seams. The generator only VisitAll's it.
	FlagSet *flag.FlagSet
}

// DocFlagSets builds every client-subcommand FlagSet the docs generator renders,
// each paired with its binaries.md block name. Ordered for stable, repeatable
// output. The sets are constructed and registered but never parsed.
func DocFlagSets() []DocFlagSet {
	// serve: the same client config surface plus the daemon-local --http-port,
	// mirroring runServe (daemon.go). The bound *flag.FlagSet captures every
	// flag's name/default/usage; the receiving structs are throwaway sinks for
	// the register seams' var bindings (never parsed, so never read).
	serveFS := flag.NewFlagSet("knowledge serve", flag.ContinueOnError)
	var serveCfg Config
	registerConfigFlags(serveFS, &serveCfg)
	var serveHTTPPort int
	serveFS.IntVar(&serveHTTPPort, "http-port", graphclient.DefaultMCPHTTPPort, "Loopback TCP port for the streamable-HTTP MCP endpoint (/mcp). Distinct from --port (the graph server).")

	// start/status share the no-timeout lifecycle flag shape; stop adds --timeout.
	startFS := flag.NewFlagSet("knowledge start", flag.ContinueOnError)
	var startFlags lifecycleFlags
	registerLifecycleFlags(startFS, &startFlags, "start")

	stopFS := flag.NewFlagSet("knowledge stop", flag.ContinueOnError)
	var stopFlags lifecycleFlags
	registerLifecycleFlags(stopFS, &stopFlags, "stop")

	statusFS := flag.NewFlagSet("knowledge status", flag.ContinueOnError)
	var statusFlags lifecycleFlags
	registerLifecycleFlags(statusFS, &statusFlags, "status")

	installFS := flag.NewFlagSet("knowledge install", flag.ContinueOnError)
	var instFlags installFlags
	registerInstallFlags(installFS, &instFlags)

	doctorFS := flag.NewFlagSet("knowledge doctor", flag.ContinueOnError)
	var docFlags doctorFlags
	registerDoctorFlags(doctorFS, &docFlags)

	installClaudeFS := flag.NewFlagSet("knowledge install-claude-assets", flag.ContinueOnError)
	var instClaudeFlags installAssetsFlags
	registerInstallClaudeFlags(installClaudeFS, &instClaudeFlags)

	installCodexFS := flag.NewFlagSet("knowledge install-codex-assets", flag.ContinueOnError)
	var instCodexFlags installCodexFlags
	registerInstallCodexFlags(installCodexFS, &instCodexFlags)

	// The bare-`knowledge` client config surface (no subcommand).
	clientFS := flag.NewFlagSet("knowledge", flag.ContinueOnError)
	var clientCfg Config
	registerConfigFlags(clientFS, &clientCfg)

	return []DocFlagSet{
		{BlockName: "flags-serve", FlagSet: serveFS},
		{BlockName: "flags-start", FlagSet: startFS},
		{BlockName: "flags-stop", FlagSet: stopFS},
		{BlockName: "flags-status", FlagSet: statusFS},
		{BlockName: "flags-install", FlagSet: installFS},
		{BlockName: "flags-doctor", FlagSet: doctorFS},
		{BlockName: "flags-install-claude-assets", FlagSet: installClaudeFS},
		{BlockName: "flags-install-codex-assets", FlagSet: installCodexFS},
		{BlockName: "flags-client", FlagSet: clientFS},
	}
}
