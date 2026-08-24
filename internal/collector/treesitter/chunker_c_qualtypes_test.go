// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCQualifierTypes covers the C arm's type-first binding rules and its closed
// type-text allowlist.
//
// EVERY FIXTURE BELOW MAKES A CALL, because qualTypesFor reads the map off a
// reference EDGE rather than off a chunk: a map that never reached an edge's
// Ref would be invisible to the resolution ladder no matter how correctly the
// walk built it.
//
// It is also the catcher for the anonymous-token rule, and C is the language
// where that matters most: `.`, `=` and `&` are all anonymous, and the shared
// class table is built on the first call here — so a keyword or punctuation
// spelling added to cKindNames panics in this test rather than in production.
func TestCQualifierTypes(t *testing.T) {
	t.Run("struct_pointer_param", func(t *testing.T) {
		const src = `void drive(struct http_conn *h) {
  use(h);
}
`
		res := chunkQualFixture(t, "src/drive.c", src)
		types := qualTypesFor(t, res, "drive")
		require.NotEmpty(t, types, "control: the C arm bound at least one qualifier")

		assert.Equal(t, QualType{Text: "http_conn"}, types["h"],
			"a struct pointer records the struct's own name, which is the spelling the struct's declaration chunk also carries")
	})

	t.Run("typedef_param", func(t *testing.T) {
		const src = `void send(Thing t, struct http_ops *ops) {
  use(t, ops);
}
`
		res := chunkQualFixture(t, "src/send.c", src)
		types := qualTypesFor(t, res, "send")
		require.NotEmpty(t, types, "control: the C arm bound at least one qualifier")

		assert.Equal(t, QualType{Text: "Thing"}, types["t"], "a typedef'd parameter binds to the name as written")
		assert.Equal(t, QualType{Text: "http_ops"}, types["ops"])
	})

	t.Run("local_decl", func(t *testing.T) {
		const src = `void run(struct http_conn *h) {
  struct http_ops v;
  Local w;
  static struct http_ops table = {0};
  use(h, v, w, table);
}
`
		res := chunkQualFixture(t, "src/run.c", src)
		types := qualTypesFor(t, res, "run")
		require.NotEmpty(t, types, "control: the C arm bound at least one qualifier")

		assert.Equal(t, QualType{Text: "http_ops"}, types["v"], "a local struct declaration binds to its declared type")
		assert.Equal(t, QualType{Text: "Local"}, types["w"], "a local typedef'd declaration binds the same way")
		// THE STATIC CASE IS THE ONE A POSITIONAL RULE LOSES. `static struct X y`
		// puts a storage_class_specifier in the first named slot, and a
		// first-child rule would read that as the type and decline — which would
		// silently drop exactly the file-scope statics that hold C's dispatch
		// tables.
		assert.Equal(t, QualType{Text: "http_ops"}, types["table"],
			"a storage class specifier is not the type: the type is found by kind, not at index zero")
	})

	t.Run("declines_primitives", func(t *testing.T) {
		const src = `void tally(struct http_conn *h, int n, const char *s) {
  int count = 0;
  double ratio = 0;
  use(h, n, s, count, ratio);
}
`
		res := chunkQualFixture(t, "src/tally.c", src)
		types := qualTypesFor(t, res, "tally")
		// KNOWN-POSITIVE CONTROL: the struct pointer binds in this very
		// declaration, so each absence below is the allowlist declining a shape
		// rather than the arm having bound nothing at all.
		require.Equal(t, QualType{Text: "http_conn"}, types["h"],
			"control: a declared struct type binds, so the absences below are declines rather than a dead arm")

		for _, name := range []string{"n", "s", "count", "ratio"} {
			_, ok := types[name]
			assert.Falsef(t, ok, "%q must not bind: a primitive type declares nothing under its name", name)
		}
	})
}
