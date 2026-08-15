// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pkgDeclFile builds a JVM target file that DECLARES a package, which is what
// makes it demonstrate a source root: the root is the prefix the file's own
// package-shaped tail hangs off, and a fixture with no package clause
// demonstrates nothing.
func pkgDeclFile(path string, lang Language, pkg, decl string) *Result {
	return &Result{
		FilePath: path,
		Language: lang,
		Chunks: []Chunk{{
			Name:    decl,
			Context: ChunkContext{PackageName: NamespaceToken(lang, pkg)},
		}},
	}
}

// TestJVMSourceRootAnchors pins the two rungs a JVM import's candidate path is
// probed along: the importing file's own ancestor prefixes, then the
// repository-level set of roots some OTHER file demonstrates.
//
// WHAT THE ROOTS ARE OBSERVABLE THROUGH, stated plainly because it is not the
// obvious thing. The bind's SCOPE is derived from the specifier alone — a
// package IS a namespace, so the arm stamps ns:<lang>:<pkg> and never a file
// path. The candidate paths exist ONLY to tell java's two readings apart:
// `import a.b.C` binds the type C in package a.b, while
// `import static a.b.C.d` binds the member d parented to type C in package a.b,
// and nothing in the binding itself says which. So a root that is never
// discovered shows up as the STATIC reading collapsing into the plain one —
// a bind naming a package no file declares, which terminates.
func TestJVMSourceRootAnchors(t *testing.T) {
	t.Run("flat_src_root_resolves", func(t *testing.T) {
		// THE GUAVA SHAPE: guava/src/com/google/common/... — the root is a
		// module directory plus src, and every ancestor probe above it misses.
		rc := &RepoContext{}
		byPath := map[string]*Result{
			"guava/src/com/google/common/base/Preconditions.java": pkgDeclFile(
				"guava/src/com/google/common/base/Preconditions.java",
				LangJava, "com.google.common.base", "checkNotNull"),
		}
		self := armFixture("guava/src/com/google/common/collect/Lists.java", LangJava,
			ImportBinding{
				Specifier: "com.google.common.base.Preconditions",
				Imported:  "checkNotNull", Local: "checkNotNull", Kind: ImportNamed,
			})

		got := BindsFor(rc, byPath, self)
		require.Contains(t, got.Binds, "checkNotNull")
		assert.Equal(t, "ns:java:com_google_common_base", got.Binds["checkNotNull"].Scope,
			"the static reading puts the type in com.google.common.base, not in ...base.Preconditions")
		assert.Equal(t, "Preconditions", got.Binds["checkNotNull"].Container,
			"the specifier's last segment is the TYPE the member is parented to")
	})

	t.Run("nested_source_set_root_resolves", func(t *testing.T) {
		// THE OKHTTP SHAPE: a multiplatform source set, four segments deep.
		rc := &RepoContext{}
		byPath := map[string]*Result{
			"okhttp/src/commonJvmAndroid/kotlin/okhttp3/Headers.kt": pkgDeclFile(
				"okhttp/src/commonJvmAndroid/kotlin/okhttp3/Headers.kt",
				LangKotlin, "okhttp3", "headersOf"),
		}
		self := armFixture("okhttp/src/commonJvmAndroid/kotlin/okhttp3/internal/Util.kt", LangKotlin,
			ImportBinding{
				Specifier: "okhttp3.Headers", Imported: "headersOf",
				Local: "headersOf", Kind: ImportNamed,
			})

		got := BindsFor(rc, byPath, self)
		require.Contains(t, got.Binds, "headersOf")
		assert.Equal(t, "ns:kotlin:okhttp3", got.Binds["headersOf"].Scope)
		assert.Equal(t, "Headers", got.Binds["headersOf"].Container)
	})

	t.Run("maven_layout_root_resolves", func(t *testing.T) {
		// THE CATS SHAPE: the maven/sbt core/src/main/scala convention.
		rc := &RepoContext{}
		byPath := map[string]*Result{
			"core/src/main/scala/cats/Functor.scala": pkgDeclFile(
				"core/src/main/scala/cats/Functor.scala", LangScala, "cats", "map"),
		}
		self := armFixture("core/src/main/scala/cats/data/Chain.scala", LangScala,
			ImportBinding{
				Specifier: "cats.Functor", Imported: "map",
				Local: "map", Kind: ImportNamed,
			})

		got := BindsFor(rc, byPath, self)
		require.Contains(t, got.Binds, "map")
		assert.Equal(t, "ns:scala:cats", got.Binds["map"].Scope)
		assert.Equal(t, "Functor", got.Binds["map"].Container)
	})

	t.Run("cross_module_import_resolves_through_the_repo_root_set", func(t *testing.T) {
		// THE ONLY CASE THAT FAILS IF RUNG 2 IS OMITTED. The importer lives
		// under guava-tests/test and the target under guava/src, so NO ancestor
		// of the importing file reaches the target — only a root some OTHER
		// file demonstrates does.
		rc := &RepoContext{}
		byPath := map[string]*Result{
			"guava/src/com/google/common/base/Preconditions.java": pkgDeclFile(
				"guava/src/com/google/common/base/Preconditions.java",
				LangJava, "com.google.common.base", "checkNotNull"),
			"guava-tests/test/com/google/common/base/StringsTest.java": pkgDeclFile(
				"guava-tests/test/com/google/common/base/StringsTest.java",
				LangJava, "com.google.common.base", "StringsTest"),
		}
		self := armFixture("guava-tests/test/com/google/common/base/PreconditionsTest.java", LangJava,
			ImportBinding{
				Specifier: "com.google.common.base.Preconditions",
				Imported:  "checkNotNull", Local: "checkNotNull", Kind: ImportNamed,
			})

		got := BindsFor(rc, byPath, self)
		require.Contains(t, got.Binds, "checkNotNull")
		assert.Equal(t, "ns:java:com_google_common_base", got.Binds["checkNotNull"].Scope)
		assert.Equal(t, "Preconditions", got.Binds["checkNotNull"].Container,
			"rung 1 cannot reach guava/src from guava-tests/test — only the repo-level root set can")
	})

	t.Run("plain_import_under_a_source_root_records_no_container", func(t *testing.T) {
		// THE PAIR TO THE FOUR ROWS ABOVE, and it fails if the arm records a
		// container unconditionally — the defect root discovery could most
		// easily introduce, since a deeper search makes the STATIC candidate
		// reachable in more places.
		rc := &RepoContext{}
		byPath := map[string]*Result{
			"guava/src/com/google/common/base/Preconditions.java": pkgDeclFile(
				"guava/src/com/google/common/base/Preconditions.java",
				LangJava, "com.google.common.base", "Preconditions"),
		}
		self := armFixture("guava/src/com/google/common/collect/Lists.java", LangJava,
			ImportBinding{
				Specifier: "com.google.common.base", Imported: "Preconditions",
				Local: "Preconditions", Kind: ImportNamed,
			})

		got := BindsFor(rc, byPath, self)
		require.Contains(t, got.Binds, "Preconditions")
		assert.Equal(t, "ns:java:com_google_common_base", got.Binds["Preconditions"].Scope)
		assert.Empty(t, got.Binds["Preconditions"].Container,
			"the plain form binds the TYPE itself, and the plain candidate is probed FIRST at every root")
	})

	t.Run("external_package_still_terminates", func(t *testing.T) {
		// THE R2X INPUT. java.util.List resolves at no root, the arm records the
		// bind anyway, and the scope it names is a package no file in the
		// fixture declares — which is what the external-qualifier rung reads to
		// terminate the reference instead of manufacturing an edge to a local
		// declaration named List.
		rc := &RepoContext{}
		byPath := map[string]*Result{
			"guava/src/com/google/common/base/Preconditions.java": pkgDeclFile(
				"guava/src/com/google/common/base/Preconditions.java",
				LangJava, "com.google.common.base", "checkNotNull"),
		}
		self := armFixture("guava/src/com/google/common/collect/Lists.java", LangJava,
			ImportBinding{Specifier: "java.util", Imported: "List", Local: "List", Kind: ImportNamed})

		got := BindsFor(rc, byPath, self)
		require.Contains(t, got.Binds, "List",
			"omitting an out-of-repo bind is what lets a bare List.of() reach a LOCAL List")
		assert.Equal(t, "ns:java:java_util", got.Binds["List"].Scope,
			"neither candidate resolves at any root, so the plain reading stands")
		assert.Empty(t, got.Binds["List"].Container)
	})

	t.Run("roots_are_derived_per_repo_context", func(t *testing.T) {
		// THE CATCHER FOR THE PACKAGE-LEVEL-STATE HAZARD, and it is buildable
		// only because the second corpus is arranged to make a leak OBSERVABLE:
		// it holds the very path the first corpus's root would reach, while
		// demonstrating no root of its own. A cache keyed anywhere but the
		// RepoContext resolves the static reading here and reds this row.
		//
		// The hazard is real rather than theoretical: the ful1347
		// multi-language corpus harness measures seven repositories in ONE
		// process, each with a fresh RepoContext.
		first := &RepoContext{}
		firstPaths := map[string]*Result{
			"guava/src/com/google/common/base/Preconditions.java": pkgDeclFile(
				"guava/src/com/google/common/base/Preconditions.java",
				LangJava, "com.google.common.base", "checkNotNull"),
		}
		firstSelf := armFixture("guava/src/com/google/common/collect/Lists.java", LangJava,
			ImportBinding{
				Specifier: "com.google.common.base.Preconditions",
				Imported:  "checkNotNull", Local: "checkNotNull", Kind: ImportNamed,
			})
		require.Equal(t, "Preconditions", BindsFor(first, firstPaths, firstSelf).Binds["checkNotNull"].Container,
			"control: the first context must derive the root at all, or the second proves nothing")

		second := &RepoContext{}
		secondPaths := map[string]*Result{
			// The same PATH, with NO package clause, so this corpus
			// demonstrates no root whatsoever.
			"guava/src/com/google/common/base/Preconditions.java": declFile(
				"guava/src/com/google/common/base/Preconditions.java", LangJava, "checkNotNull"),
		}
		secondSelf := armFixture("other/Main.java", LangJava,
			ImportBinding{
				Specifier: "com.google.common.base.Preconditions",
				Imported:  "checkNotNull", Local: "checkNotNull", Kind: ImportNamed,
			})

		got := BindsFor(second, secondPaths, secondSelf)
		require.Contains(t, got.Binds, "checkNotNull")
		assert.Empty(t, got.Binds["checkNotNull"].Container,
			"the second repository demonstrates no source root — a container here means the first one's roots leaked")
	})

	t.Run("a_wildcard_import_still_binds_nothing", func(t *testing.T) {
		// THE KNOWN-NEGATIVE CONTROL for every equality above: root discovery
		// changed which candidate path is probed, never which import FORMS
		// record a bind. `import x.y.*` names no single name and must not be
		// expanded into a guess at the package's contents.
		rc := &RepoContext{}
		self := armFixture("guava/src/com/google/common/collect/Lists.java", LangJava,
			ImportBinding{Specifier: "com.google.common.base", Kind: ImportWildcard})

		got := BindsFor(rc, map[string]*Result{}, self)
		assert.Empty(t, got.Binds)
	})
}
