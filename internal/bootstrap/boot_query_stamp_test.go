// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/dream"
	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
)

// boot_query_stamp_test.go is the ONE-SHOT half of the query-origin completeness
// gate. The other gates cannot reach these two calls: TestOperationEntryPoints
// scopes to tool dispatch by design, and hive_loops_stamp_test.go covers the
// recurring background loops. What is left are the queries a daemon issues
// exactly once while booting — the instruction-bootstrap seed and the worker
// runtime's registry validation — which is precisely the residual
// client.unstamped bucket observed in production.

// opRecordingLister records the query-origin operation the dream Registry's
// worker list is called with. It satisfies dream's unexported workerLister.
type opRecordingLister struct {
	op graphclient.Operation
	ok bool
}

func (l *opRecordingLister) List(ctx context.Context) ([]dream.Worker, error) {
	l.op, l.ok = graphclient.OperationFromContext(ctx)
	return nil, nil
}

// TestInstructionBootstrapStampsOperation asserts BOTH graph calls the
// instruction bootstrap issues carry the instruction.bootstrap operation. The
// idempotency pre-flight is the one that matters: on a daemon whose graph is
// already seeded it is the ONLY call the bootstrap makes, so leaving it
// unstamped means every restart of an established install contributes an
// unattributable read.
func TestInstructionBootstrapStampsOperation(t *testing.T) {
	t.Parallel()

	t.Run("the pre-flight read is stamped", func(t *testing.T) {
		t.Parallel()

		// queryNodeCount > 0 → the bootstrap short-circuits after the pre-flight,
		// which is the established-install shape.
		fc := &fakeBootstrapGC{queryNodeCount: 3}
		require.NoError(t, runInstructionBootstrap(context.Background(), fc, t.TempDir()))

		require.Len(t, fc.calls, 1)
		require.True(t, fc.calls[0].opOK, "the idempotency pre-flight reached the wire with NO operation on ctx")
		assert.Equal(t, graphclient.OpInstructionBootstrap, fc.calls[0].op)
	})

	t.Run("the create_batch seed is stamped", func(t *testing.T) {
		t.Parallel()

		fc := &fakeBootstrapGC{queryNodeCount: 0}
		dir := t.TempDir()
		makeBootstrapDirs(t, dir, 1, 1)
		require.NoError(t, runInstructionBootstrap(context.Background(), fc, dir))

		require.Len(t, fc.calls, 2)
		for _, call := range fc.calls {
			require.True(t, call.opOK, "the %s call reached the wire with NO operation on ctx", call.tool)
			assert.Equal(t, graphclient.OpInstructionBootstrap, call.op, "call: %s", call.tool)
		}
	})
}

// TestWorkerRuntimeStartStampsOperation asserts the boot-time dream
// Runner.Start carries its own operation all the way to the wire: Start loads
// the registry (Runner.Start → Registry.All → lister.List), so an unstamped ctx
// at the call site surfaces as an anonymous worker browse on every daemon boot.
// The term is deliberately NOT OpWorker — that one means a user invoked the
// worker tool, and collapsing boot validation into it would hide exactly the
// distinction the metrics dimension exists to make.
func TestWorkerRuntimeStartStampsOperation(t *testing.T) {
	t.Parallel()

	lister := &opRecordingLister{}
	runner := dream.NewRunner(dream.NewRegistry(lister), dream.NewEventBus(), os.TempDir(), nil, nil)

	require.NoError(t, startWorkerRuntime(context.Background(), runner))

	require.True(t, lister.ok, "the boot worker browse reached the wire with NO operation on ctx")
	assert.Equal(t, graphclient.OpWorkerRuntimeStart, lister.op)
}
