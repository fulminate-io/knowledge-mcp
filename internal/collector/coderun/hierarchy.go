// SPDX-License-Identifier: Apache-2.0

package coderun

import (
	"context"
	"log/slog"
	"path/filepath"
	"time"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/postpopulate"
)

// repoRootID is the node ID for the repo-level root node.
const repoRootID = "."

// BuildHierarchy creates NodePackage and repo root nodes in the code graph,
// and ensures file→chunk EdgeContains edges use actual chunk node IDs.
// NodeFile nodes already exist (created by the pipeline's buildGraph).
// This is the PostPopulate callback for code graph reindexing.
//
// graphName is the per-repo code graph; every read/write rides the postpopulate
// wire helpers routed by kgtypes.GraphCode (→ Target.Repo==graphName). The package
// nodes + all contains edges are emitted in ONE LinkNodesAndEdgesBatch — the
// create_batch upsert-by-ID makes re-emitting an existing package node
// idempotent, collapsing the old "exists?-then-upsert" guard.
func BuildHierarchy(ctx context.Context, gc postpopulate.GraphCaller, graphName string) error {
	start := time.Now()

	fileNodes, err := collectHierarchyFileNodes(ctx, gc, graphName, start)
	if err != nil {
		return err
	}

	dirFiles := groupFilesByDirCG(fileNodes)
	slog.Info("hierarchy: grouped files by directory", "dirs", len(dirFiles), "elapsed", time.Since(start).Round(time.Millisecond))

	var batchEdges []knowledgev1.Edge
	var packageNodes []*knowledgev1.Node

	// NOTE: neither chunker-emitted CONTAINS shape is built here. The treesitter
	// chunker emits both at collect time (chunker.go emitDeclarationEdges): the
	// file→declaration edge, and the parent-to-member edge from a Go receiver
	// type or a class to its member. Both are uploaded with the nodes — see
	// populate.go's "File→symbol membership is handled by the existing CONTAINS
	// edges emitted by treesitter/chunker.go".
	//
	// Both shapes pass through parser.resolveEdges (parser/edges.go), which
	// drops any edge whose endpoint does not resolve against the build's
	// symbolMap — so a declaration whose name is ambiguous within its file
	// still ends up with no CONTAINS edge.
	//
	// The old store-based BuildHierarchy re-derived them via a full IterateAll
	// scan; that scan has no wire-expressible 'all code-type nodes' browse and
	// is redundant given the chunker already wired the edges. The unique value
	// BuildHierarchy adds client-side is the directory/package hierarchy below.

	// Step 1: package nodes + dir→file edges.
	collectPackageNodes(dirFiles, &packageNodes, &batchEdges)
	slog.Info("hierarchy: queued package nodes", "nodes", len(packageNodes), "elapsed", time.Since(start).Round(time.Millisecond))

	// Step 2: link package hierarchy (parent→child edges, intermediate nodes).
	allDirs := make(map[string]bool, len(dirFiles))
	for dir := range dirFiles {
		allDirs[dir] = true
	}
	for dir := range allDirs {
		linkDirToParentCG(&packageNodes, &batchEdges, dir, allDirs)
	}
	slog.Info("hierarchy: queued hierarchy edges", "elapsed", time.Since(start).Round(time.Millisecond))

	// Flush package nodes + all edges in one create_batch (routed by repo).
	if err := postpopulate.LinkNodesAndEdgesBatch(ctx, gc, kgtypes.GraphCode, graphName, packageNodes, batchEdges); err != nil {
		return err
	}
	slog.Info("hierarchy: flushed", "nodes", len(packageNodes), "edges", len(batchEdges), "elapsed", time.Since(start).Round(time.Millisecond))

	return nil
}

// collectHierarchyFileNodes gathers EVERY NodeFile node from the code graph via a
// typed wire drain (routed by kgtypes.GraphCode → Target.Repo==graphName), which
// reads bounded id-keyset pages. A single browse would be capped at the browse
// default, so a repo of any real size would build its hierarchy from one page.
// File nodes already exist (created by the pipeline's buildGraph).
func collectHierarchyFileNodes(ctx context.Context, gc postpopulate.GraphCaller, graphName string, start time.Time) ([]*knowledgev1.Node, error) {
	browsed, err := postpopulate.BrowseAllNodes(ctx, gc, kgtypes.GraphCode, graphName, map[string]any{
		"type": string(kgtypes.NodeFile),
	})
	if err != nil {
		return nil, err
	}
	// BrowseAllNodes returns []*knowledgev1.Node (the typed wire node the client
	// decode layer yields); the hierarchy builder + LinkNodesAndEdgesBatch
	// consume the slice directly.
	slog.Info("hierarchy: collected file nodes", "count", len(browsed), "elapsed", time.Since(start).Round(time.Millisecond))
	return browsed, nil
}

// collectPackageNodes accumulates package nodes for each directory and the repo
// root, plus dir→file edges. The create_batch upsert-by-ID makes re-emitting an
// already-present package node idempotent, so no existence pre-check is needed.
func collectPackageNodes(dirFiles map[string][]*knowledgev1.Node, packageNodes *[]*knowledgev1.Node, batchEdges *[]knowledgev1.Edge) {
	for dir, files := range dirFiles {
		if dir == "." {
			continue
		}
		*packageNodes = append(*packageNodes, &knowledgev1.Node{Id: dir, Type: string(kgtypes.NodePackage)})
		for _, f := range files {
			*batchEdges = append(*batchEdges, knowledgev1.Edge{
				FromId: dir,
				ToId:   f.Id,
				Type:   string(kgtypes.EdgeContains),
			})
		}
	}
	// Repo root package node.
	*packageNodes = append(*packageNodes, &knowledgev1.Node{Id: repoRootID, Type: string(kgtypes.NodePackage)})
}

// groupFilesByDirCG groups file nodes by their parent directory.
func groupFilesByDirCG(fileNodes []*knowledgev1.Node) map[string][]*knowledgev1.Node {
	dirFiles := make(map[string][]*knowledgev1.Node)
	for _, f := range fileNodes {
		dir := filepath.Dir(f.Id)
		if dir == "" {
			dir = "."
		}
		dirFiles[dir] = append(dirFiles[dir], f)
	}
	return dirFiles
}

// linkDirToParentCG connects a directory to its parent, appending edges to the
// batch (and any intermediate package nodes to packageNodes).
func linkDirToParentCG(packageNodes *[]*knowledgev1.Node, edges *[]knowledgev1.Edge, dir string, allDirs map[string]bool) {
	parent := filepath.Dir(dir)
	if parent == "." || parent == "" {
		*edges = append(*edges, knowledgev1.Edge{FromId: repoRootID, ToId: dir, Type: string(kgtypes.EdgeContains)})
		return
	}
	if allDirs[parent] {
		*edges = append(*edges, knowledgev1.Edge{FromId: parent, ToId: dir, Type: string(kgtypes.EdgeContains)})
		return
	}
	ensureParentChainCG(packageNodes, edges, dir, allDirs)
}

// ensureParentChainCG accumulates intermediate NodePackage nodes and edges from
// dir up to an existing directory or root. The create_batch upsert-by-ID makes
// re-emitting an existing package node idempotent (preserving summaries on the
// server side via the carry-forward path), so no existence pre-check is needed.
func ensureParentChainCG(packageNodes *[]*knowledgev1.Node, edges *[]knowledgev1.Edge, dir string, existingDirs map[string]bool) {
	current := dir
	for {
		parent := filepath.Dir(current)
		if parent == current || parent == "" {
			break
		}
		if parent == "." {
			*edges = append(*edges, knowledgev1.Edge{
				FromId: repoRootID,
				ToId:   current,
				Type:   string(kgtypes.EdgeContains),
			})
			break
		}
		if existingDirs[parent] {
			*edges = append(*edges, knowledgev1.Edge{
				FromId: parent,
				ToId:   current,
				Type:   string(kgtypes.EdgeContains),
			})
			break
		}
		*packageNodes = append(*packageNodes, &knowledgev1.Node{Id: parent, Type: string(kgtypes.NodePackage)})
		existingDirs[parent] = true
		*edges = append(*edges, knowledgev1.Edge{
			FromId: parent,
			ToId:   current,
			Type:   string(kgtypes.EdgeContains),
		})
		current = parent
	}
}
