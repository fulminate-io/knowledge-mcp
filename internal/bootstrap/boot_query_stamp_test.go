// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
)

// boot_query_stamp_test.go is the ONE-SHOT half of the query-origin completeness
// gate. The other gates cannot reach this call: TestOperationEntryPoints scopes
// to tool dispatch by design. What is left is the one query a daemon still
// issues while booting — the instruction-bootstrap seed — which is precisely
// the residual client.unstamped bucket observed in production.

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
