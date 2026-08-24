// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"bytes"
	"context"
	"path/filepath"

	sitter "github.com/smacker/go-tree-sitter"
)

// chunker_toplevel.go holds the TOP-LEVEL DECLARATION DISCOVERY half of the
// chunker — the C/C++ header fallback and the declaration collector — split out
// of chunker.go for the 500-line file cap.
//
// They answer "what declarations does this file contain, and where"; chunker.go's
// walkTopLevel answers "what nodes and edges do those declarations produce". The
// import-edge emission stays with the walk, beside the file context it reads.

// cppHeaderFallback re-parses a C header under the cpp grammar and returns the
// alternate tree when cpp is the better reading, or nil when the C tree stands.
// A `.h` may legitimately be either language and extMap routes every one of
// them to C, so the extension is a guess that the parse confirms. The caller
// owns closing the tree this returns, and a rejected alternate is closed here.
//
// A CLEAN C PARSE IS NOT EVIDENCE OF A C HEADER, and that is why this does more
// than read HasError. The C grammar is permissive enough to parse a C++ class
// header WITHOUT an error node — measured on leveldb, whose entire public API
// lives in `include/leveldb/*.h` and every one of which parsed clean as C, so
// the abstract bases the whole library is built on were chunked by a query set
// with no class row and never captured at all. An error-only rule silently
// mis-routes exactly the headers that matter most.
//
// THE DISCRIMINATOR IS A C++-ONLY NODE KIND IN THE CPP TREE, never a token scan
// and never a guess from the C tree. Three properties make it safe in the one
// direction that matters — a real C header must NEVER flip:
//
//   - C is very nearly a subset of C++, so a pure C header parses clean under
//     cpp and produces NONE of these kinds: a C `struct` is a struct_specifier
//     under both grammars, never a class_specifier.
//   - A C header that uses a C++ keyword as an identifier is declined by one of
//     two paths, and only one of them involves an error. `int template;` and
//     `int operator;` make the cpp parse ERROR, and an erroring alternate is
//     rejected below. `int class;` parses CLEAN under cpp and yields no kind
//     from the set, so the node-kind check declines it — which is the point: a
//     clean alternate parse is never on its own a reason to adopt.
//   - The kinds are ones C has no syntax for at all, so their presence is a
//     positive statement about the source rather than an inference from the
//     absence of an error.
//
// THE SECOND PARSE IS RATIONED. A clean C header pays a byte scan for a C++
// marker first, and only a header carrying one is re-parsed; a header whose C
// parse already errored skips the scan, because its chunks are garbage either
// way. The scan may false-positive on a comment or a string literal, which
// costs one parse and changes no outcome — the node-kind check below is the
// authority.
func (c *Chunker) cppHeaderFallback(
	ctx context.Context, filePath string, src []byte, lang Language, tree *sitter.Tree,
) *sitter.Tree {
	if lang != LangC || filepath.Ext(filePath) != ".h" {
		return nil
	}
	errored := tree.RootNode().HasError()
	if !errored && !bytesHaveCPPMarker(src) {
		return nil
	}
	alt, err := c.parser.Parse(ctx, src, LangCPP)
	if err != nil {
		return nil
	}
	if alt.RootNode().HasError() {
		alt.Close()
		return nil
	}
	// A header the C grammar could not parse is adopted on the clean cpp parse
	// alone: there is no C reading to prefer. A header C parsed cleanly is
	// adopted only on positive evidence.
	if !errored && !treeHasCPPOnlyConstruct(alt) {
		alt.Close()
		return nil
	}
	return alt
}

// cppOnlyMarkers are the byte sequences that make a clean-C header worth a
// second parse. This is a RATIONING filter, not the decision.
var cppOnlyMarkers = [][]byte{
	[]byte("class"), []byte("namespace"), []byte("template"),
	[]byte("public:"), []byte("private:"), []byte("protected:"),
	[]byte("virtual"), []byte("operator"), []byte("::"),
}

// bytesHaveCPPMarker reports whether the source mentions anything that could
// make it C++.
func bytesHaveCPPMarker(src []byte) bool {
	for _, m := range cppOnlyMarkers {
		if bytes.Contains(src, m) {
			return true
		}
	}
	return false
}

// cppOnlyKinds are node kinds C has NO SYNTAX FOR, so one appearing in a clean
// cpp parse is positive evidence the source is C++.
//
// Deliberately NOT here: a struct with a member function, which is genuinely
// C++-only but whose cpp parse shape — field_declaration over a
// function_declarator — is one nesting level away from a C function-POINTER
// field, and telling them apart is a discrimination this set does not need to
// make. Every C++ header that matters carries one of the kinds below.
var cppOnlyKinds = map[string]bool{
	"class_specifier":      true,
	"namespace_definition": true,
	"template_declaration": true,
	"access_specifier":     true,
}

// treeHasCPPOnlyConstruct walks a cpp parse for the first C++-only kind.
func treeHasCPPOnlyConstruct(tree *sitter.Tree) bool {
	stack := []*sitter.Node{tree.RootNode()}
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if cppOnlyKinds[n.Type()] {
			return true
		}
		for i := range int(n.NamedChildCount()) {
			stack = append(stack, n.NamedChild(i))
		}
	}
	return false
}

// pendingDecl is a declaration collected by walkTopLevel's first pass, held
// until colliding names have been counted so the suffix can be applied before
// the chunk and its edges are built from the same name.
type pendingDecl struct {
	declNode   *sitter.Node
	chunkType  string
	name       string
	parentName string
}

// collectTopLevelDecls runs the TopLevel query and returns every declaration it
// matched, alongside the byte ranges they cover so the caller can find orphans.
// Nothing is emitted here: names are not final until colliding ones have been
// counted across the whole file.
func collectTopLevelDecls(
	root *sitter.Node,
	src []byte,
	lang Language,
	cqs *compiledQuerySet,
) (pending []pendingDecl, coveredRanges []byteRange) {
	qc := sitter.NewQueryCursor()
	defer qc.Close()
	qc.Exec(cqs.topLevel, root)

	for {
		m, ok := qc.NextMatch()
		if !ok {
			break
		}
		m = filterPredicates(cqs.topLevel, m, src)

		declNode, name := extractDeclAndName(m, cqs, src)
		if declNode == nil {
			continue
		}

		// Skip declarations whose parent is an export_statement — the
		// export_statement pattern will capture them with the full content
		// including the export keyword.
		if p := declNode.Parent(); p != nil && p.Type() == "export_statement" {
			continue
		}

		coveredRanges = append(coveredRanges, byteRange{
			start: declNode.StartByte(),
			end:   declNode.EndByte(),
		})

		chunkType := resolveChunkType(declNode)

		// Extract name from lexical_declaration (const/let/var).
		if name == "" && chunkType == "lexical_declaration" {
			name = extractLexicalName(declNode, src)
		}

		// Per-language name recovery for declarations whose TopLevel query binds
		// no @name. It runs on the PARSED NODE rather than by tightening the
		// query, because a query pattern that names a field also FILTERS on it:
		// requiring pattern:(value_name) on OCaml's value_definition deletes
		// `let () = ...` and `let%test "x" = ...` outright, and requiring
		// name:(namespace_name) on PHP's namespace_definition deletes the
		// unnamed global `namespace { ... }`. Resolving here leaves the chunk set
		// byte-identical and adds only the Name.
		//
		// The empty-name guard is the whole safety argument: a resolver can only
		// fill a name that is empty today, so no declaration that already has one
		// can change its node ID. The placement is load-bearing too — the name
		// must be final before the pending entry below, because pass 2 counts
		// collisions on (parentName, name) and a name filled later would be
		// excluded from that count and emit an unsuffixed duplicate ID.
		if name == "" {
			if resolve, ok := declNameResolvers[lang]; ok {
				name = resolve(declNode, src, chunkType)
			}
		}

		// One parent for both emitters: the chunk's ParentName and the edge
		// endpoints must be the same string, or parser/populate's symbolMap
		// key and the edge IDs disagree and the edges fail resolution.
		pending = append(pending, pendingDecl{
			declNode:   declNode,
			chunkType:  chunkType,
			name:       name,
			parentName: declParentName(declNode, src, lang, chunkType),
		})
	}
	return pending, coveredRanges
}
