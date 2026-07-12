// SPDX-License-Identifier: Apache-2.0

package transcriptsync

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"sync"
	"time"
)

// Config is the dependency set for one Run. Transport, Enumerator, and (in
// non-dry-run mode) Watermarks are required; Parse defaults to ParseTranscript
// when nil. DryRun makes the whole run fully offline (no consent fetch, no
// transport) — Transport may be nil.
type Config struct {
	Transport      ControlTransport
	Enumerator     CorpusEnumerator
	Parse          ParseFunc
	Watermarks     *WatermarkStore
	Seed           bool
	MaxConcurrency int
	// BatchSize is the number of session objects packed into one batch presign/confirm
	// request. Clamped by clampBatchSize: <=0 → defaultBatchSize (32), >maxBatchChunks
	// → maxBatchChunks (512). The clamp (never a reject) keeps an over-cap misconfig
	// from making every batch 400 and permanently stranding files.
	BatchSize int
	DryRun    bool
}

// clampBatchSize resolves the effective per-batch object count from Config.BatchSize:
// an unset/non-positive value defaults to defaultBatchSize (32); a value above the
// hand-mirrored agent ceiling maxBatchChunks (512) is CLAMPED down to it (never
// rejected — an over-cap value must not strand files by 400-ing every batch).
func clampBatchSize(n int) int {
	if n <= 0 {
		return defaultBatchSize
	}
	if n > maxBatchChunks {
		return maxBatchChunks
	}
	return n
}

// FileSummary is the per-file outcome of a Run: what the engine intended (or did)
// for one transcript session. Err is non-empty when that file failed (its watermark
// was NOT advanced); a failure on one file never aborts the others. Bytes is the
// converted parquet object size (0 under DryRun, which writes no parquet).
type FileSummary struct {
	Source  string
	Session string
	Path    string
	Rows    int
	Bytes   int64
	Err     string
}

// Summary is the batch outcome. Skipped is non-empty when the whole batch was
// gated off (consent disabled). The tallies count what shipped (or, under DryRun,
// what WOULD ship).
type Summary struct {
	Skipped       string
	DryRun        bool
	FilesScanned  int
	FilesUploaded int
	RowsShipped   int
	Files         []FileSummary
}

// filePlan is one producer's prepared work for a file: the single parquet object to
// ship, the per-session usage rollup computed from its rows, and the watermark to persist
// once it confirms. Built by prepareFile (the parse+convert half of the old processFile),
// consumed by the batch ship loop. rollup is zero-value on the non-ship paths (DryRun /
// unchanged / empty / over-cap) — it is computed only when a real object is written.
type filePlan struct {
	key      string
	emitted  int
	object   sessionObject
	rollup   rollupPayload
	advanced Watermark
}

// pendingObject is one session's parquet handed from a producer to the single
// consumer, tagged with its file's tracker index so the consumer folds the per-object
// outcome back onto the right file.
type pendingObject struct {
	fileIdx int
	object  sessionObject
}

// fileTracker is the per-file pipeline state. The producer publishes summary/total/
// advanced/key BEFORE sending the file's object — the channel send establishes
// happens-before — then never touches the tracker again. The single consumer is
// thereafter the SOLE mutator of okCount/failed/completed and the only caller of
// Watermarks.Advance (never in a defer), so no lock is needed.
type fileTracker struct {
	summary   FileSummary
	key       string
	total     int           // object count for this file (0 or 1); 0 = nothing to ship
	rollup    rollupPayload // the per-session usage rollup shipped on this file's confirm
	advanced  Watermark     // the watermark to persist once the object confirms
	okCount   int           // objects confirmed OK so far
	failed    bool          // the object failed → never advance this file
	completed bool          // fully shipped (non-DryRun) or would-ship (DryRun)
	err       error         // first per-file error, joined into the batch error
}

// residencyHook, when non-nil, is notified as parquet objects become RESIDENT in RAM
// (+1 when sealAndPutBatch reads a temp file's bytes) and are released (-1 once its
// seal+PUT completes). It exists ONLY so the memory-bound test can count the REAL
// concurrently-resident objects and assert the peak is corpus-independent; it is nil
// in production (zero overhead). The bytes are resident only INSIDE the bounded
// seal+PUT pool, so the peak is bounded by `concurrency`, never by the corpus size.
//
//nolint:gochecknoglobals // test-only residency seam.
var residencyHook func(delta int)

func noteResidency(delta int) {
	if residencyHook != nil {
		residencyHook(delta)
	}
}

// Run is the batch entry point: consent-gate (unless DryRun), enumerate the corpus,
// then ship the changed sessions through a BOUNDED PRODUCER/CONSUMER PIPELINE —
// parallel producers parse+convert each changed session to a TEMP-FILE parquet and
// feed its object under an in-flight budget, a single consumer assembles <=BatchSize
// batches and ships each over the batch presign/confirm endpoints, and each session's
// watermark advances only after its object confirms. The budget caps resident objects
// near ~2*BatchSize regardless of corpus size, and per-session conversion writes to a
// temp file with row-group flush so memory is corpus-independent (a convert-ALL design
// would OOM the full-seed path this exists to fix).
//
// Consent has two distinct failure modes: disabled → skip the entire batch (zero
// ships, Summary.Skipped set); fetch error → skip-and-retry (zero ships, NO watermark
// writes, the error returned so a scheduled re-run retries). Per file, any object
// error fails THAT file with its watermark unadvanced (next run re-converts the same
// session) while the consumer keeps draining the rest — best-effort isolation. A whole
// batch's transport error / length mismatch fails only that batch's files; the
// consumer never abandons the loop (which would strand producers on budget-acquire
// and deadlock).
func Run(ctx context.Context, cfg Config) (Summary, error) {
	// (1) Consent gate — once per batch, skipped entirely under DryRun (offline).
	if !cfg.DryRun {
		enabled, err := consentEnabled(ctx, cfg.Transport)
		if err != nil {
			// Skip-and-retry: surface the error, ship nothing, write no watermark.
			return Summary{}, err
		}
		if !enabled {
			return Summary{Skipped: "transcript collection disabled (consent off)"}, nil
		}
	}

	// (2) Enumerate the corpus.
	files, err := cfg.Enumerator.Enumerate()
	if err != nil {
		return Summary{}, fmt.Errorf("transcriptsync: enumerate corpus: %w", err)
	}

	batchSize := clampBatchSize(cfg.BatchSize)
	concurrency := cfg.MaxConcurrency
	if concurrency <= 0 {
		concurrency = runtime.NumCPU()
	}

	trackers := make([]fileTracker, len(files))
	pending := make(chan pendingObject)
	// budget is the in-flight back-pressure: a producer SENDS to acquire a slot before
	// handing its object to the consumer; releaseBatchBudget RECEIVES one per shipped
	// object. Cap = 2*BatchSize so a full batch can always assemble while a prior batch
	// is in flight (a cap < BatchSize would deadlock — producers could never hold
	// enough slots for one complete batch).
	budget := make(chan struct{}, 2*batchSize)

	// (3) PRODUCERS: parse+convert each changed session in parallel (CPU-bound) over a
	// bounded pool, then feed its object into the pipeline. Per-file isolation — one
	// file's prepare failure never aborts the others (the sem-channel + WaitGroup shape,
	// NOT errgroup.WithContext which cancels siblings on the first error).
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	for i, file := range files {
		if cerr := ctx.Err(); cerr != nil {
			trackers[i] = fileTracker{
				summary: FileSummary{Source: file.Source, Session: file.Session, Path: file.Path, Err: cerr.Error()},
				failed:  true,
				err:     cerr,
			}
			break
		}
		wg.Add(1)
		go func(idx int, f TranscriptFile) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			produceFile(ctx, cfg, idx, f, trackers, pending, budget)
		}(i, file)
	}
	// A single closer goroutine closes `pending` once every producer has finished, so
	// the consumer's range terminates.
	go func() {
		wg.Wait()
		close(pending)
	}()

	// (4) CONSUMER (this goroutine): drain `pending`, assemble <=BatchSize batches, and
	// ship each. The SOLE mutator of okCount/failed/completed + the only Advance caller.
	batch := make([]pendingObject, 0, batchSize)
	for po := range pending {
		batch = append(batch, po)
		if len(batch) == batchSize {
			shipBatch(ctx, cfg, concurrency, trackers, batch, budget)
			batch = make([]pendingObject, 0, batchSize)
		}
	}
	if len(batch) > 0 {
		shipBatch(ctx, cfg, concurrency, trackers, batch, budget)
	}

	// (5) Merge: aggregate the tallies and join per-file errors (best-effort).
	return mergeTrackers(cfg, trackers)
}

// produceFile prepares one file's parquet object and feeds it into the pipeline. On a
// prepare error, an unchanged file, or an empty session it records the per-file
// outcome and returns without enqueuing. Under DryRun it records the intended work and
// ships nothing (bounded memory — no parquet is written or enqueued). Otherwise it
// publishes total=1 + the advanced watermark BEFORE the send (the send establishes
// happens-before for the consumer), then ACQUIRES one budget slot and sends the
// object — both the acquire and the send SELECT on ctx.Done() so a cancelled run
// unwinds instead of blocking, os.Removing the already-written temp parquet on either
// cancel branch (releaseBatchBudget only reaps objects that reached a shipped batch).
func produceFile(ctx context.Context, cfg Config, idx int, file TranscriptFile, trackers []fileTracker, pending chan<- pendingObject, budget chan struct{}) {
	plan, err := prepareFile(cfg, file)
	tr := &trackers[idx]
	tr.key = plan.key
	tr.summary = FileSummary{
		Source: file.Source, Session: file.Session, Path: file.Path,
		Rows: plan.emitted, Bytes: plan.object.size,
	}
	if err != nil {
		tr.summary.Err = err.Error()
		tr.failed = true
		tr.err = err
		return
	}
	if plan.emitted == 0 {
		// Unchanged file or empty session — nothing to ship, nothing to advance.
		return
	}
	if cfg.DryRun {
		// Offline preview: report the intended rows, ship nothing (no temp written).
		tr.completed = true
		return
	}

	// Publish the file's object total + the rollup + the watermark to persist on
	// completion BEFORE the send (the channel send is the happens-before edge to the
	// consumer, which reads tr.rollup when it builds the confirm chunk).
	tr.total = 1
	tr.rollup = plan.rollup
	tr.advanced = plan.advanced

	select {
	case budget <- struct{}{}: // acquire one in-flight slot (back-pressure).
	case <-ctx.Done():
		_ = os.Remove(plan.object.path) // temp written but never shipped — reap it.
		return
	}
	select {
	case pending <- pendingObject{fileIdx: idx, object: plan.object}:
	case <-ctx.Done():
		<-budget                        // release the slot we will not send.
		_ = os.Remove(plan.object.path) // temp written but never shipped — reap it.
		return
	}
}

// prepareFile is the parse+convert half of the old per-file ship: open, Stat for the
// live size/mtime, look up the watermark, and — only when the session CHANGED (or is
// new / --seed) — parse the WHOLE file and convert its rows to a temp-file parquet. It
// mutates NO shared state and touches the network not at all — pure CPU work safe to
// run in parallel. An unchanged file or an empty session returns emitted==0 (nothing
// to ship). Under DryRun it returns the row count with NO temp parquet written
// (short-circuits before os.CreateTemp so a preview never orphans a temp). A converted
// parquet exceeding maxClientParquetBytes is skipped with a per-file error (its temp
// removed). The returned advanced Watermark is the {Size,Mtime} cursor to persist once
// the object confirms.
func prepareFile(cfg Config, file TranscriptFile) (filePlan, error) {
	plan := filePlan{key: file.Source + ":" + file.Session}

	f, err := os.Open(file.Path) //nolint:gosec // path comes from the corpus enumerator, not user text.
	if err != nil {
		return plan, fmt.Errorf("transcriptsync: open %s: %w", file.Path, err)
	}
	defer func() { _ = f.Close() }()

	stat, err := f.Stat()
	if err != nil {
		return plan, fmt.Errorf("transcriptsync: stat %s: %w", file.Path, err)
	}

	w, has := cfg.Watermarks.Lookup(plan.key)
	if !shouldReupload(stat, w, has, cfg.Seed, rollupSchemaVersion, time.Now()) {
		// Byte-identical to the last shipped session, or changed but still live within
		// the quiet window — nothing to ship this tick.
		return plan, nil
	}

	// Pre-parse raw-size cap: reject a pathological session BEFORE parsing it, so its
	// full row slice is never materialized in RAM and cannot OOM the daemon. Mirrors the
	// post-parse maxClientParquetBytes over-cap skip below — a per-file error (watermark
	// unadvanced, other files unaffected) plus a loud warn at skip time. The watermark
	// stays put, so an over-cap file re-fails on every tick until it shrinks or is
	// removed; the upload-health counters surface that durable failure.
	if stat.Size() > maxRawTranscriptBytes {
		slog.Warn("transcriptsync: raw transcript exceeds cap; skipped before parse (never materialized in memory)",
			"source", file.Source, "session", file.Session,
			"size_mib", stat.Size()>>20, "cap_mib", maxRawTranscriptBytes>>20)
		return plan, fmt.Errorf("transcriptsync: raw transcript %d MiB exceeds %d MiB cap; skipped before parse",
			stat.Size()>>20, maxRawTranscriptBytes>>20)
	}

	parse := cfg.Parse
	if parse == nil {
		parse = ParseTranscript
	}
	rows, err := parse(file.Source, f)
	if err != nil {
		return plan, fmt.Errorf("transcriptsync: parse %s transcript: %w", file.Source, err)
	}
	plan.emitted = len(rows)
	if len(rows) == 0 {
		// An empty session writes nothing and ships nothing.
		return plan, nil
	}

	// The watermark to persist once the object confirms — built here (never in a
	// defer), advanced by the consumer. RollupSchemaVersion records the contract version
	// this session was shipped under, so a later schema bump re-ships it (shouldReupload).
	plan.advanced = Watermark{Size: stat.Size(), Mtime: stat.ModTime().UnixNano(), RollupSchemaVersion: rollupSchemaVersion}

	if cfg.DryRun {
		// Offline preview: report the row count, write NO temp parquet (criterion g).
		return plan, nil
	}

	obj, err := writeSessionTempParquet(rows)
	if err != nil {
		return plan, err // writeSessionTempParquet removed its own temp on failure.
	}
	if obj.size > maxClientParquetBytes {
		_ = os.Remove(obj.path)
		return plan, fmt.Errorf("transcriptsync: session parquet %d MiB exceeds 128MiB cap; skipped", obj.size>>20)
	}
	plan.object = obj

	// Compute the per-session usage rollup from the in-memory rows (still resident here —
	// no extra retention). Only the REAL ship path reaches this: the DryRun short-circuit,
	// the unchanged/empty early returns, and the raw/parquet over-cap skips all return
	// before it, so a preview / unchanged / over-cap file computes no rollup. It rides the
	// same confirm as this parquet object (ship.go), so a rejected confirm re-derives it.
	plan.rollup = computeSessionRollup(rows)

	// Best-effort: also persist the converted parquet to the stable local cache the
	// daemon-local analyzer queries. A cache-write failure is logged and swallowed — it
	// must never abort the session's upload/ship (T3-3). DryRun short-circuits above,
	// so no cache write happens on a preview.
	if cerr := cacheSessionParquet(file.Source, file.Session, obj.path); cerr != nil {
		slog.Warn("transcriptsync: cache session parquet failed (best-effort; upload unaffected)",
			"source", file.Source, "session", file.Session, "err", cerr)
	}
	return plan, nil
}
