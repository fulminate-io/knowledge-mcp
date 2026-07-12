// SPDX-License-Identifier: Apache-2.0

package transcriptsync

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/transcripts"
)

// withTempParquetDir redirects the session-parquet temp writes to a fresh t.TempDir()
// and returns it, so a test can assert NO temp parquet leaked. Restores the seam on
// cleanup.
func withTempParquetDir(t *testing.T) string {
	t.Helper()
	prev := tempParquetDir
	dir := t.TempDir()
	tempParquetDir = dir
	t.Cleanup(func() { tempParquetDir = prev })
	return dir
}

// TestRun_DryRunWritesNoTempParquet proves criterion g on the success path: a
// --dry-run Run over changed sessions writes NO temp parquet (prepareFile
// short-circuits before os.CreateTemp), reports the would-ship row counts, advances
// no watermark, and leaves the temp dir empty.
func TestRun_DryRunWritesNoTempParquet(t *testing.T) {
	tmpDir := withTempParquetDir(t)
	files := singleObjectCorpus(t, 5)
	wm, _ := newTempWatermarkStore(t)

	summary, err := Run(context.Background(), Config{
		Enumerator: fixedEnumerator{files: files},
		Parse:      offsetsParse,
		Watermarks: wm,
		DryRun:     true,
	})
	require.NoError(t, err)
	assert.True(t, summary.DryRun)
	assert.Equal(t, 5, summary.FilesUploaded, "dry-run reports the would-ship sessions")
	assert.Positive(t, summary.RowsShipped, "dry-run reports the would-ship rows")

	ents, err := os.ReadDir(tmpDir)
	require.NoError(t, err)
	assert.Empty(t, ents, "a dry-run writes NO temp parquet")

	for i := range files {
		_, ok := wm.Lookup("claude:s" + strconv.Itoa(i))
		assert.False(t, ok, "dry-run advances no watermark")
	}
}

// TestRun_OverCapSessionSkipped proves the over-cap-skip + no-leak guarantee: a session
// whose converted parquet exceeds maxClientParquetBytes is SKIPPED — its FileSummary
// carries the over-cap error, no object is PUT for it, its watermark is NOT advanced,
// its temp parquet is removed, and a sibling under-cap session still ships.
func TestRun_OverCapSessionSkipped(t *testing.T) {
	tmpDir := withTempParquetDir(t)

	prevCap := maxClientParquetBytes
	maxClientParquetBytes = 64 << 10 // 64 KiB: the high-entropy session exceeds it, the tiny one does not.
	t.Cleanup(func() { maxClientParquetBytes = prevCap })

	dir := t.TempDir()
	bigPath := writeOffsetsFile(t, dir, "big.jsonl", 1)
	okPath := writeOffsetsFile(t, dir, "ok.jsonl", 1)

	// capParse returns a huge high-entropy (incompressible) session for the "big"
	// source and a tiny one otherwise, so the "big" parquet reliably exceeds 64 KiB
	// and the "ok" parquet is well under it — independent of Zstd's ratio.
	capParse := func(source string, _ io.Reader) ([]transcripts.Row, error) {
		if source == "big" {
			return highEntropyRows(5000), nil
		}
		return []transcripts.Row{{Source: transcripts.SourceClaude, SessionID: "ok-sess", InputTokens: 1, RecordTS: time.Unix(1, 0).UTC()}}, nil
	}

	wm, _ := newTempWatermarkStore(t)
	backend := newFakeTranscriptBackend(t)
	backend.consentEnabledFlag = true

	summary, err := Run(context.Background(), Config{
		Transport: backend,
		Enumerator: fixedEnumerator{files: []TranscriptFile{
			{Path: bigPath, Source: "big", Session: "big-sess"},
			{Path: okPath, Source: "claude", Session: "ok-sess"},
		}},
		Parse:      capParse,
		Watermarks: wm,
	})
	require.Error(t, err, "the over-cap session surfaces a per-file error")
	assert.Equal(t, 1, summary.FilesUploaded, "only the under-cap sibling shipped")

	var bigErr string
	for _, f := range summary.Files {
		if f.Session == "big-sess" {
			bigErr = f.Err
		}
	}
	assert.Contains(t, bigErr, "exceeds 128MiB cap; skipped", "over-cap session carries the skip error")

	// No object PUT for the over-cap session; only the sibling shipped one object.
	assert.Equal(t, 1, backend.putObjectCount(), "the over-cap session issued no PUT")
	// The over-cap session is unadvanced; the sibling advanced.
	_, okBig := wm.Lookup("big:big-sess")
	assert.False(t, okBig, "the over-cap session's watermark is NOT advanced")
	_, okOK := wm.Lookup("claude:ok-sess")
	assert.True(t, okOK, "the under-cap sibling advanced")

	// No temp parquet survives — the over-cap temp was removed by prepareFile, the
	// shipped temp by releaseBatchBudget.
	ents, err := os.ReadDir(tmpDir)
	require.NoError(t, err)
	assert.Empty(t, ents, "no temp parquet leaked after the over-cap skip")
}

// TestProduceFile_CancelRemovesTempParquet proves the cancel-cleanup clause:
// produceFile os.Removes the temp parquet it already wrote when the context
// is cancelled before its object can enter a shipped batch — the pre-ship leak owner
// that releaseBatchBudget cannot cover.
func TestProduceFile_CancelRemovesTempParquet(t *testing.T) {
	tmpDir := withTempParquetDir(t)

	dir := t.TempDir()
	path := writeOffsetsFile(t, dir, "a.jsonl", 1, 2, 3)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancelled: produceFile's send SELECT takes the ctx.Done cleanup branch.

	wm, _ := newTempWatermarkStore(t)
	trackers := make([]fileTracker, 1)
	pending := make(chan pendingObject) // unbuffered + never read → the send blocks, ctx.Done wins.
	budget := make(chan struct{}, 2)

	produceFile(ctx, Config{Parse: offsetsParse, Watermarks: wm}, 0,
		TranscriptFile{Path: path, Source: "claude", Session: "a"}, trackers, pending, budget)

	ents, err := os.ReadDir(tmpDir)
	require.NoError(t, err)
	assert.Empty(t, ents, "produceFile removed its temp parquet on cancellation")
}

// TestRun_RealisticBatch_WallTimeUnderWriteTimeout is the EMPIRICAL wall-time bound:
// an everyday-workload batch (defaultBatchSize sessions, each a
// KB–low-MB parquet) completes end-to-end — convert + seal + PUT + confirm, including
// the real RSA-3072 wrap/unwrap + AES-GCM crypto per object — well under the agent's
// 10s SERVER_WRITE_TIMEOUT. Measured, not asserted analytically. The 128MiB-object
// worst case is a pathological ceiling the client guard makes rare, so it is
// deliberately out of this everyday budget.
func TestRun_RealisticBatch_WallTimeUnderWriteTimeout(t *testing.T) {
	const sessions = 32 // == defaultBatchSize: one full confirm-batch.
	dir := t.TempDir()
	files := make([]TranscriptFile, sessions)
	for i := range files {
		p := writeOffsetsFile(t, dir, "s"+strconv.Itoa(i)+".jsonl", 1)
		files[i] = TranscriptFile{Path: p, Source: "claude", Session: "sess-" + strconv.Itoa(i)}
	}
	// Each session parses to ~2000 incompressible rows → a realistic KB–low-MB parquet.
	realisticParse := func(_ string, _ io.Reader) ([]transcripts.Row, error) {
		return highEntropyRows(2000), nil
	}
	wm, _ := newTempWatermarkStore(t)
	backend := newFakeTranscriptBackend(t)
	backend.consentEnabledFlag = true

	start := time.Now()
	summary, err := Run(context.Background(), Config{
		Transport: backend, Enumerator: fixedEnumerator{files: files},
		Parse: realisticParse, Watermarks: wm, BatchSize: 32,
	})
	elapsed := time.Since(start)
	require.NoError(t, err)
	require.Equal(t, sessions, summary.FilesUploaded, "the whole realistic batch shipped")
	assert.Less(t, elapsed, 8*time.Second,
		"realistic full batch completed in %s — must land well under the 10s server WriteTimeout", elapsed)
}

// highEntropyRows builds n rows carrying unique random hex in the uuid/parent_uuid
// columns, so the resulting parquet is incompressible (its size grows with n
// regardless of Zstd) — used to force a session reliably over a lowered size cap.
func highEntropyRows(n int) []transcripts.Row {
	rows := make([]transcripts.Row, n)
	for i := range rows {
		var buf [32]byte
		_, _ = rand.Read(buf[:])
		id := hex.EncodeToString(buf[:])
		rows[i] = transcripts.Row{
			Source: transcripts.SourceClaude, SessionID: "big-sess",
			RecordTS: time.Unix(int64(i), 0).UTC(), UUID: id, ParentUUID: id,
		}
	}
	return rows
}
