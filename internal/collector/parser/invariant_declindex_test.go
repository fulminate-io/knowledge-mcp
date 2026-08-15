// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// TestInvariant_DeclIndexIdentity pins the three properties the declaration
// index exists to hold: an identity collision is REJECTED rather than absorbed,
// a key collision KEEPS every declaration instead of overwriting all but one,
// and the two keyed views answer genuinely different questions.
//
// Three of the four subtests build the index directly rather than through
// Populate, so no later sweep or global operation can hollow their fixtures out
// from under the assertions. The one that does run the chunker asserts its
// cardinality against a constant the fixture states, never against a length
// taken from another lookup's result.
func TestInvariant_DeclIndexIdentity(t *testing.T) {
	t.Run("duplicate_node_id_is_rejected", func(t *testing.T) {
		// THE KNOWN-POSITIVE CONTROL for the whole "a collision is
		// unrepresentable" claim. The plan-time corpus measured 32,038
		// declarations and zero duplicate IDs, but a zero observed over a
		// fixture cannot distinguish an ENFORCED invariant from one that is
		// merely never exercised. Only an add that is required to fail can.
		ix := newDeclIndex(4)

		first := &declRec{NodeID: "svc/a.go:Handler", File: "svc/a.go", Scope: "dir:svc", Name: "Handler"}
		require.NoError(t, ix.add(first))

		dup := &declRec{NodeID: "svc/a.go:Handler", File: "svc/b.go", Scope: "dir:svc", Name: "Handler"}
		err := ix.add(dup)
		require.Error(t, err, "a second declaration under an existing node ID must be rejected")
		require.Contains(t, err.Error(), "svc/a.go:Handler",
			"the error must name the colliding ID so the defect is attributable")

		// The FIRST record survives: add rejects the newcomer rather than
		// letting it overwrite, which is the behavior resolution depends on.
		require.Same(t, first, ix.byID["svc/a.go:Handler"])

		// CONTROL for the control: add is not simply failing on everything.
		require.NoError(t, ix.add(&declRec{
			NodeID: "svc/b.go:Handler", File: "svc/b.go", Scope: "dir:svc", Name: "Handler",
		}))
	})

	t.Run("collided_key_keeps_every_declaration", func(t *testing.T) {
		// The behavior being replaced: recordSymbol wrote its key with a bare
		// assignment (`symbolMap[key] = nodeID`), so three declarations sharing
		// one key left one survivor and two silently deleted — 668 collided
		// keys destroying 826 declarations on the measured corpus. Here the
		// lookup returns the SET, and policy decides afterwards.
		ix := newDeclIndex(4)
		for _, f := range []string{"svc/a.go", "svc/b.go", "svc/c.go"} {
			require.NoError(t, ix.add(&declRec{
				NodeID: f + ":Handler", File: f, Scope: "dir:svc", Name: "Handler",
			}))
		}

		got := ix.lookup(declKey{Scope: "dir:svc", Name: "Handler"})
		require.Len(t, got, 3, "every declaration of a collided key must survive the index")

		// Build order is file order, and it is deterministic by construction —
		// the index is built from sorted results, never from a map range.
		var files []string
		for _, rec := range got {
			files = append(files, rec.File)
		}
		require.Equal(t, []string{"svc/a.go", "svc/b.go", "svc/c.go"}, files)
	})

	t.Run("base_name_finds_the_suffixed_declarations", func(t *testing.T) {
		// The catcher for baseDeclName being written but not wired. A
		// declaration whose (parent, name) collides inside its file takes a
		// "#<astPathHash>" suffix that flows into its node ID, while a
		// REFERENCE to it writes the base name only. Key on the identity and
		// every such reference silently becomes External.
		//
		// Two same-name declarations in ONE file, which Python permits.
		const fixturePath = "bin/dispatch.py"
		const collidedDecls = 2 // fixture-derived: the file declares handle() twice
		files := []fixtureFile{{path: fixturePath, src: "" +
			"def handle():\n    return 'first definition of the handler'\n\n" +
			"def handle():\n    return 'second definition, a different body'\n"}}

		results := chunkFixture(t, files)

		// The suffix must be present in the CHUNKER's own output, before the
		// parser-side DeduplicateChunks runs. Both mechanisms append the same
		// '#' shape (indexer_chunk.go:179-184 is the fallback), so asserting
		// after dedup could not tell which one produced it — and this subtest
		// is specifically about resolveCollisionNames' suffix reaching a
		// base-keyed lookup intact.
		suffixed := 0
		for _, c := range results[0].Chunks {
			if strings.HasPrefix(c.Name, "handle#") {
				suffixed++
			}
		}
		require.Equal(t, collidedDecls, suffixed,
			"fixture must provoke resolveCollisionNames: expected %d suffixed handle declarations from the chunker",
			collidedDecls)

		ix := indexResults(t, results)

		scope := treesitter.ScopeID(fixturePath, results[0].Language, "")
		got := ix.lookup(declKey{Scope: scope, Name: "handle"})
		require.Len(t, got, collidedDecls,
			"a lookup by the BASE name must return every suffixed declaration of it")
		for _, rec := range got {
			require.Contains(t, rec.NodeID, "handle#",
				"the record's identity keeps the suffix even though its key does not")
		}
	})

	t.Run("scope_name_view_spans_parents_and_stops_at_the_scope", func(t *testing.T) {
		// byScopeName has exactly one consumer — RuleDynamicScope — so without
		// this subtest it is verified only indirectly. Three declarations of
		// one name: two in the same scope under DIFFERENT parents, one in a
		// different scope.
		const sameScopeDecls = 2 // fixture-derived: Alpha.Get and Beta.Get in dir:svc
		ix := newDeclIndex(4)
		for _, rec := range []*declRec{
			{NodeID: "svc/alpha.go:Alpha.Get", File: "svc/alpha.go", Scope: "dir:svc", Parent: "Alpha", Name: "Get"},
			{NodeID: "svc/beta.go:Beta.Get", File: "svc/beta.go", Scope: "dir:svc", Parent: "Beta", Name: "Get"},
			{NodeID: "other/gamma.go:Alpha.Get", File: "other/gamma.go", Scope: "dir:other", Parent: "Alpha", Name: "Get"},
		} {
			require.NoError(t, ix.add(rec))
		}

		got := ix.lookupScopeName(scopeNameKey{Scope: "dir:svc", Name: "Get"})
		// Counted against the fixture-derived constant. Comparing it to the
		// length of another lookup would pass whenever BOTH lost the same
		// members.
		require.Len(t, got, sameScopeDecls,
			"the scope-name view must span parents: fewer than %d means it is parent-qualified like byKey and dynamic groups are under-populated",
			sameScopeDecls)
		for _, rec := range got {
			require.Equal(t, "dir:svc", rec.Scope,
				"the scope-name view leaked across scopes: RuleDynamicScope has degraded into a name-wide search")
		}

		// The contrast that proves the two views are not the same map read
		// twice: under an exact parent, byKey returns one of the two.
		qualified := ix.lookup(declKey{Scope: "dir:svc", Parent: "Alpha", Name: "Get"})
		require.Len(t, qualified, 1, "byKey is parent-qualified by construction")
		require.Equal(t, "svc/alpha.go:Alpha.Get", qualified[0].NodeID)
	})
}

// chunkFixture runs the real chunker over each fixture file in order and
// returns the raw results, BEFORE DeduplicateChunks — the point at which the
// chunker's own collision suffixes are still distinguishable from the parser's.
func chunkFixture(t *testing.T, files []fixtureFile) []*treesitter.Result {
	t.Helper()
	chunker := treesitter.NewChunker()
	defer chunker.Close()

	results := make([]*treesitter.Result, 0, len(files))
	for _, f := range files {
		r, err := chunker.ChunkFile(context.Background(), f.path, []byte(f.src))
		require.NoError(t, err, "chunking %s", f.path)
		results = append(results, r)
	}
	return results
}

// indexResults builds a declIndex from chunker results through the SAME
// production path chunkResultsToPopulate uses: DeduplicateChunks first (it
// rewrites chunk.Name and so changes ChunkNodeID), comment chunks skipped
// because populate never creates a node for one, then indexDeclaration.
func indexResults(t *testing.T, results []*treesitter.Result) *declIndex {
	t.Helper()
	DeduplicateChunks(results)

	total := 0
	for _, r := range results {
		total += len(r.Chunks)
	}
	ix := newDeclIndex(total)
	for _, r := range results {
		for _, chunk := range r.Chunks {
			if kgtypes.NodeType(chunk.ChunkType).IsComment() {
				continue
			}
			indexDeclaration(ix, r, chunk, ChunkNodeID(chunk))
		}
	}
	return ix
}
