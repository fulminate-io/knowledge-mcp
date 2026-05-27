// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// PopulateResult is the client-side hand-off type between the parser
// collector and the codesync reindex path. It carries the typed wire node
// (*knowledgev1.Node) the client builds directly — the FUL-295 T5 retype
// dropped the store.Node wrapper from the client build path, so this type
// supersedes the former store.PopulateResult. Pointer node/edge elements:
// knowledgev1.Node/Edge each value-embed a protoimpl noCopy, so a value
// slice would make the builder appends + range loops copylocks violations.
type PopulateResult struct {
	Nodes   []*knowledgev1.Node
	Edges   []*knowledgev1.Edge
	Entries []NodeEntry

	// SymbolMap maps "pkg.Symbol" → nodeID for edge resolution.
	// Used by single-file updates where cross-file resolution requires the graph.
	// For full-reindex populates, edges are pre-resolved and this is nil.
	SymbolMap map[string]string
}

// NodeEntry tracks a node and its source chunk content for summarization.
// The client-side counterpart of the former store.NodeEntry (the only
// consumer was this parser package).
type NodeEntry struct {
	NodeID  string
	Content string
}

// Populate discovers and chunks all source files in repoDir, returning a
// PopulateResult with fully-resolved edges ready for the generic reindex
// pipeline.
//
// repoName is the canonical graph identifier (typically the basename of
// repoDir, but callers may override for monorepo / branch-overlay cases).
// It is used to construct deterministic NodeLanguage IDs (lang:<repoName>:<lang>)
// so language-hub nodes collide across full-reindex / partial-reindex of
// the SAME repo but never across different repos.
func Populate(ctx context.Context, repoName, repoDir string) (PopulateResult, error) {
	files, err := DiscoverFiles(ctx, repoDir)
	if err != nil {
		return PopulateResult{}, err
	}
	results, err := ChunkFilesParallel(ctx, repoDir, files)
	if err != nil {
		return PopulateResult{}, err
	}
	return chunkResultsToPopulate(repoName, repoDir, results), nil
}

// PopulateForExternalGraph runs the parser collector flow (Populate) and
// returns nodes + BatchEdges with every ID, FilePath, and edge endpoint
// prefixed with repoName + "/" so multiple repos can be materialized into
// the SAME non-code graph (e.g. web/<source-slug>) without ID collision.
//
// This is the entry point used by the web collector's github materializer.
// It produces a flat batch of nodes/edges only — it does NOT call
// codesync.Sync, RunSummarize, or RunEmbed. Callers route the returned
// slices into their own CollectResult / write path.
//
// Namespacing invariant: every Node.ID in the returned slice begins with
// repoName + "/", every non-empty Node.FilePath begins with repoName + "/",
// every Edge FromID/ToID begins with repoName + "/", and every Entries
// NodeID matches a namespaced Node.ID. This guarantees that two repos
// "owner/foo@main" and "owner/bar@main" both materializing a README.md
// produce distinct node IDs ("owner/foo@main/README.md" vs
// "owner/bar@main/README.md").
//
// The conversion from knowledgev1.Edge to kgwire.BatchEdge mirrors
// codesync/sync.go:toBatchEdges field-for-field; the materializer never
// sees a knowledgev1.Edge.
func PopulateForExternalGraph(ctx context.Context, repoName, repoDir string) ([]*knowledgev1.Node, []kgwire.BatchEdge, error) {
	pop, err := Populate(ctx, repoName, repoDir)
	if err != nil {
		return nil, nil, err
	}

	prefix := repoName + "/"

	// Namespace every Node.ID and Node.FilePath. pop.Nodes elements are
	// *knowledgev1.Node, so the index assignment writes through the pointer.
	for _, n := range pop.Nodes {
		n.Id = prefix + n.Id
		if n.FilePath != "" {
			n.FilePath = prefix + n.FilePath
		}
	}

	// Namespace every Entries[].NodeID so node-content storage matches
	// the namespaced node IDs.
	for i := range pop.Entries {
		pop.Entries[i].NodeID = prefix + pop.Entries[i].NodeID
	}

	// Namespace every edge endpoint and convert to BatchEdge in one pass.
	// Conversion body mirrors codesync/sync.go:toBatchEdges field-for-field.
	batchEdges := make([]kgwire.BatchEdge, len(pop.Edges))
	for i, e := range pop.Edges {
		batchEdges[i] = kgwire.BatchEdge{
			FromIdx: -1,
			ToIdx:   -1,
			FromID:  prefix + e.FromId,
			ToID:    prefix + e.ToId,
			Type:    kgtypes.EdgeType(e.Type),
			Weight:  e.Weight,
		}
	}

	return pop.Nodes, batchEdges, nil
}

// chunkResultsToPopulate converts treesitter chunk results into a
// PopulateResult with fully-resolved edges. repoName is threaded through
// so per-language hub nodes (NodeLanguage) get deterministic, per-repo
// IDs of the form lang:<repoName>:<language>.
//
// Side effects per chunk result:
//   - one NodeFile per result.FilePath
//   - one NodeLanguage per distinct language seen across all results
//     (deduplicated via langNodes map; ID lang:<repoName>:<lang>)
//   - one EdgeLanguage per non-comment chunk → its language hub node
//
// File→symbol membership is handled by the existing CONTAINS edges
// emitted by treesitter/chunker.go and codegraph/hierarchy.go; we do
// NOT emit a duplicate symbol→file "defined-in" edge.
func chunkResultsToPopulate(repoName, repoDir string, results []*treesitter.Result) PopulateResult {
	DeduplicateChunks(results)

	symbolMap := make(map[string]string)
	nodeIDs := make(map[string]bool)
	langNodes := make(map[string]string) // language → lang_node ID
	var nodes []*knowledgev1.Node
	var allEdges []*knowledgev1.Edge
	var entries []NodeEntry

	for _, result := range results {
		nodeIDs[result.FilePath] = true
		// FUL-241 Phase 7: populate Content on Dockerfile NodeFile
		// entries so pkg/linker/dockerfile.go can resolve BUILDS edges
		// without reading the filesystem server-side. Other languages
		// keep empty Content (chunk-level Content fields hold the body).
		var fileContent string
		if result.Language == treesitter.LangDockerfile && repoDir != "" {
			if body, err := os.ReadFile(filepath.Join(repoDir, result.FilePath)); err == nil {
				fileContent = string(body)
			}
		}
		// Append a pointer to a fresh proto node (knowledgev1.Node value-embeds
		// a noCopy proto, so the slice carries pointers, not values).
		nodes = append(nodes, &knowledgev1.Node{
			Id:       result.FilePath,
			Type:     string(kgtypes.NodeFile),
			FilePath: result.FilePath,
			Language: string(result.Language),
			Content:  fileContent,
		})

		// pendingComments buffers preceding comment-chunk text within a file.
		// It drains into the next non-comment chunk's Description so a query
		// matching documentation text scores the documented symbol, not a
		// standalone comment node. Comment chunks are dropped from the graph
		// entirely (no node, no edges) — any tree-sitter edge pointing at a
		// comment ID fails resolution in resolveEdges and is filtered out.
		//
		// Sort chunks into document order before the fold. The chunker emits
		// declarations from the TopLevel query loop FIRST and orphans (incl.
		// comments) AFTER (treesitter/chunker.go:243 then :257), so the raw
		// slice order is `[decl, decl, ..., comment, comment]` rather than
		// the source-file order `[comment, decl, comment, decl, ...]` that
		// the fold below expects. Without this sort, every declaration is
		// processed before any comment chunk is seen and Description stays
		// empty for every code node.
		sort.SliceStable(result.Chunks, func(i, j int) bool {
			return result.Chunks[i].StartByte < result.Chunks[j].StartByte
		})

		var pendingComments []string

		for _, chunk := range result.Chunks {
			if kgtypes.NodeType(chunk.ChunkType).IsComment() {
				if text := stripCommentMarkers(chunk.Content); text != "" {
					pendingComments = append(pendingComments, text)
				}
				continue
			}

			// Fold any buffered preceding comments into the node Description.
			// appendChunkNode sets no Description of its own, so the fold owns
			// the field entirely.
			var description string
			if len(pendingComments) > 0 {
				description = strings.Join(pendingComments, "\n")
				pendingComments = nil
			}
			nodeID := appendChunkNode(chunk, description, &nodes)
			nodeIDs[nodeID] = true
			entries = append(entries, NodeEntry{NodeID: nodeID, Content: chunk.Content})
			recordSymbol(symbolMap, chunk, nodeID)
			if chunk.Language != "" {
				langID := ensureLangNode(repoName, string(chunk.Language), langNodes, &nodes)
				nodeIDs[langID] = true
				allEdges = append(allEdges, &knowledgev1.Edge{
					FromId: nodeID,
					ToId:   langID,
					Type:   string(kgtypes.EdgeLanguage),
				})
			}
		}

		allEdges = append(allEdges, ConvertEdges(result.Edges)...)
	}

	resolvedEdges := resolveEdges(allEdges, symbolMap, nodeIDs)
	return PopulateResult{
		Nodes:   nodes,
		Edges:   resolvedEdges,
		Entries: entries,
	}
}

// appendChunkNode builds a *knowledgev1.Node from one tree-sitter chunk, appends it
// as a fresh literal to *nodes, and returns the deterministic node ID. The
// caller-supplied description (folded preceding comments) populates the node's
// Description. IsExported is promoted from chunk.Exported into a denormalized
// struct field; the legacy "exported" metadata key is no longer written.
// Comment chunks never reach this function — chunkResultsToPopulate filters
// them out and folds their text into the following symbol's Description.
//
// It appends a pointer to a fresh proto node: knowledgev1.Node value-embeds a
// noCopy proto, so the slice carries *knowledgev1.Node pointers rather than
// values.
func appendChunkNode(chunk treesitter.Chunk, description string, nodes *[]*knowledgev1.Node) string {
	nodeID := ChunkNodeID(chunk)
	nodeType := kgtypes.NodeType(chunk.ChunkType)
	*nodes = append(*nodes, &knowledgev1.Node{
		Id:          nodeID,
		Type:        string(nodeType),
		SymbolName:  chunk.Name,
		FilePath:    chunk.FilePath,
		Language:    string(chunk.Language),
		StartLine:   int32(chunk.StartLine),
		EndLine:     int32(chunk.EndLine),
		Content:     chunk.Content,
		Signature:   chunk.Context.Signature,
		Description: description,
		IsExported:  chunk.Exported,
		IsTest:      chunk.IsTest,
		TestKind:    string(chunk.TestKind),
	})
	return nodeID
}

// stripCommentMarkers removes leading comment-syntax characters (//, /*, #)
// and surrounding whitespace from a comment chunk's raw content. Mirrors
// the prior comment-summary logic that lived in chunkToNode before
// comments became symbol-attached documentation rather than standalone
// indexed nodes.
func stripCommentMarkers(content string) string {
	return strings.TrimSpace(strings.TrimLeft(content, "/*# "))
}

// recordSymbol updates the symbolMap with the package-qualified key for
// later edge resolution. No-op when the chunk lacks a name or package.
func recordSymbol(symbolMap map[string]string, chunk treesitter.Chunk, nodeID string) {
	if chunk.Name == "" || chunk.Context.PackageName == "" {
		return
	}
	key := chunk.Context.PackageName + "." + chunk.Name
	if chunk.ParentName != "" {
		key = chunk.Context.PackageName + "." + chunk.ParentName + "." + chunk.Name
	}
	symbolMap[key] = nodeID
}

// ensureLangNode returns the deterministic NodeLanguage ID for (repoName,
// language), creating and appending the node on first sight. The langNodes
// map ensures exactly one NodeLanguage per language per
// chunkResultsToPopulate invocation.
func ensureLangNode(repoName, language string, langNodes map[string]string, nodes *[]*knowledgev1.Node) string {
	if id, ok := langNodes[language]; ok {
		return id
	}
	id := "lang:" + repoName + ":" + language
	langNodes[language] = id
	*nodes = append(*nodes, &knowledgev1.Node{
		Id:         id,
		Type:       string(kgtypes.NodeLanguage),
		SymbolName: language,
		Language:   language,
	})
	return id
}
