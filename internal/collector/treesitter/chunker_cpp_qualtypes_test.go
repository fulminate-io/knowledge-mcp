// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCPPQualifierTypes covers the cpp arm's type-first binding rules and its
// closed type-text allowlist.
//
// EVERY FIXTURE BELOW MAKES A CALL, because qualTypesFor reads the map off a
// reference EDGE rather than off a chunk: a map that never reached an edge's
// Ref would be invisible to the resolution ladder no matter how correctly the
// walk built it.
//
// It is also the catcher for the anonymous-token rule. Building cpp's class
// table happens on the first call here, and newSymbolClasses PANICS on any
// kinds-map name the grammar declares no regular symbol for.
func TestCPPQualifierTypes(t *testing.T) {
	t.Run("binds", func(t *testing.T) {
		const src = `void handle(Config* c, Thing& t, ns::Deep d) {
  Widget w;
  Other* o = mk();
  auto fn = [](Extra* p) { p->run(); };
  c->run();
  t.run();
  d.run();
  w.run();
  o->run();
  fn(o);
}
`
		res := chunkQualFixture(t, "src/handle.cpp", src)
		types := qualTypesFor(t, res, "handle")
		// KNOWN-POSITIVE CONTROL for the whole subtest: an arm that bound
		// nothing would leave every assertion below comparing two zero values.
		require.NotEmpty(t, types, "control: the cpp arm bound at least one qualifier")

		assert.Equal(t, QualType{Text: "Config"}, types["c"],
			"a pointer parameter binds to its type: the star lives on the DECLARATOR, not on the type node")
		assert.Equal(t, QualType{Text: "Thing"}, types["t"], "a reference parameter binds the same way")
		assert.Equal(t, QualType{Text: "ns::Deep"}, types["d"],
			"a qualified spelling keeps its namespace, for the parser to resolve against the declaring file's includes")
		assert.Equal(t, QualType{Text: "Widget"}, types["w"], "a bare local declaration binds to its declared type")
		assert.Equal(t, QualType{Text: "Other"}, types["o"], "an initialized pointer local binds through the init declarator")
		assert.Equal(t, QualType{Text: "Extra"}, types["p"],
			"a lambda's parameters are ordinary parameter_declaration nodes and bind at any depth")
	})

	t.Run("declines", func(t *testing.T) {
		const src = `void run(Config* c, int n, const char* s) {
  auto a = mk();
  int count = 0;
  c->go();
}
`
		res := chunkQualFixture(t, "src/decline.cpp", src)
		types := qualTypesFor(t, res, "run")
		// KNOWN-POSITIVE CONTROL: one parameter binds in this very declaration,
		// so each absence below is the allowlist declining a shape rather than
		// the arm having bound nothing at all.
		require.Equal(t, QualType{Text: "Config"}, types["c"],
			"control: a declared type binds, so the absences below are declines rather than a dead arm")

		for _, name := range []string{"n", "count", "a", "s"} {
			_, ok := types[name]
			assert.Falsef(t, ok,
				"%q must not bind: a primitive type declares nothing under its name, and `auto` is a placeholder this arm does not infer through", name)
		}
	})

	t.Run("template", func(t *testing.T) {
		const src = `void send(Box<Config> b, std::vector<Config> v) {
  b.run();
  v.run();
}
`
		res := chunkQualFixture(t, "src/template.cpp", src)
		types := qualTypesFor(t, res, "send")
		require.NotEmpty(t, types, "control: the cpp arm bound at least one qualifier")

		assert.Equal(t, QualType{Text: "Box"}, types["b"],
			"a template instantiation records its own name with the argument list dropped")
		assert.Equal(t, QualType{Text: "std::vector"}, types["v"],
			"a qualified template is rendered per SEGMENT, so the qualifier is retained and the arguments are still stripped")
	})
}
