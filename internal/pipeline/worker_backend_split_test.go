// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/llmproviders"
)

// TestSurvivorBackendSplit_Summary is the Hazard-A survivor-case regression: a
// SINGLE worker batch, ONE graphKey (code/myrepo), but items stamped with TWO
// DISTINCT backends (the OLD local collector + the NEW cloud collector both
// emitted survivor-graphKey items onto the global channel during a login flip).
// With the composite (graphKey, Backend) grouping the writeback MUST fan out to
// TWO distinct writeBatchUpdates targets — each backend gets exactly its own
// items — and NEITHER lands on the shared p.client.
//
// This test MUST FAIL against graphKey-ONLY grouping (which would collapse both
// items into one group and write the whole mixed batch through one backend).
func TestSurvivorBackendSplit_Summary(t *testing.T) {
	ctx := context.Background()

	pClient := newFakeWireClient() // the shared p.client — must receive NOTHING
	beLocal := newFakeWireClient() // OLD collector's backend
	beCloud := newFakeWireClient() // NEW collector's backend

	fs := &fakeSummarizer{results: map[string]llmproviders.SummarizeResult{
		"n-local": {Summary: "s-local"},
		"n-cloud": {Summary: "s-cloud"},
	}}
	p := New(Config{}, pClient, fs.call, nil)

	// One batch, one graphKey (code/myrepo), two backends.
	batch := []SummaryWork{
		{GraphType: kgtypes.GraphCode, GraphName: "myrepo", NodeID: "n-local", SummarizeText: `{"name":"n-local"}`, Backend: beLocal},
		{GraphType: kgtypes.GraphCode, GraphName: "myrepo", NodeID: "n-cloud", SummarizeText: `{"name":"n-cloud"}`, Backend: beCloud},
	}
	runSummaryWorkerBatch(ctx, p, batch)

	// Each backend gets exactly ONE writeBatchUpdates with ITS OWN item.
	assert.Equal(t, 1, beLocal.mutateCallCount(), "local backend must receive exactly one writeback")
	assert.Equal(t, 1, beCloud.mutateCallCount(), "cloud backend must receive exactly one writeback")
	assert.Equal(t, 1, beLocal.totalWriteItems(), "local backend must receive exactly its one item")
	assert.Equal(t, 1, beCloud.totalWriteItems(), "cloud backend must receive exactly its one item")

	// The shared p.client must NOT be written through — the survivor items route
	// to the backends that scanned them, never collapsed onto p.client.
	assert.Equal(t, 0, pClient.mutateCallCount(), "p.client must NOT receive any survivor writeback")

	// Each backend received the RIGHT item (no cross-routing).
	assert.Equal(t, "n-local", beLocal.recordedWrites[0][0].ID, "local backend must hold the local-scanned item")
	assert.Equal(t, "n-cloud", beCloud.recordedWrites[0][0].ID, "cloud backend must hold the cloud-scanned item")
}

// TestSurvivorBackendSplit_Embed mirrors the summary survivor case on the embed
// axis: one batch, one graphKey, two backends → two distinct writeback targets.
func TestSurvivorBackendSplit_Embed(t *testing.T) {
	ctx := context.Background()

	pClient := newFakeWireClient()
	beLocal := newFakeWireClient()
	beCloud := newFakeWireClient()

	fe := &fakeEmbedder{vectors: map[string][]byte{
		"n-local": make([]byte, 32),
		"n-cloud": make([]byte, 32),
	}}
	p := New(Config{}, pClient, nil, fe.call)

	batch := []EmbedWork{
		{GraphType: kgtypes.GraphCode, GraphName: "myrepo", NodeID: "n-local", EmbedText: "text-local", Backend: beLocal},
		{GraphType: kgtypes.GraphCode, GraphName: "myrepo", NodeID: "n-cloud", EmbedText: "text-cloud", Backend: beCloud},
	}
	runEmbedWorkerBatch(ctx, p, batch)

	assert.Equal(t, 1, beLocal.mutateCallCount(), "local backend must receive exactly one embed writeback")
	assert.Equal(t, 1, beCloud.mutateCallCount(), "cloud backend must receive exactly one embed writeback")
	assert.Equal(t, 0, pClient.mutateCallCount(), "p.client must NOT receive any survivor embed writeback")
	assert.Equal(t, "n-local", beLocal.recordedWrites[0][0].ID)
	assert.Equal(t, "n-cloud", beCloud.recordedWrites[0][0].ID)
}
