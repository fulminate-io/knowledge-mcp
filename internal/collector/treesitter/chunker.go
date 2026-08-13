// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"

	sitter "github.com/smacker/go-tree-sitter"
)

// compiledQuerySet holds pre-compiled sitter.Query objects for a language.
// Compiled once per language and reused across all files.
type compiledQuerySet struct {
	topLevel   *sitter.Query
	calls      *sitter.Query
	imports    *sitter.Query
	typeRefs   *sitter.Query
	testBlocks *sitter.Query
}

func (cqs *compiledQuerySet) Close() {
	if cqs.topLevel != nil {
		cqs.topLevel.Close()
	}
	if cqs.calls != nil {
		cqs.calls.Close()
	}
	if cqs.imports != nil {
		cqs.imports.Close()
	}
	if cqs.typeRefs != nil {
		cqs.typeRefs.Close()
	}
	if cqs.testBlocks != nil {
		cqs.testBlocks.Close()
	}
}

// Chunker extracts semantic chunks and graph edges from source files.
type Chunker struct {
	parser   *Parser
	config   *chunkerConfig
	compiled map[Language]*compiledQuerySet
}

// NewChunker creates a Chunker with default configuration.
func NewChunker() *Chunker {
	return &Chunker{
		parser:   NewParser(),
		config:   defaultConfig(),
		compiled: make(map[Language]*compiledQuerySet),
	}
}

// Close releases parser resources and compiled queries.
func (c *Chunker) Close() {
	c.parser.Close()
	for _, cqs := range c.compiled {
		cqs.Close()
	}
}

// getCompiledQueries returns cached compiled queries for a language,
// compiling them on first access.
func (c *Chunker) getCompiledQueries(lang Language) *compiledQuerySet {
	if cqs, ok := c.compiled[lang]; ok {
		return cqs
	}

	entry := registry[lang]
	qs := entry.Queries()
	cqs := &compiledQuerySet{}

	compileQuery := func(name, src string, dst **sitter.Query) {
		if src == "" {
			return
		}
		q, err := sitter.NewQuery([]byte(src), entry.lang)
		if err != nil {
			slog.Error("treesitter: query compile failed",
				"language", lang, "query", name, "error", err)
			return
		}
		*dst = q
	}
	compileQuery("TopLevel", qs.TopLevel, &cqs.topLevel)
	compileQuery("Calls", qs.Calls, &cqs.calls)
	compileQuery("Imports", qs.Imports, &cqs.imports)
	compileQuery("TypeRefs", qs.TypeRefs, &cqs.typeRefs)
	compileQuery("TestBlocks", qs.TestBlocks, &cqs.testBlocks)

	c.compiled[lang] = cqs
	return cqs
}

// ChunkFile parses a source file and returns chunks + edges.
// MaxChunkFileBytes is the largest source file the chunker will accept.
// Files above this threshold are skipped (returning a Result with no chunks
// or edges) rather than fed to tree-sitter. The C and C++ grammars have
// known pathological backtracking on multi-hundred-KB preprocessor-heavy
// files (the original trigger was redis/src/module.c at 632KB hanging
// ts_parser_parse_string for >10 minutes). 1MB is generous — most real
// source files are well under this threshold and the rare ones above it
// don't produce useful semantic chunks anyway.
const MaxChunkFileBytes = 1 << 20 // 1 MiB

func (c *Chunker) ChunkFile(ctx context.Context, filePath string, src []byte) (*Result, error) {
	lang := DetectLanguage(filePath)
	if lang == LangUnknown {
		return nil, fmt.Errorf("unsupported file type: %s", filePath)
	}

	if len(src) > MaxChunkFileBytes {
		return &Result{
			FilePath: filePath,
			Language: lang,
		}, nil
	}

	tree, err := c.parser.Parse(ctx, src, lang)
	if err != nil {
		return nil, err
	}
	// This swap MUST stay above `defer tree.Close()`: a deferred method call
	// fixes its receiver at the defer statement, so swapping below it would
	// close the discarded tree and leak the adopted one. Everything downstream
	// reads lang, so setting it here routes the query set and the Result.
	if alt := c.cppHeaderFallback(ctx, filePath, src, lang, tree); alt != nil {
		tree.Close()
		tree, lang = alt, LangCPP
	}
	defer tree.Close()

	cqs := c.getCompiledQueries(lang)

	result := &Result{
		FilePath: filePath,
		Language: lang,
	}

	fileCtx := c.extractFileContext(tree.RootNode(), src, filePath, lang, cqs)
	fileCtx.Frameworks = DetectFrameworks(lang, fileCtx.Imports)
	if extend, ok := frameworkExtenders[lang]; ok {
		fileCtx.Frameworks = extend(tree.RootNode(), src, filePath, fileCtx.Frameworks)
	}
	c.walkTopLevel(tree.RootNode(), src, filePath, lang, cqs, fileCtx, result)
	c.walkTestBlocks(tree.RootNode(), src, filePath, lang, cqs, fileCtx, result)

	return result, nil
}

// cppHeaderFallback re-parses an erroring C header under the cpp grammar and
// returns the alternate tree when it comes back clean, or nil when the C tree
// stands. A `.h` may legitimately be either language and extMap routes every
// one of them to C, so the extension is a guess that the parse confirms. The
// second parse is paid solely by a header whose C parse already produced an
// error tree — where today's chunks are garbage anyway; a clean C header pays
// one HasError read on an already-built tree. The caller owns closing the tree
// this returns, and a rejected alternate is closed here.
func (c *Chunker) cppHeaderFallback(
	ctx context.Context, filePath string, src []byte, lang Language, tree *sitter.Tree,
) *sitter.Tree {
	if lang != LangC || filepath.Ext(filePath) != ".h" || !tree.RootNode().HasError() {
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
	return alt
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

// walkTopLevel iterates top-level AST nodes, emitting chunks and edges.
//
// AST traversal requires nested type-switching across node kinds.
func (c *Chunker) walkTopLevel(
	root *sitter.Node,
	src []byte,
	filePath string,
	lang Language,
	cqs *compiledQuerySet,
	fileCtx ChunkContext,
	result *Result,
) {
	if cqs.topLevel == nil {
		return
	}

	// Pass 1: collect. Declarations are disambiguated before they are emitted,
	// because a chunk and its edges must carry the same name and only the
	// emission site still holds the AST position that tells two colliding
	// declarations apart. DeduplicateChunks renames colliding chunks after the
	// fact, but by then the edges are already identical strings to each other,
	// so no rename map can attribute an edge back to its chunk.
	pending, coveredRanges := collectTopLevelDecls(root, src, lang, cqs)

	// Unnamed declarations are excluded from the count: they carry no name to
	// collide on, their endpoints are already inert, and their stable naming is
	// a separate concern that must not gain a second astPathHash site here.
	counts := make(map[[2]string]int, len(pending))
	for _, p := range pending {
		if p.name != "" {
			counts[[2]string{p.parentName, p.name}]++
		}
	}

	// Pass 2: emit. The suffix is the same astPathHash value DeduplicateChunks
	// appends today, so node IDs are unchanged — what changes is that the
	// edges now carry the matching name.
	names := resolveCollisionNames(pending, counts)
	for i, p := range pending {
		c.emitDeclarationChunk(p.declNode, src, filePath, lang, fileCtx, p.chunkType, names.final[i], p.parentName, result)
		c.emitDeclarationEdges(p.declNode, src, filePath, lang, fileCtx, p.chunkType, names.final[i], p.parentName,
			names.parentEdgeName(p), names.typeRefAlias, cqs, result)
	}

	// Collect import edges.
	for _, imp := range fileCtx.Imports {
		result.Edges = append(result.Edges, Edge{
			FromID: filePath,
			ToID:   imp,
			Type:   EdgeImports,
		})
	}

	// Collect orphan nodes as Block chunks.
	c.collectOrphans(root, src, filePath, lang, fileCtx, coveredRanges, result)
}

// extractDeclAndName extracts the decl node and name from a query match.
func extractDeclAndName(m *sitter.QueryMatch, cqs *compiledQuerySet, src []byte) (*sitter.Node, string) {
	var declNode *sitter.Node
	var name string
	for _, cap := range m.Captures {
		capName := cqs.topLevel.CaptureNameForId(cap.Index)
		switch capName {
		case "decl":
			declNode = cap.Node
		case "name":
			name = cap.Node.Content(src)
		}
	}
	return declNode, name
}

// resolveChunkType returns the effective chunk type for a declaration node.
// For export_statement, it unwraps to the inner declaration type.
func resolveChunkType(declNode *sitter.Node) string {
	chunkType := declNode.Type()
	if chunkType != "export_statement" {
		return chunkType
	}
	for i := range int(declNode.NamedChildCount()) {
		inner := declNode.NamedChild(i)
		innerType := inner.Type()
		if innerType != "comment" && innerType != "decorator" {
			return innerType
		}
	}
	return chunkType
}

// emitDeclarationChunk adds the chunk to result.Chunks.
func (c *Chunker) emitDeclarationChunk(
	declNode *sitter.Node,
	src []byte,
	filePath string,
	lang Language,
	fileCtx ChunkContext,
	chunkType, name, parentName string,
	result *Result,
) {
	chunk := Chunk{
		Content:    declNode.Content(src),
		FilePath:   filePath,
		Language:   lang,
		ChunkType:  chunkType,
		Name:       name,
		StartLine:  int(declNode.StartPoint().Row) + 1,
		EndLine:    int(declNode.EndPoint().Row) + 1,
		StartByte:  int(declNode.StartByte()),
		EndByte:    int(declNode.EndByte()),
		Exported:   declNode.Type() == "export_statement",
		PathHash:   astPathHash(declNode),
		ParentName: parentName,
	}
	if c.config.includeContext {
		chunk.Context = fileCtx
	}
	if lang == LangGo && chunkType == "method_declaration" {
		chunk.Context.Signature = extractGoSignature(declNode, src)
	}
	if lang == LangGo && chunkType == "function_declaration" {
		chunk.Context.Signature = extractGoSignature(declNode, src)
	}
	// Bucket A test classification dispatch — per-language predicate decides
	// (IsTest, TestKind) on the declaration. Languages without a registered
	// classifier leave the chunk's defaults (IsTest=false, TestKind="").
	if classify, ok := testKindClassifiers[lang]; ok {
		isTest, kind := classify(declNode, src, chunkType, name, fileCtx, filePath)
		chunk.IsTest = isTest
		chunk.TestKind = kind
	}
	result.Chunks = append(result.Chunks, chunk)
}

// emitDeclarationEdges adds CONTAINS, CALLS, USES_TYPE, and EMBEDS edges to result.Edges.
func (c *Chunker) emitDeclarationEdges(
	declNode *sitter.Node,
	src []byte,
	filePath string,
	lang Language,
	fileCtx ChunkContext,
	chunkType, name, parentName, parentEdgeName string,
	typeRefAlias map[string]string,
	cqs *compiledQuerySet,
	result *Result,
) {
	// Compute the parent-qualified symbol name for edge IDs — "Receiver.Method"
	// for a Go method, "Class.member" elsewhere — to avoid collisions when
	// several parents in the same namespace share a member name. parentName is
	// the same value the chunk carries, so this string equals the symbolMap key
	// parser/populate builds from the chunk. Pass 2 above appends
	// "#"+astPathHash to a colliding declaration's OWN name, while a member's
	// parentName was captured in pass 1 and stays unsuffixed. When the collision
	// is on a container (two C++ blocks reopening one namespace, a Rust struct
	// beside its impls), the parent-to-member edge below therefore uses
	// parentEdgeName instead — the disambiguated name of the container that
	// lexically encloses this member — and a type reference naming that
	// container is repointed through typeRefAlias to whichever colliding
	// declaration is the type, or left alone when more than one candidate
	// survives. Both edges then name a key some chunk carries, while every
	// member's own node ID is unchanged: only the edges take the suffix.
	//
	// The name guard is load-bearing: many TopLevel patterns bind no @name, so
	// their chunks carry Name "" and qualifiedName already returns "" for them,
	// leaving the edge inert. Without the guard those endpoints would become a
	// non-empty "<ns>.<parent>." that enters resolution instead of being dropped.
	symbolName := name
	if name != "" && parentName != "" {
		symbolName = parentName + "." + name
	}

	// Emit CONTAINS edge (file → declaration).
	result.Edges = append(result.Edges, Edge{
		FromID: filePath,
		ToID:   qualifiedName(fileCtx.PackageName, symbolName),
		Type:   EdgeContains,
	})

	// Parent → member CONTAINS: a Go receiver type → its method, and a
	// class → its member in every other language.
	if name != "" && parentName != "" {
		result.Edges = append(result.Edges, Edge{
			FromID: qualifiedName(fileCtx.PackageName, parentEdgeName),
			ToID:   qualifiedName(fileCtx.PackageName, symbolName),
			Type:   EdgeContains,
		})
	}

	if name != "" {
		result.Edges = append(result.Edges, c.extractCallEdges(declNode, src, fileCtx.PackageName, symbolName, cqs)...)
		result.Edges = append(result.Edges,
			aliasTypeRefTargets(c.extractTypeRefEdges(declNode, src, fileCtx.PackageName, symbolName, cqs), typeRefAlias)...)
	}

	// For Go struct types: extract EMBEDS edges.
	if lang == LangGo && chunkType == "type_declaration" {
		embeds := extractGoEmbeds(declNode, src)
		for _, embedded := range embeds {
			result.Edges = append(result.Edges, Edge{
				FromID: qualifiedName(fileCtx.PackageName, symbolName),
				ToID:   embedded,
				Type:   EdgeEmbeds,
			})
		}
	}
}
