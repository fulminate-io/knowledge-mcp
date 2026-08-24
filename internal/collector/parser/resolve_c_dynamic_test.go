// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// TestF2DynamicRungSkippedForC pins the per-language dynamic-rung knob.
//
// THE CPP CONTROL IS WHAT MAKES THE C HALF MEAN ANYTHING. A resolution that
// returned nothing at all — a broken fixture, a query that stopped matching, a
// rung disabled for every language — would satisfy "C emits no dynamic group"
// perfectly. The control asserts a structurally identical cpp fixture STILL
// emits one, so the absence is language dispatch rather than nothing happening.
func TestF2DynamicRungSkippedForC(t *testing.T) {
	// BOTH FIXTURES ARE THE SAME SHAPE: a dispatch through a struct field whose
	// name ALSO names a local function in the same file. That collision is the
	// whole reason C turns the rung off — the local function is the CALLER's
	// sibling and provably not the referent, so an open-set group there asserts
	// a false self-call.
	cRes := populateFixture(t, []fixtureFile{{path: "src/dyn.c", src: `struct http_ops {
  int (*flush)(struct http_conn *h);
};

static int flush(struct http_conn *h) {
  return 0;
}

void drive(struct unknown_t *u) {
  u->flush(0);
}
`}})

	cppRes := populateFixture(t, []fixtureFile{{path: "src/dyn.cpp", src: `class Ops {
 public:
  int flush();
};

int flush() {
  return 0;
}

void drive(Unknown* u) {
  u->flush();
}
`}})

	t.Run("c_emits_no_dynamic_group", func(t *testing.T) {
		assert.Emptyf(t, dynamicCallTargets(cRes, "src/dyn.c:drive"), //nolint:testifylint // an empty slice is the assertion
			"C turns the dynamic rung off, so an unbindable dispatch terminates rather than enumerating every same-named declaration in the file")
	})

	t.Run("cpp_control_still_emits_one", func(t *testing.T) {
		// THE KNOWN-POSITIVE. Without it the subtest above passes over any tree
		// where resolution produced nothing.
		require.NotEmptyf(t, dynamicCallTargets(cppRes, "src/dyn.cpp:drive"),
			"cpp leaves the dynamic rung on, so the identical shape must still emit an open-set group")
	})
}

// dynamicCallTargets returns the targets of every CALLS edge leaving one node
// that belongs to an OPEN dynamic group.
//
// THE GROUP TAG IS THE DISCRIMINATOR rather than the confidence value: a
// single-member group and a bound edge can both carry confidence 1, while only
// a group carries the dynamic Method tag.
func dynamicCallTargets(res PopulateResult, from string) []string {
	var out []string
	for _, e := range res.Edges {
		if kgtypes.EdgeType(e.Type) != kgtypes.EdgeCalls || e.FromId != from {
			continue
		}
		if e.Method == kgtypes.EdgeMethodDynamic {
			out = append(out, e.ToId)
		}
	}
	return out
}
