// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"testing"

	"github.com/stretchr/testify/require"
)

const phpArmsFixture = `<?php
class Server extends \N\Base implements Greeter, Logger {
    use TraitA, TraitB { TraitA::go insteadof TraitB; }

    private Store $f;

    function go(Store $q) {
        $q->doThing();
    }

    function hop() {
        $this->f->go();
    }

    function makes() {
        $made = new Baz();
        $made->run();
    }

    function shadowed(Store $p) {
        $other = new Other();
        $p = new Other();
        $other->use();
    }
}

interface Greeter {
    public function greet();
}

trait TraitA {
    public function go() {}
}
`

// TestPHPNominalArms covers both halves of the php pair.
func TestPHPNominalArms(t *testing.T) {
	res := chunkQualFixture(t, "app/Server.php", phpArmsFixture)

	t.Run("binds_sigil_rule", func(t *testing.T) {
		// THREE ASSERTIONS, AND ALL THREE ARE NEEDED. The first two are the
		// two-spellings rule; the third is what proves the walker binds
		// anything at all on the CLASS visit — every other language's class-half
		// assertion lands on the class's own qualifier map, but php's field
		// assertion lands on a DIFFERENT map produced by a DIFFERENT arm, so
		// without it a walker that bound no property on the class would pass
		// every php subtest here.
		method := qualTypesFor(t, res, "Server.go")
		require.Equal(t, "Store", method["$q"].Text,
			"a qualifier key CARRIES the sigil, because the composed callee text is `$q->doThing` "+
				"and the qualifier handed to the rung is `$q`")

		facts := nominalFactsFor(t, res, "Server")
		require.NotNil(t, facts)
		require.Equal(t, "Store", facts.Fields["f"],
			"a FIELD key carries NO sigil, because `$this->f` accesses member `f`")
		require.NotContains(t, facts.Fields, "$f",
			"the two spellings differ on purpose and neither is normalized into the other")

		class := qualTypesFor(t, res, ":app.Server")
		require.Equal(t, "Store", class["$f"].Text,
			"and the CLASS's own qualifier map carries the property under its SIGILLED spelling, "+
				"which is what a bare `$f->go()` inside the class body hands the rung")
	})

	t.Run("binds_this", func(t *testing.T) {
		method := qualTypesFor(t, res, "Server.hop")
		require.Equal(t, "Server", method["$this"].Text,
			"php's self token is `$this`, sigil included, because that is what the composed callee "+
				"text carries")
		require.Contains(t, nominalCalleeTexts(res, "Server.hop"), "$this->f->go",
			"the composed callee keeps both segments, which is the shape the field hop is defined for")
	})

	t.Run("new_binds_direct", func(t *testing.T) {
		method := qualTypesFor(t, res, "Server.makes")
		bound := method["$made"]
		require.Equal(t, "Baz", bound.Text,
			"php locals carry no declared type, so a construction is the only local-binding shape")
		require.False(t, bound.FromCall,
			"`new Baz()` names the TYPE directly rather than a callee whose result type would need "+
				"a second lookup")
	})

	t.Run("conflict_dropped", func(t *testing.T) {
		method := qualTypesFor(t, res, "Server.shadowed")
		require.NotContains(t, method, "$p",
			"a name bound twice to different types within one declaration is conflicted and dropped")
		require.Equal(t, "Other", method["$other"].Text,
			"control: a sibling name in the same declaration still binds")
	})

	t.Run("conformance_both_clauses", func(t *testing.T) {
		got := nominalConformTexts(nominalFactsFor(t, res, "Server"))
		require.Equal(t, ConformExtends, got[`\N\Base`],
			"a base clause is an EXTENDS, and its leading namespace separator is retained verbatim")
		require.Equal(t, ConformImplements, got["Greeter"],
			"an interface-clause entry is an IMPLEMENTS — php is the one language here whose clause "+
				"kind comes straight off distinct node kinds")
		require.Equal(t, ConformImplements, got["Logger"], "both interface entries are captured")
	})

	t.Run("conformance_trait_use", func(t *testing.T) {
		got := nominalConformTexts(nominalFactsFor(t, res, "Server"))
		require.Equal(t, ConformTrait, got["TraitA"], "an in-body use pulls a trait in")
		require.Equal(t, ConformTrait, got["TraitB"], "and both named traits are captured")
		require.NotContains(t, got, "go",
			"the conflict-resolution braces hold ADAPTATIONS, not supertypes: a walk that descended "+
				"into them would record the METHOD NAME as a declared supertype spelling")
		require.Len(t, got, 5,
			"exactly the extends entry, the two interfaces and the two traits")
	})

	t.Run("trait_and_iface", func(t *testing.T) {
		require.True(t, nominalFactsFor(t, res, "Greeter").IsInterface,
			"an interface is a contract")
		require.True(t, nominalFactsFor(t, res, "TraitA").IsInterface,
			"a TRAIT is a contract too: it supplies members to the classes that use it, so a call "+
				"landing on a trait member wants those classes one hop away — and because the "+
				"emission gate reads the RESOLVED target, a trait leaving this false would capture "+
				"every use entry and emit nothing for any of them")
		require.False(t, nominalFactsFor(t, res, "Server").IsInterface,
			"control: a class in the SAME fixture is not a contract")
	})

	t.Run("no_op_declaration_binds_nothing", func(t *testing.T) {
		plain := chunkQualFixture(t, "app/plain.php",
			"<?php\nfunction run() {\n    helper();\n}\n")
		require.Nil(t, qualTypesFor(t, plain, "run"),
			"a top-level function with no typed parameter and no construction binds nothing, and "+
				"nil is what the reference builder forwards verbatim")
	})
}
