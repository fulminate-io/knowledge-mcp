// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"context"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// hashSuffix matches the astPathHash disambiguator walkTopLevel's second pass
// appends to a colliding declaration's own name. Expectations normalize it to a
// bare "#" so a fixture edit that shifts byte offsets does not rewrite the
// table, while a name that gained or lost its suffix still fails loudly.
var hashSuffix = regexp.MustCompile(`#[0-9a-f]{8}$`)

func normalizeHash(name string) string {
	return hashSuffix.ReplaceAllString(name, "#")
}

// chunkNameParents builds the Name -> ParentName map the fixtures assert, with
// hash suffixes normalized. The map COLLAPSES duplicate names by construction,
// which is why every subtest also asserts the raw chunk count separately.
func chunkNameParents(t *testing.T, path, src string) (map[string]string, int) {
	t.Helper()
	chunker := NewChunker()
	defer chunker.Close()

	result, err := chunker.ChunkFile(context.Background(), path, []byte(src))
	require.NoError(t, err)

	got := map[string]string{}
	for _, c := range result.Chunks {
		got[normalizeHash(c.Name)] = c.ParentName
	}
	return got, len(result.Chunks)
}

// TestContainerFixtures pins the member-to-container resolution for every
// language whose container kinds this change admits.
//
// EVERY EXPECTATION BELOW WAS DERIVED BY RUNNING THE LANGUAGE'S OWN TopLevel
// QUERY OVER THE FIXTURE — never by walking the AST and reasoning about which
// nodes ought to be declarations. The query is a filter: C++'s TopLevel matches
// function_definition, which REQUIRES a body, so a fixture written with
// prototypes chunks none of them. Every function in these fixtures is bodied
// for that reason; restore a body if a later edit trims one.
//
// The chunk COUNTS include chunks the TopLevel query did not match at all.
// collectOrphans emits any uncovered top-level named child as a chunk of its
// raw node kind with an empty Name and ParentName, so a container the query
// declines still appears in the output — Rust's generic impl and C++'s
// anonymous and `a::b` namespaces are all in the counts below for that reason.
func TestContainerFixtures(t *testing.T) {
	t.Run("kotlin", func(t *testing.T) {
		got, count := chunkNameParents(t, "pkg/k.kt", `package demo

class Animal(val name: String) {
    fun speak(): String { return "..." }
    companion object {
        fun create(): Animal { return Animal("x") }
    }
    class Nested {
        fun inner() {}
    }
}

data class Point(val x: Int, val y: Int)

private class Dog : Animal("dog") {
    fun bark() {}
}

interface Nameable {
    fun label(): String
}

object Registry {
    fun register() {}
}

fun topLevel() {}
`)
		// THE TWO LOAD-BEARING ROWS ARE Point AND bark. `data class Point` and
		// `private class Dog` both carry `modifiers` as their first named
		// child, so a container-name rule written as a first-named-child check
		// instead of a scan resolves neither: Point would still be chunked but
		// bark would lose its parent entirely. Do not simplify the scan.
		want := map[string]string{
			"Animal":   "",       // top-level class
			"speak":    "Animal", // ordinary member
			"create":   "Animal", // companion_object is not class-like, so the walk passes through it
			"Nested":   "Animal",
			"inner":    "Nested",
			"Point":    "",
			"Dog":      "",
			"bark":     "Dog", // fails if the scan becomes a first-child check
			"Nameable": "",    // the Kotlin grammar reuses class_declaration for an interface
			"label":    "Nameable",
			"Registry": "",
			"register": "Registry",
			"topLevel": "",
		}
		assert.Equal(t, want, got)
		assert.Equal(t, 13, count)
		assert.Len(t, want, 13, "no Kotlin name repeats, so the map and the count agree")
	})

	t.Run("rust", func(t *testing.T) {
		got, count := chunkNameParents(t, "pkg/r.rs", `mod outer {
    pub mod inner {
        pub struct Thing;

        impl Thing {
            pub fn method(&self) {}
        }

        pub trait Speak {
            fn speak(&self);
        }

        impl Speak for Thing {
            fn speak(&self) {}
        }

        pub trait Quiet {
            fn hush(&self);
        }

        pub fn helper() {}
    }

    pub fn outer_fn() {}
}

pub struct Gen<T> {
    pub t: T,
}

impl<T> Gen<T> {
    pub fn get(&self) -> &T {
        &self.t
    }
}

fn main() {}
`)
		// A trait's `fn hush(&self);` is a function_signature_item, and the
		// TopLevel query matches it, so a trait's method SPECS are chunks and
		// take the trait as their container. `Quiet`/`hush` carries that
		// assertion rather than `Speak`/`speak`, because the two speak chunks
		// share a name — rust requires it — and this table is keyed by name.
		want := map[string]string{
			"outer":  "",
			"inner":  "outer",
			"Thing#": "inner", // struct_item + both impl_items, all three disambiguated
			"method": "Thing", // the impl wins over the enclosing mod
			"Speak":  "inner",
			// TWO CHUNKS SHARE THIS KEY: the trait's spec, parented to Speak,
			// and the impl's method, parented to Thing. The table records the
			// later one in SOURCE ORDER, which is the impl's — and that value
			// is the one worth pinning here, since it is what proves
			// `impl Speak for Thing` parents by the TYPE rather than the trait.
			"speak":    "Thing",
			"Quiet":    "inner",
			"hush":     "Quiet", // a spec takes its trait, which is why the trait is a container
			"helper":   "inner",
			"outer_fn": "outer",
			"Gen":      "",
			// NEGATIVE CONTROL. `impl<T> Gen<T>` binds type: generic_type,
			// which containerName rejects, so get is present-and-unparented.
			// Asserting merely that no "Gen<T>" appears would also pass in a
			// build where the whole fixture failed to chunk.
			"get":  "",
			"main": "",
			// The generic impl the query declined, collected as an orphan.
			"": "",
		}
		assert.Equal(t, want, got)
		// Count and map size differ because "Thing" occurs three times and
		// "speak" twice.
		assert.Equal(t, 17, count)
		assert.Len(t, want, 14)
	})

	t.Run("cpp", func(t *testing.T) {
		got, count := chunkNameParents(t, "pkg/n.cpp", `namespace outer {
namespace inner {

class Widget {
 public:
  int size() const { return 1; }
};

void freeFn() {}

}  // namespace inner
}  // namespace outer

namespace a::b {
struct Deep {
  void m() {}
};
}  // namespace a::b

namespace {
void anonFn() {}
}  // namespace

void global() {}
`)
		want := map[string]string{
			"outer":  "",
			"inner":  "outer", // single-ancestor: "outer" is not chained further
			"Widget": "inner",
			"size":   "Widget", // DISCRIMINATOR: nearest ancestor wins, the class beats the namespace
			"freeFn": "inner",
			"Deep":   "a::b", // DISCRIMINATOR: the C++17 spelling arrives as ONE name node
			"m":      "Deep",
			"anonFn": "", // NEGATIVE CONTROL: an anonymous namespace yields no name
			"global": "",
			// Two orphans: the anonymous namespace, and `namespace a::b`, whose
			// name node is a nested_namespace_specifier the TopLevel pattern's
			// `name: (namespace_identifier)` does not match. containerName reads
			// the name FIELD directly, which is why Deep still resolves to a::b
			// even though the namespace itself is chunked unnamed.
			"": "",
		}
		assert.Equal(t, want, got)
		assert.Equal(t, 11, count)
		assert.Len(t, want, 10)
	})

	t.Run("csharp", func(t *testing.T) {
		// BLOCK FORM ONLY, and that scoping is this subtest's rule rather than
		// a filter it applies: every chunk of this fixture is asserted, with no
		// ChunkType filter. The file-scoped form belongs entirely to the
		// separate test created alongside the change that chunks it.
		got, count := chunkNameParents(t, "pkg/b.cs", `namespace App.Models
{
    public class User
    {
        public string GetName() { return "n"; }
    }

    namespace Nested
    {
        public class Inner
        {
            public void M() {}
        }
    }
}
`)
		want := map[string]string{
			"App.Models": "",
			"User":       "App.Models", // dotted, straight from the grammar's qualified_name
			"GetName":    "User",
			"Nested":     "App.Models",
			"Inner":      "Nested",
			"M":          "Inner",
		}
		assert.Equal(t, want, got)
		assert.Equal(t, 6, count)
		assert.Len(t, want, 6)
	})

	t.Run("php", func(t *testing.T) {
		// EVERY FUNCTION IS BODIED. The prototype spelling of this fixture
		// parses with an error and silently drops `free`: PHP has two
		// function-ish kinds and only one is affected — method_declaration
		// tolerates a missing body while function_definition does not, so the
		// class method chunks normally and the namespace-level function
		// vanishes. That asymmetry is why a rule adopted after one fix must be
		// re-applied to every sibling, including the ones that look fine.
		got, count := chunkNameParents(t, "pkg/pb.php", `<?php
namespace App\Braced {
  class Inner {
    function m() {}
  }
  function free() {}
}
namespace Other {
  class Second {
    function n() {}
  }
}
`)
		want := map[string]string{
			// PHP's TopLevel binds `(namespace_definition) @decl` with no
			// @name; the per-language declNameResolver supplies it from the
			// node's name: field, so each namespace chunk now carries its own
			// name and the two no longer share the empty key.
			`App\Braced`: "",
			"Inner":      `App\Braced`,
			"m":          "Inner",
			"free":       `App\Braced`,
			"Other":      "",
			"Second":     "Other",
			"n":          "Second",
		}
		assert.Equal(t, want, got)
		assert.Equal(t, 7, count)
		assert.Len(t, want, 7)

		// The semicolon form is correct without bodies, because its only
		// function-ish node is a method_declaration. Its namespace node spans
		// only the declaration, so the classes are its SIBLINGS and take no
		// parent from it — the shape that needs a different mechanism entirely.
		semi, semiCount := chunkNameParents(t, "pkg/ps.php", `<?php
namespace App\Models;
class User { function getName(); }
`)
		assert.Equal(t, map[string]string{`App\Models`: "", "User": "", "getName": "User"}, semi)
		assert.Equal(t, 3, semiCount)
	})
}

// containerKeys returns the symbol-map key each named chunk of the given kinds
// would be recorded under — the same `<namespace>.<name>` string
// parser/populate builds from the chunk.
func containerKeys(chunks []Chunk, kinds ...string) map[string]bool {
	kindSet := map[string]bool{}
	for _, k := range kinds {
		kindSet[k] = true
	}
	keys := map[string]bool{}
	for _, c := range chunks {
		if kindSet[c.ChunkType] && c.Name != "" {
			keys[qualifiedName(c.Context.PackageName, c.Name)] = true
		}
	}
	return keys
}

// assertMembersAddressTheirEnclosingContainer is the RE-DERIVED form of the
// container-collision invariant.
//
// It used to be checked on the edge's FromID: the parent-to-member CONTAINS
// edge carried the container's hash-SUFFIXED name, so two reopened blocks of
// one namespace could be told apart by name. The disambiguation now rides
// FromChunk — the container's chunk slot — and the FromID carries the plain
// base name, which the parser's slot pre-pass overwrites from that slot before
// anything resolves. The property being protected is unchanged and the check is
// strictly more exact than the name ever was: a slot identifies ONE chunk,
// while a name identified one only as long as the suffixing scheme held.
//
// WHAT WOULD FAIL HERE: every member piling onto one container (the original
// bug), a member addressing a container that does not enclose it, or a member
// addressing nothing at all.
func assertMembersAddressTheirEnclosingContainer(t *testing.T, result *Result, filePath string) {
	t.Helper()

	edges := 0
	for _, e := range result.Edges {
		if e.Type != EdgeContains || e.FromID == filePath {
			continue
		}
		edges++
		require.NotZero(t, e.FromChunk,
			"parent-to-member edge %q → %q carries no container slot", e.FromID, e.ToID)
		require.NotZero(t, e.ToChunk, "parent-to-member edge %q → %q carries no member slot", e.FromID, e.ToID)

		container := result.Chunks[e.FromChunk-1]
		member := result.Chunks[e.ToChunk-1]
		assert.True(t,
			container.StartByte <= member.StartByte && container.EndByte >= member.EndByte,
			"member %q (bytes %d-%d) is addressed to container %q (bytes %d-%d), which does not enclose it",
			member.Name, member.StartByte, member.EndByte,
			container.Name, container.StartByte, container.EndByte)
		assert.NotEqual(t, e.FromChunk, e.ToChunk, "a member must not contain itself")
	}
	require.NotEmpty(t, edges, "control: the parent-to-member edge is still emitted")
}

func chunkFile(t *testing.T, path, src string) *Result {
	t.Helper()
	chunker := NewChunker()
	defer chunker.Close()
	result, err := chunker.ChunkFile(context.Background(), path, []byte(src))
	require.NoError(t, err)
	return result
}

// TestContainerCollision pins the FIXED behavior when two containers in one
// file share a (ParentName, Name) pair: the disambiguating hash is appended to
// the CONTAINER'S OWN name, a member's ParentName stays unsuffixed so the
// member's node ID never moves, and the parent-to-member CONTAINS edge
// ADDRESSES BY SLOT the block that lexically encloses that member.
//
// READ THIS BEFORE "FIXING" A FAILURE HERE. cpp_single_namespace is the
// no-collision control and carries no suffix at all, so a failure there and
// nowhere else points at the parent-to-member edge in general rather than at
// the collision path. A failure in any of the other three is a REGRESSION in
// the collision fix — the edge has stopped addressing the container that
// encloses the member — rather than a fix landing, which is what these
// subtests described while they still asserted the edge was dropped.
func TestContainerCollision(t *testing.T) {
	t.Run("cpp_reopened_namespace", func(t *testing.T) {
		// Reopening a namespace in one file is routine C++, not an exotic shape.
		const path = "pkg/re.cpp"
		result := chunkFile(t, path, "namespace app { void a() {} } namespace app { void b() {} }\n")
		require.Len(t, result.Chunks, 4)

		var containers, members int
		for _, c := range result.Chunks {
			switch c.ChunkType {
			case "namespace_definition":
				containers++
				assert.Regexp(t, `^app#[0-9a-f]{8}$`, c.Name, "colliding container takes the hash suffix")
			case "function_definition":
				members++
				assert.Equal(t, "app", c.ParentName, "the member's parent stays UNSUFFIXED")
			}
		}
		assert.Equal(t, 2, containers)
		assert.Equal(t, 2, members)

		// The edge is emitted, and it addresses the ONE block that lexically
		// encloses the member.
		assertMembersAddressTheirEnclosingContainer(t, result, path)
	})

	t.Run("cpp_single_namespace", func(t *testing.T) {
		// THE KNOWN-POSITIVE CONTROL. Without it the collision subtests cannot
		// distinguish "the edge drops on collision" from "the edge never exists".
		const path = "pkg/one.cpp"
		result := chunkFile(t, path, "namespace app { void a() {} void b() {} }\n")
		require.Len(t, result.Chunks, 3)

		for _, c := range result.Chunks {
			if c.ChunkType == "namespace_definition" {
				assert.Equal(t, "app", c.Name, "a lone container keeps its plain name")
			}
		}
		assertMembersAddressTheirEnclosingContainer(t, result, path)
	})

	t.Run("rust_struct_and_impl", func(t *testing.T) {
		// A struct beside its impl is the ordinary shape of every Rust type, so
		// this collision is the common case rather than the corner one.
		const path = "pkg/si.rs"
		result := chunkFile(t, path, "pub struct Thing;\nimpl Thing { pub fn method(&self) {} }\n")
		require.Len(t, result.Chunks, 3)

		var containers int
		for _, c := range result.Chunks {
			switch c.ChunkType {
			case "struct_item", "impl_item":
				containers++
				assert.Regexp(t, `^Thing#[0-9a-f]{8}$`, c.Name)
			case "function_item":
				assert.Equal(t, "Thing", c.ParentName, "the member's parent stays UNSUFFIXED")
			}
		}
		assert.Equal(t, 2, containers)

		assertMembersAddressTheirEnclosingContainer(t, result, path)
	})

	t.Run("php_repeated_namespace_block", func(t *testing.T) {
		// Two blocks of the SAME namespace in one file. This became a genuine
		// collision once the per-language declNameResolver gave PHP namespace
		// chunks a name: before that they were both unnamed, so they could not
		// collide and the edge missed for a different reason (an unnamed
		// container is recorded under no key at all).
		const path = "pkg/rp.php"
		result := chunkFile(t, path, "<?php\nnamespace App { class A {} }\nnamespace App { class B {} }\n")
		require.Len(t, result.Chunks, 4)

		var containers int
		for _, c := range result.Chunks {
			switch c.ChunkType {
			case "namespace_definition":
				containers++
				assert.Regexp(t, `^App#[0-9a-f]{8}$`, c.Name, "colliding container takes the hash suffix")
			case "class_declaration":
				assert.Equal(t, "App", c.ParentName, "the member's parent stays UNSUFFIXED")
			}
		}
		assert.Equal(t, 2, containers)

		require.Len(t, containerKeys(result.Chunks, "namespace_definition"), 2,
			"both containers are named, each under its own suffixed name")
		assertMembersAddressTheirEnclosingContainer(t, result, path)
	})
}
