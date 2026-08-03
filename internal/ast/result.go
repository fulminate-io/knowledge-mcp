// SPDX-License-Identifier: Apache-2.0

// Package ast (result formatter + enclosing-node resolver) — turns the
// RawMatch slice from Match into the LLM-facing MatchResults shape with
// enclosing_node_id / enclosing_signature fields populated against the code
// graph.
//
// Reuse / shape: mirrors domains/topology/dead_code_review.go:96-145 verbatim.
// Builds a per-call codeNodeIndex (no global cache — a cache shared across
// calls would serve stale enclosing-node data after a re-collect) by iterating
// function-ish nodes via the
// HydratorBackend abstraction and indexing them by (file_path, start_line).
// The backend (graphClientHydratorBackend) owns the function-ish type filter
// client-side, so this file no longer pins its own NodeType list.
//
// Off-by-one tolerance: tree-sitter row counts can differ by 1 from
// go/ast positions because tree-sitter is 0-based and go/ast is 1-based —
// dead_code_review.go addresses the same shape with `for _, delta := range
// []int{0, -1, 1}` (mapOneDeadFunc:155-160). We mirror that ladder here.
//
// HydratorBackend abstraction: the binary split (corrective
// rework) moved the ast tool from server-side to client-side. The server has
// the code graph; the client has the source files. Hydrate takes a
// HydratorBackend interface so each side can supply its own enumeration: the
// production client-side graphClientHydratorBackend (cmd/knowledge/internal/
// tools/ast_hydrator.go) issues a bounded file_symbols query over the wire; the
// NoOpBackend returns zero nodes when the client has no code-graph access. The
// backend emits *knowledgev1.Node values (the wire proto) — the P2-T5 retype
// dropped the server-side node wrapper from the client read path.

package ast

import (
	"context"
	"fmt"
	"sort"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// HydratorBackend abstracts enumeration of function-ish code-graph nodes for
// Hydrate's enclosing-node lookup. The production adapter
// (graphClientHydratorBackend) scopes a file_symbols query to the raw match
// set's files; the NoOpBackend is a no-op (the client has no code-graph
// access). Both emit *knowledgev1.Node values via the visitor callback so
// Hydrate can build its codeNodeIndex without retaining a slice in memory.
//
// Implementations MUST emit only function-ish nodes (function_declaration,
// method_declaration, etc.) — Hydrate does not re-filter. files is the unique
// set of file paths from the raw match set; backends that can scope their walk
// MUST honor it. Empty files = walk all (or no-op for backends with nothing to
// emit).
type HydratorBackend interface {
	IterateFunctionish(ctx context.Context, files []string, fn func(*knowledgev1.Node) error) error
}

// NoOpBackend is a HydratorBackend that emits zero nodes. The client-side
// ast intercept uses this when the client doesn't carry the code graph
// in-process — enclosing-node IDs are unavailable without an extra RPC, which
// the corrective rework explicitly avoids.
type NoOpBackend struct{}

// IterateFunctionish returns nil immediately — the client has no nodes to
// emit. Hydrate then leaves EnclosingNodeID + EnclosingSignature empty for
// every match.
func (NoOpBackend) IterateFunctionish(_ context.Context, _ []string, _ func(*knowledgev1.Node) error) error {
	return nil
}

// MatchResult is one hydrated match with the code-graph node ID + signature
// of the enclosing function/method (when one was found in the code graph).
type MatchResult struct {
	FilePath           string             `json:"file_path"`
	StartLine          int                `json:"start_line"`
	EndLine            int                `json:"end_line"`
	Captures           map[string]Capture `json:"captures"`
	EnclosingNodeID    string             `json:"enclosing_node_id,omitempty"`
	EnclosingSignature string             `json:"enclosing_signature,omitempty"`
}

// Stats merges Match's WalkStats with Hydrate's own metrics. FilesSkipped
// passes through verbatim from WalkStats so callers can detect lossy walks.
type Stats struct {
	FilesScanned int   `json:"files_scanned"`
	FilesSkipped int   `json:"files_skipped"`
	DurationMS   int64 `json:"duration_ms"`
}

// MatchResults is the LLM-facing output of a Match+Hydrate round-trip. Hint
// is populated when len(Matches) == 0 with the ticket-specified guidance text.
// WalkedRoot echoes the directory the walk actually ran over so the caller can
// tell which tree produced (or failed to produce) the matches; the handler
// populates it from the resolved repoDir.
type MatchResults struct {
	Matches    []MatchResult `json:"matches"`
	Stats      Stats         `json:"stats"`
	WalkedRoot string        `json:"walked_root,omitempty"`
	Hint       string        `json:"hint,omitempty"`
}

// emptyResultHint is the LLM-facing guidance text emitted when Hydrate
// returns zero matches. Suggests progressively narrower diagnostics: count
// to confirm the walk produced no candidates, then a wildcard pattern $_ +
// `kind` leaf on $match to confirm the outer construct exists at all.
const emptyResultHint = "no matches — try broader scope, simplify the pattern, sanity-check with operation:\"count\" on a simpler pattern, or use pattern:\"$_\" with where:{kind:{of:\"$match\",is:\"<kind>\"}} to confirm the outer construct exists"

// ZeroScanHint is the LLM-facing guidance text emitted when the walk scanned
// ZERO files (FilesScanned == 0) — a strong wrong-root signal, distinct from
// scanned-but-no-match (which keeps emptyResultHint). walkedRoot is the resolved
// directory the walk ran over; language is the raw user-supplied language string.
func ZeroScanHint(walkedRoot, language string) string {
	return fmt.Sprintf("walked %s: no %s files found — wrong root? pass repo:<name|/abs/path> or check --root", walkedRoot, language)
}

// codeNodeIndex resolves a (file_path, line) to the smallest enclosing
// function-ish code-graph node. Per-call, NOT cached.
//
// Indexed per-file as a slice of (start_line, end_line, node) triples sorted
// by ascending range size (smallest first) so lookup picks the tightest
// enclosing scope when functions nest (e.g., a closure inside a method).
//
// AST matches happen at arbitrary statement lines INSIDE function bodies —
// `defer X.Close()` at line 168 inside a function declared at line 153 is the
// shape we need to support. The earlier line-keyed map design only worked
// when the match line equaled the function-decl line (its prior caller in
// dead_code_review.go was specifically looking up dead-function-decl
// locations, not statements inside function bodies).
type codeNodeIndex struct {
	byFile map[string][]rangeEntry
}

// rangeEntry is one function-ish node with its inclusive line range.
type rangeEntry struct {
	startLine int
	endLine   int
	node      *knowledgev1.Node
}

// Hydrate joins a RawMatch slice to code-graph node IDs by (file_path, line).
// Builds the codeNodeIndex once per call (mirrors the per-Run shape in
// dead_code_review.go — no caching, no global state). Populates
// EnclosingNodeID + EnclosingSignature when an enclosing node is found via
// the backend; leaves both empty when no match (the file isn't in the code
// graph yet, which is expected for fresh repos, OR the caller is the
// client-side intercept using NoOpBackend).
//
// A nil backend is tolerated as shorthand for NoOpBackend so callers in the
// pre-refactor early-bring-up path keep compiling without an explicit shim.
//
// When raws is empty, returns MatchResults with Matches=nil and Hint set to
// the LLM-facing guidance text.
func Hydrate(ctx context.Context, backend HydratorBackend, raws []RawMatch, walk WalkStats) (MatchResults, error) {
	out := MatchResults{
		Stats: Stats(walk),
	}
	if len(raws) == 0 {
		out.Hint = emptyResultHint
		return out, nil
	}
	if backend == nil {
		backend = NoOpBackend{}
	}

	// Collect unique file paths from raws so backends that can scope their
	// walk (e.g., the client-side query(file_symbols) call) can do one
	// round trip rather than walking the whole graph.
	seen := make(map[string]struct{}, len(raws))
	files := make([]string, 0, len(raws))
	for _, r := range raws {
		if r.FilePath == "" {
			continue
		}
		if _, dup := seen[r.FilePath]; dup {
			continue
		}
		seen[r.FilePath] = struct{}{}
		files = append(files, r.FilePath)
	}

	idx, err := buildCodeNodeIndex(ctx, backend, files)
	if err != nil {
		return MatchResults{}, err
	}

	out.Matches = make([]MatchResult, 0, len(raws))
	for _, r := range raws {
		mr := MatchResult{
			FilePath:  r.FilePath,
			StartLine: r.StartLine,
			EndLine:   r.EndLine,
			Captures:  r.Captures,
		}
		if node, ok := idx.lookup(r.FilePath, r.StartLine); ok {
			mr.EnclosingNodeID = node.GetId()
			mr.EnclosingSignature = node.GetSignature()
		}
		out.Matches = append(out.Matches, mr)
	}
	return out, nil
}

// buildCodeNodeIndex enumerates every function-ish node via the backend and
// indexes them per-file as line-range triples. NoOpBackend short-circuits to
// an empty index, leaving every Hydrate match with empty EnclosingNodeID.
// Returns a non-nil but empty index when the backend emits zero nodes rather
// than an error so Hydrate keeps emitting matches.
//
// Per-file slices are sorted by range size (smallest first) so range lookup
// picks the tightest enclosing scope when nodes nest.
func buildCodeNodeIndex(ctx context.Context, backend HydratorBackend, files []string) (*codeNodeIndex, error) {
	idx := &codeNodeIndex{byFile: make(map[string][]rangeEntry)}
	if err := backend.IterateFunctionish(ctx, files, func(n *knowledgev1.Node) error {
		if n.GetFilePath() == "" || n.GetStartLine() <= 0 {
			return nil
		}
		// EndLine 0 means "single-line" or "unknown end" — treat as
		// StartLine to avoid zero-length ranges that match everything.
		end := n.GetEndLine()
		if end <= 0 {
			end = n.GetStartLine()
		}
		idx.byFile[n.GetFilePath()] = append(idx.byFile[n.GetFilePath()], rangeEntry{
			startLine: int(n.GetStartLine()),
			endLine:   int(end),
			node:      n,
		})
		return nil
	}); err != nil {
		return nil, fmt.Errorf("ast/result: list code nodes: %w", err)
	}
	for file, entries := range idx.byFile {
		// Sort by range size ascending so the first containing entry
		// during lookup is the tightest enclosing scope (handles nested
		// closures / methods inside types).
		sort.Slice(entries, func(i, j int) bool {
			return (entries[i].endLine - entries[i].startLine) < (entries[j].endLine - entries[j].startLine)
		})
		idx.byFile[file] = entries
	}
	return idx, nil
}

// lookup finds the smallest function-ish node whose line range encloses
// the given (file, line). Returns false when no node contains the line.
//
// Implementation: per-file slices are pre-sorted by range size ascending so
// the first containing entry IS the tightest enclosing scope.
func (idx *codeNodeIndex) lookup(file string, line int) (*knowledgev1.Node, bool) {
	entries, ok := idx.byFile[file]
	if !ok {
		return nil, false
	}
	// ±1 line tolerance on both bounds absorbs the 0-based / 1-based
	// off-by-one between tree-sitter and the chunker's stored StartLine.
	// Same discipline as the prior key-based lookup.
	for _, e := range entries {
		if line >= e.startLine-1 && line <= e.endLine+1 {
			return e.node, true
		}
	}
	return nil, false
}
