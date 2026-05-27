// SPDX-License-Identifier: Apache-2.0

package engine

import (
	"encoding/json"
	"fmt"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// recentHalfLifeDays is the Temporal(30) half-life for the recent search mode.
// The engine calls q.Temporal(p.GetHalfLife()), so the client passes 30 explicitly
// to drive the LLM-facing recency-boost output (a zero half_life means "no
// temporal decay").
const recentHalfLifeDays = 30.0

// browseDefaultLimit is the LLM-facing friendly cap a no-limit query-tool
// BROWSE read (type-browse, plural-types browse, meta-only browse) compiles to.
// It is 10 to match the search compositor's self-default — the one consistent
// value across the LLM-facing surface (decision 60493b414d3014983b2112c2337e1537).
//
// FUL-302: this default lives CLIENT-SIDE because only the compile layer knows
// the user-facing intent. The server now honors the proto contract literally
// (limit==0 = no cap, engine.proto:520-521) and injects NO default, so an
// internal Match-all helper that explicitly wants everything (FetchAllNodes,
// dispatchGraphWideEdges, pivotFetchNodesClient, the logs Match-all) gets the
// whole graph. If this client default were dropped, a no-limit LLM browse would
// send limit==0 → unbounded → the entire graph; applyBrowseLimitOffset is the
// guard that keeps the user-facing browse capped at 10.
const browseDefaultLimit = 10

// queryArgs is the compile-local view of the `query` tool's wire shape,
// mirroring the server-side queryArgs (tools_query_args.go) for the fields the
// reducible read path consumes. Thought-graph filter fields (valence/magnitude/
// consistency/session/connected_to/cluster*/since/action/target/polarity/
// weight) are intentionally omitted: a query carrying any of them is a recall/
// reflect shape (SPECIALIZED), denied by hasThoughtFilter below.
type queryArgs struct {
	Graph             string            `json:"graph"`
	Name              string            `json:"name"`
	ID                string            `json:"id"`
	IDs               []string          `json:"ids"`
	Text              string            `json:"text"`
	Type              string            `json:"type"`
	Types             []string          `json:"types"`
	Status            string            `json:"status"`
	Mode              string            `json:"mode"`
	Language          string            `json:"language"`
	Account           string            `json:"account"`
	Repo              string            `json:"repo"`
	Branch            string            `json:"branch"`
	Limit             int               `json:"limit"`
	Offset            int               `json:"offset"`
	Meta              map[string]string `json:"meta"`
	IncludeEdges      *bool             `json:"include_edges"`
	IncludeCrossLinks *bool             `json:"include_cross_links"`
	QueryVector       string            `json:"query_vector"`
	IncludeTombstones bool              `json:"include_tombstones"`

	// ContentB64 opts the NodeList carrier into the binary-safe content_b64
	// encode (T-GTB1e #1): the engine base64-encodes each non-empty Node.Content
	// before marshaling nodes_json, and the caller decodes via
	// DecodeNodesContentB64. Threaded onto the type-browse + ids[] arms (the
	// log-chunk fetch shapes). Empty/false = Content rides as the raw string.
	ContentB64 bool `json:"content_b64"`

	// Format/Fields are render-only (Compile ignores them); Render reads them
	// to pick text/json + projection.
	Format string   `json:"format"`
	Fields []string `json:"fields"`

	// Thought-graph filter fields — their PRESENCE alone makes the query a
	// recall/reflect shape, so they are parsed only to detect-and-deny.
	ValenceMin   *float64 `json:"valence_min"`
	ValenceMax   *float64 `json:"valence_max"`
	MagnitudeMin *float64 `json:"magnitude_min"`
	ConsistMax   *float64 `json:"consistency_max"`
	Session      string   `json:"session"`
	ConnectedTo  string   `json:"connected_to"`
}

// reducibleQueryModes is the set of query(mode=...) values the engine read path
// can serve. Everything else — stats/examine/topology/pivot/correlations/
// timeline/explain/resolver/metadata_stats/reflect (personality/influence/
// tensions/blind_spots/evolution/summary/simulate/charges/clusters)/lineage/
// evidence/plan_tree/modules/file_symbols — is SPECIALIZED (finding 457e861e)
// and falls through to legacy.
var reducibleQueryModes = map[string]struct{}{
	"":            {}, // default: id / ids / text / type / meta dispatch
	"text":        {}, // text search
	"graph_reach": {}, // PPR rerank
	"recent":      {}, // temporal decay rerank
	"modules":     {}, // list-graphs catalog enumeration (RETURN_MODE_GRAPH_NAMES)
}

// reducibleTextRequiredModes is the subset of reducibleQueryModes whose plan is
// PURELY a text search (Queries == [a.Text]): an empty text reduces them to a
// query with nothing to search. buildQueryPlan returns ok=false on empty text
// for exactly these, which post-cutover surfaces the GENERIC engine deny — an
// illegible "tool query is not a recognized engine-reducible shape" message. The
// precheckQuery seam (GAP-B, CEO decision: REQUIRE TEXT) intercepts the empty-text
// case BEFORE Compile and returns a specific validation error naming the mode.
//
// Deliberately EXCLUDED: "" (default mode dispatches on id/ids/type/meta — text is
// only one of several recognized shapes, so empty text is not an error there) and
// "modules" (catalog enumeration takes NO text — the GraphType is the only input).
var reducibleTextRequiredModes = map[string]struct{}{
	"text":        {},
	"graph_reach": {},
	"recent":      {},
}

// precheckQuery runs the empty-text validation for the text-required query modes
// CLIENT-SIDE. It is invoked by Dispatch's query branch BEFORE Compile (a NAMED
// special-shape seam): a non-nil error is rendered as an explicit
// validation-error result and NO Execute RPC is issued. Returns nil (no gate)
// for every shape that is not a text-required mode with empty text, so Dispatch
// proceeds to Compile unchanged. It is the only remaining client-side
// pre-Compile validation gate (the mutate(create) body precheck was relocated
// server-side in FUL-306).
//
// Message phrasing is fixed (`query mode "recent" requires a non-empty text
// query`) so the LLM-facing error text stays stable and legible — distinct from
// the generic post-cutover deny these modes used to fall through to.
func precheckQuery(args json.RawMessage) error {
	var a queryArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return nil //nolint:nilerr // malformed JSON is not a validation failure — let Compile (which re-parses) return ok=false so the deny path surfaces the parse error
	}
	if _, required := reducibleTextRequiredModes[a.Mode]; !required {
		return nil // not a text-required mode → no precheck.
	}
	if a.Text != "" {
		return nil // text present → the mode is fully formed.
	}
	return validationError(fmt.Sprintf("query mode %q requires a non-empty text query", a.Mode))
}

// compileQuery translates a reducible `query` read into a QueryPlan. Returns
// ok=false (default-deny → legacy) for graph=code/logs, any SPECIALIZED mode,
// and any thought-graph-filter shape.
func compileQuery(args json.RawMessage) (*knowledgev1.ExecuteRequest, bool) {
	var a queryArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, false
	}

	// code/logs deny is NARROWED to the SPECIALIZED id/text shapes only (T-GTB1e
	// #3): graph=code id→HandleAnalyzeNode, text→HandleSearchCode (client
	// intercepts); graph=logs id/text→the log engine. A plain type-browse /
	// plural-types-browse / ids[] read against code/logs is NOT specialized — the
	// engine serves it via ResolveGraphDB (tools_graph_routing.go), and buildTarget
	// already carries repo/name/branch — so it falls through to buildQueryPlan.
	if isCodeGraph(a.Graph) || a.Graph == "logs" {
		if a.ID != "" || a.Text != "" {
			return nil, false // specialized id→analyze / text→code-search / log engine.
		}
	}
	if _, ok := reducibleQueryModes[a.Mode]; !ok {
		return nil, false // SPECIALIZED mode.
	}
	if hasThoughtFilter(a) {
		return nil, false // recall/reflect shape — thoughts surface stays legacy.
	}

	plan, ok := buildQueryPlan(a)
	if !ok {
		return nil, false
	}
	req := &knowledgev1.ExecuteRequest{
		Plan:   &knowledgev1.ExecuteRequest_Query{Query: plan},
		Target: buildTarget(a.Graph, a.Repo, a.Account, a.Name, a.Language, a.Branch),
	}
	return req, true
}

// hasThoughtFilter reports whether the query carries any thought-graph filter
// field — its presence routes the legacy tool to routeRecall, a SPECIALIZED
// surface the engine does not serve.
func hasThoughtFilter(a queryArgs) bool {
	return a.ValenceMin != nil || a.ValenceMax != nil || a.MagnitudeMin != nil ||
		a.ConsistMax != nil || a.Session != "" || a.ConnectedTo != ""
}

// buildQueryPlan lowers the recognized read shape into a QueryPlan. Returns
// ok=false when no recognized shape is present (e.g. a mode-less query with no
// id/ids/text/type/meta).
func buildQueryPlan(a queryArgs) (*knowledgev1.QueryPlan, bool) {
	switch a.Mode {
	case "graph_reach":
		return searchModePlan(a, knowledgev1.SearchMode_SEARCH_MODE_PPR), a.Text != ""
	case "recent":
		p := searchModePlan(a, knowledgev1.SearchMode_SEARCH_MODE_TEMPORAL)
		p.HalfLife = recentHalfLifeDays
		return p, a.Text != ""
	case "text":
		return textSearchPlan(a), a.Text != ""
	case "modules":
		// List-graphs catalog enumeration: the engine's RETURN_MODE_GRAPH_NAMES
		// arm enumerates the graph CATALOG of the target GraphType (carried by the
		// envelope GraphSelector, built from a.Graph/Repo/Account/Name/Language).
		// No Selection, no queries — the GraphType is the only input.
		return &knowledgev1.QueryPlan{
			ReturnMode: knowledgev1.ReturnMode_RETURN_MODE_GRAPH_NAMES,
		}, true
	}
	// Default mode (""): id / ids / text / type / meta dispatch, mirroring the
	// server's handleGenericGraphQuery precedence (ids → id → text → type →
	// meta-only).
	return buildDefaultModePlan(a)
}

// searchModePlan builds a QSearch plan for the PPR / temporal rerank modes. The
// query_vector decodes into one QueryVecs entry (engine validates length);
// Limit rides only when supplied (engine owns the default).
func searchModePlan(a queryArgs, mode knowledgev1.SearchMode) *knowledgev1.QueryPlan {
	p := &knowledgev1.QueryPlan{
		Queries:    []string{a.Text},
		SearchMode: mode,
		ReturnMode: knowledgev1.ReturnMode_RETURN_MODE_SEARCH,
	}
	applyQueryVec(p, a.QueryVector)
	applyLimitOffset(p, a.Limit, a.Offset)
	applyTombstones(p, a.IncludeTombstones)
	return p
}

// textSearchPlan builds a plain hybrid QSearch plan for mode=text.
func textSearchPlan(a queryArgs) *knowledgev1.QueryPlan {
	p := &knowledgev1.QueryPlan{
		Queries:    []string{a.Text},
		ReturnMode: knowledgev1.ReturnMode_RETURN_MODE_SEARCH,
	}
	applyQueryVec(p, a.QueryVector)
	applyLimitOffset(p, a.Limit, a.Offset)
	applyTombstones(p, a.IncludeTombstones)
	return p
}

// buildDefaultModePlan handles the mode-less dispatch: bulk ids → single id →
// text → type-browse → meta-only. Returns ok=false when none is present.
func buildDefaultModePlan(a queryArgs) (*knowledgev1.QueryPlan, bool) {
	switch {
	case len(a.IDs) > 0:
		// Bulk hydrate: QueryPlan.Ids lowers to store.ByIDs (NodeList) — the
		// landed T2.4a carrier (engine_decode.go newQForPlan ids arm). Renders
		// as {nodes:[]}. ONE plan for the whole set (no N+1).
		p := &knowledgev1.QueryPlan{Ids: a.IDs}
		applyTombstones(p, a.IncludeTombstones)
		applyContentB64(p, a.ContentB64)
		return p, true
	case a.ID != "":
		// Single id → ByID (renders a BARE node). include_edges /
		// include_cross_links are NOT compiled onto the plan: the proto carries no
		// absorption flags. A query(id) carrying either flag is intercepted in
		// dispatch.go (dispatchQueryByID) BEFORE Compile and composed via multi-call
		// orchestration — it never reaches this single-plan arm. This arm handles
		// the PLAIN by-id read only.
		p := &knowledgev1.QueryPlan{ById: a.ID}
		applyTombstones(p, a.IncludeTombstones)
		return p, true
	case a.Text != "":
		// Generic cross-graph text search.
		return textSearchPlan(a), true
	case len(a.Types) > 0:
		// Plural-types browse → Match("") all-types enumeration + engine
		// Selection.NodeTypes post-filter (postFilterBrowseNodeTypes). DISTINCT
		// from the singular a.Type Match-index arm below: the plural set has no
		// single index browse, so it enumerates then trims to the type set. status
		// + meta predicates ride as on the singular arm.
		p := &knowledgev1.QueryPlan{Selection: browsePluralSelection(a.Types, a.Status, a.Meta)}
		applyBrowseLimitOffset(p, a.Limit, a.Offset)
		applyTombstones(p, a.IncludeTombstones)
		applyContentB64(p, a.ContentB64)
		return p, true
	case a.Type != "":
		// Type-browse → Match(NodeType). Selection carries node_type + status +
		// metadata predicates; Limit/Offset ride when supplied.
		p := &knowledgev1.QueryPlan{Selection: browseSelection(a.Type, a.Status, a.Meta)}
		applyBrowseLimitOffset(p, a.Limit, a.Offset)
		applyTombstones(p, a.IncludeTombstones)
		applyContentB64(p, a.ContentB64)
		return p, true
	case len(a.Meta) > 0:
		// Meta-only enumeration → Match("") + meta predicates (every node
		// matching the meta filter regardless of type).
		p := &knowledgev1.QueryPlan{Selection: browseSelection("", a.Status, a.Meta)}
		applyBrowseLimitOffset(p, a.Limit, a.Offset)
		applyTombstones(p, a.IncludeTombstones)
		return p, true
	}
	return nil, false
}

// browseSelection builds the Selection for a type-browse / meta-only plan:
// node_type + statuses + metadata predicates. The meta map lowers to
// MetadataPredicate exactly as the engine expects (value "*" → OP_EXISTS, else
// OP_EQ — mirroring store.Meta's "*" sentinel; the engine consumes these
// predicates, the client does NOT canonicalize).
func browseSelection(nodeType, status string, meta map[string]string) *knowledgev1.Selection {
	sel := &knowledgev1.Selection{NodeType: nodeType}
	if status != "" {
		sel.Statuses = []string{status}
	}
	sel.MetadataPredicates = lowerMetaPredicates(meta)
	return sel
}

// browsePluralSelection builds the Selection for a plural-types browse: NodeType
// stays EMPTY (Match("") all-types enumeration) and NodeTypes carries the set the
// engine post-filters against (postFilterBrowseNodeTypes). status + metadata
// predicates ride exactly as browseSelection sets them. DISTINCT from
// browseSelection's singular NodeType (a single index browse).
func browsePluralSelection(nodeTypes []string, status string, meta map[string]string) *knowledgev1.Selection {
	sel := &knowledgev1.Selection{NodeTypes: nodeTypes}
	if status != "" {
		sel.Statuses = []string{status}
	}
	sel.MetadataPredicates = lowerMetaPredicates(meta)
	return sel
}

// lowerMetaPredicates maps the meta equality map onto the proto MetadataPredicate
// vocabulary: "*" → OP_EXISTS, any other value → OP_EQ. Returns nil for an empty
// map. Mirrors store.Meta(k, v) / Meta(k, "*") lowering (engine.proto:137-143).
func lowerMetaPredicates(meta map[string]string) []*knowledgev1.MetadataPredicate {
	if len(meta) == 0 {
		return nil
	}
	preds := make([]*knowledgev1.MetadataPredicate, 0, len(meta))
	for k, v := range meta {
		p := &knowledgev1.MetadataPredicate{Key: k}
		if v == "*" {
			p.Op = knowledgev1.MetadataPredicate_OP_EXISTS
		} else {
			p.Op = knowledgev1.MetadataPredicate_OP_EQ
			p.Value = v
		}
		preds = append(preds, p)
	}
	return preds
}

// applyQueryVec decodes a base64 query_vector into one QueryVecs entry. The
// engine validates the 32-byte length; a malformed base64 is left unset so the
// plan still runs the text query (the engine falls back to embedding it).
func applyQueryVec(p *knowledgev1.QueryPlan, queryVector string) {
	if queryVector == "" {
		return
	}
	raw, err := base64Decode(queryVector)
	if err != nil {
		return
	}
	p.QueryVecs = [][]byte{raw}
}

// applyLimitOffset sets Limit/Offset only when supplied. Used by the SEARCH
// arms (text / graph_reach / recent): a search plan that carries limit==0 is
// self-defaulted to 10 by the server-side compositor (search/compositor.go), so
// the client injects no default here — doing so would be redundant and could
// fight the over-fetch plan.
func applyLimitOffset(p *knowledgev1.QueryPlan, limit, offset int) {
	if limit > 0 {
		p.Limit = int32(limit)
	}
	if offset > 0 {
		p.Offset = int32(offset)
	}
}

// applyBrowseLimitOffset is the BROWSE twin of applyLimitOffset: it applies the
// client-side friendly default (browseDefaultLimit=10) when the caller supplied
// no positive limit, then sets Offset. Used by the three query-tool browse arms
// (type-browse, plural-types browse, meta-only browse).
//
// FUL-302: a browse plan does NOT route through the compositor's self-default,
// and the server no longer injects a default (it honors limit==0 = no cap). So
// without this client default a no-limit LLM browse would send limit==0 →
// unbounded → the whole graph. This helper is the load-bearing guard that keeps
// the user-facing browse capped at 10 after the server-side default-injection
// was removed. An explicit positive limit overrides the default verbatim.
func applyBrowseLimitOffset(p *knowledgev1.QueryPlan, limit, offset int) {
	if limit > 0 {
		p.Limit = int32(limit)
	} else {
		p.Limit = browseDefaultLimit
	}
	if offset > 0 {
		p.Offset = int32(offset)
	}
}

// applyTombstones threads the include_tombstones opt-in onto the plan.
func applyTombstones(p *knowledgev1.QueryPlan, include bool) {
	if include {
		p.IncludeTombstones = true
	}
}

// applyContentB64 threads the content_b64 opt-in onto the plan (binary-safe
// NodeList Content carrier). Set only when requested so non-binary browses pay
// no base64 cost; the caller decodes via DecodeNodesContentB64.
func applyContentB64(p *knowledgev1.QueryPlan, contentB64 bool) {
	if contentB64 {
		p.ContentB64 = true
	}
}

// boolPtr dereferences a tri-state *bool, treating nil as false.
func boolPtr(b *bool) bool { return b != nil && *b }
