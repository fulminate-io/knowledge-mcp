// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"

	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
)

// opRecordingHiveCaller records the operation on the ctx each half of the
// HiveCaller seam is called with.
type opRecordingHiveCaller struct {
	hiveOp    graphclient.Operation
	hiveOK    bool
	executeOp graphclient.Operation
	executeOK bool
}

func (c *opRecordingHiveCaller) Hive(
	ctx context.Context, _ *knowledgev1.HiveRequest,
) (*knowledgev1.HiveResponse, error) {
	c.hiveOp, c.hiveOK = graphclient.OperationFromContext(ctx)
	return &knowledgev1.HiveResponse{}, nil
}

func (c *opRecordingHiveCaller) Execute(
	ctx context.Context, _ *knowledgev1.ExecuteRequest,
) (*knowledgev1.ExecuteResponse, error) {
	c.executeOp, c.executeOK = graphclient.OperationFromContext(ctx)
	return &knowledgev1.ExecuteResponse{}, nil
}

// TestHiveCallerStampsOperation is the bootstrap package's half of the
// query-origin completeness gate. The hive daemons (lease monitor + machine-down
// reaper) tick on their own detached contexts with no originating tool call, so
// the stamping wrapper is the only thing keeping their RPCs out of the
// client.unstamped bucket — and both halves of the seam matter: the reaper's
// sweep issues its role-gate and stale-member scans through Execute and its
// evictions through Hive.
func TestHiveCallerStampsOperation(t *testing.T) {
	t.Parallel()

	t.Run("the graph read half is stamped", func(t *testing.T) {
		t.Parallel()

		inner := &opRecordingHiveCaller{}
		_, err := hiveCallerStampingOperation{inner: inner}.Execute(
			context.Background(), &knowledgev1.ExecuteRequest{})
		require.NoError(t, err)

		require.True(t, inner.executeOK, "the hive_member read reached the wire with NO operation on ctx")
		assert.Equal(t, graphclient.OpHiveMonitor, inner.executeOp)
	})

	t.Run("the hive op half is stamped", func(t *testing.T) {
		t.Parallel()

		inner := &opRecordingHiveCaller{}
		_, err := hiveCallerStampingOperation{inner: inner}.Hive(
			context.Background(), &knowledgev1.HiveRequest{})
		require.NoError(t, err)

		require.True(t, inner.hiveOK, "the renew/evict call reached the wire with NO operation on ctx")
		assert.Equal(t, graphclient.OpHiveMonitor, inner.hiveOp)
	})
}
