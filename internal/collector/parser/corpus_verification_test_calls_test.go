// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"fmt"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// The TEST-ATTRIBUTED half of the corpus picture. Its own file for the same
// 500-line reason corpus_verification_go_test.go and
// corpus_verification_sibling_test.go exist.
//
// THESE ROWS ARE SEPARATE ROWS AND ARE NEVER FOLDED INTO THE PRODUCTION
// COUNTERS. bound, ambiguous_groups and dynamic_groups are each read by a
// landed gate, so adding test traffic to any of them would move a shipped
// gate's population without changing a single line of that gate — the one
// outcome the ticket forbids. The two rows below are additive: nothing that
// existed before this file reads differently because of it.

// testCallsCensus holds the test-attributed counts.
//
// THE TWO RENDERED ROWS MEASURE DIFFERENT POPULATIONS ON PURPOSE, and reading
// them as one would be wrong:
//
//   - emitted counts what the CHUNKER produced — one per test-origin reference
//     entering resolution. The resolved-edge count is already rendered by the
//     byEdgeType block as edges_TEST_CALLS, so a second row carrying that same
//     number would say nothing; emitted-versus-resolved is the pair that tells
//     a dead seam apart from a corpus whose test bodies genuinely call nothing
//     in-repo.
//   - toProduction counts RESOLVED graph edges whose target is not test code.
//     It is the number the ticket's claim rests on, and emitted is its
//     known-positive control: toProduction reads zero both when no test calls
//     production and when walkTestBlocks never ran, and only a positive emitted
//     tells those apart.
type testCallsCensus struct {
	emitted      int
	toProduction int

	// residueCalls is the UNIDENTIFIABLE RESIDUE of Phase 3's leak migration:
	// calls whose source declaration is classified test code and which stay
	// CALLS anyway, because their language has no TestBlocks query and so no
	// test-block range for the migration's containment rule to consult.
	//
	// It is measured and LOGGED rather than rendered, because the step locks
	// the artifact to exactly two new rows. It is the boundary of the
	// migration stated as a number instead of a caveat.
	residueCalls int

	// emittedByLanguage attributes the emitted population per language, and it
	// exists to make ONE claim a measurement rather than an inference: the
	// ecma_*_references rows rose sharply at this regeneration, and the reason
	// offered is that TEST_CALLS now rides the same walk those rows count. A
	// per-language emitted count is what distinguishes that from a corpus that
	// simply grew. LOGGED, not rendered.
	emittedByLanguage map[string]int
}

// censusTestCalls counts the test-attributed populations over one corpus run.
//
// TEST RESIDENCE IS THE UNION OF TWO SIGNALS, and the union is what makes
// toProduction honest rather than merely computable:
//
//   - Chunk.IsTest, set by the per-language classifier dispatch at
//     chunker.go:485. It covers go, python, java, rust and the rest of the
//     classifier registry — and it does NOT cover typescript, tsx or
//     javascript, which register no classifier at all.
//   - the SOURCE of an emitted TEST_CALLS edge. This is not a second heuristic:
//     it is Phase 3's own identifiability rule read back, since the chunker
//     stamps TEST_CALLS exactly when the emitting declaration's byte range sits
//     inside a test block's. Without it every ECMAScript helper declared in a
//     spec file would count as production and inflate the very number this
//     census exists to report.
//
// WHAT THE UNION STILL MISSES, stated rather than smoothed: a test-resident
// declaration in a no-classifier language that emits no call of its own is
// invisible to both signals, so an edge targeting it counts as production.
func censusTestCalls(results []*treesitter.Result, edges []*knowledgev1.Edge) testCallsCensus {
	census := testCallsCensus{emittedByLanguage: map[string]int{}}

	classified := map[string]bool{}
	for _, result := range results {
		for _, chunk := range result.Chunks {
			if chunk.IsTest {
				classified[ChunkNodeID(chunk)] = true
			}
		}
	}

	testResident := make(map[string]bool, len(classified))
	for id := range classified {
		testResident[id] = true
	}
	for _, result := range results {
		for i := range result.Edges {
			e := &result.Edges[i]
			switch e.Type {
			case treesitter.EdgeTestCalls:
				census.emitted++
				census.emittedByLanguage[string(result.Language)]++
				testResident[e.FromID] = true
			case treesitter.EdgeCalls:
				if classified[e.FromID] {
					census.residueCalls++
				}
			case treesitter.EdgeContains, treesitter.EdgeImports,
				treesitter.EdgeUsesType, treesitter.EdgeEmbeds:
			}
		}
	}

	for _, e := range edges {
		if kgtypes.EdgeType(e.Type) != kgtypes.EdgeTestCalls {
			continue
		}
		if !testResident[e.ToId] {
			census.toProduction++
		}
	}
	return census
}

// renderTestCalls writes the two test-attributed rows.
//
// NO NIL OR EMPTY GUARD, deliberately: a row that disappears when the census
// did not run would be read by ful1336_check.awk's numeric() as an ABSENT row
// — which it fails loudly on — but by a plain grep gate as nothing at all. A
// structural zero must arrive AS a zero.
func (r *corpusReport) renderTestCalls(b *strings.Builder) {
	fmt.Fprintf(b, "test_calls_edges=%d\n", r.testCalls.emitted)
	fmt.Fprintf(b, "test_calls_test_to_production=%d\n", r.testCalls.toProduction)
}
