// SPDX-License-Identifier: Apache-2.0

package transcriptsync

import (
	"context"
	"fmt"
	"io"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/transcripts"
)

// richRollupParse is a ParseFunc that ignores the file bytes and returns a fixed pair of
// trustworthy tool rows sharing one (tool, input_hash) — so the computed rollup populates
// EVERY row kind (session scalars, a fact grain, latency-hist bins, slow calls, and a
// session-total>1 duplicate group). It lets the integration tests assert the whole rollup
// rides the confirm without crafting real transcript JSON.
func richRollupParse(source string, _ io.Reader) ([]transcripts.Row, error) {
	ts := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	return []transcripts.Row{
		{Source: transcripts.Source(source), SessionID: "rs", Project: "/w", Model: "m", ToolName: "Bash", ToolInputHash: "h", ToolInputPreview: "cmd", RecordTS: ts, DurationMs: 100, InputTokens: 10},
		{Source: transcripts.Source(source), SessionID: "rs", Project: "/w", Model: "m", ToolName: "Bash", ToolInputHash: "h", ToolInputPreview: "cmd", RecordTS: ts, DurationMs: 200, InputTokens: 20},
	}, nil
}

// TestRun_RollupOnConfirmAndWatermarkVersion proves the confirm body carries the computed
// rollup (schema_version==current, every row kind present) and that a successful confirm
// persists the watermark with RollupSchemaVersion==rollupSchemaVersion.
func TestRun_RollupOnConfirmAndWatermarkVersion(t *testing.T) {
	dir := t.TempDir()
	path := writeOffsetsFile(t, dir, "s.jsonl", 1, 2)
	wm, _ := newTempWatermarkStore(t)
	backend := newFakeTranscriptBackend(t)
	backend.consentEnabledFlag = true

	summary, err := Run(context.Background(), Config{
		Transport:  backend,
		Enumerator: fixedEnumerator{files: []TranscriptFile{{Path: path, Source: "claude", Session: "rollup-sess"}}},
		Parse:      richRollupParse,
		Watermarks: wm,
	})
	require.NoError(t, err)
	require.Equal(t, 1, summary.FilesUploaded)

	// (a) the confirm chunk carries a non-nil rollup (JSON-round-tripped by the backend)
	// with schema_version==current and every row kind populated.
	confirms := backend.confirmsForSession("rollup-sess")
	require.Len(t, confirms, 1)
	rollup := confirms[0].Rollup
	require.NotNil(t, rollup, "the confirm chunk carries the rollup payload")
	assert.Equal(t, rollupSchemaVersion, rollup.SchemaVersion, "schema_version is the current frozen version")
	assert.Equal(t, int64(2), rollup.Session.RecordCount, "session scalars computed")
	assert.NotEmpty(t, rollup.Facts, "fact rows present")
	assert.NotEmpty(t, rollup.LatencyHist, "latency-hist rows present")
	assert.NotEmpty(t, rollup.SlowCalls, "slow-call rows present")
	assert.NotEmpty(t, rollup.DuplicateCommands, "duplicate rows present (session-total 2)")

	// (c) the persisted watermark records the current rollup schema version.
	w, ok := wm.Lookup("claude:rollup-sess")
	require.True(t, ok, "the watermark advanced on the successful confirm")
	assert.Equal(t, rollupSchemaVersion, w.RollupSchemaVersion, "watermark persists the current rollup schema version")
}

// TestRun_RollupConfirmFailure_NoAdvanceRetry proves a failed/errored confirm leaves the
// watermark unadvanced and the next Run re-derives + re-ships the same session.
func TestRun_RollupConfirmFailure_NoAdvanceRetry(t *testing.T) {
	dir := t.TempDir()
	path := writeOffsetsFile(t, dir, "s.jsonl", 1, 2)
	wm, _ := newTempWatermarkStore(t)
	backend := newFakeTranscriptBackend(t)
	backend.consentEnabledFlag = true
	backend.failConfirmSession = "retry-sess" // first run's confirm returns OK:false.

	cfg := Config{
		Transport:  backend,
		Enumerator: fixedEnumerator{files: []TranscriptFile{{Path: path, Source: "claude", Session: "retry-sess"}}},
		Parse:      richRollupParse,
		Watermarks: wm,
	}

	_, err := Run(context.Background(), cfg)
	require.Error(t, err, "the failed confirm surfaces an error")
	_, ok := wm.Lookup("claude:retry-sess")
	assert.False(t, ok, "the watermark is unadvanced after a failed confirm")

	// Next Run: with the failure cleared, the same session re-ships (a second confirm
	// attempt) and now advances — the retry path.
	backend.failConfirmSession = ""
	second, err := Run(context.Background(), cfg)
	require.NoError(t, err)
	assert.Equal(t, 1, second.FilesUploaded, "the session re-shipped on the next run")
	assert.Len(t, backend.confirmsForSession("retry-sess"), 2, "the session was confirmed twice (retry)")
	w, ok := wm.Lookup("claude:retry-sess")
	require.True(t, ok, "the watermark advanced on the successful retry")
	assert.Equal(t, rollupSchemaVersion, w.RollupSchemaVersion)
}

// TestRun_ConfirmByteSplit proves the byte-aware confirm split: when the cumulative rollup
// JSON in one batch exceeds maxConfirmRollupBytes, the batch's confirm is split into
// multiple POSTs (one per file here, budget lowered) while every file still advances — the
// split is client-side only and preserves per-file advance semantics.
func TestRun_ConfirmByteSplit(t *testing.T) {
	prev := maxConfirmRollupBytes
	maxConfirmRollupBytes = 8 // any single session's rollup JSON exceeds 8 bytes.
	t.Cleanup(func() { maxConfirmRollupBytes = prev })

	dir := t.TempDir()
	files := []TranscriptFile{}
	for i := range 3 {
		p := writeOffsetsFile(t, dir, fmt.Sprintf("s%d.jsonl", i), int64(i+1))
		files = append(files, TranscriptFile{Path: p, Source: "claude", Session: fmt.Sprintf("split-%d", i)})
	}
	wm, _ := newTempWatermarkStore(t)
	backend := newFakeTranscriptBackend(t)
	backend.consentEnabledFlag = true

	summary, err := Run(context.Background(), Config{
		Transport:  backend,
		Enumerator: fixedEnumerator{files: files},
		Parse:      richRollupParse,
		Watermarks: wm,
		BatchSize:  32, // all three sessions land in ONE presign/ship batch.
	})
	require.NoError(t, err)

	assert.Equal(t, 3, summary.FilesUploaded, "every file advanced despite the split")
	assert.Equal(t, 1, backend.presignCalls, "presign is one batch (the split is confirm-only)")
	assert.Equal(t, 3, backend.confirmCalls, "the confirm split into one POST per file (budget lowered)")
	for i := range 3 {
		w, ok := wm.Lookup(fmt.Sprintf("claude:split-%d", i))
		require.True(t, ok, "file %d advanced via its own confirm POST", i)
		assert.Equal(t, rollupSchemaVersion, w.RollupSchemaVersion)
	}
}

// singleObjectCorpus writes K single-row (→ one parquet object) transcript files
// under a fresh temp dir and returns their TranscriptFile descriptors with distinct
// sessions.
func singleObjectCorpus(t *testing.T, k int) []TranscriptFile {
	t.Helper()
	dir := t.TempDir()
	files := make([]TranscriptFile, k)
	for i := range files {
		path := writeOffsetsFile(t, dir, fmt.Sprintf("f%d.jsonl", i), int64(i+1))
		files[i] = TranscriptFile{Path: path, Source: "claude", Session: fmt.Sprintf("s%d", i)}
	}
	return files
}

// noDeadlockTimeout bounds how long a transport-error run may take before the test
// declares a deadlock. Generous — the run should return near-instantly once the
// batches fail and the budget unwinds.
const noDeadlockTimeout = 10 * time.Second

// runWithTimeout runs the engine in a goroutine and FAILS the test if it does not
// return within noDeadlockTimeout — the no-deadlock guard for the transport-error cases.
func runWithTimeout(t *testing.T, cfg Config) (Summary, error) {
	t.Helper()
	type res struct {
		s   Summary
		err error
	}
	ch := make(chan res, 1)
	go func() {
		s, err := Run(context.Background(), cfg)
		ch <- res{s, err}
	}()
	select {
	case r := <-ch:
		return r.s, r.err
	case <-time.After(noDeadlockTimeout):
		t.Fatal("Run deadlocked — did not return within the timeout")
		return Summary{}, nil
	}
}

// TestRun_BatchRequestCount proves criterion a: N single-object sessions at BatchSize
// B cost exactly ceil(N/B) presign-batch and ceil(N/B) confirm-batch control calls
// (O(ceil(totalObjects/batchSize)), not ~2 per file), and every watermark advances.
func TestRun_BatchRequestCount(t *testing.T) {
	const n, b = 20, 8
	files := singleObjectCorpus(t, n)
	wm, _ := newTempWatermarkStore(t)
	backend := newFakeTranscriptBackend(t)
	backend.consentEnabledFlag = true

	summary, err := Run(context.Background(), Config{
		Transport:  backend,
		Enumerator: fixedEnumerator{files: files},
		Parse:      offsetsParse,
		Watermarks: wm,
		BatchSize:  b,
	})
	require.NoError(t, err)

	wantBatches := (n + b - 1) / b // ceil(N/B) = 3 for (20,8)
	assert.Equal(t, wantBatches, backend.presignCalls, "exactly ceil(N/B) presign-batch calls")
	assert.Equal(t, wantBatches, backend.confirmCalls, "exactly ceil(N/B) confirm-batch calls")
	assert.Equal(t, n, summary.FilesUploaded, "every file uploaded")
	for i := range files {
		_, ok := wm.Lookup(fmt.Sprintf("claude:s%d", i))
		assert.True(t, ok, "file %d watermark advanced", i)
	}
}

// TestRun_PartialBatch_OneFileConfirmFails proves criterion c (client side): in a
// single batch carrying two sessions, one session's object confirm returning OK:false
// leaves that session's watermark UNADVANCED while the other confirms and advances.
func TestRun_PartialBatch_OneFileConfirmFails(t *testing.T) {
	dir := t.TempDir()
	pathOK := writeOffsetsFile(t, dir, "ok.jsonl", 1, 2)
	pathBad := writeOffsetsFile(t, dir, "bad.jsonl", 3, 4)
	wm, _ := newTempWatermarkStore(t)
	backend := newFakeTranscriptBackend(t)
	backend.consentEnabledFlag = true
	backend.failConfirmSession = "bad" // bad's per-element confirm returns OK:false

	summary, err := Run(context.Background(), Config{
		Transport: backend,
		Enumerator: fixedEnumerator{files: []TranscriptFile{
			{Path: pathOK, Source: "claude", Session: "ok"},
			{Path: pathBad, Source: "claude", Session: "bad"},
		}},
		Parse:      offsetsParse,
		Watermarks: wm,
		BatchSize:  32, // both sessions land in ONE batch
	})
	require.Error(t, err, "the failed session's error is returned")
	assert.Equal(t, 1, summary.FilesUploaded, "only the good session uploaded")

	_, okGood := wm.Lookup("claude:ok")
	assert.True(t, okGood, "the good session advanced")
	_, okBad := wm.Lookup("claude:bad")
	assert.False(t, okBad, "the failed session is unadvanced (re-converted next run)")
}

// TestRun_WrongLengthBatchResponse proves T3-1: a presign-batch (or confirm-batch)
// reply of the WRONG length fails that batch's files (watermarks unadvanced) with a
// clear error and NO panic — positional pairing happens only AFTER the length guard.
func TestRun_WrongLengthBatchResponse(t *testing.T) {
	run := func(t *testing.T, inject func(*fakeTranscriptBackend)) (Summary, error, *fakeTranscriptBackend, *WatermarkStore) {
		t.Helper()
		files := singleObjectCorpus(t, 3)
		wm, _ := newTempWatermarkStore(t)
		backend := newFakeTranscriptBackend(t)
		backend.consentEnabledFlag = true
		inject(backend)
		summary, err := Run(context.Background(), Config{
			Transport: backend, Enumerator: fixedEnumerator{files: files},
			Parse: offsetsParse, Watermarks: wm, BatchSize: 32,
		})
		return summary, err, backend, wm
	}

	t.Run("presign-batch wrong length", func(t *testing.T) {
		summary, err, _, wm := run(t, func(b *fakeTranscriptBackend) { b.presignWrongLen = true })
		require.Error(t, err, "a length mismatch fails the batch")
		assert.Equal(t, 0, summary.FilesUploaded)
		for i := range 3 {
			_, ok := wm.Lookup(fmt.Sprintf("claude:s%d", i))
			assert.False(t, ok, "file %d unadvanced on a presign length mismatch", i)
		}
	})

	t.Run("confirm-batch wrong length", func(t *testing.T) {
		summary, err, backend, wm := run(t, func(b *fakeTranscriptBackend) { b.confirmWrongLen = true })
		require.Error(t, err, "a confirm length mismatch fails the batch")
		assert.Equal(t, 0, summary.FilesUploaded)
		assert.Positive(t, backend.putObjectCount(), "the PUTs happened before the confirm mismatch")
		for i := range 3 {
			_, ok := wm.Lookup(fmt.Sprintf("claude:s%d", i))
			assert.False(t, ok, "file %d unadvanced on a confirm length mismatch", i)
		}
	})
}

// TestRun_PresignBatchTransportError_NoDeadlock proves T2 for the presign-batch path:
// a transport error mid multi-batch run makes Run RETURN (no deadlock — the
// releaseBatchBudget defer unwinds producers blocked on the budget), the failing
// batch's files stay unadvanced, and unaffected files still advance.
func TestRun_PresignBatchTransportError_NoDeadlock(t *testing.T) {
	const k, b = 40, 4

	t.Run("every batch fails: returns, nothing advances", func(t *testing.T) {
		files := singleObjectCorpus(t, k)
		wm, _ := newTempWatermarkStore(t)
		backend := newFakeTranscriptBackend(t)
		backend.consentEnabledFlag = true
		backend.presignErr = true // EVERY presign-batch fails

		summary, err := runWithTimeout(t, Config{
			Transport: backend, Enumerator: fixedEnumerator{files: files},
			Parse: offsetsParse, Watermarks: wm, BatchSize: b, MaxConcurrency: 4,
		})
		require.Error(t, err)
		assert.Equal(t, 0, summary.FilesUploaded, "no file advanced when every presign-batch failed")
	})

	t.Run("first batch fails: unaffected files still advance", func(t *testing.T) {
		files := singleObjectCorpus(t, k)
		wm, _ := newTempWatermarkStore(t)
		backend := newFakeTranscriptBackend(t)
		backend.consentEnabledFlag = true
		backend.presignFailFirstN = 1 // only the first batch's presign fails

		summary, err := runWithTimeout(t, Config{
			Transport: backend, Enumerator: fixedEnumerator{files: files},
			Parse: offsetsParse, Watermarks: wm, BatchSize: b, MaxConcurrency: 4,
		})
		require.Error(t, err, "the failed batch's files surface an error")
		assert.Positive(t, summary.FilesUploaded, "unaffected batches' files advanced")
		assert.Less(t, summary.FilesUploaded, k, "the failed batch's files did not advance")
	})
}

// TestRun_ConfirmBatchTransportError_NoDeadlock proves T2 for the confirm-batch path:
// the same no-deadlock + isolation guarantees when the confirm-batch request errors.
func TestRun_ConfirmBatchTransportError_NoDeadlock(t *testing.T) {
	const k, b = 40, 4

	t.Run("every batch fails: returns, nothing advances", func(t *testing.T) {
		files := singleObjectCorpus(t, k)
		wm, _ := newTempWatermarkStore(t)
		backend := newFakeTranscriptBackend(t)
		backend.consentEnabledFlag = true
		backend.confirmErr = true // EVERY confirm-batch fails (PUTs still happen)

		summary, err := runWithTimeout(t, Config{
			Transport: backend, Enumerator: fixedEnumerator{files: files},
			Parse: offsetsParse, Watermarks: wm, BatchSize: b, MaxConcurrency: 4,
		})
		require.Error(t, err)
		assert.Equal(t, 0, summary.FilesUploaded, "no file advanced when every confirm-batch failed")
	})

	t.Run("first batch fails: unaffected files still advance", func(t *testing.T) {
		files := singleObjectCorpus(t, k)
		wm, _ := newTempWatermarkStore(t)
		backend := newFakeTranscriptBackend(t)
		backend.consentEnabledFlag = true
		backend.confirmFailFirstN = 1

		summary, err := runWithTimeout(t, Config{
			Transport: backend, Enumerator: fixedEnumerator{files: files},
			Parse: offsetsParse, Watermarks: wm, BatchSize: b, MaxConcurrency: 4,
		})
		require.Error(t, err)
		assert.Positive(t, summary.FilesUploaded, "unaffected batches' files advanced")
		assert.Less(t, summary.FilesUploaded, k, "the failed batch's files did not advance")
	})
}

// TestRun_MemoryBound_CorpusIndependent proves the memory bound: the peak count of
// REAL concurrently-resident parquet objects (counted via the residency seam as each
// object's bytes are read from its temp file at seal time, NOT the budget semaphore)
// stays <= 2*BatchSize + NumCPU and — critically — does NOT grow with the corpus size
// K. A convert-ALL→hold-ALL design would make the peak scale with K (the OOM this
// engine exists to avoid); holding each parquet on DISK until seal time is what keeps
// resident bytes corpus-independent.
func TestRun_MemoryBound_CorpusIndependent(t *testing.T) {
	const b = 8

	measurePeak := func(k int) int {
		files := singleObjectCorpus(t, k)
		wm, _ := newTempWatermarkStore(t)
		backend := newFakeTranscriptBackend(t)
		backend.consentEnabledFlag = true

		var mu sync.Mutex
		cur, peak := 0, 0
		prev := residencyHook
		residencyHook = func(delta int) {
			mu.Lock()
			cur += delta
			if cur > peak {
				peak = cur
			}
			mu.Unlock()
		}
		defer func() { residencyHook = prev }()

		_, err := Run(context.Background(), Config{
			Transport: backend, Enumerator: fixedEnumerator{files: files},
			Parse: offsetsParse, Watermarks: wm, BatchSize: b,
		})
		require.NoError(t, err)
		mu.Lock()
		defer mu.Unlock()
		require.Zero(t, cur, "every resident object was released (+/- balanced)")
		return peak
	}

	bound := 2*b + runtime.NumCPU()
	peakSmall := measurePeak(50)
	peakLarge := measurePeak(200) // 4x the corpus

	assert.LessOrEqual(t, peakSmall, bound, "K=50 peak resident objects within 2*BatchSize+NumCPU")
	assert.LessOrEqual(t, peakLarge, bound, "K=200 peak resident objects within 2*BatchSize+NumCPU")
	// Corpus-independence: 4x the files must not materially grow the peak (the whole
	// point — memory is bounded by the pipeline, not the corpus).
	assert.LessOrEqual(t, peakLarge, peakSmall+runtime.NumCPU(),
		"peak must NOT grow with corpus size K (got %d for K=50, %d for K=200)", peakSmall, peakLarge)
}
