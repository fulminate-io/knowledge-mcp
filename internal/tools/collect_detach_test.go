// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector"
	"github.com/fulminate-io/knowledge-mcp/internal/collectorwire"
	"github.com/fulminate-io/knowledge-mcp/internal/embed"
	"github.com/fulminate-io/knowledge-mcp/internal/hivemonitor"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/postpopulate"
)

const detachSuccessText = "Collected code /repo — streamed to server."

// TestCollectWaitOrDetach_EarlyReturn: a work that blocks past the (tiny, injected)
// detach threshold returns a NON-error STILL-RUNNING text carrying the verbatim
// manage(status) pointer, and returns promptly (well under the goroutine-guard
// timeout).
func TestCollectWaitOrDetach_EarlyReturn(t *testing.T) {
	rt := NewCollectRuntime()
	rt.detachAfter = 50 * time.Millisecond
	release := make(chan struct{})
	defer close(release)

	done := make(chan kgtools.ToolResult, 1)
	go func() {
		done <- collectWaitOrDetach(rt, "code\x00/repo", "code /repo", detachSuccessText, func() error {
			<-release
			return nil
		})
	}()
	select {
	case res := <-done:
		assert.False(t, res.IsError)
		body := resultText(res)
		assert.Contains(t, body, "STILL RUNNING")
		assert.Contains(t, body, `manage({"operation":"status"})`)
	case <-time.After(2 * time.Second):
		t.Fatal("collectWaitOrDetach blocked past the detach threshold instead of returning early")
	}
}

// TestCollectWaitOrDetach_SubThresholdByteIdentical: a work that returns nil before
// the threshold yields the CURRENT success text byte-for-byte.
func TestCollectWaitOrDetach_SubThresholdByteIdentical(t *testing.T) {
	rt := NewCollectRuntime()
	rt.detachAfter = time.Hour // completion always wins the race
	res := collectWaitOrDetach(rt, "code\x00/repo", "code /repo", detachSuccessText, func() error { return nil })
	assert.False(t, res.IsError)
	assert.Equal(t, detachSuccessText, resultText(res))
}

// TestCollectWaitOrDetach_ErrorPassthrough: a work error surfaces as an IsError
// result carrying the error text.
func TestCollectWaitOrDetach_ErrorPassthrough(t *testing.T) {
	rt := NewCollectRuntime()
	rt.detachAfter = time.Hour
	res := collectWaitOrDetach(rt, "code\x00/repo", "code /repo", detachSuccessText, func() error {
		return errors.New("collect code: boom")
	})
	assert.True(t, res.IsError)
	assert.Contains(t, resultText(res), "collect code: boom")
}

// TestCollectWaitOrDetach_CoalesceMessage: a second launch of the same target while
// one is in flight returns the "already running / not starting a duplicate" text.
func TestCollectWaitOrDetach_CoalesceMessage(t *testing.T) {
	rt := NewCollectRuntime()
	rt.detachAfter = time.Hour
	release := make(chan struct{})
	defer close(release)

	// Capture (not discard) the first launch's result so it isn't provably unused —
	// it settles after release closes at test end.
	firstDone := make(chan kgtools.ToolResult, 1)
	go func() {
		firstDone <- collectWaitOrDetach(rt, "code\x00/repo", "code /repo", detachSuccessText, func() error {
			<-release
			return nil
		})
	}()
	require.Eventually(t, func() bool {
		snap := rt.Snapshot()
		return len(snap) == 1 && snap[0].State == "running"
	}, 2*time.Second, 5*time.Millisecond, "first run should register as running")

	res := collectWaitOrDetach(rt, "code\x00/repo", "code /repo", detachSuccessText, func() error { return nil })
	body := resultText(res)
	assert.Contains(t, body, "already running")
	assert.Contains(t, body, "not starting a duplicate")
}

// --- Full-path detached-completion (reviewer T2-2) ---------------------------

// detachFullPathType is the unique stub collector type driven through the full
// InterceptCollect detached path.
const detachFullPathType = "detach-fullpath-test"

var (
	detachStubOnce sync.Once
	// detachStubStarted is closed by the stub Collect on entry; detachStubRelease
	// gates its return. Both set fresh by the single test that uses this stub.
	detachStubStarted chan struct{}
	detachStubRelease chan struct{}
)

// detachStubCollector blocks in Collect until the test signals, so the run exceeds
// the injected detach threshold and InterceptCollect returns STILL-RUNNING first.
type detachStubCollector struct{}

func (detachStubCollector) Name() string { return detachFullPathType }

func (detachStubCollector) Collect(_ context.Context, _ string, _ collector.CollectOptions) (*collectorwire.CollectResult, error) {
	close(detachStubStarted)
	<-detachStubRelease
	return &collectorwire.CollectResult{GraphType: kgtypes.GraphCloud, GraphName: "detach-smoke"}, nil
}

func registerDetachStub() {
	detachStubOnce.Do(func() { collector.Register(detachStubCollector{}) })
}

// detachFullDeps is a ClientDeps that provides the standing runtime
// (collectRuntimeProvider), records WakePipeline (pipelineWaker), reports the
// collect snapshot (collectRunReporter), and routes the post-collect tail through a
// recording GraphCaller.
type detachFullDeps struct {
	rt   *CollectRuntime
	gc   GraphCaller
	wake atomic.Int32
}

func (d *detachFullDeps) CollectRuntime() *CollectRuntime              { return d.rt }
func (d *detachFullDeps) CollectRunSnapshot() []CollectRunStatus       { return d.rt.Snapshot() }
func (d *detachFullDeps) WakePipeline()                                { d.wake.Add(1) }
func (d *detachFullDeps) LocalLiveness() LocalLiveness                 { return nil }
func (d *detachFullDeps) Sink() collector.Sink                         { return noopSink{} }
func (d *detachFullDeps) RootDir() string                              { return "" }
func (d *detachFullDeps) UsageAnalyzer() UsageAnalyzerAPI              { return nil }
func (d *detachFullDeps) WorkerRuntime() WorkerRuntimeAPI              { return nil }
func (d *detachFullDeps) WorkerReady() bool                            { return true }
func (d *detachFullDeps) PropReady() bool                              { return true }
func (d *detachFullDeps) PipelineReady() bool                          { return true }
func (d *detachFullDeps) ClaimRegistry() *hivemonitor.Registry         { return nil }
func (d *detachFullDeps) BanSet() *hivemonitor.BanSet                  { return nil }
func (d *detachFullDeps) WorkerCRUD() WorkerCRUDAPI                    { return nil }
func (d *detachFullDeps) GraphTypeCRUD() GraphTypeCRUDAPI              { return nil }
func (d *detachFullDeps) Embedder() embed.BinaryEmbedder               { return nil }
func (d *detachFullDeps) BackendResolver() BackendResolver             { return nil }
func (d *detachFullDeps) GraphCaller() GraphCaller                     { return d.gc }
func (d *detachFullDeps) LocalGraphCaller() GraphCaller                { return d.gc }
func (d *detachFullDeps) SegmentManager() SegmentSearcher              { return nil }
func (d *detachFullDeps) SegmentVectorResolver() SegmentVectorResolver { return nil }
func (d *detachFullDeps) SegmentShipper() SegmentShipper               { return nil }
func (d *detachFullDeps) SegmentPruner() SegmentPruner                 { return nil }
func (d *detachFullDeps) SegmentCoverage() SegmentCoverageReader       { return nil }
func (d *detachFullDeps) PipelineScanner() PipelineScanner             { return nil }
func (d *detachFullDeps) ReflectionForcer() ReflectionForcer           { return nil }
func (d *detachFullDeps) SimilarityForcer() SimilarityForcer           { return nil }
func (d *detachFullDeps) BlindSpotProvider() BlindSpotProvider         { return nil }
func (d *detachFullDeps) ClusterProvider() ClusterProvider             { return nil }
func (d *detachFullDeps) TensionsProvider() TensionsProvider           { return nil }

// TestInterceptCollect_DetachedCompletionRunsTail drives the FULL InterceptCollect
// detached path: a blocking stub collector makes the run exceed the injected detach
// threshold, so InterceptCollect returns STILL-RUNNING first with a running
// snapshot. After the collector is signaled, the post-collect tail (linker +
// postpopulate hook + WakePipeline) fires on the detached goroutine, and the
// snapshot transitions running -> completed. Because WakePipeline is the LAST tail
// step in builtinCollectWork — strictly after runPostCollectLinker and
// runPostCollectPostPopulate in straight-line code — observing it fire proves the
// whole tail (including the linker) executed on the detached path.
func TestInterceptCollect_DetachedCompletionRunsTail(t *testing.T) {
	registerDetachStub()

	// Map the stub type into the postpopulate + linker gates so both tail helpers
	// execute on the detached goroutine; restore afterwards.
	prevPP, hadPP := postPopulateGraphType[detachFullPathType]
	postPopulateGraphType[detachFullPathType] = kgtypes.GraphCloud
	prevLink, hadLink := postCollectLinkerTypes[detachFullPathType]
	postCollectLinkerTypes[detachFullPathType] = true
	t.Cleanup(func() {
		if hadPP {
			postPopulateGraphType[detachFullPathType] = prevPP
		} else {
			delete(postPopulateGraphType, detachFullPathType)
		}
		if hadLink {
			postCollectLinkerTypes[detachFullPathType] = prevLink
		} else {
			delete(postCollectLinkerTypes, detachFullPathType)
		}
	})

	var ppFired atomic.Int32
	postpopulate.Register(detachFullPathType, func(_ context.Context, _ postpopulate.GraphCaller, _ string) error {
		ppFired.Add(1)
		return nil
	})

	detachStubStarted = make(chan struct{})
	detachStubRelease = make(chan struct{})

	rt := NewCollectRuntime()
	rt.detachAfter = 50 * time.Millisecond
	fc := &fakeGraphCaller{
		listGraphsResult: &kgtools.ToolResult{
			Content: []kgtools.ContentBlock{{Type: "text", Text: `{"graphs":[{"graph_type":"cloud","graph_name":"aws-acct-1"}]}`}},
		},
	}
	deps := &detachFullDeps{rt: rt, gc: fc}

	args := json.RawMessage(`{"type":"` + detachFullPathType + `","id":"detach-id","force":true}`)
	resCh := make(chan kgtools.ToolResult, 1)
	go func() {
		_, res := InterceptCollect(deps, kgtools.CallToolParams{Name: "collect", Arguments: args})
		resCh <- res
	}()

	<-detachStubStarted // the collector is in flight

	var res kgtools.ToolResult
	select {
	case res = <-resCh:
	case <-time.After(3 * time.Second):
		t.Fatal("InterceptCollect did not return the STILL-RUNNING message while the collect ran long")
	}
	require.False(t, res.IsError, resultText(res))
	assert.Contains(t, resultText(res), "STILL RUNNING")

	// The run is still blocked: snapshot shows running, and no tail step has fired.
	snap := deps.CollectRunSnapshot()
	require.Len(t, snap, 1)
	assert.Equal(t, "running", snap[0].State)
	assert.Equal(t, int32(0), ppFired.Load(), "tail must not fire until the detached collect completes")
	assert.Equal(t, int32(0), deps.wake.Load())

	// Signal completion; the detached goroutine now runs the post-collect tail.
	close(detachStubRelease)

	require.Eventually(t, func() bool {
		s := deps.CollectRunSnapshot()
		return len(s) == 1 && s[0].State == "completed"
	}, 3*time.Second, 10*time.Millisecond, "snapshot must transition running -> completed after detached completion")

	assert.Equal(t, int32(1), ppFired.Load(), "postpopulate hook fired AFTER the detached completion")
	assert.GreaterOrEqual(t, deps.wake.Load(), int32(1), "WakePipeline fired AFTER the detached completion (proving the linker step before it also ran)")
}
