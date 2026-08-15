// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// typeRefFixture is one language's type-reference case, TWO-SIDED by
// construction: a known-positive set that must be present beside a
// known-negative set that must be absent.
//
// NEITHER HALF ALONE CATCHES ITS OPPOSITE ERROR. A query that captures nothing
// passes every negative set, and a query that captures every identifier passes
// every positive set — which is exactly the defect these anchored queries
// remove, and exactly why every existing per-language test asserting only
// "edges are non-empty" could not see it.
type typeRefFixture struct {
	lang    Language
	path    string
	src     string
	present []string
	absent  []string
}

// TestTypeRefPositions covers the languages whose TypeRefs query is anchored to
// grammar type positions.
func TestTypeRefPositions(t *testing.T) {
	for _, f := range typeRefFixtures() {
		t.Run(string(f.lang), func(t *testing.T) {
			chunker := NewChunker()
			defer chunker.Close()

			res, err := chunker.ChunkFile(context.Background(), f.path, []byte(f.src))
			require.NoError(t, err)

			var got []string
			for _, e := range res.Edges {
				if e.Type == EdgeUsesType {
					got = append(got, e.ToID)
				}
			}
			require.NotEmpty(t, got, "fixture produced no type references at all")

			for _, want := range f.present {
				assert.Contains(t, got, want, "a real type reference must be captured")
			}
			for _, no := range f.absent {
				assert.NotContains(t, got, no, "a non-type identifier must not be a type reference")
			}
		})
	}
}

func typeRefFixtures() []typeRefFixture {
	return []typeRefFixture{
		{
			// C# has no type_identifier node kind, so the type POSITIONS are
			// enumerated instead. Every name in absent was a USES_TYPE target
			// under the previous every-identifier capture.
			lang: LangCSharp, path: "a/A.cs",
			src:     "class A : Base { List<Foo> f; Bar g; void M(Baz b) { obj.DoThing(); } }\n",
			present: []string{"Foo", "Bar", "Baz", "List", "Base"},
			absent:  []string{"f", "g", "M", "A", "obj", "DoThing"},
		},
		{
			// The package SEGMENTS are the pre-existing defect the anchoring
			// removes: an unanchored scoped_type_identifier arm is RECURSIVE
			// and emitted java.util, java and util beside the real reference.
			lang: LangJava, path: "a/A.java",
			src: "class A extends Base implements Iface {\n    java.util.List<String> x;\n" +
				"    Bar y;\n    void m(Baz b) { obj.call(); }\n}\n",
			present: []string{"Base", "Iface", "String", "Bar", "Baz", "java.util.List"},
			absent:  []string{"java", "util", "obj", "call", "x", "y", "m"},
		},
		{
			// Rust is UNCHANGED by this ticket and is here as the
			// characterization control: its bare type_identifier capture is
			// already anchored by the grammar's own node kind.
			lang: LangRust, path: "a/w.rs",
			src:     "pub struct Widget;\npub fn build(x: Widget) -> Widget { x }\n",
			present: []string{"Widget"},
			absent:  []string{"build", "x"},
		},
		{
			// The qualified_identifier kind fires in CALL position too, so an
			// unanchored arm would claim the FUNCTION ns::g as a type.
			lang: LangCPP, path: "a/n.cpp",
			src:     "namespace n {\n  void f() { ns::g(3); }\n  ns::T t;\n  ns::U u;\n}\n",
			present: []string{"ns::T", "ns::U"},
			absent:  []string{"ns::g", "g"},
		},
		{
			// Ruby has NO type syntax, so the previous bare constant capture
			// matched every constant — MAX, a numeric constant, was emitted
			// TWICE as a USES_TYPE target. Whole-node scope_resolution keeps
			// the qualifier: Foo::Bar, never a bare Foo.
			lang: LangRuby, path: "a/r.rb",
			src:     "class Base\nend\n\nMAX = 1\n\nclass Foo < Base\n  def go\n    Other.new\n    Foo::Bar\n  end\nend\n",
			present: []string{"Base", "Foo::Bar", "Other"},
			absent:  []string{"MAX"},
		},
	}
}
