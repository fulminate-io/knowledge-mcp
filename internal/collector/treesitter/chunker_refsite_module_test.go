// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestF0SwiftModuleScope pins the swift module derivation against the real
// artifacts it meets, not against invented inputs.
//
// THE WIDENING IS WHAT NEEDS PROVING. Assigning a NARROWER-than-real unit can
// only widen the residue and can never mis-bind, which is why swift was file
// scoped before; moving off that is the first time swift can mis-bind at all.
// Two of the subtests below are the guards on exactly that: the full-path key
// keeps two same-named targets apart, and any tree outside the convention keeps
// today's narrow answer.
func TestF0SwiftModuleScope(t *testing.T) {
	t.Run("same_module_shares_scope", func(t *testing.T) {
		// THE KNOWN-POSITIVE. Without it every other subtest here would be
		// satisfied by a derivation that returned "" for everything.
		a := ScopeID("Sources/Greeting/Greeter.swift", LangSwift, "")
		b := ScopeID("Sources/Greeting/Server.swift", LangSwift, "")
		require.Equal(t, "mod:Sources/Greeting", a)
		require.Equal(t, a, b,
			"two files of one module must share a resolution unit, or nothing cross-file can resolve")
	})

	t.Run("distinct_packages_stay_distinct", func(t *testing.T) {
		// The full-path key is what prevents two packages that merely share a
		// target name from merging into one scope.
		a := ScopeID("pkgA/Sources/Utils/U.swift", LangSwift, "")
		b := ScopeID("pkgB/Sources/Utils/U.swift", LangSwift, "")
		require.Equal(t, "mod:pkgA/Sources/Utils", a)
		require.Equal(t, "mod:pkgB/Sources/Utils", b)
		require.NotEqual(t, a, b,
			"two packages sharing a target name are two modules: merging them would mis-bind across package boundaries")
	})

	t.Run("no_sources_segment_falls_back", func(t *testing.T) {
		// Every tree outside the convention keeps the narrow, safe unit.
		require.Equal(t, "file:m/a.swift", ScopeID("m/a.swift", LangSwift, ""))
		require.Equal(t, "file:Sources/Top.swift", ScopeID("Sources/Top.swift", LangSwift, ""),
			"a file directly under Sources names no module directory, so it falls back")
	})

	t.Run("empty_path_preserves_terminator", func(t *testing.T) {
		// THE NON-OBVIOUS COUPLING. The swift binds arm calls ScopeID with an
		// EMPTY path to build its deliberately terminating scope, so a
		// derivation that returned anything at all for an empty path would
		// silently change that arm's recorded bind.
		require.Equal(t, "file:", ScopeID("", LangSwift, ""),
			"an empty path carries no module directory, so the fallback must reproduce the terminator byte-for-byte")
	})

	t.Run("tests_dir_is_its_own_module", func(t *testing.T) {
		lib := ScopeID("Sources/Greeting/Greeter.swift", LangSwift, "")
		tst := ScopeID("Tests/GreetingTests/GreeterTests.swift", LangSwift, "")
		require.Equal(t, "mod:Tests/GreetingTests", tst)
		require.NotEqual(t, lib, tst,
			"a test target is a separate module: its declarations are not visible to the library target")
	})
}
