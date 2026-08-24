// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// groovyArmsFixture carries the clean shapes AND the hazardous ones side by
// side, so every decline below has a known-positive control in the same file.
const groovyArmsFixture = `class B extends Base {
    Store f
    Other other

    void go(Store q) {
        Store t = null
        def z = other
        q.doThing()
    }

    void hop() {
        this.f.go()
    }
}

interface J extends I {
    void go()
}

interface J2 extends I, K {
}

class Server extends Base implements Greeter {
}

interface Iface {
}

class C {
    Store s
}
`

// TestGroovyNominalArms covers both halves of the groovy pair, at the
// grammar's actual capability.
func TestGroovyNominalArms(t *testing.T) {
	res := chunkQualFixture(t, "app/arms.groovy", groovyArmsFixture)

	t.Run("binds_params_decls", func(t *testing.T) {
		method := qualTypesFor(t, res, "B.go")
		require.Equal(t, "Store", method["q"].Text, "the method binds its own parameter")
		require.Equal(t, "Store", method["t"].Text, "the method binds its own typed local")
		require.NotContains(t, method, "other",
			"`def z = other` declares no type, so the positional rule must not read the NAME as the "+
				"type and bind the initializer's spelling to it")
		require.NotContains(t, method, "z",
			"and the untyped declaration itself binds nothing either")

		class := qualTypesFor(t, res, ":app.B")
		require.Equal(t, "Store", class["f"].Text, "the class binds its own field declaration")
		require.NotContains(t, class, "q", "and the class's walk stops at each method")
	})

	t.Run("binds_this", func(t *testing.T) {
		method := qualTypesFor(t, res, "B.hop")
		require.Equal(t, "B", method["this"].Text,
			"the self token binds to the enclosing class's name, reached through the name field the "+
				"declaration binds")

		facts := nominalFactsFor(t, res, "B")
		require.NotNil(t, facts)
		require.Equal(t, "Store", facts.Fields["f"],
			"the arm records the class's field types, or the hop has nothing to read")
	})

	t.Run("conformance_extends_only", func(t *testing.T) {
		cls := nominalConformTexts(nominalFactsFor(t, res, "B"))
		require.Equal(t, ConformExtends, cls["Base"],
			"a single-supertype extends clause on a class IS captured — it simply emits nothing "+
				"later, because a concrete base class is not a contract")

		iface := nominalConformTexts(nominalFactsFor(t, res, "J"))
		require.Equal(t, ConformExtends, iface["I"],
			"a single-supertype interface extending an interface is the ONE groovy shape that "+
				"produces an edge")
		require.Len(t, iface, 1)

		multi := nominalFactsFor(t, res, "J2")
		require.Empty(t, nominalConformTexts(multi),
			"the MULTI-supertype form carries a direct ERROR child, so the whole declaration's "+
				"conformance capture declines rather than recording whichever supertype the parse "+
				"happened to recover onto")
	})

	t.Run("combined_clause_declines", func(t *testing.T) {
		got := nominalConformTexts(nominalFactsFor(t, res, "Server"))
		require.NotContains(t, got, "Greeter",
			"THE SPECIFIC CATCH: on a combined clause the grammar's `superclass:` field binds the "+
				"IMPLEMENTS target, so a field-reading arm would record Greeter under the extends "+
				"kind — and because Greeter resolves to a contract, that fabricates an edge whose "+
				"label contradicts the source")
		require.Empty(t, got,
			"nothing at all is captured from a declaration carrying a direct ERROR child: a "+
				"recovered parse cannot say which spelling was extends and which was implements")

		require.Equal(t, ConformExtends, nominalConformTexts(nominalFactsFor(t, res, "B"))["Base"],
			"control: the CLEAN single-supertype declaration in the same fixture still captures, so "+
				"the decline above is the ERROR discriminator rather than an arm that captures nothing")
	})

	t.Run("implements_unparseable", func(t *testing.T) {
		// THE PARSE-LEVEL DISCRIMINATOR, ASSERTED IN BOTH DIRECTIONS. This is
		// what the decline rule keys on, and asserting only the declining side
		// would pass against a tree where every declaration looked broken.
		const bareSrc = "class D implements I {\n}\n\nclass A extends Base {\n}\n"
		require.True(t, groovyDeclHasDirectError(t, bareSrc, 0),
			"a bare `implements` clause cannot be represented by this grammar at all and produces a "+
				"direct ERROR child")
		require.False(t, groovyDeclHasDirectError(t, bareSrc, 1),
			"control: a clean single-supertype extends clause in the same file has NO direct ERROR "+
				"child, so the discriminator distinguishes rather than always firing")

		bare := chunkQualFixture(t, "app/bare.groovy", bareSrc)
		require.Empty(t, nominalConformTexts(nominalFactsFor(t, bare, "D")),
			"and the declining declaration captures nothing")
		require.Equal(t, ConformExtends, nominalConformTexts(nominalFactsFor(t, bare, "A"))["Base"],
			"while the clean one still captures")
	})

	t.Run("iface_from_anon_child", func(t *testing.T) {
		require.True(t, nominalFactsFor(t, res, "Iface").IsInterface,
			"`interface I { }` parses as a class_definition carrying an anonymous `interface` child, "+
				"which no symbol-class table can name")
		// THE CONTROL CARRIES A FIELD ON PURPOSE. A declaration that states no
		// type facts at all records NOTHING — nil rather than a zeroed carrier —
		// so a bodyless class would prove only that the arm returned nil, not
		// that it read the keyword and answered false.
		ctl := nominalFactsFor(t, res, "C")
		require.NotNil(t, ctl, "control: the plain class DOES carry type facts, through its field")
		require.False(t, ctl.IsInterface,
			"control: a plain class in the SAME fixture is not a contract")
	})

	t.Run("no_op_declaration_binds_nothing", func(t *testing.T) {
		plain := chunkQualFixture(t, "app/plain.groovy",
			"class Plain {\n    static void run() {\n        helper()\n    }\n}\n")
		require.Nil(t, qualTypesFor(t, plain, "Plain.run"),
			"a static method has no enclosing instance to bind and no typed parameter or local, so "+
				"the arm establishes nothing and returns nil")
	})
}

// groovyDeclHasDirectError reports whether the n-th top-level declaration of a
// groovy source carries a direct ERROR child — the exact predicate the
// conformance decline keys on.
//
// It reads the parse tree itself rather than any arm's output, because the
// claim under test is about the TREE: the decline rule is only honest if the
// discriminator really separates the shapes it says it separates.
func groovyDeclHasDirectError(t *testing.T, src string, index int) bool {
	t.Helper()
	p := NewParser()
	tree, err := p.Parse(context.Background(), []byte(src), LangGroovy)
	require.NoError(t, err)
	t.Cleanup(tree.Close)

	root := tree.RootNode()
	require.Greater(t, int(root.NamedChildCount()), index,
		"the fixture declares at least %d top-level nodes", index+1)
	decl := root.NamedChild(index)
	for j := range int(decl.ChildCount()) {
		if decl.Child(j).IsError() {
			return true
		}
	}
	return false
}

// TestGroovyInterfaceMembersAreNamed pins that an interface's abstract members
// reach the graph WITH NAMES.
//
// A chunk carrying an empty Name leaves its reference edges inert, so a member
// node that exists but is unnamed is reachable by nothing — and the declared
// conformance emitter pairs members BY NAME, so an unnamed member pairs with
// nothing either.
func TestGroovyInterfaceMembersAreNamed(t *testing.T) {
	res := chunkQualFixture(t, "app/named.groovy",
		"interface J {\n    void go()\n}\n\nclass C {\n    void run() { }\n}\n")

	byName := map[string]string{}
	parents := map[string]string{}
	for i := range res.Chunks {
		byName[res.Chunks[i].Name] = res.Chunks[i].ChunkType
		parents[res.Chunks[i].Name] = res.Chunks[i].ParentName
	}

	t.Run("declaration_named", func(t *testing.T) {
		require.Equal(t, "function_declaration", byName["go"],
			"an interface's abstract member is chunked, as its own kind")
		require.Equal(t, "J", parents["go"],
			"and it takes its interface as ParentName, by equality rather than by non-emptiness — a "+
				"resolver returning some other identifier would satisfy a NotEmpty assertion")
	})

	t.Run("definition_named", func(t *testing.T) {
		require.Equal(t, "function_definition", byName["run"],
			"control: a concrete method in the same fixture still resolves its name, so neither the "+
				"query change nor the resolver case broke the landed arm")
		require.Equal(t, "C", parents["run"])
	})
}
