// SPDX-License-Identifier: Apache-2.0

// segment_shutdown_deadline_test.go — the shutdown drain honors the stop deadline
// (GitHub issue #172, requirement R5).
//
// The sibling file asserts that the drain SHIPS what is queued. This one asserts
// the other half of the same contract: that it stops when the window closes. At
// v0.10.5 the rebuild chain below drainSegmentBacklog carried no context at all,
// so the deadline was observed only BETWEEN graphs — an in-flight partition
// rebuild ran to completion whatever the window said, and the reported daemon
// exited 32.9 seconds after SIGTERM for a three-second window.
//
// IT DRIVES THE REAL SHUTDOWN CLOSURE, drainOnShutdown, for the reason the sibling
// file states: a direct drainSegmentBacklog call would be satisfied by a drain
// that ships nothing on an actual SIGTERM.

package bootstrap

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/hnsw"
)

const (
	// THE FIXTURE STAGES A RE-EMIT THAT GENUINELY OUTLIVES daemonStopDeadline,
	// in-process and through the real hnsw builder, because a fixture finishing
	// inside the window would make every assertion here vacuous. The abandoned
	// graph's backlog is queued in lease-shaped batches and never drained — the
	// state a clean stop finds between reconcile ticks — so its rebuild has to emit
	// every partition of the corpus in one group swap.
	//
	// THE CONSTANTS ARE CALIBRATED, AND THE CALIBRATION IS RE-READ ON EVERY RUN
	// rather than trusted: the test prints the staged segment count, the rebuild's
	// own elapsed_ms and the drain's wall time, and the assertions below are only
	// meaningful while that elapsed_ms exceeds the window. Measured at b19ea8973 on
	// the author's machine: 4,950 resident segments, a 7,594 ms rebuild, against a
	// 3,000 ms window.
	shutdownDeadlineBatches   = 60
	shutdownDeadlineBatchDocs = 2000
	// shutdownDeadlineControlCorpus is the graph that DOES fit in the window: the
	// same-run known positive. Without it, a drain that skipped every graph for any
	// reason at all would satisfy the skip assertion below. It is a CODE graph
	// named to sort ahead of the abandoned one, because the walk is serial in
	// workingset.Set.Members order (graph type, then name) and a control walked
	// after the abandoned graph would be skipped for want of a window rather than
	// drained — proving nothing.
	shutdownDeadlineControlCorpus = 64
	// shutdownDrainFence is a PATHOLOGY FENCE, NOT THE GATE. R5's claim is about
	// the RECORD — which graph was abandoned, and the error naming what it did not
	// rebuild — because a wall-clock bound on a loaded runner measures the runner.
	// The fence is kept because an unbounded drain is the reported symptom, and it
	// sits at three times the stage budget so it reddens on a drain that runs away
	// rather than on a slow machine. IT PASSES AT v0.10.5 ON THIS FIXTURE: what
	// this test reds on before the fix is the record, not the clock.
	shutdownDrainFence = 10 * time.Second
)

// TestShutdownDrainHonorsTheStopDeadline is R5: SIGTERM during an in-flight
// partition rebuild abandons that rebuild, records the graph as SKIPPED with an
// error that names what was not rebuilt, publishes nothing from the abandoned
// group, and lets the process leave inside the shutdown budget.
func TestShutdownDrainHonorsTheStopDeadline(t *testing.T) {
	// The names decide the walk order: Members sorts by graph type then name, so
	// the control is drained first and the abandoned graph meets a window that is
	// still open.
	const controlRepo = "aaControlRepo"
	const bigRepo = "zzDeadlineRepo"

	logs := captureShutdownLogs(t)

	ctx := opCtx()
	c, _, dir := buildReconcileClientWithDir(t, 100, controlRepo, bigRepo)

	require.NoError(t, c.segmentMgr.AddAndMarkDirty(ctx, kgtypes.GraphCode, controlRepo,
		fastloadVecDocs("control", shutdownDeadlineControlCorpus)))
	// The graph whose rebuild outlives the window: many small batches, so the
	// resident segment set grows the way the reported host's did.
	seedStart := time.Now()
	for b := range shutdownDeadlineBatches {
		require.NoError(t, c.segmentMgr.AddAndMarkDirty(ctx, kgtypes.GraphCode, bigRepo,
			fastloadVecDocs("deadline"+strconv.Itoa(b), shutdownDeadlineBatchDocs)))
	}
	format := hnsw.New().Name()
	t.Logf("staged %d resident %s segments over %d documents in %v",
		c.segmentMgr.ResidentSegmentCount(kgtypes.GraphCode, bigRepo, format),
		format, shutdownDeadlineBatches*shutdownDeadlineBatchDocs, time.Since(seedStart))

	before := l2SegmentIDs(t, dir, bigRepo, format)

	// The drain is gated on the pipeline readiness flag — the same flag that tells
	// the shutdown closure a segment manager was ever wired.
	c.markPipelineReady()

	start := time.Now()
	c.drainOnShutdown()
	elapsed := time.Since(start)
	t.Logf("drainOnShutdown returned after %v (deadline %v, fence %v)", elapsed, daemonStopDeadline, shutdownDrainFence)
	logs.logRebuilds(t)

	// THE RECORD IS ASSERTED FIRST, and the clock last, because the record is the
	// claim and reproduces on any machine while the fence is a derived bound that
	// reads the machine as much as the code.
	record := logs.find(t, "bootstrap: clean-shutdown segment backlog drain")
	drained := logs.strings(t, record, "drained")
	skipped := logs.strings(t, record, "skipped")
	t.Logf("drain record: drained=%v skipped=%v", drained, skipped)

	require.Contains(t, skipped, "code/"+bigRepo,
		"a graph whose rebuild outlived the window must be recorded as SKIPPED; recording it as drained attributes work that was abandoned")
	require.Contains(t, drained, "code/"+controlRepo,
		"the control graph fits inside the window and must still drain; if it does not, this run proves nothing about the deadline — it proves the drain stopped doing anything")

	abandon := logs.find(t, "abandoned work at the shutdown deadline")
	msg := logs.field(t, abandon, "error")
	require.Contains(t, msg, bigRepo, "the abandon error must name the graph whose work was dropped")
	require.Contains(t, msg, format, "the abandon error must name the format whose partitions were not rebuilt")
	require.Contains(t, msg, "partitions_not_rebuilt",
		"the abandon error must name the work it skipped; a bare context error tells an operator nothing about what was dropped")
	require.True(t, strings.Contains(msg, "context deadline exceeded") || strings.Contains(msg, "context canceled"),
		"the abandon must be reported as the context error it is, not as a generic failure: %s", msg)

	require.Equal(t, before, l2SegmentIDs(t, dir, bigRepo, format),
		"an abandoned group publishes NOTHING — publishing the partitions that finished would be the data-loss shape ReplaceBucketGroup's all-or-nothing contract exists to prevent")

	require.Less(t, elapsed, shutdownDrainFence,
		"the shutdown closure must leave the drain inside its own budget; %v against a %v stage deadline means the rebuild chain is still ignoring the context",
		elapsed, daemonStopDeadline)
}

// shutdownLogCapture is the process logger redirected into a buffer for the
// duration of one test, decoded as JSON so a list attribute is read as a list
// rather than matched as a substring of a rendered line.
type shutdownLogCapture struct{ buf *bytes.Buffer }

// captureShutdownLogs installs a JSON handler on the default logger and restores
// the previous one when the test ends.
func captureShutdownLogs(t *testing.T) *shutdownLogCapture {
	t.Helper()
	buf := &bytes.Buffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &shutdownLogCapture{buf: buf}
}

// logRebuilds reports every group rebuild the drain ran, with what it walked and
// how long it took. THIS IS THE FIXTURE'S OWN CALIBRATION RECORD: the assertions
// above are only meaningful while a staged rebuild genuinely outlives the stop
// deadline, and these lines are what a reader checks that against on a machine
// the constants were not chosen on.
func (c *shutdownLogCapture) logRebuilds(t *testing.T) {
	t.Helper()
	for _, rec := range c.all("segmentdist: group_rebuild") {
		t.Logf("group rebuild: graph=%v name=%v format=%v elapsed_ms=%v resolved_segments=%v walked_segments=%v",
			rec["graph"], rec["name"], rec["format"], rec["elapsed_ms"], rec["resolved_segments"], rec["walked_segments"])
	}
}

// all returns every record whose message contains the given text, in order.
func (c *shutdownLogCapture) all(contains string) []map[string]any {
	var out []map[string]any
	for line := range strings.SplitSeq(c.buf.String(), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		if msg, ok := rec["msg"].(string); ok && strings.Contains(msg, contains) {
			out = append(out, rec)
		}
	}
	return out
}

// find returns the first record whose message CONTAINS the given text, failing
// the test when none does — an absent record is a broken probe, not a pass.
func (c *shutdownLogCapture) find(t *testing.T, contains string) map[string]any {
	t.Helper()
	for line := range strings.SplitSeq(c.buf.String(), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue // a non-JSON line is another writer's, not ours.
		}
		if msg, ok := rec["msg"].(string); ok && strings.Contains(msg, contains) {
			return rec
		}
	}
	t.Fatalf("no log record whose message contains %q; the probe read nothing and every assertion over it would be vacuous", contains)
	return nil
}

// strings reads a string-list attribute off a record.
func (c *shutdownLogCapture) strings(t *testing.T, rec map[string]any, key string) []string {
	t.Helper()
	raw, ok := rec[key]
	if !ok {
		return nil
	}
	list, ok := raw.([]any)
	require.True(t, ok, "attribute %q is %T, not a list", key, raw)
	out := make([]string, 0, len(list))
	for _, v := range list {
		s, ok := v.(string)
		require.True(t, ok, "attribute %q holds a %T", key, v)
		out = append(out, s)
	}
	return out
}

// field reads a string attribute off a record.
func (c *shutdownLogCapture) field(t *testing.T, rec map[string]any, key string) string {
	t.Helper()
	s, ok := rec[key].(string)
	require.True(t, ok, "attribute %q is absent or is %T rather than a string", key, rec[key])
	return s
}

// TestShutdownReportsAPipelineThatDidNotDrain is the other half of the stop
// budget: the pipeline stage, whose error the drain used to discard.
//
// WHAT IT OBSERVES. Pipeline.Stop returns a context error naming WHICH of its
// three bounded waits did not finish, and a shutdown that dropped it left an
// operator knowing only that the process left — not that the embed workers were
// still running when it did. The assertion is on the RECORD, through the same
// JSON capture the deadline test above uses.
//
// IT STUBS THE STOPPER, and that indirection exists for this test. The pipeline's
// wait groups are unexported, so through the concrete *pipeline.Pipeline there is
// no way to make Stop fail from this package; c.pipelineStop is the seam, defaulted
// to the real Stop wherever a pipeline is actually wired.
func TestShutdownReportsAPipelineThatDidNotDrain(t *testing.T) {
	logs := captureShutdownLogs(t)

	stopped := 0
	c := &client{
		pipelineStop: func(context.Context) error {
			stopped++
			return fmt.Errorf("pipeline: workers did not drain: %w", context.DeadlineExceeded)
		},
	}
	c.markPipelineReady()

	c.drainOnShutdown()

	require.Equal(t, 1, stopped, "PRECONDITION: the drain must have reached the pipeline stage at all")
	rec := logs.find(t, "pipeline did not finish draining before the shutdown deadline")
	msg := logs.field(t, rec, "error")
	require.Contains(t, msg, "workers did not drain",
		"the record must carry the STAGE the pipeline stopped at; a drain that logs only that something failed tells an operator nothing to act on")
	require.Contains(t, msg, "context deadline exceeded")
}

// TestShutdownSaysNothingWhenThePipelineDrainsClean is the known negative for the
// row above: without it, an assertion that the drain REPORTS a failure would be
// satisfied by a drain that logs that line unconditionally.
func TestShutdownSaysNothingWhenThePipelineDrainsClean(t *testing.T) {
	logs := captureShutdownLogs(t)

	c := &client{pipelineStop: func(context.Context) error { return nil }}
	c.markPipelineReady()

	c.drainOnShutdown()

	require.NotContains(t, logs.buf.String(), "pipeline did not finish draining",
		"a pipeline that drained inside its window must produce no failure record")
}
