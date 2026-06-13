// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// backstopFake is a tiny Caller that acts as a by-id metadata store for the
// last_full_pass watermark round-trip AND captures every mutation request so the
// test can assert the upsert shape (resource type, reflect-inert flag unset).
type backstopFake struct {
	mu       sync.Mutex
	nodes    map[string]*knowledgev1.Node
	lastMut  *knowledgev1.MutationPlan
	lastBody *knowledgev1.NodeBody
}

func (f *backstopFake) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.nodes == nil {
		f.nodes = map[string]*knowledgev1.Node{}
	}
	if m := req.GetMutation(); m != nil {
		f.lastMut = m
		for _, b := range m.GetNodeBodies() {
			f.lastBody = b
			n := &knowledgev1.Node{Id: b.GetId(), Type: b.GetType(), SymbolName: b.GetName()}
			for k, v := range b.GetMetadata() {
				kgtypes.SetValue(n, k, v)
			}
			f.nodes[b.GetId()] = n
		}
		return &knowledgev1.ExecuteResponse{}, nil
	}
	if q := req.GetQuery(); q != nil && q.GetById() != "" {
		if n, ok := f.nodes[q.GetById()]; ok {
			return &knowledgev1.ExecuteResponse{Nodes: []*knowledgev1.Node{n}}, nil
		}
	}
	return &knowledgev1.ExecuteResponse{}, nil
}

// TestLastFullPassRoundTrip writes a lastFullPass and reads it back, asserting
// RFC3339-precision round-trip AND that the write is a PLAIN upsert of the
// reflection-watermark resource singleton with reflect_inert_writeback UNSET (the
// write is reflect-inert by resource TYPE, not via executeReflectInertMutate).
func TestLastFullPassRoundTrip(t *testing.T) {
	fake := &backstopFake{}
	// RFC3339 has second precision — truncate so the round-trip is exact.
	want := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, writeLastFullPass(context.Background(), fake, want))

	// The captured mutation is an upsert of the resource singleton, NOT reflect-inert.
	require.NotNil(t, fake.lastMut, "writeLastFullPass must issue a mutation")
	assert.False(t, fake.lastMut.GetReflectInertWriteback(),
		"writeLastFullPass must be a PLAIN upsert (reflect-inert by resource type, NOT the writeback flag)")
	require.NotNil(t, fake.lastBody, "the upsert carries a node body")
	assert.Equal(t, watermarkNodeID, fake.lastBody.GetId(), "targets the shared watermark singleton")
	assert.Equal(t, "resource", fake.lastBody.GetType(), "the singleton is a resource node")
	assert.Equal(t, want.Format(time.RFC3339), fake.lastBody.GetMetadata()[lastFullPassKey],
		"persists last_full_pass as an RFC3339 timestamp")

	got, ok := readLastFullPass(context.Background(), fake)
	require.True(t, ok, "readLastFullPass finds the persisted watermark")
	assert.True(t, want.Equal(got), "round-trip is exact at RFC3339 precision: want %v got %v", want, got)
}

// TestLastFullPassAbsent asserts the cold case: no persisted watermark → (zero, false).
func TestLastFullPassAbsent(t *testing.T) {
	fake := &backstopFake{}
	got, ok := readLastFullPass(context.Background(), fake)
	assert.False(t, ok, "absent watermark returns ok=false")
	assert.True(t, got.IsZero(), "absent watermark returns the zero time")
}
