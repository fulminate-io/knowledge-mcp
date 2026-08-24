// SPDX-License-Identifier: Apache-2.0

package remote

import (
	"bytes"
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/contribhash"
	"github.com/fulminate-io/knowledge-mcp/internal/collectorwire"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// TestWriteResult_NodeHashesStayWithTheirOwnNodesAfterFileGrouping drives the FULL
// WriteResult path and asserts the property the file grouping put at risk: every
// chunk's node_contribution_hashes[i] is the digest of THAT chunk's nodes[i].
//
// WHY THE FULL PATH RATHER THAN A UNIT TEST OF THE PERMUTATION. The digests are
// computed once over result.Nodes and sliced POSITIONALLY by the chunk builder
// (collectChunkRequests walks `offset += len(nc)`), so the alignment is a property
// of the whole assembly — the permutation, the diff filter, the byte-split and the
// offset walk together. A permutation applied to the nodes alone would hand every
// chunk its predecessors' digests, and the server's only structural guard is a
// LENGTH check, which a permutation passes: every file's aggregate would then
// disagree with the client's, no file would ever decline, and every gate would
// stay green.
//
// THE FIXTURE CONTROL IS LOAD-BEARING. The delivered node order is asserted to be
// file-grouped AND to differ from the input order, so the permutation is proven to
// have actually happened. Without it this test is satisfied by a build that does
// no grouping at all — the identity permutation trivially keeps every hash with
// its node.
func TestWriteResult_NodeHashesStayWithTheirOwnNodesAfterFileGrouping(t *testing.T) {
	client, rec := startRecordingIngest(t)
	sink := NewUploadSink(client)

	// ~1 MiB of Content per node against the 4 MiB DefaultBatchBytes, so the node
	// set spans SEVERAL chunks and the builder's offset walk is exercised rather
	// than assumed.
	body := strings.Repeat("x", 1<<20)
	node := func(id, path string) *knowledgev1.Node {
		return &knowledgev1.Node{
			Id: id, Type: string(kgtypes.NodeFile), FilePath: path,
			SymbolName: id, Content: body + id,
		}
	}
	files := []string{"pkg/a.go", "pkg/b.go", "pkg/c.go"}

	// PRODUCTION-SHAPED INPUT: no file's nodes are contiguous, and file-less nodes
	// sit both mid-slice (as ensureLangNode appends them) and at the tail (as the
	// hierarchy append does).
	var nodes []*knowledgev1.Node
	for round := range 2 {
		for _, f := range files {
			nodes = append(nodes, node(f+":Sym"+strconv.Itoa(round), f))
		}
		if round == 0 {
			nodes = append(nodes, node("lang:go", ""))
		}
	}
	nodes = append(nodes, node("repo-root", ""))
	inputOrder := make([]string, len(nodes))
	for i, n := range nodes {
		inputOrder[i] = n.GetId()
	}
	require.NotEqual(t, nodes[0].GetFilePath(), nodes[1].GetFilePath(),
		"fixture must be production-shaped: a file's nodes must NOT be contiguous in the input")

	result := &collectorwire.CollectResult{
		GraphType:              kgtypes.GraphCode,
		GraphName:              "hash-alignment-repo",
		Nodes:                  nodes,
		DiscoveryFingerprint:   "fingerprint-hash-alignment-fixture",
		CollectorOutputVersion: testCollectorOutputVersion,
	}
	require.NoError(t, sink.WriteResult(context.Background(), "", result))

	rec.mu.Lock()
	captured := rec.chunks
	rec.mu.Unlock()

	var nodeChunks []*knowledgev1.CollectChunkRequest
	for _, req := range captured {
		if len(req.GetNodes()) > 0 {
			nodeChunks = append(nodeChunks, req)
		}
	}
	require.GreaterOrEqual(t, len(nodeChunks), 2,
		"the fixture must span several node chunks, or the offset walk is untested")

	// THE PROPERTY: each chunk's digests belong to that chunk's OWN nodes, in
	// order. Recomputing the digest from the delivered node is what makes this an
	// identity check rather than a length check.
	var delivered []string
	for ci, req := range nodeChunks {
		chunkNodes, chunkHashes := req.GetNodes(), req.GetNodeContributionHashes()
		require.Lenf(t, chunkHashes, len(chunkNodes),
			"chunk %d carries %d digests for %d nodes", ci, len(chunkHashes), len(chunkNodes))
		for i, n := range chunkNodes {
			want := contribhash.NodeContributionHash(n)
			assert.Truef(t, bytes.Equal(want[:], chunkHashes[i]),
				"chunk %d position %d: node %q carries a digest that is not its own — "+
					"the node slice was permuted without its index-aligned hash array", ci, i, n.GetId())
			delivered = append(delivered, n.GetId())
		}
	}

	// NO NODE DROPPED by the permutation.
	assert.Len(t, delivered, len(nodes), "every input node must land across the chunks")

	// FIXTURE CONTROL: the permutation really happened, so the assertion above is
	// not satisfied by an identity reorder.
	assert.NotEqual(t, inputOrder, delivered,
		"the delivered order must differ from the input order, or no grouping ran and this test is vacuous")
	fileOf := map[string]string{}
	for _, n := range nodes {
		fileOf[n.GetId()] = n.GetFilePath()
	}
	seen := map[string]bool{}
	prev := "\x00never-a-path"
	for _, id := range delivered {
		p := fileOf[id]
		if p == prev {
			continue
		}
		assert.Falsef(t, seen[p], "file %q resumes after another file's nodes — the delivered order is not file-grouped", p)
		seen[p] = true
		prev = p
	}
}
