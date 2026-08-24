// SPDX-License-Identifier: Apache-2.0

package coderun

import (
	"path/filepath"
	"sort"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// repoRootID is the node ID for the repo-level root node.
const repoRootID = "."

// HierarchyFromNodes derives the directory/package hierarchy for a code graph
// from the nodes a collect already holds in memory: it returns the NodePackage
// nodes (every source directory, plus the repo root) and the EdgeContains edges
// that wire directories to their files and to each other.
//
// It is PURE — no context, no graph reads, no writes. The hierarchy is computed
// mid-collect so it can be appended to the collect payload, which is what gets
// it epoch-stamped by the collect path's single stamping site and lets it
// survive its own finalize. Computing it from a post-collect graph drain
// instead is what left these nodes stamped 0 and swept by the next collect.
//
// The file filter is applied INSIDE this function rather than at the call site,
// deliberately: it keeps the seam one argument wide and makes it impossible for
// a caller to hand over the unfiltered node slice, which would turn every chunk
// node's file path into a package.
//
// NOTE: neither chunker-emitted CONTAINS shape is built here. The treesitter
// chunker emits both at collect time (chunker.go emitDeclarationEdges): the
// file→declaration edge, and the parent-to-member edge from a Go receiver
// type or a class to its member. Both are uploaded with the nodes — see
// populate.go's "File→symbol membership is handled by the existing CONTAINS
// edges emitted by treesitter/chunker.go".
//
// Both shapes pass through parser.resolveEdges (parser/edges.go). A reference
// whose endpoint does not resolve to exactly one declaration is NOT simply
// dropped: the scope walk has four outcomes, and only the last of them emits
// nothing (parser/edges.go:32-50 — an ambiguous or dynamic-with-candidates
// reference emits one edge per candidate, and only an external or
// dynamic-with-no-candidates reference emits none).
//
// The old store-based hierarchy pass re-derived them via a full IterateAll
// scan; that scan has no wire-expressible 'all code-type nodes' browse and
// is redundant given the chunker already wired the edges. The unique value
// HierarchyFromNodes adds is the directory/package hierarchy below.
func HierarchyFromNodes(nodes []*knowledgev1.Node) (packageNodes []*knowledgev1.Node, edges []*knowledgev1.Edge) {
	fileNodes := make([]*knowledgev1.Node, 0, len(nodes))
	for _, n := range nodes {
		if kgtypes.NodeType(n.Type) == kgtypes.NodeFile {
			fileNodes = append(fileNodes, n)
		}
	}

	dirFiles := groupFilesByDirCG(fileNodes)

	// Step 1: package nodes + dir→file edges.
	collectPackageNodes(dirFiles, &packageNodes, &edges)

	// Step 2: link package hierarchy (parent→child edges, intermediate nodes).
	allDirs := make(map[string]bool, len(dirFiles))
	for dir := range dirFiles {
		allDirs[dir] = true
	}
	// THE WALK IS OVER A SORTED SNAPSHOT, and it closes two distinct hazards with
	// one change. Go randomizes map iteration per run, so walking allDirs directly
	// made the emitted order — and therefore the collect payload — a function of
	// the runtime rather than of the tree. And linkDirToParentCG's callee writes
	// NEW keys into this very map as it walks it, which Go leaves explicitly
	// unspecified: a key added during a range may or may not be visited. Iterating
	// a snapshot taken before the walk makes the visited set the ORIGINAL
	// directories, exactly once each, in a fixed order.
	for _, dir := range sortedKeys(allDirs) {
		linkDirToParentCG(&packageNodes, &edges, dir, allDirs)
	}

	return packageNodes, edges
}

// sortedKeys returns m's keys in ascending order, so a caller can walk a map
// deterministically. It exists because Go's map iteration order is randomized
// per run and this package's output ORDER is part of the collect payload.
func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// collectPackageNodes accumulates package nodes for each directory and the repo
// root, plus dir→file edges. The collect upsert is keyed by ID, so re-emitting
// an already-present package node is idempotent and no existence pre-check is
// needed.
//
// The dir == "." skip is PRESERVED BEHAVIOR, not an oversight: root-level files
// get no CONTAINS edge from the repo root. Changing it would be a redesign of
// the directory-grouping shape, which is out of scope for the lifecycle change.
func collectPackageNodes(dirFiles map[string][]*knowledgev1.Node, packageNodes *[]*knowledgev1.Node, batchEdges *[]*knowledgev1.Edge) {
	// Sorted rather than a bare map range: Go randomizes map iteration per run, so
	// walking dirFiles directly emitted the package nodes and their CONTAINS edges
	// in a different order on every call over one fixed node set.
	dirs := make([]string, 0, len(dirFiles))
	for dir := range dirFiles {
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)
	for _, dir := range dirs {
		files := dirFiles[dir]
		if dir == "." {
			continue
		}
		*packageNodes = append(*packageNodes, &knowledgev1.Node{Id: dir, Type: string(kgtypes.NodePackage)})
		for _, f := range files {
			*batchEdges = append(*batchEdges, &knowledgev1.Edge{
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
//
// Called with dir == "." this emits a SELF edge "."→"." — filepath.Dir(".") is
// ".". That is PRESERVED BEHAVIOR: the self-edge already exists in the live
// code graphs, and the server's container-staleness predicate is written around
// it (its parent/child comparison degenerates to an equality when child IS
// parent, so the self-edge is inert rather than re-staling the root forever).
// Removing it is a grouping-shape change, out of scope for the lifecycle move.
func linkDirToParentCG(packageNodes *[]*knowledgev1.Node, edges *[]*knowledgev1.Edge, dir string, allDirs map[string]bool) {
	parent := filepath.Dir(dir)
	if parent == "." || parent == "" {
		*edges = append(*edges, &knowledgev1.Edge{FromId: repoRootID, ToId: dir, Type: string(kgtypes.EdgeContains)})
		return
	}
	if allDirs[parent] {
		*edges = append(*edges, &knowledgev1.Edge{FromId: parent, ToId: dir, Type: string(kgtypes.EdgeContains)})
		return
	}
	ensureParentChainCG(packageNodes, edges, dir, allDirs)
}

// ensureParentChainCG accumulates intermediate NodePackage nodes and edges from
// dir up to an existing directory or root. The collect upsert is keyed by ID, so
// re-emitting an existing package node is idempotent — and because these nodes
// now ride the collect payload, the collect path's carry-forward preserves the
// summary and keywords of a content-unchanged row rather than blanking them.
func ensureParentChainCG(packageNodes *[]*knowledgev1.Node, edges *[]*knowledgev1.Edge, dir string, existingDirs map[string]bool) {
	current := dir
	for {
		parent := filepath.Dir(current)
		if parent == current || parent == "" {
			break
		}
		if parent == "." {
			*edges = append(*edges, &knowledgev1.Edge{
				FromId: repoRootID,
				ToId:   current,
				Type:   string(kgtypes.EdgeContains),
			})
			break
		}
		if existingDirs[parent] {
			*edges = append(*edges, &knowledgev1.Edge{
				FromId: parent,
				ToId:   current,
				Type:   string(kgtypes.EdgeContains),
			})
			break
		}
		*packageNodes = append(*packageNodes, &knowledgev1.Node{Id: parent, Type: string(kgtypes.NodePackage)})
		existingDirs[parent] = true
		*edges = append(*edges, &knowledgev1.Edge{
			FromId: parent,
			ToId:   current,
			Type:   string(kgtypes.EdgeContains),
		})
		current = parent
	}
}
