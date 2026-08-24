// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRubyQualifierTypes covers the ruby arm's two routes and records the
// absence of annotation syntax as an asserted fact.
func TestRubyQualifierTypes(t *testing.T) {
	t.Run("self_receiver", func(t *testing.T) {
		const src = `class Server
  def run
    self.log
  end
end
`
		res := chunkQualFixture(t, "app/recv.rb", src)
		got := qualTypesFor(t, res, "Server.run")
		require.NotEmpty(t, got, "control: the declaration bound qualifiers at all")
		assert.Equal(t, QualType{Text: "Server"}, got["self"], "self binds to the enclosing class")
	})

	t.Run("self_receiver_in_module", func(t *testing.T) {
		const src = `module Runner
  def run
    self.log
  end
end
`
		res := chunkQualFixture(t, "app/mod.rb", src)
		got := qualTypesFor(t, res, "Runner.run")
		require.NotEmpty(t, got, "control: the declaration bound qualifiers at all")
		assert.Equal(t, QualType{Text: "Runner"}, got["self"],
			"a module is a container too, so self binds inside one as well")
	})

	t.Run("nested_container_self_does_not_bind_outward", func(t *testing.T) {
		// AT A RUBY CONTAINER'S BODY LEVEL, `self` IS THAT CONTAINER. An ascent
		// that started above the declaration walked straight past Inner and bound
		// its body-level self to Outer — a WRONG TARGET, not a missing one, since
		// every `self.x` written in Inner's body would resolve against Outer.
		const src = `class Outer
  class Inner
    self.setup
  end

  def om
    self.log
  end
end
`
		res := chunkQualFixture(t, "app/nested.rb", src)

		// THE CONTROL IS IN THE SAME RUN: a METHOD's self still ascends to the
		// class that encloses it, so the assertion below is the nesting rule
		// firing rather than the receiver route having been switched off.
		method := qualTypesFor(t, res, "Outer.om")
		require.NotEmpty(t, method, "control: a method still binds its receiver")
		assert.Equal(t, QualType{Text: "Outer"}, method["self"],
			"control: a method's self is the class enclosing it")

		inner := qualTypesFor(t, res, "Outer.Inner")
		assert.Equal(t, QualType{Text: "Inner"}, inner["self"],
			"a nested container's body-level self is that container, never the one outside it")
	})

	t.Run("new_constructor_local", func(t *testing.T) {
		const src = `class Server
  def run
    c = Client.new
    d = Client.build
    c.send
    d.send
  end
end
`
		res := chunkQualFixture(t, "app/new.rb", src)
		got := qualTypesFor(t, res, "Server.run")
		require.NotEmpty(t, got, "control: the declaration bound qualifiers at all")
		assert.Equal(t, QualType{Text: "Client"}, got["c"], "the allocator names the type")

		// ONLY `.new`. A general strip-the-last-segment rule would bind d to
		// Client as well, where the value's type is whatever build returns — a
		// guess dressed as a fact.
		_, boundBuild := got["d"]
		assert.False(t, boundBuild, "a non-allocator class method declines")
	})

	t.Run("no_annotation_syntax_control", func(t *testing.T) {
		// A RECORDED LIMITATION, NOT AN ABSENCE NOBODY CHECKED. A YARD doc comment
		// carries a type, and it is a comment: comment chunks are dropped from the
		// graph entirely, so nothing here can read it. The control beside it is
		// what makes the absence readable.
		const src = `class Server
  # @param [Client] c
  def run(c)
    x = Client.new
    c.send
    x.send
  end
end
`
		res := chunkQualFixture(t, "app/yard.rb", src)
		got := qualTypesFor(t, res, "Server.run")
		require.NotEmpty(t, got, "control: the declaration bound qualifiers at all")
		assert.Equal(t, QualType{Text: "Client"}, got["x"],
			"control: the allocator route works in the same declaration")

		_, boundFromDoc := got["c"]
		assert.False(t, boundFromDoc, "ruby writes no annotations, and a YARD comment is not parsed")
	})
}

// TestRubyConformanceCapture covers the superclass and the three mixin calls,
// the body-level-only rule, and the contract predicate in both directions.
func TestRubyConformanceCapture(t *testing.T) {
	t.Run("superclass", func(t *testing.T) {
		res := chunkQualFixture(t, "app/sup.rb", "class A < Base\nend\n")
		got, ok := conformsOf(t, res, "A")
		require.True(t, ok, "control: the class carries type facts at all")
		assert.Equal(t, []DeclaredSupertype{{Text: "Base", Kind: ConformExtends}}, got)
	})

	t.Run("include_module", func(t *testing.T) {
		res := chunkQualFixture(t, "app/inc.rb", "class A\n  include Runner\n  include Foo::Bar\nend\n")
		got, ok := conformsOf(t, res, "A")
		require.True(t, ok, "control: the class carries type facts at all")
		assert.Equal(t, []DeclaredSupertype{
			{Text: "Runner", Kind: ConformMixin},
			{Text: "Foo::Bar", Kind: ConformMixin},
		}, got, "a qualified spelling keeps its qualifier for the parser to bind")
	})

	t.Run("extend_module", func(t *testing.T) {
		res := chunkQualFixture(t, "app/ext.rb", "class A\n  extend Helper\nend\n")
		got, ok := conformsOf(t, res, "A")
		require.True(t, ok, "control: the class carries type facts at all")
		assert.Equal(t, []DeclaredSupertype{{Text: "Helper", Kind: ConformMixin}}, got)
	})

	t.Run("prepend_module", func(t *testing.T) {
		// THE THIRD NAME IS ASSERTED RATHER THAN ASSUMED. An arm recognizing only
		// include and extend passes a two-name gate while silently dropping every
		// `prepend`, which is why the name is in the closed list AND here.
		res := chunkQualFixture(t, "app/pre.rb", "class A\n  prepend Mix\nend\n")
		got, ok := conformsOf(t, res, "A")
		require.True(t, ok, "control: the class carries type facts at all")
		assert.Equal(t, []DeclaredSupertype{{Text: "Mix", Kind: ConformMixin}}, got)
	})

	t.Run("method_body_include_declines", func(t *testing.T) {
		// An include inside a method body is a runtime call on whatever receiver
		// is in scope, not a declaration — admitting it would manufacture edges
		// out of control flow.
		res := chunkQualFixture(t, "app/inner.rb",
			"class A\n  include Body\n  def handle\n    include Nested\n  end\nend\n")
		got, ok := conformsOf(t, res, "A")
		require.True(t, ok, "control: the class carries type facts at all")
		assert.Equal(t, []DeclaredSupertype{{Text: "Body", Kind: ConformMixin}}, got,
			"the body-level include is captured and the method-body one is not")
		for _, c := range got {
			assert.NotEqual(t, "Nested", c.Text, "a method-body include is not declared conformance")
		}
	})

	t.Run("module_is_a_contract", func(t *testing.T) {
		res := chunkQualFixture(t, "app/contract.rb", "module Runner\n  def run\n  end\nend\n")
		assert.True(t, isContract(t, res, "Runner"),
			"a ruby module cannot be instantiated and exists to be mixed in — it is the contract construct")
	})

	t.Run("class_is_not_a_contract", func(t *testing.T) {
		res := chunkQualFixture(t, "app/both.rb",
			"module Runner\n  def run\n  end\nend\n\nclass A < Base\n  include Runner\nend\n")
		require.True(t, isContract(t, res, "Runner"), "control: the predicate is not simply always false")
		assert.False(t, isContract(t, res, "A"),
			"a class is the concrete thing on the other end of the relationship, not a contract")
	})
}

// TestElixirConformanceCapture covers @behaviour capture under both spellings,
// the declining attributes, and the @callback contract predicate in both
// directions.
func TestElixirConformanceCapture(t *testing.T) {
	t.Run("single_behaviour", func(t *testing.T) {
		res := chunkQualFixture(t, "lib/w.ex",
			"defmodule Worker do\n  @behaviour Runner\n\n  def run(x) do\n    :ok\n  end\nend\n")
		got, ok := conformsOf(t, res, "Worker")
		require.True(t, ok, "control: the module carries type facts at all")
		assert.Equal(t, []DeclaredSupertype{{Text: "Runner", Kind: ConformBehaviour}}, got)
	})

	t.Run("two_behaviours", func(t *testing.T) {
		// BOTH SPELLINGS. Elixir accepts the American form and treats it
		// identically, so an arm matching only the British one would drop every
		// module written the other way.
		res := chunkQualFixture(t, "lib/two.ex",
			"defmodule Worker do\n  @behaviour Runner\n  @behavior Other\nend\n")
		got, ok := conformsOf(t, res, "Worker")
		require.True(t, ok, "control: the module carries type facts at all")
		assert.Equal(t, []DeclaredSupertype{
			{Text: "Runner", Kind: ConformBehaviour},
			{Text: "Other", Kind: ConformBehaviour},
		}, got)
	})

	t.Run("module_attribute_that_is_not_behaviour_declines", func(t *testing.T) {
		// THE DISCRIMINATOR MUST NOT WIDEN TO ANY ATTRIBUTE CARRYING AN ALIAS.
		// @impl takes one, and reading it as conformance would double-count every
		// module that annotates its callback implementations.
		res := chunkQualFixture(t, "lib/attrs.ex",
			"defmodule Worker do\n  @behaviour Runner\n  @impl Runner\n  @moduledoc \"hi\"\n"+
				"  @spec run(x) :: :ok\n  def run(x) do\n    :ok\n  end\nend\n")
		got, ok := conformsOf(t, res, "Worker")
		require.True(t, ok, "control: the module carries type facts at all")
		assert.Equal(t, []DeclaredSupertype{{Text: "Runner", Kind: ConformBehaviour}}, got,
			"exactly one entry: @behaviour is captured once and @impl is not conformance")
	})

	t.Run("callback_module_is_a_contract", func(t *testing.T) {
		res := chunkQualFixture(t, "lib/contract.ex",
			"defmodule Runner do\n  @callback run(x) :: :ok\nend\n")
		assert.True(t, isContract(t, res, "Runner"),
			"@callback is how elixir DEFINES a behaviour, so the module declaring one is the contract")
	})

	t.Run("non_callback_module_is_not_a_contract", func(t *testing.T) {
		res := chunkQualFixture(t, "lib/plain.ex",
			"defmodule Runner do\n  @callback run(x) :: :ok\nend\n\n"+
				"defmodule Worker do\n  @spec run(x) :: :ok\n  @doc \"hi\"\n"+
				"  def run(x) do\n    :ok\n  end\nend\n")
		require.True(t, isContract(t, res, "Runner"), "control: the predicate is not simply always false")
		assert.False(t, isContract(t, res, "Worker"),
			"a defmodule declaring no @callback is an ordinary module, not a behaviour")
	})
}
