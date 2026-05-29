// SPDX-License-Identifier: Apache-2.0

// Package bootstrap holds the MCP stdio client bootstrap layer extracted
// from cmd/knowledge. cmd/knowledge/main.go is a thin entry point that
// dispatches subcommands (login/logout/start/stop/status/install-claude-
// assets/doctor/worker) then parses flags and calls bootstrap.Run to
// enter the MCP stdio loop. Every long-running wire (dream Runner,
// PropagationLoop, LLM pipeline, auto-prune) and every helper (server
// spawn, schema cache, intercept chain) lives in this package.
package bootstrap

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
	"github.com/fulminate-io/knowledge-mcp/internal/profiling"
)

// programName returns os.Args[0] verbatim so the FlagSet's printed
// program name matches what Go's default flag.CommandLine emits
// (`Usage of <os.Args[0]>:`). Mirrors cmd/knowledge-server/bootstrap.
func programName() string {
	if len(os.Args) == 0 {
		return "knowledge"
	}
	return os.Args[0]
}

// Version is the binary version string, surfaced into the startup slog
// line. cmd/knowledge/main.go's init() publishes its ldflags-injected
// `version` into this var so Run reports the right SHA/tag.
var Version = "dev"

// Config holds every flag-driven knob the MCP stdio client needs. Pure
// data — Run consumes the fields directly; callers either let
// bootstrap.ParseFlags populate the struct from argv or build it by
// hand (tests).
type Config struct {
	GraphStorage         string
	RootDir              string
	Port                 int
	LogLevel             string
	LogFile              string
	NoWorkerRuntime      bool
	NoPropagationRuntime bool
	Pprof                bool
	PprofPort            int
	SkipLLMPrecheck      bool

	// LLM pipeline (post-BCN5: lives client-side) worker-pool tuning.
	NoLLMPipeline      bool
	SummaryChannelSize int
	SummaryBatchSize   int
	SummaryWorkers     int
	EmbedChannelSize   int
	EmbedBatchSize     int
	EmbedWorkers       int
	PipelineTick       time.Duration

	// LocalDialer constructs the local *graphclient.GraphClient. Defaults to
	// graphclient.NewGraphClient when nil. Test seam (FUL-323 Phase 3 step 2)
	// — tests inject a closure that points the local client at an
	// httptest.Server URL via graphclient.NewGraphClientForURL. Production
	// callers leave this nil and constructClient dials 127.0.0.1:Port.
	LocalDialer func(port int) *graphclient.GraphClient
}

// ParseFlags parses args into a Config. Caller passes os.Args[1:] from
// main(). Returns flag.ErrHelp (verbatim) when --help was requested so
// the caller can detect and exit 0; the FlagSet's own usage text already
// printed to stderr by then.
func ParseFlags(args []string) (Config, error) {
	fs := flag.NewFlagSet(programName(), flag.ContinueOnError)
	var cfg Config
	fs.StringVar(&cfg.GraphStorage, "graph-storage", "~/.knowledge/", "Directory for graph storage (display-only; server owns the bin file)")
	fs.StringVar(&cfg.RootDir, "root", ".", "Project root directory (display-only; server is the one that collects from root)")
	fs.IntVar(&cfg.Port, "port", graphclient.DefaultPort, "TCP port the graph server listens on")
	fs.StringVar(&cfg.LogLevel, "log-level", "info", "Log level: debug, info, warn, error")
	fs.StringVar(&cfg.LogFile, "log-file", "", "Log file path (logs to both stderr and file when set)")
	fs.BoolVar(&cfg.NoWorkerRuntime, "no-worker-runtime", false, "Skip dream Runner wiring. Set on child processes spawned by claude-cli's --mcp-config to break the worker-spawning recursion (parent runs Runner; child is a pure MCP stdio server).")
	fs.BoolVar(&cfg.NoPropagationRuntime, "no-propagation-runtime", false, "Skip client-side PropagationLoop wiring. The MCP stdio loop continues to work and reflective tools still run on demand, but the hourly background cluster detection + valence propagation stops. Use for offline development or to silence background log noise.")
	fs.BoolVar(&cfg.Pprof, "pprof", true, fmt.Sprintf("Start the pprof profiling HTTP endpoint on %s (/debug/pprof/) at boot. Also reachable on demand via manage(pprof_start). Use to profile client-side work such as collect. Default-on during the BCN11.x → general-stability investigation window; flip to false once the startup-timeout flake (ticket fb39323b...) is diagnosed.", profiling.Addr()))
	fs.IntVar(&cfg.PprofPort, "pprof-port", profiling.DefaultPort, "TCP port for the pprof profiling HTTP endpoint (loopback only)")
	fs.BoolVar(&cfg.SkipLLMPrecheck, "skip-llm-precheck", false, "Skip the live-ping check that runs against every configured (provider, model) tuple at client startup. Use for offline development or CI sandboxes; default is to fail-fast at boot rather than at first tool call.")
	fs.BoolVar(&cfg.NoLLMPipeline, "no-llm-pipeline", false, "Skip client-side LLM pipeline (summarize + embed) wiring. The MCP stdio loop and other tools continue to work; only background summarization/embedding stops.")
	fs.IntVar(&cfg.SummaryChannelSize, "summary-channel-size", 10000, "Client-side LLM pipeline: SummaryWork channel buffer size (full = collector blocks)")
	fs.IntVar(&cfg.SummaryBatchSize, "summary-batch-size", 20, "Client-side LLM pipeline: items per summary worker batch")
	fs.IntVar(&cfg.SummaryWorkers, "summary-workers", 25, "Client-side LLM pipeline: count of summary worker goroutines")
	fs.IntVar(&cfg.EmbedChannelSize, "embed-channel-size", 10000, "Client-side LLM pipeline: EmbedWork channel buffer size (full = collector blocks)")
	fs.IntVar(&cfg.EmbedBatchSize, "embed-batch-size", 100, "Client-side LLM pipeline: items per embed worker batch (under voyageEmbedder's 128 internal cap)")
	fs.IntVar(&cfg.EmbedWorkers, "embed-workers", 20, "Client-side LLM pipeline: count of embed worker goroutines")
	fs.DurationVar(&cfg.PipelineTick, "pipeline-tick", 250*time.Millisecond, "Client-side LLM pipeline: per-graph collector poll interval")
	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}
	return cfg, nil
}
