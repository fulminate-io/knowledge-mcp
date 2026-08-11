// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"sync"
	"testing"
	"time"

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

// discoveryRecorder records the operation stamped on every Execute the
// graph-catalog discovery path issues, and signals the first one so a test
// driving the background loop can stop it deterministically instead of sleeping.
// It is mutex-guarded rather than reusing opRecordingWireClient because the
// discovery pass registers a collector, whose own scans then run concurrently
// with the assertion.
type discoveryRecorder struct {
	mu        sync.Mutex
	executeOp graphclient.Operation

	seenOnce sync.Once
	seen     chan struct{}
}

func newDiscoveryRecorder() *discoveryRecorder {
	return &discoveryRecorder{seen: make(chan struct{})}
}

func (d *discoveryRecorder) PipelineScan(
	_ context.Context, _ *knowledgev1.PipelineScanRequest,
) (*knowledgev1.PipelineScanResponse, error) {
	return &knowledgev1.PipelineScanResponse{}, nil
}

func (d *discoveryRecorder) PipelineGenPoll(
	_ context.Context, _ *knowledgev1.PipelineGenPollRequest,
) (*knowledgev1.PipelineGenPollResponse, error) {
	return &knowledgev1.PipelineGenPollResponse{}, nil
}

func (d *discoveryRecorder) Execute(
	ctx context.Context, _ *knowledgev1.ExecuteRequest,
) (*knowledgev1.ExecuteResponse, error) {
	d.mu.Lock()
	d.executeOp, _ = graphclient.OperationFromContext(ctx)
	d.mu.Unlock()
	d.seenOnce.Do(func() { close(d.seen) })
	return &knowledgev1.ExecuteResponse{}, nil
}

func (d *discoveryRecorder) op() graphclient.Operation {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.executeOp
}

// TestGraphDiscoveryStampsOperation guards the graph-CATALOG discovery path: one
// graph-names read per eligible graph type on every pass it runs. Both entry
// points into that path are covered because they carry different contexts and
// neither inherits from the other — the loop runs under the daemon wire ctx and
// the boot seed under a fresh bootstrap ctx, so a stamp on one says nothing about
// the other.
func TestGraphDiscoveryStampsOperation(t *testing.T) {
	t.Run("the discovery loop stamps its own operation", func(t *testing.T) {
		rec := newDiscoveryRecorder()
		p := New(Config{}, rec, nil, nil)
		t.Cleanup(func() { p.UnregisterGraph(kgtypes.GraphKnowledge, "default") })

		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			defer close(done)
			p.RefreshLoadedGraphs(ctx)
		}()

		// The loop is wake-driven and issues nothing until it is signaled, so the
		// stamp can only be observed on a pass a wake actually buys.
		p.wakeCatalog()

		select {
		case <-rec.seen:
		case <-time.After(10 * time.Second):
			cancel()
			t.Fatal("the discovery loop issued no catalog read within 10s")
		}
		cancel()
		<-done

		assert.Equal(t, graphclient.OpPipelineGraphDiscovery, rec.op(),
			"the discovery loop must stamp its own operation — it has no originating tool call to inherit one from")
	})

	t.Run("the boot seed stamps the same operation", func(t *testing.T) {
		rec := newDiscoveryRecorder()
		p := New(Config{}, rec, nil, nil)
		t.Cleanup(func() { p.UnregisterGraph(kgtypes.GraphKnowledge, "default") })

		p.RefreshOnceForBoot(context.Background())

		assert.Equal(t, graphclient.OpPipelineGraphDiscovery, rec.op(),
			"the one-shot boot seed runs the SAME catalog read under a fresh bootstrap ctx and must be stamped too")
	})
}
