// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/pipeline"
)

// TestActivityHookWakesPipelineOnWatermarkMove is the cross-seam proof: every
// other test in this plan pins ONE side of the seam, this one runs the whole
// path in-process. A fake engine stamps a moving account watermark on its
// responses; the client observes it off the wire, the intercept-chain hook
// compares it, and the pipeline's central gen-poll loop actually polls.
//
// The wake is observed as a gen-poll RPC on the engine rather than through a
// stub waker, so the assertion covers the real WakeAll → loop → RPC path. The
// loop's own cadence is set to an hour so nothing but a wake can move the
// counter during the test.
func TestActivityHookWakesPipelineOnWatermarkMove(t *testing.T) {
	ctx := opCtx()
	url, eng := startCountingEngine(t)
	c := buildE2EClient(graphclient.NewGraphClientForURL(url), "http://cloud.invalid", newFakeAuthStore(), time.Hour)

	// A real pipeline over the same routing layer the client dispatches through
	// — the production wiring, minus the LLM workers (nil summarizer + embedder
	// leaves every collector axis disabled, so no scan traffic competes with the
	// gen-poll count).
	p := pipeline.New(
		pipeline.Config{Tick: time.Hour, CloudTick: time.Hour},
		routedWireClient{router: c.router}, nil, nil)
	c.pipeline = p
	require.NoError(t, p.Start(ctx))

	loopCtx, cancelLoop := context.WithCancel(ctx)
	t.Cleanup(func() {
		cancelLoop()
		require.NoError(t, p.Stop(context.Background()))
	})
	// One registered graph, so a poll has something to ask about: with an empty
	// collector set genPollOnce issues no RPC at all.
	p.RegisterGraph(loopCtx, kgtypes.GraphCode, "repo")
	go p.RunGenPollLoop(loopCtx)

	// The loop's boot poll is the baseline AND the known-positive control: it
	// proves the counter moves at all, so a later "no further polls" assertion
	// is a real zero rather than a probe pointed at nothing.
	require.Eventually(t, func() bool { return eng.genPoll.Load() == 1 },
		2*time.Second, 5*time.Millisecond, "the gen-poll loop must issue its boot poll")

	searchArgs := json.RawMessage(`{"query":"x","graph":"knowledge"}`)
	benign := kgtools.CallToolParams{Name: "list_branches", Arguments: json.RawMessage(`{}`)}

	// A tool call that falls through to the server: the chain handles nothing,
	// the caller forwards it, and the response carries the moved watermark.
	_, handled, _ := c.runInterceptChain(ctx, benign)
	require.False(t, handled, "the fixture tool must fall through to the server")
	eng.freshnessGen.Store(5)
	_, err := c.engineDispatch(ctx, "search", searchArgs)
	require.NoError(t, err)
	require.Equal(t, 1, int(eng.genPoll.Load()), "forwarding a call must not poll by itself")

	// The NEXT tool call's hook sees the moved watermark and wakes the pipeline.
	c.runInterceptChain(ctx, benign)
	require.Eventually(t, func() bool { return eng.genPoll.Load() == 2 },
		2*time.Second, 5*time.Millisecond, "a moved watermark must wake the gen-poll loop exactly once")

	// A second move inside the cool-off window: observed, recorded as pending,
	// but not woken — the whole point of the gate.
	eng.freshnessGen.Store(6)
	_, err = c.engineDispatch(ctx, "search", searchArgs)
	require.NoError(t, err)
	c.runInterceptChain(ctx, benign)

	time.Sleep(150 * time.Millisecond)
	assert.Equal(t, 2, int(eng.genPoll.Load()),
		"a second watermark move inside the cool-off must add no further poll")
	assert.Equal(t, 2, int(eng.execute.Load()),
		"exactly the two forwarded tool calls reached the engine")
}
