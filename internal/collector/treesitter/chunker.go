// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"

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
	// Framework detection matches on SPECIFIERS alone — it asks which modules the
	// file depends on, a question no local name or site ordinal bears on — so the
	// sites are projected down to the same strings it always received.
	fileCtx.Frameworks = DetectFrameworks(lang, importSpecifiers(fileCtx.Imports))
	if extend, ok := frameworkExtenders[lang]; ok {
		fileCtx.Frameworks = extend(tree.RootNode(), src, filePath, fileCtx.Frameworks)
	}

	// One RefSite per file, shared by every reference edge the file emits: the
	// walks below assign this same pointer rather than constructing a site per
	// edge, so a file costs one struct and one Binds map however many
	// references it makes.
	//
	// Its Binds field is where the file's imports stop being discarded.
	// Context.Imports is captured at chunker_filecontext.go:19-37 and, before
	// this carrier existed, went no further than the chunk's embedding context
	// — nothing downstream could see what a file imported, so a reference could
	// only ever be matched by name. bindsFor returns nil until a language
	// registers a BindsResolver.
	ref := &RefSite{
		File:  filePath,
		Scope: ScopeID(filePath, lang, fileCtx.PackageName),
		Lang:  lang,
	}
	// ALLOCATE the map here, and only when this language has an arm, because
	// refForParent derives a parented site by VALUE: a later pass that assigned
	// a fresh map would update the file-level site alone and leave every
	// parented reference reading the nil header it copied. A map is a reference
	// type, so an allocation here is visible through every by-value copy and
	// the pass can fill it in place.
	if hasBindsResolver(lang) {
		ref.Binds = map[string]Bind{}
		ref.DotScopes = map[string]bool{}
	}
	result.Ref = ref

	// THE TEST-BLOCK COLLECTION IS HOISTED ABOVE walkTopLevel, and the landmark
	// that forces it is emitDeclarationEdges rather than the orphan pass. Both
	// consumers of these ranges live inside walkTopLevel: the leaked-declaration
	// migration tests containment INSIDE the emitDeclarationEdges loop, and twin
	// suppression needs them by the time collectOrphans runs — which is LATER. A
	// collection placed between the two would satisfy "before the orphan pass"
	// literally while every leak silently tested as not-leaked, a no-op with
	// nothing red at the seam. One collection above both serves both.
	//
	// The chunk EMISSION order is unchanged by the hoist: this pass only reads
	// the tree, and walkTestBlocks still appends its chunks after walkTopLevel's.
	testBlocks := c.collectTestBlockSites(tree.RootNode(), src, filePath, lang, cqs, fileCtx)

	c.walkTopLevel(tree.RootNode(), src, filePath, lang, cqs, fileCtx, ref, testBlocks, result)
	c.walkTestBlocks(testBlocks, src, filePath, lang, cqs, fileCtx, ref, result)

	return result, nil
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
	ref *RefSite,
	testBlocks []testBlockSite,
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

	// TWIN SUPPRESSION. Every top-level test block was chunked TWICE: once as a
	// test_block here, and once as an orphan expression_statement by
	// collectOrphans, because coveredRanges was built from TopLevel matches
	// alone and walkTestBlocks contributed nothing to it. Contributing the
	// test-block extents is what makes the orphan pass skip the duplicate.
	//
	// The twin is SUPPRESSED rather than emitted-and-deleted: containment edges
	// address chunks POSITIONALLY by 1-based slot (types.go:166-179), so
	// removing a chunk after the fact would renumber every slot above it and
	// silently repoint edges at the wrong declarations. A node never created
	// costs nothing to address.
	//
	// THE CONTRIBUTED EXTENT IS coverRange, NOT the @decl range — see
	// testBlockCoverRange for the one-byte reason.
	for i := range testBlocks {
		coveredRanges = append(coveredRanges, testBlocks[i].coverRange)
	}

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
	//
	// The emission is SPLIT into two sub-passes because containment is now
	// addressed by chunk slot rather than by name. A member's parent-to-member
	// edge needs its CONTAINER's slot, and tree-sitter yields matches in the
	// query's order rather than the source's, so the container is not
	// guaranteed to have been emitted yet when the member is reached. Emitting
	// every chunk first makes every slot available to every edge.
	names := resolveCollisionNames(pending, counts)
	slots := make(slotIndex, len(pending))
	for i, p := range pending {
		c.emitDeclarationChunk(p.declNode, src, filePath, lang, fileCtx, p.chunkType, names.final[i], p.parentName, result)
		// 1-based: the slot is len AFTER the append, so 0 stays available as
		// "unset" on an Edge literal that omits the field.
		slots[byteRange{start: p.declNode.StartByte(), end: p.declNode.EndByte()}] = len(result.Chunks)
	}
	for i, p := range pending {
		// LEAK MIGRATION. A declaration whose byte range falls inside a test
		// block's is test-origin, and its call edges have ALWAYS been emitted —
		// the ECMAScript (lexical_declaration) @decl pattern is unanchored, so
		// tree-sitter matches it at any depth and a binding inside an it() body
		// chunks as an ordinary declaration. Those edges were CALLS and are now
		// TEST_CALLS, so the graph does not end up with two classes of test edge
		// and only one of them labeled.
		//
		// THE RULE IS RANGE CONTAINMENT AND NOTHING ELSE — no name matching, no
		// path heuristic. Its boundary is honest and documented: a test-origin
		// declaration in one of the languages with no TestBlocks query has no
		// test_block range to sit inside, so it is not identifiable by this rule
		// and its edges stay CALLS.
		declRange := byteRange{start: p.declNode.StartByte(), end: p.declNode.EndByte()}
		testOrigin := testBlockRangeContains(testBlocks, declRange)
		c.emitDeclarationEdges(p.declNode, src, filePath, lang, fileCtx, p.chunkType, names.final[i], p.parentName,
			names.typeRefAlias, cqs, slots, ref, testOrigin, result)
	}

	// Collect import edges — ONE PER SITE, each carrying a per-site group key, and
	// deliberately NOT deduplicated. The ruling this loop used to wait on is in:
	// per-site edges, "because this also is a major code smell finding that
	// customers would want to search for".
	//
	// WHAT THE KEY FIXES. Two import constructs naming ONE target used to produce
	// two edges byte-identical in all seven hashed fields, because this loop set
	// only FromID, ToID and Type. The duplication is REAL AT THE SOURCE, not a
	// parser artifact: cmd/knowledge/internal/tools/tools_logs_traverse_test.go
	// imports one path plainly and again under an alias, and
	// scripts/criterion_hygiene_gates.py carries two from-imports of one module.
	// The per-file contribution hash folds one hash per emitted edge, while the
	// edges identity is UNIQUE over (from_id, to_id, type, COALESCE(evidence,'')),
	// so only one row could land — the client's aggregate covered a row the server
	// could not store, the file's hash could never agree, and the file re-uploaded
	// on every collect, forever. Stamping evidence makes the two sites two rows,
	// which is what lets both the storage and the hash agree that there are two.
	//
	// THE KEY IS POSITION-INDEPENDENT, sharing one scheme with the reference group
	// key and spelling the ORDINAL LAST: `import:<local>:<n>`. A line- or
	// offset-derived key would trade this defect for a worse one — every edit above
	// an import block would re-key every import below it, minting an orphan per
	// site — measured on a live graph for the reference key, which carried the same
	// defect. <local> is empty where the language has no import arm to know
	// it, yielding a doubled colon (`import::0`) that must NOT be collapsed: the
	// ordinal has to stay recoverable as the final colon-separated field.
	//
	// EXPECTED CHURN, so it is not read later as a regression: every import edge in
	// every language gains evidence, so every file with imports re-lands ONCE. That
	// is delivered by the contribution-hash scheme bump rather than by drift, and
	// the two residual files quiesce afterwards.
	importOrdinals := make(map[ImportSite]int)
	for _, imp := range fileCtx.Imports {
		// The ordinal is scoped to the FULL discriminator — specifier AND local —
		// never to the specifier alone. Under specifier-only scoping, SWAPPING two
		// import lines of one module renumbers both and mints two orphans, which is
		// the very defect class this format exists to avoid. Scoped to the whole
		// site, a swap is a no-op and the ordinal separates only sites that are
		// identical in every recorded respect: the Python case.
		n := importOrdinals[imp]
		importOrdinals[imp] = n + 1
		result.Edges = append(result.Edges, Edge{
			FromID:   filePath,
			ToID:     imp.Specifier,
			Type:     EdgeImports,
			Evidence: "import:" + imp.Local + ":" + strconv.Itoa(n),
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

// unwrapExportedDecl returns the declaration an export_statement wraps, or the
// node itself when it wraps none and for every other kind.
//
// IT IS SHARED RATHER THAN PRIVATE TO THE CHUNK-TYPE PATH because the NODE, not
// only its type string, is what the per-language arms descend. The TopLevel
// query's export arm binds @decl to the export_statement, so for
// `export class E implements I` the node handed to qualifierTypesFor and
// typeFactsFor is the export_statement — and any descent written against
// class_declaration finds nothing there. Most real TypeScript classes are
// exported, so an arm that skipped this unwrap would be silently inert on the
// majority of its own corpus while every fixture written against an unexported
// declaration still passed.
func unwrapExportedDecl(node *sitter.Node) *sitter.Node {
	if node == nil || node.Type() != "export_statement" {
		return node
	}
	for i := range int(node.NamedChildCount()) {
		inner := node.NamedChild(i)
		innerType := inner.Type()
		if innerType != "comment" && innerType != "decorator" {
			return inner
		}
	}
	return node
}

// resolveChunkType returns the effective chunk type for a declaration node.
// For export_statement, it unwraps to the inner declaration type.
func resolveChunkType(declNode *sitter.Node) string {
	return unwrapExportedDecl(declNode).Type()
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
	// Go declarations that HAVE a signature render one, and an interface's method
	// spec is one of them. extractGoSignature needs no new case for it: a spec has
	// no `body` field, and the no-body branch returns the whole node — which for a
	// spec is exactly its signature text. Without `method_elem` here every one of
	// these nodes rendered a BLANK signature in symbol listings while every
	// sibling declaration kind rendered one.
	if lang == LangGo {
		switch chunkType {
		case "function_declaration", "method_declaration", "method_elem":
			chunk.Context.Signature = extractGoSignature(declNode, src)
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
	// Type-facts dispatch — the per-language arm records the declaration's
	// declared result types and struct field types for the typed-qualifier
	// resolution rung. A language with no registered arm leaves the chunk's
	// zero value, which is a nil pointer and no allocation.
	chunk.TypeFacts = typeFactsFor(lang, declNode, chunkType, src)
	result.Chunks = append(result.Chunks, chunk)
}
