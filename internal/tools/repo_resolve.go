// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// RepoResolver caches the server's loaded code-graph names per MCP
// session and resolves a working directory to a code-graph name via
// basename / suffix matching. The cache is populated lazily on the
// first ResolveCwd call via a single graph-catalog read (the generic
// RETURN_MODE_GRAPH_NAMES Execute over the code GraphType); the
// sync.Once guarantees concurrent callers all observe the same load
// result without re-firing the read.
//
// The server is filesystem-blind: the client computes its own cwd and matches
// against the server-reported graph list. The activerepo.Detect suffix-match
// heuristic (state.go:54-61) lives here so callers that habitually invoke
// `knowledge ...` from a subdirectory of the repo (rather than the repo root) keep
// working.
//
// Cache invalidation is intentionally out of scope (per OQ-5 lock):
// the list refreshes on next MCP session start. A long-lived client
// that creates a new code graph mid-session must `repo:`-inject
// explicitly until restart.
//
// The lazy load enumerates the loaded code graphs via the generic
// Execute seam (RETURN_MODE_GRAPH_NAMES over the code GraphType) rather
// than a bespoke RPC.
type RepoResolver struct {
	gc GraphCaller

	once       sync.Once
	codeGraphs []string
	loadErr    error
}

// NewRepoResolver constructs a resolver bound to the given GraphCaller.
// The 3-arg GraphCaller surface (not the 4-arg WireClient) is the
// canonical intercept dependency — keep this narrow.
func NewRepoResolver(gc GraphCaller) *RepoResolver {
	return &RepoResolver{gc: gc}
}

// ResolveCwd matches the given working directory against the loaded
// code-graph names. Returns (name, true, nil) on match, ("", false, nil)
// on no match, or ("", false, err) when the lazy load failed.
//
// Matching strategy (mirrors activerepo.Detect at activerepo/state.go:54-61):
//  1. basename(cwd) equals a loaded graph name → match.
//  2. /<name>/ appears in cwd → match (handles `cd repo/subdir`).
//  3. cwd ends with /<name> → match (covers the bare-suffix case).
//
// Empty cwd returns ("", false, nil) — caller decides whether to error
// or fall through to an explicit-repo path.
func (r *RepoResolver) ResolveCwd(ctx context.Context, cwd string) (string, bool, error) {
	r.once.Do(func() {
		r.codeGraphs, r.loadErr = loadCodeGraphNames(ctx, r.gc)
	})
	if r.loadErr != nil {
		return "", false, r.loadErr
	}
	if cwd == "" {
		return "", false, nil
	}
	base := filepath.Base(cwd)
	// First pass: basename equality.
	for _, name := range r.codeGraphs {
		if name == base {
			return name, true, nil
		}
	}
	// Second pass: name-as-path-component / suffix.
	for _, name := range r.codeGraphs {
		if name == "" {
			continue
		}
		needle := "/" + name
		if strings.HasSuffix(cwd, needle) || strings.Contains(cwd, needle+"/") {
			return name, true, nil
		}
	}
	return "", false, nil
}

// loadCodeGraphNames enumerates the loaded code graphs via the generic Execute
// seam (RETURN_MODE_GRAPH_NAMES over the code GraphType, the fetchGraphNamesOfType
// helper) and projects GraphInfo.Name. The per-type query makes the old
// graph_type=="code" filter implicit; empty names are dropped.
func loadCodeGraphNames(ctx context.Context, gc GraphCaller) ([]string, error) {
	infos, err := fetchGraphNamesOfType(ctx, gc, string(kgtypes.GraphCode))
	if err != nil {
		return nil, fmt.Errorf("repo resolver: %w", err)
	}
	out := make([]string, 0, len(infos))
	for _, gi := range infos {
		if gi.Name == "" {
			continue
		}
		out = append(out, gi.Name)
	}
	return out, nil
}
