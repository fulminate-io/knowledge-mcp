// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	sitter "github.com/smacker/go-tree-sitter"
)

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

	// IDOrdinal disambiguates UNNAMED chunks whose content is byte-identical
	// within one file — the only case a content-derived id cannot separate on its
	// own. Zero means "no ordinal", which is every chunk that does not collide;
	// DeduplicateChunks assigns 2, 3, … to the second and later occurrences, so the
	// first keeps the bare id and appending a duplicate does not disturb it.
	//
	// IT EXISTS BECAUSE THE NAME MUST STAY EMPTY. The pre-existing collision
	// breaker appends PathHash to Name, which for an unnamed chunk would move it to
	// the NAMED branch of ChunkNodeID and give its node a non-empty SymbolName —
	// turning an unnamed span into something every symbol-addressed consumer reads
	// as a named declaration. A separate field keeps that bound intact.
	IDOrdinal int

	// Context provides surrounding information for retrieval quality.
	Context ChunkContext

	// ParentName is the containing type/class name, if any.
	ParentName string

	// TypeFacts are the declaration's syntax-visible type facts — a function's
	// declared result types, a struct's field types — for the typed-qualifier
	// resolution rung. It is nil for every language with no registered
	// TypeFactsResolver, and nil for any declaration whose kind carries none.
	//
	// IT DOES NOT REACH THE WIRE. appendChunkNode builds the protobuf node
	// field by field and is deliberately not given this one: these facts are
	// consumed by the parser's declaration index during resolution and have no
	// reader on the server side.
	TypeFacts *TypeFacts
}

// ImportSite is ONE import statement as the source wrote it — the unit the
// IMPORTS edge is emitted per, which is why this is a per-SITE record rather
// than the bare specifier string it replaced.
//
// WHY A SITE AND NOT A SPECIFIER. Every entry here becomes one IMPORTS edge, and
// an edge's identity is (from, to, type, evidence). Two import statements naming
// the SAME specifier in one file — Python's two `from x import ...` lines are the
// measured case — produced two edges identical in all four parts, so the schema
// stored them as one and one membership was silently lost. Carrying the site lets
// the emission stamp a distinguishing group key, so two statements stay two rows.
type ImportSite struct {
	// Specifier is exactly what the bare string entry carried before this type
	// existed — the package path or module specifier, quote-stripped — and it is
	// still what the IMPORTS edge's ToID is built from. Framework detection reads
	// only this.
	Specifier string

	// Local is the name bound in this file, carried VERBATIM, and EMPTY when this
	// language has no registered import arm to know it. Empty means "not known
	// here", never "no alias": the same rule ImportBinding.Local states at length,
	// including Go's distinct "." and "_" forms, applies unchanged.
	//
	// Only four languages register an arm that can fill this (Go, TypeScript, TSX,
	// JavaScript), which is precisely why the group key cannot be alias-keyed
	// alone — Python, one of the two languages exhibiting the defect, has no arm.
	Local string
}

// importSpecifiers projects import sites down to the bare specifier strings, for
// the consumers that ask only which modules a file depends on. It preserves
// ORDER and DUPLICATES: two sites naming one specifier project to two entries,
// exactly as the bare-string field carried them before ImportSite existed, so no
// consumer's counting changes.
func importSpecifiers(sites []ImportSite) []string {
	if len(sites) == 0 {
		return nil
	}
	out := make([]string, len(sites))
	for i, s := range sites {
		out[i] = s.Specifier
	}
	return out
}

// ChunkContext holds contextual information attached to a chunk for better embeddings.
type ChunkContext struct {
	// Imports relevant to this chunk, one entry per import SITE (see ImportSite).
	Imports []ImportSite

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

	// EdgeImplements records that a concrete type satisfies an interface.
	//
	// THE CHUNKER DOES NOT EMIT IT. Interface satisfaction is derived over the
	// declaration index, which only the parser holds, and the parser emits the
	// edge through kgtypes.EdgeImplements. This constant exists so the two client
	// vocabularies stay COMPLETE and pinned against each other — the same
	// per-module-duplicate idiom this file's other constants follow, since no
	// hand-written package is shared across the two binaries — not because
	// anything in this package produces the edge.
	EdgeImplements EdgeType = "IMPLEMENTS"

	// EdgeFlowsToReturn, EdgeFlowsToArg and EdgeFlowsToField are the model-free
	// flow facts, and unlike EdgeImplements above THIS PACKAGE EMITS THEM: the
	// chunker runs the per-language flow-step arm and the closure engine inside
	// emitDeclarationEdges and appends these edges itself.
	//
	// The wire literals mirror kgtypes.EdgeFlowsToReturn / EdgeFlowsToArg /
	// EdgeFlowsToField verbatim, in the same per-module-duplicate idiom this
	// file's other constants follow. kgtypes carries the single authoritative
	// doc block — endpoints, the Evidence grammar, and the STRUCTURAL
	// (Type == FLOWS_TO_ARG && FromID == ToID) test for an unresolved-callee
	// self-edge — and it is not restated here.
	EdgeFlowsToReturn EdgeType = "FLOWS_TO_RETURN"
	EdgeFlowsToArg    EdgeType = "FLOWS_TO_ARG"
	EdgeFlowsToField  EdgeType = "FLOWS_TO_FIELD"

	// EdgeEvidenceFlowPrefix opens the Evidence string every flow edge carries,
	// mirroring kgtypes.EdgeEvidenceFlowPrefix verbatim.
	//
	// THE CHUNKER RENDERS EVIDENCE FROM THIS MIRROR, NOT FROM kgtypes, and that
	// is the point of mirroring it. This package has zero production kgtypes
	// imports — deliberately, because no hand-written package is shared across
	// the two binaries — so a kgtypes reference in the emission path would
	// contradict the reason the constants above are duplicated at all.
	// TestFlowVocabularyLockstep pins this spelling against the kgtypes
	// declaration so the two cannot drift.
	EdgeEvidenceFlowPrefix = "flow:"
)

// Edge represents a directed relationship between two code entities.
type Edge struct {
	FromID string
	ToID   string
	Type   EdgeType

	// Weight is the call count for CALLS and TEST_CALLS edges (the number of
	// times the caller invokes the callee inside the body) or 0 for edge types
	// where aggregation does not apply (CONTAINS, IMPORTS, USES_TYPE, EMBEDS).
	//
	// TEST_CALLS IS WEIGHTED, NOT ZERO, and the distinction is easy to get
	// backwards. A test-origin call edge is built by the same weighted producer a
	// production one is, and the two sites that retype it to TEST_CALLS mutate
	// ONLY the Type field — nothing between production and append touches Weight
	// — so a TEST_CALLS edge carries the call count exactly as a CALLS edge does.
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

	// Evidence is the GROUP KEY, not a justification for the edge — the same
	// field-with-a-misleading-name the resolved edge carries, populated here for
	// the one edge kind the parser passes through without resolving.
	//
	// SET on IMPORTS, where it names the import SITE (`import:<local>:<n>`) so two
	// statements naming one specifier stay two rows under the four-part edge
	// identity. ALSO SET on FLOWS_TO_RETURN, FLOWS_TO_ARG and FLOWS_TO_FIELD,
	// where it carries the flow key — the source and sink POSITIONS the fact is
	// about, `flow:p0>a2` and the like — under the grammar the edge-type
	// vocabulary documents. The optional callee and group-key components of that
	// grammar are appended by the parser, which is the only layer that knows
	// them. Empty on every other kind emitted here: a reference's key can only
	// be built once resolution knows the candidate set, so the parser stamps it.
	Evidence string

	// NO POSITION FIELD LIVES HERE, and its absence is load-bearing rather than
	// an omission. This carried RefByte — the emitting declaration's StartByte,
	// once the second component of the ambiguity group key — until the key went
	// position-independent. A write-only position field is exactly the affordance
	// that invites a future author to re-derive an identity from a byte offset,
	// which is the defect measured on the reference key: an
	// edit shifts the offset, the shifted key is a NEW edge identity, the pre-edit
	// row is never reclaimed and the file's contribution hash can never agree.
	// The group key names the enclosing DECLARATION instead (parser.groupKey).

	// Ref is the reference site: one per file, shared by every edge whose
	// declaration has no parent, plus one derived copy per parented
	// declaration carrying that declaration's Parent.
	//
	// nil on IMPORTS, on positionally-addressed CONTAINS, and on
	// FLOWS_TO_RETURN, which is a self-edge addressed by slot at both ends and
	// references nothing; SET on reference edges, on a Go method's
	// parent-to-member CONTAINS, whose receiver source is a sibling the chunker
	// cannot address by slot, and on FLOWS_TO_ARG and FLOWS_TO_FIELD, which
	// resolve a callee spelling and a field owner respectively against this same
	// site.
	Ref *RefSite
}

// FlowStepKind names one GRAMMAR SHAPE a per-language flow-step arm recognized
// inside a declaration. The vocabulary is closed at five members.
type FlowStepKind string

const (
	StepParam   FlowStepKind = "param"    // Target is a parameter's name node; Index is its position
	StepDefine  FlowStepKind = "define"   // a local name is bound; Sources are the operands read
	StepAssign  FlowStepKind = "assign"   // Sources flow into Target (or into Field on the receiver)
	StepCallArg FlowStepKind = "call-arg" // Sources occupy argument Index of a call to Callee
	StepReturn  FlowStepKind = "return"   // Sources occupy result Index of a return
)

// FlowStep is ONE syntactic observation a flow-step arm made inside a single
// declaration: a name was bound, operands were read, a value occupied an
// argument or result position.
//
// THE ARM SUPPLIES GRAMMAR SHAPE; THE ENGINE SUPPLIES DATAFLOW SEMANTICS. An
// arm reports what the tree says and nothing more; deciding which parameter
// reaches which sink — alias closure, rebinding, receiver-field tracking — is
// the language-agnostic closure engine's job. That split is the whole reason a
// fifth per-language registry beats fifteen closure implementations, and an arm
// that computes reachability itself has misread the contract.
//
// THREE CONTRACT PROPERTIES ARE LOAD-BEARING:
//
//  1. STEPS ARE RETURNED IN SOURCE ORDER (ascending StartByte). The closure
//     engine is order-sensitive — a rebind clears a binding — so an arm that
//     returns QUERY order rather than source order produces wrong facts
//     silently. tree-sitter yields matches in query order, not source order;
//     populate.go already sorts chunks by StartByte for exactly this reason and
//     its sort comment records the measured symptom.
//  2. FlowStep HOLDS LIVE *sitter.Node POINTERS, valid only while the owning
//     tree is alive. Consume within the parse; NEVER store a FlowStep on a
//     Chunk or carry it past ChunkFile. This is why flow facts are computed and
//     emitted inside emitDeclarationEdges rather than carried like TypeFacts.
//  3. Callee IS NOT A FREE STRING. It is whatever normalizeCallee returns for
//     that call — the SAME derivation extractCallEdges uses — and an arm that
//     cannot reproduce that spelling emits no StepCallArg at all. The
//     callee-spelling parity rule is stated authoritatively on the Go reference
//     arm; see chunker_go_flowsteps.go.
type FlowStep struct {
	// Kind is the grammar shape this step observed.
	Kind FlowStepKind

	// Target is the bound name node on StepParam, StepDefine and StepAssign.
	// Nil on StepCallArg and StepReturn, which bind nothing.
	Target *sitter.Node

	// Sources are the operand nodes this step READS. Empty when the step reads
	// nothing the arm could name.
	Sources []*sitter.Node

	// Callee is the callee spelling on StepCallArg, under the parity rule
	// property 3 states. Empty on every other kind.
	Callee string

	// Index is a StepCallArg argument index, a StepReturn result index, or a
	// StepParam parameter position.
	Index int

	// Field is the field name when StepAssign writes into a field rather than
	// into a plain local. Empty otherwise.
	Field string

	// Receiver marks the receiver parameter on StepParam, and marks a StepAssign
	// whose target is a field ON THE RECEIVER rather than on some other value.
	Receiver bool
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
