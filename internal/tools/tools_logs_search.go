// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// searchLogs reads an ephemeral log graph identified by Name (the log
// pipeline's queryID) and filters it with a case-insensitive substring
// match. Results are filtered to NodeLogTemplate
// because templates are the primary user-facing unit — chunks hold
// compressed payloads and streams are label buckets, neither of which
// is useful as a plain search hit.
//
// Client-side handler. The search runs against the server via
// gc.Call("query", graph:"logs", text:..., name:..., format:"json").
// Server-side searchLogs is gone — the dispatch returns
// errLogsHandledClientSide so older clients see the move.
//
// No vector fallback: log graphs are excluded from the embedder pipeline
// (see store.SkipsLLMProcessing), so HNSW is never populated for them.
// There is no server-side BM25 index for a log graph either. What makes a
// template findable is the client-side case-insensitive substring filter
// in filterLogHitsBySubstring, which tests SymbolName, metadata["summary"]
// and metadata["pattern"] — so a template stored with SymbolName=Pattern
// plus severity metadata is reachable by any fragment of those three.
func (h *Handler) searchLogs(ctx context.Context, a searchArgs) kgtools.ToolResult {
	if strings.TrimSpace(a.Name) == "" {
		return kgtools.ErrorResult("name is required for graph=logs (pass the queryID returned by log collection)")
	}
	queries := mergeQueries(a.Query, a.Queries)
	if len(queries) == 0 {
		return kgtools.ErrorResult("query or queries is required for log graph search")
	}
	gc := h.graphCaller()
	if gc == nil {
		return kgtools.ErrorResult("search logs: no GraphCaller configured")
	}

	limit := int(a.Limit)
	if limit <= 0 {
		limit = 10
	}

	// Pre-flight existence check: a minimal query against the named log
	// graph should resolve. Reaching for a missing graph surfaces "not
	// found" instead of silently returning zero matches.
	if err := checkLogGraphExists(ctx, gc, a.Name); err != nil {
		return kgtools.ErrorResult(err.Error())
	}

	results := runLogSearchQueries(ctx, gc, a.Name, queries, limit)
	if len(results) == 0 {
		return kgtools.TextResult(fmt.Sprintf("No log template matches in graph %q for: %s",
			a.Name, strings.Join(queries, ", ")))
	}
	if a.Format == "json" {
		return engine.RenderForCaller(strings.Join(queries, " | "), logHitsToResults(a.Name, results), "json", nil, "")
	}
	return formatLogSearchResults(a.Name, strings.Join(queries, " | "), results)
}

// logHitsToResults projects each logSearchHit into an engine.SearchResult for the
// format:"json" arm: the template id, its pattern (SymbolName), score, and the
// full metadata map (severity/count/first_seen/last_seen — what the text path
// surfaces per entry) ride through renderJSON's verbatim Metadata copy. Each row
// is stamped with its source-graph identity: Graph:"logs" and
// GraphInstance:name (the log queryID), so the universal contract holds for the
// logs family too.
func logHitsToResults(name string, hits []logSearchHit) []engine.SearchResult {
	out := make([]engine.SearchResult, len(hits))
	for i, h := range hits {
		out[i] = engine.SearchResult{
			Node: &knowledgev1.Node{
				Id:         h.id,
				SymbolName: h.symbolName,
				Metadata:   h.metadata,
			},
			Score:         h.score,
			Graph:         "logs",
			GraphInstance: name,
		}
	}
	return out
}

// logSearchHit is the minimal per-result view the formatter needs. Mirrors
// engine.SearchResult{Node, Score} but uses a generic-decoded map so the
// JSON pipeline doesn't need to depend on store types.
type logSearchHit struct {
	id         string
	symbolName string
	score      float64
	metadata   map[string]string
}

// runLogSearchQueries executes each query and returns the deduplicated,
// template-only result list. Per-query failures are swallowed so one bad
// query doesn't abort the batch. Returning early on error would have
// hidden partial results the user could still act on.
func runLogSearchQueries(
	ctx context.Context, gc GraphCaller, name string, queries []string, limit int,
) []logSearchHit {
	var out []logSearchHit
	seen := make(map[string]bool)
	// Over-fetch because non-template hits get filtered out post-query.
	fetch := max(limit*5, limit)
	for _, q := range queries {
		hits, err := queryLogSearch(ctx, gc, name, q, fetch)
		if err != nil {
			continue
		}
		for _, r := range hits {
			if r.metadata["__type"] != "log-template" {
				continue
			}
			if seen[r.id] {
				continue
			}
			seen[r.id] = true
			out = append(out, r)
			if len(out) >= limit {
				return out
			}
		}
	}
	return out
}

// queryLogSearch fetches EVERY log-template node in the graph via
// fetchLogNodesByType (which drains keyset pages over the raw plan) and filters
// them client-side by substring match on SymbolName + summary fields.
// Per the "server stores nodes, client translates" lock: ranking + text
// filter is translation work; server only serves the raw nodes. Server-
// side `text:` against graph=logs is the label-filter parser (key=value
// / severity>=LEVEL), not BM25 — using it would re-route through
// InterceptLogsQuery's parser and reject free text.
//
// The client-side substring filter is only as complete as the set it filters,
// so this reads the whole type rather than one bounded page. The engine Match
// selects on the CANONICAL node type: kgtypes.NodeLogTemplate ("log-template")
// — the historical hardcoded "log_template" was an inert selector the client
// __type filter compensated for; the carrier Match needs the real type string.
func queryLogSearch(ctx context.Context, gc GraphCaller, name, q string, limit int) ([]logSearchHit, error) {
	nodes, err := fetchLogNodesByType(ctx, gc, name, string(kgtypes.NodeLogTemplate), false)
	if err != nil {
		return nil, fmt.Errorf("query: %w", err)
	}
	all := logSearchHitsFromNodes(nodes)
	// Tag every hit so the caller's template filter still works; the
	// type-browse RPC already returned log-template nodes so this is
	// belt-and-suspenders for the metadata["__type"] check.
	for i := range all {
		if all[i].metadata == nil {
			all[i].metadata = map[string]string{}
		}
		if all[i].metadata["__type"] == "" {
			all[i].metadata["__type"] = "log-template"
		}
	}
	return filterLogHitsBySubstring(all, q, limit), nil
}

// filterLogHitsBySubstring keeps hits whose SymbolName or summary
// metadata field contains q (case-insensitive). Cheap stand-in for
// BM25; matches the "server is the database, client interprets"
// principle. limit==0 returns every match.
func filterLogHitsBySubstring(hits []logSearchHit, q string, limit int) []logSearchHit {
	needle := strings.ToLower(strings.TrimSpace(q))
	if needle == "" {
		return hits
	}
	out := make([]logSearchHit, 0, len(hits))
	for _, h := range hits {
		if strings.Contains(strings.ToLower(h.symbolName), needle) ||
			strings.Contains(strings.ToLower(h.metadata["summary"]), needle) ||
			strings.Contains(strings.ToLower(h.metadata["pattern"]), needle) {
			out = append(out, h)
			if limit > 0 && len(out) >= limit {
				return out
			}
		}
	}
	return out
}

// logSearchHitsFromNodes projects drained log-template nodes into logSearchHits.
// Score is 0 (a type-browse carries no rank; the template filter is
// substring-based, score-independent). The node Type is stashed under the
// synthetic "__type" metadata key so callers filter template-only without a
// separate field.
func logSearchHitsFromNodes(nodes []*knowledgev1.Node) []logSearchHit {
	out := make([]logSearchHit, 0, len(nodes))
	for _, n := range nodes {
		md := make(map[string]string, len(n.Metadata)+1)
		maps.Copy(md, n.Metadata)
		md["__type"] = n.Type
		out = append(out, logSearchHit{
			id:         n.Id,
			symbolName: n.SymbolName,
			metadata:   md,
		})
	}
	return out
}

// checkLogGraphExists issues a minimal query to confirm the named log
// graph is present in the store. Returns a clear "not found" error when
// the graph doesn't exist so callers can distinguish missing graphs from
// zero-match searches.
func checkLogGraphExists(ctx context.Context, gc GraphCaller, name string) error {
	args, err := json.Marshal(map[string]any{
		"graph": "logs",
		"name":  name,
		"type":  string(kgtypes.NodeLogTemplate),
		"limit": 1,
	})
	if err != nil {
		return fmt.Errorf("marshal existence check: %w", err)
	}
	// The existence probe rides the Execute carrier seam (logs type-browse). An
	// Execute error surfaces the server's "not found" for unknown query_ids.
	if _, err := executeQuery(ctx, gc, args); err != nil {
		return fmt.Errorf("log graph %q not found: %w", name, err)
	}
	return nil
}

// formatLogSearchResults renders log templates as markdown. The rows are NOT
// ranked: they arrive in drain order, and every hit carries a zero score
// because the substring filter produces no rank to print. The pattern is the
// template's SymbolName; severity, count, first_seen, and last_seen come from
// metadata. Truncation keeps long patterns from blowing out the chat width.
func formatLogSearchResults(
	name, query string, results []logSearchHit,
) kgtools.ToolResult {
	var sb strings.Builder
	fmt.Fprintf(&sb, "## Log templates in %q matching %q (%d result%s)\n\n",
		name, query, len(results), pluralSuffix(len(results)))
	for i, r := range results {
		formatLogTemplateResult(&sb, i+1, r)
	}
	return kgtools.TextResult(sb.String())
}

// formatLogTemplateResult writes one template entry to sb. Extracted so
// formatLogSearchResults stays under the 30-complexity budget and the
// structure of each entry is easy to adjust in one place.
func formatLogTemplateResult(sb *strings.Builder, index int, r logSearchHit) {
	pattern := r.symbolName
	if pattern == "" {
		pattern = r.metadata["pattern"]
	}
	fmt.Fprintf(sb, "%d. %s (score: %.3f)\n", index, truncate(pattern, 160), r.score)
	if sev := r.metadata["severity"]; sev != "" {
		fmt.Fprintf(sb, "   Severity: %s\n", sev)
	}
	if count := r.metadata["count"]; count != "" {
		fmt.Fprintf(sb, "   Count: %s\n", count)
	}
	if first := r.metadata["first_seen"]; first != "" {
		fmt.Fprintf(sb, "   First seen: %s\n", first)
	}
	if last := r.metadata["last_seen"]; last != "" {
		fmt.Fprintf(sb, "   Last seen: %s\n", last)
	}
	fmt.Fprintf(sb, "   ID: %s\n\n", r.id)
}

// pluralSuffix returns "s" when n != 1. Keeps result headers grammatical
// without sprinkling ternary expressions through the format code.
func pluralSuffix(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// mergeQueries combines the legacy single-query field with the queries
// slice and dedupes, dropping empty entries. Mirrors the server-side
// helper at cmd/knowledge-server/tools/tools_search_source.go.
func mergeQueries(query string, queries []string) []string {
	// Cap on len(queries) alone (plus the single `query`, which append grows
	// for free). Computing `1+len(queries)` as the allocation size could
	// overflow int for a pathologically large slice (CWE-190); a bare len()
	// can't overflow, so use it directly as the hint.
	out := make([]string, 0, len(queries))
	seen := map[string]bool{}
	add := func(q string) {
		q = strings.TrimSpace(q)
		if q == "" || seen[q] {
			return
		}
		seen[q] = true
		out = append(out, q)
	}
	add(query)
	for _, q := range queries {
		add(q)
	}
	return out
}

// truncate clips s to n runes and appends an ellipsis when it overflows.
// Mirrors the server-side helper at cmd/knowledge-server/tools/helpers.go.
func truncate(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "..."
}
