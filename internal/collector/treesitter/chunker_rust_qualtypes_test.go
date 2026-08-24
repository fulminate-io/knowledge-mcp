// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRustQualifierTypes covers the rust arm's binding rules, its closed
// type-text allowlist, and the trait-object shapes that make a rust qualifier
// worth binding at all.
//
// EVERY FIXTURE BELOW MAKES A CALL, because qualTypesFor reads the map off a
// reference EDGE rather than off a chunk: a map that never reached an edge's
// Ref would be invisible to the resolution ladder no matter how correctly the
// walk built it.
//
// It is also the catcher for the anonymous-token rule. Building rust's class
// table happens on the first call here, and newSymbolClasses PANICS on any
// kinds-map name the grammar declares no regular symbol for — so a keyword
// spelling added to rustKindNames fails in this test rather than in production.
func TestRustQualifierTypes(t *testing.T) {
	t.Run("binds", func(t *testing.T) {
		const src = `impl Greeter for Server {
    fn greet(&self, p: &Config, q: Extra) -> String {
        let a: Thing = mk();
        let mut m: Other = mk2();
        let n: mod_a::Deep = mk3();
        let f = |c: &Config| { c.run() };
        a.run();
        m.run();
        n.run();
        p.run();
        q.run();
        f(p);
        self.other();
        String::new()
    }
}
`
		res := chunkQualFixture(t, "src/greet.rs", src)
		types := qualTypesFor(t, res, "Server.greet")
		// KNOWN-POSITIVE CONTROL for the whole subtest: an arm that bound
		// nothing would leave every assertion below comparing two zero values.
		require.NotEmpty(t, types, "control: the rust arm bound at least one qualifier")

		assert.Equal(t, QualType{Text: "Server"}, types["self"],
			"the receiver binds to the type the enclosing impl block names")
		assert.Equal(t, QualType{Text: "Config"}, types["p"], "a reference parameter binds through the wrapper")
		assert.Equal(t, QualType{Text: "Extra"}, types["q"], "a bare parameter binds to its declared type")
		assert.Equal(t, QualType{Text: "Thing"}, types["a"], "an annotated let binds to its declared type")
		assert.Equal(t, QualType{Text: "Other"}, types["m"], "a mutable binding's specifier is not its pattern")
		assert.Equal(t, QualType{Text: "mod_a::Deep"}, types["n"],
			"a scoped spelling is carried AS WRITTEN, for the parser to bind against the declaring file's use statements")
		assert.Equal(t, QualType{Text: "Config"}, types["c"], "a closure's annotated parameter is local syntax of the same declaration")
	})

	t.Run("declines", func(t *testing.T) {
		const src = `impl Server2 {
    fn run(&self) {
        let untyped = mk();
        let (x, y): (A, B) = pair();
        let t: (A, B) = pair2();
        let s: [Config; 2] = arr();
        let g = |u| { u.go() };
        self.go();
    }
    fn assoc(n: Config) -> Server2 {
        n.run();
        Server2
    }
}
`
		res := chunkQualFixture(t, "src/decline.rs", src)
		types := qualTypesFor(t, res, "Server2.run")
		// KNOWN-POSITIVE CONTROL: the receiver binds in this very declaration,
		// so each absence below is the allowlist declining a shape rather than
		// the arm having bound nothing at all.
		require.Equal(t, QualType{Text: "Server2"}, types["self"],
			"control: the receiver binds, so the absences below are declines rather than a dead arm")

		for _, name := range []string{"untyped", "x", "y", "t", "s", "u"} {
			_, ok := types[name]
			assert.Falsef(t, ok, "%q must not bind: an unannotated binding, a destructuring pattern and a container type each decline", name)
		}

		// An associated function declares no receiver, so nothing in its body
		// can name one — binding `self` there would record a qualifier the
		// source cannot write.
		assoc := qualTypesFor(t, res, "Server2.assoc")
		require.Equal(t, QualType{Text: "Config"}, assoc["n"],
			"control: the associated function binds its own parameter, so the absence below is the receiver rule")
		_, hasSelf := assoc["self"]
		assert.False(t, hasSelf, "an associated function binds no self")
	})

	t.Run("dyn", func(t *testing.T) {
		const src = `impl Server3 {
    fn dispatch(&self, g: &dyn Greeter, b: Box<dyn Greeter>, m: &mut Config) {
        g.greet();
        b.greet();
        m.set();
    }
}
`
		res := chunkQualFixture(t, "src/dyn.rs", src)
		types := qualTypesFor(t, res, "Server3.dispatch")
		require.NotEmpty(t, types, "control: the rust arm bound at least one qualifier")

		assert.Equal(t, QualType{Text: "Greeter"}, types["g"],
			"a trait object behind a reference binds to the trait, which is the declaration a call through it targets")
		assert.Equal(t, QualType{Text: "Box"}, types["b"],
			"a generic instantiation records its own name with the type arguments dropped")
		assert.Equal(t, QualType{Text: "Config"}, types["m"],
			"a mutable reference binds through both the reference and the mutable specifier")
	})
}
