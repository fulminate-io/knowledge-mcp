// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"bytes"
	"context"
	"log/slog"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// backstopCorpusFake is a stateful Caller for the backstop force tests. It models
// a CLEAN thought corpus (every node's UpdatedAt <= the persisted
// last_reflected_updatedat watermark, so the per-tick dirty seed is EMPTY but
// non-nil), serves the adjacency edges, serves the watermark singletons, applies
// the loop's metadata writeback, and captures the propagated_* rows of the most
// recent DeGroot writeback. On a forced tick that correctly passes dirtySeed=nil
// the DeGroot recompute touches EVERY component → propagatedRows is non-empty; if
// the empty seed leaked through instead, the closure is zero components and
// propagatedRows stays empty.
type backstopCorpusFake struct {
	mu             sync.Mutex
	thoughts       map[string]*knowledgev1.Node
	order          []string
	edges          [][2]string
	watermark      int64 // served as last_reflected_updatedat so the corpus reads clean.
	propagatedRows map[string]map[string]string
	// singletons persists resource-singleton upserts (e.g. the reflection-watermark
	// node carrying last_full_pass) so a restart test can read back what a prior loop
	// instance wrote. Shared by reference across two loop instances over one fake.
	singletons map[string]*knowledgev1.Node
}

// cleanCorpusUpdatedAt is the UpdatedAt every node carries AND the served
// last_reflected_updatedat watermark — equal so no node is dirty (the per-tick seed
// is empty, exercising the forced-tick nil-seed bypass).
const cleanCorpusUpdatedAt int64 = 1000

func newBackstopCorpusFake(ids []string, edges [][2]string) *backstopCorpusFake {
	f := &backstopCorpusFake{
		thoughts:  make(map[string]*knowledgev1.Node, len(ids)),
		order:     append([]string(nil), ids...),
		edges:     edges,
		watermark: cleanCorpusUpdatedAt,
	}
	for _, id := range ids {
		f.thoughts[id] = &knowledgev1.Node{Id: id, Type: string(kgtypes.NodeThought), UpdatedAt: cleanCorpusUpdatedAt}
	}
	return f
}

func (f *backstopCorpusFake) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if m := req.GetMutation(); m != nil {
		captured := map[string]map[string]string{}
		for _, it := range m.GetUpdateItems() {
			n := f.thoughts[it.GetId()]
			meta := it.GetMetadata()
			if n != nil {
				for k, v := range meta {
					kgtypes.SetValue(n, k, v)
				}
			}
			if _, hasV := meta["propagated_valence"]; hasV {
				captured[it.GetId()] = map[string]string{
					"propagated_valence":   meta["propagated_valence"],
					"propagated_magnitude": meta["propagated_magnitude"],
				}
			}
		}
		// Singleton watermark upserts (last_full_pass etc.) carry NodeBodies — persist
		// them so a restart test reads back the prior instance's write.
		for _, b := range m.GetNodeBodies() {
			if f.singletons == nil {
				f.singletons = map[string]*knowledgev1.Node{}
			}
			n := f.singletons[b.GetId()]
			if n == nil {
				n = &knowledgev1.Node{Id: b.GetId(), Type: b.GetType(), SymbolName: b.GetName()}
				f.singletons[b.GetId()] = n
			}
			for k, v := range b.GetMetadata() {
				kgtypes.SetValue(n, k, v)
			}
		}
		if len(captured) > 0 {
			f.propagatedRows = captured
		}
		return &knowledgev1.ExecuteResponse{}, nil
	}

	q := req.GetQuery()
	if q == nil {
		return &knowledgev1.ExecuteResponse{}, nil
	}

	// Watermark singleton reads: serve last_reflected_updatedat so the corpus reads
	// clean (empty per-tick seed). The reflect-gen singleton returns absent → 0.
	if q.GetById() == reflectWatermarkNodeID {
		n := &knowledgev1.Node{Id: reflectWatermarkNodeID, Type: "resource"}
		kgtypes.SetValue(n, reflectWatermarkKey, strconv.FormatInt(f.watermark, 10))
		return &knowledgev1.ExecuteResponse{Nodes: []*knowledgev1.Node{n}}, nil
	}
	if id := q.GetById(); id != "" {
		// Serve a persisted singleton (e.g. reflection-watermark / last_full_pass)
		// so the restart test reads back the prior loop instance's write.
		if n, ok := f.singletons[id]; ok {
			return &knowledgev1.ExecuteResponse{Nodes: []*knowledgev1.Node{cloneNode(n)}}, nil
		}
		return &knowledgev1.ExecuteResponse{}, nil // other singletons absent.
	}

	if q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_EDGES {
		if sel := q.GetSelection(); sel != nil {
			for _, et := range sel.GetEdgeTypes() {
				if et == string(kgtypes.EdgeKGContains) || et == string(kgtypes.EdgeChargedBy) {
					return &knowledgev1.ExecuteResponse{}, nil
				}
			}
		}
		var out []*knowledgev1.Edge
		for _, e := range f.edges {
			out = append(out, &knowledgev1.Edge{Type: string(kgtypes.EdgeRelatesTo), FromId: e[0], ToId: e[1]})
		}
		return &knowledgev1.ExecuteResponse{Edges: out}, nil
	}

	if len(q.GetIds()) > 0 {
		var nodes []*knowledgev1.Node
		for _, id := range q.GetIds() {
			if n, ok := f.thoughts[id]; ok {
				nodes = append(nodes, cloneNode(n))
			}
		}
		return &knowledgev1.ExecuteResponse{Nodes: nodes}, nil
	}
	if q.GetOffset() > 0 {
		return &knowledgev1.ExecuteResponse{}, nil
	}
	var nodes []*knowledgev1.Node
	for _, id := range f.order {
		nodes = append(nodes, cloneNode(f.thoughts[id]))
	}
	return &knowledgev1.ExecuteResponse{Nodes: nodes}, nil
}

func (f *backstopCorpusFake) propagatedCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.propagatedRows)
}

// captureFullPass installs a slog handler capturing the boolean full_pass field of
// the "clusters detected" line, returning a getter and a restore func.
func captureFullPass(t *testing.T) (sawFull func() (bool, bool), restore func()) {
	t.Helper()
	var (
		mu       sync.Mutex
		full     bool
		recorded bool
		buf      bytes.Buffer
	)
	prev := slog.Default()
	h := &fullPassHandler{
		inner: slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}),
		onAttr: func(full_ bool) {
			mu.Lock()
			full, recorded = full_, true
			mu.Unlock()
		},
	}
	slog.SetDefault(slog.New(h))
	return func() (bool, bool) {
			mu.Lock()
			defer mu.Unlock()
			return full, recorded
		}, func() {
			slog.SetDefault(prev)
		}
}

type fullPassHandler struct {
	inner  slog.Handler
	onAttr func(bool)
}

func (h *fullPassHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.inner.Enabled(ctx, l)
}
func (h *fullPassHandler) Handle(ctx context.Context, r slog.Record) error {
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == "full_pass" {
			h.onAttr(a.Value.Bool())
			return false
		}
		return true
	})
	return h.inner.Handle(ctx, r)
}
func (h *fullPassHandler) WithAttrs(as []slog.Attr) slog.Handler {
	return &fullPassHandler{inner: h.inner.WithAttrs(as), onAttr: h.onAttr}
}
func (h *fullPassHandler) WithGroup(n string) slog.Handler {
	return &fullPassHandler{inner: h.inner.WithGroup(n), onAttr: h.onAttr}
}

// testBackstopInterval is the cadence every backstop test runs at (24h, the prod
// default). Tests drive the fake clock relative to this to force / not-force.
const testBackstopInterval = 24 * time.Hour

// newBackstopLoop builds a PropagationLoop over the fake with an injected clock and
// the standard 24h backstop interval; lastFull seeds lastFullPass so the next tick
// forces (or not) based on the clock delta.
func newBackstopLoop(fake Caller, clk func() time.Time, lastFull time.Time) *PropagationLoop {
	return &PropagationLoop{
		gc:               fake,
		interval:         time.Hour,
		backstopInterval: testBackstopInterval,
		clock:            clk,
		lastFullPass:     lastFull,
		stopCh:           make(chan struct{}),
		admitted:         admittedGate(),
	}
}

// TestBackstopForcesFullDeGroot (FAILS-WHEN-ABSENT, T2-a) forces a tick over a
// CLEAN corpus (empty per-tick seed) and asserts DeGroot recomputed EVERY component
// — proven by a non-empty propagated_* writeback. If the forced tick leaked the
// empty seed into RunPropagationScoped the closure would be zero components and NO
// propagated_* rows would be written.
func TestBackstopForcesFullDeGroot(t *testing.T) {
	now := time.Date(2026, 6, 10, 3, 0, 0, 0, time.UTC)
	clk := func() time.Time { return now }

	// A connected triangle so DeGroot has a real component to recompute.
	ids := []string{"t1", "t2", "t3"}
	edges := [][2]string{{"t1", "t2"}, {"t2", "t3"}, {"t1", "t3"}}
	fake := newBackstopCorpusFake(ids, edges)

	// lastFullPass is interval+1s in the past → forceFull is true this tick.
	loop := newBackstopLoop(fake, clk, now.Add(-testBackstopInterval-time.Second))

	loop.runBackgroundPropagation()

	assert.Positive(t, fake.propagatedCount(),
		"a forced backstop tick must pass dirtySeed=nil so DeGroot recomputes EVERY component (non-empty propagated_* writeback)")

	// lastFullPass advanced to now on the completed forced pass.
	loop.mu.Lock()
	got := loop.lastFullPass
	loop.mu.Unlock()
	assert.True(t, now.Equal(got), "a completed forced pass advances lastFullPass to clock(): want %v got %v", now, got)
}

// TestBackstopForcesFullLeiden asserts a forced tick runs a TRUE FULL Leiden pass,
// proven via the full_pass=true slog field on the "clusters detected" line.
func TestBackstopForcesFullLeiden(t *testing.T) {
	sawFull, restore := captureFullPass(t)
	defer restore()

	now := time.Date(2026, 6, 10, 3, 0, 0, 0, time.UTC)
	clk := func() time.Time { return now }
	ids := []string{"t1", "t2", "t3"}
	edges := [][2]string{{"t1", "t2"}, {"t2", "t3"}, {"t1", "t3"}}
	fake := newBackstopCorpusFake(ids, edges)
	loop := newBackstopLoop(fake, clk, now.Add(-testBackstopInterval-time.Hour))

	loop.runBackgroundPropagation()

	full, recorded := sawFull()
	require.True(t, recorded, "the clusters-detected line must carry a full_pass field")
	assert.True(t, full, "a forced backstop tick runs a TRUE full Leiden pass (full_pass=true)")
}

// TestBackstopWithinCadenceDoesNotForce confirms a tick INSIDE the cadence
// (lastFullPass == clock(), interval not elapsed) does NOT force: the full_pass
// flag is false (the corpus has prior state → incremental) and lastFullPass is
// unchanged.
func TestBackstopWithinCadenceDoesNotForce(t *testing.T) {
	now := time.Date(2026, 6, 10, 3, 0, 0, 0, time.UTC)
	clk := func() time.Time { return now }
	ids := []string{"t1", "t2", "t3"}
	edges := [][2]string{{"t1", "t2"}, {"t2", "t3"}, {"t1", "t3"}}
	fake := newBackstopCorpusFake(ids, edges)

	// lastFullPass == now → interval NOT elapsed → no force.
	loop := newBackstopLoop(fake, clk, now)

	loop.runBackgroundPropagation()

	loop.mu.Lock()
	got := loop.lastFullPass
	forceNext := loop.forceFullNext
	loop.mu.Unlock()
	assert.True(t, now.Equal(got), "a within-cadence tick must NOT advance lastFullPass")
	assert.False(t, forceNext, "a within-cadence tick must not set forceFullNext")
}

// TestForceFullPass_RunsFullAndPersists (FAILS-WHEN-ABSENT) proves the on-demand
// operator lever runs a TRUE full pass over a CLEAN corpus (empty per-tick seed)
// regardless of cadence: even though lastFullPass==now (interval NOT elapsed, so a
// tick would NOT force), ForceFullPass pins forceFull true unconditionally, so
// DeGroot recomputes EVERY component (non-empty propagated_* writeback) and a
// completed pass advances + persists lastFullPass. Without the lever's
// unconditional force this within-cadence call would run the incremental no-op.
func TestForceFullPass_RunsFullAndPersists(t *testing.T) {
	sawFull, restore := captureFullPass(t)
	defer restore()

	now := time.Date(2026, 6, 10, 3, 0, 0, 0, time.UTC)
	clk := func() time.Time { return now }
	ids := []string{"t1", "t2", "t3"}
	edges := [][2]string{{"t1", "t2"}, {"t2", "t3"}, {"t1", "t3"}}
	fake := newBackstopCorpusFake(ids, edges)

	// lastFullPass == now → a TICK would NOT force (interval not elapsed). The manual
	// lever must force anyway.
	loop := newBackstopLoop(fake, clk, now)

	result, err := loop.ForceFullPass(context.Background())
	require.NoError(t, err, "an uncontended forced pass over a healthy corpus must succeed")

	full, recorded := sawFull()
	require.True(t, recorded, "the clusters-detected line must carry a full_pass field")
	assert.True(t, full, "the manual lever runs a TRUE full Leiden pass (full_pass=true) regardless of cadence")
	assert.Positive(t, fake.propagatedCount(),
		"the manual lever passes dirtySeed=nil so DeGroot recomputes EVERY component (non-empty propagated_* writeback)")
	assert.Positive(t, result.ThoughtsProcessed, "the returned result reports the processed corpus for the rendered summary")

	// A completed forced pass advances + persists lastFullPass to clock(), resetting
	// the backstop cadence exactly as a cadence-forced tick would.
	loop.mu.Lock()
	got := loop.lastFullPass
	loop.mu.Unlock()
	assert.True(t, now.Equal(got), "a completed manual forced pass advances lastFullPass to clock(): want %v got %v", now, got)
	persisted, ok := readLastFullPass(context.Background(), fake)
	require.True(t, ok, "the manual forced pass must persist last_full_pass")
	assert.True(t, now.Equal(persisted), "the persisted last_full_pass matches clock(): got %v", persisted)
}

// TestForceFullPass_CoalescesWhenGuardHeld (FAILS-WHEN-ABSENT) proves the manual
// lever claims the SAME per-account single-flight guard the tick holds: with the
// guard pre-claimed (simulating an in-flight tick), ForceFullPass returns
// ErrReflectionInFlight WITHOUT running a second concurrent recompute — no
// propagated_* writeback lands and lastFullPass is unchanged.
func TestForceFullPass_CoalescesWhenGuardHeld(t *testing.T) {
	release, ok := AcquireReflectionPass(ReflectionPassKey)
	require.True(t, ok, "test must win the first claim")
	defer release()

	now := time.Date(2026, 6, 10, 3, 0, 0, 0, time.UTC)
	ids := []string{"t1", "t2", "t3"}
	edges := [][2]string{{"t1", "t2"}, {"t2", "t3"}, {"t1", "t3"}}
	fake := newBackstopCorpusFake(ids, edges)
	loop := newBackstopLoop(fake, func() time.Time { return now }, now.Add(-2*testBackstopInterval))

	_, err := loop.ForceFullPass(context.Background())
	require.ErrorIs(t, err, ErrReflectionInFlight,
		"a forced pass while the guard is held must coalesce with ErrReflectionInFlight, not run a second recompute")
	assert.Zero(t, fake.propagatedCount(), "a coalesced forced pass must NOT drive a DeGroot writeback")

	loop.mu.Lock()
	got := loop.lastFullPass
	loop.mu.Unlock()
	assert.True(t, now.Add(-2*testBackstopInterval).Equal(got),
		"a coalesced forced pass must NOT advance lastFullPass")
}

// TestBackstopCadenceSurvivesRestart (FAILS-WHEN-ABSENT) proves the backstop
// cadence is restored across a daemon restart rather than re-anchored every boot:
// loop A's forced pass persists lastFullPass=T0 to a fake metadata store shared by
// reference; loop B (the "restart") restores T0 via its boot path; a sub-interval
// tick on B does NOT force a full pass. Without persistence loop B would start at a
// zero-value lastFullPass and force a full pass on its first tick.
func TestBackstopCadenceSurvivesRestart(t *testing.T) {
	t0 := time.Date(2026, 6, 10, 3, 0, 0, 0, time.UTC)
	ids := []string{"t1", "t2", "t3"}
	edges := [][2]string{{"t1", "t2"}, {"t2", "t3"}, {"t1", "t3"}}
	// One fake, shared across both loop instances → its singleton store persists.
	fake := newBackstopCorpusFake(ids, edges)

	// Loop A: forced pass at T0 (lastFullPass far in the past) persists T0.
	loopA := newBackstopLoop(fake, func() time.Time { return t0 }, t0.Add(-2*testBackstopInterval))
	loopA.runBackgroundPropagation()

	persisted, ok := readLastFullPass(context.Background(), fake)
	require.True(t, ok, "loop A's forced pass must persist last_full_pass")
	require.True(t, t0.Equal(persisted), "loop A persisted lastFullPass=T0: got %v", persisted)

	// Loop B: simulated restart over the SAME fake, booting with NOTHING in memory.
	// The restore has moved off the boot path and onto the first admitted tick, so
	// the tick itself must perform it — no manual pre-restore here. If that read
	// were dropped rather than moved, loop B would tick against a zero watermark
	// and force a full pass, which the assertions below catch.
	t1 := t0.Add(time.Hour)
	loopB := newBackstopLoop(fake, func() time.Time { return t1 }, time.Time{})
	require.True(t, loopB.lastFullPass.IsZero(),
		"precondition: loop B boots with no watermark in memory")

	loopB.runBackgroundPropagation()

	loopB.mu.Lock()
	gotB := loopB.lastFullPass
	forceNextB := loopB.forceFullNext
	loopB.mu.Unlock()
	// Interval (24h) NOT elapsed at T0+1h → no force; lastFullPass stays T0.
	assert.True(t, t0.Equal(gotB), "a sub-interval tick after restart must NOT force a full pass (cadence restored)")
	assert.False(t, forceNextB, "a sub-interval tick after restart must not set forceFullNext")
}
