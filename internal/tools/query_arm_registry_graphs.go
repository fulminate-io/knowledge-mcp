// SPDX-License-Identifier: Apache-2.0

package tools

// query_arm_registry_graphs.go holds the per-graph-family half of the query arm
// table: the practice/linkage arms, the two knowledge
// text-search arms, and the single-arm per-graph entry points (knowledge
// stats, registered-custom search, rules).
//
// THE RAW-GRAPH AND BUILT-IN STATS ARMS MOVED to the fourth sibling,
// query_arm_registry_stats.go, when the checks stats arm pushed this
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
	// THE ENUMERATION IS ASKED FOR BY NAME NOW. An empty selector used to reach
	// this arm before `mode` was ever read, because a practice read had to name a
	// graph; an empty selector BROWSES the one combined graph today, so the
	// CATALOG read is reached by mode:"modules" and `mode` is CONSUMED here rather
	// than rejected.
	//
	// `language` IS REJECTED ON EVERY PRACTICE ARM, here and in the four cells
	// below, and that is requirement 2's client clause. It was CONSUMED while the
	// field addressed one of eight instance-keyed practice graphs; those are
	// retired, so nothing reads it and the accounting gate is what refuses it —
	// removing the declaration IS the enforcement. A caller reaching a practice
	// arm with it set is answered earlier and more usefully by
	// refusePracticeLanguageOnRead, which names `source`; this cell is the
	// partition's statement of the same fact.
	armPracticeListGraphs: {
		operation: "query",
		handler:   "InterceptQueryPracticeLinkage listPracticeGraphs",
		consumed:  qparams(qkeys("graph", "mode")),
		rejected: qparams(
			qgPracticeHub,
			qgIdentity, qgPaging, qgCode, qgThought, qgSimulate,
			qgTopology, qgPivot, qgStats, qgCloud, qgRules,
			// qgText minus `mode`, which this arm now consumes as its own
			// discriminant.
			qkeys("text", "queries", "query_vector"),
			qkeys("name", "repo", "account", "branch", "language"),
		),
		// The browse-shaped params get the SPECIFIC tail rather than the generic
		// one, on the justifyRulesKnowledgeOnly precedent: a caller sending them
		// wants a browse, and `language` IS the separate call the generic tail
		// vaguely points at. See practiceListGraphsUnrouted.
		//
		// id and ids are DELIBERATELY ABSENT from this map even though the cell
		// rejects them: the entry point never reaches this gate for an id-bearing
		// payload at all. practiceShapeIsForeign (intercept_query_practice_linkage.go)
		// DECLINES both by-id shapes to the engine dispatch that serves them, and
		// with the combined graph a language-less by-id read is the normal case
		// rather than a refusal. A reason here would be unreachable configuration.
		rejectionReasons: map[string]string{
			"type": practiceListGraphsUnrouted, "types": practiceListGraphsUnrouted,
			"status": practiceListGraphsUnrouted, "meta": practiceListGraphsUnrouted,
			"limit": practiceListGraphsUnrouted, "offset": practiceListGraphsUnrouted,
			"text": practiceListGraphsUnrouted, "queries": practiceListGraphsUnrouted,
		},
		deliberatelyIgnored: queryRenderIgnored(),
	},

	// The practice stats body reads Format and Samples.
	armPracticeStats: {
		operation: "query",
		handler:   "InterceptQueryPracticeLinkage routePracticeClient stats",
		consumed:  qparams(qkeys("graph", "mode", "format", "samples")),
		rejected: qparams(
			// `source` is REFUSED here rather than narrowing, and that is an honest
			// classification instead of a gap. This arm's body is the Stats RPC,
			// which answers with whole-graph node/edge/vector counts computed
			// server-side; there is no per-hub arithmetic behind it to narrow, and
			// accepting the param to return unnarrowed totals would be a silent
			// drop on the one arm whose whole output is numbers. The browse answers
			// the hub-scoped question.
			qgPracticeHub,
			qgIdentity, qgPaging, qgCode, qgThought, qgSimulate,
			qgTopology, qgPivot, qgCloud, qgRules,
			qkeys("name", "repo", "account", "branch", "language", "text", "queries", "query_vector"),
		),
		rejectionReasons: map[string]string{
			"source": practiceStatsNoHubScope,
		},
		deliberatelyIgnored: queryFieldsIgnored(),
	},

	// The text-less practice BROWSE: one Selection carrying type/types/status/meta,
	// paged by limit/offset, rendered through engine.RenderBrowse (which reads BOTH
	// render params — format selects the json envelope and fields projects it). text
	// and queries are CONSUMED as the DISPATCH DISCRIMINANT: both must be empty for
	// this arm to be selected, which is authoring rule (3).
	armPracticeBrowse: {
		operation: "query",
		handler:   "InterceptQueryPracticeLinkage practiceBrowse",
		consumed: qparams(qgPracticeHub, qgPaging, qgRender,
			qkeys("graph", "mode", "type", "types", "status", "meta",
				"include_tombstones", "text", "queries")),
		rejected: qparams(
			qgCode, qgThought, qgSimulate, qgTopology, qgPivot, qgStats, qgCloud, qgRules,
			qkeys(
				"name", "repo", "account", "branch", "language",
				"id", "ids", "since", "include_edges", "include_cross_links",
				"query_vector",
			),
		),
	},

	// The compact style-rule INDEX. It is its own arm rather than a format on the
	// browse for two reasons the browse's own cell records: the browse REJECTS
	// `repo`, so the selection inputs requirement 3 needs have nowhere to ride
	// there, and this arm's paging is a DRAIN rather than a caller-supplied
	// window — an index that stopped at a page boundary would silently omit rules
	// a reader was relying on it to list, so `limit` and `offset` are rejected
	// rather than routed.
	//
	// `mode` is the discriminant and is CONSUMED. `repo`, `path_prefix` and
	// `path_prefixes` are the CLIENT-SIDE scope narrowing; `source` is the hub;
	// `meta` rides the same predicate lowering the browse uses, with the hub and
	// the practice kind folded in.
	armPracticeStyleIndex: {
		operation: "query",
		handler:   "InterceptQueryPracticeLinkage practiceStyleIndex",
		consumed: qparams(qgPracticeHub, qkeys(
			"graph", "mode", "meta", "repo",
			"path_prefix", "path_prefixes", "format")),
		rejected: qparams(
			qgPaging, qgThought, qgSimulate, qgTopology, qgPivot, qgStats, qgCloud, qgRules,
			qkeys(
				"name", "account", "branch", "language",
				"id", "ids", "type", "types", "status", "since",
				"include_tombstones", "include_edges", "include_cross_links",
				"text", "queries", "query_vector",
				"file_path", "file_paths", "repos",
				"include_source", "include_comments", "include_tests", "test_kinds",
				"group_by_file", "caller_depth", "callee_depth",
			),
		),
		rejectionReasons: map[string]string{
			"limit":  styleIndexPagingRejected,
			"offset": styleIndexPagingRejected,
		},
		deliberatelyIgnored: queryFieldsIgnored(),
	},

	// The practice ranked search. Same read set as the retired fan-out minus the
	// enumeration. `limit` is CONSUMED: it resolves the Search k. `offset` stays
	// REJECTED and is listed as a LOOSE key rather than riding qgPaging, because a
	// segment-engine ranked search has nowhere to put one — dropping the group
	// here would leave offset in no cell and fail the partition assertion.
	armPracticeSearch: {
		operation: "query",
		handler:   "InterceptQueryPracticeLinkage composePracticeSearchClient",
		consumed: qparams(qgPracticeHub, qkeys(
			"graph", "mode", "text", "queries", "format", "fields", "limit")),
		rejected: qparams(
			qgIdentity, qgCode, qgThought, qgSimulate,
			qgTopology, qgPivot, qgStats, qgCloud, qgRules,
			qkeys("name", "repo", "account", "branch", "language", "query_vector", "offset"),
		),
	},

	// routeLinkageClient's list-graphs gate reads id, text, mode and queries in
	// that order (intercept_query_linkage.go:28).
	armLinkageListGraphs: {
		operation: "query",
		handler:   "InterceptQueryPracticeLinkage listLinkageGraphs",
		consumed:  qparams(qkeys("graph", "id", "text", "mode", "queries")),
		rejected: qparams(qgPracticeHub,
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
		rejected: qparams(qgPracticeHub,
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
		rejected: qparams(qgPracticeHub,
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
		rejected: qparams(qgPracticeHub,
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
		rejected: qparams(qgPracticeHub,
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
		rejected: qparams(qgPracticeHub,
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
		rejected: qparams(qgPracticeHub,
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
		rejected: qparams(qgPracticeHub,
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
		rejected: qparams(qgPracticeHub,
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
