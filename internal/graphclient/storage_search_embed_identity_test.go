// SPDX-License-Identifier: Apache-2.0

package graphclient

import (
	"strings"
	"testing"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// storage_search_embed_identity_test.go is the row that only the real MCP
// endpoint can answer: with the local store OFF THE AIR, a signed-in client's
// knowledge catalog read is served by the cloud leg AND carries that graph's
// embed identity — which is the whole of requirement 1's headline, "a
// cloud-backed client resolves its query embedder without a local server".
//
// WHAT THIS HARNESS OBSERVES, and what it deliberately does not. Its Dispatch is
// a stub, so this row is about ROUTING and about what the catalog read RETURNS.
// The retrieval claim — that the resolved embedder actually drives the vector
// arm and the footer says so — lives on the in-process tools harness
// (tools.TestVectorRefusal_RecordedIdentityWithNoCredentialIsLoud and the mode
// suite), which can see the segment engine. Neither harness can make the other's
// claim, which is why both rows exist.

// catalogEmbedIdentity is the identity the cloud leg records for
// knowledge/default — voyage-code-3 at 256 ubinary, the shape a real cloud graph
// carries in its per-graph __meta.
func catalogEmbedIdentity() *knowledgev1.EmbedIdentity {
	return &knowledgev1.EmbedIdentity{
		Provider: "voyage", Model: "voyage-code-3", Dimension: 256, Dtype: "ubinary",
	}
}

// TestStorageSearchOverMCP_CloudCatalogCarriesTheEmbedIdentityWithLocalDown is
// the served arm.
//
// THE LOCAL LEG RECORDS NOTHING, so an identity that reaches the caller can only
// have come from the cloud leg — and the local leg is stopped anyway, which the
// zero local.execute count confirms rather than assumes.
func TestStorageSearchOverMCP_CloudCatalogCarriesTheEmbedIdentityWithLocalDown(t *testing.T) {
	s := newSignedInMCPSearchStack(t)
	s.cloud.serveGraphNames([]*knowledgev1.GraphInfo{{
		Name: "default", Loaded: true, EmbedIdentity: catalogEmbedIdentity(),
	}})
	s.local.serveGraphNames([]*knowledgev1.GraphInfo{{Name: "default", Loaded: true}})
	s.stopLocal()

	text, isError := s.callText(t, "search", map[string]any{
		"query": "anything", "graph": "knowledge", "mode": "vector",
	})
	if isError {
		t.Fatalf("mode:vector with the local store down: isError = true, want false; text = %q", text)
	}
	const wantIdentity = "embed_identities=[default=voyage/voyage-code-3/256/ubinary]"
	if !strings.Contains(text, wantIdentity) {
		t.Fatalf("text = %q, want it to carry %q — the identity a query embedder is resolved from must "+
			"reach the client with no local server running", text, wantIdentity)
	}
	if !strings.Contains(text, "search_destinations=[{Storage:cloud AccountID:probe-account}]") {
		t.Fatalf("text = %q, want the cloud destination alone", text)
	}
	if s.local.execute.Load() != 0 {
		t.Fatalf("local.execute = %d, want 0: the local store is down and must not have been consulted",
			s.local.execute.Load())
	}
	if s.cloud.execute.Load() == 0 {
		t.Fatalf("cloud.execute = 0, want the cloud store read")
	}
}

// TestStorageSearchOverMCP_NoCloudIdentityRendersAnExplicitAbsence is the
// FALSIFYING CONTROL for the row above. Without it, the assertion there could
// not be told from one that passes whatever the catalog says: the renderer emits
// an explicit "[]" for an empty list, so absence is a rendered fact rather than
// a blank the request itself contributed.
func TestStorageSearchOverMCP_NoCloudIdentityRendersAnExplicitAbsence(t *testing.T) {
	s := newSignedInMCPSearchStack(t)
	s.cloud.serveGraphNames([]*knowledgev1.GraphInfo{{Name: "default", Loaded: true}})
	s.stopLocal()

	text, isError := s.callText(t, "search", map[string]any{
		"query": "anything", "graph": "knowledge", "mode": "vector",
	})
	if isError {
		t.Fatalf("isError = true, want false; text = %q", text)
	}
	if !strings.Contains(text, "embed_identities=[]") {
		t.Fatalf("text = %q, want an explicit empty identity list — a catalog carrying no identity must "+
			"be distinguishable from one that was never read", text)
	}
}
