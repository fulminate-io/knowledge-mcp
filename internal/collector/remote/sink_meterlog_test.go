// SPDX-License-Identifier: Apache-2.0

package remote

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordingHandler captures structured slog RECORDS rather than formatted
// output. That distinction is load-bearing: against a bytes.Buffer of rendered
// text, an implementation that logged the meter reading into the WRONG record —
// the "all chunks uploaded" summary, say — would still satisfy a substring
// search, and the gate would pass on wrong wiring. Asserting an attribute on a
// NAMED record is what pins each reading to the message it belongs to, and it
// is the only way the exactly-one-record and the Info-level assertions below
// can be expressed at all.
type recordingHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *recordingHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *recordingHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r.Clone())
	return nil
}

func (h *recordingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *recordingHandler) WithGroup(string) slog.Handler      { return h }

// withMessage returns every captured record carrying exactly this message.
func (h *recordingHandler) withMessage(msg string) []slog.Record {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []slog.Record
	for _, r := range h.records {
		if r.Message == msg {
			out = append(out, r)
		}
	}
	return out
}

// attr returns the value of key on rec, and whether it was present.
func attr(rec slog.Record, key string) (slog.Value, bool) {
	var (
		val   slog.Value
		found bool
	)
	rec.Attrs(func(a slog.Attr) bool {
		if a.Key == key {
			val, found = a.Value, true
			return false
		}
		return true
	})
	return val, found
}

// installRecorder swaps the default logger for a recorder for the duration of
// one test and restores it afterwards.
func installRecorder(t *testing.T) *recordingHandler {
	t.Helper()
	h := &recordingHandler{}
	prev := slog.Default()
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return h
}

// TestChunkLog_MeterDeltaOnSendAndFailure asserts the per-chunk meter reading
// reaches the log on BOTH paths. The failure leg is the one that matters most:
// before this change a chunk that exhausted its budget returned with no meter
// reading at all, which is precisely the event the instrument exists to explain.
func TestChunkLog_MeterDeltaOnSendAndFailure(t *testing.T) {
	t.Run("success carries the delta on the chunk-sent record", func(t *testing.T) {
		rec := installRecorder(t)
		sink := NewUploadSink(startScriptedIngest(t, &scriptedIngest{}))

		require.NoError(t, sink.WriteResult(context.Background(), "", oneChunkResult("meter-success-repo")))

		sent := rec.withMessage("remote sink: chunk sent")
		require.Len(t, sent, 1, "exactly one chunk was sent, so exactly one chunk-sent record")
		for _, key := range []string{"in_write_ms", "socket_writes", "socket_bytes"} {
			_, ok := attr(sent[0], key)
			assert.True(t, ok, "the chunk-sent record must carry %q", key)
		}
	})

	t.Run("failure does not lose the delta", func(t *testing.T) {
		rec := installRecorder(t)
		eng := &scriptedIngest{
			onCollectChunk: func(int32) error {
				// Non-retryable AND non-ambiguous: one attempt, then the failure
				// path, with no backoff sleep in the way.
				return connect.NewError(connect.CodeInvalidArgument, errors.New("bad chunk"))
			},
		}
		sink := NewUploadSink(startScriptedIngest(t, eng))

		err := sink.WriteResult(context.Background(), "", oneChunkResult("meter-failure-repo"))
		require.Error(t, err, "the chunk genuinely failed")

		failed := rec.withMessage("remote sink: chunk failed")
		require.Len(t, failed, 1, "the failed chunk must produce exactly one reading record")
		v, ok := attr(failed[0], "in_write_ms")
		require.True(t, ok, "the failure path must carry in_write_ms — this is the case the meter exists for")
		assert.NotNil(t, v)

		// Known-positive control on the same probe: the success message is
		// genuinely ABSENT here rather than the matcher being broken, and the
		// matcher demonstrably finds it in the sibling sub-test above.
		assert.Empty(t, rec.withMessage("remote sink: chunk sent"),
			"a failed chunk emits no chunk-sent record")
	})
}

// TestClientSideStallLog_EmitsLoudLine drives the emission helper directly with
// the MEASURED stall pair and pins what an operator actually receives. Exactly
// one record matters: a helper that logs twice, or that logs at Debug where the
// daemon's default level would swallow it, is a real defect and both are shapes
// a substring search over rendered output would miss.
func TestClientSideStallLog_EmitsLoudLine(t *testing.T) {
	rec := installRecorder(t)

	// MEASURED pair: 15.3s elapsed, 6.3ms inside Write — the reproduced
	// client-side stall.
	logClientSideStall(3, 7, 4*1024*1024, 15300*time.Millisecond, 6300*time.Microsecond, 520)

	rec.mu.Lock()
	all := append([]slog.Record(nil), rec.records...)
	rec.mu.Unlock()
	require.Len(t, all, 1, "the helper emits EXACTLY ONE record — never two, never zero")

	got := all[0]
	assert.Equal(t, slog.LevelInfo, got.Level,
		"INFO: nothing is broken when this fires, and WARN would train the operator to ignore it")

	for _, key := range []string{"i", "of", "bytes", "dur", "in_write_ms", "socket_writes"} {
		_, ok := attr(got, key)
		assert.True(t, ok, "the loud line must carry %q", key)
	}

	v, ok := attr(got, "i")
	require.True(t, ok)
	assert.Equal(t, int64(3), v.Int64(), "the chunk index is the one the caller passed")

	// The operator's next step must be nameable from the line itself.
	next, ok := attr(got, "next")
	require.True(t, ok, "the loud line must name the operator's next step")
	assert.Contains(t, next.String(), "GODEBUG=http2debug=2",
		"frame detail on the next occurrence is the substitute for in-process flow-control headroom")
}
