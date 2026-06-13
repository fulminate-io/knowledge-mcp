// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// gateFake implements thought.Caller (Execute) AND reflectProbe (PipelineScan)
// for the quiet-tick gate test. It serves the reflect probe a configurable gen
// (or an error), acts as a tiny by-id metadata store for the watermark
// read/upsert, and flags whether the pass BODY ran — detected by any Execute that
// is NOT the watermark by-id read (the pass body issues a type=thought browse and
// traverses via fetchAdjacency, all of which carry a Type or a traversal anchor,
// never the watermark ById).
type gateFake struct {
	mu          sync.Mutex
	probeGen    uint64
	probeErr    bool
	nodes       map[string]*knowledgev1.Node
	passBodyRan bool
}

func (f *gateFake) PipelineScan(_ context.Context, _ *knowledgev1.PipelineScanRequest) (*knowledgev1.PipelineScanResponse, error) {
	if f.probeErr {
		return nil, errors.New("probe unavailable")
	}
	return &knowledgev1.PipelineScanResponse{DirtyGen: f.probeGen}, nil
}

func (f *gateFake) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.nodes == nil {
		f.nodes = map[string]*knowledgev1.Node{}
	}

	// Upsert mutation (the watermark write): capture the node body.
	if m := req.GetMutation(); m != nil {
		for _, b := range m.GetNodeBodies() {
			n := &knowledgev1.Node{Id: b.GetId(), Type: b.GetType(), SymbolName: b.GetName()}
			for k, v := range b.GetMetadata() {
				kgtypes.SetValue(n, k, v)
			}
			f.nodes[b.GetId()] = n
		}
		return &knowledgev1.ExecuteResponse{}, nil
	}

	q := req.GetQuery()
	if q == nil {
		return &knowledgev1.ExecuteResponse{}, nil
	}
	// Watermark by-id read: NOT a pass-body call. Serve the stored node.
	if q.GetById() == watermarkNodeID {
		if n, ok := f.nodes[watermarkNodeID]; ok {
			return &knowledgev1.ExecuteResponse{Nodes: []*knowledgev1.Node{n}}, nil
		}
		return &knowledgev1.ExecuteResponse{}, nil
	}
	// Any other query/traverse is a pass-body read (fetchAdjacency, the
	// type=thought browse, etc.) → the pass DID run. Return empty so the body
	// quiesces quickly (an empty corpus finishes the drain in one short page).
	f.passBodyRan = true
	return &knowledgev1.ExecuteResponse{}, nil
}

func (f *gateFake) didPassRun() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.passBodyRan
}

func (f *gateFake) persistedGen() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if n, ok := f.nodes[watermarkNodeID]; ok {
		return kgtypes.Value(n, watermarkGenKey)
	}
	return ""
}

func newGateLoop(fake *gateFake) *PropagationLoop {
	return &PropagationLoop{gc: fake, stopCh: make(chan struct{})}
}

// TestRunBackgroundPropagation_QuietTickSkips drives a tick
// where the probe gen equals the persisted watermark: the pass body must NOT run
// and the SKIPPED path is taken.
func TestRunBackgroundPropagation_QuietTickSkips(t *testing.T) {
	fake := &gateFake{probeGen: 5}
	// Persist a matching watermark (gen 5) up front.
	require.NoError(t, writeLastReflectedGen(context.Background(), fake, 5))

	newGateLoop(fake).runBackgroundPropagation()

	assert.False(t, fake.didPassRun(),
		"quiet tick (probe gen == persisted gen) must NOT run the pass body (no fetchAdjacency)")
}

// TestRunBackgroundPropagation_RunsOnBump drives a tick where
// the probe gen differs from the persisted watermark: the pass body MUST run and
// the new (start-of-pass) gen is persisted.
func TestRunBackgroundPropagation_RunsOnBump(t *testing.T) {
	fake := &gateFake{probeGen: 9}
	require.NoError(t, writeLastReflectedGen(context.Background(), fake, 5)) // stale watermark

	newGateLoop(fake).runBackgroundPropagation()

	assert.True(t, fake.didPassRun(), "a bumped reflect gen must run the pass body")
	assert.Equal(t, "9", fake.persistedGen(),
		"a completed pass must persist the start-of-pass reflect gen as the new watermark")
}

// TestRunBackgroundPropagation_ProbeFailureRuns asserts that a
// FAILING probe never skips — it degrades to running the pass.
func TestRunBackgroundPropagation_ProbeFailureRuns(t *testing.T) {
	fake := &gateFake{probeErr: true}
	require.NoError(t, writeLastReflectedGen(context.Background(), fake, 5))

	newGateLoop(fake).runBackgroundPropagation()

	assert.True(t, fake.didPassRun(), "a failing probe must NOT skip — it degrades to running the pass")
}

// TestRunBackgroundPropagation_ColdStartRuns asserts that with no persisted
// watermark (lastReflectedGen==0) the tick always runs, even when the probe
// reports a gen.
func TestRunBackgroundPropagation_ColdStartRuns(t *testing.T) {
	fake := &gateFake{probeGen: 3} // no watermark persisted

	newGateLoop(fake).runBackgroundPropagation()

	assert.True(t, fake.didPassRun(), "cold start (no persisted watermark) must run the pass")
}
