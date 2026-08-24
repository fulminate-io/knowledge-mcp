// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// statsForFixture runs the production resolve pass over a fixture and returns
// its residue counts, through the same entry point the corpus verification
// reads so the numbers here are the shipped ones.
func statsForFixture(t *testing.T, files []fixtureFile) resolveStats {
	t.Helper()
	results := chunkFixture(t, files)
	fillBinds(&treesitter.RepoContext{}, results)
	DeduplicateChunks(results)
	// STRICTLY AFTER DeduplicateChunks (which rewrites chunk.Name and so
	// changes ChunkNodeID) and strictly before the index build. Without it a
	// reference edge's FromID is still the name-built spelling rather than a
	// node ID, so resolveEdgesWithStats' nodeIDs gate drops the edge and every
	// residue counter reads zero — which is exactly how this helper first
	// reported a wired counter as unwired.
	resolveSlotEdges(results)

	total := 0
	for _, r := range results {
		total += len(r.Chunks)
	}
	ix := newDeclIndex(total)
	nodeIDs := make(map[string]bool, total)
	for _, r := range results {
		for _, chunk := range r.Chunks {
			if kgtypes.NodeType(chunk.ChunkType).IsComment() {
				continue
			}
			id := ChunkNodeID(chunk)
			nodeIDs[id] = true
			indexDeclaration(ix, r, chunk, id)
		}
	}
	_, stats := resolveEdgesWithStats(results, ix, nodeIDs)
	return stats
}

// TestResolveStatsTypedQualifierCounters drives BOTH R2T counters non-zero.
//
// A COUNTER THAT CANNOT BE DRIVEN NON-ZERO IS INDISTINGUISHABLE FROM A COUNTER
// NEVER WIRED — it reads zero in both worlds, and a test asserting only that it
// exists would pass against a field nothing ever increments.
func TestResolveStatsTypedQualifierCounters(t *testing.T) {
	t.Run("binds_counter_is_non_zero", func(t *testing.T) {
		stats := statsForFixture(t, []fixtureFile{{path: "svc/svc.go", src: r2tServerSrc}})

		assert.Positive(t, stats.TypedQualifierBinds,
			"the rung bound a typed qualifier, so its bind counter moved")
		assert.Zero(t, stats.TypedQualifierGroups,
			"one exact answer is a BIND and never a group")
	})

	t.Run("group_counter_counts_groups_not_members", func(t *testing.T) {
		// TWO declarations share one (scope, parent, name), so the rung's
		// step-4 lookup returns a CLOSED ambiguous set of two. The counter must
		// move by ONE — the question it answers is "how many references did
		// this rung decide", and a group is ONE reference whose answer is a set.
		const a = `package svc

type Server struct{}

func (s *Server) Handle() {}
`
		const b = `package svc

func (s *Server) Handle() {}
`
		const caller = `package svc

func run() {
	s := Server{}
	s.Handle()
}
`
		stats := statsForFixture(t, []fixtureFile{
			{path: "svc/a.go", src: a},
			{path: "svc/b.go", src: b},
			{path: "svc/caller.go", src: caller},
		})

		require.Equal(t, 1, stats.TypedQualifierGroups,
			"ONE reference produced ONE group, however many members it holds")
		// KNOWN-POSITIVE CONTROL on the members: without this the assertion
		// above would also hold for a group that somehow carried a single
		// member, which is a bind rather than the group this case set up.
		assert.GreaterOrEqual(t, stats.AmbiguousEdges, 2,
			"control: the group really did carry more than one member")
	})
}

// TestResolveStatsTypedQualifierRoutes drives EACH of the rung's three entry
// routes non-zero, in one run, through the production resolve pass.
//
// A ROUTE COUNTER THAT CANNOT BE DRIVEN NON-ZERO IS INDISTINGUISHABLE FROM ONE
// NEVER WIRED — it reads zero in both worlds. Three separate fixtures would each
// prove one counter while leaving open whether the entry classifies them
// EXCLUSIVELY, so the three shapes sit in one file and the totals are asserted
// to agree with the undifferentiated bind count.
func TestResolveStatsTypedQualifierRoutes(t *testing.T) {
	const src = `class Config {
  load(): void {}
}

class Client {
  send(): void {}
}

function makeClient(): Client {
  return new Client();
}

class Server {
  store: Config;

  direct(cfg: Config): void {
    cfg.load();
  }

  fromCall(): void {
    const c = makeClient();
    c.send();
  }

  viaField(): void {
    this.store.load();
  }
}
`
	stats := statsForFixture(t, []fixtureFile{{path: "web/routes.ts", src: src}})

	assert.Positive(t, stats.TypedQualifierByRoute["typescript/direct_type"],
		"an annotated parameter resolves through the DIRECT-TYPE route")
	assert.Positive(t, stats.TypedQualifierByRoute["typescript/call_return"],
		"a local assigned from a call resolves through the CALL-RETURN route")
	assert.Positive(t, stats.TypedQualifierByRoute["typescript/field_hop"],
		"a dotted `this.field` qualifier resolves through the FIELD-HOP route")

	// THE SPLIT ACCOUNTS FOR EVERY BIND. Asserted against the rung's own
	// undifferentiated counter rather than against a literal, so a route the
	// entry fails to classify shows up as arithmetic that does not close rather
	// than as three numbers that each look plausible.
	total := 0
	for _, n := range stats.TypedQualifierByRoute {
		total += n
	}
	assert.Equal(t, stats.TypedQualifierBinds, total,
		"every typed-qualifier bind carries exactly one route")
}
