// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cppVarFixture is one source file exercising every scope the variable rows
// reach and the one scope they must not.
const cppVarFixture = `int g_counter = 0;

namespace ns {
int ns_flag = 1;
}

class Base {
 public:
  int flags;
  int (*cb)(int);
  virtual void run(Arg a) = 0;
};

void f() {
  int local_should_not_chunk = 3;
}

int K::shared = 7;
`

// cppMemberFixture is one source file carrying all four member shapes: a
// header-declared member, an out-of-class definition, a function-pointer data
// member that neither row may take, and a namespace-nested pair.
const cppMemberFixture = `class Base {
 public:
  void declared(Other o);
  int (*cb)(int);
  virtual void pure(Arg a) = 0;
};

void Base::run(Arg a) { }

namespace ns {
class Deep {
 public:
  void m();
};
}

void ns::Deep::m() { }
`

// cppChunkFor returns the chunk with one name, or nil.
func cppChunkFor(res *Result, name string) *Chunk {
	for i := range res.Chunks {
		if res.Chunks[i].Name == name {
			return &res.Chunks[i]
		}
	}
	return nil
}

// TestCppVarCapture covers the three scope-anchored C++ declaration rows.
//
// THE SCOPE DISCRIMINATION IS AN ANCHOR RATHER THAN A PREDICATE. A tree-sitter
// pattern cannot express "not inside a compound_statement", so each row is
// anchored on a CONTAINER that is in scope — a translation unit, a namespace's
// declaration list, a class's field list. A function-body local is excluded
// because no row names compound_statement, which makes the exclusion
// un-bypassable rather than merely intended.
func TestCppVarCapture(t *testing.T) {
	c := NewChunker()
	t.Cleanup(c.Close)
	res, err := c.ChunkFile(context.Background(), "src/vars.cpp", []byte(cppVarFixture))
	require.NoError(t, err)
	require.NotEmpty(t, res.Chunks, "fixture control: the file produced chunks at all")

	t.Run("file_scope", func(t *testing.T) {
		ch := cppChunkFor(res, "g_counter")
		require.NotNil(t, ch, "a file-scope variable must chunk")
		assert.Equal(t, "declaration", ch.ChunkType)
		assert.Empty(t, ch.ParentName, "a file-scope declaration has no container")
	})

	t.Run("namespace_scope", func(t *testing.T) {
		ch := cppChunkFor(res, "ns_flag")
		require.NotNil(t, ch, "a namespace-scope variable must chunk")
		assert.Equal(t, "declaration", ch.ChunkType)
		assert.Equal(t, "ns", ch.ParentName,
			"the namespace arrives through the existing container ascent, with no new machinery")
	})

	t.Run("class_scope", func(t *testing.T) {
		// THIS IS AN ANCHOR-LIVENESS MATCH PROBE, not merely a positive
		// assertion. A tree-sitter pattern that can never match is still a
		// VALID pattern, so the landed query-compile fence passes over a dead
		// row — an earlier draft of this anchor was written over
		// `declaration > init_declarator`, which a C++ data member never
		// produces, and it compiled cleanly while matching nothing.
		ch := cppChunkFor(res, "flags")
		require.NotNil(t, ch, "a class data member must chunk, or the field_declaration_list anchor is dead")
		assert.Equal(t, "field_declaration", ch.ChunkType)
		assert.Equal(t, "Base", ch.ParentName)

		// KNOWN-NEGATIVE IN THE SAME CLASS BODY: a function-pointer member is
		// taken by NEITHER this row nor the sibling header-member row, because
		// its declarator is a function_declarator rather than a bare
		// field_identifier. That disjointness is what the C dispatch phase's
		// own field-node row depends on.
		assert.Nil(t, cppChunkFor(res, "cb"),
			"a function-pointer data member is taken by no row here: its declarator is a function_declarator")
	})

	t.Run("function_local_excluded", func(t *testing.T) {
		// THE ASSERTION THAT THE BOUNDED HALF STAYED BOUNDED. Without it, a
		// later editor who "simplified" the three anchors into one bare
		// `(declaration) @decl` row would sweep in every function-body local
		// and every other subtest here would still pass. It depends on no row
		// naming compound_statement, and expires the moment one does.
		assert.Nil(t, cppChunkFor(res, "local_should_not_chunk"),
			"a function-body local must not chunk: no row anchors on compound_statement")

		// An out-of-class STATIC DATA definition is likewise out: its
		// declarator is a qualified_identifier, and these rows require a bare
		// identifier.
		assert.Nil(t, cppChunkFor(res, "shared"),
			"an out-of-class static data definition binds a qualified_identifier and is taken by no row here")

		// KNOWN-POSITIVE CONTROL for the two absences above: the rows DID fire
		// in this same run, so a nil here is a scope decision rather than a
		// query that matched nothing at all.
		require.NotNil(t, cppChunkFor(res, "g_counter"),
			"control: the file-scope row fired, so the absences above are the anchors declining rather than a dead query set")
	})
}

// TestCppMemberCapture covers the two member shapes queries_cpp.go's single
// function arm matches nowhere: a header-declared member function, whose node
// is a field_declaration rather than a function_definition, and an out-of-class
// definition, whose declarator binds a qualified_identifier the arm's
// [(identifier) (field_identifier)] alternation does not admit.
//
// THE TWO ROWS PRODUCE DELIBERATELY DIFFERENT ID SHAPES. A header-declared
// member takes <file>:<Class>.<member> and IS reachable by the Parent-keyed
// lookup, so it is the node a bound call targets and the implementer side of a
// member-level IMPLEMENTS edge. An out-of-class definition takes
// <file>:<Class>::<member> with no parent; it exists so the definition's BODY
// has a source node, and giving it a parent would need a query-supplied
// ParentName, which is a shared-chunker change across every language.
func TestCppMemberCapture(t *testing.T) {
	c := NewChunker()
	t.Cleanup(c.Close)
	res, err := c.ChunkFile(context.Background(), "src/members.cc", []byte(cppMemberFixture))
	require.NoError(t, err)
	require.NotEmpty(t, res.Chunks, "fixture control: the file produced chunks at all")

	t.Run("header_declared", func(t *testing.T) {
		ch := cppChunkFor(res, "declared")
		require.NotNil(t, ch, "a header-declared member function must chunk")
		assert.Equal(t, "field_declaration", ch.ChunkType)
		assert.Equal(t, "Base", ch.ParentName,
			"it takes the SAME receiver-qualified shape the pure-virtual members already take")

		// KNOWN-POSITIVE CONTROL for that shape: the pure-virtual member was
		// already captured by the landed function arm, so the two must agree.
		pure := cppChunkFor(res, "pure")
		require.NotNil(t, pure, "control: the landed function arm still captures a pure-virtual member")
		assert.Equal(t, "Base", pure.ParentName)
	})

	t.Run("out_of_class", func(t *testing.T) {
		// THE NAME IS ASSERTED, NOT ONLY THE COUNT. A file-scope definition
		// over 30 bytes was already collected as a NAMELESS orphan before this
		// row existed, so a count assertion could not tell the two apart.
		ch := cppChunkFor(res, "Base::run")
		require.NotNil(t, ch, "an out-of-class definition must chunk under its full qualified spelling")
		assert.Equal(t, "function_definition", ch.ChunkType)
		assert.Empty(t, ch.ParentName,
			"it carries no parent: a query-supplied ParentName would be a shared-chunker change across every language")
	})

	t.Run("fp_field_excluded", func(t *testing.T) {
		// ROW A'S SOLE KNOWN-NEGATIVE, and the cross-phase disjointness guard:
		// the C dispatch phase adds a row for exactly this shape to
		// queries_c.go, and the two must stay disjoint. They are, by nesting
		// depth — a function-POINTER field wraps its field_identifier in
		// parenthesized_declarator > pointer_declarator, which row A's
		// direct-child requirement excludes.
		assert.Nil(t, cppChunkFor(res, "cb"),
			"a function-pointer data member is taken by neither the member rows nor the variable rows")

		require.NotNil(t, cppChunkFor(res, "declared"),
			"control: row A fired in this same run, so the absence above is its declarator requirement rather than a dead row")
	})

	t.Run("nested_qualified", func(t *testing.T) {
		// Catches a naming rule that kept only the LAST segment of the
		// qualified spelling.
		ch := cppChunkFor(res, "ns::Deep::m")
		require.NotNil(t, ch, "a namespace-nested out-of-class definition keeps its FULL qualified spelling")
		assert.Equal(t, "function_definition", ch.ChunkType)

		member := cppChunkFor(res, "m")
		require.NotNil(t, member, "the nested class's own declaration must chunk")
		assert.Equal(t, "Deep", member.ParentName,
			"containment is SINGLE-ANCESTOR: the member takes its nearest named container, not a dotted chain")
	})
}
