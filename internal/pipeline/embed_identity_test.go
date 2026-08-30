// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/config"
	"github.com/fulminate-io/knowledge-mcp/internal/embed"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/llmproviders"
)

// embed_identity_test.go covers the client half of the embed-identity seam: what
// the pipeline STATES on the wire about the vectors it produces.
//
// THE SERVER HALF IS NOT REACHABLE FROM HERE and is not simulated. cmd/knowledge
// and cmd/knowledge-server are separate Go modules with no dependency in either
// direction, so these tests assert on the OUTGOING request — the last thing this
// module owns — and the engine's record-or-refuse behaviour for that request is
// pinned on the server side (TestExecuteMutation_UpdateItemsEmbedIdentity,
// TestEngine_PipelineScan_RefusesMismatchedEmbedIdentity). A local re-enactment
// of the server's gate would agree with this client by construction and prove
// nothing about the real one.

// wireIdentity is a stated identity distinct from anything a default config
// resolves to, so an assertion that finds it on the wire found THIS value rather
// than a coincidence.
func wireIdentity() *knowledgev1.EmbedIdentity {
	return &knowledgev1.EmbedIdentity{
		Provider: "voyage", Model: "voyage-code-3", Dimension: 256, Dtype: "ubinary",
	}
}

// updateItemsFor flattens every UPDATE_ITEMS plan the fake observed into one
// slice, in call order. Selection is by PLAN KIND rather than by call position:
// one embed batch produces several writebacks (the vector write, and a terminal
// marker for any empty-text item), and a positional read would silently follow
// any reordering of them.
func updateItemsFor(f *fakeWireClient) []*knowledgev1.UpdateItem {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*knowledgev1.UpdateItem
	for _, req := range f.execRequests {
		m := req.GetMutation()
		if m == nil || m.GetKind() != knowledgev1.MutationPlan_MUTATION_KIND_UPDATE_ITEMS {
			continue
		}
		out = append(out, m.GetUpdateItems()...)
	}
	return out
}

// assertIdentity fails unless id states exactly the four fields given. Compared
// field-by-field so a failure names WHICH part of the tuple drifted; a whole-
// message compare reports only that two blobs differ.
func assertIdentity(t *testing.T, id *knowledgev1.EmbedIdentity, provider, model string, dim int32, dtype string) {
	t.Helper()
	require.NotNil(t, id, "the item states no embed identity at all")
	assert.Equal(t, provider, id.GetProvider(), "provider")
	assert.Equal(t, model, id.GetModel(), "model")
	assert.Equal(t, dim, id.GetDimension(), "dimension")
	assert.Equal(t, dtype, id.GetDtype(), "dtype")
}

// TestEmbedWriteback_StatesTheIdentityOnVectorBearingItemsOnly is the writeback
// half of the seam.
//
// BOTH HALVES ARE MEASURED IN ONE RUN, and that is what stops the second one
// being vacuous. "No identity on a vector-less item" is also what a completely
// unwired field looks like, so the empty-text item below rides the SAME batch as
// two real ones: the vector-bearing items are the known-positive proving the
// field can be populated at all, and the marker item is then a real absence
// rather than an unwired one.
//
// THE VECTOR-LESS CONTRACT IS THE SERVER'S, NOT AN AESTHETIC CHOICE: an identity
// stated without a vector claims nothing about stored bytes, and the batch gate
// ignores it there. Stating one would make a terminal-marker write look like a
// vector writeback to a gate that is deciding what a graph is embedded under.
func TestEmbedWriteback_StatesTheIdentityOnVectorBearingItemsOnly(t *testing.T) {
	ctx := context.Background()
	be := newFakeWireClient()
	fe := &fakeEmbedder{vectors: map[string][]byte{"n1": vec32(1), "n2": vec32(2)}}
	p := New(Config{EmbedIdentity: wireIdentity()}, be, nil, fe.call)

	runEmbedWorkerBatch(ctx, p, []EmbedWork{
		{GraphType: kgtypes.GraphCode, GraphName: "repo", NodeID: "n1", EmbedText: "a", Backend: be},
		{GraphType: kgtypes.GraphCode, GraphName: "repo", NodeID: "n2", EmbedText: "b", Backend: be},
		// Whitespace-only server-composed text: routed to the terminal-marker
		// write, which carries no vector.
		{GraphType: kgtypes.GraphCode, GraphName: "repo", NodeID: "n3", EmbedText: "   ", Backend: be},
	})

	items := updateItemsFor(be)
	require.NotEmpty(t, items, "the batch must have produced writebacks to assert on")

	withVector, withoutVector := 0, 0
	for _, it := range items {
		if len(it.GetBinaryVector()) > 0 {
			withVector++
			assertIdentity(t, it.GetEmbedIdentity(), "voyage", "voyage-code-3", 256, "ubinary")
			continue
		}
		withoutVector++
		assert.Nil(t, it.GetEmbedIdentity(),
			"item %q carries no vector, so it must claim nothing about what any vector IS", it.GetId())
	}
	assert.Equal(t, 2, withVector, "both embedded nodes are written back with their vectors")
	assert.Equal(t, 1, withoutVector, "the empty-text node is written back as a terminal marker")
}

// TestEmbedWriteback_StatesTheIdentityTheClientItselfResolved drives the value
// across the boundary the two halves of this seam meet at: the identity is
// produced by the PRODUCTION resolver from a real parsed config, threaded through
// the real pipeline Config, and read back off the real outgoing ExecuteRequest.
//
// A TEST THAT BUILT THE IDENTITY BY HAND WOULD PROVE ONLY THAT A STRUCT SURVIVES
// A COPY. What can actually break here is the resolution: config carries no
// model, the arm fills its own default, and an identity assembled from the
// section alone would state an empty model for vectors produced by voyage-code-3.
// A graph records the first identity offered to it and is authoritative
// afterwards, so that would be permanent short of an explicit migration.
func TestEmbedWriteback_StatesTheIdentityTheClientItselfResolved(t *testing.T) {
	// No model named: the ordinary no-config case, where the ARM's default is
	// the only thing that can supply one.
	cfg, err := config.Parse([]byte("[embedder]\nprovider = \"voyage\"\n[credentials]\nvoyage_api_key = \"test-key\"\n"))
	require.NoError(t, err)
	t.Cleanup(config.SetForTest(cfg))

	identity, err := llmproviders.ResolvedEmbedIdentity(embed.InputRoleDocument)
	require.NoError(t, err)
	require.NotNil(t, identity, "a configured embedder must resolve an identity to state")

	ctx := context.Background()
	be := newFakeWireClient()
	fe := &fakeEmbedder{vectors: map[string][]byte{"n1": vec32(7)}}
	p := New(Config{EmbedIdentity: identity}, be, nil, fe.call)

	runEmbedWorkerBatch(ctx, p, []EmbedWork{
		{GraphType: kgtypes.GraphKnowledge, GraphName: "default", NodeID: "n1", EmbedText: "a", Backend: be},
	})

	items := updateItemsFor(be)
	require.Len(t, items, 1)
	require.NotEmpty(t, items[0].GetBinaryVector())
	// The expectation is spec-authored, not read back from the resolver: this is
	// the tuple the deployment expects on knowledge:default and the code graphs.
	assertIdentity(t, items[0].GetEmbedIdentity(), "voyage", "voyage-code-3", 256, "ubinary")
}

// scanRecorder captures every PipelineScanRequest the pipeline issues. The
// identity lives in the REQUEST the client builds, so asserting the request is
// what proves the claim; asserting a response would only describe what a fake
// chose to say.
type scanRecorder struct {
	reqs []*knowledgev1.PipelineScanRequest
}

func (s *scanRecorder) PipelineScan(
	_ context.Context, req *knowledgev1.PipelineScanRequest,
) (*knowledgev1.PipelineScanResponse, error) {
	s.reqs = append(s.reqs, req)
	return &knowledgev1.PipelineScanResponse{}, nil
}

func (s *scanRecorder) PipelineGenPoll(
	_ context.Context, _ *knowledgev1.PipelineGenPollRequest,
) (*knowledgev1.PipelineGenPollResponse, error) {
	return &knowledgev1.PipelineGenPollResponse{}, nil
}

func (s *scanRecorder) Execute(
	_ context.Context, _ *knowledgev1.ExecuteRequest,
) (*knowledgev1.ExecuteResponse, error) {
	return &knowledgev1.ExecuteResponse{}, nil
}

// TestScanGaps_StatesTheEmbedIdentityOnTheEmbedAxisOnly pins the fail-fast half.
//
// WHY THE SCAN STATES IT AT ALL, when the writeback already does: the client pays
// an embedding provider BETWEEN the two. The server refuses an embed scan whose
// stated identity is not the graph's recorded one, so a client that would be
// refused at writeback learns before it spends. A scan that states nothing
// bypasses that check entirely.
//
// THE NON-EMBED AXES ARE THE CONTROL, and they are not decoration: the summary
// axis produces no vectors and segment_rebuild reads vectors a graph already
// holds, so an identity on either claims something about bytes neither one
// produces — and a client that stated it uniformly would be refused for scans
// that embed nothing.
func TestScanGaps_StatesTheEmbedIdentityOnTheEmbedAxisOnly(t *testing.T) {
	rec := &scanRecorder{}
	id := wireIdentity()

	for _, axis := range []string{"embed", "summary", "segment_rebuild"} {
		_, _, _, err := scanGaps(context.Background(), rec, kgtypes.GraphCode, "repo", axis, 10, 0, id)
		require.NoError(t, err)
	}
	require.Len(t, rec.reqs, 3)

	assertIdentity(t, rec.reqs[0].GetEmbedIdentity(), "voyage", "voyage-code-3", 256, "ubinary")
	assert.Nil(t, rec.reqs[1].GetEmbedIdentity(), "the summary axis produces no vectors")
	assert.Nil(t, rec.reqs[2].GetEmbedIdentity(), "the segment_rebuild axis produces no vectors")
}

// TestScanGaps_StatesNothingWhenNoEmbedderIsWired is the other half of the embed
// axis's rule: nil in, nil on the wire.
//
// IT IS NOT A REDUNDANT NIL CHECK. A client with no embedder produces no
// vectors, so it makes no claim — and the server serves an unstated identity
// deliberately, because refusing it would break every client that embeds
// nothing. A scan that synthesized an identity from something other than a wired
// embedder would be refused on any graph recorded differently, for work it was
// never going to do.
func TestScanGaps_StatesNothingWhenNoEmbedderIsWired(t *testing.T) {
	rec := &scanRecorder{}
	_, _, _, err := scanGaps(context.Background(), rec, kgtypes.GraphCode, "repo", "embed", 10, 0, nil)
	require.NoError(t, err)
	require.Len(t, rec.reqs, 1)
	assert.Nil(t, rec.reqs[0].GetEmbedIdentity())
}
