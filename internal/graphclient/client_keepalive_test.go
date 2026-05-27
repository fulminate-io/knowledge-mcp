// SPDX-License-Identifier: Apache-2.0

package graphclient

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// fakePinger satisfies the package-private healthPinger interface.
// pingErr is swappable per-tick so a single test can switch between
// success and failure modes.
type fakePinger struct {
	mu      sync.Mutex
	pingErr error
	calls   int
}

func (p *fakePinger) setErr(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pingErr = err
}

func (p *fakePinger) Check(
	_ context.Context,
	_ *connect.Request[knowledgev1.HealthCheckRequest],
) (*connect.Response[knowledgev1.HealthCheckResponse], error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	if p.pingErr != nil {
		return nil, p.pingErr
	}
	return connect.NewResponse(&knowledgev1.HealthCheckResponse{}), nil
}

func (p *fakePinger) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

// captureHandler is a minimal slog.Handler that appends every record
// to an in-memory slice. Used so the keepalive tests can assert
// slog.Warn / slog.Error messages without deterministic log order
// gymnastics.
type captureHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (c *captureHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }
func (c *captureHandler) WithAttrs(_ []slog.Attr) slog.Handler         { return c }
func (c *captureHandler) WithGroup(_ string) slog.Handler              { return c }
func (c *captureHandler) Handle(_ context.Context, r slog.Record) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.records = append(c.records, r.Clone())
	return nil
}

func (c *captureHandler) snapshot() []slog.Record {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]slog.Record, len(c.records))
	copy(out, c.records)
	return out
}

// hasRecord checks whether the captured records contain any record at
// level `lvl` whose message contains `substr`.
func hasRecord(records []slog.Record, lvl slog.Level, substr string) bool {
	for _, r := range records {
		if r.Level == lvl && strings.Contains(r.Message, substr) {
			return true
		}
	}
	return false
}

// TestKeepalive_EscalatingFailures drives keepaliveLoopWith through
// healthy → failing × 2 → failing × 5 → recovered transitions and
// asserts the per-threshold slog outputs.
func TestKeepalive_EscalatingFailures(t *testing.T) {
	pinger := &fakePinger{}
	ticks := make(chan time.Time, 16)

	handler := &captureHandler{}
	prev := slog.Default()
	slog.SetDefault(slog.New(handler))
	t.Cleanup(func() { slog.SetDefault(prev) })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	c := &GraphClient{}

	done := make(chan struct{})
	go func() {
		c.keepaliveLoopWith(ctx, ticks, pinger)
		close(done)
	}()

	waitForCalls := func(n int) {
		deadline := time.Now().Add(500 * time.Millisecond)
		for time.Now().Before(deadline) {
			if pinger.callCount() >= n {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatalf("timeout waiting for %d calls (have %d)", n, pinger.callCount())
	}

	// Phase 1 — 2 healthy pings produce no Warn/Error.
	pinger.setErr(nil)
	ticks <- time.Now()
	ticks <- time.Now()
	waitForCalls(2)
	records := handler.snapshot()
	assert.False(t, hasRecord(records, slog.LevelWarn, "keepalive"),
		"no Warn on healthy pings")
	assert.False(t, hasRecord(records, slog.LevelError, "keepalive"),
		"no Error on healthy pings")

	// Phase 2 — switch to failing. Tick 1, 2. Expect slog.Warn at
	// consecutive_failures == 2.
	pinger.setErr(errors.New("ping fail"))
	ticks <- time.Now()
	ticks <- time.Now()
	waitForCalls(4)
	records = handler.snapshot()
	assert.True(t, hasRecord(records, slog.LevelWarn, "knowledge-server keepalive failing"),
		"expect slog.Warn at consecutive_failures=2")

	// Phase 3 — Ticks 3, 4, 5. Expect slog.Error at consecutive_failures == 5.
	ticks <- time.Now()
	ticks <- time.Now()
	ticks <- time.Now()
	waitForCalls(7)
	records = handler.snapshot()
	assert.True(t, hasRecord(records, slog.LevelError, "server appears unreachable"),
		"expect slog.Error at consecutive_failures=5")

	// Phase 4 — Switch back to success. Expect slog.Info "recovered"
	// and consecutive reset.
	pinger.setErr(nil)
	ticks <- time.Now()
	waitForCalls(8)
	records = handler.snapshot()
	assert.True(t, hasRecord(records, slog.LevelInfo, "keepalive recovered"),
		"expect slog.Info 'recovered' after failure streak ended")

	cancel()
	<-done
}

// TestKeepalive_CtxCancelReturnsPromptly asserts the loop exits when
// ctx cancels.
func TestKeepalive_CtxCancelReturnsPromptly(t *testing.T) {
	pinger := &fakePinger{}
	ticks := make(chan time.Time)

	ctx, cancel := context.WithCancel(context.Background())
	c := &GraphClient{}

	done := make(chan struct{})
	go func() {
		c.keepaliveLoopWith(ctx, ticks, pinger)
		close(done)
	}()

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("keepaliveLoopWith did not exit on ctx cancel")
	}
}
