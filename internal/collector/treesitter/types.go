// SPDX-License-Identifier: Apache-2.0

package treesitter

// ChunkType classifies the semantic unit a chunk represents.
// Values are raw tree-sitter node types (e.g., "function_declaration", "comment",
// "import_declaration") passed through without translation.
type ChunkType = string

// Chunk represents a semantic unit of source code extracted from AST parsing.
type Chunk struct {
	// Content is the raw source text of this chunk.
	Content string

	// FilePath is the source file this chunk came from.
	FilePath string

	// Language is the detected programming language.
	Language Language

	// ChunkType is the raw tree-sitter node type (e.g., "function_declaration",
	// "comment", "method_declaration", "import_declaration", "block_mapping_pair").
	ChunkType ChunkType

	// Name is the identifier (function name, type name, etc.). Empty for anonymous chunks.
	Name string

	// StartLine and EndLine are 1-indexed line numbers.
	StartLine int
	EndLine   int

	// StartByte and EndByte are byte offsets into the original source.
	StartByte int
	EndByte   int

	// Exported indicates the declaration has public visibility (e.g., JS/TS export).
	Exported bool

	// IsTest is true when this chunk represents test code (test, benchmark,
	// example, fuzz, setup, teardown, fixture, mock, helper). Populated by
	// per-language predicate logic in sibling tickets; this ticket only
	// delivers the rails. Defaults to false on every chunk until predicates land.
	IsTest bool

	// TestKind is the classification within IsTest. Empty string (TestKindNone)
	// when IsTest is false; one of the 9 TestKind constants when IsTest is true.
	// Predicate logic populates this in sibling tickets.
	TestKind TestKind

	// PathHash is a short hash of the AST path from this node to the root.
	// Used as a collision breaker when two chunks in the same file produce
	// the same node ID (e.g., same variable name in sibling scopes).
	PathHash string

	// Context provides surrounding information for retrieval quality.
	Context ChunkContext

	// ParentName is the containing type/class name, if any.
	ParentName string
}

// ChunkContext holds contextual information attached to a chunk for better embeddings.
type ChunkContext struct {
	// Imports relevant to this chunk (package paths or module specifiers).
	Imports []string

	// Frameworks is the per-file set of test frameworks detected from imports
	// at chunk time. Populated by DetectFrameworks(lang, Imports) inside
	// ChunkFile (chunker.go) once per file; predicates in sibling chunker_<lang>.go
	// files read this to decide which classification rules apply. Empty for
	// non-test files, files in languages without a detection table, and
	// languages whose tree-sitter Imports query is empty (Ruby, Lua, Bash,
	// Elixir, HCL) — those Bucket B predicates use their own AST signals.
	Frameworks []Framework

	// ImportBindings is the file's import table: one entry per NAME its import
	// statements bind, in the language-neutral ImportBinding shape. It is a
	// FAITHFUL record of the statement list rather than a filtered one — a
	// side-effect import binds no name and is recorded anyway, with an empty
	// Local.
	//
	// Populated only for languages with a registered importParsers arm, and
	// empty for the other 29. It is a file-level fact: every chunk of one file
	// carries the same table.
	ImportBindings []ImportBinding

	// ReExports is the file's `export ... from '<spec>'` table — names it
	// forwards from another module without binding them locally. Populated
	// only for languages with a registered importParsers arm; empty for the
	// other 29, and empty for a file whose exports name no source.
	ReExports []ReExport

	// DefaultExportName is the declared name behind this file's
	// `export default`, for languages that have the form: the function or class
	// name in `export default function App() {}`, or the identifier in
	// `export default someIdent`. Empty when the file default-exports nothing,
	// when the default export is anonymous, and for every language with no
	// registered importParsers arm.
	DefaultExportName string

	// PackageName is the symbol namespace edge IDs are qualified with: the Go
	// package clause when the file declares one, otherwise
	// "<language>:<directory>" derived from the file path by fileNamespace.
	// It is never persisted — it exists only between chunker emission and edge
	// resolution, which rewrites every surviving endpoint to a graph node ID.
	PackageName string

	// Signature is the full function/method signature if applicable.
	Signature string
}

// QuerySet defines tree-sitter queries for extracting semantic nodes and edges
// from a specific language. The S-expression strings are compiled into
// *sitter.Query objects by the Chunker on first use per language.
type QuerySet struct {
	// TopLevel matches top-level declarations to extract as chunks.
	TopLevel string

	// Calls matches function call expressions within a node.
	Calls string

	// Imports matches import statements.
	Imports string

	// TypeRefs matches type references in signatures/fields.
	TypeRefs string

	// TestBlocks matches call-expression / macro / do-end style test
	// invocations (e.g., it/describe/test/context, RSpec do-end, GoogleTest
	// TEST macro). Per-language Bucket B predicate tickets populate this.
	// Empty for languages whose tests are declaration-based (Go, Python, etc.) —
	// the chunker skips the test-blocks pass when this is empty, identical to
	// how Imports="" skips the imports pass.
	//
	// Capture convention:
	//   @decl        — the call/macro/do-block node (required).
	//   @name        — string-literal label (optional; falls back to firstStringArg).
	//   @parent_name — outer describe/context name when nested (optional).
	//   @params      — closure parameter list text (optional; assigned verbatim
	//                  to chunk.Context.Signature).
	TestBlocks string
}

// EdgeType is the relationship type for edges extracted during chunking.
type EdgeType string

const (
	EdgeContains EdgeType = "CONTAINS"
	EdgeImports  EdgeType = "IMPORTS"
	EdgeCalls    EdgeType = "CALLS"
	EdgeUsesType EdgeType = "USES_TYPE"
	EdgeEmbeds   EdgeType = "EMBEDS"

	// EdgeTestCalls is the CALLS edge a TEST source emits — the body of a
	// test_block chunk, or a declaration lexically inside one. Resolution
	// treats it exactly as EdgeCalls (it falls to the same reference arm at
	// parser/edges.go:128), so the distinct type buys the READ side an
	// explicit choice rather than buying the write side any new behavior.
	//
	// The wire literal mirrors kgtypes.EdgeTestCalls verbatim, in the same
	// per-module-duplicate idiom this file's other four constants already
	// follow — the chunker carries its own EdgeType vocabulary because no
	// hand-written package is shared across the two binaries.
	EdgeTestCalls EdgeType = "TEST_CALLS"
)

// Edge represents a directed relationship between two code entities.
type Edge struct {
	FromID string
	ToID   string
	Type   EdgeType

	// Weight is the call count for CALLS edges (number of times caller
	// invokes callee inside the function body) or 0 for edge types where
	// aggregation does not apply (CONTAINS, IMPORTS, USES_TYPE, EMBEDS).
	Weight float64

	// FromChunk and ToChunk address a chunk POSITIONALLY: a 1-BASED index
	// into the emitting Result.Chunks, with 0 meaning unset. The 1-based
	// encoding is load-bearing — an Edge composite literal that omits the
	// field zero-values it, and slot 0 is a real chunk, so a 0-based field
	// would make every existing literal in every test claim to point at the
	// file's first declaration.
	//
	// A containment edge carries BOTH a name-built endpoint and a slot. The
	// slot is authoritative: the parser's pre-pass overwrites an endpoint
	// from its slot wherever one exists, and leaves the name alone where the
	// slot is 0. The name is a legacy carrier, not a fallback the pre-pass
	// consults.
	FromChunk int
	ToChunk   int

	// RefByte is the StartByte of the DECLARATION that emitted this
	// reference, and the second component of the ambiguity group key. It is
	// 0 on containment and IMPORTS edges, which are not references. Byte 0
	// needs no unset encoding: it is the file's first byte, and no
	// declaration whose reference we resolve starts there.
	RefByte int

	// Ref is the reference site: one per file, shared by every edge whose
	// declaration has no parent, plus one derived copy per parented
	// declaration carrying that declaration's Parent.
	//
	// nil on IMPORTS and on positionally-addressed CONTAINS; SET on
	// reference edges AND on a Go method's parent-to-member CONTAINS, whose
	// receiver source is a sibling the chunker cannot address by slot.
	Ref *RefSite
}

// Result holds the complete output of parsing and chunking a single file.
type Result struct {
	FilePath string
	Language Language
	Chunks   []Chunk
	Edges    []Edge

	// Ref is the file-level reference site, carried here so the parser's
	// post-chunk Binds pass can reach it directly. Reaching it through Edges
	// instead would miss any file that emitted no reference edge at all.
	Ref *RefSite
}
