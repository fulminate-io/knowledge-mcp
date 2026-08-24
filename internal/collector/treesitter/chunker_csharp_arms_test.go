// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"testing"

	"github.com/stretchr/testify/require"
)

const csharpArmsFixture = `class Server : Base, IFoo, N.IBar, IBox<Store> {
    private Store f = Factory.Make();

    void Go(Store q) {
        Store t = null;
        q.DoThing();
    }

    void Hop() {
        this.f.Go();
    }

    void Shadowed(Store p) {
        Other other = null;
        Other p = null;
        other.Use();
    }

    void Predefined(int n, Store ok) {
        ok.Use();
    }

    void Containers(List<string> xs, Store fine) {
        fine.Use();
    }
}

interface IFoo {
    void Go();
}
`

// TestCSharpNominalArms covers both halves of the csharp pair.
func TestCSharpNominalArms(t *testing.T) {
	res := chunkQualFixture(t, "app/Server.cs", csharpArmsFixture)

	t.Run("binds_param_local_field", func(t *testing.T) {
		method := qualTypesFor(t, res, "Server.Go")
		require.Equal(t, "Store", method["q"].Text, "the method binds its own parameter")
		require.Equal(t, "Store", method["t"].Text, "the method binds its own typed local")
		require.NotContains(t, method, "f",
			"a method's map must NOT carry its class's fields; those are reached through the self "+
				"token and the field hop")

		class := qualTypesFor(t, res, ":app.Server")
		require.Equal(t, "Store", class["f"].Text, "the class binds its own field")
		require.NotContains(t, class, "q",
			"and the class's walk stops at each method")
	})

	t.Run("binds_this", func(t *testing.T) {
		method := qualTypesFor(t, res, "Server.Hop")
		require.Equal(t, "Server", method["this"].Text, "the self token binds to the enclosing class's name")

		facts := nominalFactsFor(t, res, "Server")
		require.NotNil(t, facts)
		require.Equal(t, "Store", facts.Fields["f"],
			"the arm records the class's field types, or the hop has nothing to read")
		require.Contains(t, nominalCalleeTexts(res, "Server.Hop"), "this.f.Go",
			"the composed callee keeps both segments, which is the shape the field hop is defined for")
	})

	t.Run("predefined_declines", func(t *testing.T) {
		method := qualTypesFor(t, res, "Server.Predefined")
		require.NotContains(t, method, "n",
			"a predefined type names no in-repo declaration, so binding it would send the rung "+
				"looking up members of a scope that holds nothing")
		require.Equal(t, "Store", method["ok"].Text,
			"control: a user-typed sibling in the SAME declaration still binds")
	})

	t.Run("conflict_dropped", func(t *testing.T) {
		method := qualTypesFor(t, res, "Server.Shadowed")
		require.NotContains(t, method, "p",
			"a name bound twice to different types within one declaration is conflicted and dropped")
		require.Equal(t, "Other", method["other"].Text,
			"control: a sibling name in the same declaration still binds")
	})

	t.Run("declines_containers", func(t *testing.T) {
		method := qualTypesFor(t, res, "Server.Containers")
		require.NotContains(t, method, "xs",
			"a generic instantiation names a container, whose methods are not the element's")
		require.Equal(t, "Store", method["fine"].Text,
			"control: a bindable sibling in the SAME declaration still binds")
	})

	t.Run("conformance_base_list", func(t *testing.T) {
		got := nominalConformTexts(nominalFactsFor(t, res, "Server"))
		require.Equal(t, ConformUndeclared, got["Base"],
			"a base-list entry carries no class-versus-interface marker")
		require.Equal(t, ConformUndeclared, got["IFoo"],
			"and the I-prefix is a NAMING CONVENTION, not a grammar fact: an arm that read it as a "+
				"contract would state something the tree does not carry")
		require.Equal(t, ConformUndeclared, got["N.IBar"],
			"a dotted entry keeps its qualifier, because the declaring file's usings are what bind it")
		require.Equal(t, ConformUndeclared, got["IBox"],
			"a generic supertype keeps its HEAD with the type arguments stripped")
		require.Len(t, got, 4, "exactly the four base-list entries")

		require.False(t, nominalFactsFor(t, res, "Server").IsInterface, "a class is not a contract")
		require.True(t, nominalFactsFor(t, res, "IFoo").IsInterface,
			"control: an interface declaration IS a contract, and that is the fact the emission gate "+
				"reads off the RESOLVED target rather than off the clause")
	})

	t.Run("no_op_declaration_binds_nothing", func(t *testing.T) {
		// A STATIC method has no enclosing instance, so there is no self to
		// bind; with no typed parameter and no typed local either, the arm
		// establishes nothing at all.
		plain := chunkQualFixture(t, "app/Plain.cs",
			"class Plain {\n    static void Run() {\n        Helper();\n    }\n}\n")
		require.Nil(t, qualTypesFor(t, plain, "Plain.Run"),
			"a declaration that binds nothing returns nil, which the reference builder forwards "+
				"verbatim")
	})
}
