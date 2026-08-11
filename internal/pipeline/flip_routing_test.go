// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"reflect"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/llmproviders"
)

// flippableBackend is a test WireClient + BackendResolver pair over two
// in-process backends (local + cloud). loggedIn flips which backend services
// PipelineScan/Execute and what LoggedIn/Backend report — modeling the
// routedWireClient over a *graphclient.Router across a mid-session login flip.
type flippableBackend struct {
	loggedIn atomic.Bool
	local    *fakeWireClient
	cloud    *fakeWireClient

	mu            sync.Mutex
	cloudScanGens []uint64 // LastSeenGen of every scan the cloud backend served
}

func newFlippableBackend() *flippableBackend {
	return &flippableBackend{local: newFakeWireClient(), cloud: newFakeWireClient()}
}

func (f *flippableBackend) current() *fakeWireClient {
	if f.loggedIn.Load() {
		return f.cloud
	}
	return f.local
}

func (f *flippableBackend) PipelineScan(ctx context.Context, req *knowledgev1.PipelineScanRequest) (*knowledgev1.PipelineScanResponse, error) {
	// listLoadedGraphs only issues Execute; collectors scan through the
	// recordingBackend returned by Backend(). This direct path stays for
	// WireClient completeness.
	return f.current().PipelineScan(ctx, req)
}

func (f *flippableBackend) PipelineGenPoll(ctx context.Context, req *knowledgev1.PipelineGenPollRequest) (*knowledgev1.PipelineGenPollResponse, error) {
	// Delegate to the current concrete backend for WireClient completeness (the
	// gen-poll path is not exercised by the flip-routing tests).
	return f.current().PipelineGenPoll(ctx, req)
}

// cloudSawGenZeroScan reports whether the cloud backend ever served a scan with
// LastSeenGen==0 (a fresh, non-short-circuiting scan — proof a flipped survivor
// re-scans the cloud backend from gen 0).
func (f *flippableBackend) cloudSawGenZeroScan() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Contains(f.cloudScanGens, 0)
}

func (f *flippableBackend) Execute(ctx context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	return f.current().Execute(ctx, req)
}

func (f *flippableBackend) Backend(_ context.Context) (WireClient, error) {
	// Return a recording proxy bound to the CURRENT concrete backend so the
	// collector scans through it (mirroring the production *GraphClient binding)
	// while we capture the LastSeenGen each scan carries.
	loggedIn := f.loggedIn.Load()
	return &recordingBackend{parent: f, inner: f.current(), cloud: loggedIn}, nil
}

// recordingBackend wraps one concrete backend and records the LastSeenGen of
// each scan it serves into the parent flippableBackend (only when bound to the
// cloud backend) so the gen-cache-reset assertion can observe a fresh gen-0 scan.
type recordingBackend struct {
	parent *flippableBackend
	inner  *fakeWireClient
	cloud  bool
}

func (r *recordingBackend) PipelineScan(ctx context.Context, req *knowledgev1.PipelineScanRequest) (*knowledgev1.PipelineScanResponse, error) {
	if r.cloud {
		r.parent.mu.Lock()
		r.parent.cloudScanGens = append(r.parent.cloudScanGens, req.GetLastSeenGen())
		r.parent.mu.Unlock()
	}
	return r.inner.PipelineScan(ctx, req)
}

func (r *recordingBackend) PipelineGenPoll(ctx context.Context, req *knowledgev1.PipelineGenPollRequest) (*knowledgev1.PipelineGenPollResponse, error) {
	return r.inner.PipelineGenPoll(ctx, req)
}

func (r *recordingBackend) Execute(ctx context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	return r.inner.Execute(ctx, req)
}

func (f *flippableBackend) LoggedIn(_ context.Context) bool { return f.loggedIn.Load() }

var (
	_ WireClient      = (*flippableBackend)(nil)
	_ BackendResolver = (*flippableBackend)(nil)
)

// registeredKeys snapshots the current collector key set under the lock.
func registeredKeys(p *Pipeline) map[graphKey]struct{} {
	p.collectorMu.Lock()
	defer p.collectorMu.Unlock()
	out := make(map[graphKey]struct{}, len(p.collectorCancels))
	for k := range p.collectorCancels {
		out[k] = struct{}{}
	}
	return out
}

// cancelFor returns the (function-pointer identity of the) cancel func tracked
// for key, or nil if absent. Used to prove a collector was torn down + recreated
// (the cancel func identity changes) rather than left in place.
func cancelFor(p *Pipeline, key graphKey) uintptr {
	p.collectorMu.Lock()
	defer p.collectorMu.Unlock()
	c, ok := p.collectorCancels[key]
	if !ok {
		return 0
	}
	return reflect.ValueOf(c).Pointer()
}

// TestRefreshOnceSkipsLoginFlip proves the decoupling: the catalog diff no
// longer performs the login-flip teardown, so a flip observed with no
// CheckLoginFlip call leaves the survivor's collector in place. Without this
// negative assertion the decoupling is unfalsifiable — a bare occurrence count
// cannot tell "the call moved" from "the call ran twice".
//
// The discriminator is the RE-REGISTRATION signal, not the collector's cancel
// identity: cancelFor reports a func's code pointer, which is the same literal
// inside context.WithCancel for every collector ever created, so it cannot
// distinguish a torn-down-and-recreated collector from an untouched one. A
// teardown, in contrast, empties the collector set and makes the survivor look
// NEW to the diff, which registers it again and wakes the gen-poll loop. No
// wake means no teardown.
func TestRefreshOnceSkipsLoginFlip(t *testing.T) {
	fb := newFlippableBackend()
	// Both catalogs hold only the survivor, so the wanted/have diff registers
	// nothing on its own: a registration can only come from a teardown.
	fb.local.seedGraphNames(kgtypes.GraphCode, "survivor")
	fb.cloud.seedGraphNames(kgtypes.GraphCode, "survivor")

	p := New(Config{}, fb, nil, nil)
	ctx := context.Background()
	survivor := graphKey{GraphType: kgtypes.GraphCode, GraphName: "survivor"}

	p.refreshOnce(ctx)
	require.Contains(t, registeredKeys(p), survivor, "survivor must be registered logged-out")
	// Known-positive control for the emptiness assertion below: the first pass
	// DID register the survivor, so the probe reports a real registration wake.
	require.True(t, drainWake(p.genPollWake), "the first pass registers the survivor and wakes the gen-poll loop")

	fb.loggedIn.Store(true)
	p.refreshOnce(ctx)

	require.Contains(t, registeredKeys(p), survivor, "survivor must still be registered after the flip")
	assert.False(t, drainWake(p.genPollWake),
		"refreshOnce must not tear down collectors on a login flip — that is CheckLoginFlip's job, driven by the client activity hook")

	require.NoError(t, p.Stop(ctx))
}

// TestRefreshFlipDelta proves the cloud-only (no-union) decision: the registered
// collector set always equals the CURRENT backend's catalog, never the union.
func TestRefreshFlipDelta(t *testing.T) {
	fb := newFlippableBackend()
	// Logged-out catalog: a local-only code repo + the survivor repo.
	fb.local.seedGraphNames(kgtypes.GraphCode, "local-only", "survivor")
	// Logged-in (cloud) catalog: a cloud-only code repo + the survivor repo.
	fb.cloud.seedGraphNames(kgtypes.GraphCode, "cloud-only", "survivor")

	p := New(Config{}, fb, nil, nil)
	ctx := context.Background()

	// Logged out → local catalog registered.
	p.refreshOnce(ctx)
	have := registeredKeys(p)
	assert.Contains(t, have, graphKey{GraphType: kgtypes.GraphCode, GraphName: "local-only"})
	assert.Contains(t, have, graphKey{GraphType: kgtypes.GraphCode, GraphName: "survivor"})
	assert.NotContains(t, have, graphKey{GraphType: kgtypes.GraphCode, GraphName: "cloud-only"})

	// Flip to logged in → cloud catalog registered, local-only unregistered.
	// The flip teardown is driven by CheckLoginFlip (the client activity hook's
	// job in production); refreshOnce only diffs the catalog.
	// The hook runs on EVERY tool call, so in production the pre-flip state is
	// already seeded by the time a flip happens; model that first observation
	// here (it only seeds, and reports no transition).
	require.False(t, p.CheckLoginFlip(ctx), "the first observation seeds the login state, it is not a transition")
	fb.loggedIn.Store(true)
	require.True(t, p.CheckLoginFlip(ctx), "the login transition must be detected")
	p.refreshOnce(ctx)
	have = registeredKeys(p)
	assert.Contains(t, have, graphKey{GraphType: kgtypes.GraphCode, GraphName: "cloud-only"})
	assert.Contains(t, have, graphKey{GraphType: kgtypes.GraphCode, GraphName: "survivor"})
	assert.NotContains(t, have, graphKey{GraphType: kgtypes.GraphCode, GraphName: "local-only"},
		"local-only graph must be unregistered after login (cloud-only, no union)")

	require.NoError(t, p.Stop(ctx))
}

// TestFlipResetsGenCache proves Hazard B: a graphKey present in BOTH catalogs
// (the survivor) is torn down + re-registered on a login flip — even though the
// plain wanted/have diff would leave it in place — so its fresh collector starts
// with a zeroed dirty-gen cache and re-scans the NEW (cloud) backend with
// LastSeenGen=0 (the server does NOT short-circuit, so the cloud gaps are
// discovered). The behavioral proof: after the flip the cloud backend serves a
// scan with LastSeenGen==0.
func TestFlipResetsGenCache(t *testing.T) {
	cfg := Config{
		SummaryChannelSize: 4, SummaryBatchSize: 1, SummaryWorkers: 1,
		EmbedChannelSize: 4, EmbedBatchSize: 1, EmbedWorkers: 1,
		Tick: 5 * time.Millisecond,
	}
	fb := newFlippableBackend()
	fb.local.seedGraphNames(kgtypes.GraphCode, "survivor")
	fb.cloud.seedGraphNames(kgtypes.GraphCode, "survivor")
	// Logged-out scans return a NON-ZERO dirty gen with zero items, so the
	// pre-flip collector caches lastSummaryGen/lastEmbedGen = 7. If the flip did
	// NOT reset the cache, the post-flip scan against cloud would carry
	// LastSeenGen=7 (a potential short-circuit), not 0.
	fb.local.scanResp = &knowledgev1.PipelineScanResponse{DirtyGen: 7}
	// Cloud scans also return zero items at a different gen — we only assert the
	// LastSeenGen the collector SENDS (0 after reset), not the response.
	fb.cloud.scanResp = &knowledgev1.PipelineScanResponse{DirtyGen: 9}

	// A non-nil summarizer enables the summary axis so the collector's summary
	// loop actually scans (the gen-cache-reset behavior under test is axis-
	// agnostic; both axes share the same dirty-gen reset path). With BOTH LLM
	// functions nil the per-axis gates would disable every collector loop and no
	// scan would fire.
	noopSum := func(_ context.Context, _ []llmproviders.BatchChunk) (map[string]llmproviders.SummarizeResult, error) {
		return nil, nil
	}
	p := New(cfg, fb, noopSum, nil)
	ctx := context.Background()
	require.NoError(t, p.Start(ctx))
	survivor := graphKey{GraphType: kgtypes.GraphCode, GraphName: "survivor"}

	// Logged out → survivor registered; capture its collector cancel identity.
	p.refreshOnce(ctx)
	before := cancelFor(p, survivor)
	require.NotZero(t, before, "survivor must be registered logged-out")
	// Let the logged-out collector tick a few times so it caches gen=7.
	time.Sleep(40 * time.Millisecond)

	// Flip → CheckLoginFlip tears down every collector (the client activity hook
	// drives it in production) and the following refreshOnce re-registers the
	// survivor fresh, gen cache reset to 0. The recreated collector then scans
	// cloud at gen 0.
	// The hook runs on EVERY tool call, so in production the pre-flip state is
	// already seeded by the time a flip happens; model that first observation
	// here (it only seeds, and reports no transition).
	require.False(t, p.CheckLoginFlip(ctx), "the first observation seeds the login state, it is not a transition")
	fb.loggedIn.Store(true)
	require.True(t, p.CheckLoginFlip(ctx), "the login transition must be detected")
	p.refreshOnce(ctx)
	after := cancelFor(p, survivor)
	require.NotZero(t, after, "survivor must remain registered after the flip")

	// Give the fresh cloud-bound collector a few ticks to scan.
	time.Sleep(40 * time.Millisecond)
	assert.True(t, fb.cloudSawGenZeroScan(),
		"the flipped survivor's fresh collector must scan the cloud backend with LastSeenGen=0 (gen cache reset)")

	require.NoError(t, p.Stop(ctx))
}

// TestPipelineWritebackRoutesByLogin is the in-process cloud-vs-local routing
// integration assertion (NOT the Phase-4 live-verify): a worker batch whose items
// are stamped with the login-resolved backend writes back to THAT backend — cloud
// when logged in, local when logged out. Drives the worker directly with the
// resolver-picked backend so both the scan-time binding and the writeback target
// are exercised end to end.
func TestPipelineWritebackRoutesByLogin(t *testing.T) {
	cases := []struct {
		name     string
		loggedIn bool
	}{
		{"logged_out_routes_local", false},
		{"logged_in_routes_cloud", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fb := newFlippableBackend()
			fb.loggedIn.Store(tc.loggedIn)

			fs := &fakeSummarizer{results: map[string]llmproviders.SummarizeResult{
				"n1": {Summary: "s1"},
			}}
			p := New(Config{}, fb, fs.call, nil)

			// The collector would resolve this backend at scan time; model that by
			// resolving via the same seam the pipeline uses and stamping it.
			be, err := p.resolver.Backend(context.Background())
			require.NoError(t, err)

			batch := []SummaryWork{{
				GraphType: kgtypes.GraphCode, GraphName: "myrepo", NodeID: "n1",
				SummarizeText: `{"name":"n1"}`, Backend: be,
			}}
			runSummaryWorkerBatch(context.Background(), p, batch)

			if tc.loggedIn {
				assert.Equal(t, 1, fb.cloud.mutateCallCount(), "logged-in writeback must land on cloud")
				assert.Equal(t, 0, fb.local.mutateCallCount(), "local must receive nothing when logged in")
			} else {
				assert.Equal(t, 1, fb.local.mutateCallCount(), "logged-out writeback must land on local")
				assert.Equal(t, 0, fb.cloud.mutateCallCount(), "cloud must receive nothing when logged out")
			}
		})
	}
}
