// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTestCallsProvenance pins the three behaviors ful1350 changes, plus the
// leaf rule that is the only thing standing between the design and an edge set
// asserting false calls-relations.
//
// EVERY FIXTURE CARRIES ITS OWN KNOWN-POSITIVE CONTROL: a production caller
// calling a production helper, whose CALLS edge must survive. Three of the four
// subtests assert the ABSENCE of a CALLS edge for the test-called symbol, and
// an absence is unreadable on its own — a chunker that emitted no CALLS edges
// at all, or a query that stopped matching, would satisfy every one of them.
// The control fails in exactly those cases and passes only while ordinary CALLS
// emission is alive in the same run.
//
// Fixture symbols are DISTINCT per subtest and distinct within each fixture, so
// no assertion can be satisfied by a capture that saw a different one.
func TestTestCallsProvenance(t *testing.T) {
	t.Run("bare_statement_emits_test_calls", func(t *testing.T) {
		// The population the whole ticket exists to make visible: a call in a
		// BARE expression statement, which today emits nothing at all because
		// walkTestBlocks never reached extractCallEdges.
		const src = `function alphaHelper() { return 1; }
function alphaCaller() { return alphaHelper(); }
function alphaProduce() { return 2; }

it("alpha case", () => {
  alphaProduce();
});
`
		res := chunkTestCallsFixture(t, "Alpha.test.tsx", src)

		assert.True(t, hasEdge(res, EdgeTestCalls, "alpha case", "alphaProduce"),
			"the bare statement's call emits TEST_CALLS from the test block")
		assert.False(t, hasEdgeTo(res, EdgeCalls, "alphaProduce"),
			"no CALLS edge carries the test-origin call")

		assert.True(t, hasEdge(res, EdgeCalls, "alphaCaller", "alphaHelper"),
			"CONTROL: an ordinary production call still emits CALLS in this same run")
	})

	t.Run("leaked_lexical_declaration_migrates", func(t *testing.T) {
		// This edge EXISTS today and is CALLS: the unanchored (lexical_declaration)
		// @decl pattern matches at any depth, so a binding inside a test body has
		// always chunked as a declaration and emitted its calls. The subtest proves
		// MIGRATION rather than mere addition — and it is what reds when the
		// test-block range collection lands after the emitDeclarationEdges loop
		// instead of before it, because the containment test then runs against
		// nothing and every leak silently tests as not-leaked.
		const src = `function betaHelper() { return 1; }
function betaCaller() { return betaHelper(); }
function betaProduce() { return 2; }

it("beta case", () => {
  const betaValue = betaProduce();
  return betaValue;
});
`
		res := chunkTestCallsFixture(t, "Beta.test.tsx", src)

		assert.True(t, hasEdge(res, EdgeTestCalls, "betaValue", "betaProduce"),
			"the leaked lexical_declaration's call edge is relabeled TEST_CALLS")
		assert.False(t, hasEdgeTo(res, EdgeCalls, "betaProduce"),
			"nothing test-origin is left behind as CALLS")

		assert.True(t, hasEdge(res, EdgeCalls, "betaCaller", "betaHelper"),
			"CONTROL: a production declaration OUTSIDE every test-block range keeps CALLS")
	})

	t.Run("orphan_twin_suppressed", func(t *testing.T) {
		// Every top-level test block was chunked twice — once as test_block, once
		// as an orphan expression_statement over the same source. This subtest is
		// the explicit record that the node population changed.
		const src = `function gammaHelper() { return 1; }
function gammaCaller() { return gammaHelper(); }

describe("gamma suite", () => {
  gammaProduce();
});
`
		res := chunkTestCallsFixture(t, "Gamma.test.tsx", src)

		start := strings.Index(src, `describe("gamma suite"`)
		require.GreaterOrEqual(t, start, 0, "fixture control: the describe block is in the source")

		var covering []Chunk
		for _, ch := range res.Chunks {
			if ch.StartByte == start {
				covering = append(covering, ch)
			}
		}
		require.Len(t, covering, 1,
			"exactly one chunk covers the top-level test block's range; got %s", chunkTypes(covering))
		assert.Equal(t, "test_block", covering[0].ChunkType,
			"and the surviving chunk is the test_block, not the orphan expression_statement")

		assert.True(t, hasEdge(res, EdgeCalls, "gammaCaller", "gammaHelper"),
			"CONTROL: suppressing the twin did not suppress ordinary declaration chunking")
	})

	t.Run("nested_innermost_only", func(t *testing.T) {
		// THE LEAF GATE. Without it an implementation that emits from every
		// enclosing block passes every other assertion in this file while
		// multiplying each callee's inbound count by its nesting depth.
		// Outer, inner and callee names are mutually distinct so the FROM-endpoint
		// assertion can tell them apart.
		const src = `function etaHelper() { return 1; }
function etaCaller() { return etaHelper(); }
function zetaProduce() { return 2; }

describe("delta suite", () => {
  it("epsilon case", () => {
    zetaProduce();
  });
});
`
		res := chunkTestCallsFixture(t, "Delta.test.tsx", src)

		var froms []string
		for i := range res.Edges {
			e := &res.Edges[i]
			if e.Type == EdgeTestCalls && e.ToID == "zetaProduce" {
				froms = append(froms, e.FromID)
			}
		}
		require.Len(t, froms, 1,
			"the callee receives EXACTLY ONE TEST_CALLS edge; got %v", froms)
		assert.True(t, strings.HasSuffix(froms[0], "epsilon case"),
			"its FROM endpoint is the INNER block's chunk, not the outer describe; got %q", froms[0])

		for i := range res.Edges {
			e := &res.Edges[i]
			assert.False(t, strings.HasSuffix(e.FromID, "delta suite") && e.ToID == "zetaProduce",
				"the OUTER describe emits no edge of any type for the inner block's callee")
		}

		assert.True(t, hasEdge(res, EdgeCalls, "etaCaller", "etaHelper"),
			"CONTROL: ordinary CALLS emission is alive, so the single-edge count is a real measurement")
	})
}

// chunkTestCallsFixture chunks one in-memory fixture through the real Chunker.
// The testdata tree is owned by the TestKind corpus tests, which fail on
// unexpected files, so fixtures live as string literals here.
func chunkTestCallsFixture(t *testing.T, name, src string) *Result {
	t.Helper()
	c := NewChunker()
	t.Cleanup(c.Close)
	res, err := c.ChunkFile(context.Background(), "/tmp/ful1350/"+name, []byte(src))
	require.NoError(t, err)
	require.NotEmpty(t, res.Chunks, "fixture control: the file produced chunks")
	return res
}

// hasEdge reports whether an edge of the given type runs from a source whose
// qualified ID ends in fromSuffix to exactly toID. The suffix match keeps the
// assertion off the namespace prefix, which is derived from the file's path.
func hasEdge(res *Result, typ EdgeType, fromSuffix, toID string) bool {
	for i := range res.Edges {
		e := &res.Edges[i]
		if e.Type == typ && e.ToID == toID && strings.HasSuffix(e.FromID, fromSuffix) {
			return true
		}
	}
	return false
}

// hasEdgeTo reports whether ANY edge of the given type targets toID, from any
// source. It backs the "nothing is left behind as CALLS" assertions.
func hasEdgeTo(res *Result, typ EdgeType, toID string) bool {
	for i := range res.Edges {
		if res.Edges[i].Type == typ && res.Edges[i].ToID == toID {
			return true
		}
	}
	return false
}

func chunkTypes(chunks []Chunk) string {
	var out []string
	for _, ch := range chunks {
		out = append(out, ch.ChunkType)
	}
	return strings.Join(out, ", ")
}
