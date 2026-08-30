// SPDX-License-Identifier: Apache-2.0

package transcriptsync

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/transcripts"
)

// TestShouldReupload is the whole-session-reupload decision guard: a file re-uploads
// when there is no watermark, --seed is set, the size changed, or the size is
// unchanged but the mtime differs (an equal-size in-place rewrite — the T2.2 mtime
// guard); an unchanged size+mtime skips. A CHANGED but still-live session is DEBOUNCED
// — it defers while recently modified and recently shipped, ships once idle for the
// quiet window, and force-ships once its last ship is older than the max defer age (a
// hard eventual-ship bound so an active session is never starved). The forced/changed
// existing cases pass a `now` well past both windows so they still ship as before.
// Replaces the old generation/offset computeUploadPlan test.
func TestShouldReupload(t *testing.T) {
	w := Watermark{Size: 8192, Mtime: time.Unix(0, 2_000_000).UnixNano()}
	// now sits well past both the quiet window and the max defer age relative to the
	// tiny fixture mtimes, so the forced/changed cases below ship exactly as they did
	// before the debounce was added.
	now := time.Unix(0, 2_000_000).Add(24 * time.Hour)

	t.Run("no watermark → reupload", func(t *testing.T) {
		st := fakeStat{size: 8192, mod: time.Unix(0, 2_000_000)}
		assert.True(t, shouldReupload(st, Watermark{}, false, false, 0, now))
	})

	t.Run("seed flag → reupload", func(t *testing.T) {
		st := fakeStat{size: 8192, mod: time.Unix(0, 2_000_000)}
		assert.True(t, shouldReupload(st, w, true, true, 0, now))
	})

	t.Run("truncation (size <) → reupload", func(t *testing.T) {
		st := fakeStat{size: 4096, mod: time.Unix(0, 2_000_000)}
		assert.True(t, shouldReupload(st, w, true, false, 0, now))
	})

	t.Run("growth (size >) → reupload", func(t *testing.T) {
		st := fakeStat{size: 16384, mod: time.Unix(0, 2_000_000)}
		assert.True(t, shouldReupload(st, w, true, false, 0, now))
	})

	t.Run("equal-size in-place rewrite (size == && mtime !=) → reupload", func(t *testing.T) {
		st := fakeStat{size: 8192, mod: time.Unix(0, 9_999_999)}
		assert.True(t, shouldReupload(st, w, true, false, 0, now))
	})

	t.Run("unchanged (size == && mtime ==) → skip", func(t *testing.T) {
		st := fakeStat{size: 8192, mod: time.Unix(0, 2_000_000)}
		assert.False(t, shouldReupload(st, w, true, false, 0, now))
	})

	// Debounce subtests: a fixed, realistic reference time with the fixture mtimes
	// expressed relative to it, so the quiet-window and max-defer-age boundaries are
	// exercised meaningfully.
	ref := time.Unix(1_700_000_000, 0)

	t.Run("changed + recently modified + recent last-ship → defer", func(t *testing.T) {
		// Modified 5m ago (< 15m quiet window) and last shipped 30m ago (< 6h max defer
		// age): still live and recently shipped, so hold off.
		st := fakeStat{size: 16384, mod: ref.Add(-5 * time.Minute)}
		wm := Watermark{Size: 8192, Mtime: ref.Add(-30 * time.Minute).UnixNano()}
		assert.False(t, shouldReupload(st, wm, true, false, 0, ref))
	})

	t.Run("changed + idle beyond quiet window → ship", func(t *testing.T) {
		// Idle for 20m (>= 15m quiet window): the session went quiet, ship it now.
		st := fakeStat{size: 16384, mod: ref.Add(-20 * time.Minute)}
		wm := Watermark{Size: 8192, Mtime: ref.Add(-30 * time.Minute).UnixNano()}
		assert.True(t, shouldReupload(st, wm, true, false, 0, ref))
	})

	t.Run("changed + active but last-ship older than max defer age → force-ship", func(t *testing.T) {
		// Modified 5m ago (still active, quiet window not met) but last shipped 7h ago
		// (>= 6h max defer age): hard eventual-ship so an active session is not starved.
		st := fakeStat{size: 16384, mod: ref.Add(-5 * time.Minute)}
		wm := Watermark{Size: 8192, Mtime: ref.Add(-7 * time.Hour).UnixNano()}
		assert.True(t, shouldReupload(st, wm, true, false, 0, ref))
	})
}

// TestShouldReupload_RollupVersion proves the schema-bump backfill: a session whose
// Size/Mtime EXACTLY match its watermark still re-ships when its stored RollupSchemaVersion
// is below the current one (forced-reship short-circuit ahead of the debounce), and skips
// when the stored version equals current — so a same-version unchanged session still
// short-circuits.
func TestShouldReupload_RollupVersion(t *testing.T) {
	// now sits far past both debounce windows, so ONLY the version gate distinguishes the
	// two cases (Size/Mtime are identical to the live stat in both).
	now := time.Unix(0, 2_000_000).Add(24 * time.Hour)
	st := fakeStat{size: 8192, mod: time.Unix(0, 2_000_000)}

	t.Run("stored version < current → reupload despite identical size/mtime", func(t *testing.T) {
		wm := Watermark{Size: 8192, Mtime: time.Unix(0, 2_000_000).UnixNano(), RollupSchemaVersion: 0}
		assert.True(t, shouldReupload(st, wm, true, false, 1, now),
			"a schema bump forces a re-derive + re-ship of an otherwise-unchanged session")
	})

	t.Run("stored version == current + matching size/mtime → skip", func(t *testing.T) {
		wm := Watermark{Size: 8192, Mtime: time.Unix(0, 2_000_000).UnixNano(), RollupSchemaVersion: 1}
		assert.False(t, shouldReupload(st, wm, true, false, 1, now),
			"a same-version unchanged session short-circuits")
	})

	// The two subtests above pin the MECHANISM against hardcoded versions and stay valid
	// whatever the live constant is. The two below tie the same property to the LIVE
	// rollupSchemaVersion, spelled version-agnostically so the next bump inherits the gate
	// rather than silently outgrowing it.
	t.Run("live constant: a previous-version watermark re-ships", func(t *testing.T) {
		wm := Watermark{
			Size: 8192, Mtime: time.Unix(0, 2_000_000).UnixNano(),
			RollupSchemaVersion: rollupSchemaVersion - 1,
		}
		assert.True(t, shouldReupload(st, wm, true, false, rollupSchemaVersion, now),
			"a session watermarked at the previous schema version re-derives and re-ships whole")
	})

	t.Run("live constant: a current-version watermark still short-circuits", func(t *testing.T) {
		wm := Watermark{
			Size: 8192, Mtime: time.Unix(0, 2_000_000).UnixNano(),
			RollupSchemaVersion: rollupSchemaVersion,
		}
		assert.False(t, shouldReupload(st, wm, true, false, rollupSchemaVersion, now),
			"the bump does not turn into a permanent re-ship loop for unchanged sessions")
	})
}

// TestWriteSessionTempParquet_WritesAndCleans proves the temp-parquet helper writes a
// non-empty parquet whose size is reported, and (the criterion-g prepare-path owner)
// that a caller removing the temp leaves the temp dir clean.
func TestWriteSessionTempParquet_WritesAndCleans(t *testing.T) {
	rows := []transcripts.Row{
		{Source: transcripts.SourceClaude, SessionID: "s", RecordTS: time.Unix(1, 0).UTC(), InputTokens: 10},
		{Source: transcripts.SourceClaude, SessionID: "s", RecordTS: time.Unix(2, 0).UTC(), OutputTokens: 20},
	}
	obj, err := writeSessionTempParquet(rows)
	require.NoError(t, err)
	require.NotEmpty(t, obj.path)
	info, statErr := os.Stat(obj.path)
	require.NoError(t, statErr, "the temp parquet exists on disk")
	assert.Equal(t, info.Size(), obj.size, "reported size matches the on-disk file")
	assert.Positive(t, obj.size, "the parquet is non-empty")

	require.NoError(t, os.Remove(obj.path))
	_, statErr = os.Stat(obj.path)
	assert.True(t, os.IsNotExist(statErr), "the temp is gone after removal")
}

// TestParseTranscript_CodexWholeFileCarriesSessionAndModel is the whole-file parse
// regression guard for the ParseTranscript seam prepareFile drives: a Codex token
// row depends on session_meta (line 1) and a model set by an EARLIER turn_context, so
// parsing the WHOLE file (never a windowed tail) is what lets a later row carry the
// non-empty session id + model. Depends on the stateful Codex parser.
func TestParseTranscript_CodexWholeFileCarriesSessionAndModel(t *testing.T) {
	lines := []string{
		`{"timestamp":"2026-06-01T00:00:00Z","type":"session_meta","payload":{"id":"codex-sess-1","cwd":"/work","cli_version":"1.2.3","git":{"branch":"main"}}}`,
		`{"timestamp":"2026-06-01T00:00:01Z","type":"turn_context","payload":{"model":"gpt-5-codex"}}`,
		`{"timestamp":"2026-06-01T00:00:02Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":100,"output_tokens":50}}}}`,
		`{"timestamp":"2026-06-01T00:00:03Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":200,"output_tokens":80}}}}`,
	}
	raw := []byte(strings.Join(lines, "\n") + "\n")

	rows, err := ParseTranscript(string(transcripts.SourceCodex), bytes.NewReader(raw))
	require.NoError(t, err)
	require.Len(t, rows, 2, "two token_count rows")
	for _, r := range rows {
		assert.Equal(t, "codex-sess-1", r.SessionID,
			"whole-file parse carried the line-1 session_meta id onto every row")
		assert.Equal(t, "gpt-5-codex", r.Model,
			"whole-file parse carried the earlier turn_context model onto every row")
	}
}

// fakeStat is a minimal os.FileInfo for shouldReupload tests (only Size + ModTime are
// read).
type fakeStat struct {
	size int64
	mod  time.Time
}

func (f fakeStat) Name() string       { return "fixture.jsonl" }
func (f fakeStat) Size() int64        { return f.size }
func (f fakeStat) Mode() os.FileMode  { return 0o644 }
func (f fakeStat) ModTime() time.Time { return f.mod }
func (f fakeStat) IsDir() bool        { return false }
func (f fakeStat) Sys() any           { return nil }

// compile-time guard that fakeStat satisfies os.FileInfo.
var _ os.FileInfo = fakeStat{}
