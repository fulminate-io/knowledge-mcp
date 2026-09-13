// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// worker_summary_split_log_test.go — requirement 5's observable. The assertions
// are over the log RECORD and its attributes, not over a formatted line, because
// a substring assertion on a rendered line passes for a line that happens to
// contain the number somewhere and cannot tell an attribute from a message.

// recordingHandler collects every slog.Record it is given, with its attributes
// flattened into a map. It is a slog.Handler rather than a buffer so a test can
// read the attrs the producer set instead of re-parsing a render.
type recordingHandler struct {
	mu      sync.Mutex
	records []loggedRecord
}

// loggedRecord is one captured record: its level, its message, and its
// attributes as a map from key to the value's rendered form.
type loggedRecord struct {
	Level slog.Level
	Msg   string
	Attrs map[string]string
}

func (h *recordingHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *recordingHandler) Handle(_ context.Context, r slog.Record) error {
	rec := loggedRecord{Level: r.Level, Msg: r.Message, Attrs: make(map[string]string, r.NumAttrs())}
	r.Attrs(func(a slog.Attr) bool {
		rec.Attrs[a.Key] = a.Value.String()
		return true
	})
	h.mu.Lock()
	h.records = append(h.records, rec)
	h.mu.Unlock()
	return nil
}

func (h *recordingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *recordingHandler) WithGroup(string) slog.Handler      { return h }

// find returns the records whose message contains want, at or above minLevel.
func (h *recordingHandler) find(minLevel slog.Level, want string) []loggedRecord {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []loggedRecord
	for _, r := range h.records {
		if r.Level >= minLevel && strings.Contains(r.Msg, want) {
			out = append(out, r)
		}
	}
	return out
}

// captureRecords installs a record-collecting default logger for the test.
func captureRecords(t *testing.T) *recordingHandler {
	t.Helper()
	h := &recordingHandler{}
	prior := slog.Default()
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(prior) })
	return h
}

// TestSummarySplit_LogsPerDocumentSizesAndTheLimit is T20: on a size refusal the
// worker logs, at WARN or above, the per-document composed sizes of the batch and
// the limit the provider reported. Without it an operator has a summary-fail
// count and no way to learn WHICH document is too large or by how much — the
// position this ticket's investigation started from.
//
// Every expected size is computed by this test from its own fixture.
func TestSummarySplit_LogsPerDocumentSizesAndTheLimit(t *testing.T) {
	h := captureRecords(t)
	ctx := context.Background()
	const (
		oversizeChars = 5000
		smallChars    = 100
		budget        = 2000
		smallCount    = 19
	)
	work := append(
		sizedSummaryFixture(0, 0, oversizeChars),
		sizedSummaryFixture(smallCount, smallChars, 0)...,
	)
	ss := &strideSummarizer{charBudget: budget}
	p := New(Config{}, newFakeWireClient(), ss.call, nil)

	drainSummaryThroughDispatcher(ctx, p, work, len(work))

	recs := h.find(slog.LevelWarn, "refused the batch as too large")
	require.NotEmpty(t, recs, "the size refusal must be logged at WARN or above; records seen: %d", len(h.records))
	rec := recs[0]
	t.Logf("level=%v msg=%q attrs=%v", rec.Level, rec.Msg, rec.Attrs)

	assert.Equal(t, "20", rec.Attrs["documents"], "the record must name how many documents the refused call carried")
	assert.Equal(t, "2000", rec.Attrs["provider_limit"], "the record must name the limit the provider reported")
	assert.Equal(t, "pkg/huge.go", rec.Attrs["largest_document"], "the record must name the largest document")
	assert.Equal(t, "5000", rec.Attrs["largest_document_chars"], "the record must name the largest document's composed size")
	assert.Equal(t, "6900", rec.Attrs["composed_chars_total"],
		"the record must name the batch total: one document of %d plus %d of %d", oversizeChars, smallCount, smallChars)

	// The PER-DOCUMENT sizes, largest first, so the answer to "which one" is the
	// first entry rather than a scan of a batch-ordered list.
	perDoc := rec.Attrs["document_chars"]
	assert.Contains(t, perDoc, "pkg/huge.go=5000", "the record must carry the oversize document's own size")
	assert.Contains(t, perDoc, "pkg/small.go:S0=100", "the record must carry every document's size, not only the largest")
	assert.Less(t, strings.Index(perDoc, "pkg/huge.go=5000"), strings.Index(perDoc, "pkg/small.go:S0=100"),
		"the per-document sizes must be ordered largest first: %s", perDoc)

	// The terminal mark names the size and the limit too, at Error level, because
	// it is the durable outcome an operator triages from.
	marks := h.find(slog.LevelError, "too large for the provider")
	require.Len(t, marks, 1, "the lone oversize document's terminal mark must be logged once")
	assert.Equal(t, "5000", marks[0].Attrs["composed_chars"])
	assert.Equal(t, "2000", marks[0].Attrs["provider_limit"])
	assert.Equal(t, "pkg/huge.go", marks[0].Attrs["node_id"])
}

// TestSummarySplit_LogsUnknownLimitAsUnknown is T21: when the transport reported
// no limit, the record still names the sizes and says the limit is UNKNOWN. A
// zero rendered as a limit is the defect this row pins — it reads as "the
// provider accepts nothing", which is the opposite of what was observed, and it
// would send an operator looking for a broken provider instead of a big document.
func TestSummarySplit_LogsUnknownLimitAsUnknown(t *testing.T) {
	h := captureRecords(t)
	ctx := context.Background()
	work := sizedSummaryFixture(20, 100, 0)
	ss := &strideSummarizer{charBudget: 550, hideLimit: true}
	p := New(Config{}, newFakeWireClient(), ss.call, nil)

	drainSummaryThroughDispatcher(ctx, p, work, len(work))

	recs := h.find(slog.LevelWarn, "refused the batch as too large")
	require.NotEmpty(t, recs, "the refusal must still be logged when no limit was reported")
	rec := recs[0]
	t.Logf("level=%v attrs=%v", rec.Level, rec.Attrs)

	assert.Equal(t, "unknown", rec.Attrs["provider_limit"],
		"an unreported limit must render as unknown, never as 0")
	assert.NotEqual(t, "0", rec.Attrs["provider_limit"], "0 would read as a real bound of zero")
	assert.Equal(t, "2000", rec.Attrs["composed_chars_total"], "the sizes must still be named without a limit")
	assert.Contains(t, rec.Attrs["document_chars"], "=100", "the per-document sizes must still be named")
}

// TestSummarySplit_FailedMarkerWriteIsReportedNotSwallowed is T29: the marker
// write is best-effort, so a FAILED one must be reported and must not present
// itself as a durable terminal mark. A silently dropped marker returns the node
// to the queue on the next scan — the ticket's own failure mode — so the WARN is
// the only record that the node was not marked.
func TestSummarySplit_FailedMarkerWriteIsReportedNotSwallowed(t *testing.T) {
	h := captureRecords(t)
	ctx := context.Background()
	work := sizedSummaryFixture(0, 0, 5000)
	wc := newFakeWireClient()
	wc.mutateErr = errWireDown
	ss := &strideSummarizer{charBudget: 2000}
	p := New(Config{}, wc, ss.call, nil)

	drainSummaryThroughDispatcher(ctx, p, work, len(work))

	// CONTROL: the write really was attempted, so the assertions below are not
	// passing on a path that never ran.
	require.Equal(t, 1, wc.mutateCallCount(), "control: the marker write must have been attempted")
	_, markers := splitWrites(wc)
	assert.Empty(t, markers, "a failed write stores nothing — the node is NOT durably marked")

	recs := h.find(slog.LevelWarn, "write failure markers failed")
	require.NotEmpty(t, recs, "a dropped marker must be logged at WARN or above; records seen: %d", len(h.records))
	t.Logf("msg=%q attrs=%v", recs[0].Msg, recs[0].Attrs)
	assert.Equal(t, "1", recs[0].Attrs["items"], "the record must name how many nodes went unmarked")
	assert.NotEmpty(t, recs[0].Attrs["error"], "the record must carry the write error")
	assert.Contains(t, recs[0].Msg, "NOT durably marked",
		"the message must say the nodes were not marked, so a reader cannot take the line for a successful mark")
}

// errWireDown is a sentinel write failure for the best-effort marker path.
var errWireDown = errors.New("wire client unavailable")

// TestSummarySplit_AbandonedSplitIsReportedAtWarn is the observability half of
// T3. A cancelled split drops the documents in its remaining sub-batches: they
// stay summary-eligible and the next scan re-discovers them, so nothing is lost
// — but the work WAS dropped, and a dropped-work line logged at Debug is
// invisible at the verbosity the daemon runs at. The record must name how many
// documents were abandoned, so the line is worth something beyond its existence.
func TestSummarySplit_AbandonedSplitIsReportedAtWarn(t *testing.T) {
	h := captureRecords(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	work := sizedSummaryFixture(20, 100, 0)
	ss := &strideSummarizer{charBudget: 1000}
	ss.onCall = func(call int) {
		if call == 1 {
			cancel()
		}
	}
	p := New(Config{}, newFakeWireClient(), ss.call, nil)

	drainSummaryThroughDispatcher(ctx, p, work, len(work))

	recs := h.find(slog.LevelWarn, "oversize split abandoned")
	require.NotEmpty(t, recs,
		"the abandonment must be logged at WARN or above: at Debug the dropped work is reported nowhere the daemon prints; records seen: %d", len(h.records))
	t.Logf("level=%v attrs=%v", recs[0].Level, recs[0].Attrs)
	assert.Equal(t, "20", recs[0].Attrs["documents_abandoned"],
		"the record must name how many documents were dropped — the fixture's twenty, none of which reached a sub-batch")
	assert.Equal(t, "0", recs[0].Attrs["merged_so_far"], "nothing was summarized before the cancellation")
}

// TestSummarySplit_UnknownLimitMarkerSaysUnknown is the marker half of T21: a
// document terminally marked with no reported limit must say so on the node
// too, since that value is what an operator reads back by id.
func TestSummarySplit_UnknownLimitMarkerSaysUnknown(t *testing.T) {
	ctx := context.Background()
	// One document, refused with no limit reported: the base case marks it.
	work := sizedSummaryFixture(0, 0, 5000)
	require.Len(t, work, 1, "fixture: a single document")
	wc := newFakeWireClient()
	ss := &strideSummarizer{charBudget: 2000, hideLimit: true}
	p := New(Config{}, wc, ss.call, nil)

	drainSummaryThroughDispatcher(ctx, p, work, len(work))

	_, markers := splitWrites(wc)
	require.Len(t, markers, 1, "the single refused document must be marked terminally")
	for key, value := range markers[0].Metadata {
		t.Logf("%s=%q", key, value)
		assert.Contains(t, value, "unknown", "%s must say the limit is unknown rather than render a zero", key)
		assert.NotContains(t, value, "limit 0", "%s must not render a zero limit", key)
		assert.NotContains(t, value, "limit=0", "%s must not render a zero limit", key)
	}
}
