// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"

	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// graphClientHydratorBackend implements ast.HydratorBackend by issuing a
// single query(mode:"file_symbols", format:"json", path_prefixes:[...]) call
// to the server. Bulk fetch — one round trip per Hydrate call regardless of
// match count, dropping the N+1 round trips that a per-file lookup would
// require.
//
// The client-side ast intercept builds one of these per Match call (with the
// repo from the trigger payload) and passes it to ast.Hydrate. Hydrate
// extracts unique file paths from the raw match set and feeds them in via the
// files arg.
//
// The gc field is the narrow Execute-only GraphCaller interface
// so the hydrator routes per-call to local or cloud via the same Router every
// other tool consumes. Narrowing from *graphclient.GraphClient also prevents
// callers from re-introducing the concrete-type dependency.
// The branch field carries the active branch overlay. Without it hydration reads
// the BASE graph even when the branch overlay is what holds the files: the ast
// tool is not in codeGraphToolNames, so the repo intercept never stamps a branch
// into its args, and astArgs has no branch field — the value is derived
// in-process at the construction site instead.
type graphClientHydratorBackend struct {
	gc     GraphCaller
	repo   string
	branch string
}

// IterateFunctionish collects the function-ish symbols for the union of file
// paths via the Execute carrier seam and emits each via fn. It reuses the same
// bounded per-tool-call collector the file_symbols intercept uses
// (fileSymbolsCollector: file ByID + CONTAINS-forward traverse + ONE bulk ids[]
// hydrate, all via Execute+engine.DecodeNodes) — the file_symbols query MODE is
// specialized (not engine-reducible), so the collection rides the reducible
// by-id/traverse/hydrate primitives instead. ONE collector serves every file in
// the call, so the suffix fallback's file index is drained at most once per
// Hydrate rather than once per file. Empty files = no-op (Hydrate has nothing to
// look up; the index stays empty and matches get empty EnclosingNodeID).
// Best-effort: a missing file node (fresh repo, no collect run) contributes
// nothing rather than aborting the whole match.
func (b graphClientHydratorBackend) IterateFunctionish(ctx context.Context, files []string, fn func(*knowledgev1.Node) error) error {
	if len(files) == 0 || b.gc == nil {
		return nil
	}
	// Re-stamp over the tool-level ast term: hydration issues one read per
	// matched FILE, so its cost scales with the match set rather than with the
	// single tool call, and it is worth separating in the metrics.
	ctx = graphclient.WithOperation(ctx, graphclient.OpAstHydrate)
	target := &knowledgev1.GraphSelector{Graph: "code", Repo: b.repo, Branch: b.branch}
	c := newFileSymbolsCollector(b.gc.Execute, target, false)
	for _, file := range files {
		nodes := c.collect(ctx, file)
		for i := range nodes {
			if !isFunctionishType(nodes[i].Type) {
				continue
			}
			if err := fn(nodes[i]); err != nil {
				return err
			}
		}
	}
	return nil
}

// functionishTypeSet mirrors the types listed in cmd/knowledge/internal/ast/
// result.go's functionishTypes — we filter client-side after the server returns ALL
// symbols for the requested files. Cheap; the response is bounded by the
// match-set's file count, not the whole graph.
var functionishTypeSet = map[string]struct{}{
	"function_declaration": {},
	"method_declaration":   {},
	"function_definition":  {},
	"method_definition":    {},
	"function_item":        {},
	"function":             {},
	"func_literal":         {},
}

func isFunctionishType(t string) bool {
	_, ok := functionishTypeSet[t]
	return ok
}
