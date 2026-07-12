// SPDX-License-Identifier: Apache-2.0

package transcriptsync

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/transcripts"
)

// fixedEnumerator is a CorpusEnumerator returning a fixed file list.
type fixedEnumerator struct{ files []TranscriptFile }

func (e fixedEnumerator) Enumerate() ([]TranscriptFile, error) { return e.files, nil }

// offsetsParse is a fake ParseFunc: it reads whitespace-separated ints from the
// reader and returns one Row per int (InputTokens = the int). It lets a test drive
// exactly how many rows a session yields without crafting real transcript JSON.
func offsetsParse(source string, r io.Reader) ([]transcripts.Row, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	var rows []transcripts.Row
	for f := range strings.FieldsSeq(string(data)) {
		off, perr := strconv.ParseInt(f, 10, 64)
		if perr != nil {
			continue
		}
		rows = append(rows, transcripts.Row{
			Source:       transcripts.Source(source),
			SessionID:    "row-sess",
			SourceOffset: off,
			InputTokens:  off,
		})
	}
	return rows, nil
}

func writeOffsetsFile(t *testing.T, dir, name string, offsets ...int64) string {
	t.Helper()
	path := filepath.Join(dir, name)
	var sb strings.Builder
	for _, o := range offsets {
		fmt.Fprintf(&sb, "%d\n", o)
	}
	require.NoError(t, os.WriteFile(path, []byte(sb.String()), 0o600))
	return path
}

func newTempWatermarkStore(t *testing.T) (*WatermarkStore, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript-watermarks.json")
	return &WatermarkStore{path: path}, path
}

// TestRun_ConsentGate covers the two gate failure modes:
//   - disabled → zero presign calls, Summary marks the batch skipped, no watermark.
//   - fetch error → zero presign calls, the error is returned, no watermark written.
func TestRun_ConsentGate(t *testing.T) {
	t.Run("disabled skips the whole batch", func(t *testing.T) {
		backend := newFakeTranscriptBackend(t)
		backend.consentEnabledFlag = false
		wm, wmPath := newTempWatermarkStore(t)
		dir := t.TempDir()
		path := writeOffsetsFile(t, dir, "a.jsonl", 1, 2, 3)

		summary, err := Run(context.Background(), Config{
			Transport:  backend,
			Enumerator: fixedEnumerator{files: []TranscriptFile{{Path: path, Source: "claude", Session: "a"}}},
			Parse:      offsetsParse,
			Watermarks: wm,
		})
		require.NoError(t, err)
		assert.NotEmpty(t, summary.Skipped, "Summary marks the batch skipped")
		assert.Equal(t, 0, backend.presignCalls, "consent off → zero presign calls")
		_, statErr := os.Stat(wmPath)
		assert.True(t, os.IsNotExist(statErr), "no watermark written when consent is off")
	})

	t.Run("fetch error is skip-and-retry", func(t *testing.T) {
		backend := newFakeTranscriptBackend(t)
		backend.consentErr = true
		wm, wmPath := newTempWatermarkStore(t)
		dir := t.TempDir()
		path := writeOffsetsFile(t, dir, "a.jsonl", 1, 2, 3)

		_, err := Run(context.Background(), Config{
			Transport:  backend,
			Enumerator: fixedEnumerator{files: []TranscriptFile{{Path: path, Source: "claude", Session: "a"}}},
			Parse:      offsetsParse,
			Watermarks: wm,
		})
		require.Error(t, err, "a consent fetch error surfaces for skip-and-retry")
		assert.Equal(t, 0, backend.presignCalls, "consent fetch error → zero presign calls")
		_, statErr := os.Stat(wmPath)
		assert.True(t, os.IsNotExist(statErr), "the WatermarkStore is never written on a consent fetch error")
	})
}

// TestRun_TwoFileCorpus_PerFileIsolation is the core orchestration test: two fresh
// sessions A and B ship over the fake transport + httptest GCS; B's confirm is forced
// to fail. A ships ONE parquet object carrying its (source,session) identity and its
// watermark advances to the live {Size,Mtime}; B's failure leaves B unadvanced and
// does NOT abort A (per-file isolation).
func TestRun_TwoFileCorpus_PerFileIsolation(t *testing.T) {
	dir := t.TempDir()

	pathA := writeOffsetsFile(t, dir, "A.jsonl", 100, 200, 300, 400)
	statA, err := os.Stat(pathA)
	require.NoError(t, err)
	pathB := writeOffsetsFile(t, dir, "B.jsonl", 10, 20, 30)

	wm, _ := newTempWatermarkStore(t)
	backend := newFakeTranscriptBackend(t)
	backend.consentEnabledFlag = true
	backend.failConfirmSession = "B-sess" // file B fails; A must still succeed.

	summary, err := Run(context.Background(), Config{
		Transport: backend,
		Enumerator: fixedEnumerator{files: []TranscriptFile{
			{Path: pathA, Source: "claude", Session: "A-sess"},
			{Path: pathB, Source: "codex", Session: "B-sess"},
		}},
		Parse:          offsetsParse,
		Watermarks:     wm,
		MaxConcurrency: 2,
	})

	require.Error(t, err, "file B's confirm failure is joined into the batch error")
	assert.Equal(t, 2, summary.FilesScanned)
	assert.Equal(t, 1, summary.FilesUploaded, "only file A uploaded")

	// File A: exactly one parquet object confirmed, carrying its (source,session).
	aConfirms := backend.confirmsForSession("A-sess")
	require.Len(t, aConfirms, 1, "A shipped exactly one parquet object per session")
	assert.Equal(t, "claude", aConfirms[0].Source)
	assert.Equal(t, "A-sess", aConfirms[0].Session)

	// File A's watermark advanced to the live size/mtime.
	wA, okA := wm.Lookup("claude:A-sess")
	require.True(t, okA)
	assert.Equal(t, statA.Size(), wA.Size, "A records the live size")
	assert.Equal(t, statA.ModTime().UnixNano(), wA.Mtime, "A records the live mod time")

	// File B: attempted its object but its watermark NEVER advanced (confirm failed).
	bConfirms := backend.confirmsForSession("B-sess")
	require.Len(t, bConfirms, 1, "B attempted its object before failing")
	_, okB := wm.Lookup("codex:B-sess")
	assert.False(t, okB, "B's watermark is unadvanced after its confirm failure")
}

// TestRun_UnchangedFileRerunShipsNothing asserts the whole-session incremental
// property: after a successful Run, re-running over a byte-identical file (same
// size + mtime) makes ZERO presign / PUT calls.
func TestRun_UnchangedFileRerunShipsNothing(t *testing.T) {
	dir := t.TempDir()
	path := writeOffsetsFile(t, dir, "a.jsonl", 100, 200, 300)
	wm, _ := newTempWatermarkStore(t)
	backend := newFakeTranscriptBackend(t)
	backend.consentEnabledFlag = true

	cfg := Config{
		Transport:  backend,
		Enumerator: fixedEnumerator{files: []TranscriptFile{{Path: path, Source: "claude", Session: "a"}}},
		Parse:      offsetsParse,
		Watermarks: wm,
	}

	first, err := Run(context.Background(), cfg)
	require.NoError(t, err)
	require.Equal(t, 1, first.FilesUploaded, "first run ships the file")
	presignAfterFirst := backend.presignCalls
	objectsAfterFirst := backend.putObjectCount()
	require.Positive(t, presignAfterFirst)

	second, err := Run(context.Background(), cfg)
	require.NoError(t, err)
	assert.Equal(t, 0, second.FilesUploaded, "second run ships nothing (unchanged file)")
	assert.Equal(t, presignAfterFirst, backend.presignCalls, "zero additional presign calls on the unchanged re-run")
	assert.Equal(t, objectsAfterFirst, backend.putObjectCount(), "zero additional GCS PUTs on the unchanged re-run")
}

// TestRun_ChangedSessionReuploadsOnlyThatSession is the incremental criterion: after a
// successful Run over two sessions, changing ONE session's file (a size change) makes
// only THAT session re-upload on the next Run; the unchanged sibling ships nothing.
func TestRun_ChangedSessionReuploadsOnlyThatSession(t *testing.T) {
	dir := t.TempDir()
	pathA := writeOffsetsFile(t, dir, "A.jsonl", 1, 2)
	pathB := writeOffsetsFile(t, dir, "B.jsonl", 3, 4)
	wm, _ := newTempWatermarkStore(t)
	backend := newFakeTranscriptBackend(t)
	backend.consentEnabledFlag = true
	cfg := Config{
		Transport: backend,
		Enumerator: fixedEnumerator{files: []TranscriptFile{
			{Path: pathA, Source: "claude", Session: "A"},
			{Path: pathB, Source: "claude", Session: "B"},
		}},
		Parse:      offsetsParse,
		Watermarks: wm,
	}

	first, err := Run(context.Background(), cfg)
	require.NoError(t, err)
	require.Equal(t, 2, first.FilesUploaded, "first run ships both sessions")

	// Change ONLY session A (a larger file → size differs from its watermark), then
	// backdate its mtime past the quiet window so the debounce treats it as idle and
	// ships it this tick — a freshly-modified changed session would instead defer. B is
	// byte-identical to what it last shipped.
	changedA := writeOffsetsFile(t, dir, "A.jsonl", 1, 2, 5, 6)
	idle := time.Now().Add(-2 * transcriptQuietWindow)
	require.NoError(t, os.Chtimes(changedA, idle, idle))

	second, err := Run(context.Background(), cfg)
	require.NoError(t, err)
	assert.Equal(t, 1, second.FilesUploaded, "only the changed session A re-uploads")

	// A confirmed in BOTH runs; B only in the first (unchanged → skipped the second).
	assert.Len(t, backend.confirmsForSession("A"), 2, "A shipped an object in both runs")
	assert.Len(t, backend.confirmsForSession("B"), 1, "B shipped only in the first run (unchanged since)")
}

// TestPrepareFile_RawSizeCapSkipsBeforeParse proves the pre-parse raw-size cap: a
// session file whose raw on-disk size exceeds maxRawTranscriptBytes is skipped BEFORE
// parse — prepareFile returns a non-nil over-cap error with zero emitted rows and an
// unadvanced watermark, the injected Parse seam is never invoked (so the full row slice
// is never materialized), and no temp parquet is written. Mirrors the post-parse
// maxClientParquetBytes over-cap test with the cap lowered so no multi-GB fixture is
// needed.
func TestPrepareFile_RawSizeCapSkipsBeforeParse(t *testing.T) {
	prevCap := maxRawTranscriptBytes
	maxRawTranscriptBytes = 8 // 8 bytes: the fixture below (16 bytes) exceeds it.
	t.Cleanup(func() { maxRawTranscriptBytes = prevCap })

	// Route any temp parquet into an isolated dir so we can assert none was written.
	tmpParquet := withTempParquetDir(t)

	dir := t.TempDir()
	fixture := writeOffsetsFile(t, dir, "big.jsonl", 1, 2, 3, 4, 5, 6, 7, 8) // well over 16 bytes.

	parseCalled := false
	cfg := Config{
		Parse: func(source string, r io.Reader) ([]transcripts.Row, error) {
			parseCalled = true
			return offsetsParse(source, r)
		},
	}
	plan, err := prepareFile(cfg, TranscriptFile{Path: fixture, Source: "claude", Session: "big-sess"})

	require.Error(t, err, "an over-cap raw file returns a per-file error")
	assert.Contains(t, err.Error(), "exceeds", "the error names the cap violation")
	assert.Equal(t, 0, plan.emitted, "no rows emitted — parse never ran")
	assert.False(t, parseCalled, "the cap skips BEFORE parse — the parser is never invoked")
	assert.Equal(t, Watermark{}, plan.advanced, "the watermark is not advanced for a skipped file")

	entries, rerr := os.ReadDir(tmpParquet)
	require.NoError(t, rerr)
	assert.Empty(t, entries, "no temp parquet was written (parse/convert never ran)")
}
