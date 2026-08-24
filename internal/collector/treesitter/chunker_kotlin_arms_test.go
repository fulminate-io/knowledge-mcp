// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// kotlinArmsFixture carries the three delegation-specifier shapes on one class,
// and writes every abstract member's RETURN TYPE.
//
// THE RETURN TYPE IS A GRAMMAR FACT, NOT A STYLE CHOICE: `fun go()` with no
// return type produces an ERROR node under the vendored grammar, so a fixture
// omitting it would be testing the parser's recovery rather than the arm.
//
// THE CLASS IS DECLARED `private` DELIBERATELY. Kotlin binds no name field on a
// class, so the container ascent reaches the name by scanning direct named
// children — and `modifiers` occupies index 0 on any class carrying a
// visibility modifier, which is exactly the case a first-child scan gets wrong.
const kotlinArmsFixture = `private class Server(val f: Store, plain: Other) : Base(), Logger by impl, Greeter {
    val extra: Store = make()

    fun go(p: Store): Unit {
        val t: Store = make()
        p.doThing()
    }

    fun hop(): Unit {
        this.f.go()
    }

    fun shadowed(p: Store): Unit {
        val other: Other = make()
        val p: Other = make()
        other.use()
    }

    fun containers(xs: List<String>, ok: Store): Unit {
        ok.use()
    }
}

interface Greeter {
    fun greet(): Unit
}
`

// TestKotlinNominalArms covers both halves of the kotlin pair.
func TestKotlinNominalArms(t *testing.T) {
	res := chunkQualFixture(t, "app/Server.kt", kotlinArmsFixture)

	t.Run("binds_params_props", func(t *testing.T) {
		// TWO MAPS: the class binds its constructor parameters and its body
		// properties, the function binds its own parameters and locals.
		class := qualTypesFor(t, res, ":app.Server")
		require.Equal(t, "Store", class["f"].Text, "a val class parameter is a property and binds on the class")
		require.Equal(t, "Other", class["plain"].Text,
			"a constructor parameter with no binding marker is still visible in the class's own scope")
		require.Equal(t, "Store", class["extra"].Text, "a body property binds on the class")
		require.NotContains(t, class, "t",
			"a function's local is the FUNCTION's scope: the class's walk stops at each member")

		fn := qualTypesFor(t, res, "Server.go")
		require.Equal(t, "Store", fn["p"].Text, "the function binds its own parameter")
		require.Equal(t, "Store", fn["t"].Text, "the function binds its own typed local")
		require.NotContains(t, fn, "extra",
			"a function's map must NOT carry its class's properties; those are reached through the "+
				"self token and the field hop")
	})

	t.Run("binds_this", func(t *testing.T) {
		fn := qualTypesFor(t, res, "Server.hop")
		require.Equal(t, "Server", fn["this"].Text,
			"the self token binds to the enclosing class's name even though the class carries a "+
				"visibility modifier at child index 0")

		facts := nominalFactsFor(t, res, "Server")
		require.NotNil(t, facts)
		require.Equal(t, "Store", facts.Fields["f"],
			"the val class parameter is recorded as a field, which is what the hop reads")
		require.NotContains(t, facts.Fields, "plain",
			"a constructor parameter with NO binding marker is not a property, and recording it "+
				"would let a hop reach a name the type does not hold")
		require.Contains(t, nominalCalleeTexts(res, "Server.hop"), "this.f.go",
			"the composed callee keeps both segments, which is the shape the field hop is defined for")
	})

	t.Run("conformance_three_shapes", func(t *testing.T) {
		got := nominalConformTexts(nominalFactsFor(t, res, "Server"))
		require.Equal(t, ConformExtends, got["Base"],
			"a constructor invocation PROVES a class, because only a class can be constructed")
		require.Equal(t, ConformImplements, got["Logger"],
			"an explicit delegation is an implements by a rule of the LANGUAGE — kotlin allows "+
				"delegation to an interface only — rather than by a fact the tree states")
		require.Equal(t, ConformUndeclared, got["Greeter"],
			"a BARE user type cannot be attributed: it is legally an interface, and it is also what "+
				"a class supertype produces when the subclass has no primary constructor")
		require.Len(t, got, 3, "exactly the three declared supertypes")
	})

	t.Run("iface_from_anon_child", func(t *testing.T) {
		iface := nominalFactsFor(t, res, "Greeter")
		require.NotNil(t, iface, "the interface declaration carries type facts")
		require.True(t, iface.IsInterface,
			"there is no interface_declaration kind in this grammar: an interface is a "+
				"class_declaration carrying an anonymous `interface` child, which no symbol-class "+
				"table can name")
		require.False(t, nominalFactsFor(t, res, "Server").IsInterface,
			"control: a plain class in the SAME fixture is NOT a contract, so the read above is the "+
				"keyword rather than a constant true")
	})

	t.Run("conflict_dropped", func(t *testing.T) {
		fn := qualTypesFor(t, res, "Server.shadowed")
		require.NotContains(t, fn, "p",
			"a name bound twice to different types within one declaration is conflicted and dropped")
		require.Equal(t, "Other", fn["other"].Text,
			"control: a sibling name in the same declaration still binds")
	})

	t.Run("declines_containers", func(t *testing.T) {
		fn := qualTypesFor(t, res, "Server.containers")
		require.NotContains(t, fn, "xs",
			"a generic instantiation names a container, whose methods are not the element's")
		require.Equal(t, "Store", fn["ok"].Text,
			"control: a bindable sibling in the SAME declaration still binds")
	})

	t.Run("no_op_declaration_binds_nothing", func(t *testing.T) {
		// A TOP-LEVEL function, which kotlin allows: it has no enclosing
		// class-like container, so there is no self to bind and no typed
		// parameter or local either.
		plain := chunkQualFixture(t, "app/Plain.kt",
			"fun run(): Unit {\n    helper()\n}\n")
		require.Nil(t, qualTypesFor(t, plain, "run"),
			"a declaration that binds nothing returns nil, which the reference builder forwards "+
				"verbatim")
	})
}
