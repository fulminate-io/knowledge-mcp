// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// nominalFactsFor returns the type facts recorded on the chunk whose node
// identity ends in the given suffix.
//
// The chunk is addressed by NAME rather than by index, because a fixture that
// gains a declaration would otherwise silently re-point every assertion in the
// test that reads it.
func nominalFactsFor(t *testing.T, res *Result, name string) *TypeFacts {
	t.Helper()
	for i := range res.Chunks {
		if res.Chunks[i].Name == name {
			return res.Chunks[i].TypeFacts
		}
	}
	t.Fatalf("no chunk named %q in the fixture", name)
	return nil
}

// nominalConformTexts flattens a carrier into text-to-kind pairs, so an
// assertion states BOTH halves of every captured entry. Asserting the texts
// alone would pass against an arm that recorded every entry under the first
// member of the kind vocabulary.
func nominalConformTexts(facts *TypeFacts) map[string]ConformanceKind {
	out := map[string]ConformanceKind{}
	if facts == nil {
		return out
	}
	for _, c := range facts.Conforms {
		out[c.Text] = c.Kind
	}
	return out
}

// nominalCalleeTexts returns the composed callee text of every call edge
// leaving the declaration whose source ends in fromSuffix.
func nominalCalleeTexts(res *Result, fromSuffix string) []string {
	var out []string
	for i := range res.Edges {
		e := &res.Edges[i]
		if e.Type == EdgeCalls && hasSuffix(e.FromID, fromSuffix) {
			out = append(out, e.ToID)
		}
	}
	return out
}

// hasSuffix is strings.HasSuffix under a local name, kept so the assertions
// below read as identity questions rather than string questions.
func hasSuffix(s, suffix string) bool {
	return len(s) >= len(suffix) && s[len(s)-len(suffix):] == suffix
}

const javaArmsFixture = `class Server extends a.b.Base implements Greeter, Logger<Store> {
    private Store f = Factory.make();

    void go(Store q) {
        Store t = null;
        q.doThing();
    }

    void hop() {
        this.f.go();
    }

    void shadowed(Store p) {
        Other p2 = null;
        Other p = null;
        p2.use();
    }

    void containers(java.util.List<String> xs, Store[] arr, Store ok) {
        ok.use();
    }
}

interface Greeter extends Loud, Quiet {
    void greet();
}
`

// TestJavaNominalArms covers both halves of the java pair: what the qualifier
// arm binds, and what the conformance arm captures.
func TestJavaNominalArms(t *testing.T) {
	res := chunkQualFixture(t, "app/Server.java", javaArmsFixture)

	t.Run("binds_param_local_field", func(t *testing.T) {
		// TWO MAPS, NOT ONE, AND THAT IS THE POINT OF THIS SUBTEST. A
		// declaration binds its OWN scope: the class binds its field, the
		// method binds its parameter and its typed local. An implementation
		// that rescanned the enclosing class from each method would also
		// satisfy a method-map assertion, so the class-map leg is what
		// distinguishes the two shapes.
		method := qualTypesFor(t, res, "Server.go")
		require.Equal(t, "Store", method["q"].Text, "the method binds its own parameter")
		require.Equal(t, "Store", method["t"].Text, "the method binds its own typed local")
		require.NotContains(t, method, "f",
			"a method's map must NOT carry its enclosing class's fields; the field is reached "+
				"through the self token and the field hop instead")

		class := qualTypesFor(t, res, ":app.Server")
		require.Equal(t, "Store", class["f"].Text, "the class binds its own field")
		require.NotContains(t, class, "q",
			"and the class's map stops at each method: a method's parameters are the METHOD's scope, "+
				"walked again under that method's own declaration")
	})

	t.Run("binds_this", func(t *testing.T) {
		method := qualTypesFor(t, res, "Server.hop")
		require.Equal(t, "Server", method["this"].Text,
			"the self token binds to the ENCLOSING class's name, which is what a field hop looks the "+
				"owning declaration up by")

		// THE OTHER HALF OF THE HOP, asserted because a "this" key alone proves
		// nothing about reachability: the field hop reads the OWNING
		// declaration's recorded field types, which is the conformance arm's
		// Fields map.
		facts := nominalFactsFor(t, res, "Server")
		require.NotNil(t, facts, "the class declaration carries type facts")
		require.Equal(t, "Store", facts.Fields["f"],
			"the arm must record the class's field types, or the hop has nothing to read")

		// AND THE REFERENCE THE RESOLVER ACTUALLY SEES. `this.f.go()` must reach
		// the ladder as a TWO-SEGMENT qualifier over one callee, because the hop
		// is defined on exactly two segments. The resolved end of this route is
		// asserted in the collector's parser package, which owns the ladder;
		// this package cannot import it, so what is pinned here is that both of
		// the hop's inputs exist and that the composed callee text is the shape
		// the hop is defined for.
		require.Contains(t, nominalCalleeTexts(res, "Server.hop"), "this.f.go",
			"the composed callee must keep both segments, or the field hop is never reached")
	})

	t.Run("conflict_dropped", func(t *testing.T) {
		// A WITHIN-DECLARATION conflict: a local shadowing a parameter with a
		// DIFFERENT type. Fields and locals no longer share a map, so the
		// conflict is exercised where it can actually happen.
		method := qualTypesFor(t, res, "Server.shadowed")
		require.NotContains(t, method, "p",
			"a name bound twice to different types is CONFLICTED and must be dropped, not resolved "+
				"to whichever binding was seen first")
		require.Equal(t, "Other", method["p2"].Text,
			"control: a sibling name in the same declaration still binds, so the drop above is the "+
				"conflict rule rather than the walk failing")
	})

	t.Run("declines_containers", func(t *testing.T) {
		method := qualTypesFor(t, res, "Server.containers")
		require.NotContains(t, method, "xs",
			"a generic instantiation names a CONTAINER: the methods reachable through it are the "+
				"container's, not the element's, so it declines rather than binding either")
		require.NotContains(t, method, "arr",
			"an array type declines for the same reason")
		require.Equal(t, "Store", method["ok"].Text,
			"control: a bindable sibling in the SAME declaration still binds")
	})

	t.Run("conformance_both_clauses", func(t *testing.T) {
		got := nominalConformTexts(nominalFactsFor(t, res, "Server"))
		require.Equal(t, ConformExtends, got["a.b.Base"],
			"the superclass clause is an EXTENDS, and its qualifier is retained because the "+
				"declaring file's imports are what bind it")
		require.Equal(t, ConformImplements, got["Greeter"],
			"an interfaces-clause entry is an IMPLEMENTS")
		require.Equal(t, ConformImplements, got["Logger"],
			"a generic supertype keeps its HEAD with the type arguments stripped")
		require.Len(t, got, 3, "exactly the three declared supertypes, and nothing invented")

		facts := nominalFactsFor(t, res, "Server")
		require.False(t, facts.IsInterface, "a class is not a contract")
	})

	t.Run("conformance_iface_extends", func(t *testing.T) {
		facts := nominalFactsFor(t, res, "Greeter")
		require.NotNil(t, facts, "the interface declaration carries type facts")
		require.True(t, facts.IsInterface,
			"an interface IS a contract, which is what makes a supertype resolving to it emit")

		got := nominalConformTexts(facts)
		require.Equal(t, ConformExtends, got["Loud"],
			"an interface's own extends clause binds no field and hangs its types off a "+
				"differently-kinded child, so an arm keyed on the class side alone captures neither")
		require.Equal(t, ConformExtends, got["Quiet"], "BOTH supertypes are captured, not just the first")
		require.Len(t, got, 2)
	})

	t.Run("conformance_cross_file", func(t *testing.T) {
		// The supertype is declared in one file, imported by another, and named
		// UNQUALIFIED in the implements clause. What this subtest owns at the
		// chunker boundary is that the spelling is carried FORWARD UNRESOLVED —
		// resolution happens against a complete index, which this package does
		// not build.
		sup := chunkQualFixture(t, "svc/Greeter.java",
			"package svc;\n\npublic interface Greeter {\n    void greet();\n}\n")
		require.True(t, nominalFactsFor(t, sup, "Greeter").IsInterface)

		sub := chunkQualFixture(t, "app/Client.java",
			"package app;\n\nimport svc.Greeter;\n\n"+
				"public class Client implements Greeter {\n    public void greet() {}\n}\n")
		got := nominalConformTexts(nominalFactsFor(t, sub, "Client"))
		require.Equal(t, ConformImplements, got["Greeter"],
			"the unqualified spelling is carried as written, for the emitter to bind against the "+
				"DECLARING file's imports once the index is complete")

		// KNOWN-NEGATIVE CONTROL, in the same fixture set: a same-named decoy in
		// a package the subtype does NOT import must not become the captured
		// spelling. The capture stage records a spelling and reads no index, so
		// what it must not do is fabricate a qualifier the source never wrote.
		decoy := chunkQualFixture(t, "other/Greeter.java",
			"package other;\n\npublic interface Greeter {\n    void greet();\n}\n")
		require.True(t, nominalFactsFor(t, decoy, "Greeter").IsInterface,
			"control: the decoy really is a same-named contract, so the discrimination below is "+
				"about scope rather than about kind")
		assert.NotContains(t, got, "other.Greeter",
			"the arm records the spelling AS WRITTEN and must never qualify it toward a package the "+
				"declaring file did not import")
	})

	t.Run("no_op_declaration_binds_nothing", func(t *testing.T) {
		// The bind-only bar: a file whose declarations establish nothing must
		// produce a NIL map, so the reference site is the exact one it carried
		// before this rung existed rather than an equivalent empty one.
		plain := chunkQualFixture(t, "app/Plain.java",
			"class Plain {\n    static void run() {\n        helper();\n    }\n}\n")
		require.Nil(t, qualTypesFor(t, plain, "Plain.run"),
			"a declaration that binds nothing returns nil, which the reference builder forwards "+
				"verbatim")
	})
}
