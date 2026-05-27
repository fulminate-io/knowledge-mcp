// SPDX-License-Identifier: Apache-2.0

package web

// github_materializer.go implements URL-aware materialization of github.com
// links inside the web collector's BFS. Detected github URLs short-circuit
// the HTML fetch+readability path: tree URLs trigger a codeload tarball
// download + parser.PopulateForExternalGraph; blob URLs trigger a single
// raw.githubusercontent fetch + treesitter chunking.
//
// The materializer produces nodes/edges only. The web collector envelope
// (WebCollector.Collect) routes them into the active web/<source-slug>
// graph (GraphWebRaw, SkipsLLMProcessing=true) alongside ordinary HTML
// pages. The materializer file is forbidden from referencing reindex-
// flow names (verified by Phase 6 grep) so the literal token names are
// kept out of source comments here. See the project plan node for the
// audit verdict and rationale.
//
// AUDIT — GraphType==GraphCode call-sites (Phase 1, Step 3):
//
// Every kgtypes.GraphCode reference in the codebase falls into one of three
// buckets, and none of them fire on this materializer's write path:
//
//   (1) Read-side queries against the code graph
//       (collector/codegraph/format.go:151,188; tools.go:126,150,263;
//        routing.go:91-265; search.go:141-299; coderun/state.go:37-54;
//        coderun/git.go:131). All do
//        store.Store().Scope(ctx, GraphCode, key) to READ from a code
//        graph. The materializer never queries the code graph. Verdict: N/A.
//
//   (2) Codesync write path entries
//       (codesync/sync.go:70,144; codesync/collector.go:73;
//        codesync/register_postpopulate.go:18). These set GraphCode on
//        write requests and register the code-graph PostPopulate hook +
//        EqualFunc. The materializer never enters the codesync write
//        flow. Verdict: SAFE.
//
//   (3) Generic graph-keyed dispatch in domains/store
//       (proxy.go, registry_saver.go, search_index_bm25.go). These case
//       on GraphCode for proxy resolution / saver routing / index
//       enumeration. They enumerate graphs registered AS GraphCode.
//       Materializer output lands in GraphWebRaw graphs (web/<source-slug>)
//       via WebCollector.Collect, never GraphCode. Verdict: N/A.
//
// The web collector's existing envelope handles GraphWebRaw correctly
// (SkipsLLMProcessing=true, no LLM-pipeline post-processing). No new
// branches were added to cmd/knowledge/internal/collector/parser as a result of this
// audit.

import (
	"net/url"
	"strings"
)

// githubURLKind classifies the parsed github URL.
type githubURLKind int

const (
	// kindRoot identifies github.com/{owner}/{repo} with no path —
	// the codeload fetcher will follow a redirect to the default branch.
	kindRoot githubURLKind = iota
	// kindTree identifies github.com/{owner}/{repo}/tree/{ref}/{path...} —
	// fetched as a codeload tarball and unpacked.
	kindTree
	// kindBlob identifies github.com/{owner}/{repo}/blob/{ref}/{path...} —
	// fetched as a single raw.githubusercontent file.
	kindBlob
)

// githubURLInfo holds the parsed components of a github.com URL the
// materializer recognizes. Owner and Repo are normalized to lower-case for
// dedupe-key purposes; the original casing is preserved on the source URL
// retained alongside this struct.
type githubURLInfo struct {
	Owner string
	Repo  string
	Ref   string // branch / tag / sha; empty for kindRoot (resolved later via codeload)
	Path  string // sub-path inside the repo; empty for kindRoot when no path follows
	Kind  githubURLKind
}

// parseGitHubURL parses a github.com URL into a structured githubURLInfo.
// Returns ok=false for any URL the materializer should not handle: non-https
// schemes, hosts other than github.com / www.github.com, paths that name
// non-content shapes (issues, pulls, releases, raw/, codeload/), and URLs
// with empty owner/repo.
//
// Recognized shapes:
//
//   - https://github.com/{owner}/{repo}                    → kindRoot
//   - https://github.com/{owner}/{repo}/tree/{ref}/{path}  → kindTree
//   - https://github.com/{owner}/{repo}/blob/{ref}/{path}  → kindBlob
//
// blob URLs without a ref+path tail are rejected (a "blob" with no file
// is a 404 on github).
//
// Owner and Repo are lower-cased for dedupe-key consistency. Ref is left
// in original casing (refs are case-sensitive on git).
func parseGitHubURL(rawURL string) (info githubURLInfo, ok bool) {
	u, err := url.Parse(rawURL)
	if err != nil || u == nil {
		return githubURLInfo{}, false
	}
	if u.Scheme != "https" {
		return githubURLInfo{}, false
	}
	host := strings.ToLower(u.Host)
	host = strings.TrimPrefix(host, "www.")
	if host != "github.com" {
		return githubURLInfo{}, false
	}
	parts := splitNonEmpty(u.Path, '/')
	if len(parts) < 2 {
		return githubURLInfo{}, false
	}
	owner := strings.ToLower(parts[0])
	repo := strings.ToLower(parts[1])
	if owner == "" || repo == "" {
		return githubURLInfo{}, false
	}
	// Strip any trailing ".git" suffix github sometimes accepts.
	repo = strings.TrimSuffix(repo, ".git")

	if len(parts) == 2 {
		return githubURLInfo{
			Owner: owner,
			Repo:  repo,
			Kind:  kindRoot,
		}, true
	}

	switch parts[2] {
	case "tree":
		if len(parts) < 4 {
			return githubURLInfo{}, false
		}
		ref := parts[3]
		path := ""
		if len(parts) > 4 {
			path = strings.Join(parts[4:], "/")
		}
		return githubURLInfo{
			Owner: owner,
			Repo:  repo,
			Ref:   ref,
			Path:  path,
			Kind:  kindTree,
		}, true
	case "blob":
		if len(parts) < 5 {
			return githubURLInfo{}, false
		}
		ref := parts[3]
		path := strings.Join(parts[4:], "/")
		if path == "" {
			return githubURLInfo{}, false
		}
		return githubURLInfo{
			Owner: owner,
			Repo:  repo,
			Ref:   ref,
			Path:  path,
			Kind:  kindBlob,
		}, true
	default:
		// issues, pulls, releases, actions, wiki, raw, codeload, ... — not
		// a content URL the materializer handles.
		return githubURLInfo{}, false
	}
}

// isGitHubURL is a lightweight gate used by the dispatcher in
// splitNonEmpty splits s on sep and drops empty elements. Equivalent to
// strings.FieldsFunc with a single-rune separator but avoids an allocation
// for the rune-fn closure.
func splitNonEmpty(s string, sep byte) []string {
	out := make([]string, 0, 8)
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == sep {
			if i > start {
				out = append(out, s[start:i])
			}
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}
