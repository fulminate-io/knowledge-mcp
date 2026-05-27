// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"

	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// queryModesNeedingEmbedding is the set of query(mode=...) values that
// trigger a server-side vector search and therefore benefit from a
// client-supplied query_vector. The default-empty mode behaves like
// "hybrid"; modes that never look at the embedder (stats, examine,
// lineage, evidence, plan_tree, topology, modules, personality, ...)
// are left untouched. Single source of truth so the gate stays
// discoverable and testable.
var queryModesNeedingEmbedding = map[string]struct{}{
	"":       {}, // default = hybrid
	"hybrid": {},
}

// InterceptQuery is the cmd/knowledge stdio client's "query" interceptor.
// Mirrors InterceptSearch's Phase-4.5 client-side embedding behavior: when
// deps.Embedder() is non-nil AND the query mode is in
// queryModesNeedingEmbedding AND the args carry a non-empty text query AND
// the caller did not already supply query_vector, the query text is
// embedded locally and the bytes are forwarded via query_vector. The
// server-side compositor short-circuits its own embed call, so post-BCN5
// servers (no Voyage key) still return vector-quality results.
//
// Returns (handled, result). When the gate misses (wrong mode, no
// embedder, empty query, etc.) returns (false, _) so the next chain
// step (or the bare server call) takes over with the ORIGINAL params.
func InterceptQuery(deps ClientDeps, params kgtools.CallToolParams) (bool, kgtools.ToolResult) {
	if params.Name != "query" {
		return false, kgtools.ToolResult{}
	}
	ctx := context.Background()
	emb := deps.Embedder()
	if emb == nil {
		return false, kgtools.ToolResult{}
	}
	mode, ok := queryModeFromArgs(params.Arguments)
	if !ok {
		return false, kgtools.ToolResult{}
	}
	if _, eligible := queryModesNeedingEmbedding[mode]; !eligible {
		return false, kgtools.ToolResult{}
	}
	args, didEmbed := maybeEmbedQuery(ctx, emb, params.Arguments)
	if !didEmbed {
		return false, kgtools.ToolResult{}
	}
	gc := deps.GraphClient()
	if !gc.Healthy() {
		return true, errorResult("server unreachable; start it with `knowledge-server`")
	}
	// Route the tail through the compile-or-DENY dispatcher: a reducible query
	// compiles to Engine.Execute; an unrecognized shape is denied legibly (there is
	// no wire fallback). The query-domain + query-rendering intercepts already claim
	// every specialized query mode upstream in the chain,
	// so only the reducible read modes reach here. The client embed pre-step above
	// is preserved verbatim — the embedded query_vector rides on the args the
	// compiler lowers into the plan.
	resp, err := engine.Dispatch(ctx, gc.Execute, "query", args)
	if err != nil {
		return true, errorResult("query call failed: " + err.Error())
	}
	return true, resp
}

// queryModeFromArgs decodes the "mode" field from a query tool's args.
// Returns ("", true) when the field is absent (default-hybrid mode);
// returns (val, true) for any string value; returns ("", false) only on
// JSON decode failure (caller falls through to the bare server call).
func queryModeFromArgs(args json.RawMessage) (string, bool) {
	var obj struct {
		Mode string `json:"mode"`
	}
	if err := json.Unmarshal(args, &obj); err != nil {
		return "", false
	}
	return obj.Mode, true
}
