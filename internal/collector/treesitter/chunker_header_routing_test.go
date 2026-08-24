// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"context"
	"fmt"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestHeaderRoutingQualifierArms pins which language's arms fire for each of
// the two ways a `.h` file can be routed.
//
// THE ROUTING DECISION HAPPENS BEFORE THE ARM REGISTRY IS CONSULTED, which is
// why this needs its own test: extMap sends every `.h` to C, and the cpp
// grammar is adopted only where the cpp reading is the better one — on an
// errored C parse, or on a clean one whose cpp tree holds a C++-only kind. A
// bug there
// disarms cpp silently for every header in a real codebase, and no fixture
// written against a `.cc` file would notice. BOTH BRANCHES ARE LIVE IN REAL
// SOURCE rather than hypothetical: on the pinned cpp corpus, 103 of 132 files
// end up cpp — 27 of them `.h` — while 29 `.h` files parse clean under C and
// stay there.
func TestHeaderRoutingQualifierArms(t *testing.T) {
	t.Run("c_clean", func(t *testing.T) {
		// Plain C: no class, no namespace, no template — the C grammar parses
		// it without error and the source carries no C++ marker, so the
		// fallback is never even attempted.
		const src = `struct http_ops {
  int size;
};

void drive(struct http_ops *h) {
  use(h);
}
`
		res := chunkQualFixture(t, "include/clean.h", src)
		assert.Equal(t, LangC, res.Language,
			"a header that parses clean as C and holds nothing C++-only stays C")

		// THE QUESTION IS WHICH ARM FIRED, NOT WHETHER ONE DID. C carries arms
		// of its own, so "nothing happened" would be the wrong assertion here
		// and would pass just as well against a registry that never fires for
		// headers at all.
		types := qualTypesFor(t, res, "drive")
		require.NotEmpty(t, types, "the C arm must fire on a C-routed header")
		assert.Equal(t, QualType{Text: "http_ops"}, types["h"],
			"the binding is the C arm's own: a struct pointer records the struct's name")

		// AND THE CPP ARM MUST NOT HAVE. Its type-facts arm records a
		// conformance carrier for a class or struct with a base clause and
		// marks a pure-virtual-bearing declaration a contract; neither may
		// appear on a C-routed file.
		for _, ch := range res.Chunks {
			if ch.TypeFacts == nil {
				continue
			}
			assert.Emptyf(t, ch.TypeFacts.Conforms,
				"%s: a C-routed header must not reach the cpp conformance arm", ch.Name)
			assert.Falsef(t, ch.TypeFacts.IsInterface,
				"%s: a C-routed header must not reach the cpp contract rule", ch.Name)
		}
	})

	t.Run("cpp_fallback", func(t *testing.T) {
		// KNOWN-POSITIVE CONTROL for the subtest above: without it, a wrong
		// arm's output would be indistinguishable from an arm registry that
		// never fires for headers at all.
		const src = `namespace svc {

class Greeter {
 public:
  virtual void greet(Req r) = 0;
};

class Server : public Greeter {
 public:
  void greet(Req r) override;
};

}  // namespace svc
`
		res := chunkQualFixture(t, "include/server.h", src)
		require.Equal(t, LangCPP, res.Language,
			"a header the C grammar errors on is re-parsed under cpp and adopted, and everything downstream follows that reassignment")

		var greeter, server *Chunk
		for i := range res.Chunks {
			switch res.Chunks[i].Name {
			case "Greeter":
				greeter = &res.Chunks[i]
			case "Server":
				server = &res.Chunks[i]
			}
		}
		require.NotNil(t, greeter, "control: the adopted cpp parse produced the class chunks")
		require.NotNil(t, server)

		require.NotNil(t, greeter.TypeFacts, "the cpp type-facts arm must fire on an adopted header")
		assert.True(t, greeter.TypeFacts.IsInterface,
			"a class with a pure-virtual member is a contract")

		require.NotNil(t, server.TypeFacts)
		assert.Equal(t, []DeclaredSupertype{{Text: "Greeter", Kind: ConformUndeclared}}, server.TypeFacts.Conforms,
			"the base-class clause is captured with the access specifier dropped and the kind left undeclared")
		assert.False(t, server.TypeFacts.IsInterface,
			"a class whose only member is a plain override declares no pure virtual and is not a contract")
	})
}

// TestCppHeaderFallbackDiscriminator pins the widened `.h` routing rule in BOTH
// directions.
//
// THE NON-REROUTE HALF IS THE ONE THAT MATTERS. Adopting cpp for a header that
// is really C changes its persisted Language and every node ID under it, and
// there is no error for it to fail on — a C header that flipped would simply be
// chunked by the wrong query set forever. So the pure-C cases are asserted
// beside the positive one rather than assumed from it.
func TestCppHeaderFallbackDiscriminator(t *testing.T) {
	t.Run("cpp_class_in_a_clean_c_header_reroutes", func(t *testing.T) {
		// THIS IS THE CASE THE ERROR-ONLY RULE MISSED. The C grammar parses
		// this without a single error node, so the old rule left it as C and
		// its class was chunked by a query set that has no class row.
		const src = `struct plain { int x; };

class Greeter {
 public:
  virtual void greet() = 0;
};
`
		res := chunkQualFixture(t, "include/mixed.h", src)
		assert.Equal(t, LangCPP, res.Language,
			"a clean-C-parsing header that declares a class is C++, and the class_specifier in its cpp parse says so positively")
	})

	t.Run("pure_c_header_never_reroutes", func(t *testing.T) {
		// THE MANDATORY CONTROL. Every token here is legal C, and the header
		// deliberately contains a `::` and the word `class` INSIDE A COMMENT and
		// a string, which the cheap byte scan cannot tell from real C++ — so
		// this also proves the scan is only a rationing filter and the
		// node-kind check is the authority.
		const src = `/* see docs at http://example.com/c::api — not a class */
struct http_ops {
  int (*flush)(struct http_conn *h);
};

static const char *kDoc = "class Foo { };";

void drive(struct http_ops *h) {
  use(h);
}
`
		res := chunkQualFixture(t, "include/pure.h", src)
		assert.Equal(t, LangC, res.Language,
			"a header whose cpp parse holds no C++-only construct stays C, whatever its comments and string literals say")

		// KNOWN-POSITIVE CONTROL: the file really was chunked, so the language
		// assertion is about routing rather than about an empty result.
		require.NotEmpty(t, res.Chunks, "control: the pure-C header produced chunks")
	})

	t.Run("c_keyword_as_identifier_stays_c", func(t *testing.T) {
		// THE ERRORING-ALTERNATE HALF OF THE RULE, which no other subtest
		// reaches. A C header may legally use a C++ keyword as an identifier,
		// and `template` is one the cpp grammar cannot read that way: the
		// alternate parse ERRORS and is rejected. The identifier is also its own
		// rationing marker, so the second parse is genuinely attempted rather
		// than skipped — a fixture using a keyword only as a SUBSTRING of an
		// ordinary identifier would be declined by the node-kind check instead
		// and would duplicate the subtest above.
		const src = `struct s { int x; };
int template;
`
		res := chunkQualFixture(t, "include/kw.h", src)
		assert.Equal(t, LangC, res.Language, "an alternate parse that errors is never adopted")
	})
}

// TestChunkFile_DotHRoutesByParse pins both directions of the .h fallback: a
// C++ header named .h is adopted by the cpp grammar, and a plain C header is
// left on the C grammar untouched. The control matters as much as the subject —
// asserting only the C++ side would pass on a change that routed every .h to
// cpp.
//
// IT ASSERTS THE WHOLE CHUNK SET, not just the language, and that is the
// coverage nothing else here provides: a change that routes a header correctly
// and then chunks it WRONGLY — a query row that captures too much or too
// little — moves this set while leaving every language assertion green.
func TestChunkFile_DotHRoutesByParse(t *testing.T) {
	const cppHeader = `#pragma once

namespace app {

template <typename T>
class Box {
 public:
  T get() const { return value_; }

 private:
  T value_;
};

}  // namespace app

int helper(int x) { return x + 1; }
`

	const cHeader = `#ifndef C_H
#define C_H

struct point {
  int x;
  int y;
};

int add(int a, int b);

#endif
`

	cases := []struct {
		desc     string
		path     string
		src      string
		wantLang Language
		wantSet  []string
	}{
		{
			desc:     "cpp_header_adopts_cpp_grammar",
			path:     "inc/cpp.h",
			src:      cppHeader,
			wantLang: LangCPP,
			wantSet: []string{
				"(template_declaration)",
				"app(namespace_definition)",
				"Box(class_specifier)",
				"get(function_definition)",
				// The cpp query set captures data members, so the class's
				// private field is a chunk of its own.
				"value_(field_declaration)",
				"helper(function_definition)",
			},
		},
		{
			desc:     "c_header_stays_c_control",
			path:     "inc/c.h",
			src:      cHeader,
			wantLang: LangC,
			wantSet: []string{
				// The prototype `int add(int a, int b);` stays UNNAMED: a
				// prototype shares its name with its definition, and naming both
				// would make every call to that function ambiguous.
				"(declaration)",
				"(preproc_ifdef)",
				"point(struct_specifier)",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			chunker := NewChunker()
			defer chunker.Close()

			res, err := chunker.ChunkFile(context.Background(), tc.path, []byte(tc.src))
			if err != nil {
				t.Fatalf("ChunkFile: %v", err)
			}
			if res.Language != tc.wantLang {
				t.Errorf("Language = %q; want %q", res.Language, tc.wantLang)
			}

			got := make([]string, 0, len(res.Chunks))
			for _, c := range res.Chunks {
				got = append(got, fmt.Sprintf("%s(%s)", c.Name, c.ChunkType))
			}
			sort.Strings(got)
			want := append([]string(nil), tc.wantSet...)
			sort.Strings(want)

			if len(got) != len(want) {
				t.Fatalf("chunk set has %d entries; want %d\n got: %v\nwant: %v", len(got), len(want), got, want)
			}
			for i := range want {
				if got[i] != want[i] {
					t.Fatalf("chunk set mismatch at %d: got %q want %q\n got: %v\nwant: %v", i, got[i], want[i], got, want)
				}
			}
		})
	}
}
