// SPDX-License-Identifier: Apache-2.0

package engine

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// render_search.go holds the search-result renderers, relocated from
// cmd/knowledge/internal/tools/search_rerank.go into the neutral engine package
// so the Phase-4 dispatcher (engine.Render) can render WITHOUT importing
// internal/tools — the tools-side InterceptSearch/Query reroute through
// engine.Dispatch, so engine→tools would be a cycle (finding 3bdc9695). The
// tools-side rerank path (applyClientRerank) and intercept_query_decisions now
// import these from engine. The bodies are unchanged — this is relocation, not
// duplication.

// SearchJSONResult is the client-local JSON shape for --format json search
// output. Relocated from pkg/store/search_json_result.go (T5.5): this envelope
// is CLIENT-ONLY and never crosses the wire — renderJSON builds it for tool
// output and HydrateFromJSON re-reads it for the client rerank + decisions
// narrowing. The real search wire is the typed HydratedResult/Node proto
// (engine_decode.go), NOT this struct. The JSON tags are preserved verbatim so
// the --format json output shape stays stable.
type SearchJSONResult struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Type        string            `json:"type"`
	Score       float64           `json:"score"`
	SymbolName  string            `json:"symbol_name,omitempty"`
	Signature   string            `json:"signature,omitempty"`
	Summary     string            `json:"summary,omitempty"`
	Keywords    string            `json:"keywords,omitempty"`
	TestKind    string            `json:"test_kind,omitempty"`
	Description string            `json:"description,omitempty"`
	Source      string            `json:"source,omitempty"`
	Content     string            `json:"content,omitempty"`
	FilePath    string            `json:"file_path,omitempty"`
	Line        int               `json:"line,omitempty"`
	Repo        string            `json:"repo,omitempty"`
	Language    string            `json:"language,omitempty"`
	Status      string            `json:"status,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

// SearchJSONResponse is the client-local envelope wrapping the JSON search
// rows. Relocated from pkg/store/search_json_result.go (T5.5) for the same
// client-only reason as SearchJSONResult: it is built by renderJSON and
// consumed by HydrateFromJSON entirely client-side; it never crosses the wire.
type SearchJSONResponse struct {
	Query   string             `json:"query"`
	Total   int                `json:"total"`
	Results []SearchJSONResult `json:"results"`
}

// RenderForCaller re-renders a hydrated (and optionally reranked) slice for the
// caller, switching on the originally-requested format. Mirrors the server-side
// renderer's shape so the engine path is LLM-facing-equivalent to the legacy
// search path.
func RenderForCaller(query string, results []SearchResult, format string, fields []string) kgtools.ToolResult {
	switch format {
	case "json":
		return renderJSON(query, results, fields)
	default:
		return renderText(query, results)
	}
}

// renderSearchResponse decodes an engine ExecuteResponse's search blob and
// renders it for the caller with the mode-label suffix applied. It is the
// search arm of engine.Render.
func renderSearchResponse(resp *knowledgev1.ExecuteResponse, query, format string, fields []string, mode knowledgev1.SearchMode) (kgtools.ToolResult, error) {
	results, err := decodeSearch(resp)
	if err != nil {
		return kgtools.ToolResult{}, err
	}
	return RenderForCaller(labelForSearch(query, mode), results, format, fields), nil
}

// renderSearchResponseFiltered is renderSearchResponse plus a client-side
// resource_type prefix post-filter (T-GTB2 site (c)). The cloud/cicd
// resource_type filter does NOT compose with a QSearch post-rank server-side
// (T-GTB1), so the client trims the decoded SearchList here. An empty prefix is
// a no-op (RenderForCaller renders the full set).
func renderSearchResponseFiltered(resp *knowledgev1.ExecuteResponse, query, format string, fields []string, mode knowledgev1.SearchMode, resourceType string) (kgtools.ToolResult, error) {
	results, err := decodeSearch(resp)
	if err != nil {
		return kgtools.ToolResult{}, err
	}
	results = filterByResourceTypePrefix(results, resourceType)
	return RenderForCaller(labelForSearch(query, mode), results, format, fields), nil
}

// filterByResourceTypePrefix trims the result set to those whose resource_type
// metadata begins with prefix — the VERBATIM behavior of the server-side
// srvtools.FilterCloudResultsByResourceType (tools_query_cloud.go:226) the
// engine post-filter previously applied. An empty prefix is a no-op (returns the
// input). T-GTB2 site (c) moved this trim from the engine search post-rank to
// the client render path (OP_PREFIX is inert on a QSearch — T-GTB1).
func filterByResourceTypePrefix(results []SearchResult, prefix string) []SearchResult {
	if prefix == "" {
		return results
	}
	filtered := make([]SearchResult, 0, len(results))
	for _, r := range results {
		if strings.HasPrefix(kgtypes.Value(r.Node, "resource_type"), prefix) {
			filtered = append(filtered, r)
		}
	}
	return filtered
}

// labelForSearch appends the server's mode-label suffix to the query label:
// " (PPR graph-reach)" for SearchMode_PPR (tools_search.go:242) and
// " (recency-boosted)" for SearchMode_TEMPORAL (tools_search.go:244 /
// tools_query_search_modes.go:58). The hybrid/default mode adds no suffix. This
// is the ONE net-new render helper — the suffix is appended inline server-side
// with no reusable analog.
func labelForSearch(query string, mode knowledgev1.SearchMode) string {
	switch mode {
	case knowledgev1.SearchMode_SEARCH_MODE_PPR:
		return query + " (PPR graph-reach)"
	case knowledgev1.SearchMode_SEARCH_MODE_TEMPORAL:
		return query + " (recency-boosted)"
	default:
		return query
	}
}

// renderJSON re-packs the results as a SearchJSONResponse envelope. When
// fields is non-empty, projects each result down to the requested keys (mirrors
// the server-side projectSearchResult shape).
func renderJSON(query string, results []SearchResult, fields []string) kgtools.ToolResult {
	if len(fields) > 0 {
		return renderJSONProjected(query, results, fields)
	}
	resp := SearchJSONResponse{
		Query:   query,
		Total:   len(results),
		Results: make([]SearchJSONResult, len(results)),
	}
	for i, r := range results {
		name := r.Node.SymbolName
		if name == "" {
			name = r.Node.Description
		}
		resp.Results[i] = SearchJSONResult{
			ID:          r.Node.Id,
			Name:        name,
			Type:        r.Node.Type,
			Score:       r.Score,
			SymbolName:  r.Node.SymbolName,
			Signature:   r.Node.Signature,
			Summary:     r.Node.Summary,
			Keywords:    r.Node.Keywords,
			TestKind:    r.Node.TestKind,
			Description: r.Node.Description,
			Source:      r.Node.Source,
			Content:     r.Node.Content,
			FilePath:    r.Node.FilePath,
			Line:        int(r.Node.StartLine),
			Language:    r.Node.Language,
			Status:      r.Node.Status,
			Metadata:    r.Node.Metadata,
		}
	}
	data, err := json.Marshal(resp)
	if err != nil {
		return errorResult("render JSON: " + err.Error())
	}
	return kgtools.TextResult(string(data))
}

// renderJSONProjected emits a projected map per result containing only the
// requested keys. Mirrors the server-side projectSearchResult vocabulary.
func renderJSONProjected(query string, results []SearchResult, fields []string) kgtools.ToolResult {
	type projectedResponse struct {
		Query   string           `json:"query"`
		Total   int              `json:"total"`
		Results []map[string]any `json:"results"`
	}
	resp := projectedResponse{
		Query:   query,
		Total:   len(results),
		Results: make([]map[string]any, len(results)),
	}
	for i, r := range results {
		resp.Results[i] = projectHydratedResult(r, fields)
	}
	data, err := json.Marshal(resp)
	if err != nil {
		return errorResult("render JSON projection: " + err.Error())
	}
	return kgtools.TextResult(string(data))
}

// projectHydratedResult maps a HydratedResult to a key-projected map according
// to the requested field list. Unknown field names are silently dropped.
// Mirrors the server-side projectSearchResult shape.
func projectHydratedResult(r SearchResult, fields []string) map[string]any {
	out := make(map[string]any, len(fields))
	for _, f := range fields {
		switch f {
		case "id":
			out["id"] = r.Node.Id
		case "name":
			name := r.Node.SymbolName
			if name == "" {
				name = r.Node.Description
			}
			out["name"] = name
		case "type":
			out["type"] = r.Node.Type
		case "score":
			out["score"] = r.Score
		case "description":
			out["description"] = r.Node.Description
		case "source":
			out["source"] = r.Node.Source
		case "status":
			out["status"] = r.Node.Status
		case "symbol_name":
			out["symbol_name"] = r.Node.SymbolName
		case "signature":
			out["signature"] = r.Node.Signature
		case "summary":
			out["summary"] = r.Node.Summary
		case "keywords":
			out["keywords"] = r.Node.Keywords
		case "test_kind":
			out["test_kind"] = r.Node.TestKind
		case "file_path":
			out["file_path"] = r.Node.FilePath
		case "line":
			out["line"] = r.Node.StartLine
		case "language":
			out["language"] = r.Node.Language
		case "metadata":
			if len(r.Node.Metadata) > 0 {
				out["metadata"] = r.Node.Metadata
			}
		default:
			if key, ok := strings.CutPrefix(f, "metadata."); ok {
				if v, ok := r.Node.Metadata[key]; ok {
					out[f] = v
				}
			}
		}
	}
	return out
}

// renderText emits a compact markdown summary of the result slice. Mirrors the
// server-side formatSearchResults shape closely enough that a caller in text
// mode sees a comparable response.
func renderText(query string, results []SearchResult) kgtools.ToolResult {
	var sb strings.Builder
	if query != "" {
		fmt.Fprintf(&sb, "Search: %s (%d results)\n", query, len(results))
	} else {
		fmt.Fprintf(&sb, "Search: %d results\n", len(results))
	}
	for i, r := range results {
		name := r.Node.SymbolName
		if name == "" {
			name = r.Node.Description
		}
		if name == "" {
			name = r.Node.Id
		}
		fmt.Fprintf(&sb, "%d. [%s] %s (score=%.3f)\n", i+1, r.Node.Type, name, r.Score)
		if r.Node.FilePath != "" {
			fmt.Fprintf(&sb, "   %s:%d\n", r.Node.FilePath, r.Node.StartLine)
		}
		if r.Node.Summary != "" {
			fmt.Fprintf(&sb, "   %s\n", r.Node.Summary)
		}
	}
	return kgtools.TextResult(sb.String())
}

// HydrateFromJSON deserializes a SearchJSONResponse envelope into
// []SearchResult. Maps every field on SearchJSONResult including Metadata
// (load-bearing — augmentCallerHints populates the "callers" key server-side and
// the rerank package consumes kgtypes.Value(n, "callers")). Builds the typed
// *knowledgev1.Node directly (T5/FUL-295 dropped the store.Node wrapper layer from
// the client read path). Relocated from tools so intercept_query_decisions +
// applyClientRerank import it here.
func HydrateFromJSON(body string) ([]SearchResult, error) {
	var env SearchJSONResponse
	if err := json.Unmarshal([]byte(body), &env); err != nil {
		return nil, fmt.Errorf("decode search JSON envelope: %w", err)
	}
	out := make([]SearchResult, len(env.Results))
	for i, r := range env.Results {
		symbol := r.SymbolName
		if symbol == "" {
			symbol = r.Name
		}
		out[i] = SearchResult{
			Score: r.Score,
			Node: &knowledgev1.Node{
				Id:          r.ID,
				SymbolName:  symbol,
				Type:        r.Type,
				Description: r.Description,
				Source:      r.Source,
				Status:      r.Status,
				Signature:   r.Signature,
				Summary:     r.Summary,
				Keywords:    r.Keywords,
				TestKind:    r.TestKind,
				FilePath:    r.FilePath,
				StartLine:   int32(r.Line),
				Language:    r.Language,
				Content:     r.Content,
				Metadata:    r.Metadata,
			},
		}
	}
	return out, nil
}

// FirstTextContent extracts the first text content block from a ToolResult.
// Returns "" when no text content is present. Relocated from tools.
func FirstTextContent(resp kgtools.ToolResult) string {
	for _, c := range resp.Content {
		if c.Type == "text" {
			return c.Text
		}
	}
	return ""
}

// errorResult builds an error-flagged ToolResult. Local to the engine package
// (the tools-side errorResult, manage.go:225, is not importable here without a
// cycle).
func errorResult(msg string) kgtools.ToolResult {
	return kgtools.ToolResult{
		Content: []kgtools.ContentBlock{{Type: "text", Text: msg}},
		IsError: true,
	}
}
