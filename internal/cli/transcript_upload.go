// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/auth"
	"github.com/fulminate-io/knowledge-mcp/internal/transcripts"
	"github.com/fulminate-io/knowledge-mcp/internal/transcriptsync"
)

// transcriptUploadUsage is printed by `knowledge transcript-upload --help`. Terse,
// factual. The cloud host is build-tag-pinned (no endpoint flag).
const transcriptUploadUsage = `knowledge transcript-upload — ship local CLI transcripts to the agent

Usage:
  knowledge transcript-upload [--seed] [--max-concurrency N] [--batch-size N] [--dry-run]

Ships the local ~/.claude and ~/.codex transcript corpus to the agent — one parquet
object per changed session — over the presigned-GCS sync path, per-session
size/mtime-incremental, gated on the per-account transcript-collection consent flag.
Standalone and daemon-independent.

Flags:
  --seed              Force a full re-ship of every session (ignore the recorded
                      size/mtime) instead of only the changed sessions.
  --max-concurrency N Bound the number of sessions parsed/converted/shipped in
                      parallel (default: NumCPU). Caps peak memory on a large corpus.
  --batch-size N      Session parquet objects packed into one batch presign/confirm
                      request (default: 32). Clamped to the 512 ceiling the agent enforces.
  --dry-run           Run fully offline — enumerate + parse + report the intended
                      work per file, contacting neither the keychain nor network.
`

// buildSyncTransportFn is the sync-transport constructor the non-dry-run path
// uses. Tests override it to assert the --dry-run path never reaches it (no
// keychain access). Production callers leave the default.
//
//nolint:gochecknoglobals // overridable transport seam for testability; mirrors newStoreFn.
var buildSyncTransportFn = BuildSyncTransport

// TranscriptUploadCmd implements `knowledge transcript-upload`. Returns nil on
// success; a non-nil error is printed to stderr + exit 1 by the caller. It is
// daemon-independent: the only external dependency (non-dry-run) is the keychain
// (via BuildSyncTransport) — no LocalGraphCaller, no MCP, no local graph.
func TranscriptUploadCmd(args []string) error {
	fs := flag.NewFlagSet("transcript-upload", flag.ContinueOnError)
	fs.SetOutput(os.Stdout)
	fs.Usage = func() { fmt.Fprint(os.Stdout, transcriptUploadUsage) }
	seed := fs.Bool("seed", false, "force a full re-ship (fresh generation, offset 0)")
	maxConcurrency := fs.Int("max-concurrency", 0, "bound parallel file processing (default NumCPU)")
	batchSize := fs.Int("batch-size", 0, "session parquet objects per batch presign/confirm request (default 32, clamped to 512)")
	dryRun := fs.Bool("dry-run", false, "run fully offline: enumerate + parse + report, no network")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	return runTranscriptUpload(ctx, os.Stdout, *seed, *maxConcurrency, *batchSize, *dryRun)
}

// runTranscriptUpload runs one upload batch and writes the summary to out. Factored
// from the flag parsing so tests can drive it with a captured writer. It delegates the
// engine wiring to execTranscriptUpload (the single upload implementation) and only owns
// the CLI-facing summary rendering.
func runTranscriptUpload(ctx context.Context, out io.Writer, seed bool, maxConcurrency, batchSize int, dryRun bool) error {
	// The standalone subcommand is a separate CLI process with no serve
	// --auth-token flag, so it resolves its transport from the env+keychain
	// default (buildSyncTransportFn = BuildSyncTransport).
	summary, runErr := execTranscriptUpload(ctx, seed, maxConcurrency, batchSize, dryRun, buildSyncTransportFn)
	printTranscriptSummary(out, summary)
	return runErr
}

// execTranscriptUpload is the SINGLE transcript-upload implementation: it assembles the
// transcriptsync.Config (KN-1 corpus enumerator + ParseTranscript + the per-file
// size/mtime watermark store + the non-dry-run sync transport) and runs ONE batch,
// returning the Summary. Both the `transcript-upload` subcommand (runTranscriptUpload,
// which prints the Summary) and the daemon's hourly ticker (RunTranscriptUploadOnce,
// which logs it) call through here, so there is exactly one engine-wiring site — the
// daemon does not duplicate the enumerator/parse/watermark/transport assembly.
//
// On --dry-run it builds NO transport (the keychain is never touched) and the engine
// skips the consent fetch — a fully offline preview.
//
// transportFn is the non-dry-run sync-transport constructor. Threading it as a
// parameter lets each caller inject its own source: the standalone subcommand
// passes the env+keychain default (buildSyncTransportFn) while the daemon passes
// its shared cloud token source (bootstrap.buildCloudSyncTransport) so a
// machine-authed headless daemon uses the resolved flag token, not a cold
// keyring read. It is never called under --dry-run.
func execTranscriptUpload(ctx context.Context, seed bool, maxConcurrency, batchSize int, dryRun bool, transportFn func() (*auth.Transport, error)) (transcriptsync.Summary, error) {
	wm, err := transcriptsync.NewDefaultWatermarkStore()
	if err != nil {
		return transcriptsync.Summary{}, err
	}

	cfg := transcriptsync.Config{
		Enumerator:     corpusEnumerator{},
		Parse:          transcriptsync.ParseTranscript,
		Watermarks:     wm,
		Seed:           seed,
		MaxConcurrency: maxConcurrency,
		BatchSize:      batchSize,
		DryRun:         dryRun,
	}

	if !dryRun {
		tr, terr := transportFn()
		if terr != nil {
			return transcriptsync.Summary{}, terr
		}
		cfg.Transport = tr
	}

	return transcriptsync.Run(ctx, cfg)
}

// RunTranscriptUploadOnce runs ONE transcript-upload batch with the daemon's defaults —
// incremental (no --seed), default concurrency + batch-size, and a live network (not a
// dry-run) — and returns the batch Summary. It is the daemon's entry into the SAME
// upload engine the `transcript-upload` subcommand runs (execTranscriptUpload →
// transcriptsync.Run): file-level incremental via the per-file size/mtime watermark,
// consent-gated, best-effort. The serve daemon's hourly ticker calls it; any error
// (consent unreachable, transport/keychain build, per-file failures) is the caller's to
// log-and-retry — this never prints or exits the process.
//
// transportFn is the sync-transport constructor the daemon injects (its shared
// cloud token source) so the hourly loop presents the one warm credential
// instead of a cold per-tick keychain read.
func RunTranscriptUploadOnce(ctx context.Context, transportFn func() (*auth.Transport, error)) (transcriptsync.Summary, error) {
	return execTranscriptUpload(ctx, false /* seed */, 0 /* maxConcurrency */, 0 /* batchSize */, false /* dryRun */, transportFn)
}

// printTranscriptSummary renders the batch outcome: the skip reason (consent off),
// or the per-session rows + parquet size and the batch tallies. Under DryRun the
// counts are what WOULD ship (and no parquet is written, so no size is shown).
func printTranscriptSummary(out io.Writer, s transcriptsync.Summary) {
	if s.Skipped != "" {
		fmt.Fprintf(out, "transcript-upload: skipped — %s\n", s.Skipped)
		return
	}
	verb := "shipped"
	if s.DryRun {
		fmt.Fprintln(out, "transcript-upload: DRY RUN (offline) — no data left this machine")
		verb = "would ship"
	}
	for _, f := range s.Files {
		if f.Err != "" {
			fmt.Fprintf(out, "  %-7s %s: FAILED — %s\n", f.Source, f.Session, f.Err)
			continue
		}
		if f.Rows == 0 {
			continue
		}
		if f.Bytes > 0 {
			fmt.Fprintf(out, "  %-7s %s: %d row(s), %d KiB\n", f.Source, f.Session, f.Rows, f.Bytes>>10)
			continue
		}
		fmt.Fprintf(out, "  %-7s %s: %d row(s)\n", f.Source, f.Session, f.Rows)
	}
	fmt.Fprintf(out, "transcript-upload: %d file(s) scanned, %s %d row(s) from %d file(s)\n",
		s.FilesScanned, verb, s.RowsShipped, s.FilesUploaded)
}

// corpusEnumerator is the KN-1 enumerator adapter: it wraps transcripts.Enumerate
// and surfaces each file as a transcriptsync.TranscriptFile, deriving the
// per-file Session (which transcripts.Entry does not carry) from the path.
type corpusEnumerator struct{}

func (corpusEnumerator) Enumerate() ([]transcriptsync.TranscriptFile, error) {
	entries, err := transcripts.Enumerate()
	if err != nil {
		return nil, err
	}
	files := make([]transcriptsync.TranscriptFile, 0, len(entries))
	for _, e := range entries {
		files = append(files, transcriptsync.TranscriptFile{
			Path:    e.Path,
			Source:  string(e.Source),
			Session: deriveSession(e),
			Size:    e.Size,
		})
	}
	return files, nil
}

// deriveSession resolves the session id AG-1 namespaces the transcript's parts by.
// For claude the filename stem IS the session id; for codex the authoritative id
// is the line-1 session_meta payload.id (falling back to the filename stem). Both
// derivations match the SessionID KN-1's parser stamps on every Row.
func deriveSession(e transcripts.Entry) string {
	stem := strings.TrimSuffix(filepath.Base(e.Path), ".jsonl")
	if e.Source == transcripts.SourceCodex {
		if meta, ok := transcripts.ReadCodexSessionMeta(e.Path); ok && meta.Payload.ID != "" {
			return meta.Payload.ID
		}
	}
	return stem
}
