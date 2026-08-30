// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"context"
	"fmt"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"
)

// The chunk and edge cardinality the FROZEN benchmark fixture yields. Pinned as
// EXTERNAL EXPECTATIONS, derived once from the fixture and written down here —
// not recomputed from the other side of the comparison. Two sets that lost the
// same members are still equal, so an identity assertion alone would stay green
// on a chunker that emitted nothing at all; these constants are what makes that
// state fail.
//
// They move only when the fixture moves, and the fixture is frozen.
const (
	benchFixtureChunks = 143
	benchFixtureEdges  = 293
	// The declarations in the fixture that carry a composed signature. It is
	// the ONLY difference the type-facts arm makes to this input, which is what
	// the identity assertion below states positively.
	benchFixtureSigChunks = 12
)

// chunkFixtureArmed chunks the frozen benchmark fixture with the Go
// qualifier-type and type-facts arms in their production registration.
func chunkFixtureArmed(t *testing.T, src []byte) *Result {
	t.Helper()
	// KNOWN-POSITIVE CONTROL, mirroring BenchmarkChunkFile's: an arm that is
	// not registered here would make this side measure the same code as the
	// unarmed one, and every comparison below would pass while proving nothing.
	_, quals := qualifierTypeResolvers[LangGo]
	require.True(t, quals, "control: the Go qualifier-type arm is NOT registered, so this is not the armed reading")
	_, facts := typeFactsResolvers[LangGo]
	require.True(t, facts, "control: the Go type-facts arm is NOT registered, so this is not the armed reading")

	return chunkBenchFixture(t, src)
}

// chunkFixtureUnarmed chunks the same fixture with both Go arms unregistered,
// restoring them before it returns.
func chunkFixtureUnarmed(t *testing.T, src []byte) *Result {
	t.Helper()
	UnregisterQualifierTypes(LangGo)
	UnregisterTypeFacts(LangGo)
	// RESTORE THE PRODUCTION ARMS. An arm left unregistered would silently
	// disarm the feature for every later test in this binary.
	t.Cleanup(func() {
		RegisterQualifierTypes(LangGo, goQualifierTypes)
		RegisterTypeFacts(LangGo, goTypeFacts)
	})
	if _, ok := qualifierTypeResolvers[LangGo]; ok {
		t.Fatal("control: the Go qualifier-type arm is still registered, so this is not the unarmed reading")
	}
	if _, ok := typeFactsResolvers[LangGo]; ok {
		t.Fatal("control: the Go type-facts arm is still registered, so this is not the unarmed reading")
	}

	return chunkBenchFixture(t, src)
}

// chunkBenchFixture runs one ChunkFile pass over the fixture bytes.
func chunkBenchFixture(t *testing.T, src []byte) *Result {
	t.Helper()
	chunker := NewChunker()
	defer chunker.Close()

	res, err := chunker.ChunkFile(context.Background(), benchInputPath, src)
	require.NoError(t, err)
	return res
}

// chunkIdentities renders a result's chunks as comparable strings.
func chunkIdentities(res *Result) []string {
	out := make([]string, 0, len(res.Chunks))
	for _, c := range res.Chunks {
		out = append(out, fmt.Sprintf("%s|%s|%s|%d-%d|%d-%d",
			c.ChunkType, c.ParentName, c.Name, c.StartLine, c.EndLine, c.StartByte, c.EndByte))
	}
	sort.Strings(out)
	return out
}

// edgeIdentities renders a result's edges as comparable strings.
func edgeIdentities(res *Result) []string {
	out := make([]string, 0, len(res.Edges))
	for _, e := range res.Edges {
		out = append(out, fmt.Sprintf("%s|%s|%s|%g|%d-%d|%s",
			e.Type, e.FromID, e.ToID, e.Weight, e.FromChunk, e.ToChunk, e.Evidence))
	}
	sort.Strings(out)
	return out
}

// TestGoArmsEmitIdenticalChunksAndEdges pins the property the allocation
// budget is a budget FOR: the Go qualifier-type and type-facts arms buy
// declaration facts, and they must buy them without moving a single chunk or
// edge.
//
// THE FLOW-STEP ARM STAYS REGISTERED ON BOTH SIDES and is deliberately not part
// of the comparison. It is a third Go arm, registered independently of these
// two, so its FLOWS_TO_ARG and FLOWS_TO_RETURN edges appear in BOTH readings and
// cancel — which is exactly why the edge count below is a fixture-derived
// constant rather than a number either side computes. When that arm's output
// changes, this constant moves and the identity assertion does not. Everything the arms cost is therefore marginal cost on an identical
// emission — which is exactly what makes an arm_on/arm_off allocation ratio a
// meaningful number rather than a comparison of two different outputs.
//
// It is also the standing guard on any allocation work done under that ratchet:
// a reduction that changed what the chunker emits would be a behavior change
// wearing a performance change's clothes, and this test is what refuses it.
func TestGoArmsEmitIdenticalChunksAndEdges(t *testing.T) {
	src := loadBenchInput(t)

	// The ARMED reading is taken FIRST and its identities captured, because the
	// unarmed helper unregisters the arms for the rest of the test.
	armed := chunkFixtureArmed(t, src)
	armedChunks := chunkIdentities(armed)
	armedEdges := edgeIdentities(armed)

	// THE CARDINALITY GUARD, against fixture-derived constants rather than
	// against the other side. Without it, a chunker emitting nothing would
	// satisfy every equality below.
	require.Len(t, armedChunks, benchFixtureChunks,
		"the frozen fixture's chunk count moved; the fixture is frozen, so this is a chunker change and every recorded ratchet number needs re-deriving")
	require.Len(t, armedEdges, benchFixtureEdges,
		"the frozen fixture's edge count moved; see the chunk-count message")

	// THE ARM MUST ACTUALLY DO SOMETHING. This is the known-positive that
	// separates "the arms emit identical output" from "the arms are inert": the
	// signature facts exist on the armed side and are absent on the unarmed
	// one, and that difference is the whole of what the ratchet's numerator
	// pays for.
	require.Equal(t, benchFixtureSigChunks, countSigChunks(armed),
		"the armed reading does not carry the expected number of composed signatures, so the type-facts arm is not doing the work this ratchet budgets for")

	unarmed := chunkFixtureUnarmed(t, src)
	require.Zero(t, countSigChunks(unarmed),
		"the unarmed reading carries composed signatures, so UnregisterTypeFacts did not take effect and both readings measure the same code")

	require.Equal(t, armedChunks, chunkIdentities(unarmed),
		"the Go arms changed which chunks are emitted; the arms are supposed to annotate declarations, never to move them")
	require.Equal(t, armedEdges, edgeIdentities(unarmed),
		"the Go arms changed which edges are emitted; the arms are supposed to annotate declarations, never to change the graph")
}

// countSigChunks counts the chunks carrying a composed signature.
func countSigChunks(res *Result) int {
	n := 0
	for _, c := range res.Chunks {
		if c.TypeFacts != nil && c.TypeFacts.Sig != nil {
			n++
		}
	}
	return n
}
