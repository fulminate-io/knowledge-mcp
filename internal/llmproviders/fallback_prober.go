// SPDX-License-Identifier: Apache-2.0

package llmproviders

import (
	"context"
	"log/slog"
	"time"

	"github.com/cloudwego/eino/schema"

	"github.com/fulminate-io/knowledge-mcp/internal/llm"
)

// defaultHealthProbeInterval is the prober cadence used when config supplies no
// health_probe_interval. Conservative by design: a recovery check is a billed
// provider call, and quota refills on the order of minutes, so re-checking a
// limited entry every 10 minutes is the right balance of responsiveness vs cost
// (the resilience-over-perf bar — a tighter interval is the gold-plating the
// design forbids).
const defaultHealthProbeInterval = 10 * time.Minute

// healthProber is the background goroutine that re-checks LIMITED summarizer
// chain entries on a fixed interval and marks a recovered one healthy again, so
// the selection summarizer shifts traffic back to the highest-priority entry
// once its quota / rate-limit / outage clears. One instance per chain, sharing
// the same *chainHealth as the selection summarizer.
//
// probes[i] is the cheap recovery check for entry i, built from that entry's
// llm.Client (see newGeneratePingProbe). It is INJECTED rather than built here
// so the loop is testable with plain closures and so the prober never re-derives
// provider-client construction.
type healthProber struct {
	probes   []func(ctx context.Context) error
	health   *chainHealth
	interval time.Duration
}

// newHealthProber constructs a healthProber. A non-positive interval falls back
// to defaultHealthProbeInterval.
func newHealthProber(probes []func(ctx context.Context) error, health *chainHealth, interval time.Duration) *healthProber {
	if interval <= 0 {
		interval = defaultHealthProbeInterval
	}
	return &healthProber{probes: probes, health: health, interval: interval}
}

// RunHealthProbeLoop runs the prober until ctx is cancelled. Each tick it
// snapshots the currently-limited entries and probes only those (a healthy entry
// is never probed — no wasted billed calls); a probe that returns nil marks its
// entry healthy, so the next ActiveIndex read shifts selection back. Mirrors the
// gen-poll loop's ctx.Done()/time.After select lifecycle so it starts and stops
// with the pipeline runtime and leaks no goroutine.
func (p *healthProber) RunHealthProbeLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(p.interval):
			p.probeOnce(ctx)
		}
	}
}

// probeOnce probes every currently-limited entry exactly once and marks any that
// recovers healthy. Bounded cost: at most chain-length probes per tick.
func (p *healthProber) probeOnce(ctx context.Context) {
	for _, i := range p.health.LimitedIndices() {
		if i < 0 || i >= len(p.probes) || p.probes[i] == nil {
			continue
		}
		if err := p.probes[i](ctx); err != nil {
			slog.Debug("llmproviders: chain entry still limited", "entry", i, "error", err)
			continue
		}
		slog.Info("llmproviders: chain entry recovered; restoring to selection", "entry", i)
		p.health.MarkHealthy(i)
	}
}

// newGeneratePingProbe builds the default cheap recovery probe for a chain
// entry: a single 1-token llm.Client.Generate ping with NO ResponseFormat.
// Quota is the failure mode a limited entry is recovering from, and a bare ping
// hits the same quota, so a successful ping (nil error) is sufficient evidence
// the entry can serve again — exercising the full json_schema summary contract
// would only burn more quota for no extra signal. Production (bootstrap) wires
// one of these per entry from the chain's llm.Client + model.
func newGeneratePingProbe(client llm.Client, model llm.Model) func(ctx context.Context) error {
	return func(ctx context.Context) error {
		_, err := client.Generate(ctx, []*schema.Message{
			{Role: schema.User, Content: "ping"},
		},
			llm.WithModel(model),
			llm.WithMaxTokens(1),
		)
		return err
	}
}
