// SPDX-License-Identifier: Apache-2.0

// worker_subcommand.go — operator-facing CLI surface for the dream
// runtime. Mirrors the shape of runAuthSubcommand (login / logout /
// whoami) at main.go: runWorkerSubcommand inspects os.Args[1] and, when
// it equals "worker", parses os.Args[2] as the sub-op (trigger | status)
// then defers to a per-op flag set. Returns true when it handled the
// invocation (caller exits immediately) and false when os.Args[1] is
// not "worker" so the default MCP stdio path runs.
//
// Dispatch runs BEFORE parseFlags + constructClient: the subcommand
// path is short-lived (one operator command, prints results, exits)
// and has no business carrying the long-lived MCP wiring. It owns its
// own *graphclient.GraphClient (constructed via graphclient.NewGraphClient)
// and its own dream.Runner (constructed via buildRuntime — the same
// helper the MCP path uses, see dream.go). Construction order stays
// identical across the two callers so behavior is uniform.
//
// Sub-ops:
//
//   - trigger <name> [--payload '{...}'] [--no-wait] [--port N]
//     [--graph-storage PATH] [--timeout 30s]
//
//     Fires runtime.OnManualTrigger. By default, blocks up to --timeout
//     (default 30s) on a worker-completed event matching <name>; with
//     --no-wait, returns immediately after firing (fire-and-forget).
//     On the blocking path, prints the completion event and a log tail
//     (Status returns the last 5 InvocationRecords).
//
//   - status <name> [--limit N] [--port N] [--graph-storage PATH]
//
//     Reads recent invocation records via runtime.Status and prints
//     each as a single JSON line (newline-delimited JSON), newest
//     first. --limit defaults to 20.
//
// Test seam: parseTriggerArgs and parseStatusArgs are pure helpers that
// take []string and return the parsed option structs (or an error).
// Tests in worker_subcommand_test.go exercise flag parsing without
// constructing a real Runner. End-to-end runtime behavior is covered
// by the Phase H tests on InterceptWorker and the Phase J smoke test.

package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/dream"
	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
)

// workerUsage is printed when `knowledge worker` is invoked without a
// recognized sub-op. Matches the conversational tone of LoginCmd's
// usage strings.
const workerUsage = `usage: knowledge worker <subcommand> [flags]

subcommands:
  trigger <name>   Fire a worker manually. Blocks up to --timeout on
                   completion unless --no-wait is set.
  status  <name>   Print recent invocation records for <name>.

run "knowledge worker <subcommand> --help" for per-subcommand flags.
`

// RunWorkerSubcommand is the top-level dispatcher. Returns (false, 0)
// when os.Args[1] is not "worker" so main() falls through to the
// default MCP stdio path; returns (true, exitCode) when this function
// handled the call.
//
// Errors from sub-ops print to stderr and return (true, 1); successful
// sub-ops return (true, 0). Mirrors cmd/knowledge-server/bootstrap's
// RunAuthSubcommand contract — main() owns the os.Exit.
func RunWorkerSubcommand() (handled bool, exitCode int) {
	if len(os.Args) < 2 || os.Args[1] != "worker" {
		return false, 0
	}
	// Recursion guard: when claude-cli's --mcp-config spawns a child of
	// this binary, it inherits the parent's argv (which may include the
	// "worker trigger ..." subcommand args) and we appended
	// --no-worker-runtime. The child must NOT re-trigger the worker;
	// fall through to MCP stdio mode where ParseFlags sees the flag and
	// skips wireWorkerRuntime. See bootstrap.ParseFlags.
	if hasNoWorkerRuntimeFlag(os.Args) {
		return false, 0
	}
	if len(os.Args) < 3 {
		fmt.Fprint(os.Stderr, workerUsage)
		return true, 1
	}
	sub := os.Args[2]
	rest := os.Args[3:]
	var err error
	switch sub {
	case "trigger":
		err = runWorkerTrigger(rest, os.Stdout)
	case "status":
		err = runWorkerStatus(rest, os.Stdout)
	default:
		fmt.Fprintf(os.Stderr, "worker: unknown subcommand %q\n\n%s", sub, workerUsage)
		return true, 1
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "worker %s: %v\n", sub, err)
		return true, 1
	}
	return true, 0
}

// triggerOpts is the parsed shape of `worker trigger` flags.
type triggerOpts struct {
	name         string
	payload      string
	noWait       bool
	port         int
	graphStorage string
	timeout      time.Duration
}

// statusOpts is the parsed shape of `worker status` flags.
type statusOpts struct {
	name         string
	limit        int
	port         int
	graphStorage string
}

// parseTriggerArgs parses a `worker trigger` argv slice. Pure helper
// — does not touch global state, does not construct a runtime — so
// tests can exercise flag handling directly. Returns flag.ErrHelp
// verbatim when --help is requested so the caller distinguishes a
// help-print exit from a malformed-args exit.
func parseTriggerArgs(args []string) (triggerOpts, error) {
	fs := flag.NewFlagSet("worker trigger", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), `usage: knowledge worker trigger <name> [flags]

flags:
  --payload '{...}'    JSON payload passed to the worker (default: null)
  --no-wait            return immediately after firing (default: blocks
                       on worker-completed event up to --timeout)
  --timeout DURATION   max time to block on completion (default: 30s)
  --port N             TCP port the graph server listens on
  --graph-storage PATH directory for graph storage / per-worker logs
                       (default: ~/.knowledge/)
`)
	}

	var opts triggerOpts
	fs.StringVar(&opts.payload, "payload", "", "JSON payload passed to the worker")
	fs.BoolVar(&opts.noWait, "no-wait", false, "return immediately after firing")
	fs.DurationVar(&opts.timeout, "timeout", 30*time.Second, "max time to block on completion")
	fs.IntVar(&opts.port, "port", graphclient.DefaultPort, "TCP port the graph server listens on")
	fs.StringVar(&opts.graphStorage, "graph-storage", "~/.knowledge/", "directory for graph storage")

	if err := fs.Parse(args); err != nil {
		return triggerOpts{}, err
	}
	rest := fs.Args()
	if len(rest) < 1 {
		return triggerOpts{}, errors.New("name is required (positional)")
	}
	if len(rest) > 1 {
		return triggerOpts{}, fmt.Errorf("unexpected positional args: %v", rest[1:])
	}
	opts.name = strings.TrimSpace(rest[0])
	if opts.name == "" {
		return triggerOpts{}, errors.New("name must not be empty")
	}
	if opts.payload != "" {
		// Reject malformed JSON early so we don't fire a worker with
		// payload the runtime will fail to parse later.
		if !json.Valid([]byte(opts.payload)) {
			return triggerOpts{}, fmt.Errorf("--payload is not valid JSON: %q", opts.payload)
		}
	}
	return opts, nil
}

// parseStatusArgs parses a `worker status` argv slice. Pure helper —
// see parseTriggerArgs for rationale.
func parseStatusArgs(args []string) (statusOpts, error) {
	fs := flag.NewFlagSet("worker status", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), `usage: knowledge worker status <name> [flags]

flags:
  --limit N            number of recent records to print (default: 20)
  --port N             TCP port the graph server listens on
  --graph-storage PATH directory for graph storage / per-worker logs
                       (default: ~/.knowledge/)
`)
	}

	var opts statusOpts
	fs.IntVar(&opts.limit, "limit", 20, "number of recent records to print")
	fs.IntVar(&opts.port, "port", graphclient.DefaultPort, "TCP port the graph server listens on")
	fs.StringVar(&opts.graphStorage, "graph-storage", "~/.knowledge/", "directory for graph storage")

	if err := fs.Parse(args); err != nil {
		return statusOpts{}, err
	}
	rest := fs.Args()
	if len(rest) < 1 {
		return statusOpts{}, errors.New("name is required (positional)")
	}
	if len(rest) > 1 {
		return statusOpts{}, fmt.Errorf("unexpected positional args: %v", rest[1:])
	}
	opts.name = strings.TrimSpace(rest[0])
	if opts.name == "" {
		return statusOpts{}, errors.New("name must not be empty")
	}
	if opts.limit <= 0 {
		return statusOpts{}, fmt.Errorf("--limit must be > 0, got %d", opts.limit)
	}
	return opts, nil
}

// runWorkerTrigger executes the trigger sub-op. Constructs its own
// GraphClient + Runner (subcommand path runs before constructClient),
// dispatches OnManualTrigger, and either returns immediately
// (--no-wait) or blocks on a worker-completed event up to --timeout.
//
// On the blocking path, the bus subscription is installed BEFORE
// OnManualTrigger fires so a fast worker that completes before the
// caller starts reading still delivers its event into the buffered
// subscription channel.
func runWorkerTrigger(args []string, out io.Writer) error {
	opts, err := parseTriggerArgs(args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	graphStorage := expandTilde(opts.graphStorage)

	gc := graphclient.NewGraphClient(opts.port)
	runner, err := buildRuntime(gc, opts.port, graphStorage, nil)
	if err != nil {
		return fmt.Errorf("build runtime: %w", err)
	}
	// Drain on exit so the async invocation finishes its log writes
	// before the process exits. The blocking path already waits via
	// the event subscription; this is the safety net for --no-wait.
	defer runner.Stop(60 * time.Second)

	payload := json.RawMessage(`null`)
	if opts.payload != "" {
		payload = json.RawMessage(opts.payload)
	}

	ctx := context.Background()

	if opts.noWait {
		if err := runner.OnManualTrigger(ctx, opts.name, payload); err != nil {
			return err
		}
		fmt.Fprintf(out, "worker %q fired (running asynchronously; use 'knowledge worker status %s' to inspect)\n", opts.name, opts.name)
		return nil
	}

	// Subscribe BEFORE firing — see docstring.
	completed := runner.Bus.Subscribe(dream.Trigger{
		Event:  dream.EventWorkerCompleted,
		Filter: map[string]string{"worker": opts.name},
	})
	defer runner.Bus.Unsubscribe(completed)

	if err := runner.OnManualTrigger(ctx, opts.name, payload); err != nil {
		return err
	}

	select {
	case ev, ok := <-completed:
		if !ok {
			return errors.New("event subscription closed before worker completed")
		}
		printCompletionEvent(out, ev)
		// Tail the log so the operator sees what the worker did.
		records, statusErr := runner.Status(ctx, opts.name, 5)
		if statusErr != nil {
			fmt.Fprintf(out, "(log tail unavailable: %v)\n", statusErr)
			return nil
		}
		if err := printRecords(out, records); err != nil {
			return err
		}
		if ev.Status != "ok" {
			return fmt.Errorf("worker completed with status %q", ev.Status)
		}
		return nil
	case <-time.After(opts.timeout):
		return fmt.Errorf("timed out after %s waiting for worker %q to complete (use --no-wait or raise --timeout)", opts.timeout, opts.name)
	}
}

// runWorkerStatus executes the status sub-op. Reads recent invocation
// records and prints each as a single JSON line.
func runWorkerStatus(args []string, out io.Writer) error {
	opts, err := parseStatusArgs(args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	graphStorage := expandTilde(opts.graphStorage)

	gc := graphclient.NewGraphClient(opts.port)
	runner, err := buildRuntime(gc, opts.port, graphStorage, nil)
	if err != nil {
		return fmt.Errorf("build runtime: %w", err)
	}
	defer runner.Stop(0) // No in-flight invocations to drain on the status path.

	records, err := runner.Status(context.Background(), opts.name, opts.limit)
	if err != nil {
		return err
	}
	if len(records) == 0 {
		fmt.Fprintf(out, "(no invocation records for worker %q)\n", opts.name)
		return nil
	}
	return printRecords(out, records)
}

// printCompletionEvent writes a one-line summary of a worker-completed
// event to out. The full record body is in the log tail; this line is
// the high-signal "did it finish, how long did it take" header.
func printCompletionEvent(out io.Writer, ev dream.Event) {
	fmt.Fprintf(out,
		"worker %q completed: status=%s duration=%dms at=%s\n",
		ev.Worker, statusOrUnknown(ev.Status), ev.DurationMs, ev.At.Format(time.RFC3339),
	)
}

// printRecords writes one InvocationRecord per line as JSON. Newest
// first — ReadRecent already orders the slice that way.
func printRecords(out io.Writer, records []dream.InvocationRecord) error {
	enc := json.NewEncoder(out)
	for _, rec := range records {
		// json.Encoder appends a newline for us — that's the
		// newline-delimited JSON shape.
		if err := enc.Encode(rec); err != nil {
			return fmt.Errorf("encode invocation record: %w", err)
		}
	}
	return nil
}

// statusOrUnknown returns ev.Status verbatim or "unknown" when the
// runner emitted an event without setting Status (defensive — every
// worker-completed event in v1 carries one).
func statusOrUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}
