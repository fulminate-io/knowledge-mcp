// SPDX-License-Identifier: Apache-2.0

package remote

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/collectorwire"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// collect_chunk_hash_fields_test.go asserts on the BUILDER'S OUTPUT rather than
// through a server round trip, and that is the point of it.
//
// WHY A CLIENT-SIDE ASSERTION. Every server-side decline test constructs its own
// CollectChunkRequest in-test, so all of them stay green against a client that
// populates none of these fields — the feature would be inert in production
// behind a fully green plan. The only thing that catches that is an assertion on
// what the sole builder actually emits.

// TestCollectChunkRequests_PopulateHashFields drives two node chunks and one
// edge chunk and requires the manifest identity, the per-chunk-aligned node
// digests, and the per-file hashes to reach every request.
func TestCollectChunkRequests_PopulateHashFields(t *testing.T) {
	result := &collectorwire.CollectResult{
		GraphType:     kgtypes.GraphCode,
		GraphName:     "repo",
		CurrentBranch: "main",
	}
	nodes := []*knowledgev1.Node{
		{Id: "a.go:Alpha", FilePath: "a.go"},
		{Id: "a.go:Beta", FilePath: "a.go"},
		{Id: "b.go:Gamma", FilePath: "b.go"},
		{Id: "fileless-note"},
	}
	// Distinct digests per node: an off-by-one in the offset walk is only visible
	// when the values differ, and a chunk carrying its NEIGHBOUR's digests would
	// otherwise be indistinguishable from a correct one.
	nodeHashes := [][32]byte{{0xA1}, {0xA2}, {0xB3}, {0xF4}}
	perFile := map[string][32]byte{"a.go": {0x0A}, "b.go": {0x0B}}

	// The chunker splits by BYTES, so an uneven split is the realistic shape: 3
	// nodes then 1, never a fixed stride.
	nodeChunks := [][]*knowledgev1.Node{nodes[:3], nodes[3:]}
	edgeChunks := [][]*knowledgev1.BatchEdge{{
		{FromId: "b.go:Gamma", ToId: "a.go:Alpha", Type: "CALLS"},
	}}

	reqs := collectChunkRequests(999, result, nodeChunks, edgeChunks, diffModeOn, chunkHashFields{
		manifestID:    "mf-live-1",
		nodeHashes:    nodeHashes,
		perFileHashes: perFile,
		fileByNodeID:  map[string]string{"a.go:Alpha": "a.go", "a.go:Beta": "a.go", "b.go:Gamma": "b.go"},
	})
	require.Len(t, reqs, 3, "two node chunks and one edge chunk")

	// THE VALIDITY TOKEN RIDES EVERY CHUNK. A chunk without it is declined
	// against nothing, so a builder that stamped only the first would silently
	// disable the feature for every chunk after it.
	for i, r := range reqs {
		assert.Equal(t, "mf-live-1", r.GetManifestId(), "chunk %d must echo the manifest identity", i)
	}

	// PER-CHUNK ALIGNMENT, asserted against the fixture's own digests rather than
	// by length. Two chunks of equal length would pass a length check while
	// carrying each other's hashes.
	first := reqs[0]
	require.Len(t, first.GetNodeContributionHashes(), 3,
		"the first chunk's hashes must be exactly as many as its nodes")
	assert.Equal(t, byte(0xA1), first.GetNodeContributionHashes()[0][0])
	assert.Equal(t, byte(0xA2), first.GetNodeContributionHashes()[1][0])
	assert.Equal(t, byte(0xB3), first.GetNodeContributionHashes()[2][0])

	second := reqs[1]
	require.Len(t, second.GetNodeContributionHashes(), 1)
	assert.Equal(t, byte(0xF4), second.GetNodeContributionHashes()[0][0],
		"the SECOND chunk must carry the FOURTH digest — the offset is carried across chunks, "+
			"not restarted, and a restart would hand it 0xA1 here")

	// A node chunk names its own files, and only files it actually carries.
	assert.Equal(t, []string{"a.go", "b.go"}, entryPaths(first.GetFileContributions()))
	assert.Empty(t, second.GetFileContributions(),
		"a chunk of only FILELESS nodes names no file: a fileless row is outside the manifest "+
			"and can never be declined")

	// An EDGE chunk names the owning files of its edges, resolved through the FROM
	// node — the same derivation the server's edge-side decline performs.
	edgeReq := reqs[2]
	assert.Empty(t, edgeReq.GetNodeContributionHashes(), "an edge chunk carries no node digests")
	assert.Equal(t, []string{"b.go"}, entryPaths(edgeReq.GetFileContributions()),
		"the edge's owning file is its FROM node's file, not its target's")
	require.NotEmpty(t, edgeReq.GetFileContributions())
	assert.Equal(t, byte(0x0B), edgeReq.GetFileContributions()[0].GetContributionHash()[0])
}

// TestCollectChunkRequests_NoManifestSendsNoHashes pins the fail-closed lane: a
// collect that fetched no manifest — a non-diff-eligible family, or a degraded
// one — sends an empty identity and no per-file hashes, so the server declines
// nothing and every row lands.
func TestCollectChunkRequests_NoManifestSendsNoHashes(t *testing.T) {
	result := &collectorwire.CollectResult{GraphType: kgtypes.GraphWebRaw, GraphName: "site"}
	nodes := []*knowledgev1.Node{{Id: "page-1", FilePath: "index.html"}}

	reqs := collectChunkRequests(7, result, [][]*knowledgev1.Node{nodes}, nil, diffModeOff, chunkHashFields{
		// A non-eligible family computes no per-file map by construction, but the
		// PER-ROW digests are still computed for it — every graph's rows carry a
		// contribution_hash column.
		nodeHashes: [][32]byte{{0xC7}},
	})
	require.Len(t, reqs, 1)
	assert.Empty(t, reqs[0].GetManifestId(), "no manifest was fetched, so nothing can be declined")
	assert.Empty(t, reqs[0].GetFileContributions(), "a non-eligible family has no per-file map")
	require.Len(t, reqs[0].GetNodeContributionHashes(), 1,
		"per-ROW digests still ride: every graph family's rows carry the column")
	assert.Equal(t, byte(0xC7), reqs[0].GetNodeContributionHashes()[0][0])
}

// entryPaths lists a chunk's manifest entry paths in the order sent.
func entryPaths(entries []*knowledgev1.ManifestEntry) []string {
	if len(entries) == 0 {
		return nil
	}
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.GetFilePath()
	}
	return out
}
