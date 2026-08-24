// SPDX-License-Identifier: Apache-2.0

package remote

import (
	"testing"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/contribhash"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
)

// row_contribution_hashes_test.go covers the PER-ROW hashing entry point and the
// two properties the wire contract rests on: every row of both slices is hashed,
// and the digests come back in INPUT ORDER so the caller can pair them by index.
//
// WHY THE FILELESS AND NON-CODE CASES ARE HERE RATHER THAN IMPLIED. The per-FILE
// map is computed only for diff-eligible graph families and only for file-bearing
// nodes; the per-ROW digests are computed for every family and every row, because
// contribution_hash is a column on every graph's node and edge tables. A row this
// function skipped would land NULL and strand its file.

// TestRowContributionHashes_HashesEveryRowAfterSanitize drives a result that
// mixes file-bearing and fileless rows, asserts one digest per row in input
// order, and shows the digest tracks the SANITIZED bytes the server stores.
func TestRowContributionHashes_HashesEveryRowAfterSanitize(t *testing.T) {
	// A deliberately mixed set: two file-bearing nodes, one FILELESS node (no
	// file path — outside the manifest entirely, but still a stored row), and one
	// carrying invalid UTF-8 that the sink's sanitize loop rewrites.
	nodes := []*knowledgev1.Node{
		{Id: "pkg/a.go:Alpha", Type: "function_declaration", FilePath: "pkg/a.go", Content: "func Alpha() {}"},
		{Id: "pkg/b.go:Beta", Type: "function_declaration", FilePath: "pkg/b.go", Content: "func Beta() {}"},
		{Id: "graph-level-note", Type: "document", Content: "no owning file at all"},
		{Id: "pkg/c.go:Gamma", Type: "function_declaration", FilePath: "pkg/c.go", Content: "func Gamma() {} // \xff\xfe"},
	}
	edges := []kgwire.BatchEdge{
		{FromIdx: -1, ToIdx: -1, FromID: "pkg/a.go:Alpha", ToID: "pkg/b.go:Beta", Type: kgtypes.EdgeType("CALLS")},
		{FromIdx: -1, ToIdx: -1, FromID: "pkg/b.go:Beta", ToID: "pkg/c.go:Gamma", Type: kgtypes.EdgeType("CALLS"),
			Evidence: "pkg/b.go:1:CALLS:Gamma"},
		{FromIdx: -1, ToIdx: -1, FromID: "pkg/c.go:Gamma", ToID: "pkg/a.go:Alpha", Type: kgtypes.EdgeType("USES_TYPE"),
			Evidence: "pkg/c.go:2:USES_TYPE:Alpha"},
	}

	// THE SANITIZE ORDERING, made observable. WriteResult runs this loop before
	// hashing because it rewrites hashed fields in place; hashing first would
	// digest bytes the server never stores. Capturing the pre-sanitize digest
	// gives the assertion below a known-positive: the two genuinely differ, so
	// "hashed the sanitized bytes" is a claim that could have failed.
	preSanitize := contribhash.NodeContributionHash(nodes[3])
	for _, n := range nodes {
		sanitizeNodeText(n)
	}

	nodeHashes, edgeHashes := contribhash.RowContributionHashes(nodes, edges)

	require.Len(t, nodeHashes, len(nodes), "EVERY node row is hashed, fileless ones included")
	require.Len(t, edgeHashes, len(edges), "EVERY edge row is hashed")

	// INPUT ORDER, asserted per index against a freshly computed digest. This is
	// what lets the caller pair digests with rows by index all the way onto the
	// wire's index-aligned array.
	for i, n := range nodes {
		require.Equal(t, contribhash.NodeContributionHash(n), nodeHashes[i],
			"node %d (%s) must carry its OWN digest at its OWN index", i, n.GetId())
	}
	for i := range edges {
		require.Equal(t, contribhash.EdgeContributionHash(edges[i]), edgeHashes[i],
			"edge %d must carry its OWN digest at its OWN index", i)
	}

	require.NotEqual(t, preSanitize, nodeHashes[3],
		"KNOWN-POSITIVE for the ordering: sanitize rewrote a hashed field, so hashing before it "+
			"would have produced a different digest than the one the server can reproduce")

	// A ZERO DIGEST WOULD MEAN AN UNHASHED ROW, and an all-same digest would mean
	// the fan-out wrote one index for every worker. Both are excluded by
	// distinctness over rows that genuinely differ.
	seen := make(map[[32]byte]string, len(nodes)+len(edges))
	for i, h := range nodeHashes {
		require.NotEqual(t, [32]byte{}, h, "node %d was never hashed", i)
		require.NotContains(t, seen, h, "node %d duplicates an earlier digest", i)
		seen[h] = nodes[i].GetId()
	}
	for i, h := range edgeHashes {
		require.NotEqual(t, [32]byte{}, h, "edge %d was never hashed", i)
		require.NotContains(t, seen, h, "edge %d duplicates an earlier digest", i)
		seen[h] = edges[i].FromID
	}
}

// TestRowContributionHashes_EmptyAndSingleRow pins the two boundary shapes the
// fan-out branches on: nothing to hash, and too little to fan out.
func TestRowContributionHashes_EmptyAndSingleRow(t *testing.T) {
	nodeHashes, edgeHashes := contribhash.RowContributionHashes(nil, nil)
	require.Empty(t, nodeHashes)
	require.Empty(t, edgeHashes)

	one := []*knowledgev1.Node{{Id: "pkg/a.go:Alpha", Type: "function_declaration", FilePath: "pkg/a.go"}}
	nodeHashes, edgeHashes = contribhash.RowContributionHashes(one, nil)
	require.Len(t, nodeHashes, 1)
	require.Empty(t, edgeHashes)
	require.Equal(t, contribhash.NodeContributionHash(one[0]), nodeHashes[0],
		"the serial arm must produce the same digest as the encoder it delegates to")
}

// TestBatchEdgeToProto_CarriesContributionHash pins the stamped digest surviving
// the conversion the chunker feeds on, and the unstamped case staying nil rather
// than becoming 32 zero bytes.
func TestBatchEdgeToProto_CarriesContributionHash(t *testing.T) {
	stamped := kgwire.BatchEdge{
		FromIdx: -1, ToIdx: -1, FromID: "a", ToID: "b", Type: kgtypes.EdgeType("CALLS"),
		ContributionHash: [32]byte{0xAA, 0xBB},
	}
	got := stamped.ToProto().GetContributionHash()
	require.Len(t, got, 32, "a stamped digest rides the wire at full width")
	require.Equal(t, byte(0xAA), got[0])
	require.Equal(t, byte(0xBB), got[1])

	unstamped := kgwire.BatchEdge{FromIdx: -1, ToIdx: -1, FromID: "a", ToID: "b", Type: kgtypes.EdgeType("CALLS")}
	require.Nil(t, unstamped.ToProto().GetContributionHash(),
		"unstamped stays absent on the wire, so it is distinguishable from an all-zero digest")
}
