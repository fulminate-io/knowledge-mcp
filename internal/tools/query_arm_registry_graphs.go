// SPDX-License-Identifier: Apache-2.0

package tools

// query_arm_registry_graphs.go holds the per-graph-family half of the query arm
// table: the cloud/cicd arms, the practice/linkage arms, the two knowledge
// text-search arms, and the four single-arm per-graph entry points (knowledge
// stats, registered-custom search, logs, rules).
//
// THE RAW-GRAPH AND BUILT-IN STATS ARMS MOVED to the fourth sibling,
// query_arm_registry_stats.go, when the checks/transformers stats arm pushed this
// file past the cap. Same table, same rules, one more file — the split is a
// file-length concern only, exactly as the three-way split already was.
//
// Split out of query_arm_registry.go (which owns the param groups, the armIDs
// and the single init that assembles the registry) purely to keep both files
// inside the repo's file-length convention; the two are one logical unit in one
// package. The authoring rules, the emptiness-gate clarification, the
// graph-is-always-consumed exception and the resolver table for the five
// selector params all live in that file's header — read them there rather than
// re-deriving them per cell.

// queryGraphArmSpecs is the per-graph-family group of the query arm registry.
var queryGraphArmSpecs = map[armID]armSpec{
	// list-graphs fires only when account, id and text are all absent
	// (intercept_query_cloud_cicd.go:99), so nothing else can ride this shape.
	// `queries` is the live catch: the gate tests only a.Text, so a caller who
	// sends queries[] with no account lands here and the search terms vanish.
	armCloudCICDListGraphs: {
		operation: "query",
		handler:   "InterceptQueryCloudCICD listResourceGraphs",
		consumed:  qparams(qkeys("graph", "mode", "account", "id", "text")),
		rejected: qparams(
			qgPaging, qgCode, qgThought, qgSimulate, qgTopology, qgPivot, qgStats, qgCloud, qgRules,
			qkeys(
				"name", "repo", "language", "branch",
				"ids", "type", "types", "status", "meta", "since",
				"include_tombstones", "include_edges", "include_cross_links",
				"queries", "query_vector",
			),
		),
		deliberatelyIgnored: queryRenderIgnored(),
	},

	// resourceGetNode reads only the id and the account key; the render is the
	// unconditional engine.RenderResourceNode, so no format switch is consulted.
	armCloudCICDGetNode: {
		operation: "query",
		handler:   "InterceptQueryCloudCICD resourceGetNode",
		consumed:  qparams(qkeys("graph", "mode", "account", "id")),
		rejected: qparams(
			qgPaging, qgCode, qgThought, qgSimulate, qgTopology, qgPivot, qgStats, qgCloud, qgRules,
			qkeys(
				"name", "repo", "language", "branch",
				"ids", "type", "types", "status", "meta", "since",
				"include_tombstones", "include_edges", "include_cross_links",
				"text", "queries", "query_vector",
			),
		),
		deliberatelyIgnored: queryRenderIgnored(),
	},

	// resourceStats reads Account, Format and Samples. limit/offset are the
	// pair the Phase-1 reproduction measured on the sibling knowledge stats arm:
	// a Stats RPC request has nowhere to put them.
	armCloudCICDStats: {
		operation: "query",
		handler:   "InterceptQueryCloudCICD resourceStats",
		consumed:  qparams(qkeys("graph", "mode", "account", "id", "samples", "format")),
		rejected: qparams(
			qgPaging, qgCode, qgThought, qgSimulate, qgTopology, qgPivot, qgCloud, qgRules,
			qkeys(
				"name", "repo", "language", "branch",
				"ids", "type", "types", "status", "meta", "since",
				"include_tombstones", "include_edges", "include_cross_links",
				"text", "queries", "query_vector",
			),
		),
		deliberatelyIgnored: queryFieldsIgnored(),
	},

	// The ranked-text arm: resourceQueryText reads text then queries[0], and
	// composeResourceSearchClient takes the format. `limit` is REJECTED because
	// the search is hardcoded to knowledgeSearchDefaultLimit — a caller's limit
	// is dropped today. This is the fifth arm, one above the locked floor of
	// four: its read set (Manager.Search → RRF → hydrate) is distinct from the
	// browse's Execute Match.
	armCloudCICDSearch: {
		operation: "query",
		handler:   "InterceptQueryCloudCICD composeResourceSearchClient",
		consumed:  qparams(qkeys("graph", "mode", "account", "id", "text", "queries", "format")),
		rejected: qparams(
			qgPaging, qgCode, qgThought, qgSimulate, qgTopology, qgPivot, qgStats, qgCloud, qgRules,
			qkeys(
				"name", "repo", "language", "branch",
				"ids", "type", "types", "status", "meta", "since",
				"include_tombstones", "include_edges", "include_cross_links",
				"query_vector",
			),
		),
		deliberatelyIgnored: queryFieldsIgnored(),
	},

	// resourceBrowse is the only cloud/cicd arm that pages: it reads Limit,
	// Offset and the resource_type metadata prefix. text/queries are read on the
	// way in (both must be empty for the browse to be selected).
	armCloudCICDBrowse: {
		operation: "query",
		handler:   "InterceptQueryCloudCICD resourceBrowse",
		consumed: qparams(qgPaging, qgCloud,
			qkeys("graph", "mode", "account", "id", "text", "queries")),
		rejected: qparams(
			qgCode, qgThought, qgSimulate, qgTopology, qgPivot, qgStats, qgRules,
			qkeys(
				"name", "repo", "language", "branch",
				"ids", "type", "types", "status", "meta", "since",
				"include_tombstones", "include_edges", "include_cross_links",
				"query_vector",
			),
		),
		deliberatelyIgnored: queryRenderIgnored(),
	},

	// routePracticeClient checks a.Language FIRST, so an empty language reaches
	// list-graphs before mode is ever read. `mode` is therefore REJECTED here:
	// query(graph:"practice", mode:"stats") with no language lands on this arm
	// and the mode is dropped.
	armPracticeListGraphs: {
		operation: "query",
		handler:   "InterceptQueryPracticeLinkage listPracticeGraphs",
		consumed:  qparams(qkeys("graph", "language")),
		rejected: qparams(
			qgIdentity, qgText, qgPaging, qgCode, qgThought, qgSimulate,
			qgTopology, qgPivot, qgStats, qgCloud, qgRules,
			qkeys("name", "repo", "account", "branch"),
		),
		// The browse-shaped params get the SPECIFIC tail rather than the generic
		// one, on the justifyRulesKnowledgeOnly precedent: a caller sending them
		// wants a browse, and `language` IS the separate call the generic tail
		// vaguely points at. See practiceListGraphsUnrouted.
		//
		// id and ids are DELIBERATELY ABSENT from this map even though the cell
		// rejects them: the entry point refuses a language-less by-id read before
		// this gate runs, with practiceByIDNeedsLanguage, which names the by-id call
		// rather than a browse. A reason here would be unreachable configuration.
		rejectionReasons: map[string]string{
			"type": practiceListGraphsUnrouted, "types": practiceListGraphsUnrouted,
			"status": practiceListGraphsUnrouted, "meta": practiceListGraphsUnrouted,
			"limit": practiceListGraphsUnrouted, "offset": practiceListGraphsUnrouted,
			"text": practiceListGraphsUnrouted, "queries": practiceListGraphsUnrouted,
		},
		deliberatelyIgnored: queryRenderIgnored(),
	},

	// The practice stats body reads Language, Format and Samples.
	armPracticeStats: {
		operation: "query",
		handler:   "InterceptQueryPracticeLinkage routePracticeClient stats",
		consumed:  qparams(qkeys("graph", "language", "mode", "format", "samples")),
		rejected: qparams(
			qgIdentity, qgPaging, qgCode, qgThought, qgSimulate,
			qgTopology, qgPivot, qgCloud, qgRules,
			qkeys("name", "repo", "account", "branch", "text", "queries", "query_vector"),
		),
		deliberatelyIgnored: queryFieldsIgnored(),
	},

	// The text-less practice BROWSE: one Selection carrying type/types/status/meta,
	// paged by limit/offset, rendered through engine.RenderBrowse (which reads BOTH
	// render params — format selects the json envelope and fields projects it). text
	// and queries are CONSUMED as the DISPATCH DISCRIMINANT: both must be empty for
	// this arm to be selected, which is authoring rule (3), exactly as
	// armCloudCICDBrowse treats them.
	armPracticeBrowse: {
		operation: "query",
		handler:   "InterceptQueryPracticeLinkage practiceBrowse",
		consumed: qparams(qgPaging, qgRender,
			qkeys("graph", "language", "mode", "type", "types", "status", "meta",
				"include_tombstones", "text", "queries")),
		rejected: qparams(
			qgCode, qgThought, qgSimulate, qgTopology, qgPivot, qgStats, qgCloud, qgRules,
			qkeys(
				"name", "repo", "account", "branch",
				"id", "ids", "since", "include_edges", "include_cross_links",
				"query_vector",
			),
		),
	},

	// language:"all" fans out across every loaded practice graph
	// (composePracticeSearchFanOut, intercept_query_practice_linkage.go:148-149).
	// This arm sits above the locked floor of eight rather than being folded
	// into it: the scatter-gather read set is genuinely distinct from the
	// single-language search. Stated as a property rather than an ordinal on
	// purpose — an ordinal goes stale every time a sibling arm is added, and
	// this one already had.
	armPracticeSearchFanOut: {
		operation: "query",
		handler:   "InterceptQueryPracticeLinkage composePracticeSearchFanOut",
		consumed: qparams(qkeys(
			"graph", "language", "mode", "text", "queries", "format", "fields", "limit")),
		rejected: qparams(
			qgIdentity, qgCode, qgThought, qgSimulate,
			qgTopology, qgPivot, qgStats, qgCloud, qgRules,
			qkeys("name", "repo", "account", "branch", "query_vector", "offset"),
		),
	},

	// The per-language practice search. Same read set as the fan-out minus the
	// enumeration. `limit` is CONSUMED on both: it resolves the per-graph Search k
	// (and, on the fan-out, the merge cap too). `offset` stays REJECTED and is
	// listed as a LOOSE key rather than riding qgPaging, because a segment-engine
	// ranked search has nowhere to put one — dropping the group here would leave
	// offset in no cell and fail the partition assertion.
	armPracticeSearch: {
		operation: "query",
		handler:   "InterceptQueryPracticeLinkage composePracticeSearchClient",
		consumed: qparams(qkeys(
			"graph", "language", "mode", "text", "queries", "format", "fields", "limit")),
		rejected: qparams(
			qgIdentity, qgCode, qgThought, qgSimulate,
			qgTopology, qgPivot, qgStats, qgCloud, qgRules,
			qkeys("name", "repo", "account", "branch", "query_vector", "offset"),
		),
	},

	// routeLinkageClient's list-graphs gate reads id, text, mode and queries in
	// that order (intercept_query_linkage.go:28).
	armLinkageListGraphs: {
		operation: "query",
		handler:   "InterceptQueryPracticeLinkage listLinkageGraphs",
		consumed:  qparams(qkeys("graph", "id", "text", "mode", "queries")),
		rejected: qparams(
			qgPaging, qgCode, qgThought, qgSimulate, qgTopology, qgPivot, qgStats, qgCloud, qgRules,
			qkeys(
				"name", "repo", "account", "language", "branch",
				"ids", "type", "types", "status", "meta", "since",
				"include_tombstones", "include_edges", "include_cross_links",
				"query_vector",
			),
		),
		deliberatelyIgnored: queryRenderIgnored(),
	},

	// linkageStatsClient takes only the format; the graph is the single unnamed
	// linkage instance, which is why `name` is rejected here even though the
	// arm is a named-graph shape elsewhere — resolveSimple passes a hardcoded
	// "default".
	armLinkageStats: {
		operation: "query",
		handler:   "InterceptQueryPracticeLinkage linkageStatsClient",
		consumed:  qparams(qkeys("graph", "mode", "format")),
		rejected: qparams(
			qgIdentity, qgPaging, qgCode, qgThought, qgSimulate,
			qgTopology, qgPivot, qgStats, qgCloud, qgRules,
			qkeys("name", "repo", "account", "language", "branch", "text", "queries", "query_vector"),
		),
		deliberatelyIgnored: queryFieldsIgnored(),
	},

	armLinkageGetNode: {
		operation: "query",
		handler:   "InterceptQueryPracticeLinkage routeLinkageClient getNode",
		consumed:  qparams(qkeys("graph", "mode", "id")),
		rejected: qparams(
			qgPaging, qgCode, qgThought, qgSimulate, qgTopology, qgPivot, qgStats, qgCloud, qgRules,
			qkeys(
				"name", "repo", "account", "language", "branch",
				"ids", "type", "types", "status", "meta", "since",
				"include_tombstones", "include_edges", "include_cross_links",
				"text", "queries", "query_vector",
			),
		),
		deliberatelyIgnored: queryRenderIgnored(),
	},

	// The retired linkage ranked search returns a fixed explanatory message and
	// reads nothing further. text/queries/id/mode stay CONSUMED anyway: they are
	// the shape this arm exists to answer, and rejecting them would replace the
	// designed retirement notice with a param error.
	armLinkageSearchRetired: {
		operation: "query",
		handler:   "InterceptQueryPracticeLinkage rankedSearchRetiredResult",
		consumed:  qparams(qkeys("graph", "mode", "id", "text", "queries")),
		rejected: qparams(
			qgPaging, qgCode, qgThought, qgSimulate, qgTopology, qgPivot, qgStats, qgCloud, qgRules,
			qkeys(
				"name", "repo", "account", "language", "branch",
				"ids", "type", "types", "status", "meta", "since",
				"include_tombstones", "include_edges", "include_cross_links",
				"query_vector",
			),
		),
		deliberatelyIgnored: queryRenderIgnored(),
	},

	// The transformers/checks ranked-search refusal. It answers from a fixed
	// message and reads nothing, so format/fields are deliberately ignored exactly
	// as on the retired linkage arm above.
	//
	// ITS CONSUMED SET IS THE REGISTERED-GRAPH TWIN'S, NOT THE LINKAGE ONE, because
	// InterceptQueryUnrankedBuiltin's gate is the twin's gate. Two consequences that
	// a copy of the linkage row would have got wrong:
	//
	//   - id and ids are REJECTED, not consumed. The linkage arm dispatches on id
	//     BEFORE its retired-search branch, so a by-id linkage read is genuinely
	//     served there. This arm serves no by-id read at all — a checks/transformers
	//     by-id read belongs to the engine dispatch arm, and claiming it here would
	//     break the browse the refusal itself hands the caller.
	//   - queries is REJECTED. The linkage list-graphs gate reads len(a.Queries);
	//     this arm reads only a.Text, so a queries-only payload declines rather than
	//     being answered, and declaring it consumed would assert a routing that does
	//     not exist.
	//
	// `name` IS consumed, which is the one place this row diverges from the twin's
	// reasoning rather than its shape. transformers is keyed by name
	// (graphsel.InstanceField → FieldName; the recipe store is the "recipes"
	// bucket), so a transformers search legitimately carries one and rejecting it
	// would answer a routine payload with a param error instead of the refusal.
	// checks is keyed by nothing (FieldNone) and simply never sends the key.
	armUnrankedBuiltinSearchRefused: {
		operation: "query",
		handler:   "InterceptQueryUnrankedBuiltin transformers/checks search-unavailable",
		consumed:  qparams(qkeys("graph", "name", "mode", "text")),
		rejected: qparams(
			qgPaging, qgCode, qgThought, qgSimulate, qgTopology, qgPivot, qgStats, qgCloud, qgRules,
			qkeys(
				"repo", "account", "language", "branch",
				"id", "ids", "type", "types", "status", "meta", "since",
				"include_tombstones", "include_edges", "include_cross_links",
				"queries", "query_vector",
			),
		),
		deliberatelyIgnored: queryRenderIgnored(),
	},

	// Bare mode:recent (empty text) is a temporal BROWSE: composeRecentBrowse
	// reads both type spellings onto the fetch selection, include_tombstones,
	// the post-sort limit, and both render params. `name` is the selector-level
	// drop the Phase-1 reproduction traced — domainTarget copies it onto a
	// knowledge Target whose resolver states outright that it never reads it.
	//
	// THIS IS `since`'s ONE READER on the whole query surface: it lowers to an
	// updated_at GTE field predicate on the fetch selection, so the window narrows
	// the drain server-side rather than filtering the render.
	armKnowledgeRecentBrowse: {
		operation: "query",
		handler:   "InterceptQueryKnowledgeSearch composeRecentBrowse",
		consumed: qparams(qgRender,
			qkeys("graph", "mode", "text", "type", "types", "include_tombstones", "limit", "since")),
		rejected: qparams(
			qgCode, qgThought, qgSimulate, qgTopology, qgPivot, qgStats, qgCloud, qgRules,
			qkeys(
				"name", "repo", "account", "language", "branch",
				"id", "ids", "status", "meta",
				"include_edges", "include_cross_links",
				"queries", "query_vector", "offset",
			),
		),
		deliberatelyIgnored: map[string]string{},
	},

	// The text-bearing knowledge search. knowledgeQueryToSearchArgs carries
	// text, limit, format, fields, the type/types precedence and meta.
	// `query_vector` is REJECTED and the rejection is real: this arm embeds the
	// text itself and never reads a caller-supplied vector. id/ids are rejected
	// for the same reason — on mode hybrid/text/recent the claim is
	// unconditional, so an id-selector rides along and is dropped.
	armKnowledgeSearch: {
		operation: "query",
		handler:   "InterceptQueryKnowledgeSearch composeKnowledgeSearch",
		consumed: qparams(qgRender,
			qkeys("graph", "mode", "text", "type", "types", "meta", "limit")),
		rejected: qparams(
			qgCode, qgThought, qgSimulate, qgTopology, qgPivot, qgStats, qgCloud, qgRules,
			qkeys(
				"name", "repo", "account", "language", "branch",
				"id", "ids", "status", "since", "include_tombstones",
				"include_edges", "include_cross_links",
				"queries", "query_vector", "offset",
			),
		),
		deliberatelyIgnored: map[string]string{},
	},

	// knowledgeStats reads only Format and Samples — the arm the Phase-1
	// reproduction measured for the dropped limit/offset pair.
	armKnowledgeStats: {
		operation: "query",
		handler:   "InterceptQueryStats knowledgeStats",
		consumed:  qparams(qkeys("graph", "mode", "format", "samples")),
		rejected: qparams(
			qgIdentity, qgPaging, qgCode, qgThought, qgSimulate,
			qgTopology, qgPivot, qgCloud, qgRules,
			qkeys("name", "repo", "account", "language", "branch", "text", "queries", "query_vector"),
		),
		deliberatelyIgnored: queryFieldsIgnored(),
	},

	// The registered-custom-graph search twin of the knowledge arm. `name` is
	// CONSUMED: resolveRegisteredCustom resolves the named graph via
	// Retrieve(sel.Name), and the client keys the segment engine on it too.
	armRegisteredGraphSearch: {
		operation: "query",
		handler:   "InterceptQueryRegisteredGraphSearch composeRegisteredGraphSearch",
		consumed: qparams(qgRender,
			qkeys("graph", "name", "mode", "text", "type", "types", "meta", "limit")),
		rejected: qparams(
			qgCode, qgThought, qgSimulate, qgTopology, qgPivot, qgStats, qgCloud, qgRules,
			qkeys(
				"repo", "account", "language", "branch",
				"id", "ids", "status", "since", "include_tombstones",
				"include_edges", "include_cross_links",
				"queries", "query_vector", "offset",
			),
		),
		deliberatelyIgnored: map[string]string{},
	},

	// handleLogsQuery dispatches on mode/id/text over the pre-fetched log state
	// and reads the pivot axes, the timeline/explain extra map, the stats
	// samples flag and the format. `name` is the query_id and is load-bearing —
	// resolveLogs refuses without it.
	armLogsQuery: {
		operation: "query",
		handler:   "InterceptLogsQuery handleLogsQuery",
		consumed: qparams(qgStats,
			qkeys("graph", "name", "mode", "id", "text", "rows", "cols", "extra", "format")),
		rejected: qparams(
			qgPaging, qgCode, qgThought, qgSimulate, qgCloud, qgRules,
			qkeys(
				"algorithm", "top_k", "edge_type", "time_field",
				"repo", "account", "language", "branch",
				"ids", "type", "types", "status", "meta", "since",
				"include_tombstones", "include_edges", "include_cross_links",
				"queries", "query_vector",
			),
		),
		deliberatelyIgnored: queryFieldsIgnored(),
	},

	// The rule browse drains the corpus in keyset pages, applies the scope filter,
	// then slices the caller's page out of the filtered set. status, meta and
	// include_tombstones ride the fetch Selection; limit and offset are applied to
	// the filtered set client-side. graph is REJECTED — rules exist in exactly one
	// graph family, so the selector has no meaning here (see
	// justifyRulesKnowledgeOnly) — EXCEPT for the values that name that same
	// family, which are served as redundant via knowledgeGraphRedundantAliases.
	armRules: {
		operation: "query",
		handler:   "InterceptQueryRules",
		consumed: qparams(qgRender, qgRules, qgPaging,
			qkeys("type", "status", "meta", "include_tombstones")),
		rejected: qparams(
			qgCode, qgThought, qgSimulate, qgTopology, qgPivot, qgStats, qgCloud,
			qkeys(
				"graph", "name", "repo", "account", "language", "branch",
				"id", "ids", "types", "since",
				"include_edges", "include_cross_links",
				"text", "queries", "query_vector", "mode",
			),
		),
		rejectionReasons:    map[string]string{"graph": justifyRulesKnowledgeOnly},
		redundantValues:     map[string][]string{"graph": knowledgeGraphRedundantAliases},
		deliberatelyIgnored: map[string]string{},
	},
}
