// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// MOST SUBTESTS BUILD THE INDEX BY HAND, AND ONE DOES NOT. The hand-built ones
// stage DECLINE cases that real source cannot be made to produce on demand — a
// slot with no field node, a target declared twice, a positional index past the
// end of its struct — and each needs the index in an exact state to drive one
// counter. The records still go in through the PRODUCTION path,
// indexDeclaration, so nothing here can drift from what a collect builds.
//
// production_path_end_to_end IS THE ONE THAT PROVES THE FEATURE, and it exists
// because a hand-built index cannot: it chunks real C through the real populate
// pass and asserts the edges come out the other side. Two properties of C's
// query set used to make that impossible — an unnamed `(declaration) @decl` row
// whose chunks indexDeclaration dropped, and a struct row matching every
// `struct X` MENTION so the owner lookup saw two candidates and declined. Both
// are fixed at the capture: resolveDeclNameC names the variable, and the struct
// row now requires a body. Without that subtest, eight green ones would still
// have described an emitter that emitted nothing.

const (
	slotFile   = "src/ops.c"
	slotStruct = slotFile + ":http_ops"
	slotFlush  = slotFile + ":http_ops.flush"
	slotClose  = slotFile + ":http_ops.close"
	slotReal   = slotFile + ":real_flush"
	slotRealC  = slotFile + ":real_close"
)

// slotBindIndex builds the shared fixture index every subtest reads.
func slotBindIndex(t *testing.T, binds ...treesitter.SlotBind) *declIndex {
	t.Helper()
	ix := newDeclIndex(16)
	decls := []gateDecl{
		// The dispatch table and its two function-pointer field nodes. Only a
		// function-pointer field becomes a node, which is what makes the node's
		// EXISTENCE the slot's function-pointer-ness test.
		{nodeID: slotStruct, name: "http_ops", facts: &treesitter.TypeFacts{
			FieldOrder: []string{"flush", "close", "version"},
		}},
		{nodeID: slotFlush, name: "flush", parent: "http_ops", facts: &treesitter.TypeFacts{}},
		{nodeID: slotClose, name: "close", parent: "http_ops", facts: &treesitter.TypeFacts{}},

		// The implementations.
		{nodeID: slotReal, name: "real_flush", facts: &treesitter.TypeFacts{}},
		{nodeID: slotRealC, name: "real_close", facts: &treesitter.TypeFacts{}},

		// TWO declarations of one name, so the ambiguous-target gate has a real
		// case to decline rather than an imagined one.
		{nodeID: slotFile + ":dup_a", name: "dup", facts: &treesitter.TypeFacts{}},
		{nodeID: slotFile + ":dup_b", name: "dup", facts: &treesitter.TypeFacts{}},

		// The filled vtable itself, carrying whatever binds the subtest passes.
		{nodeID: slotFile + ":table", name: "table", facts: &treesitter.TypeFacts{SlotBinds: binds}},
	}
	gateIndexOf(ix, slotFile, treesitter.LangC, decls)
	ix.resolveSigKeys()
	return ix
}

// slotBind is a terser constructor for the fixture binds.
func slotBind(field, target string, index int) treesitter.SlotBind {
	return treesitter.SlotBind{Type: "http_ops", Field: field, Target: target, Index: index}
}

// TestCSlotBindEmission covers the emitter's two gates, its seven decline
// counters and the edge direction.
func TestCSlotBindEmission(t *testing.T) {
	t.Run("designated_edge", func(t *testing.T) {
		edges, stats := deriveSlotBindEdges(slotBindIndex(t, slotBind("flush", "real_flush", -1)))
		require.Len(t, edges, 1, "a designated bind naming a real field and a real target emits one edge")
		assert.Equal(t, slotFlush, edges[0].FromId)
		assert.Equal(t, slotReal, edges[0].ToId)
		assert.Equal(t, 1, stats.Edges)
		assert.Equal(t, 1, stats.Binds, "control: the bind was seen, so the edge count is over real input")
	})

	t.Run("positional_edge", func(t *testing.T) {
		// Index 1 resolves through the struct's own FieldOrder to `close`,
		// which is the whole reason that carrier exists.
		edges, _ := deriveSlotBindEdges(slotBindIndex(t, slotBind("", "real_close", 1)))
		require.Len(t, edges, 1, "a positional bind resolves its slot through the declaration's field order")
		assert.Equal(t, slotClose, edges[0].FromId,
			"index 1 is the SECOND field; landing on the first would be the mis-indexing the ordering contract prevents")
		assert.Equal(t, slotRealC, edges[0].ToId)
	})

	t.Run("method_token_carries_the_shape", func(t *testing.T) {
		des, _ := deriveSlotBindEdges(slotBindIndex(t, slotBind("flush", "real_flush", -1)))
		pos, _ := deriveSlotBindEdges(slotBindIndex(t, slotBind("", "real_close", 1)))
		require.Len(t, des, 1)
		require.Len(t, pos, 1)
		assert.Equal(t, kgtypes.EdgeMethodSlotBind+"designated", des[0].Method)
		assert.Equal(t, kgtypes.EdgeMethodSlotBind+"positional", pos[0].Method)
		assert.NotEqual(t, des[0].Method, pos[0].Method,
			"the two shapes must be distinguishable, or the suffix records nothing a reader could use")
	})

	t.Run("unknown_slot_counted", func(t *testing.T) {
		// `version` is a plain data field: it is in FieldOrder but has no field
		// NODE, because only a function-pointer field becomes one.
		edges, stats := deriveSlotBindEdges(slotBindIndex(t, slotBind("version", "real_flush", -1)))
		assert.Empty(t, edges, "a slot with no field node emits nothing")
		assert.Equal(t, 1, stats.UnknownSlot)
		assert.Zero(t, stats.UnresolvedTarget, "the target here is real; only the SLOT is unknown, and the counters must say so separately")
	})

	t.Run("unresolved_target_counted", func(t *testing.T) {
		// A macro is a bare identifier in the parse tree, so it is captured as a
		// spelling and declines HERE.
		edges, stats := deriveSlotBindEdges(slotBindIndex(t, slotBind("flush", "ZERO_NULL", -1)))
		assert.Empty(t, edges)
		assert.Equal(t, 1, stats.UnresolvedTarget)
		assert.Zero(t, stats.UnknownSlot, "the slot here is real; only the TARGET is unresolved")
	})

	t.Run("ambiguous_target_declines", func(t *testing.T) {
		edges, stats := deriveSlotBindEdges(slotBindIndex(t, slotBind("flush", "dup", -1)))
		assert.Empty(t, edges, "with two candidates the target is genuinely unknown, and taking the head is a wrong-target generator")
		assert.Equal(t, 1, stats.AmbiguousTarget)
		assert.Zero(t, stats.UnresolvedTarget, "the name DOES resolve; it resolves to two things, which is a different fact")
	})

	t.Run("declined_initializer_counted", func(t *testing.T) {
		// Index 9 is past the end of a three-field struct, which means the type
		// resolved to the wrong declaration — so the whole initializer is
		// untrustworthy rather than one element of it.
		edges, stats := deriveSlotBindEdges(slotBindIndex(t, slotBind("", "real_flush", 9)))
		assert.Empty(t, edges)
		assert.Equal(t, 1, stats.DeclinedInitializers)
	})

	t.Run("production_path_end_to_end", func(t *testing.T) {
		// THE ONLY SUBTEST THAT PROVES THE FEATURE RATHER THAN THE LOGIC. It
		// runs the real chunker and the real populate pass over a real vtable,
		// so every layer between the source and the edge has to work: the
		// variable is NAMED by the C resolver and therefore indexed, its struct
		// type resolves to ONE owner because the struct row now requires a
		// body, the field node exists because the function-pointer row created
		// it, and the target binds across files through the include binds.
		res := populateFixture(t, []fixtureFile{
			{path: "src/ops.h", src: `struct http_conn;

struct http_ops {
  int (*flush)(struct http_conn *h);
  int (*close)(struct http_conn *h);
  int version;
};
`},
			{path: "src/vtable.c", src: `#include "ops.h"

static int real_flush(struct http_conn *h) { return 0; }
static int real_close(struct http_conn *h) { return 0; }

static struct http_ops designated_ops = {
    .flush = real_flush,
    .close = NULL,
    .version = 2,
};

static struct http_ops positional_ops = {
    &real_flush,
    real_close,
    3,
};
`},
		})

		var got []string
		for _, e := range res.Edges {
			if kgtypes.EdgeType(e.Type) != kgtypes.EdgeImplements {
				continue
			}
			if _, ok := strings.CutPrefix(e.Method, kgtypes.EdgeMethodSlotBind); ok {
				got = append(got, e.FromId+" -> "+e.ToId+" "+e.Method)
			}
		}
		sort.Strings(got)
		assert.Equal(t, []string{
			"src/ops.h:http_ops.close -> src/vtable.c:real_close " + kgtypes.EdgeMethodSlotBind + "positional",
			"src/ops.h:http_ops.flush -> src/vtable.c:real_flush " + kgtypes.EdgeMethodSlotBind + "designated",
			"src/ops.h:http_ops.flush -> src/vtable.c:real_flush " + kgtypes.EdgeMethodSlotBind + "positional",
		}, got,
			"both shapes must emit through production, CROSS-FILE from the header's field node to the .c file's function; the NULL slot and the plain data field bind nothing")
	})

	t.Run("direction_is_field_to_target", func(t *testing.T) {
		// THE CATCHER FOR THE ONE PROPERTY A REVERSED IMPLEMENTATION WOULD
		// SATISFY while every other subtest here still passed: both endpoints
		// exist, both are real node IDs, and only the direction distinguishes a
		// graph that answers traversals correctly from one that answers them
		// backwards.
		edges, _ := deriveSlotBindEdges(slotBindIndex(t, slotBind("flush", "real_flush", -1)))
		require.Len(t, edges, 1)
		assert.Equal(t, slotFlush, edges[0].FromId,
			"FROM is the FIELD: a consumer standing on a bound dispatch's target walks OUT to the implementations")
		assert.NotEqual(t, slotReal, edges[0].FromId, "the edge must not run target to field")
		assert.Equal(t, string(kgtypes.EdgeImplements), edges[0].Type)
	})
}
