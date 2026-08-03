// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/hnsw"
)

// rebuildLogCapture records slog records so the operator-visibility gates can
// assert over the REAL default logger rather than a test-only sink.
type rebuildLogCapture struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *rebuildLogCapture) Enabled(context.Context, slog.Level) bool { return true }
func (h *rebuildLogCapture) WithAttrs([]slog.Attr) slog.Handler       { return h }
func (h *rebuildLogCapture) WithGroup(string) slog.Handler            { return h }
func (h *rebuildLogCapture) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r.Clone())
	return nil
}

// linesContaining renders every record at the given level whose message contains
// substr, with its attributes appended.
func (h *rebuildLogCapture) linesContaining(level slog.Level, substr string) []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for _, r := range h.records {
		if r.Level != level || !strings.Contains(r.Message, substr) {
			continue
		}
		var b strings.Builder
		b.WriteString(r.Message)
		r.Attrs(func(a slog.Attr) bool {
			b.WriteString(" " + a.Key + "=" + a.Value.String())
			return true
		})
		out = append(out, b.String())
	}
	return out
}

func captureRebuildLogs(t *testing.T) *rebuildLogCapture {
	t.Helper()
	h := &rebuildLogCapture{}
	prior := slog.Default()
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(prior) })
	return h
}

// twoBucketScanner returns a scanner whose corpus splits into exactly two hash
// buckets, so `built` is a known 2 and the cardinality comparisons below are exact.
func twoBucketScanner() *fakeRebuildScanner {
	min := searchengine.DefaultMinSegmentDocs
	return &fakeRebuildScanner{pages: [][]*knowledgev1.PipelineScanItem{
		makeScanPage("a", 0, min),
		makeScanPage("b", 0, min),
	}}
}

// TestManifestCardinalityMatchesBuildCount is the cardinality gate. On a FULL/RESET
// rebuild the manifest is READ BACK FROM THE SOURCE and compared against what the
// run reported building.
//
// WHICH PRODUCTION CALL WOULD HAVE TO BE WRONG: the publish path emitting a manifest
// whose entry count disagrees with the build count. The read therefore goes through
// the SegmentShipper seam and the assertions are over the driver's outcome and its
// WARN — if the answer were "a local variable compared to itself", the gate would be
// in the wrong place, which is what the first draft of this criterion did.
//
// THE 32-OF-128 EVENT IS NOT AN INSTANCE OF THIS. The manifest was complete there
// and the client's partial L2 cache was the truncation — and even that is an
// INFERENCE from the control run, since the live manifest was overwritten before
// anyone read it. This gate exists because the cardinality was UNVERIFIABLE, and it
// closes that prospectively.
func TestManifestCardinalityMatchesBuildCount(t *testing.T) {
	ctx := context.Background()

	t.Run("full rebuild reads the manifest back and agrees", func(t *testing.T) {
		logs := captureRebuildLogs(t)
		shipper := &fakeRebuildShipper{manifestConfigured: true, manifestCount: 2}
		out, err := RebuildSegments(ctx, twoBucketScanner(), shipper, kgtypes.GraphCode, "myrepo", true)
		require.NoError(t, err)
		require.True(t, out.Published, "fixture: the swap must land or the read-back never runs")
		require.Equal(t, 2, out.Built)

		require.Positive(t, shipper.manifestReads.Load(),
			"the cardinality was never READ BACK FROM THE SOURCE — a local-vs-local comparison cannot fail for the reason this gate exists")
		require.Equal(t, []string{hnsw.New().Name()}, shipper.manifestFormats,
			"the read-back must target the HNSW arm: the deterministic rebuild publishes exactly the buckets it built there, "+
				"while the shared BM25 engine legitimately carries extra sealed tails")
		require.Equal(t, out.Built, out.PublishedManifest,
			"the published manifest must hold as many entries as the rebuild reported building")
		require.Empty(t, logs.linesContaining(slog.LevelWarn, "FEWER entries"),
			"an agreeing cardinality must not WARN")
	})

	t.Run("a SHORT manifest is reported and warned", func(t *testing.T) {
		logs := captureRebuildLogs(t)
		shipper := &fakeRebuildShipper{manifestConfigured: true, manifestCount: 1}
		out, err := RebuildSegments(ctx, twoBucketScanner(), shipper, kgtypes.GraphCode, "shortrepo", true)
		require.NoError(t, err)
		require.Equal(t, 2, out.Built)
		require.Equal(t, 1, out.PublishedManifest, "the outcome must carry what the source actually published")

		warns := logs.linesContaining(slog.LevelWarn, "FEWER entries")
		require.NotEmpty(t, warns, "a manifest shorter than the build count must be loud — nothing else on this path can check it")
		require.Contains(t, warns[0], "built=2")
		require.Contains(t, warns[0], "published_manifest=1")
	})

	t.Run("a LONGER manifest is not a fault", func(t *testing.T) {
		logs := captureRebuildLogs(t)
		// The embed path publishes the union of its own resident set with the
		// deterministic engine's, so a ship landing between the swap and the read-back
		// legitimately grows the manifest. Only SHORT means built content is unreferenced.
		shipper := &fakeRebuildShipper{manifestConfigured: true, manifestCount: 5}
		out, err := RebuildSegments(ctx, twoBucketScanner(), shipper, kgtypes.GraphCode, "longrepo", true)
		require.NoError(t, err)
		require.Equal(t, 5, out.PublishedManifest)
		require.Empty(t, logs.linesContaining(slog.LevelWarn, "FEWER entries"),
			"a manifest longer than the build count is ordinary and must not warn")
	})

	t.Run("DELTA rebuilds are out of the build-count invariant", func(t *testing.T) {
		logs := captureRebuildLogs(t)
		// reset=false ALONE DOES NOT MAKE A RUN INCREMENTAL. A graph with no persisted
		// watermark scans from zero, so its buckets ARE the whole corpus and the
		// build-count invariant applies to it exactly as it does to a reset. Only a
		// NON-ZERO watermark scopes a run to a window, and there the manifest
		// legitimately holds entries this run never built — comparing it to the build
		// count would fail correct work.
		shipper := &fakeRebuildShipper{manifestConfigured: true, manifestCount: 99}
		shipper.watermark = 1_700_000_000_000_000_000
		out, err := RebuildSegments(ctx, twoBucketScanner(), shipper, kgtypes.GraphCode, "increpo", false)
		require.NoError(t, err)
		require.Equal(t, int64(1), shipper.deltaCalls.Load(), "a window-scoped run finalizes through the delta path")
		require.Empty(t, logs.linesContaining(slog.LevelWarn, "FEWER entries"),
			"the build-count invariant must NOT be applied to a window-scoped run")
		require.Equal(t, manifestCardinalityUnmeasured, out.PublishedManifest,
			"not measured must be distinguishable from a manifest of zero entries")
	})

	t.Run("a ZERO-watermark run is corpus-complete and IS read back", func(t *testing.T) {
		// The widening: a full run reached without an explicit reset is still a run whose
		// buckets are the whole corpus, and leaving it unmeasured was a blind spot.
		shipper := &fakeRebuildShipper{manifestConfigured: true, manifestCount: 2}
		out, err := RebuildSegments(ctx, twoBucketScanner(), shipper, kgtypes.GraphCode, "zerowm", false)
		require.NoError(t, err)
		require.Zero(t, shipper.deltaCalls.Load(), "a zero watermark is a from-scratch run, not a delta")
		require.Equal(t, 2, out.PublishedManifest, "the read-back must run without an explicit reset")
	})

	t.Run("a failed read-back never fails a landed rebuild", func(t *testing.T) {
		shipper := &fakeRebuildShipper{manifestErr: errors.New("registry unreachable")}
		out, err := RebuildSegments(ctx, twoBucketScanner(), shipper, kgtypes.GraphCode, "errrepo", true)
		require.NoError(t, err, "the corpus is already published — a verification read must not discard work that landed")
		require.True(t, out.Published)
		require.Equal(t, manifestCardinalityUnmeasured, out.PublishedManifest)
	})

	t.Run("a refused swap is never read back", func(t *testing.T) {
		shipper := &fakeRebuildShipper{noSwap: true, manifestConfigured: true, manifestCount: 2}
		out, err := RebuildSegments(ctx, twoBucketScanner(), shipper, kgtypes.GraphCode, "noswaprepo", true)
		require.NoError(t, err)
		require.False(t, out.Published)
		require.Zero(t, shipper.manifestReads.Load(),
			"nothing swapped, so there is no new manifest to check — reading one would compare against the PRIOR live set")
	})
}

// TestRebuildEmitsOperatorVisiblePublishLines is observability gate 4b, scoped to
// the counts the INTERCEPTOR OWNS.
//
// WHY IT IS SCOPED THAT WAY: the per-format shipped-versus-skipped-as-present counts
// do not exist at this layer. Asserting them here would either fail against correct
// work or be satisfied by a number the interceptor synthesised, which is worse; they
// are gated in segmentdist over a real distManager instead. The FinalizeRebuild
// seam is deliberately NOT widened to carry them up.
//
// WHICH PRODUCTION CALL WOULD HAVE TO BE WRONG: the rebuild interceptor's result
// construction and its completion emit. The red-first evidence is three daemon lines
// for a 78-second rebuild that truncated the served corpus to a quarter.
func TestRebuildEmitsOperatorVisiblePublishLines(t *testing.T) {
	ctx := context.Background()

	t.Run("a landed rebuild logs its own counts", func(t *testing.T) {
		logs := captureRebuildLogs(t)
		shipper := &fakeRebuildShipper{
			manifestConfigured: true, manifestCount: 2,
			pruned: []searchengine.SegmentID{"seg-old"},
		}
		out, err := RebuildSegments(ctx, twoBucketScanner(), shipper, kgtypes.GraphCode, "myrepo", true)
		require.NoError(t, err)

		lines := logs.linesContaining(slog.LevelInfo, "rebuild_segments: run complete")
		require.NotEmpty(t, lines, "a rebuild that reached a publish must say so in the daemon log")
		line := lines[0]
		for _, want := range []string{
			"graph_type=", "name=", "scanned=", "built=2", "published=true", "pruned=1", "published_manifest=2",
		} {
			require.Contains(t, line, want, "the completion line is missing an interceptor-owned count")
		}
		require.NotContains(t, line, "skipped_as_present",
			"shipped-vs-skipped does not exist at this layer — a value here could only have been synthesised")
		require.Equal(t, 2, out.Built)
	})

	t.Run("the result distinguishes what was BUILT from what LANDED", func(t *testing.T) {
		// A refused swap returns a NIL ERROR, so a result that reported only the build
		// count would read as success over a corpus that was never made live. This is
		// exactly how the failed clean restore was scored as having run.
		deps := rebuildClientDeps{scanner: twoBucketScanner(), shipper: &fakeRebuildShipper{noSwap: true}}
		res := handleClientRebuildSegments(ctx, deps, manageArgs{
			Operation: "rebuild_segments", Graph: "code", Name: "myrepo",
		})
		require.False(t, res.IsError)
		text := res.Content[0].Text
		require.Contains(t, text, "INCOMPLETE")
		require.Contains(t, text, "REFUSED")
		require.Contains(t, text, "NOT the live set")

		// And the landed case says the opposite, in the same place.
		deps2 := rebuildClientDeps{scanner: twoBucketScanner(), shipper: &fakeRebuildShipper{}}
		res2 := handleClientRebuildSegments(ctx, deps2, manageArgs{
			Operation: "rebuild_segments", Graph: "code", Name: "myrepo2",
		})
		require.False(t, res2.IsError)
		require.Contains(t, res2.Content[0].Text, "PUBLISHED as the live set")
	})
}
