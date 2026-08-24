// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cSlotFactsFor chunks one fixture and returns the type facts of the chunk
// whose source contains a marker.
//
// THE CHUNK IS FOUND BY CONTENT, NOT BY NAME, and that is a fact about C's own
// query set rather than a convenience here: queries_c.go captures a variable
// with a bare `(declaration) @decl` row binding no @name at all, so an
// initialized dispatch table chunks UNNAMED. That costs the slot-bind path
// nothing — the edges it feeds run from the FIELD's node to the target
// function, and the initialized variable's own identity is never an endpoint.
func cSlotFactsFor(t *testing.T, path, src, marker string) *TypeFacts {
	t.Helper()
	c := NewChunker()
	t.Cleanup(c.Close)
	res, err := c.ChunkFile(context.Background(), path, []byte(src))
	require.NoError(t, err)
	require.NotEmpty(t, res.Chunks, "fixture control: the file produced chunks at all")
	for i := range res.Chunks {
		if strings.Contains(res.Chunks[i].Content, marker) && res.Chunks[i].TypeFacts != nil {
			return res.Chunks[i].TypeFacts
		}
	}
	// Fall back to the marker match alone so a nil-facts outcome is reported as
	// nil facts rather than as a missing fixture.
	for i := range res.Chunks {
		if strings.Contains(res.Chunks[i].Content, marker) {
			return res.Chunks[i].TypeFacts
		}
	}
	t.Fatalf("the fixture produced no chunk containing %q", marker)
	return nil
}

// TestCSlotBindCapture covers the composite-literal slot binds that are C's
// IMPLEMENTS analog, one subtest per shape verdict so no verdict can drift into
// prose.
//
// It is also the anonymous-token catcher for the C class table, which this arm
// shares with the qualifier arm: the table is built on first use, so tabling
// `&`, `=` or `.` panics inside this test.
func TestCSlotBindCapture(t *testing.T) {
	t.Run("designated", func(t *testing.T) {
		const src = `struct http_ops {
  int (*flush)(struct http_conn *h);
  int version;
};

static struct http_ops ops = {
    .flush = real_flush,
    .version = 2,
};
`
		facts := cSlotFactsFor(t, "src/designated.c", src, ".flush = real_flush")
		require.NotNil(t, facts, "the C arm must record type facts for an initialized declaration")
		assert.Equal(t, []SlotBind{{Type: "http_ops", Field: "flush", Target: "real_flush", Index: -1}}, facts.SlotBinds,
			"a designated pair records its field name and Index -1; a pair whose value names no function binds nothing")
	})

	t.Run("address_of", func(t *testing.T) {
		const src = `struct http_ops {
  int (*flush)(struct http_conn *h);
};

static struct http_ops ops = {
    .flush = &real_flush,
};
`
		facts := cSlotFactsFor(t, "src/addrof.c", src, ".flush = &real_flush")
		require.NotNil(t, facts)
		assert.Equal(t, []SlotBind{{Type: "http_ops", Field: "flush", Target: "real_flush", Index: -1}}, facts.SlotBinds,
			"taking the address of a function names the same function, so the leading & is stripped")
	})

	t.Run("positional", func(t *testing.T) {
		const src = `struct http_ops {
  int (*flush)(struct http_conn *h);
  int (*close)(struct http_conn *h);
  int version;
};

static struct http_ops ops = {
    &real_flush,
    real_close,
    3,
};
`
		facts := cSlotFactsFor(t, "src/positional.c", src, "&real_flush,")
		require.NotNil(t, facts)
		assert.Equal(t, []SlotBind{
			{Type: "http_ops", Target: "real_flush", Index: 0},
			{Type: "http_ops", Target: "real_close", Index: 1},
		}, facts.SlotBinds,
			"a positional element records its zero-based position; the literal `3` names no target and binds nothing, but still consumed position 2")
	})

	t.Run("mixed_declines", func(t *testing.T) {
		// C99 lets a designator RESET the position, so a partial reading of a
		// mixed sequence is exactly how a mis-indexed wrong target gets
		// manufactured. The WHOLE initializer declines.
		const src = `struct http_ops {
  int (*flush)(struct http_conn *h);
  int (*close)(struct http_conn *h);
};

static struct http_ops ops = {
    .flush = real_flush,
    real_close,
};
`
		facts := cSlotFactsFor(t, "src/mixed.c", src, "real_close,")
		if facts != nil {
			assert.Empty(t, facts.SlotBinds,
				"a mixed initializer declines WHOLE: a designator resets the position the positional half would be read against")
		}
	})

	t.Run("array_designator_declines", func(t *testing.T) {
		// A subscript designator indexes an ARRAY, not a field, so it names no
		// slot this carrier describes.
		const src = `struct http_ops {
  int (*flush)(struct http_conn *h);
};

static struct http_ops ops = {
    [3] = real_flush,
};
`
		facts := cSlotFactsFor(t, "src/arraydes.c", src, "[3] = real_flush")
		if facts != nil {
			assert.Empty(t, facts.SlotBinds, "an array designator declines the whole initializer")
		}
	})

	t.Run("non_identifier_target_declines", func(t *testing.T) {
		// A literal names no declaration and a DEREFERENCE names a value rather
		// than a function — which is why the address-of discrimination has to be
		// real rather than "any pointer_expression will do".
		//
		// A MACRO IS NOT DECLINED HERE, AND IT CANNOT BE. `ZERO_NULL` is a bare
		// identifier in the parse tree and is structurally indistinguishable
		// from a function name; telling them apart needs the preprocessor,
		// which the chunker does not run. It is captured as a spelling and
		// declines LATER, at emission, where the name resolves to no
		// declaration and the unresolved-target counter records it. Declining
		// it here would need a name heuristic, which is the class of rule this
		// whole ladder exists to avoid.
		const src = `struct http_ops {
  int (*flush)(struct http_conn *h);
  int (*close)(struct http_conn *h);
  int (*reset)(struct http_conn *h);
  int version;
};

static struct http_ops ops = {
    .flush = ZERO_NULL,
    .close = *fnptr,
    .reset = real_reset,
    .version = 2,
};
`
		facts := cSlotFactsFor(t, "src/nonident.c", src, ".reset = real_reset")
		require.NotNil(t, facts)
		// KNOWN-POSITIVE CONTROL IN THE SAME LITERAL: two pairs DO bind, so the
		// two absences are per-target declines rather than the whole
		// initializer having been thrown away.
		assert.Equal(t, []SlotBind{
			{Type: "http_ops", Field: "flush", Target: "ZERO_NULL", Index: -1},
			{Type: "http_ops", Field: "reset", Target: "real_reset", Index: -1},
		}, facts.SlotBinds,
			"a literal and a dereference name no function and decline at capture; a macro is a bare identifier and declines later, at resolution")
	})

	t.Run("field_order_holds_positions", func(t *testing.T) {
		// THE CATCHER FOR THE ORDERING CONTRACT. A FieldOrder built from the
		// Fields MAP's keys would pass every other subtest here and mis-index
		// every real struct: Fields omits a field whose type cannot be bound,
		// and a map carries no order at all.
		const src = `struct mixedbag {
  int (*flush)(struct http_conn *h);
  int counter[8];
  struct { int inner; };
  int (*close)(struct http_conn *h);
};
`
		facts := cSlotFactsFor(t, "src/fieldorder.c", src, "struct mixedbag {")
		require.NotNil(t, facts, "a struct declaration must record its field order")
		require.Len(t, facts.FieldOrder, 4,
			"every member holds a position, including the ones no name is read from")
		assert.Equal(t, "flush", facts.FieldOrder[0])
		assert.Equal(t, "counter", facts.FieldOrder[1])
		assert.Empty(t, facts.FieldOrder[2],
			"an anonymous member records the EMPTY STRING rather than being dropped, so later positions stay true")
		assert.Equal(t, "close", facts.FieldOrder[3],
			"the last field's position is what a dropped entry above it would have shifted")
	})
}
