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

// testBlockSite is one TestBlocks match that SURVIVED the strict-positive
// classifier gate, collected before walkTopLevel runs so its byte ranges are
// available to declaration edge emission and to orphan collection.
//
// A rejected match is absent from this list entirely, and that absence is
// load-bearing rather than tidy: a rejected match produces no test_block chunk,
// so contributing its range would suppress the orphan twin of a node that was
// never created and delete the only chunk covering that source.
type testBlockSite struct {
	declNode *sitter.Node
	captures testBlockCaptures
	// testKind is the classifier's verdict, carried so emitTestBlockChunk does
	// not classify a second time. isTest is false — with testKind empty — for a
	// language that registers no classifier, preserving the pre-Bucket-B shape.
	isTest   bool
	testKind TestKind

	// declRange is the @decl node's own extent: what "inside this test block"
	// means for the leaf rule and for the leaked-declaration migration.
	declRange byteRange
	// coverRange is what the site contributes to coveredRanges, and it is NOT
	// declRange. collectOrphans tests whole ROOT-LEVEL CHILDREN for containment
	// (chunker_edges.go:229-235), and the @decl of a JS/TS test block is the
	// call_expression while the root child is the expression_statement wrapping
	// it — one byte longer whenever the source terminates the call with a
	// semicolon. Contributing declRange would leave `end <= r.end` false and
	// suppress nothing at all, silently. See testBlockCoverRange.
	coverRange byteRange
	// leaf is true when NO other surviving site is strictly contained in this
	// one. Only leaves emit TEST_CALLS: a describe does not call what its its
	// call, and emitting the inner call from every enclosing level would
	// multiply a callee's inbound count by the nesting depth of whichever test
	// happened to exercise it.
	leaf bool
}

// collectTestBlockSites runs the TestBlocks query and returns every match that
// passes the classifier, with the two ranges and the leaf flag resolved.
//
// IT RUNS BEFORE walkTopLevel — see ChunkFile — because both consumers of these
// ranges live INSIDE walkTopLevel: the emitDeclarationEdges loop tests leak
// containment, and collectOrphans, which runs after it, tests twin suppression.
// One collection serves both; the query executes exactly once per file and
// walkTestBlocks emits from this same list rather than re-running it.
func (c *Chunker) collectTestBlockSites(
	root *sitter.Node,
	src []byte,
	filePath string,
	lang Language,
	cqs *compiledQuerySet,
	fileCtx ChunkContext,
) []testBlockSite {
	if cqs.testBlocks == nil {
		return nil
	}

	qc := sitter.NewQueryCursor()
	defer qc.Close()
	qc.Exec(cqs.testBlocks, root)

	var sites []testBlockSite
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

		site := testBlockSite{
			declNode:   declNode,
			captures:   captures,
			declRange:  byteRange{start: declNode.StartByte(), end: declNode.EndByte()},
			coverRange: testBlockCoverRange(declNode),
		}
		// Bucket B dispatch (locked Q1, Q9). The strict-positive gate DROPS a
		// match the classifier rejects, here rather than at emission, so a
		// dropped match contributes no range and leaves no chunk.
		if classify, ok := testBlockClassifiers[lang]; ok {
			isTest, kind := classify(declNode, src, captures, fileCtx, filePath)
			if !isTest {
				continue
			}
			site.isTest, site.testKind = isTest, kind
		}
		sites = append(sites, site)
	}

	markLeafTestBlocks(sites)
	return sites
}

// walkTestBlocks emits one test_block chunk per surviving site, its file
// CONTAINS edge, and — for LEAF sites only — the TEST_CALLS edges its body's
// call sites produce.
//
// Strictly disjoint from walkTopLevel on the CHUNK side: leaf chunks only, one
// CONTAINS edge per chunk, no USES_TYPE and no EMBEDS. It is no longer
// edge-free: the body's calls now emit, through the SAME Calls query, the same
// refForParent/attachRefSite carriers and the same resolution ladder that a
// declaration's calls ride — only the edge TYPE differs.
//
// Emits nothing when sites is empty, which covers both the languages whose
// TestBlocks query string is empty and the files that contain no test block.
func (c *Chunker) walkTestBlocks(
	sites []testBlockSite,
	src []byte,
	filePath string,
	lang Language,
	cqs *compiledQuerySet,
	fileCtx ChunkContext,
	ref *RefSite,
	result *Result,
) {
	for i := range sites {
		site := &sites[i]

		slot := c.emitTestBlockChunk(site, src, filePath, lang, fileCtx, result)

		// Qualify by the same parent the chunk carries, under the same name
		// guard the declaration path uses: the declaration index keys a
		// test_block as <ns>.<ParentName>.<Name> whenever @parent_name bound, so
		// an unqualified endpoint here would fail resolution for exactly those.
		symbolName := site.captures.Name
		if site.captures.Name != "" && site.captures.ParentName != "" {
			symbolName = site.captures.ParentName + "." + site.captures.Name
		}
		// ToChunk names the chunk emitTestBlockChunk just appended, making the
		// target exact so the pre-pass can overwrite the qualified name. That is
		// what closes the unnamed-test-block case: a block whose query bound no
		// @name and whose body offered no string literal produced an empty ToID,
		// which resolution dropped, leaving the chunk node contained by nothing.
		result.Edges = append(result.Edges, Edge{
			FromID:  filePath,
			ToID:    qualifiedName(fileCtx.PackageName, symbolName),
			Type:    EdgeContains,
			ToChunk: slot,
		})

		c.emitTestBlockCallEdges(site, src, fileCtx, symbolName, cqs, slot, ref, result)
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
// returns its 1-BASED slot in that slice. The 1-based encoding matches
// Edge.FromChunk/ToChunk, where 0 means unset.
// Mirrors emitDeclarationChunk's shape but diverges on three contracts:
//
//   - ChunkType is the literal string "test_block" (NOT declNode.Type()).
//   - Exported is always false (locked decision Q9).
//   - ParentName comes from the @parent_name capture only — no automatic
//     AST ascent via findEnclosingScope. This is by design: the capture
//     surfaces describe→it nesting deterministically and language-specific
//     scoping rules (RSpec contexts, Lua busted blocks) don't map cleanly
//     onto the scope-ascent helper.
//
// THE RETURN IS ALWAYS NON-ZERO. The strict-positive gate (locked Q1, Q9) has
// already run in collectTestBlockSites, which is where a classifier's
// (false, TestKindNone) drops the match — so every site reaching here is one
// the classifier accepted, or one whose language registers no classifier at
// all and therefore carries IsTest=false TestKind="" as it always has.
//
// Context.Signature carries the @params text verbatim.
func (c *Chunker) emitTestBlockChunk(
	site *testBlockSite,
	src []byte,
	filePath string,
	lang Language,
	fileCtx ChunkContext,
	result *Result,
) int {
	chunk := Chunk{
		Content:    site.declNode.Content(src),
		FilePath:   filePath,
		Language:   lang,
		ChunkType:  "test_block",
		Name:       site.captures.Name,
		StartLine:  int(site.declNode.StartPoint().Row) + 1,
		EndLine:    int(site.declNode.EndPoint().Row) + 1,
		StartByte:  int(site.declRange.start),
		EndByte:    int(site.declRange.end),
		Exported:   false,
		PathHash:   astPathHash(site.declNode),
		ParentName: site.captures.ParentName,
		IsTest:     site.isTest,
		TestKind:   site.testKind,
	}
	if c.config.includeContext {
		chunk.Context = fileCtx
	}
	chunk.Context.Signature = site.captures.Params

	result.Chunks = append(result.Chunks, chunk)
	// 1-based: len AFTER the append.
	return len(result.Chunks)
}
