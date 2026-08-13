// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// TestPipelineLoopsStampOperation is the pipeline package's half of the
// query-origin completeness gate (graphclient's TestOperationEntryPoints covers
// the tool-dispatch family and cannot reach these unexported loop functions).
// The pipeline is a background daemon with no originating tool call, so if it
// did not stamp its own operation its RPCs would arrive labeled client.unstamped
// — indistinguishable in the metrics from a real client stamping bug.
//
// It is named distinctly from the graphclient gate on purpose: two tests sharing
// one name across packages would let a passing one mask a failing one under the
// anchored `--- PASS` grep the criteria use.
type opRecordingWireClient struct {
	scanOp    graphclient.Operation
	genPollOp graphclient.Operation
	executeOp graphclient.Operation
}

func (c *opRecordingWireClient) PipelineScan(
	ctx context.Context, _ *knowledgev1.PipelineScanRequest,
) (*knowledgev1.PipelineScanResponse, error) {
	c.scanOp, _ = graphclient.OperationFromContext(ctx)
	return &knowledgev1.PipelineScanResponse{}, nil
}

func (c *opRecordingWireClient) PipelineGenPoll(
	ctx context.Context, _ *knowledgev1.PipelineGenPollRequest,
) (*knowledgev1.PipelineGenPollResponse, error) {
	c.genPollOp, _ = graphclient.OperationFromContext(ctx)
	return &knowledgev1.PipelineGenPollResponse{}, nil
}

func (c *opRecordingWireClient) Execute(
	ctx context.Context, _ *knowledgev1.ExecuteRequest,
) (*knowledgev1.ExecuteResponse, error) {
	c.executeOp, _ = graphclient.OperationFromContext(ctx)
	return &knowledgev1.ExecuteResponse{}, nil
}

func TestPipelineLoopsStampOperation(t *testing.T) {
	t.Run("gap scan stamps its own operation", func(t *testing.T) {
		c := &opRecordingWireClient{}
		_, _, err := scanGaps(context.Background(), c, kgtypes.GraphKnowledge, "default", "embed", 10, 0)
		require.NoError(t, err)
		assert.Equal(t, graphclient.OpPipelineGapScan, c.scanOp,
			"the gap scan must stamp its own operation — it has no originating tool call to inherit one from")
	})

	t.Run("writeback stamps its own operation", func(t *testing.T) {
		c := &opRecordingWireClient{}
		summary := "s"
		err := writeBatchUpdates(context.Background(), c, kgtypes.GraphKnowledge, "default",
			[]updateBatchItem{{ID: "n1", Summary: &summary}})
		require.NoError(t, err)
		assert.Equal(t, graphclient.OpPipelineEmbedWriteback, c.executeOp,
			"the writeback is the pipeline's WRITE half and must be distinguishable from its scans")
	})
}
