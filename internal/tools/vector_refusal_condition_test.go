// SPDX-License-Identifier: Apache-2.0

package tools

// vector_refusal_condition_test.go pins what a mode:vector refusal SAYS, arm by
// arm. There are three arms and they refuse for two different reasons, so the
// shape is shared and the sentence is not: the knowledge arm resolves its query
// embedder from the TARGET GRAPH'S recorded identity, while the registered-graph
// and raw-graph arms resolve theirs from this client's own configuration. One
// message across all three was true for at most one of them, and the wrong one
// sends an operator to fix a thing that is not broken.

import (
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// requireClientEmbedderWording asserts one refusal names THIS CLIENT'S missing
// embedder and its remedies, and does NOT claim anything about the graph's
// record.
//
// THE NEGATIVE LEG IS THE MUTATION GUARD: writing the knowledge arm's sentence
// at either of these sites — the easy edit, since the three were a deliberate
// copy of one string — reds this.
func requireClientEmbedderWording(t *testing.T, msg, graph string) {
	t.Helper()
	assert.Contains(t, msg, graph, "the refusal names the graph")
	assert.Contains(t, msg, "mode:vector", "the refusal names the requested mode")
	assert.Contains(t, msg, "this client has no embedder configured",
		"this arm's vector comes from the client's configured embedder, so that is the condition it names")
	assert.Contains(t, msg, "mode:text", "the remedy that works right now")
	assert.Contains(t, msg, "~/.knowledge/config", "and the remedy that fixes the cause")
	assert.NotContains(t, msg, "records no embed identity",
		"this arm never consulted the graph's record, so it may not report one as the cause")
	assert.NotContains(t, strings.ToLower(msg), "local server",
		"a local server is not a remedy for anything on this path")
}

// TestVectorRefusal_RegisteredGraphArmNamesTheClientsCondition drives the
// registered custom-graph arm (intercept_search_registered_graph.go) with no
// embedder on deps, which is exactly what its guard tests.
func TestVectorRefusal_RegisteredGraphArmNamesTheClientsCondition(t *testing.T) {
	t.Setenv("VOYAGE_API_KEY", "")
	var execHits atomic.Int64
	gc, handler := newInterceptHarnessWithHandler(t, &execHits, cannedNodesResp())
	handler.graphNames = []string{"demo"}
	mgr := &fakeSegmentSearcher{}
	// NO emb on deps: this arm reads deps.Embedder(), so a nil one is the
	// condition under test.
	deps := &interceptDeps{gc: gc, segMgr: mgr, gtCRUD: registeredGraphTypes("hellograph")}

	handled, out := InterceptSearch(opCtx(), deps, searchParams(t, map[string]any{
		"graph": "hellograph", "name": "demo", "mode": "vector", "query": "x",
	}))
	require.True(t, handled, "mode:vector on a registered graph is claimed, then refused")
	require.True(t, out.IsError, "it must be refused, not served empty")
	requireClientEmbedderWording(t, engine.FirstTextContent(out), "hellograph")
	assert.Equal(t, int64(0), mgr.calls.Load(),
		"the refusal fires BEFORE the engine — asking it and rendering zero rows is the shape this rejects")
}

// TestVectorRefusal_KnowledgeArmNamesTheGraphsMissingRecord is the knowledge
// arm's own condition, and it is a different sentence because it is a different
// fact: this arm's embedder comes from the graph's recorded identity, so a
// missing one is a property of the GRAPH, not of the client's config.
//
// THE MUTATION THAT MUST RED THIS: write either sibling arm's sentence here.
func TestVectorRefusal_KnowledgeArmNamesTheGraphsMissingRecord(t *testing.T) {
	t.Setenv("VOYAGE_API_KEY", "")
	deps, mgr, _ := newModeHonorDeps(t, modeHonorNodes(), modeHonorHits(), false)

	handled, out := InterceptSearch(opCtx(), deps, searchParams(t, map[string]any{
		"graph": "knowledge", "mode": "vector", "query": "x",
	}))
	require.True(t, handled)
	require.True(t, out.IsError, "mode:vector against a graph with no identity must be refused")

	msg := engine.FirstTextContent(out)
	assert.Contains(t, msg, "mode:vector", "the refusal names the requested mode")
	assert.Contains(t, msg, "knowledge/"+knowledgeDefaultName,
		"the refusal names the graph whose record is missing, so the operator knows which one to fix")
	assert.Contains(t, msg, "records no embed identity",
		"the condition is the graph's record, never a guess about this machine's config")
	assert.Contains(t, msg, "mode:text", "the remedy that works right now")
	assert.Contains(t, msg, "collect or push",
		"and the remedy that fixes the cause — recording an identity on the graph")
	assert.NotContains(t, msg, "no embedder is configured",
		"the retired sentence states a condition this arm never checked")
	assert.NotContains(t, strings.ToLower(msg), "local server",
		"a cloud-backed client needs no local server for this, and offering one is the wrong fix")
	assert.Equal(t, int64(0), mgr.calls.Load(), "the refusal fires before the engine")
}

// TestVectorRefusal_HybridOnAnUnrecordedGraphDegradesOutLoud is the second half
// of requirement 3: mode:hybrid on a graph with no recorded identity is SERVED,
// and its footer states that only the BM25 arm ran. The degrade is disclosed,
// never silent.
func TestVectorRefusal_HybridOnAnUnrecordedGraphDegradesOutLoud(t *testing.T) {
	t.Setenv("VOYAGE_API_KEY", "")
	deps, mgr, _ := newModeHonorDeps(t, modeHonorNodes(), modeHonorHits(), false)

	handled, out := InterceptSearch(opCtx(), deps, searchParams(t, map[string]any{
		"graph": "knowledge", "mode": "hybrid", "query": "x",
	}))
	require.True(t, handled)
	require.False(t, out.IsError, "hybrid is served: %s", engine.FirstTextContent(out))
	assert.Empty(t, mgr.lastVec, "no identity means no vector arm to run")
	assert.Contains(t, engine.FirstTextContent(out), "_search mode: BM25-only_",
		"the footer must say which arms actually ran — a hybrid label over a BM25-only run is the "+
			"silent degrade this asserts against")
}

// TestVectorRefusal_RecordedIdentityWithNoCredentialIsLoud is the
// CREDENTIAL-ABSENT CELL, and it is the behavior change this ticket carries onto
// the cloud path rather than a new rule.
//
// Once a cloud graph's catalog entry carries an identity, a client that cannot
// CONSTRUCT that identity moves from a silent BM25 degrade to a loud error —
// because the graph HAS vectors under that identity, and answering a semantic
// search with keyword results while reporting success is a worse answer the
// caller cannot detect. The write-side "no credential at all → BM25-only"
// degrade is a different thing and is untouched.
//
// NO CREDENTIAL REACHES A STORED NODE ON THIS PATH: the identity carries
// provider, model, dimension and dtype only, and llmproviders resolves the
// secret separately — so the fixture below carries no key, and a fixture that
// put one in a catalog entry would be asserting a shape the product does not
// have.
func TestVectorRefusal_RecordedIdentityWithNoCredentialIsLoud(t *testing.T) {
	t.Setenv("VOYAGE_API_KEY", "")
	var execHits atomic.Int64
	// A catalog entry recording an identity this client cannot build: the voyage
	// provider with no credential anywhere.
	resp := &knowledgev1.ExecuteResponse{
		Nodes: modeHonorNodes(),
		GraphNames: []*knowledgev1.GraphInfo{{
			Name: knowledgeDefaultName, Loaded: true,
			EmbedIdentity: &knowledgev1.EmbedIdentity{
				Provider: "voyage", Model: "voyage-code-3", Dimension: 256, Dtype: "ubinary",
			},
		}},
	}
	gc := newInterceptHarness(t, &execHits, resp)
	mgr := &fakeSegmentSearcher{hits: modeHonorHits()}
	deps := &interceptDeps{gc: gc, segMgr: mgr}

	for _, mode := range []string{"hybrid", "vector"} {
		t.Run(mode, func(t *testing.T) {
			handled, out := InterceptSearch(opCtx(), deps, searchParams(t, map[string]any{
				"graph": "knowledge", "mode": mode, "query": "x",
			}))
			require.True(t, handled)
			require.True(t, out.IsError,
				"an identity this client cannot construct must be reported, never answered with keyword "+
					"results the caller reads as the semantic answer")
			msg := engine.FirstTextContent(out)
			assert.Contains(t, msg, "voyage", "the error names the provider it could not construct")
			assert.Contains(t, strings.ToLower(msg), "credential",
				"and says what is missing, so an operator can supply it")
		})
	}

	// KNOWN-POSITIVE, SAME PACKAGE AND PATH: an identity that CAN be constructed
	// still serves, so the errors above are about that identity rather than about
	// every catalog-carried identity failing.
	okDeps, okMgr, _ := newModeHonorDeps(t, modeHonorNodes(), modeHonorHits(), true)
	handled, out := InterceptSearch(opCtx(), okDeps, searchParams(t, map[string]any{
		"graph": "knowledge", "mode": "vector", "query": "x",
	}))
	require.True(t, handled)
	require.False(t, out.IsError, "control: a constructible identity still serves: %s",
		engine.FirstTextContent(out))
	assert.Len(t, okMgr.lastVec, cannedCatalogVecBytes,
		"control: the vector came from the GRAPH's identity, which its width is what proves")
	assert.Contains(t, engine.FirstTextContent(out), "_search mode: vector_")
}

// rawRefusalHits keeps the raw-graph fixture's ranked set beside the arms above
// so the raw leg reads in one place with them.
func rawRefusalHits() []searchengine.Hit {
	return []searchengine.Hit{{ID: "para1", Score: 0.9}}
}

// TestVectorRefusal_RawGraphArmNamesTheClientsCondition is the third arm. Its
// vector comes from embedQueryForArm — this client's embedder — so it names the
// same condition the registered-graph arm does.
func TestVectorRefusal_RawGraphArmNamesTheClientsCondition(t *testing.T) {
	t.Setenv("VOYAGE_API_KEY", "")
	h := rawSegmentFixture()
	mgr := &fakeSegmentSearcher{hits: rawRefusalHits()}
	deps := &interceptDeps{gc: rawSegmentHarness(t, h), segMgr: mgr} // emb nil: no embedder configured

	handled, out := InterceptSearch(opCtx(), deps, searchParams(t, map[string]any{
		"graph": "web", "name": "doc-slug", "query": "idempotent retries", "mode": "vector",
	}))
	require.True(t, handled, "mode:vector is claimed, then refused")
	requireClientEmbedderWording(t, engine.FirstTextContent(out), "web")
	assert.Equal(t, int64(0), mgr.calls.Load(), "the refusal fires before the engine")
}
