// SPDX-License-Identifier: Apache-2.0

// Package bootstrap holds the cmd/knowledge client bootstrap layer.
// cmd/knowledge/main.go is a thin entry point that dispatches subcommands
// (serve/login/logout/start/stop/status/install-claude-assets/doctor/worker)
// then parses flags. MCP is served by the `knowledge serve` daemon (runServe
// → buildClient) over a loopback streamable-HTTP endpoint; every long-running
// wire (dream Runner, PropagationLoop, LLM pipeline, auto-prune) and every
// helper (server spawn, schema cache, intercept chain) lives in this package.
package bootstrap

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
	"github.com/fulminate-io/knowledge-mcp/internal/profiling"
)

// defaultWebOrigins is the allow-list applied to --web-origin when the flag is
// absent: the canonical Fulminate agent hosts (prod app at the root domain +
// the dev host). Restricted on purpose — never widened to '*' (see
// Config.AllowedWebOrigins / corsMiddleware).
var defaultWebOrigins = []string{"https://fulminate.io", "https://dev.fulminate.io"}

// csvOrigins is a flag.Value backing the --web-origin flag. There is no
// []string flag in the std flag package, so it accepts a comma-separated list
// and splits it into the bound []string. Registering it via fs.Var keeps the
// flag's definition (and its CSV split) centralized in registerConfigFlags so
// it lands identically on every FlagSet that shares that registration
// (ParseFlags + runServe), and the doc generator renders the default verbatim.
// An explicit --web-origin REPLACES the default list (it does not append).
type csvOrigins struct {
	target *[]string
	set    bool
}

// String renders the current value for the flag package's default/usage
// printing. A nil/empty target prints the empty string.
func (c *csvOrigins) String() string {
	if c == nil || c.target == nil {
		return ""
	}
	return strings.Join(*c.target, ",")
}

// Set parses a comma-separated --web-origin value, trimming surrounding
// whitespace per entry and dropping empties. The first Set REPLACES the
// default list; a repeated flag appends to the in-progress override.
func (c *csvOrigins) Set(v string) error {
	parsed := make([]string, 0, 4)
	for part := range strings.SplitSeq(v, ",") {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			parsed = append(parsed, trimmed)
		}
	}
	if !c.set {
		*c.target = parsed
		c.set = true
		return nil
	}
	*c.target = append(*c.target, parsed...)
	return nil
}

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

// Config holds every flag-driven knob the cmd/knowledge client needs. Pure
// data — the serve daemon (runServe → buildClient) consumes the fields;
// callers either let bootstrap.ParseFlags populate the struct from argv or
// build it by hand (tests).
type Config struct {
	GraphStorage string
	RootDir      string
	// RootDirSet reports whether --root was explicitly passed (vs the built-in
	// "." default). Set by applyRootDirSet AFTER fs.Parse via fs.Visit — a
	// value-compare against "." would misclassify an explicit --root=. as
	// defaulted. Consumed by the ast walk-root guard: an omitted repo with no
	// session cwd AND a defaulted root fails loud instead of silently walking
	// the process cwd.
	RootDirSet bool
	Port       int
	// AuthToken is an opaque machine bearer token presented on every request,
	// bypassing the interactive browser-based login flow and the platform
	// keychain. Populated from the --auth-token flag (defaulting to the
	// KNOWLEDGE_AUTH_TOKEN environment variable) for headless/automated
	// callers. Empty leaves the interactive login path fully intact.
	AuthToken string
	// NoAuth forces the client local-only at the Router.pick chokepoint. When
	// set, constructClient suppresses BOTH cloud-selection triggers: machineAuth
	// is forced false (the --auth-token / KNOWLEDGE_AUTH_TOKEN value is NOT
	// consulted) and the keychain is replaced with a no-op store so AuthState
	// reports IsLoggedIn==false even when a live `knowledge login` refresh token
	// exists. The result is fail-closed: no routed op can reach a fulminate.io
	// host regardless of credentials present. Capability reduction only — the
	// cloud endpoint is never overridden. Populated from the --no-auth flag.
	NoAuth bool
	// AllowedWebOrigins is the allow-list of browser web origins permitted to
	// make cross-origin (CORS) requests to the daemon's loopback streamable-HTTP
	// MCP endpoint. The corsMiddleware reflects a request's Origin only when it
	// appears in this list — it is NEVER widened to '*'. Populated from the
	// --web-origin flag, defaulting to the canonical Fulminate agent hosts so a
	// browser page served from those https origins can fetch the loopback daemon.
	AllowedWebOrigins    []string
	LogLevel             string
	LogFile              string
	NoWorkerRuntime      bool
	NoPropagationRuntime bool
	Pprof                bool
	PprofPort            int
	SkipLLMPrecheck      bool

	// Headless is the umbrella flag for an embedded/supervisor-managed daemon.
	// Populated from --headless. applyHeadless (headless.go) expands it into the
	// implied gate set (the four --no-* bools above plus the three internal
	// NoHive*/NoTranscriptUpload bools below); nothing reads Headless directly
	// past applyHeadless.
	Headless bool
	// NoHiveMonitor, NoHiveReaper, and NoTranscriptUpload are the coordination-loop
	// gates. applyHeadless sets all three when Headless is true, and each also has
	// its own --no-* flag for a daemon that needs the LLM pipeline (which
	// --headless disables) but must run no coordination loops.
	// The hive lifecycle controller (hive_loops.go) captures NoHiveMonitor/
	// NoHiveReaper at wiring time and suppresses that loop for the daemon's whole
	// life, whatever its hive sessions do; maybeStartTranscriptUpload consults
	// NoTranscriptUpload to skip the background transcript-upload loops.
	NoHiveMonitor      bool
	NoHiveReaper       bool
	NoTranscriptUpload bool

	// LLM pipeline (lives client-side) worker-pool tuning.
	NoLLMPipeline      bool
	SummaryChannelSize int
	SummaryBatchSize   int
	SummaryWorkers     int
	EmbedChannelSize   int
	EmbedBatchSize     int
	EmbedWorkers       int
	EmbedRPM           int
	PipelineTick       time.Duration

	// SegmentResidencyBudgetBytes caps the RESIDENT HEAP BYTES the client's
	// per-graph segment pools may occupy together before the coldest of them are
	// evicted from memory and left to reload from the local L2 disk cache on their
	// next search. 0 disables eviction entirely. Populated from
	// --segment-residency-budget-bytes / KNOWLEDGE_SEGMENT_RESIDENCY_BUDGET_BYTES;
	// consumed by ensureSegmentManager (client_segment.go).
	SegmentResidencyBudgetBytes int64

	// ReflectBackstopInterval is the cadence of the full-corpus reflection
	// backstop pass that resets DF-Leiden incremental drift. The hourly
	// PropagationLoop runs incrementally; once this interval elapses since the last
	// completed full pass, the next tick forces a full Leiden + DeGroot recompute.
	ReflectBackstopInterval time.Duration

	// LocalDialer constructs the local *graphclient.GraphClient. Defaults to
	// graphclient.NewGraphClient when nil. Test seam
	// — tests inject a closure that points the local client at an
	// httptest.Server URL via graphclient.NewGraphClientForURL. Production
	// callers leave this nil and constructClient dials 127.0.0.1:Port.
	LocalDialer func(port int) *graphclient.GraphClient
}

// registerConfigFlags registers every Config-backed flag on fs, binding
// each into cfg. Shared by ParseFlags (the bare-`knowledge` flag parser) and
// runServe (the `serve` daemon entry) so both accept an identical client-flag
// surface from one definition — adding a knob in one place covers both.
func registerConfigFlags(fs *flag.FlagSet, cfg *Config) {
	fs.StringVar(&cfg.GraphStorage, "graph-storage", "~/.knowledge/", "Directory for graph storage: the server writes its .bin here, and the client roots its segment cache + worker runtime under it (default ~/.knowledge/)")
	fs.StringVar(&cfg.RootDir, "root", ".", "Project root the client walks for ast + topology, and the current-tree fallback for resolving a bare repo name (default \".\")")
	fs.IntVar(&cfg.Port, "port", graphclient.DefaultPort, "TCP port the graph server listens on")
	fs.StringVar(&cfg.AuthToken, "auth-token", os.Getenv("KNOWLEDGE_AUTH_TOKEN"), "Opaque machine bearer token presented on every request, bypassing the interactive browser login and the platform keychain. Defaults to the KNOWLEDGE_AUTH_TOKEN environment variable; an explicit flag value wins. Empty leaves the interactive login path intact.")
	fs.StringVar(&cfg.LogLevel, "log-level", "info", "Log level: debug, info, warn, error")
	fs.StringVar(&cfg.LogFile, "log-file", "", "Log file path (logs to both stderr and file when set)")
	// Pre-apply the default allow-list so the no-flag path (and the
	// never-parsed doc sinks) carry the canonical origins; an explicit
	// --web-origin replaces this list via csvOrigins.Set. Copy the package
	// default so callers cannot mutate the shared slice through cfg.
	cfg.AllowedWebOrigins = append([]string(nil), defaultWebOrigins...)
	fs.Var(&csvOrigins{target: &cfg.AllowedWebOrigins}, "web-origin", "Comma-separated allow-list of browser web origins permitted to make cross-origin (CORS) requests to the daemon's loopback streamable-HTTP MCP endpoint. The Origin is reflected back only when it matches an entry; the list is never widened to '*'. An explicit value replaces the default (https://fulminate.io,https://dev.fulminate.io). Repeatable.")
	fs.BoolVar(&cfg.NoAuth, "no-auth", false, "Force the client local-only: suppress BOTH cloud-selection triggers at the Router.pick chokepoint (machineAuth forced false WITHOUT consulting --auth-token/KNOWLEDGE_AUTH_TOKEN, and the keychain replaced with a no-op store so a live `knowledge login` refresh token reports IsLoggedIn==false). Fail-closed: no routed op can reach a fulminate.io host regardless of credentials present. Capability reduction only — the cloud endpoint is never overridden. Use for offline/OSS mode and as the safety floor for the bug-hunt harness.")
	fs.BoolVar(&cfg.NoWorkerRuntime, "no-worker-runtime", false, "Skip dream Runner wiring. Run knowledge purely to serve/exercise the graph (e.g. the bench harness) without starting its own background worker runtime.")
	fs.BoolVar(&cfg.NoPropagationRuntime, "no-propagation-runtime", false, "Skip client-side PropagationLoop wiring. The MCP daemon continues to serve and reflective tools still run on demand, but the hourly background cluster detection + valence propagation stops. Use for offline development or to silence background log noise.")
	fs.BoolVar(&cfg.Pprof, "pprof", false, fmt.Sprintf("Start the pprof profiling HTTP endpoint on %s (/debug/pprof/) at boot, AND pass --pprof to the knowledge-server this daemon spawns so its own /debug/pprof/ mounts too. Both endpoints bind loopback only. Also reachable on demand for this process via manage(pprof_start). Use to profile client-side work such as collect.", profiling.Addr()))
	fs.IntVar(&cfg.PprofPort, "pprof-port", profiling.DefaultPort, "TCP port for this process's pprof profiling HTTP endpoint (loopback only). Applied when --pprof is set; the spawned knowledge-server serves its own /debug/pprof/ on --port instead.")
	fs.BoolVar(&cfg.SkipLLMPrecheck, "skip-llm-precheck", false, "Skip the live-ping check that runs against every configured (provider, model) tuple at client startup. Use for offline development or CI sandboxes; default is to fail-fast at boot rather than at first tool call.")
	fs.BoolVar(&cfg.NoLLMPipeline, "no-llm-pipeline", false, "Skip client-side LLM pipeline (summarize + embed) wiring. The MCP daemon and other tools continue to work; only background summarization/embedding stops.")
	fs.BoolVar(&cfg.Headless, "headless", false, "Run as an embedded/supervisor-managed daemon: serve the loopback /mcp endpoint and resolve query embeddings, but skip every background content + coordination loop. Implies --no-worker-runtime, --no-propagation-runtime, --skip-llm-precheck and --no-llm-pipeline, and additionally disables the hive monitor, hive reaper, and transcript upload loops. Still loads ~/.knowledge/config (so [credentials] resolve config-first) and still seeds .claude agents/skills. Does not change auth.")
	fs.BoolVar(&cfg.NoHiveMonitor, "no-hive-monitor", false, "Skip the background hive monitor loop. Individually addressable form of one of the three gates --headless implies — for daemons that need the LLM pipeline (which --headless disables) but must not run coordination loops (e.g. the bench harness's corpus-pull daemon).")
	fs.BoolVar(&cfg.NoHiveReaper, "no-hive-reaper", false, "Skip the background hive reaper loop. See --no-hive-monitor for when to use the individual gates instead of --headless.")
	fs.BoolVar(&cfg.NoTranscriptUpload, "no-transcript-upload", false, "Skip the background transcript-upload loops, including their HOME-side transcript cache writes. See --no-hive-monitor for when to use the individual gates instead of --headless.")
	fs.IntVar(&cfg.SummaryChannelSize, "summary-channel-size", 10000, "Client-side LLM pipeline: SummaryWork channel buffer size (full = collector blocks)")
	fs.IntVar(&cfg.SummaryBatchSize, "summary-batch-size", 20, "Client-side LLM pipeline: items per summary worker batch")
	fs.IntVar(&cfg.SummaryWorkers, "summary-workers", 25, "Client-side LLM pipeline: count of summary worker goroutines")
	fs.IntVar(&cfg.EmbedChannelSize, "embed-channel-size", 10000, "Client-side LLM pipeline: EmbedWork channel buffer size (full = collector blocks)")
	fs.IntVar(&cfg.EmbedBatchSize, "embed-batch-size", 100, "Client-side LLM pipeline: items per embed worker batch (under voyageEmbedder's 128 internal cap)")
	fs.IntVar(&cfg.EmbedWorkers, "embed-workers", 20, "Client-side LLM pipeline: count of embed worker goroutines")
	fs.IntVar(&cfg.EmbedRPM, "embed-rpm", 0, "Client-side LLM pipeline: max embed (Voyage) API requests per MINUTE across all embed workers; 0 = unlimited (default, preserves current 20-worker behavior). Proactive throttle for low-tier Voyage accounts — paces the opening burst so it respects the account RPM before the first 429. Companion to the reactive Retry-After backoff.")
	fs.DurationVar(&cfg.PipelineTick, "pipeline-tick", 250*time.Millisecond, "Client-side LLM pipeline: per-graph collector poll interval")
	fs.DurationVar(&cfg.ReflectBackstopInterval, "reflect-backstop-interval", 24*time.Hour, "Client-side reflection: cadence of the full-corpus reflection backstop pass that resets DF-Leiden incremental drift. The hourly loop runs incrementally; once this interval elapses since the last full pass, the next tick forces a full Leiden recompute. Default 24h (nightly).")
	fs.Int64Var(&cfg.SegmentResidencyBudgetBytes, "segment-residency-budget-bytes", segmentResidencyBudgetDefault(), "Client-side segment residency ceiling, in RESIDENT HEAP BYTES summed across every per-graph segment pool: once the total crosses it, the coldest pools are unloaded from memory and reload from the local L2 disk cache on their next search. 0 disables eviction entirely. This counts modeled Go-heap bytes — the per-segment membership index, the liveness bitset, and whatever each payload declares it holds. A mapped segment's blob is page cache and is NOT counted, so the budget is not a bound on a pool's on-disk size. Defaults to the KNOWLEDGE_SEGMENT_RESIDENCY_BUDGET_BYTES environment variable and otherwise to 1073741824; an explicit flag value wins.")
}

// defaultSegmentResidencyBudgetBytes is 1 GiB of RESIDENT HEAP BYTES. Written as a
// decimal literal rather than 1<<30 so a plain text search for the number finds it.
const defaultSegmentResidencyBudgetBytes int64 = 1073741824

// segmentResidencyBudgetDefault resolves --segment-residency-budget-bytes's
// default: KNOWLEDGE_SEGMENT_RESIDENCY_BUDGET_BYTES when set, otherwise
// defaultSegmentResidencyBudgetBytes. Mirrors how --auth-token defaults from
// KNOWLEDGE_AUTH_TOKEN above; an explicit flag value wins over both.
//
// AN UNPARSEABLE ENV VALUE IS FATAL rather than quietly falling back to the
// literal. A budget the operator meant to set and mistyped would otherwise become
// "the default" with nothing saying the knob was ignored, and the symptom —
// residency growing to a ceiling nobody chose — looks nothing like a typo. This is
// the same treatment the flag package gives an unparseable flag value, applied to
// the environment form of the same knob, and it happens at process start before
// anything is serving.
func segmentResidencyBudgetDefault() int64 {
	raw := os.Getenv("KNOWLEDGE_SEGMENT_RESIDENCY_BUDGET_BYTES")
	if raw == "" {
		return defaultSegmentResidencyBudgetBytes
	}
	parsed, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		fmt.Fprintf(os.Stderr,
			"knowledge: KNOWLEDGE_SEGMENT_RESIDENCY_BUDGET_BYTES=%q is not a base-10 int64 "+
				"(expected a byte count, e.g. 1073741824, or 0 to disable eviction): %v\n", raw, err)
		os.Exit(2)
	}
	return parsed
}

// applyPprof starts this process's loopback pprof endpoint when --pprof is set,
// on the port --pprof-port names. It is the SOLE consumer of cfg.Pprof /
// cfg.PprofPort on the client side, and it is called from runServe — the only
// live path that serves anything, since bare `knowledge` now returns the
// "run the daemon" error rather than serving MCP over stdio (run.go Run).
//
// IT EXISTS BECAUSE THE FLAGS DID NOT WORK. --pprof and --pprof-port were
// registered and never read: there was no profiling.EnsureServer or
// profiling.SetPort call anywhere in the repository, so a daemon started with
// --pprof bound nothing and reported nothing, and --pprof-port could not move a
// port that was never opened. The endpoint reached a listening state only
// through manage(pprof_start), which starts a CPU capture as a side effect. The
// flag's default moved true -> false in the same change that made it real: a
// knob that never functioned must not begin opening a port on every daemon the
// day it starts functioning, and the endpoint's handlers expose process
// internals.
//
// SetPort BEFORE EnsureServer is required, not stylistic — SetPort's own
// contract (profiling.go) is that it must precede the first EnsureServer/StartCPU,
// because the address is read once when the listener binds.
func applyPprof(cfg *Config) {
	if !cfg.Pprof {
		return
	}
	profiling.SetPort(cfg.PprofPort)
	profiling.EnsureServer()
}

// applyRootDirSet records whether --root was explicitly passed on fs. It is the
// SOLE assignment site of cfg.RootDirSet, called by BOTH parse paths (ParseFlags
// and runServe) so the was-set logic cannot diverge between them. MUST be called
// AFTER fs.Parse: flag.Visit only reports flags actually set on THIS FlagSet, so
// a value-compare against "." (which the ticket rejects) is never needed — an
// explicit --root=. still counts as set.
func applyRootDirSet(fs *flag.FlagSet, cfg *Config) {
	fs.Visit(func(fl *flag.Flag) {
		if fl.Name == "root" {
			cfg.RootDirSet = true
		}
	})
}

// ParseFlags parses args into a Config. Caller passes os.Args[1:] from
// main(). Returns flag.ErrHelp (verbatim) when --help was requested so
// the caller can detect and exit 0; the FlagSet's own usage text already
// printed to stderr by then.
func ParseFlags(args []string) (Config, error) {
	fs := flag.NewFlagSet(programName(), flag.ContinueOnError)
	var cfg Config
	registerConfigFlags(fs, &cfg)
	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}
	applyRootDirSet(fs, &cfg)
	return cfg, nil
}
