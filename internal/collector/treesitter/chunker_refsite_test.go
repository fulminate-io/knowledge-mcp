// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestScopeKindsCoversEveryRegisteredLanguage is the catcher for a language
// registered and then forgotten by the scope table.
//
// ScopeFile is the zero value of ScopeKind, so a missing entry does not fail
// anywhere — it silently declares that the language's declarations are visible
// only inside their own file, which for a directory-scoped or
// namespace-scoped language means every cross-file reference resolves to
// nothing. The failure is invisible at the point it is introduced and shows up
// as missing edges much later, which is exactly the shape a closed-allowlist
// gate exists to prevent.
func TestScopeKindsCoversEveryRegisteredLanguage(t *testing.T) {
	langs := RegisteredLanguages()
	require.NotEmpty(t, langs, "control: the registry must be readable")

	for _, lang := range langs {
		_, ok := scopeKinds[lang]
		assert.True(t, ok,
			"language %q is registered but has no scopeKinds entry: it would take the ScopeFile zero value silently", lang)
	}

	// The reverse direction: an entry for a language that is not registered is
	// dead weight and usually a typo in a constant.
	for lang := range scopeKinds {
		_, ok := registry[lang]
		assert.True(t, ok, "scopeKinds names %q, which is not a registered language", lang)
	}
}
