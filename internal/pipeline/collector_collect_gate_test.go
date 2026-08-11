// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/llmproviders"
)

// The two gate markers, written as LITERALS here rather than referencing the
// constants the production code emits. That duplication is deliberate: it is what
// makes this test pin the exact wire text the live-confirmation artifact greps out
// of the daemon log. Sharing a constant with production would let a reworded
// marker stay green here while silently breaking that gate.
const (
	gatedMarkerText   = "pipeline.collector: gap scan gated by in-flight collect"
	resumedMarkerText = "pipeline.collector: gap scan resumed after collect"
)

// collector_collect_gate_test.go covers throttle #4 — the collect-gate — end to
// end through the real Pipeline/RegisterGraph/runLoop path, asserting on the
// COUNTED scan RPCs rather than on a log line.

const gateTestRepo = "myrepo"

// startGatedPipeline builds a summary-only pipeline whose collect-gate predicate
// reads gated, registers one code graph, and returns the pipeline plus the RPC
// counter. The caller drives gated.
func startGatedPipeline(t *testing.T, gated *atomic.Bool) (*Pipeline, *fakeWireClient) {
	t.Helper()
	cfg := Config{
		SummaryChannelSize: 4,
		SummaryBatchSize:   1,
		SummaryWorkers:     1,
		Tick:               5 * time.Millisecond,
	}
	noopSum := func(_ context.Context, _ []llmproviders.BatchChunk) (map[string]llmproviders.SummarizeResult, error) {
		return nil, nil
	}
	wc := newFakeWireClient()
	p := New(cfg, wc, noopSum, nil)
	p.AttachCollectGateFactory(func(_ kgtypes.GraphType, _ string) func() bool {
		return gated.Load
	})
	require.NoError(t, p.Start(context.Background()))
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = p.Stop(stopCtx)
	})
	p.RegisterGraph(context.Background(), kgtypes.GraphCode, gateTestRepo)
	return p, wc
}

// TestCollectGate_NoScanWhileCollectInFlight is the gate's primary behavioral
// proof: while the predicate reports a collect in flight, the scan loop issues NO
// PipelineScan RPCs for that graph; once it clears, scanning resumes.
//
// The post-release advance is also the known-positive control for the zero. A
// collector that was simply never going to scan — a wedged loop, a graph that
// never registered — produces the same zero as a correctly-gated one, so the zero
// only means anything next to a demonstrated non-zero from the same collector.
func TestCollectGate_NoScanWhileCollectInFlight(t *testing.T) {
	var gated atomic.Bool
	gated.Store(true)
	_, wc := startGatedPipeline(t, &gated)

	// Many base ticks. An ungated collector scans repeatedly in this window.
	time.Sleep(60 * time.Millisecond)
	require.Zero(t, wc.scanCountForAxis("summary"),
		"no scan may be issued while a collect into this graph is in flight")

	gated.Store(false)
	require.Eventually(t, func() bool { return wc.scanCountForAxis("summary") > 0 },
		2*time.Second, 5*time.Millisecond,
		"scanning must resume once the collect completes — without this the zero above proves nothing")
}

// TestCollectGate_WakeDuringGateStillRescans pins the sleepFor-vs-sleepForWake
// choice in the gate's skip.
//
// A collect fires a pipeline wake from inside its own work, i.e. while the gate is
// STILL up. The wake channels are buffered(1) and coalescing, so a gated iteration
// that slept on the WAKE channel would CONSUME that signal and then sleep anyway —
// the early re-scan the collect asked for would be silently swallowed and the graph
// would wait out a full idle interval instead. Gating with the plain timer sleep
// leaves the signal queued for the first ungated iteration.
//
// The assertion is therefore twofold: the queued wake SURVIVES the gate, and a scan
// follows once the gate clears.
func TestCollectGate_WakeDuringGateStillRescans(t *testing.T) {
	var gated atomic.Bool
	gated.Store(true)
	p, wc := startGatedPipeline(t, &gated)

	key := graphKey{GraphType: kgtypes.GraphCode, GraphName: gateTestRepo}
	p.collectorMu.Lock()
	wakes := p.collectorWakes[key]
	p.collectorMu.Unlock()
	require.NotEmpty(t, wakes, "the registered collector must expose its wake channels")

	// Deliver a wake exactly as a collect does — while the gate is still up.
	for _, w := range wakes {
		select {
		case w <- struct{}{}:
		default: // already queued — coalesced, same as production
		}
	}

	// Let the gated loop turn over many times with the wake pending.
	time.Sleep(60 * time.Millisecond)
	require.Zero(t, wc.scanCountForAxis("summary"), "the gate must hold even with a wake queued")
	for _, w := range wakes {
		require.Len(t, w, 1,
			"a gated iteration must NOT consume the queued wake — it slept on the timer, not the wake channel")
	}

	gated.Store(false)
	require.Eventually(t, func() bool { return wc.scanCountForAxis("summary") > 0 },
		2*time.Second, 5*time.Millisecond,
		"the re-scan must still happen after the gate clears")
}

// syncBuf is a race-safe io.Writer for the captured log stream: the collector
// goroutine writes to it while the test reads.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// captureCollectorLogs redirects the default logger into a buffer at Debug level
// for the test's duration. Debug is where the collector's per-cycle markers live,
// and where the daemon runs.
func captureCollectorLogs(t *testing.T) *syncBuf {
	t.Helper()
	buf := &syncBuf{}
	prior := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prior) })
	return buf
}

// TestCollectGate_LogsGatedAndResumedOnce is the observability gate. Without the
// two markers the collect-gate is entirely silent: it skips the scan and continues,
// so nothing in production could show that it held, released, or wedged — and the
// live confirmation would have nothing to grep for, permanently.
//
// ONCE-NESS IS THE ASSERTION THAT MATTERS, not presence. The gate skips once per
// base tick for the whole collect, so a per-iteration log would emit dozens of
// lines per collect per axis — and would still satisfy any test that merely
// checked the marker appeared. The counts below are what separate an
// edge-triggered marker from a per-iteration one.
func TestCollectGate_LogsGatedAndResumedOnce(t *testing.T) {
	logs := captureCollectorLogs(t)

	var gated atomic.Bool
	gated.Store(true)
	_, wc := startGatedPipeline(t, &gated)

	// Many base ticks, so a per-iteration marker would log many times over.
	time.Sleep(60 * time.Millisecond)
	require.Zero(t, wc.scanCountForAxis("summary"),
		"precondition: the gate must actually be holding, or the counts below prove nothing")
	require.Equal(t, 1, strings.Count(logs.String(), gatedMarkerText),
		"the gated marker must fire ONCE for the whole gated run, not once per skipped iteration")
	require.Zero(t, strings.Count(logs.String(), resumedMarkerText),
		"nothing has resumed yet — a resumed marker here would mean the latch never latched")

	gated.Store(false)
	require.Eventually(t, func() bool { return wc.scanCountForAxis("summary") > 0 },
		2*time.Second, 5*time.Millisecond, "the gate must release so the falling edge can fire")

	out := logs.String()
	require.Equal(t, 1, strings.Count(out, resumedMarkerText),
		"the resumed marker must fire exactly once, on the first ungated iteration")
	require.Equal(t, 1, strings.Count(out, gatedMarkerText),
		"releasing the gate must not re-fire the gated marker")

	// The fields an operator needs to act on either line: WHICH graph and axis was
	// gated, and — on release — for how long. A marker that says only "a gate
	// happened" cannot be attributed to a graph in a daemon serving many.
	gatedLine := logLineContaining(t, out, gatedMarkerText)
	resumedLine := logLineContaining(t, out, resumedMarkerText)
	for _, field := range []string{"graph_type=code", "name=" + gateTestRepo, "axis=summary"} {
		require.Contains(t, gatedLine, field, "the gated marker must carry %s", field)
		require.Contains(t, resumedLine, field, "the resumed marker must carry %s", field)
	}
	require.Contains(t, resumedLine, "gated_for=",
		"the resumed marker must report how long the gate held — the whole point of the falling edge")
}

// logLineContaining returns the single captured log line holding want.
func logLineContaining(t *testing.T, out, want string) string {
	t.Helper()
	for line := range strings.SplitSeq(out, "\n") {
		if strings.Contains(line, want) {
			return line
		}
	}
	t.Fatalf("no captured log line contains %q", want)
	return ""
}
