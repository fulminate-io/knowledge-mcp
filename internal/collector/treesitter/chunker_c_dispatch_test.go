// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cDispatchFixture is the vtable shape C uses in place of a supertype
// construct, with a plain data field beside the function pointers as the
// known-negative.
const cDispatchFixture = `struct http_ops {
  int (*flush)(struct http_conn *h);
  int (*close)(struct http_conn *h);
  int flags;
};

struct http_conn {
  struct http_ops *ops;
  int fd;
};
`

// cChunkFor returns the chunk with one name, or nil.
func cChunkFor(res *Result, name string) *Chunk {
	for i := range res.Chunks {
		if res.Chunks[i].Name == name {
			return &res.Chunks[i]
		}
	}
	return nil
}

// TestCFunctionPointerFieldNodes covers the row that gives a C dispatch
// reference something to resolve TO.
//
// WITHOUT THESE NODES THE REST OF C'S DISPATCH CAPTURE IS A WRONG-TARGET
// GENERATOR: the typed-qualifier rung looks a member up under its container's
// name, and with no node at <Struct>.<field> every widened reference falls to
// the open-set rung over the whole file scope instead.
//
// THE PATTERN NESTS THROUGH parenthesized_declarator > pointer_declarator ON
// PURPOSE. That layer is what distinguishes a function POINTER field from a
// C++ header-declared method, which parses as field_declaration over a
// function_declarator WITHOUT it — so the nesting is what keeps this row from
// silently changing cpp node population through the `.h` fallback routing.
func TestCFunctionPointerFieldNodes(t *testing.T) {
	c := NewChunker()
	t.Cleanup(c.Close)
	res, err := c.ChunkFile(context.Background(), "src/ops.c", []byte(cDispatchFixture))
	require.NoError(t, err)
	require.NotEmpty(t, res.Chunks, "fixture control: the file produced chunks at all")

	t.Run("fp_field", func(t *testing.T) {
		ch := cChunkFor(res, "flush")
		require.NotNil(t, ch, "a function-pointer struct field must chunk")
		assert.Equal(t, "field_declaration", ch.ChunkType)
		assert.Equal(t, "http_ops", ch.ParentName,
			"the container ascent gives it the receiver-qualified <Struct>.<field> shape the member lookup keys on")

		// The second pointer proves the row is not matching only the first
		// field of a struct.
		closeField := cChunkFor(res, "close")
		require.NotNil(t, closeField, "every function-pointer field must chunk, not just the first")
		assert.Equal(t, "http_ops", closeField.ParentName)
	})

	t.Run("plain_field_excluded", func(t *testing.T) {
		// THE KNOWN-NEGATIVE, in the SAME struct so it runs against real
		// output. Without it a row that matched every field_declaration would
		// pass the positive half above.
		assert.Nil(t, cChunkFor(res, "flags"),
			"a plain data field carries no function_declarator and must not chunk from this row")
		assert.Nil(t, cChunkFor(res, "fd"),
			"a plain data field carries no function_declarator and must not chunk from this row")

		require.NotNil(t, cChunkFor(res, "flush"),
			"control: the row fired in this same run, so the absences above are its declarator requirement rather than a dead row")
	})
}

// cCalleeTexts returns the callee text of every CALLS edge in one chunked file.
func cCalleeTexts(res *Result) []string {
	var out []string
	for i := range res.Edges {
		if res.Edges[i].Type == EdgeCalls {
			out = append(out, res.Edges[i].ToID)
		}
	}
	return out
}

// TestCDispatchCallCapture pins a verdict for each of C's four dispatch shapes,
// so none of them can drift silently.
//
// THE DECLINE IS AN ASSERTION, NOT A SENTENCE IN A COMMENT. Three shapes are
// captured and one is captured-but-unbindable, and the fourth's verdict is
// pinned by its own subtest rather than left to prose.
func TestCDispatchCallCapture(t *testing.T) {
	const src = `void drive(struct http_conn *c, struct http_ops *ops, struct http_conn *h) {
  h->write(c);
  ops.flush(c);
  (*h->close)();
  c->ops.flush(c);
  c->ops->flush(c);
}
`
	c := NewChunker()
	t.Cleanup(c.Close)
	res, err := c.ChunkFile(context.Background(), "src/dispatch.c", []byte(src))
	require.NoError(t, err)
	callees := cCalleeTexts(res)
	// KNOWN-POSITIVE CONTROL: the widened query captured something at all, so
	// each assertion below is about WHICH text was captured.
	require.NotEmpty(t, callees, "control: the C Calls query captured at least one callee")

	hasCallee := func(suffix string) bool {
		for _, id := range callees {
			if strings.HasSuffix(id, suffix) {
				return true
			}
		}
		return false
	}

	t.Run("arrow", func(t *testing.T) {
		assert.Truef(t, hasCallee("h->write"), "the arrow form must be captured whole, got %v", callees)
	})

	t.Run("value", func(t *testing.T) {
		assert.Truef(t, hasCallee("ops.flush"), "the value form reaches the same field_expression arm, got %v", callees)
	})

	t.Run("deref", func(t *testing.T) {
		// THE INNER field_expression IS BOUND, NOT THE WRAPPER. Capturing
		// `(*h->close)` would hand the resolver leading punctuation that the
		// qualifier split tears into a qualifier naming nothing.
		assert.Truef(t, hasCallee("h->close"), "the explicit-deref form records the inner callee, got %v", callees)
		for _, id := range callees {
			assert.NotContainsf(t, id, "(*", "no callee may carry the parenthesized wrapper's punctuation: %q", id)
		}
	})

	t.Run("nested_declines", func(t *testing.T) {
		// CAPTURED BUT NOT BINDABLE. The qualifier split takes the LAST
		// separator, so `c->ops.flush` yields the qualifier `c->ops`, which
		// still contains a separator — and the field hop requires exactly two
		// segments. The shape is recorded so a reader can see it was considered;
		// the resolution side is what declines it.
		// BOTH SPELLINGS ARE PRESENT because both occur in real source and both
		// reduce to the same unbindable qualifier `c->ops`.
		assert.Truef(t, hasCallee("c->ops.flush"),
			"the nested form is captured — its decline happens at resolution, not at capture, got %v", callees)
		assert.Truef(t, hasCallee("c->ops->flush"),
			"the arrow-arrow nested form is captured on the same arm, got %v", callees)
	})
}
