// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
)

// TestPropagationLoopStampsOperation is the thought package's half of the
// query-origin completeness gate (graphclient's TestOperationEntryPoints covers
// the tool-dispatch family and cannot reach this constructor). The propagation
// loop is a daemon-lifetime background loop with no originating tool call, so
// every RPC of every pass — the thought browse, the bulk edge read, the node
// hydrate, the metadata writeback — derives from the ONE base context minted
// here. Left unstamped, all of it arrives labeled client.unstamped, which is
// indistinguishable in the metrics from a real stamping bug elsewhere.
func TestPropagationLoopStampsOperation(t *testing.T) {
	t.Parallel()

	t.Run("the loop's base context carries the propagation operation", func(t *testing.T) {
		t.Parallel()

		p := NewPropagationLoop(nil, time.Hour)
		t.Cleanup(p.baseCancel)

		op, ok := graphclient.OperationFromContext(p.baseContext())
		require.True(t, ok,
			"the loop's base ctx carries NO operation — every pass derives from it, so the whole loop would report as client.unstamped")
		assert.Equal(t, graphclient.OpPropagationReflect, op,
			"the base ctx must carry the propagation term so passes without their own per-call-site stamp are still attributable")
	})

	t.Run("a per-call-site stamp still overrides the base term", func(t *testing.T) {
		t.Parallel()

		// Innermost-wins: the two existing per-call-site stamps in this package
		// (reflect_gen.go's probe and loop_corpus.go's CorpusDelta drain) sit
		// BELOW the base ctx, so the base stamp must not shadow them.
		p := NewPropagationLoop(nil, time.Hour)
		t.Cleanup(p.baseCancel)

		ctx := graphclient.WithOperation(p.baseContext(), graphclient.OpCorpusDeltaDrain)
		op, ok := graphclient.OperationFromContext(ctx)
		require.True(t, ok)
		assert.Equal(t, graphclient.OpCorpusDeltaDrain, op,
			"the base stamp is a FLOOR, not a ceiling — a narrower per-call-site term must still win")
	})
}

// TestPropagationLoopBaseContextNilGuard pins that the nil-baseCtx fallback used
// by direct struct-literal construction (the gate/rehydrate test fakes) is
// unaffected by the stamp: it has no ctx to stamp, and callers of those fakes do
// not reach the wire.
func TestPropagationLoopBaseContextNilGuard(t *testing.T) {
	t.Parallel()

	p := &PropagationLoop{}
	assert.Equal(t, context.Background(), p.baseContext())
}
