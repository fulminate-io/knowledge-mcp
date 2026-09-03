// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// TestServeBindFirst_EngineOpWorks_RuntimeOpsGated is the bind-first startup change fails-when-absent
// guard for In-Scope item 2 (bind first, gate runtime-dependent surfaces during
// the wiring window). It drives the *client's intercept chain + engineDispatch
// with all three readiness flags FALSE (the bind-first window before
// wireRuntimesBackground completes) and asserts:
//
//   - a runtime-INDEPENDENT mutate (node create → engineDispatch → router.Execute
//     against a fake local engine) SUCCEEDS pre-wiring — engine ops serve
//     immediately after bind, with NO segment-engine / runtime dependency;
//   - the runtime-DEPENDENT ops each return the loud "daemon still starting" error
//     while their flag is false and NEVER panic: a knowledge TEXT search AND a
//     knowledge query text-search (both → composeKnowledgeSearch, gated on
//     PipelineReady — the T2-A panic arm; the segment Manager is left nil, the
//     exact window state) and thoughts propagate force_full (PropReady);
//   - flipping the flags via mark*Ready() lets the same ops reach the wired/degrade
//     path WITHOUT panicking.
//
// The knowledge-text-search leg is load-bearing: it fails if that arm panics on a
// nil mgr.Search (ungated) or fails to return the not-ready error pre-wiring.
func TestServeBindFirst_EngineOpWorks_RuntimeOpsGated(t *testing.T) {
	localURL, eng := startCountingEngine(t)
	local := graphclient.NewGraphClientForURL(localURL)
	t.Cleanup(local.CloseIdleConnections)
	// Logged-out client → router dispatches to the local fake engine. Readiness
	// flags default false (zero value) — the bind-first wiring window.
	c := closeRouterOnCleanup(t, buildE2EClient(local, "http://cloud.invalid", newFakeAuthStore(), 0))

	ctx := opCtx()

	call := func(name string, args map[string]any) (kgtools.ToolResult, bool) {
		raw, err := json.Marshal(args)
		require.NoError(t, err)
		_, handled, res := c.runInterceptChain(ctx, kgtools.CallToolParams{Name: name, Arguments: raw})
		return res, handled
	}
	bodyOf := func(res kgtools.ToolResult) string {
		if len(res.Content) == 0 {
			return ""
		}
		return res.Content[0].Text
	}

	// (2) Runtime-INDEPENDENT mutate succeeds pre-wiring via engineDispatch →
	// router.Execute against the fake local engine. No readiness flag consulted.
	t.Run("mutate-engine-op-ungated", func(t *testing.T) {
		before := eng.execute.Load()
		mutateArgs, err := json.Marshal(map[string]any{
			"operation": "create", "type": "finding",
			"name": "bind-first probe", "summary": "engine op works pre-wiring",
		})
		require.NoError(t, err)
		res, derr := c.engineDispatch(ctx, "mutate", mutateArgs)
		require.NoError(t, derr, "engine mutate must dispatch pre-wiring: %s", bodyOf(res))
		assert.Greater(t, eng.execute.Load(), before, "the mutate reached the engine (no readiness gate)")
		assert.NotContains(t, bodyOf(res), "daemon still starting",
			"a runtime-independent engine op must NOT be gated pre-wiring")
	})

	// (3) Runtime-DEPENDENT ops return the loud not-ready error while their flag is
	// false — and never panic (the test process would crash on an ungated nil deref).
	gatedCases := []struct {
		name string
		tool string
		args map[string]any
	}{
		{"knowledge-search", "search", map[string]any{"query": "anything", "graph": "knowledge"}},
		{"knowledge-query-text", "query", map[string]any{"text": "anything", "mode": "text", "graph": "knowledge"}},
		{"propagate-force-full", "thoughts", map[string]any{"operation": "propagate", "force_full": true}},
	}
	for _, tc := range gatedCases {
		t.Run("gated/"+tc.name, func(t *testing.T) {
			require.False(t, c.PipelineReady() || c.PropReady(),
				"this subtest requires the pre-wiring window (all flags false)")
			res, handled := call(tc.tool, tc.args)
			require.True(t, handled, "the gated op must be handled client-side, not fall through")
			require.True(t, res.IsError, "a not-ready op must be an error result")
			assert.Contains(t, bodyOf(res), "daemon still starting",
				"%s must return the loud not-ready error pre-wiring", tc.name)
		})
	}

	// (4) Flip the flags (mimicking wireRuntimesBackground completing each stage) —
	// the same ops now reach the wired/degrade path WITHOUT panicking. The runtimes
	// are nil here (no real wiring in this harness), so they hit their permanent
	// degrade message, NOT the not-ready message — and crucially do not panic.
	c.markPropReady()
	c.markPipelineReady()

	for _, tc := range gatedCases {
		t.Run("ready/"+tc.name, func(t *testing.T) {
			res, handled := call(tc.tool, tc.args)
			require.True(t, handled)
			// Reaching here without a panic is the core assertion. Post-flip the
			// ops degrade (nil runtime / nil segment engine) rather than emit the
			// wiring-window message.
			assert.NotContains(t, bodyOf(res), "daemon still starting",
				"%s must leave the wiring-window path once its flag is set", tc.name)
		})
	}
}
