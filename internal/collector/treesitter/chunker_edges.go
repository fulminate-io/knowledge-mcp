// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"strings"
	"unicode"

	sitter "github.com/smacker/go-tree-sitter"
)

// byteRange represents a byte offset range in source code.
type byteRange struct {
	start, end uint32
}

// stripSpace removes every whitespace rune from s. A callee name never
// contains whitespace in any grammar the chunker parses, so whitespace
// inside a composed callee span is always source layout — a line break in a
// qualified name, the indent after a chained call's dot, or a grammar that
// folds leading whitespace into a node's extent — and it must not reach an
// index key. The ContainsFunc guard keeps the no-whitespace case allocation
// free, which is 99.9% of callees.
func stripSpace(s string) string {
	if !strings.ContainsFunc(s, unicode.IsSpace) {
		return s
	}
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, s)
}

// extractCallEdges finds all function calls within a node and emits one CALLS
// edge per unique callee. The Edge.Weight carries the number of call sites
// inside this caller — used by weighted PageRank to give heavily-called
// helpers a stronger gravity well.
//
// A match's callee is the source span across its `callee` captures with every
// whitespace rune removed. That span is reduced to the trailing name when it
// crosses an argument list or a subscript.
// So a grammar with no single node spanning the qualified callee can capture
// qualifier and name separately, without a chained call or an indexed receiver
// leaking into the callee.
//
// THE SPAN IS THEN NORMALIZED AGAINST THE LANGUAGE'S CALLEE PROFILE, which is
// what stops the chunker emitting a callee for a receiver the syntax does not
// name. A composite-literal receiver emits its TYPE; an optional-chain or
// non-null-assertion operator is dropped; and a span that is not a name at all,
// or a BARE method name whose receiver this emission threw away, emits NOTHING
// rather than a spelling that binds by accident. Every one of those rules is
// opted into per language: applied globally they destroy Elixir and Ruby
// predicate and bang method names and shell command words.
func (c *Chunker) extractCallEdges(node *sitter.Node, src []byte, lang Language, pkgName, sourceName string, cqs *compiledQuerySet) []Edge {
	if cqs.calls == nil {
		return nil
	}

	qc := sitter.NewQueryCursor()
	defer qc.Close()
	qc.Exec(cqs.calls, node)

	// HOISTED OUT OF THE PER-MATCH LOOP: the profile is a map read whose result
	// is identical for every match in this call.
	prof := calleeProfileFor(lang)

	// First pass: count call sites per callee while preserving the order
	// in which each callee was first observed (deterministic output).
	counts := make(map[string]int)
	var order []string

	for {
		m, ok := qc.NextMatch()
		if !ok {
			break
		}
		m = filterPredicates(cqs.calls, m, src)

		// Consider ONLY the captures named `callee`, mirroring the filter
		// extractTypeRefEdges applies below, and compose their source span.
		// Both bounds are seeded from the FIRST kept capture rather than the
		// zero value: a minStart left at 0 would start every composed span at
		// the file's first byte.
		var minStart, maxEnd uint32
		var lastCapture string
		// firstNode is the FIRST kept capture's node, seeded on the same branch
		// that seeds minStart. It is read only by the receiver-elision check
		// below, which needs a node to walk ancestors from.
		var firstNode *sitter.Node
		found := false
		for _, cap := range m.Captures {
			if cqs.calls.CaptureNameForId(cap.Index) != "callee" {
				continue
			}
			start, end := cap.Node.StartByte(), cap.Node.EndByte()
			if !found {
				minStart, maxEnd, found = start, end, true
				firstNode = cap.Node
			}
			minStart, maxEnd = min(minStart, start), max(maxEnd, end)
			lastCapture = cap.Node.Content(src)
		}
		if !found {
			continue
		}

		// THE WHOLE NORMALIZATION TAIL LIVES IN normalizeCallee, and this call
		// is not a tidiness refactor: the flow-step arms must derive the callee
		// spelling from the SAME code this edge does, because a FLOWS_TO_ARG
		// edge carries the spelling as its endpoint and resolves it against the
		// same reference site. A spelling differing by one character binds a
		// different declaration silently. See normalizeCallee's doc block.
		callee, emit := normalizeCallee(string(src[minStart:maxEnd]), prof, lastCapture, firstNode, minStart)
		if !emit {
			continue
		}
		if _, ok := counts[callee]; !ok {
			order = append(order, callee)
		}
		counts[callee]++
	}

	return weightedCallEdges(qualifiedName(pkgName, sourceName), order, counts)
}

// cutCalleeTail reduces a composed span past an argument list or a subscript:
// it cuts after the closing delimiter at closeIdx and strips the separators that
// joined the tail to what came before, falling back to the last kept capture's
// own text when nothing follows that delimiter.
func cutCalleeTail(callee string, closeIdx int, lastCapture string) string {
	// No space in the cutset: stripSpace already removed every whitespace rune
	// from the composed span, so a space here could never match. `?` and `!` are
	// DELIBERATELY absent — see the note at the call site.
	tail := strings.TrimLeft(callee[closeIdx+1:], ".:->\\")
	callee = tail
	if tail == "" {
		// lastCapture is raw node text that never passed through the
		// composition, so on a grammar that folds whitespace into node extents
		// this path emitted an untrimmed name. It gets the same treatment as the
		// composed span.
		callee = stripSpace(lastCapture)
	}
	return callee
}

// weightedCallEdges turns the first-seen callee order and the per-callee call
// site counts into one weighted CALLS edge each, preserving the observation
// order so the emitted set is deterministic.
func weightedCallEdges(from string, order []string, counts map[string]int) []Edge {
	if len(order) == 0 {
		return nil
	}
	edges := make([]Edge, 0, len(order))
	for _, callee := range order {
		edges = append(edges, Edge{
			FromID: from,
			ToID:   callee,
			Type:   EdgeCalls,
			Weight: float64(counts[callee]),
		})
	}
	return edges
}

// rangeIsNestedIn reports whether r lies inside any of the ranges in outer
// WITHOUT being one of them, which is the test for "this capture is part of a
// type expression already captured whole".
func rangeIsNestedIn(r byteRange, outer []byteRange) bool {
	for _, o := range outer {
		if r != o && r.start >= o.start && r.end <= o.end {
			return true
		}
	}
	return false
}

// extractTypeRefEdges finds type references within a node and emits USES_TYPE edges.
func (c *Chunker) extractTypeRefEdges(node *sitter.Node, src []byte, pkgName, sourceName string, cqs *compiledQuerySet) []Edge {
	if cqs.typeRefs == nil {
		return nil
	}

	qc := sitter.NewQueryCursor()
	defer qc.Close()
	qc.Exec(cqs.typeRefs, node)

	seen := make(map[string]bool)
	var edges []Edge

	// accepted holds the byte range of every @typeref capture kept so far, so a
	// capture nested inside one already accepted can be dropped. See the
	// containment test below for why.
	var accepted []byteRange

	// Skip built-in types.
	builtins := map[string]bool{
		"string": true, "int": true, "bool": true, "error": true,
		"byte": true, "rune": true, "float64": true, "float32": true,
		"int8": true, "int16": true, "int32": true, "int64": true,
		"uint": true, "uint8": true, "uint16": true, "uint32": true, "uint64": true,
		"any": true, "comparable": true,
	}

	for {
		m, ok := qc.NextMatch()
		if !ok {
			break
		}
		m = filterPredicates(cqs.typeRefs, m, src)
		for _, cap := range m.Captures {
			capName := cqs.typeRefs.CaptureNameForId(cap.Index)
			if capName != "typeref" {
				continue
			}
			// KEEP ONLY THE OUTERMOST CAPTURE PER TYPE EXPRESSION. A query
			// capturing both a qualified type and a bare type identifier yields
			// `store.Node` and then its inner `Node`, and both survive the seen
			// dedupe below because their texts differ — so without this test
			// every cross-package type reference would emit TWO USES_TYPE
			// edges, one qualified and one bare. Expressed as range containment
			// rather than a node-kind test, because the property is "this token
			// is part of a type expression already captured whole". Captures
			// arrive outermost-first for the nesting these grammars produce, so
			// one forward pass suffices and no sort is needed.
			//
			// It sits AFTER the capture-name filter and BEFORE the seen and
			// builtins tests deliberately: a suppressed inner capture must
			// never mark its bare text as seen, or a later legitimate bare
			// `Node` in the same declaration would be swallowed.
			r := byteRange{start: cap.Node.StartByte(), end: cap.Node.EndByte()}
			if rangeIsNestedIn(r, accepted) {
				continue
			}
			accepted = append(accepted, r)

			typeRef := cap.Node.Content(src)
			if typeRef == "" || seen[typeRef] || builtins[typeRef] {
				continue
			}
			seen[typeRef] = true
			edges = append(edges, Edge{
				FromID: qualifiedName(pkgName, sourceName),
				ToID:   typeRef,
				Type:   EdgeUsesType,
			})
		}
	}

	return edges
}

func (c *Chunker) collectOrphans(
	root *sitter.Node,
	src []byte,
	filePath string,
	lang Language,
	fileCtx ChunkContext,
	coveredRanges []byteRange,
	result *Result,
) {
	for i := range int(root.NamedChildCount()) {
		child := root.NamedChild(i)
		// Skip package clause and import declarations.
		if child.Type() == "package_clause" || child.Type() == "import_declaration" {
			continue
		}

		start := child.StartByte()
		end := child.EndByte()
		covered := false
		for _, r := range coveredRanges {
			if start >= r.start && end <= r.end {
				covered = true
				break
			}
		}
		if covered {
			continue
		}

		content := child.Content(src)
		if estimateTokens(content) < 10 {
			continue // Skip trivially small orphans.
		}

		chunk := Chunk{
			Content:   content,
			FilePath:  filePath,
			Language:  lang,
			ChunkType: child.Type(), // raw tree-sitter type (e.g., "comment", "const_declaration")
			StartLine: int(child.StartPoint().Row) + 1,
			EndLine:   int(child.EndPoint().Row) + 1,
			StartByte: int(start),
			EndByte:   int(end),
			PathHash:  astPathHash(child),
		}
		if c.config.includeContext {
			chunk.Context = fileCtx
		}
		result.Chunks = append(result.Chunks, chunk)

		// Every chunk comes from a file by construction, so a chunk node with
		// no file containment is an emitter defect. This was the one of the
		// three chunk-emitting sites that appended to result.Chunks and never
		// to result.Edges, which left every orphan node an island — effectively
		// all of them in the languages that name no chunks at all.
		//
		// The slot is the ONLY addressing here: an orphan has no name, so
		// there is no qualified ToID to carry alongside it. 1-based, taken
		// AFTER the append, matching the other two emission sites. No Ref and
		// no Ref at all — containment is positional, never a reference.
		result.Edges = append(result.Edges, Edge{
			FromID:  filePath,
			Type:    EdgeContains,
			ToChunk: len(result.Chunks),
		})
	}
}

// extractLexicalName extracts the variable name from a lexical_declaration
// (const/let/var) node for TypeScript.
func extractLexicalName(node *sitter.Node, src []byte) string {
	for i := range int(node.NamedChildCount()) {
		child := node.NamedChild(i)
		if child.Type() == "variable_declarator" {
			nameNode := child.ChildByFieldName("name")
			if nameNode != nil {
				return nameNode.Content(src)
			}
		}
	}
	return ""
}

// qualifiedName builds a package-qualified symbol name.
func qualifiedName(pkgName, name string) string {
	if pkgName == "" || name == "" {
		return name
	}
	return pkgName + "." + name
}

// estimateTokens provides a rough token count for a source code string.
// Code has shorter identifiers and more syntax tokens than prose,
// so we use ~3 chars per token (conservative to avoid under-splitting).
func estimateTokens(s string) int {
	return len(s) / 3
}
