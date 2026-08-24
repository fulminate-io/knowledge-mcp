// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"testing"

	"github.com/stretchr/testify/require"
)

const scalaArmsFixture = `class Server(f: Store, plain: Other) extends Base with Greeter with a.b.Logger {
  val extra: Store = make()

  def go(p: Store): Unit = {
    val t: Store = make()
    p.doThing()
  }

  def hop(): Unit = {
    this.f.go()
  }

  def shadowed(p: Store): Unit = {
    val other: Other = make()
    val p: Other = make()
    other.use()
  }

  def containers(xs: List[String], ok: Store): Unit = {
    ok.use()
  }
}

trait Greeter {
  def greet(): Unit
}
`

// TestScalaNominalArms covers both halves of the scala pair.
func TestScalaNominalArms(t *testing.T) {
	res := chunkQualFixture(t, "app/Server.scala", scalaArmsFixture)

	t.Run("binds_params_vals", func(t *testing.T) {
		class := qualTypesFor(t, res, ":app.Server")
		require.Equal(t, "Store", class["f"].Text, "a class parameter is a field and binds on the class")
		require.Equal(t, "Other", class["plain"].Text, "every class parameter is visible in the class's own scope")
		require.Equal(t, "Store", class["extra"].Text, "a template-body val binds on the class")
		require.NotContains(t, class, "t",
			"a function's local is the FUNCTION's scope: the class's walk stops at each member")

		fn := qualTypesFor(t, res, "Server.go")
		require.Equal(t, "Store", fn["p"].Text, "the function binds its own parameter")
		require.Equal(t, "Store", fn["t"].Text, "the function binds its own typed val")
		require.NotContains(t, fn, "extra",
			"a function's map must NOT carry its class's members")
	})

	t.Run("binds_this", func(t *testing.T) {
		fn := qualTypesFor(t, res, "Server.hop")
		require.Equal(t, "Server", fn["this"].Text, "the self token binds to the enclosing class's name")

		facts := nominalFactsFor(t, res, "Server")
		require.NotNil(t, facts)
		require.Equal(t, "Store", facts.Fields["f"],
			"a class parameter is recorded as a field, which is what the hop reads")
		require.Equal(t, "Store", facts.Fields["extra"], "so is a template-body val")
		require.Contains(t, nominalCalleeTexts(res, "Server.hop"), "this.f.go",
			"the composed callee keeps both segments, which is the shape the field hop is defined for")
	})

	t.Run("conformance_extends_with", func(t *testing.T) {
		got := nominalConformTexts(nominalFactsFor(t, res, "Server"))
		require.Equal(t, ConformExtends, got["Base"],
			"the type after `extends` is an EXTENDS")
		require.Equal(t, ConformMixin, got["Greeter"],
			"a type after `with` is a MIXIN, and telling the two apart is what the ordered walk buys: "+
				"an arm reading only the NAMED children would see three identical type nodes")
		require.Equal(t, ConformMixin, got["a.b.Logger"],
			"a stable (dotted) mixin keeps its qualifier, because the declaring file's imports bind it")
		require.Len(t, got, 3, "exactly the three declared supertypes")
	})

	t.Run("trait_is_iface", func(t *testing.T) {
		trait := nominalFactsFor(t, res, "Greeter")
		require.NotNil(t, trait, "the trait declaration carries type facts")
		require.True(t, trait.IsInterface, "a trait IS scala's contract")
		require.False(t, nominalFactsFor(t, res, "Server").IsInterface,
			"control: a class in the SAME fixture is not a contract, so the read above is the "+
				"declaration kind rather than a constant true")
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

	t.Run("abstract_members_are_chunked_once", func(t *testing.T) {
		// A trait's abstract member is a DIFFERENT node kind from a concrete
		// method, so both arms of the query set are needed and neither
		// double-counts. Asserted BY KIND EQUALITY per declaration rather than
		// by a total, which the right number of wrong chunks would satisfy.
		byName := map[string]string{}
		for i := range res.Chunks {
			if res.Chunks[i].ParentName == "" {
				continue
			}
			byName[res.Chunks[i].ParentName+"."+res.Chunks[i].Name] = res.Chunks[i].ChunkType
		}
		require.Equal(t, "function_declaration", byName["Greeter.greet"],
			"a trait's abstract member is chunked, and as its own kind")
		require.Equal(t, "function_definition", byName["Server.go"],
			"control: a concrete method still chunks as a definition, so the two arms are disjoint")

		seen := 0
		for i := range res.Chunks {
			if res.Chunks[i].Name == "greet" {
				seen++
			}
		}
		require.Equal(t, 1, seen, "the abstract member is chunked exactly once, not by both arms")
	})

	t.Run("no_op_declaration_binds_nothing", func(t *testing.T) {
		plain := chunkQualFixture(t, "app/Plain.scala",
			"object Plain {\n  def run(): Unit = {\n    helper()\n  }\n}\n")
		require.Equal(t, map[string]QualType{"this": {Text: "Plain"}}, qualTypesFor(t, plain, "Plain.run"),
			"scala has no static members, so a member of an object still has a self to bind — and "+
				"that binding is the WHOLE of what a declaration with no typed parameter or local "+
				"establishes, asserted by equality so an extra binding would fail here")
	})
}
