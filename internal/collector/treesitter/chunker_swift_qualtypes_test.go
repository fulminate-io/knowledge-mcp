// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSwiftQualifierTypes covers the swift arm's binding rules and its closed
// type-text allowlist.
//
// EVERY FIXTURE BELOW MAKES A CALL, because qualTypesFor reads the map off a
// reference EDGE rather than off a chunk: a map that never reached an edge's
// Ref would be invisible to the resolution ladder no matter how correctly the
// walk built it.
//
// It is also the catcher for the anonymous-token rule. Building swift's class
// table happens on the first call here, and newSymbolClasses PANICS on any
// kinds-map name the grammar declares no regular symbol for — which matters
// most in swift, where `class`, `protocol`, `extension` and `:` are all
// anonymous.
func TestSwiftQualifierTypes(t *testing.T) {
	t.Run("binds", func(t *testing.T) {
		const src = `class Server: Base {
    func handle(p: Req, q: Extra) -> Resp {
        let a: Thing = mk()
        let f = { (c: Config) in c.run() }
        a.run()
        p.run()
        q.run()
        f(p)
        self.other()
        return mk2()
    }
}

extension Server: Other {
    func more(w: Widget) {
        w.run()
        self.handle(p: mk3(), q: mk4())
    }
}
`
		res := chunkQualFixture(t, "Sources/Demo/server.swift", src)
		types := qualTypesFor(t, res, "Server.handle")
		// KNOWN-POSITIVE CONTROL for the whole subtest: an arm that bound
		// nothing would leave every assertion below comparing two zero values.
		require.NotEmpty(t, types, "control: the swift arm bound at least one qualifier")

		assert.Equal(t, QualType{Text: "Server"}, types["self"], "self binds to the enclosing type's own name")
		assert.Equal(t, QualType{Text: "Req"}, types["p"], "a parameter binds to its declared type")
		assert.Equal(t, QualType{Text: "Extra"}, types["q"], "a parameter binds to its declared type")
		assert.Equal(t, QualType{Text: "Thing"}, types["a"], "an annotated local property binds to its declared type")
		assert.Equal(t, QualType{Text: "Config"}, types["c"], "a closure's annotated parameter is local syntax of the same declaration")

		// AN EXTENSION IS THE SAME NODE KIND AS A CLASS, and its members see
		// the same self — which is what makes extension conformance worth
		// chunking at all.
		ext := qualTypesFor(t, res, "more")
		require.NotEmpty(t, ext, "control: the extension's method bound at least one qualifier")
		assert.Equal(t, QualType{Text: "Server"}, ext["self"], "a method declared in an extension sees the extended type as self")
		assert.Equal(t, QualType{Text: "Widget"}, ext["w"])
	})

	t.Run("declines", func(t *testing.T) {
		const src = `class Store {
    func run() {
        let untyped = mk()
        let list: [Config] = []
        let table: [String: Config] = [:]
        let fn: (Config) -> Resp = mk2()
        self.go()
    }
}
`
		res := chunkQualFixture(t, "Sources/Demo/store.swift", src)
		types := qualTypesFor(t, res, "Store.run")
		// KNOWN-POSITIVE CONTROL: self binds in this very declaration, so each
		// absence below is the allowlist declining a shape rather than the arm
		// having bound nothing at all.
		require.Equal(t, QualType{Text: "Store"}, types["self"],
			"control: self binds, so the absences below are declines rather than a dead arm")

		for _, name := range []string{"untyped", "list", "table", "fn"} {
			_, ok := types[name]
			assert.Falsef(t, ok, "%q must not bind: an unannotated property and a container type each decline", name)
		}
	})

	t.Run("optional", func(t *testing.T) {
		const src = `class Dispatch {
    func send(o: Server?, n: Registry.Entry) {
        o?.greet()
        n.run()
    }
}
`
		res := chunkQualFixture(t, "Sources/Demo/dispatch.swift", src)
		types := qualTypesFor(t, res, "Dispatch.send")
		require.NotEmpty(t, types, "control: the swift arm bound at least one qualifier")

		assert.Equal(t, QualType{Text: "Server"}, types["o"],
			"an optional is STRIPPED rather than declined: Server? has Server's members, and a call through it targets Server's declaration")
		assert.Equal(t, QualType{Text: "Registry"}, types["n"],
			"a nested user_type keeps only its leading type_identifier, the same shape the other arms use for a generic instantiation")
	})
}
