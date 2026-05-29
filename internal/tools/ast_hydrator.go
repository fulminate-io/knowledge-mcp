// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"

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
// The gc field is the narrow Execute-only GraphCaller interface (post-FUL-323)
// so the hydrator routes per-call to local or cloud via the same Router every
// other tool consumes. Narrowing from *graphclient.GraphClient also prevents
// callers from re-introducing the concrete-type dependency.
type graphClientHydratorBackend struct {
	gc   GraphCaller
	repo string
}

// IterateFunctionish collects the function-ish symbols for the union of file
// paths via the Execute carrier seam and emits each via fn. It reuses the same
// bounded file-symbols collector the file_symbols intercept uses
// (collectFileSymbolsClient: file ByID + CONTAINS-forward traverse + ONE bulk
// ids[] hydrate, all via Execute+engine.DecodeNodes) — the file_symbols query
// MODE is specialized (not engine-reducible), so the collection rides the
// reducible by-id/traverse/hydrate primitives instead. Empty files = no-op
// (Hydrate has nothing to look up; the index stays empty and matches get empty
// EnclosingNodeID). Best-effort: a missing file node (fresh repo, no collect
// run) contributes nothing rather than aborting the whole match.
func (b graphClientHydratorBackend) IterateFunctionish(ctx context.Context, files []string, fn func(*knowledgev1.Node) error) error {
	if len(files) == 0 || b.gc == nil {
		return nil
	}
	target := &knowledgev1.GraphSelector{Graph: "code", Repo: b.repo}
	for _, file := range files {
		nodes := collectFileSymbolsClient(ctx, b.gc.Execute, target, file, false)
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
