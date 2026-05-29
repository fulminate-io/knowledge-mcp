// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	sitter "github.com/smacker/go-tree-sitter"
)

// byteRange represents a byte offset range in source code.
type byteRange struct {
	start, end uint32
}

// extractCallEdges finds all function calls within a node and emits one CALLS
// edge per unique callee. The Edge.Weight carries the number of call sites
// inside this caller — used by weighted PageRank to give heavily-called
// helpers a stronger gravity well.
func (c *Chunker) extractCallEdges(node *sitter.Node, src []byte, pkgName, sourceName string, cqs *compiledQuerySet) []Edge {
	if cqs.calls == nil {
		return nil
	}

	qc := sitter.NewQueryCursor()
	defer qc.Close()
	qc.Exec(cqs.calls, node)

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
		for _, cap := range m.Captures {
			callee := cap.Node.Content(src)
			if callee == "" {
				continue
			}
			if _, ok := counts[callee]; !ok {
				order = append(order, callee)
			}
			counts[callee]++
		}
	}

	if len(order) == 0 {
		return nil
	}
	from := qualifiedName(pkgName, sourceName)
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
