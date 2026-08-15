// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
)

// PopulateResult is the client-side hand-off type between the parser
// collector and the codesync reindex path. It carries the typed wire node
// (*knowledgev1.Node) the client builds directly — the T5 retype
// dropped the store.Node wrapper from the client build path, so this type
// supersedes the former store.PopulateResult. Pointer node/edge elements:
// knowledgev1.Node/Edge each value-embed a protoimpl noCopy, so a value
// slice would make the builder appends + range loops copylocks violations.
type PopulateResult struct {
	Nodes   []*knowledgev1.Node
	Edges   []*knowledgev1.Edge
	Entries []NodeEntry
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
	// The repo context is already in hand here — Populate holds the root and
	// the discovered file list — so nothing has to be plumbed from further
	// away. The DISCOVERED file set is preferred over one derived from the
	// results: a file that produced no chunks still has a scope.
	//
	// THE ERROR IS DISCARDED DELIBERATELY, exactly as the collector discards
	// it: a repo with no go.mod is the normal non-Go case, not a failure. An
	// empty module path makes a language arm that consumes it return its zero
	// result — the same value the seam returns for a language with no arm
	// registered at all, so nothing downstream can tell the two apart.
	mp, _ := ReadModulePath(repoDir)
	rc := &treesitter.RepoContext{
		Root:       repoDir,
		ModulePath: mp,
		Files:      files,
	}
	return chunkResultsToPopulate(repoName, rc, results), nil
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
// The conversion from knowledgev1.Edge to kgwire.BatchEdge is delegated to
// ToBatchEdges, which both callers share; the materializer never sees a
// knowledgev1.Edge.
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
	return pop.Nodes, ToBatchEdges(pop.Edges, prefix), nil
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
// THE RepoContext IS TAKEN BY POINTER, not by value. It carries the per-collect
// derivation caches two binds arms fill lazily — the rust crate anchors and the
// JVM source-root set — so a copy would fork those caches, and the sync
// primitives guarding them make a copy a vet failure besides.
func chunkResultsToPopulate(repoName string, rc *treesitter.RepoContext, results []*treesitter.Result) PopulateResult {
	repoDir := rc.Root
	DeduplicateChunks(results)
	// Strictly after DeduplicateChunks, which rewrites chunk.Name and so
	// changes ChunkNodeID, and strictly before the loop below, whose
	// sort.SliceStable reorders result.Chunks and invalidates every slot.
	resolveSlotEdges(results)
	// Strictly after DeduplicateChunks and strictly before the index build, so
	// an arm sees final names and the ladder sees filled binds.
	fillBinds(rc, results)

	totalChunks := 0
	for _, result := range results {
		totalChunks += len(result.Chunks)
	}
	ix := newDeclIndex(totalChunks)
	nodeIDs := make(map[string]bool)
	langNodes := make(map[string]string) // language → lang_node ID
	var nodes []*knowledgev1.Node
	var allEdges []*knowledgev1.Edge
	var entries []NodeEntry

	for _, result := range results {
		nodeIDs[result.FilePath] = true
		// Populate Content on Dockerfile NodeFile
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
			indexDeclaration(ix, result, chunk, nodeID)
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
	}

	// Populate-then-resolve, now structural rather than incidental: the index
	// is complete across every file before any reference is resolved, and
	// resolution consumes the chunk results directly so the reference site each
	// edge carries survives to the walk.
	//
	// allEdges holds only the language-hub edges built above — they name node
	// IDs on both ends already and never enter resolution.
	resolvedEdges := append(allEdges, resolveEdges(results, ix, nodeIDs)...)
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

// indexDeclaration records one named declaration in the declaration index.
//
// The key is built from BASE names while the identity keeps the suffixed one:
// a reference writes Thing, never Thing#a1b2c3d4, so base-name keying is what
// lets a reference to a collided declaration find the whole surviving set.
//
// Unnamed chunks are skipped — they carry no name to be referenced by. The
// package-name guard the old scalar map also applied is deliberately NOT
// carried over: the namespace is no longer part of the key, and every language
// now gets a non-empty namespace regardless.
//
// A duplicate node ID means ChunkNodeID or DeduplicateChunks has regressed. It
// is a defect to alarm on rather than a case to serve, so it is logged and the
// first record is kept.
func indexDeclaration(ix *declIndex, result *treesitter.Result, chunk treesitter.Chunk, nodeID string) {
	if chunk.Name == "" {
		return
	}
	err := ix.add(&declRec{
		NodeID: nodeID,
		File:   result.FilePath,
		Scope:  treesitter.ScopeID(result.FilePath, result.Language, chunk.Context.PackageName),
		Parent: baseDeclName(chunk.ParentName),
		Name:   baseDeclName(chunk.Name),
	})
	if err != nil {
		slog.Error("collector: duplicate declaration node ID", "file", result.FilePath, "error", err)
	}
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
