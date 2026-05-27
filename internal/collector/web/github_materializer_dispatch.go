// SPDX-License-Identifier: Apache-2.0

package web

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/parser"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// materializeGithub is the dispatcher entry point invoked by processURL
// when parseGitHubURL accepts a URL.
//
// Design (post per-URL-link refactor):
//
//   - The materialization unit is the whole repository at (owner, repo, ref).
//     The first URL into a (owner, repo, ref) triggers a single codeload
//     tarball fetch + parser.PopulateForExternalGraph pass over the entire
//     repo. URL kind (blob / tree / root) and info.Path do NOT affect what
//     gets materialized — they only affect which node the URL links to.
//
//   - The dedupe registry is keyed by (owner, repo, ref). Subsequent URLs
//     into the same repo skip the fetch + parse but still resolve their
//     OWN per-URL link target inside the already-materialized graph.
//
//   - Per-URL link target (urlToID[raw]):
//     blob → the namespaced NodeFile ID for info.Path
//     tree → gh-root (no NodePackage type exists in PopulateForExternalGraph
//     output; sub-tree URLs fall back to gh-root)
//     root → gh-root
//
// RECURSION BOUND: this function and the helpers it calls do NOT enqueue
// new BFS work. Materialized chunk nodes do NOT re-enter the BFS — they
// are leaf content as far as link discovery is concerned.
//
// REINDEX-FLOW AVOIDANCE: this function and everything it calls produce
// only nodes + BatchEdges. The materializer never enters the reindex
// flow.
func (s *crawlState) materializeGithub(ctx context.Context, fc *fetchClient, raw string, info githubURLInfo, depth int) {
	_ = depth // recursion bound: depth is unused on purpose — materialized chunks never re-enter BFS

	key := githubKey{Owner: info.Owner, Repo: info.Repo, Ref: info.Ref}
	existingID, claimed, ready := s.githubMat.claim(key)
	if !claimed && ready {
		// Another URL already materialized this repo. Compute THIS URL's
		// per-URL link target and register — second URL gets its own
		// specific NodeFile, not a copy of the first URL's target.
		target := perURLTarget(info, existingID)
		s.mu.Lock()
		s.urlToID[raw] = target
		s.mu.Unlock()
		return
	}
	if !claimed && !ready {
		// Another worker tried but failed; we observed the abort. Skip.
		slog.Debug("github_materializer: prior materialization aborted, skipping", "url", raw)
		return
	}

	maxBytes := s.opts.MaxDownloadBytes
	if maxBytes < 0 {
		maxBytes = math.MaxInt64
	}

	s.materializeRepo(ctx, fc, raw, info, key, maxBytes)
}

// materializeRepo downloads the whole repo tarball at (owner, repo, ref),
// runs parser.PopulateForExternalGraph against it, builds a repo-scoped
// gh-root + CONTAINS edges to every NodeFile, and registers the FIRST
// URL's per-URL link target (which may be a NodeFile for a blob URL or
// the gh-root for a tree/root URL).
//
// Always-whole-repo: info.Path is IGNORED for the fetch/unpack — the
// tarball is unpacked in full. info.Path is consulted only when computing
// the per-URL link target via perURLTarget().
func (s *crawlState) materializeRepo(ctx context.Context, fc *fetchClient, raw string, info githubURLInfo, key githubKey, maxBytes int64) {
	repoInfo := info
	repoInfo.Path = "" // always materialize the whole repo

	rootDir, cleanup, w, err := fetchCodeloadTarball(ctx, fc, repoInfo, maxBytes)
	if err != nil {
		slog.Debug("github_materializer: tarball fetch failed", "url", raw, "err", err)
		s.githubMat.abort(key)
		return
	}
	if w != nil {
		s.appendWarning(raw, info, w, key)
		return
	}
	if cleanup != nil {
		s.mu.Lock()
		s.materializedCleanups = append(s.materializedCleanups, cleanup)
		s.mu.Unlock()
	}

	repoName := info.Owner + "/" + info.Repo + "@" + info.Ref
	if info.Ref == "" {
		repoName = info.Owner + "/" + info.Repo + "@HEAD"
	}
	nodes, edges, err := parser.PopulateForExternalGraph(ctx, repoName, rootDir)
	if err != nil {
		slog.Debug("github_materializer: PopulateForExternalGraph failed", "url", raw, "err", err)
		s.githubMat.abort(key)
		return
	}

	enrichForRecipes(nodes, repoName)

	ghRoot, ghEdges := buildGhRoot(repoInfo, raw, nodes)
	nodes = append(nodes, ghRoot)
	edges = append(edges, ghEdges...)

	target := perURLTarget(info, ghRoot.Id)

	s.mu.Lock()
	s.matNodes = append(s.matNodes, nodes...)
	s.matEdges = append(s.matEdges, edges...)
	s.urlToID[raw] = target
	s.mu.Unlock()

	s.githubMat.publish(key, ghRoot.Id)
}

// enrichForRecipes lifts struct-only fields (FilePath, Language) onto
// metadata + SymbolName so the recipe DSL can read them. The DSL exposes
// well-known struct fields (page.name, page.id, page.type, ...) plus
// metadata (page.<key>); FilePath / Language are neither, so without
// this pass a recipe operating on a github-materialized graph cannot see
// the file path or language a NodeFile carries.
//
// Per NodeFile we set:
//   - SymbolName = repo-relative path (e.g. "pkg/foo.go") so page.name
//     is meaningful and recipes can filter via page.name ~= /\.go$/.
//   - Metadata["file_path"] = full namespaced FilePath
//   - Metadata["relpath"] = repo-relative path (same as SymbolName)
//   - Metadata["language"] = Language struct value
//   - Metadata["repo"] = "<owner>/<repo>@<ref>"
//
// Other node types (chunks, language hubs) are left untouched.
func enrichForRecipes(nodes []*knowledgev1.Node, repoName string) {
	prefix := repoName + "/"
	for i := range nodes {
		n := nodes[i]
		if kgtypes.NodeType(n.Type) != kgtypes.NodeFile {
			continue
		}
		relpath := strings.TrimPrefix(n.FilePath, prefix)
		if n.SymbolName == "" {
			n.SymbolName = relpath
		}
		if n.Metadata == nil {
			n.Metadata = map[string]string{}
		}
		n.Metadata["file_path"] = n.FilePath
		n.Metadata["relpath"] = relpath
		n.Metadata["language"] = n.Language
		n.Metadata["repo"] = repoName
	}
}

// perURLTarget computes the node ID a github URL should link to inside
// the already-materialized repo graph:
//
//   - blob URL → the namespaced NodeFile ID, owner/repo@ref/path
//   - tree URL → gh-root (no NodePackage type exists in the parser output;
//     sub-tree paths fall back to the repo root)
//   - root URL → gh-root
//
// The blob target is computed deterministically from (info.Owner, info.Repo,
// info.Ref, info.Path) — the same shape parser.PopulateForExternalGraph
// produces. No registry lookup or node scan needed.
func perURLTarget(info githubURLInfo, ghRootID string) string {
	if info.Kind == kindBlob && info.Path != "" {
		return info.Owner + "/" + info.Repo + "@" + info.Ref + "/" + info.Path
	}
	return ghRootID
}

// buildGhRoot constructs the synthetic NodeGithubRepo node + CONTAINS
// edges from the gh-root to every NodeFile in the materialized batch.
//
// gh-root ID format: "gh-root:<owner>/<repo>@<ref>". The ID is repo-scoped
// (no path component) — there is exactly one gh-root per (owner, repo, ref)
// and it represents the whole repository. Per-URL link targets are
// computed separately by perURLTarget().
func buildGhRoot(info githubURLInfo, sourceURL string, nodes []*knowledgev1.Node) (*knowledgev1.Node, []kgwire.BatchEdge) {
	id := fmt.Sprintf("gh-root:%s/%s@%s", info.Owner, info.Repo, info.Ref)
	name := fmt.Sprintf("%s/%s@%s", info.Owner, info.Repo, info.Ref)
	var edges []kgwire.BatchEdge
	fileCount := 0
	for _, n := range nodes {
		if kgtypes.NodeType(n.Type) == kgtypes.NodeFile {
			fileCount++
			edges = append(edges, kgwire.BatchEdge{
				FromIdx: -1, ToIdx: -1,
				FromID: id,
				ToID:   n.Id,
				// Knowledge-style lowercase "contains" — the gh-root is a
				// synthetic anchor for downstream recipe walks, not a
				// code-graph file→symbol relation. Distinguishes
				// gh-root → file from PopulateForExternalGraph's
				// uppercase file → chunk CONTAINS edges.
				Type: kgtypes.EdgeKGContains,
			})
		}
	}
	root := &knowledgev1.Node{
		Id:         id,
		Type:       string(kgtypes.NodeGithubRepo),
		SymbolName: name,
		Source:     "web-collect:github-materializer",
		Metadata: map[string]string{
			"owner":           info.Owner,
			"repo":            info.Repo,
			"ref":             info.Ref,
			"source_url":      sourceURL,
			"materialized_at": time.Now().UTC().Format(time.RFC3339),
			"file_count":      fmt.Sprintf("%d", fileCount),
		},
		CreatedAt: time.Now().UnixNano(),
	}
	return root, edges
}

// appendWarning emits a materialization-skipped warning node + edge,
// records the URL → warning-node mapping, and aborts the in-flight
// claim. Distinct from publish() because the registry should NOT be
// populated with a warning ID — subsequent retries within the same
// crawl should re-attempt the materialization (current policy: dedupe
// is by-success, not by-attempt).
//
// The current implementation maps urlToID[raw] = warningID so internal
// link resolution wires up to the warning node, but does NOT publish
// to the registry — that way a second URL hitting the same key has a
// fresh shot (e.g. if maxBytes was raised mid-crawl, hypothetically).
func (s *crawlState) appendWarning(raw string, info githubURLInfo, w *materializerWarning, key githubKey) {
	parentID := "" // seed-URL warnings have no parent page; future enhancement could thread one through
	node, edges := emitMaterializerWarning(parentID, info, w)
	s.mu.Lock()
	s.matNodes = append(s.matNodes, node)
	s.matEdges = append(s.matEdges, edges...)
	s.urlToID[raw] = node.Id
	s.mu.Unlock()
	s.githubMat.abort(key)
}
