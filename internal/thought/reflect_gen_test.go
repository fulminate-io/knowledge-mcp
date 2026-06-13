// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// reflectGenFake implements BOTH thought.Caller (Execute) and reflectProbe
// (PipelineScan). It serves the probe a fixed dirty_gen (counting calls) and acts
// as a tiny by-id metadata store so write/readLastReflectedGen can round-trip: an
// upsert mutation captures the watermark node's metadata, and a by-id query serves
// it back.
type reflectGenFake struct {
	probeGen   uint64
	probeCalls int
	nodes      map[string]*knowledgev1.Node // by id, populated by upsert
}

func (f *reflectGenFake) PipelineScan(_ context.Context, _ *knowledgev1.PipelineScanRequest) (*knowledgev1.PipelineScanResponse, error) {
	f.probeCalls++
	return &knowledgev1.PipelineScanResponse{DirtyGen: f.probeGen}, nil
}

func (f *reflectGenFake) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	if f.nodes == nil {
		f.nodes = map[string]*knowledgev1.Node{}
	}
	// Upsert mutation: capture the watermark node body into the store.
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
	// By-id query: serve the stored node (if present).
	if q := req.GetQuery(); q != nil && q.GetById() != "" {
		if n, ok := f.nodes[q.GetById()]; ok {
			return &knowledgev1.ExecuteResponse{Nodes: []*knowledgev1.Node{n}}, nil
		}
	}
	return &knowledgev1.ExecuteResponse{}, nil
}

// TestProbeReflectGen_OnePipelineScan asserts probeReflectGen
// returns the fake's dirty_gen via EXACTLY one PipelineScan call.
func TestProbeReflectGen_OnePipelineScan(t *testing.T) {
	ctx := context.Background()
	fake := &reflectGenFake{probeGen: 42}

	gen, ok := probeReflectGen(ctx, fake)
	require.True(t, ok, "probe must report available")
	assert.Equal(t, uint64(42), gen, "probe must return the fake's dirty_gen")
	assert.Equal(t, 1, fake.probeCalls, "probeReflectGen must issue exactly one PipelineScan")
}

// TestProbeReflectGen_UnavailableOnNil asserts a nil probe is treated as
// unavailable (so the loop runs the pass rather than skipping on a missing probe).
func TestProbeReflectGen_UnavailableOnNil(t *testing.T) {
	gen, ok := probeReflectGen(context.Background(), nil)
	assert.False(t, ok)
	assert.Zero(t, gen)
}

// TestLastReflectedGen_RoundTrip asserts write/readLastReflectedGen
// round-trips the gen through the fake's metadata store, and that a read before any
// write returns 0 (first-run = dirty).
func TestLastReflectedGen_RoundTrip(t *testing.T) {
	ctx := context.Background()
	fake := &reflectGenFake{}

	// Before any write: absent → 0.
	assert.Zero(t, readLastReflectedGen(ctx, fake), "absent watermark reads as 0 (first run)")

	require.NoError(t, writeLastReflectedGen(ctx, fake, 7))
	assert.Equal(t, uint64(7), readLastReflectedGen(ctx, fake), "watermark round-trips")

	// Overwrite advances the persisted value.
	require.NoError(t, writeLastReflectedGen(ctx, fake, 9))
	assert.Equal(t, uint64(9), readLastReflectedGen(ctx, fake), "watermark overwrite persists the new gen")

	// The watermark node is a NON-reflection `resource` type (so its own write
	// never bumps the reflect gen).
	require.Contains(t, fake.nodes, watermarkNodeID)
	assert.Equal(t, "resource", fake.nodes[watermarkNodeID].GetType(),
		"watermark must be a non-reflection resource node")
}
