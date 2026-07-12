// SPDX-License-Identifier: Apache-2.0

package transcriptsync

import (
	"fmt"
	"os"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/transcripts"
)

// maxClientParquetBytes caps the size of a single session's converted parquet the
// client will ship. It MIRRORS the server's maxTranscriptPlaintextBytes cap (agent
// sync_confirm.go, Phase 4): a session whose parquet exceeds it is SKIPPED
// client-side (no seal/PUT/confirm, no wasted GCS round-trip, watermark unadvanced)
// so the pathological object never reaches the server 413 — which stays as
// defense-in-depth. 128 MiB. A package var only so a test can lower it to exercise
// the over-cap skip without a 128 MiB fixture.
//
//nolint:gochecknoglobals // test-overridable size guard; mirrors the exec-seam idiom.
var maxClientParquetBytes int64 = 128 << 20

// maxRawTranscriptBytes caps the RAW on-disk size of a session file the client will
// parse. A file above it is skipped BEFORE parse (prepareFile), so a pathological
// multi-GB session never materializes its full row slice in RAM and cannot OOM the
// daemon. 512 MiB is generous headroom over any realistic session while still catching
// the GB+ outlier.
//
// MEMORY TRUTH: this bounds the PER-FILE worst case, NOT aggregate resident memory —
// the engine runs up to NumCPU parallel producers, so the pathological headroom is
// ~NumCPU * 512 MiB across concurrent producers (the concurrency knob is the aggregate
// bound). It fixes the unbounded-SINGLE-SESSION OOM, which is the identified risk.
//
// OVER-CAP STEADY STATE: an over-cap file's watermark never advances, so it loud-warns
// and per-file-fails on EVERY tick, forever, until it drops below the cap or is
// deleted. That durable re-failure is what the upload-health counters surface on the
// daemon status. The cap only rejects the pathological outlier — it keeps the full
// parsed-row model intact for every in-bounds session, so downstream per-session
// rollup compute is unaffected. A package var only so a test can lower it to exercise
// the over-cap skip without a multi-GB fixture.
//
//nolint:gochecknoglobals // test-overridable size guard; mirrors maxClientParquetBytes.
var maxRawTranscriptBytes int64 = 512 << 20

// tempParquetDir is the directory session parquets are written to. Empty (the
// production default) means os.TempDir(); a test overrides it to a t.TempDir() so it
// can assert NO temp parquet leaked (dry-run / error / cancel paths).
//
//nolint:gochecknoglobals // test-only temp-dir seam; mirrors the maxClientParquetBytes seam.
var tempParquetDir string

// sessionObject is a converted session's on-disk parquet: the temp-file path the
// bounded pipeline ships from and the file's size (used for the 128MiB guard and
// the per-file summary). It replaces the old per-chunk NDJSON deltaChunk — one
// parquet object per session, held on DISK (not the heap) until seal time.
type sessionObject struct {
	path string
	size int64
}

// transcriptQuietWindow is the idle period a CHANGED session must go quiet for before
// the client re-seals and re-ships its whole parquet. Without it an actively-written
// session differs in Size/Mtime on every hourly tick and would be re-parsed,
// re-converted, re-sealed and re-PUT every hour while still live. A session ships on
// the first tick after it has been idle at least this long; the value is kept below
// the hourly upload cadence so a session that goes quiet still ships promptly.
//
// transcriptMaxDeferAge is the hard eventual-ship bound: a pathologically continuous
// session (never idle for a full quiet window) must not be starved forever, so it
// ships at least once per this interval. Both are fixed policy consts, documented and
// trivially tunable (mirrors the fixed-const idiom of the upload interval).
const (
	transcriptQuietWindow = 15 * time.Minute
	transcriptMaxDeferAge = 6 * time.Hour
)

// shouldReupload decides whether a session file needs a full re-upload and — for a
// changed but still-live session — whether to DEFER that re-upload until the session
// goes quiet. Ordered gates: (1) forced re-ship short-circuits when there is no
// watermark, --seed is set, OR the session's last shipped rollup schema version is below
// the current one (a schema bump forces a whole-session re-derive + re-ship — the
// backfill — and must bypass the quiet-window debounce; additional forced-reship
// conditions are simply added to this OR, ahead of the debounce); (2) a byte-identical
// file (Size AND Mtime both unchanged) is skipped entirely — the client never even parses
// it; (3) a CHANGED file is debounced — it ships once it has been idle for
// transcriptQuietWindow (now minus its live mtime), OR once its last shipped watermark is
// older than transcriptMaxDeferAge (a hard eventual-ship bound so a continuously-active
// session is never starved — w.Mtime freezes at the last SHIPPED mtime while the session
// defers, so now minus w.Mtime measures how long it has gone unshipped), otherwise it
// defers to a later tick. currentRollupVersion is the current rollupSchemaVersion; now is
// the current time (time.Now() in production; a fixed base in tests). Replaces the old
// computeUploadPlan/newGeneration generation-and-offset logic, which the parquet-object
// model retired.
func shouldReupload(stat os.FileInfo, w Watermark, hasWatermark, seed bool, currentRollupVersion int, now time.Time) bool {
	if !hasWatermark || seed || w.RollupSchemaVersion < currentRollupVersion {
		// Forced re-ship conditions short-circuit ahead of the debounce. A stored rollup
		// schema version below current forces a whole-session re-derive + re-ship, so a
		// stale-schema session must NOT be held back by the quiet-window defer.
		return true
	}
	if stat.Size() == w.Size && stat.ModTime().UnixNano() == w.Mtime {
		// Byte-identical to the last shipped session — nothing to ship.
		return false
	}
	// Changed session: debounce until it goes idle, with a hard eventual-ship bound.
	if now.Sub(stat.ModTime()) >= transcriptQuietWindow {
		return true // idle for the quiet window — ship on this tick.
	}
	if now.Sub(time.Unix(0, w.Mtime)) >= transcriptMaxDeferAge {
		return true // hard eventual-ship — never starve a continuously-active session.
	}
	return false // still live and recently shipped — defer.
}

// writeSessionTempParquet converts one session's rows to a bounded parquet in a temp
// file and returns its descriptor. The GenericWriter row-group-flushes so a huge
// session never buffers every row in RAM. On ANY failure after os.CreateTemp
// (write/flush/close), it os.Removes its own temp file before returning the error —
// the prepare-path leak owner (releaseBatchBudget only reaps objects that reached a
// shipped batch). The over-cap guard is applied by the caller (prepareFile), which
// removes the temp when the parquet exceeds maxClientParquetBytes.
func writeSessionTempParquet(rows []transcripts.Row) (sessionObject, error) {
	tmp, err := os.CreateTemp(tempParquetDir, "knowledge-transcript-*.parquet")
	if err != nil {
		return sessionObject{}, fmt.Errorf("transcriptsync: create temp parquet: %w", err)
	}
	path := tmp.Name()
	if werr := transcripts.WriteSessionParquet(rows, tmp); werr != nil {
		_ = tmp.Close()
		_ = os.Remove(path)
		return sessionObject{}, fmt.Errorf("transcriptsync: write session parquet: %w", werr)
	}
	if cerr := tmp.Close(); cerr != nil {
		_ = os.Remove(path)
		return sessionObject{}, fmt.Errorf("transcriptsync: close temp parquet: %w", cerr)
	}
	info, serr := os.Stat(path)
	if serr != nil {
		_ = os.Remove(path)
		return sessionObject{}, fmt.Errorf("transcriptsync: stat temp parquet: %w", serr)
	}
	return sessionObject{path: path, size: info.Size()}, nil
}
