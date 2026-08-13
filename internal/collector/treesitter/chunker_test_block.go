// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	sitter "github.com/smacker/go-tree-sitter"
)

// testBlockCaptures groups the optional captures pulled from a TestBlocks
// query match. Bucket B per-language queries declare these capture names;
// missing captures zero-value to the empty string and the chunker propagates
// the absence (no synthetic AST ascent, no normalization).
type testBlockCaptures struct {
	// Name is the string-literal label captured as @name (e.g., "rejects expired").
	// Falls back to firstStringArg over the @decl call when @name is absent.
	Name string
	// ParentName is the outer describe/context name captured as @parent_name.
	// Empty when the query did not bind @parent_name for this match.
	// Assigned verbatim to chunk.ParentName — no AST ascent.
	ParentName string
	// Params is the closure parameter list text captured as @params (e.g., "(done)").
	// Empty when @params is absent. Assigned verbatim to chunk.Context.Signature.
	Params string
}

// walkTestBlocks runs the TestBlocks query and emits one test_block chunk per
// match. Strictly disjoint from walkTopLevel — leaf chunks only, one CONTAINS
// edge per chunk, no CALLS / USES_TYPE / EMBEDS edges, no orphan tracking.
//
// Returns immediately when cqs.testBlocks is nil (the per-language TestBlocks
// query string was empty), mirroring the topLevel == nil guard at chunker.go:202.
func (c *Chunker) walkTestBlocks(
	root *sitter.Node,
	src []byte,
	filePath string,
	lang Language,
	cqs *compiledQuerySet,
	fileCtx ChunkContext,
	result *Result,
) {
	if cqs.testBlocks == nil {
		return
	}

	qc := sitter.NewQueryCursor()
	defer qc.Close()
	qc.Exec(cqs.testBlocks, root)

	for {
		m, ok := qc.NextMatch()
		if !ok {
			break
		}
		m = filterPredicates(cqs.testBlocks, m, src)

		declNode, captures := extractTestBlockCaptures(m, cqs, src)
		if declNode == nil {
			continue
		}
		// Fall back to first string-literal argument when @name was not
		// captured by the query — handles the common case where the query
		// author bound @decl on the outer call without an explicit @name.
		if captures.Name == "" {
			captures.Name = firstStringArg(declNode, src)
		}

		if !c.emitTestBlockChunk(declNode, src, filePath, lang, fileCtx, captures, result) {
			// Strict-positive gate (locked Q9): predicate returned (false,
			// TestKindNone). Skip CONTAINS edge so dropped chunks don't leave
			// orphan edges referencing a non-emitted node.
			continue
		}

		// Qualify by the same parent the chunk carries, under the same name
		// guard the declaration path uses: recordSymbol keys a test_block as
		// <ns>.<ParentName>.<Name> whenever @parent_name bound, so an
		// unqualified endpoint here would fail resolution for exactly those.
		symbolName := captures.Name
		if captures.Name != "" && captures.ParentName != "" {
			symbolName = captures.ParentName + "." + captures.Name
		}
		result.Edges = append(result.Edges, Edge{
			FromID: filePath,
			ToID:   qualifiedName(fileCtx.PackageName, symbolName),
			Type:   EdgeContains,
		})
	}
}

// extractTestBlockCaptures pulls the @decl, @name, @parent_name, and @params
// captures from a TestBlocks query match. Mirrors extractDeclAndName at
// chunker.go:262 but recognizes four capture names and reads from
// cqs.testBlocks.CaptureNameForId.
//
// Each of @name, @parent_name, @params is optional — when a capture isn't
// present in the match, the corresponding struct field is the empty string.
func extractTestBlockCaptures(m *sitter.QueryMatch, cqs *compiledQuerySet, src []byte) (*sitter.Node, testBlockCaptures) {
	var declNode *sitter.Node
	var captures testBlockCaptures
	for _, cap := range m.Captures {
		capName := cqs.testBlocks.CaptureNameForId(cap.Index)
		switch capName {
		case "decl":
			declNode = cap.Node
		case "name":
			s := cap.Node.Content(src)
			// Strip outer quotes from string-literal captures so the chunk
			// Name matches the firstStringArg fallback shape (which also
			// strips quotes via chunker_identity.go:147-150).
			if len(s) >= 2 && (s[0] == '"' || s[0] == '\'' || s[0] == '`') {
				s = s[1 : len(s)-1]
			}
			captures.Name = s
		case "parent_name":
			s := cap.Node.Content(src)
			if len(s) >= 2 && (s[0] == '"' || s[0] == '\'' || s[0] == '`') {
				s = s[1 : len(s)-1]
			}
			captures.ParentName = s
		case "params":
			captures.Params = cap.Node.Content(src)
		}
	}
	return declNode, captures
}

// emitTestBlockChunk appends a single test_block chunk to result.Chunks and
// returns true. Mirrors emitDeclarationChunk's shape but diverges on three
// contracts:
//
//   - ChunkType is the literal string "test_block" (NOT declNode.Type()).
//   - Exported is always false (locked decision Q9).
//   - ParentName comes from the @parent_name capture only — no automatic
//     AST ascent via findEnclosingScope. This is by design: the capture
//     surfaces describe→it nesting deterministically and language-specific
//     scoping rules (RSpec contexts, Lua busted blocks) don't map cleanly
//     onto the scope-ascent helper.
//
// Bucket B dispatch (locked Q1, Q9): when a testBlockClassifiers entry exists
// for the language, call it; if it returns (false, TestKindNone), the strict-
// positive gate DROPS the chunk and emitTestBlockChunk returns false so the
// caller can skip the parallel CONTAINS edge append. When no classifier is
// registered, the chunk is appended with IsTest=false TestKind="" preserving
// the pre-Bucket-B behavior (transient state during phased rollout).
//
// Context.Signature carries the @params text verbatim.
func (c *Chunker) emitTestBlockChunk(
	declNode *sitter.Node,
	src []byte,
	filePath string,
	lang Language,
	fileCtx ChunkContext,
	captures testBlockCaptures,
	result *Result,
) bool {
	chunk := Chunk{
		Content:    declNode.Content(src),
		FilePath:   filePath,
		Language:   lang,
		ChunkType:  "test_block",
		Name:       captures.Name,
		StartLine:  int(declNode.StartPoint().Row) + 1,
		EndLine:    int(declNode.EndPoint().Row) + 1,
		StartByte:  int(declNode.StartByte()),
		EndByte:    int(declNode.EndByte()),
		Exported:   false,
		PathHash:   astPathHash(declNode),
		ParentName: captures.ParentName,
	}
	if c.config.includeContext {
		chunk.Context = fileCtx
	}
	chunk.Context.Signature = captures.Params

	if classify, ok := testBlockClassifiers[lang]; ok {
		isTest, kind := classify(declNode, src, captures, fileCtx, filePath)
		if !isTest {
			// Strict-positive gate (locked Q9): drop entirely. The caller
			// guards the CONTAINS edge on this return value.
			return false
		}
		chunk.IsTest = isTest
		chunk.TestKind = kind
	}

	result.Chunks = append(result.Chunks, chunk)
	return true
}
