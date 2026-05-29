// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

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
	defer tree.Close()

	cqs := c.getCompiledQueries(lang)

	result := &Result{
		FilePath: filePath,
		Language: lang,
	}

	fileCtx := c.extractFileContext(tree.RootNode(), src, cqs)
	fileCtx.Frameworks = DetectFrameworks(lang, fileCtx.Imports)
	if extend, ok := frameworkExtenders[lang]; ok {
		fileCtx.Frameworks = extend(tree.RootNode(), src, filePath, fileCtx.Frameworks)
	}
	c.walkTopLevel(tree.RootNode(), src, filePath, lang, cqs, fileCtx, result)
	c.walkTestBlocks(tree.RootNode(), src, filePath, lang, cqs, fileCtx, result)

	return result, nil
}

// extractFileContext pulls imports and package name from the AST root.
func (c *Chunker) extractFileContext(root *sitter.Node, src []byte, cqs *compiledQuerySet) ChunkContext {
	ctx := ChunkContext{}

	// Extract imports.
	if cqs.imports != nil {
		qc := sitter.NewQueryCursor()
		defer qc.Close()
		qc.Exec(cqs.imports, root)
		for {
			m, ok := qc.NextMatch()
			if !ok {
				break
			}
			m = filterPredicates(cqs.imports, m, src)
			for _, cap := range m.Captures {
				importPath := cap.Node.Content(src)
				// Strip quotes from Go import paths.
				importPath = strings.Trim(importPath, "\"'`")
				ctx.Imports = append(ctx.Imports, importPath)
			}
		}
	}

	// Extract Go package name.
	for i := range int(root.ChildCount()) {
		child := root.Child(i)
		if child.Type() == "package_clause" {
			for j := range int(child.ChildCount()) {
				gc := child.Child(j)
				if gc.Type() == "package_identifier" {
					ctx.PackageName = gc.Content(src)
					break
				}
			}
			break
		}
	}

	return ctx
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

	qc := sitter.NewQueryCursor()
	defer qc.Close()
	qc.Exec(cqs.topLevel, root)

	// Track byte ranges covered by declarations to find orphan nodes.
	var coveredRanges []byteRange

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

		c.emitDeclarationChunk(declNode, src, filePath, lang, fileCtx, chunkType, name, result)
		c.emitDeclarationEdges(declNode, src, filePath, lang, fileCtx, chunkType, name, cqs, result)
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
	chunkType, name string,
	result *Result,
) {
	chunk := Chunk{
		Content:   declNode.Content(src),
		FilePath:  filePath,
		Language:  lang,
		ChunkType: chunkType,
		Name:      name,
		StartLine: int(declNode.StartPoint().Row) + 1,
		EndLine:   int(declNode.EndPoint().Row) + 1,
		StartByte: int(declNode.StartByte()),
		EndByte:   int(declNode.EndByte()),
		Exported:  declNode.Type() == "export_statement",
		PathHash:  astPathHash(declNode),
	}
	if c.config.includeContext {
		chunk.Context = fileCtx
	}
	if lang == LangGo && chunkType == "method_declaration" {
		chunk.ParentName = extractGoReceiver(declNode, src)
		chunk.Context.Signature = extractGoSignature(declNode, src)
	}
	if lang == LangGo && chunkType == "function_declaration" {
		chunk.Context.Signature = extractGoSignature(declNode, src)
	}
	// For any declaration inside a function/method body, set ParentName
	// to the enclosing function so the node ID is unique (e.g., "func.varName").
	if chunk.ParentName == "" {
		if enclosing := findEnclosingFunction(declNode, src); enclosing != "" {
			chunk.ParentName = enclosing
		}
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
	chunkType, name string,
	cqs *compiledQuerySet,
	result *Result,
) {
	// Compute the receiver-qualified symbol name for edge IDs.
	// For methods, this is "Receiver.Method" (e.g., "db.Retrieve") to avoid
	// collisions when multiple types in the same package share a method name.
	symbolName := name
	var receiver string
	if lang == LangGo && chunkType == "method_declaration" {
		receiver = extractGoReceiver(declNode, src)
		if receiver != "" {
			symbolName = receiver + "." + name
		}
	}

	// Emit CONTAINS edge (file → declaration).
	result.Edges = append(result.Edges, Edge{
		FromID: filePath,
		ToID:   qualifiedName(fileCtx.PackageName, symbolName),
		Type:   EdgeContains,
	})

	// For Go methods: emit CONTAINS (type → method).
	if receiver != "" {
		result.Edges = append(result.Edges, Edge{
			FromID: qualifiedName(fileCtx.PackageName, receiver),
			ToID:   qualifiedName(fileCtx.PackageName, symbolName),
			Type:   EdgeContains,
		})
	}

	result.Edges = append(result.Edges, c.extractCallEdges(declNode, src, fileCtx.PackageName, symbolName, cqs)...)
	result.Edges = append(result.Edges, c.extractTypeRefEdges(declNode, src, fileCtx.PackageName, symbolName, cqs)...)

	// For Go struct types: extract EMBEDS edges.
	if lang == LangGo && chunkType == "type_declaration" {
		embeds := extractGoEmbeds(declNode, src)
		for _, embedded := range embeds {
			result.Edges = append(result.Edges, Edge{
				FromID: qualifiedName(fileCtx.PackageName, name),
				ToID:   embedded,
				Type:   EdgeEmbeds,
			})
		}
	}
}
