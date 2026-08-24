// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// edgeIdentityRootEnv points this probe at a real repository. Like the corpus
// verification beside it, the run is a MEASUREMENT INSTRUMENT rather than a
// gate, so it SKIPS when unset: the numbers depend on the corpus, and a corpus
// is not something CI can be assumed to have.
const edgeIdentityRootEnv = "FUL1405_CORPUS_ROOT"

// TestFUL1405CorpusEdgeIdentity measures whether adding a per-edge ATTRIBUTION
// changed the EDGE SET itself.
//
// THE IDENTITY IS (From, To, Type, Weight) AND NOTHING ELSE. That tuple is what
// "the same edges" means: which declarations are connected, by what kind of
// reference, at what strength. Method is deliberately outside it — the whole
// point of the measurement is that the attribution moves while the set does not,
// so a digest covering Method would go different by construction and prove
// nothing. Confidence and Evidence are outside it for the same reason they are
// outside the collect membership: they describe a candidate GROUP rather than
// the connection.
//
// THIS INSTRUMENT WRITES NOTHING. The corpus verification beside it writes a
// tracked artifact that landed release gates read, so putting these rows there
// would move those gates for a measurement that has nothing to do with them.
//
// THE DIGEST FORMAT IS PINNED BY THE CRITERIA THAT READ IT. Any change to the
// identity line — a field, a separator, the float precision, the trailing
// newline — produces a different digest for an unchanged edge set, which reads
// as a false failure. If the format must ever change, the expected digests must
// be re-derived against a pre-stamp tree and the criteria amended together.
func TestFUL1405CorpusEdgeIdentity(t *testing.T) {
	root := os.Getenv(edgeIdentityRootEnv)
	if root == "" {
		t.Skipf("set %s to a repository path to run the corpus edge-identity probe", edgeIdentityRootEnv)
	}
	// Sanitized before it reaches discovery: an operator-supplied root is an
	// external input, and the walk below joins it with every discovered path.
	root = filepath.Clean(root)
	require.True(t, filepath.IsAbs(root), "%s must be an absolute path, got %q", edgeIdentityRootEnv, root)

	ctx := context.Background()
	files, err := DiscoverFiles(ctx, root)
	require.NoError(t, err)
	require.NotEmpty(t, files, "control: discovery found no files under %s", root)

	results, _, err := ChunkFilesParallel(ctx, root, files)
	require.NoError(t, err)
	require.NotEmpty(t, results, "control: chunking produced no results")

	// PRODUCTION ORDER, and each step is load-bearing. DeduplicateChunks fixes
	// the names the node IDs derive from; resolveSlotEdges consumes the chunk
	// slots, which a later sort would invalidate — so this walk deliberately does
	// NOT sort each result's chunks; fillBinds fills the import binds the two
	// import rungs read, and without the module path an arm that maps import
	// paths onto repo directories returns its zero result on every file with no
	// error and no other signal.
	DeduplicateChunks(results)
	resolveSlotEdges(results)
	modulePath, _ := ReadModulePath(root)
	rc := treesitter.RepoContext{Root: root, ModulePath: modulePath, Files: files}
	fillBinds(&rc, results)

	ix := newDeclIndex(0)
	nodeIDs := map[string]bool{}
	for _, r := range results {
		nodeIDs[r.FilePath] = true
		for _, chunk := range r.Chunks {
			if kgtypes.NodeType(chunk.ChunkType).IsComment() {
				continue
			}
			id := ChunkNodeID(chunk)
			nodeIDs[id] = true
			indexDeclaration(ix, r, chunk, id)
		}
	}

	edges, stats := resolveEdgesWithStats(results, ix, nodeIDs)
	require.NotEmpty(t, edges, "control: resolution produced no edges at all")

	lines := make([]string, 0, len(edges))
	byMethod := map[string]int{}
	serialized := 0
	for _, e := range edges {
		lines = append(lines, fmt.Sprintf("%s\t%s\t%s\t%.9f", e.FromId, e.ToId, e.Type, e.Weight))
		byMethod[e.Method]++
		b, err := proto.Marshal(e)
		require.NoError(t, err)
		serialized += len(b)
	}
	// SORTED, so the digest is a property of the SET rather than of the order the
	// walk happened to visit files in — which is parallel and therefore not a
	// property anything guarantees.
	sort.Strings(lines)
	sum := sha256.New()
	for _, line := range lines {
		sum.Write([]byte(line + "\n"))
	}

	t.Logf("identity_digest=%s edges=%d serialized_bytes=%d bound=%d external=%d",
		hex.EncodeToString(sum.Sum(nil)), len(edges), serialized, stats.Bound, stats.External)
	for _, m := range sortedKeys(byMethod) {
		t.Logf("method=%q count=%d", m, byMethod[m])
	}

	// THE COMPLETENESS PROOF: every bound edge carries a rung, so the rung counts
	// must SUM to the bound count exactly. An equality rather than a positive
	// check, because a stamp that reached most arms and missed one would leave
	// this short while every per-rung fixture stayed green.
	stamped := 0
	for _, rule := range boundReachableRules {
		stamped += byMethod[string(rule)]
	}
	require.Equal(t, stats.Bound, stamped,
		"every bound edge must carry one of the bound-reachable rungs — a shortfall is an unstamped arm")

	// KNOWN POSITIVE FOR THE EMPTY BUCKET. CONTAINS and IMPORTS edges never enter
	// resolution and legitimately carry no Method, so a zero here would mean the
	// walk produced no such edges at all and the equality above was measuring a
	// corpus that is not the real one.
	require.Positive(t, byMethod[""],
		"control: containment and import edges carry no Method, so this bucket cannot be empty")
}
