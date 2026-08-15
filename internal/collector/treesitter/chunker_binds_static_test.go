// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestJVMStaticImportRecordsContainer pins the ONE thing Bind.Container exists
// for: `import static a.b.C.d` names a MEMBER of a type, so the bind keys "d"
// while the declaration that satisfies it is parented to "C". Nothing else in
// Bind carries "C", which is why this could not be fixed in the resolution walk
// alone the way the qualified rung's half was.
//
// THE TWO ROWS ARE A PAIR AND NEITHER CATCHES THE OTHER'S ERROR. The first
// fails if the arm records no container at all; the second fails if it records
// one unconditionally — the defect this field could most easily introduce,
// since the arm cannot read a modifier keyword and must infer the reading from
// which candidate path resolved.
func TestJVMStaticImportRecordsContainer(t *testing.T) {
	rc := &RepoContext{}

	// The shared corpus: a/b/C.java EXISTS and a/b/C/d.java does NOT, which is
	// what makes the two candidate paths distinguishable at all.
	byPath := map[string]*Result{"a/b/C.java": declFile("a/b/C.java", LangJava, "d")}

	t.Run("static_import_records_container", func(t *testing.T) {
		self := armFixture("app/Main.java", LangJava,
			ImportBinding{Specifier: "a.b.C", Imported: "d", Local: "d", Kind: ImportNamed})

		got := BindsFor(rc, byPath, self)
		require.Contains(t, got.Binds, "d")
		assert.Equal(t, "ns:java:a_b", got.Binds["d"].Scope,
			"the specifier's last segment is the TYPE, so the PACKAGE is everything before it")
		assert.Equal(t, "C", got.Binds["d"].Container,
			"the specifier's last dotted segment is the container the member is parented to")
	})

	t.Run("plain_import_records_no_container", func(t *testing.T) {
		// `import a.b.C` resolves through the OTHER candidate — the specifier
		// plus the name — and in that reading the bound name IS the type and has
		// no container. Recording one here would send the resolution rung looking
		// for a declaration parented to "b", which nothing is.
		self := armFixture("app/Main.java", LangJava,
			ImportBinding{Specifier: "a.b", Imported: "C", Local: "C", Kind: ImportNamed})

		got := BindsFor(rc, byPath, self)
		require.Contains(t, got.Binds, "C")
		assert.Equal(t, "ns:java:a_b", got.Binds["C"].Scope,
			"both readings put the type in package a.b — only the Container tells them apart")
		assert.Empty(t, got.Binds["C"].Container,
			"the plain form binds the TYPE itself — a container here would be a fiction")
	})
}
