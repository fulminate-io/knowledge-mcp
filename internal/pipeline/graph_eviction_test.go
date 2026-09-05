// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/llmproviders"
)

// scanFailingWireClient answers every PipelineScan with a scripted error and
// COUNTS the attempts.
//
// THE COUNT IS WHAT DISTINGUISHES A LANE THAT STOPPED from one still re-firing
// behind a backoff. Without it, "the eviction fired" would be satisfied by a
// collector that evicted AND kept scanning forever, which is half the defect.
//
// THE SCRIPTED ERROR IS WRAPPED WITH %w because that is the production shape —
// the scan seam wraps — and an unwrapped fixture would never exercise the
// classifier's unwrap, passing against a classifier that only type-asserts.
type scanFailingWireClient struct {
	mu    sync.Mutex
	scans int
	err   error
}

func (c *scanFailingWireClient) PipelineGenPoll(
	context.Context, *knowledgev1.PipelineGenPollRequest,
) (*knowledgev1.PipelineGenPollResponse, error) {
	return &knowledgev1.PipelineGenPollResponse{}, nil
}

func (c *scanFailingWireClient) PipelineScan(
	context.Context, *knowledgev1.PipelineScanRequest,
) (*knowledgev1.PipelineScanResponse, error) {
	c.mu.Lock()
	c.scans++
	c.mu.Unlock()
	return nil, fmt.Errorf("pipeline scan: %w", c.err)
}

func (c *scanFailingWireClient) Execute(
	context.Context, *knowledgev1.ExecuteRequest,
) (*knowledgev1.ExecuteResponse, error) {
	return &knowledgev1.ExecuteResponse{}, nil
}

func (c *scanFailingWireClient) scanCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.scans
}

// noopSummarizer is enough to ENABLE the summary axis. A Pipeline built with a
// nil summarizer AND a nil embedder leaves both axes disabled, the collector
// launches no loop at all, and a test waiting on the loop times out reading
// exactly like a genuine red.
func noopSummarizer(context.Context, []llmproviders.BatchChunk) (map[string]llmproviders.SummarizeResult, error) {
	return map[string]llmproviders.SummarizeResult{}, nil
}

// evictionTestPipeline builds a Pipeline over the scripted scan backend with the
// summary axis enabled and a fast tick.
func evictionTestPipeline(fake *scanFailingWireClient) *Pipeline {
	return New(Config{
		SummaryBatchSize: 4, SummaryWorkers: 1,
		EmbedBatchSize: 4, EmbedWorkers: 1,
		Tick: 5 * time.Millisecond, CloudTick: 5 * time.Millisecond, IdleTickMax: 5 * time.Millisecond,
	}, fake, noopSummarizer, nil)
}

// TestCollector_DurableNotFoundEvictsAndStops is the convergence property: a
// graph the server says does not exist ENDS its lane rather than backing off
// forever.
func TestCollector_DurableNotFoundEvictsAndStops(t *testing.T) {
	fake := &scanFailingWireClient{err: connect.NewError(connect.CodeNotFound, errors.New("graph not found"))}
	p := evictionTestPipeline(fake)

	var mu sync.Mutex
	var evictions []string
	c := newCollector(kgtypes.GraphCode, "phantom", p.cfg, p.summaryCh, p.embedCh, p.metrics, p.client,
		p.cfg.TickOrDefault(), p.cfg.TickOrDefault(), nil, nil,
		p.summaryEnabled(), p.embedEnabled(), p.genSnapshotFor, nil, nil)
	c.evictOnDurableNotFound = func(reason string) {
		mu.Lock()
		evictions = append(evictions, reason)
		mu.Unlock()
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		c.run(context.Background())
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the collector loop did not RETURN on a durable not-found — it is still backing off, which is the immortal-retry defect")
	}

	mu.Lock()
	got := append([]string(nil), evictions...)
	mu.Unlock()
	require.NotEmpty(t, got, "a durable not-found must evict the working-set member")
	assert.Equal(t, "durable_not_found", got[0], "the eviction reason names the cause")

	assert.LessOrEqual(t, fake.scanCount(), 2,
		"the lane must stop after the not-found, not keep re-scanning behind a backoff")
}

// TestCollector_TransientScanErrorDoesNotEvict is the known-positive control and
// the reason the discrimination is narrow: evicting on ANY error would tear down
// a live graph on one transport blip.
//
// THE POSITIVE SCAN COUNT IS THE SECOND HALF. Without it, "zero evictions" would
// be satisfied by a collector that never scanned at all.
func TestCollector_TransientScanErrorDoesNotEvict(t *testing.T) {
	for _, code := range []connect.Code{
		connect.CodeUnavailable,
		connect.CodeInternal,
		connect.CodeDeadlineExceeded,
	} {
		t.Run(code.String(), func(t *testing.T) {
			fake := &scanFailingWireClient{err: connect.NewError(code, errors.New("transient"))}
			p := evictionTestPipeline(fake)

			var mu sync.Mutex
			evictions := 0
			c := newCollector(kgtypes.GraphCode, "live", p.cfg, p.summaryCh, p.embedCh, p.metrics, p.client,
				p.cfg.TickOrDefault(), p.cfg.TickOrDefault(), nil, nil,
				p.summaryEnabled(), p.embedEnabled(), p.genSnapshotFor, nil, nil)
			c.evictOnDurableNotFound = func(string) {
				mu.Lock()
				evictions++
				mu.Unlock()
			}

			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			go func() {
				defer close(done)
				c.run(ctx)
			}()

			time.Sleep(200 * time.Millisecond)
			cancel()
			<-done

			mu.Lock()
			gotEvictions := evictions
			mu.Unlock()
			assert.Zero(t, gotEvictions,
				"a transient scan error must NOT evict — a live graph would be torn down on one blip")
			assert.Positive(t, fake.scanCount(),
				"the collector must actually have scanned, or the zero above is vacuous")
		})
	}
}

// TestIsGraphNotFound_ClassifiesOnlyNotFound pins the classifier itself.
func TestIsGraphNotFound_ClassifiesOnlyNotFound(t *testing.T) {
	assert.True(t, isGraphNotFound(connect.NewError(connect.CodeNotFound, errors.New("x"))),
		"a bare not-found is the eviction condition")

	assert.True(t, isGraphNotFound(fmt.Errorf("pipeline scan: %w", connect.NewError(connect.CodeNotFound, errors.New("x")))),
		"the scan seam wraps with %w, so the classifier MUST unwrap — a type assertion would miss every production error")

	for _, code := range []connect.Code{
		connect.CodeUnavailable,
		connect.CodeInternal,
		connect.CodeDeadlineExceeded,
		connect.CodePermissionDenied,
		connect.CodeResourceExhausted,
	} {
		assert.False(t, isGraphNotFound(connect.NewError(code, errors.New("x"))),
			"%s is not a durable per-graph absence", code)
	}

	assert.False(t, isGraphNotFound(nil), "a nil error classifies as nothing")
	assert.False(t, isGraphNotFound(errors.New("plain non-wire error")),
		"a non-wire error carries no code and must not evict")
}

// TestRegisterGraph_EvictionClosureRemovesTheMemberAndWakesTheCatalog drives
// RegisterGraph ITSELF, because the closure's WIRING is what two plausible-wrong
// implementations get wrong and no collector-level test can see.
//
// NO BALANCE FACTORY IS ATTACHED, DELIBERATELY. p.balanceFactory is nil whenever
// no segment manager is wired, and the graphs most likely to be phantoms are
// exactly the ones with no segments — so a closure placed inside that guard is
// silently disabled on precisely the configuration it exists for. This test is
// that configuration.
func TestRegisterGraph_EvictionClosureRemovesTheMemberAndWakesTheCatalog(t *testing.T) {
	fake := &scanFailingWireClient{err: connect.NewError(connect.CodeNotFound, errors.New("graph not found"))}
	p := evictionTestPipeline(fake)

	var mu sync.Mutex
	var evicted []string
	p.AttachGraphEvictor(func(gt kgtypes.GraphType, name, reason string) {
		mu.Lock()
		evicted = append(evicted, string(gt)+"/"+name+"/"+reason)
		mu.Unlock()
	})

	// Drain any queued catalog wake first, so the one asserted below is THIS
	// eviction's rather than one left over from construction.
	select {
	case <-p.catalogWake:
	default:
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.RegisterGraph(ctx, kgtypes.GraphCode, "phantom")

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(evicted) > 0
	}, 10*time.Second, 10*time.Millisecond,
		"THE EVICTION HALF: the closure RegisterGraph builds must reach the attached evictor — a closure built inside the balanceFactory guard never fires here, because no balance factory is attached")

	mu.Lock()
	got := append([]string(nil), evicted...)
	mu.Unlock()
	assert.Equal(t, "code/phantom/durable_not_found", got[0],
		"the evictor receives the graph type, the name and the reason")

	select {
	case <-p.catalogWake:
	case <-time.After(2 * time.Second):
		t.Fatal("THE WAKE HALF: the catalog was never woken. The working set IS the wanted set refreshOnce diffs, and the refresh loop is wake-driven — without this wake the unregistration waits until some unrelated graph is admitted")
	}

	cancel()
	p.collectorWG.Wait()
}

// TestRegisterGraph_InternalScanErrorKeepsTheMember is the membership-level
// control for the eviction closure, and the direction the not-found test above
// cannot cover.
//
// WHY IT IS NOT A DUPLICATE OF TestCollector_TransientScanErrorDoesNotEvict.
// That test wires the callback by hand onto a collector it constructed, so it
// pins the COLLECTOR's discrimination. This one drives RegisterGraph, so what it
// pins is that the closure RegisterGraph builds — the one that actually removes
// the working-set member and wakes the catalog — is not reached by a fault. A
// graph whose enrichment work is still pending must keep its membership when the
// server reports a fault, because the fault is retryable and the absence is not.
//
// THE SERVER IS WHAT MAKES THIS REACHABLE. A storage fault on the scan path
// answers CodeInternal rather than CodeNotFound only because the producer
// classifies its Scope failure; the classification this test depends on lives in
// the server's pipeline-scan handler, not here.
func TestRegisterGraph_InternalScanErrorKeepsTheMember(t *testing.T) {
	fake := &scanFailingWireClient{err: connect.NewError(connect.CodeInternal, errors.New("scope: unreadable image"))}
	p := evictionTestPipeline(fake)

	var mu sync.Mutex
	var evicted []string
	p.AttachGraphEvictor(func(gt kgtypes.GraphType, name, reason string) {
		mu.Lock()
		evicted = append(evicted, string(gt)+"/"+name+"/"+reason)
		mu.Unlock()
	})

	// Drain the wake New left queued, so the emptiness asserted below is a reading
	// of THIS graph's lane. RegisterGraph itself never wakes the catalog — the only
	// wakeCatalog on this path is inside the eviction closure — so a wake observed
	// after this point could only have come from an eviction.
	select {
	case <-p.catalogWake:
	default:
	}

	ctx := t.Context()
	p.RegisterGraph(ctx, kgtypes.GraphCode, "faulted")

	// THE KNOWN-POSITIVE FIRST, so the emptiness asserted below is a reading of
	// the classifier and not of a lane that never ran. Waiting on the scan count
	// also gives the eviction its chance to fire: the eviction is the scan-error
	// branch's FIRST statement, so a scan that has been observed is a branch that
	// has already been through it.
	require.Eventually(t, func() bool { return fake.scanCount() > 0 }, 10*time.Second, 10*time.Millisecond,
		"the collector must actually have scanned, or the emptiness below is vacuous")

	mu.Lock()
	got := append([]string(nil), evicted...)
	mu.Unlock()
	assert.Empty(t, got,
		"a fault is not an absence: the working-set member must survive it, or enrichment stops for a graph that is present and has work pending")

	select {
	case <-p.catalogWake:
		t.Fatal("a fault woke the catalog for an unregistration that must not happen")
	default:
	}

	// THE LANE IS STOPPED BY UnregisterGraph, NOT by canceling the ctx passed to
	// RegisterGraph: the collector runs on its own context, stored alongside its
	// cancel and invoked from here or from the stop sequence. Canceling the caller's
	// ctx leaves the loop sleeping out its scan backoff, which is the whole point —
	// a fault BACKS OFF where an absence ENDS, so this lane has to be told to stop.
	p.UnregisterGraph(kgtypes.GraphCode, "faulted")
	p.collectorWG.Wait()
}
