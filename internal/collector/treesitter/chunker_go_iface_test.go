// SPDX-License-Identifier: Apache-2.0

// AUTHORITATIVE VOCABULARY for Go interface method-spec nodes, the syntax-level
// IMPLEMENTS derivation, and interface-declaration CALLS targeting. This block is
// the single place these values are written down; the collector code and its
// sibling tests cite it rather than restating it. (The package's own doc comment
// lives in doc.go; this is a file-scoped reference block, not a second one.)
//
// V1. NODE KIND. `method_elem` is the tree-sitter Go grammar's node kind for an
// interface method spec. Proven by parsing rather than assumed: an explain over
//
//	type SubCollector interface {
//		Collect(ctx context.Context, req Req) (SubCollectorResult, error)
//		Name() string
//		io.Closer
//	}
//
// returns type_declaration > type_spec > interface_type > method_elem(
// field_identifier, parameter_list, <result>), and the embedded `io.Closer`
// element is a sibling `type_elem`, NOT a method_elem.
//
// V2. NODE ID SHAPE. `<file>:<Interface>.<Method>` — receiver-qualified exactly
// like a Go method, produced by ChunkNodeID from Chunk.Name=<Method> and
// Chunk.ParentName=<Interface>.
//
// V3. THE ANCHORED QUERY ARM, verbatim, and it MUST be written UNBROKEN ON ONE
// LINE inside the TopLevel raw string (gofmt never rewraps raw string literal
// contents, and a gate greps this exact byte sequence):
//
//	(type_declaration (type_spec name: (type_identifier) (interface_type (method_elem name: (field_identifier) @name) @decl)))
//
// NOTE the type_spec's name carries NO capture. Capturing it would bind a second
// @name in the same alternation member and extractDeclAndName would take the
// INTERFACE's name as the chunk's name.
//
// V4. WHY ANCHORED. Proven by execution against this module's vendored grammar
// pin: over a fixture containing three named interfaces and one ANONYMOUS
// interface in a parameter (`func f(x interface{ Anon() error }) {}`), the bare
// arm `(method_elem name: (field_identifier) @name) @decl` returned 5 matches
// INCLUDING Anon, and the anchored arm returned exactly the 4 named-interface
// specs. An anonymous interface's spec has no enclosing type_spec name, so under
// the bare arm it would chunk with ParentName="" and take node ID `<file>:Anon`
// — colliding with any function of that name in the file.
//
// V5. PARENT ASCENT. From the method_elem node: Parent() must be
// `interface_type`, its Parent() must be `type_spec`, and the name is that
// type_spec's `name:` field. Returns "" if any link fails. Proven over real repo
// files: the collector's own Sink interface yielded Sink.WriteResult, its
// Collector interface yielded Collector.Name and Collector.Collect, and the
// server store's DB interface yielded 48 specs all with parent-ascent="DB". A
// generic interface (`type Gen[T any] interface{...}`) still resolves: the
// type_spec's `name:` field binds the type_identifier regardless of the sibling
// type_parameter_list.
//
// V6. EMBEDDED-ELEMENT RULE. An `interface_type` child of kind `type_elem` is an
// EMBED spelling when it has EXACTLY ONE named child of kind type_identifier,
// qualified_type or generic_type; it DECLINES otherwise. Proven by parsing
// `type Num interface { ~int | ~float64; comparable; io.Reader; Other }`: the
// union is ONE type_elem with TWO negated_type children (declines by the count
// rule), while comparable, io.Reader and Other are single-child type_elems
// (accepted as spellings; comparable then declines at resolution because no
// in-repo scope declares it — the same treatment the measured prototype gave its
// external-embed cases).
//
// V7. EDGE TYPE. `IMPLEMENTS`, uppercase, mirrored in BOTH client vocabularies:
// kgtypes.EdgeImplements and treesitter.EdgeImplements. NO PROTO CHANGE — the
// engine proto declares the edge type as an open-vocabulary verbatim string, and
// the in-tree precedent is TEST_CALLS. Client-side traverse also needs no table
// entry: the client RESOLVES a caller's edge-type spelling against the graph's
// OWN stored vocabulary, so traverse(graph:"code", edge_types:["implements"])
// reaches IMPLEMENTS by unique case-insensitive match — not by the per-graph
// uppercase fold that used to do it, which no longer exists. NOTE,
// per V11: TEST_CALLS' zero-server-reference property does NOT carry to
// IMPLEMENTS, which already appears in server test files.
//
// V8. SIGNATURE-KEY GRAMMAR. Mirrors the measured prototype under
// ~/.knowledge/treesitter-parity-evidence/fanout/analyze/sigmatch.go
// (typeKey/sigKey), which is what produced the measured precision figures:
//
//	sigkey  := "(" params ")(" results ")"   — params/results are comma-joined typekeys in source order
//	typekey := "*"typekey | "[]"typekey | "..."typekey | "map["typekey"]"typekey |
//	           "chan "typekey | "func"sigkey | typekey"["typekey{","typekey}"]" | leaf
//	leaf    := <Scope> \x00 <Name>   for a type the DECLARING file's imports bind to an indexed in-repo scope
//	         | "ext:" <Name>          for anything else (stdlib, unindexed, a type parameter)
//	         | "ext:any"              for an empty inline interface type
//	         | "ext:iface"            for a non-empty inline interface type
//
// A parameter_declaration carrying N names and one type contributes N entries;
// names themselves are dropped. \x00 is the separator because a Go Scope ID and a
// Go identifier can never contain it. Go's Scope for the code graph is
// directory-granular — chunker_refsite.go maps LangGo to ScopeDir and ScopeID
// returns "dir:"+filepath.Dir(path) — so <Scope> is 1:1 with the Go package
// directory, which is exactly the `dir` key the prototype used. That is why the
// derivation can reproduce the measured numbers.
//
// V9. GATE COMMAND SHAPE for every test gate in this work. Both directions were
// probed:
//
//	cd "$(git rev-parse --show-toplevel)/cmd/knowledge" || exit 1; T=<Test>; L=<log>; rm -f $L; CGO_ENABLED=1 go test ./internal/collector/<pkg>/ -run "^$T$" -v -count=1 > $L 2>&1; for s in '' <subtests>; do grep -q -- "--- PASS: $T$s " $L || exit 1; done
//
// NEVER $-anchor the PASS line: go test appends a duration, so
// `--- PASS: TestX (0.00s)`. The trailing SPACE after $s is what pins the last
// path element. A -run selector matching nothing exits 0 (observed:
// `testing: warning: no tests to run`, go test exit 0), which is precisely why
// the gate is the anchored grep and not go test's status. EVERY test gate PINS
// ITS SUBTESTS — a parent-only PASS grep cannot see a test that silently lost
// half its checks. Absolute paths are forbidden in gate commands — the negative
// half of an absence gate goes vacuously green from the wrong tree.
//
//	THE RE-RUN SHAPE IS NOT UNIVERSAL, and V11 governs borrowing it. A gate that
//	RE-RUNS a test and demands `--- FAIL:` is only correct on a step that lands NO
//	PRODUCTION CHANGE, so the test is still red when the step completes. On a step
//	that authors the red test AND lands the fix, a re-running FAIL gate contradicts
//	the same step's PASS gate and no tree state satisfies both; there the red is
//	gated by READING the preserved artifact instead.
//
// V10. FROZEN ARTIFACTS, read-only:
//
//	$HOME/.knowledge/tsparity/corpora/{knowledge,agent}                    the frozen corpora
//	$HOME/.knowledge/treesitter-parity-evidence/fanout/k_impl_truth.tsv    go/types ground truth, knowledge, 3397 rows
//	$HOME/.knowledge/treesitter-parity-evidence/fanout/a_impl_truth.tsv    go/types ground truth, agent, 886 rows
//
// Row format: <relDir>\x00<IfaceName> TAB <relDir>\x00<ConcreteName>, produced by
// packages.Load + types.Implements over both value and pointer receivers with
// Tests:true. The row counts are MEASURED PROPERTIES OF THE FROZEN FILES and are
// legitimate cardinality controls precisely because the files are frozen; they
// are NOT tree-derived.
//
// V11. THE BORROWED-PRECEDENT RULE — standing. WHEN A CHANGE COPIES AN IN-TREE
// PRECEDENT, NAME THE PROPERTY THAT MADE THE PRECEDENT CORRECT AND RE-DERIVE THAT
// PROPERTY AGAINST THE NEW SUBJECT BEFORE WRITING THE CHANGE. IT GOVERNS BORROWED
// TEST GATES EXACTLY AS IT GOVERNS BORROWED CODE. Four sightings, each a
// different property silently assumed to carry: a census shape borrowed with its
// "zero server-module references" gate onto an edge type that already appears in
// server tests; an acceptance harness borrowed without its PER-CORPUS expect
// table, producing a single floor one corpus cannot clear; the findNodeByType
// walking idiom borrowed without re-deriving its DESCENDANT-SAFETY (see V12); and
// the re-running red-first gate shape borrowed onto a step that does land a
// production change (see V9's closing paragraph). The rule is cheap: for each
// borrowed thing, write one sentence naming the property, then run the one
// command that checks it on the new subject.
//
// V12. TYPE-BODY ANCHORING — the concrete application of V11 for both embed
// extractors, and a defect that pre-exists. extractGoEmbeds locates its
// struct_type with findNodeByType, which is a DEPTH-FIRST DESCENT. Proven by
// executing that exact walk over a fixture:
//
//	type IfaceWithAnonStructParam interface { F(x struct{ Base }) error }   → the descent returns [Base]; a FALSE EMBEDS edge
//	type HasAnonIfaceField struct { X interface{ TokenSource }; C int }     → the same idiom applied to interface_type returns [TokenSource]; a FALSE edge
//
// THE ANCHOR: bind the body from the enclosing type_spec's `type` FIELD and never
// search descendants. Proven regression-free on the real shapes — `type
// PlainStruct struct { Base; C int }` and `type PlainIface interface { Other;
// M() error }` return identically under both walks — and generic-safe: `type
// Gen[T any] struct { EmbG }` binds struct_type through the `type` field despite
// the sibling type_parameter_list.
//
//	THE GROUPED RESIDUAL, MEASURED AND DECLINED: a `type ( A struct{...}; B
//	interface{...} )` declaration holds several type_specs under ONE
//	type_declaration, and the chunker's @decl is that shared node, so an extractor
//	given only the declaration CANNOT tell which spec the emitting declaration is.
//	The unanchored code credits every spec in such a group with the FIRST spec's
//	embeds. Both extractors therefore DECLINE (return nil) when the type_declaration
//	holds more than one type_spec — an honest "cannot attribute" rather than a
//	confident wrong answer.
//
// Measured in this repository: 3,012 type declarations, ZERO holding more than
// one TYPE_SPEC — and the qualifier matters, because the tree does contain
// exactly two `type (` GROUPS (both trees, tests included). Both are ALIAS
// groups. Proven by parsing: a `type ( X = Y )` group's children are `type_alias`
// nodes, NOT `type_spec`, while the same explain over `type ( A struct{...}; B
// interface{...} )` returns type_declaration > type_spec. The decline rule counts
// TYPE_SPECS, so it correctly does not fire on either alias group, and both
// extractors would return nil for them anyway (an alias group has no type_spec
// whose `type` field could be bound). The decline therefore costs nothing here;
// it is written for the corpora that are not this one.
//
// V13. CENSUS-DETECTOR PROBING — standing. A DETECTOR KEYS ON A PROPERTY; A GREP
// IS ONLY A PROXY FOR IT. Before pinning a corpus, STATE THE PROPERTY, then probe
// the proxy in BOTH directions against the real tree:
//
//	OVER-MATCH — what does the pattern hit that does NOT have the property? Every
//	such class gets an explicit exclusion row carrying its reason, never a silent
//	omission.
//	UNDER-MATCH — what HAS the property that the pattern cannot see? The recurring
//	forms: an ACCESSOR beside a field (`GetX()` where the grep says `.X`), helper
//	indirection, aliases, a constant standing in for a literal, and the same token
//	living in a different syntax (a raw-string query pattern rather than a Go
//	comparison).
//
// PROBING ONLY OVER-MATCH IS HOW A CENSUS STAYS GREEN WHILE READERS SLIP THROUGH
// IT. Measured here: an edge-Weight census declared the property, enumerated TWO
// over-match classes, and still missed SIX non-test files reading an edge's
// Weight through the generated accessor `GetWeight()` — one of them a
// state-digest consumer, not plumbing. The under-match probe is one command —
// build a widened list and `comm -13` the narrow one against it — and it either
// finds readers or CONFIRMS the scoping; both outcomes are worth recording. EVERY
// CENSUS RECORDS BOTH PROBES AND ATTRIBUTES EVERY MISS: a miss either gains a
// disposition row, or the change names the OTHER gate that covers it.

package treesitter

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// goIfaceFixture is the single source fixture behind every subtest of
// TestGoInterfaceMethodSpecChunks. It holds, in one file: a named interface with
// two method specs and one embedded element, an ANONYMOUS interface in a
// parameter position, and a generic interface.
const goIfaceFixture = `package app

import "io"

type Result struct{}

type Sink interface {
	WriteResult(r Result) error
	Name() string
	io.Closer
}

func f(x interface{ Anon() error }) {}

type Seg struct{}

type Gen[T any] interface {
	Build(q T) (Seg, error)
}
`

// chunkGoIfaceFixture drives the package's standard fixture idiom — chunk an
// inline Go source string through NewChunker().ChunkFile and read Result.Chunks.
// Only "how to drive the chunker in a test" is borrowed from the sibling fixture
// helpers; that property carries unconditionally (V11).
func chunkGoIfaceFixture(t *testing.T) []Chunk {
	t.Helper()
	c := NewChunker()
	t.Cleanup(c.Close)
	res, err := c.ChunkFile(context.Background(), "app/iface.go", []byte(goIfaceFixture))
	require.NoError(t, err)
	require.NotEmpty(t, res.Chunks, "fixture control: the file produced chunks at all")
	return res.Chunks
}

// TestGoInterfaceMethodSpecChunks pins that a Go interface's method specs become
// chunks in their own right, parented to the interface, and that neither an
// anonymous interface's specs nor an embedded type_elem is mistaken for one.
func TestGoInterfaceMethodSpecChunks(t *testing.T) {
	chunks := chunkGoIfaceFixture(t)

	t.Run("named_specs_chunk", func(t *testing.T) {
		want := map[string]string{"WriteResult": "Sink", "Name": "Sink"}
		got := map[string]string{}
		for _, c := range chunks {
			if c.ChunkType != "method_elem" {
				continue
			}
			if _, ok := want[c.Name]; ok {
				got[c.Name] = c.ParentName
			}
		}
		require.Equal(t, want, got,
			"each named interface's method spec chunks as method_elem with the interface as its parent")
	})

	t.Run("anon_specs_excluded", func(t *testing.T) {
		// KNOWN-POSITIVE CONTROL: the same fixture must have produced at least
		// one method_elem chunk, so the absence assertion cannot pass over an
		// empty chunk set.
		specs := 0
		for _, c := range chunks {
			if c.ChunkType == "method_elem" {
				specs++
			}
		}
		require.Positive(t, specs, "control: the fixture produced method_elem chunks at all")

		for _, c := range chunks {
			require.NotEqual(t, "Anon", c.Name,
				"a spec of an ANONYMOUS interface in a parameter has no enclosing type_spec name and must not chunk")
		}
	})

	t.Run("type_elem_not_a_spec", func(t *testing.T) {
		for _, c := range chunks {
			require.NotEqual(t, "type_elem", c.ChunkType,
				"an embedded element is a type_elem, not a method spec, and is not chunked as a declaration")
			require.NotEqual(t, "Closer", c.Name,
				"the embedded io.Closer element is not a method spec")
		}
	})

	t.Run("spec_renders_its_signature", func(t *testing.T) {
		// A method spec is a DECLARATION KIND LIKE ANY OTHER in symbol listings,
		// and every other Go declaration kind renders a signature there. A spec
		// has no body, so extractGoSignature's no-body branch returns the whole
		// node — which for a spec is exactly its signature text, receiver-free.
		want := map[string]string{
			"WriteResult": "WriteResult(r Result) error",
			"Name":        "Name() string",
			"Build":       "Build(q T) (Seg, error)",
		}
		got := map[string]string{}
		for _, c := range chunks {
			if c.ChunkType != "method_elem" {
				continue
			}
			if _, ok := want[c.Name]; ok {
				got[c.Name] = c.Context.Signature
			}
		}
		require.Equal(t, want, got,
			"every interface method spec renders its own signature, never the empty string")

		// KNOWN-POSITIVE CONTROL FOR THE OTHER KINDS, so this subtest cannot pass
		// by a change that filled specs and emptied methods.
		for _, c := range chunks {
			if c.ChunkType == "function_declaration" && c.Name == "f" {
				require.Equal(t, "func f(x interface{ Anon() error })", c.Context.Signature,
					"control: a function declaration still renders its signature without its body")
			}
		}
	})

	t.Run("generic_spec_named", func(t *testing.T) {
		found := false
		for _, c := range chunks {
			if c.ChunkType == "method_elem" && c.Name == "Build" {
				found = true
				require.Equal(t, "Gen", c.ParentName,
					"a generic interface's type_spec still binds its name: field despite the sibling type_parameter_list")
			}
		}
		require.True(t, found, "the generic interface's Build spec chunks")
	})
}
