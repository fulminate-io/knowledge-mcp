// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// The fixture harness for the member-ownership cases. It sits apart from the
// cases themselves because the two grow for different reasons: a harness change
// is about how a fixture reaches the derivation, a case change is about which
// shape is covered — and keeping them together put the pair over this
// repository's per-file line cap.

// ownerFixtureEdges runs the production populate path and returns the
// DECLARED-CONFORMANCE relationships it produced.
//
// It filters to the declared half on purpose: the method-set derivation stamps
// its own prefix and is not this test's subject, and a bare IMPLEMENTS filter
// would let a Go-shaped pair drift into these assertions unnoticed.
func ownerFixtureEdges(t *testing.T, files []fixtureFile) []*knowledgev1.Edge {
	t.Helper()
	var out []*knowledgev1.Edge
	for _, e := range populateFixture(t, files).Edges {
		if kgtypes.EdgeType(e.Type) != kgtypes.EdgeImplements {
			continue
		}
		if !strings.HasPrefix(e.Method, kgtypes.EdgeMethodDeclaredConformance) {
			continue
		}
		out = append(out, e)
	}
	return out
}

// ownerFixtureIndex builds the declaration index for a fixture through the
// production ORDER, including the owner stamping, so a test can read the
// derivation's STATS rather than only its edges.
func ownerFixtureIndex(t *testing.T, files []fixtureFile) *declIndex {
	t.Helper()
	chunker := treesitter.NewChunker()
	defer chunker.Close()

	results := make([]*treesitter.Result, 0, len(files))
	for _, f := range files {
		r, err := chunker.ChunkFile(context.Background(), f.path, []byte(f.src))
		require.NoError(t, err, "chunking %s", f.path)
		results = append(results, r)
	}
	DeduplicateChunks(results)
	resolveSlotEdges(results)
	rc := treesitter.RepoContext{ModulePath: "example.com/fixture"}
	fillBinds(&rc, results)

	ix := newDeclIndex(0)
	for _, r := range results {
		for _, chunk := range r.Chunks {
			if kgtypes.NodeType(chunk.ChunkType).IsComment() {
				continue
			}
			indexDeclaration(ix, r, chunk, ChunkNodeID(chunk))
		}
	}
	stampDeclOwners(ix, results)
	return ix
}

// ownerFixtureIndexInjected builds the index from REAL chunking with type facts
// supplied by the caller.
//
// IT EXISTS FOR LANGUAGES WHOSE CAPTURE ARM IS ANOTHER TICKET'S. What is under
// test here is the shared member PAIRING, not any language's capture, so the
// facts are injected while the CONTAINMENT — the thing the pairing now reads —
// stays entirely real: the chunker parses the source, the slot pass resolves the
// containment edges, and the owner stamping reads those. Injecting the facts
// keeps a pairing defect reproducible on a language that cannot yet capture,
// without pretending the capture exists.
func ownerFixtureIndexInjected(
	t *testing.T,
	files []fixtureFile,
	facts func(chunk treesitter.Chunk, nodeID string) *treesitter.TypeFacts,
) *declIndex {
	t.Helper()
	chunker := treesitter.NewChunker()
	defer chunker.Close()

	results := make([]*treesitter.Result, 0, len(files))
	for _, f := range files {
		r, err := chunker.ChunkFile(context.Background(), f.path, []byte(f.src))
		require.NoError(t, err, "chunking %s", f.path)
		results = append(results, r)
	}
	DeduplicateChunks(results)
	resolveSlotEdges(results)
	rc := treesitter.RepoContext{ModulePath: "example.com/fixture"}
	fillBinds(&rc, results)

	ix := newDeclIndex(0)
	for _, r := range results {
		for _, chunk := range r.Chunks {
			if kgtypes.NodeType(chunk.ChunkType).IsComment() {
				continue
			}
			id := ChunkNodeID(chunk)
			chunk.TypeFacts = facts(chunk, id)
			indexDeclaration(ix, r, chunk, id)
		}
	}
	stampDeclOwners(ix, results)
	return ix
}

// ownerMemberPaired reports whether the derivation paired a supertype member
// with a subtype member.
func ownerMemberPaired(pairs []conformPair, specID, implID string) bool {
	for _, p := range pairs {
		for _, m := range p.members {
			if m.spec.NodeID == specID && m.impl.NodeID == implID {
				return true
			}
		}
	}
	return false
}

// ownerAnySpecPaired reports whether a supertype member was paired with
// ANYTHING, whatever the subtype member turned out to be.
func ownerAnySpecPaired(pairs []conformPair, specID string) bool {
	for _, p := range pairs {
		for _, m := range p.members {
			if m.spec.NodeID == specID {
				return true
			}
		}
	}
	return false
}

// ownerEdgeExists reports whether a relationship runs from→to.
func ownerEdgeExists(edges []*knowledgev1.Edge, from, to string) bool {
	for _, e := range edges {
		if e.FromId == from && e.ToId == to {
			return true
		}
	}
	return false
}

// chunkKindOf re-chunks a fixture and returns the tree-sitter kind of the chunk
// carrying a node ID, so a test can name WHICH of two same-named containers a
// record is without hard-coding a path hash.
func chunkKindOf(t *testing.T, files []fixtureFile, nodeID string) string {
	t.Helper()
	chunker := treesitter.NewChunker()
	defer chunker.Close()

	results := make([]*treesitter.Result, 0, len(files))
	for _, f := range files {
		r, err := chunker.ChunkFile(context.Background(), f.path, []byte(f.src))
		require.NoError(t, err)
		results = append(results, r)
	}
	DeduplicateChunks(results)
	for _, r := range results {
		for _, chunk := range r.Chunks {
			if ChunkNodeID(chunk) == nodeID {
				return chunk.ChunkType
			}
		}
	}
	t.Fatalf("no chunk carries node id %q", nodeID)
	return ""
}
