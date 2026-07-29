// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/pipeline"
)

// bootstrapCapturingSlog records emitted records so a test can assert a specific
// terminal WARN fired (message + graph identity attributes).
type bootstrapCapturingSlog struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *bootstrapCapturingSlog) Enabled(context.Context, slog.Level) bool { return true }
func (h *bootstrapCapturingSlog) WithAttrs([]slog.Attr) slog.Handler       { return h }
func (h *bootstrapCapturingSlog) WithGroup(string) slog.Handler            { return h }
func (h *bootstrapCapturingSlog) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r.Clone())
	return nil
}

func (h *bootstrapCapturingSlog) warnsContaining(substr string) []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for _, r := range h.records {
		if r.Level != slog.LevelWarn || !strings.Contains(r.Message, substr) {
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

func installBootstrapCapturingSlog(t *testing.T) *bootstrapCapturingSlog {
	t.Helper()
	h := &bootstrapCapturingSlog{}
	prior := slog.Default()
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(prior) })
	return h
}

// TestBuildHealFactory_DisarmsAfterNoProgress is the RED-first heal-disarm test
// (bootstrap half of v5 4.2): a scan returning 0 rebuild items drives ran=true /
// scanned=0 → RecordNoProgress; after healBreakerTripThreshold no-progress passes the
// breaker latches, Allow==false, and the closure short-circuits with
// pipeline.ErrHealDisarmed so NO further RebuildSegments/scan fires. RED against
// pre-fix code (the closure re-fires RebuildSegments every wake, unbounded); GREEN
// once the Allow gate + classifier land. The terminal trip WARN is asserted with
// graph identity via a capturing slog handler.
func TestBuildHealFactory_DisarmsAfterNoProgress(t *testing.T) {
	warns := installBootstrapCapturingSlog(t)
	ctx := context.Background()
	const repo = "healDisarmRepo"

	// embedded 120 (>= floor) with an empty L2 (resident 0) → healNeedsRebuild fires
	// the one-shot rebuild every pass; the empty scanItems make each rebuild scanned=0.
	c, eng := buildOSSHealClient(t, 120, repo)
	heal := c.buildHealFactory()(kgtypes.GraphCode, repo)
	require.NotNil(t, heal)

	// Pre-trip: each no-progress pass runs a rebuild (scans once) and returns nil
	// (best-effort). The threshold-th pass latches the breaker.
	for range healBreakerTripThreshold {
		require.NoError(t, heal(ctx), "a no-progress heal returns nil until the breaker latches")
	}
	scansAtTrip := eng.scanCallCount(repo)
	require.Equal(t, healBreakerTripThreshold, scansAtTrip, "each pre-trip pass scanned exactly once")
	require.False(t, c.healBreaker.Allow(kgtypes.GraphCode, repo), "the breaker latched after K no-progress passes")

	// Post-trip: the closure short-circuits with the disarm sentinel and NO further scan
	// fires. RED pre-fix: the closure re-fires RebuildSegments every call (scan climbs).
	for range 4 {
		require.ErrorIs(t, heal(ctx), pipeline.ErrHealDisarmed, "a latched breaker returns the disarm sentinel")
	}
	require.Equal(t, scansAtTrip, eng.scanCallCount(repo),
		"no further RebuildSegments/scan after the breaker latched (RED: unbounded re-fire pre-fix)")

	// The terminal trip WARN fired with graph identity.
	trip := warns.warnsContaining("auto-heal suspended for graph")
	require.NotEmpty(t, trip, "the breaker emits the terminal trip WARN on latching")
	require.Contains(t, trip[0], repo, "the trip WARN carries the graph identity")
	// The per-pass no-progress WARN (scanned 0) also fired.
	require.NotEmpty(t, warns.warnsContaining("scanned 0 nodes"), "each no-progress pass emits the scanned-0 WARN")
}

// TestClassifyHealOutcome_ProgressDoesNotTrip pins the strict-classification PROGRESS
// rule (v5 2.1): a scanned>0 && built==0 && partial>0 pass (a sub-1024 tail-only
// rebuild) on the OSS/L2 path is PROGRESS and must NOT trip the breaker — and it RESETS
// a pre-trip no-progress streak. Guards against a classifier that treats built==0 as
// no-progress.
func TestClassifyHealOutcome_ProgressDoesNotTrip(t *testing.T) {
	ctx := context.Background()
	const repo = "healProgressRepo"
	c, _ := buildOSSHealClient(t, 120, repo)
	require.True(t, c.segmentMgr.IsL2Authoritative(kgtypes.GraphCode, repo), "OSS caller → L2-authoritative path")

	// A built==0 / partial>0 pass is PROGRESS on the L2 path (judged by scanned only).
	c.classifyHealOutcome(ctx, kgtypes.GraphCode, repo, true /*ran*/, 10 /*scanned*/, 0 /*built*/, 10 /*partial*/)
	require.True(t, c.healBreaker.Allow(kgtypes.GraphCode, repo), "scanned>0/built==0/partial>0 is PROGRESS — no trip")

	// It also RESETS a pre-trip no-progress streak: one no-progress then a progress pass
	// means one more no-progress does not latch (the reset dropped the earlier streak).
	require.False(t, c.healBreaker.RecordNoProgress(kgtypes.GraphCode, repo), "first no-progress below threshold")
	c.classifyHealOutcome(ctx, kgtypes.GraphCode, repo, true, 10, 0, 10) // PROGRESS resets the streak
	require.False(t, c.healBreaker.RecordNoProgress(kgtypes.GraphCode, repo), "post-reset no-progress is below threshold again")
	require.True(t, c.healBreaker.Allow(kgtypes.GraphCode, repo), "the progress pass reset the streak, so no latch yet")

	// A ran==false outcome is NEVER recorded (coalesce / pipeline-not-wired no-op).
	c.classifyHealOutcome(ctx, kgtypes.GraphCode, repo, false, 0, 0, 0)
	require.True(t, c.healBreaker.Allow(kgtypes.GraphCode, repo), "ran==false is not a heal outcome — never recorded")
}
