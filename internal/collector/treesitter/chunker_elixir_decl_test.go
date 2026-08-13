// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const elixirDeclSrc = `defmodule MyApp.Worker do
  use GenServer

  def perform(arg) do
    helper(arg)
  end

  defp helper(x), do: x

  def no_args do
    :ok
  end

  def with_guard(x) when is_integer(x) do
    x
  end

  defstruct [:a, :b]

  defmodule Inner do
    def deep, do: 1
  end
end

defprotocol Shape do
  def area(shape)
end

Enum.map([1, 2], fn x -> x end)
`

const elixirPlainCallSrc = `defmodule LoginTest do
  use ExUnit.Case

  test "login succeeds" do
    assert 1 + 1 == 2
  end

  def normalize(list) do
    Enum.map(list, fn x -> x end)
  end
end
`

// TestElixirDecls pins the two halves of the Elixir naming change: definitions
// are named after the entity rather than the macro keyword, and expressions
// that merely look like calls stop being declarations at all.
//
// Elixir has no declaration node kind — a definition is a `call` whose target
// is a macro keyword, and so is every other expression in the language. The
// TopLevel query therefore binds that keyword as @kw behind a definition
// predicate and captures no @name, and the per-language resolver reads the real
// name out of the call's arguments.
func TestElixirDecls(t *testing.T) {
	t.Run("entity_names", func(t *testing.T) {
		result := chunkFile(t, "pkg/w.ex", elixirDeclSrc)

		var names []string
		for _, c := range result.Chunks {
			names = append(names, c.Name)
		}
		require.Equal(t, []string{
			"MyApp.Worker", // defmodule    — the argument is an alias
			"perform",      // def with parens — the argument is a call
			"helper",       // defp with a `, do:` body
			"no_args",      // def with no parens — the argument is a bare identifier
			"with_guard",   // guarded def — the left of the `when` operator
			"",             // defstruct [:a, :b] — defines fields, not a named entity
			"Inner",        // nested defmodule
			"deep",
			"Shape", // defprotocol
			"area",
			"", // Enum.map(...) — a qualified call, collected as an orphan
		}, names)

		// The defect this replaces named every definition after its macro, so
		// every `def` in a file collided with every other. Assert the macro
		// keywords are gone as names in their own right.
		for _, c := range result.Chunks {
			assert.NotContains(t, []string{"def", "defp", "defmodule", "defprotocol", "defstruct"}, c.Name,
				"no chunk may be named after the macro that defines it")
		}
	})

	t.Run("excludes_plain_calls", func(t *testing.T) {
		result := chunkFile(t, "test/login_test.exs", elixirPlainCallSrc)

		var decls, testBlocks []string
		for _, c := range result.Chunks {
			if c.ChunkType == "test_block" {
				testBlocks = append(testBlocks, c.Name)
				continue
			}
			decls = append(decls, c.Name)
		}

		// `use ExUnit.Case`, `assert 1 + 1 == 2` and `Enum.map(...)` are all
		// calls, and all three used to be declarations. Only the two real
		// definitions survive.
		assert.Equal(t, []string{"LoginTest", "normalize"}, decls)

		// KNOWN-POSITIVE CONTROL. The `test "..." do` invocation is still
		// chunked by the parallel TestBlocks pass, which is also why dropping
		// it from TopLevel removes a DUPLICATE rather than a chunk. Without
		// this control a build where Elixir stopped chunking entirely would
		// satisfy the exclusion assertion above.
		assert.Equal(t, []string{"login succeeds"}, testBlocks)
	})
}

// TestDeclNameRegistryWithElixir is the catcher for an Elixir resolver written
// but never registered — the mistake the phase ordering makes easy, since the
// resolver is unreachable until the query change lands and so cannot be caught
// by any Elixir naming assertion on its own.
func TestDeclNameRegistryWithElixir(t *testing.T) {
	for _, lang := range []Language{LangGroovy, LangLua, LangOCaml, LangElm, LangPHP, LangElixir} {
		assert.Contains(t, declNameResolvers, lang, "%s must register a declNameResolver", lang)
	}
	assert.Len(t, declNameResolvers, 6)
}
