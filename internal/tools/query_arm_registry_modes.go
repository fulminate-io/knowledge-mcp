// SPDX-License-Identifier: Apache-2.0

package tools

// query_arm_registry_modes.go holds the composite-mode, code-graph and
// rendering half of the query arm table: correlations/pivot, explain/timeline,
// the code arms (modules, stats, analyze, search, file_symbols), metadata_stats,
// topology, the engine-dispatch tail, and the four knowledge rendering arms
// (plan_tree, evidence, lineage, examine + the project-domain examine).
//
// Split out of query_arm_registry.go (which owns the param groups, the armIDs
// and the single init that assembles the registry) purely to keep both files
// inside the repo's file-length convention. The authoring rules and the
// resolver table for the five selector params live in that file's header.
//
// ONE CROSS-CUTTING NOTE for the four non-logs composite arms below
// (correlations, pivot, explain, timeline): `name` is REJECTED on all four.
// They build their Execute Target through domainTarget
// (intercept_query_correlations_pivot.go:412-419), which copies Name for every
// graph family — but the families these arms can actually serve are the ones
// whose resolver discards it (logs is excluded upstream by InterceptLogsQuery,
// and the knowledge case is the passthrough the Phase-1 reproduction traced).
// domainTarget also omits Branch entirely, so `branch` is dropped by the client
// before the resolver is reached, which is why it is rejected here rather than
// treated as a resolver-discarded selector.

// queryModeArmSpecs is the composite-mode, code and rendering group of the query
// arm registry.
var queryModeArmSpecs = map[armID]armSpec{
	// composeCorrelations reads the edge_type filter, the tombstone opt-in and
	// the caller's limit (as the RENDER row cap, distinct from the scan cap),
	// plus the per-family instance keys domainTarget/domainGraphLabel consume.
	armCorrelations: {
		operation: "query",
		handler:   "InterceptQueryCorrelationsPivot composeCorrelations",
		consumed: qparams(qkeys(
			"graph", "mode", "repo", "account", "language",
			"edge_type", "include_tombstones", "limit",
		)),
		rejected: qparams(
			qgCode, qgThought, qgSimulate, qgTopology, qgStats, qgCloud, qgRules,
			qkeys(
				"name", "branch",
				"id", "ids", "type", "types", "status", "meta", "since",
				"include_edges", "include_cross_links",
				"text", "queries", "query_vector", "offset",
				"rows", "cols", "time_field",
			),
		),
		deliberatelyIgnored: queryRenderIgnored(),
	},

	// composePivot requires rows and cols, then streams the candidate node set
	// through pivotFetchNodesClient — which reads the singular `type` for the
	// keyset drain and `text` for the seed-search arm. `limit` is REJECTED: the
	// seed search is hardcoded to knowledgeSearchDefaultLimit and the matrix
	// render is capped at 20x20 by capPivotKeys.
	armPivot: {
		operation: "query",
		handler:   "InterceptQueryCorrelationsPivot composePivot",
		consumed: qparams(qkeys(
			"graph", "mode", "repo", "account", "language",
			"rows", "cols", "type", "text", "include_tombstones",
		)),
		rejected: qparams(
			qgPaging, qgCode, qgThought, qgSimulate, qgTopology, qgStats, qgCloud, qgRules,
			qkeys(
				"name", "branch",
				"id", "ids", "types", "status", "meta", "since",
				"include_edges", "include_cross_links",
				"queries", "query_vector",
				"edge_type", "time_field",
			),
		),
		deliberatelyIgnored: queryRenderIgnored(),
	},

	// composeExplain pivots on a single node: extra{a,b} or id, filtered by
	// edge_type. Its cost is O(degree of one node), so it takes no limit.
	armExplain: {
		operation: "query",
		handler:   "InterceptQueryExplainTimeline composeExplain",
		consumed: qparams(qkeys(
			"graph", "mode", "repo", "account", "language",
			"id", "extra", "edge_type",
		)),
		rejected: qparams(
			qgPaging, qgCode, qgThought, qgSimulate, qgStats, qgCloud, qgRules,
			qkeys(
				"name", "branch",
				"ids", "type", "types", "status", "meta", "since",
				"include_tombstones", "include_edges", "include_cross_links",
				"text", "queries", "query_vector",
				"algorithm", "top_k",
				"rows", "cols", "time_field",
			),
		),
		deliberatelyIgnored: queryRenderIgnored(),
	},

	// composeTimeline requires time_field, retains the earliest
	// timelineRowCap(limit) entries, and reads extra["bucket"] for the bucketed
	// render. It shares pivotFetchNodesClient with the pivot arm, hence the same
	// type/text/include_tombstones cells.
	armTimeline: {
		operation: "query",
		handler:   "InterceptQueryExplainTimeline composeTimeline",
		consumed: qparams(qkeys(
			"graph", "mode", "repo", "account", "language",
			"time_field", "limit", "extra", "type", "text", "include_tombstones",
		)),
		rejected: qparams(
			qgCode, qgThought, qgSimulate, qgStats, qgCloud, qgRules,
			qkeys(
				"name", "branch",
				"id", "ids", "types", "status", "meta", "since",
				"include_edges", "include_cross_links",
				"queries", "query_vector", "offset",
				"algorithm", "top_k",
				"rows", "cols", "edge_type",
			),
		),
		deliberatelyIgnored: queryRenderIgnored(),
	},

	// composeListModules resolves the repo set (repo / repos / repo="all") and
	// rolls packages+files up client-side, filtered by path_prefix. `branch` is
	// REJECTED: listModulesForRepo builds a bare {code, repo} selector with no
	// Branch, so an overlay is not read.
	armCodeModules: {
		operation: "query",
		handler:   "InterceptQueryModulesCodeStats composeListModules",
		consumed:  qparams(qkeys("graph", "mode", "repo", "repos", "path_prefix")),
		rejected: qparams(
			qgIdentity, qgPaging, qgThought, qgSimulate, qgTopology, qgPivot, qgStats, qgCloud, qgRules,
			qkeys(
				"name", "account", "language", "branch",
				"text", "queries", "query_vector",
				"path_prefixes", "file_path", "file_paths",
				"include_source", "include_comments", "include_tests", "test_kinds",
				"group_by_file", "caller_depth", "callee_depth",
			),
		),
		deliberatelyIgnored: queryRenderIgnored(),
	},

	// composeCodeStats targets {code, repo, branch} and reads Format and
	// Samples — the one code arm where branch reaches resolveCode's overlay
	// Scope.
	armCodeStats: {
		operation: "query",
		handler:   "InterceptQueryModulesCodeStats composeCodeStats",
		consumed:  qparams(qkeys("graph", "mode", "repo", "branch", "format", "samples")),
		rejected: qparams(
			qgIdentity, qgPaging, qgCode, qgThought, qgSimulate, qgTopology, qgPivot, qgCloud, qgRules,
			qkeys("name", "account", "language", "text", "queries", "query_vector"),
		),
		deliberatelyIgnored: queryFieldsIgnored(),
	},

	// composeAnalyzeNode does ByID plus two CALLS traversals at the clamped
	// caller/callee depths, rendered with or without source.
	armAnalyzeNode: {
		operation: "query",
		handler:   "InterceptQueryAnalyzeNode composeAnalyzeNode",
		consumed: qparams(qkeys(
			"graph", "mode", "id", "repo", "branch",
			"caller_depth", "callee_depth", "include_source",
		)),
		rejected: qparams(
			qgPaging, qgThought, qgSimulate, qgTopology, qgPivot, qgStats, qgCloud, qgRules,
			qkeys(
				"name", "account", "language",
				"ids", "type", "types", "status", "meta", "since",
				"include_tombstones", "include_edges", "include_cross_links",
				"text", "queries", "query_vector",
				"path_prefix", "path_prefixes", "file_path", "file_paths", "repos",
				"include_comments", "include_tests", "test_kinds", "group_by_file",
			),
		),
		deliberatelyIgnored: queryRenderIgnored(),
	},

	// The code search arm is the widest consumer on the query surface: it takes
	// both query spellings, the caller-supplied vector, the repo set, the limit,
	// and the whole code presentation group. `fields` is ignored because
	// flattenCodeResults renders json with a nil projection.
	armCodeSearch: {
		operation: "query",
		handler:   "InterceptQueryCodeSearch composeCodeSearch",
		consumed: qparams(qgText,
			qkeys(
				"graph", "id", "repo", "repos", "branch", "limit", "format",
				"path_prefix", "group_by_file", "include_source",
				"include_comments", "include_tests", "test_kinds",
			)),
		rejected: qparams(
			qgThought, qgSimulate, qgTopology, qgPivot, qgStats, qgCloud, qgRules,
			qkeys(
				"name", "account", "language",
				"ids", "type", "types", "status", "meta", "since",
				"include_tombstones", "include_edges", "include_cross_links",
				"offset",
				"path_prefixes", "file_path", "file_paths", "caller_depth", "callee_depth",
			),
		),
		deliberatelyIgnored: queryFieldsIgnored(),
	},

	// InterceptFileSymbols serves BOTH the standalone file_symbols tool and
	// query(mode:file_symbols), mapping path_prefix(es) onto file_path(s). It
	// pins the code graph rather than reading the selector — see the
	// graph-is-always-consumed note in the core file's header.
	//
	// `limit` caps the RENDERED symbol rows on both entry points, so it is
	// declared on FileSymbolsToolDef as well as QueryToolDef — the one sweep is
	// against the QUERY schema. offset stays REJECTED as a LOOSE key rather than
	// riding qgPaging, because dropping the group here would leave offset in no
	// cell and fail the partition assertion.
	armFileSymbols: {
		operation: "query",
		handler:   "InterceptFileSymbols composeFileSymbols",
		consumed: qparams(qkeys(
			"graph", "mode", "repo", "branch", "format", "limit",
			"file_path", "file_paths", "path_prefix", "path_prefixes",
			"include_source", "include_tombstones",
		)),
		rejected: qparams(
			qgThought, qgSimulate, qgTopology, qgPivot, qgStats, qgCloud, qgRules,
			qkeys(
				"name", "account", "language", "offset",
				"id", "ids", "type", "types", "status", "meta", "since",
				"include_edges", "include_cross_links",
				"text", "queries", "query_vector",
				"repos", "include_comments", "include_tests", "test_kinds",
				"group_by_file", "caller_depth", "callee_depth",
			),
		),
		deliberatelyIgnored: queryFieldsIgnored(),
	},

	// metadata_stats is a server-side aggregate over the MetadataStats RPC with
	// no node enumeration. `name` IS consumed here, unlike on the four composite
	// arms above: this intercept runs ahead of InterceptLogsQuery in the chain,
	// so it is the claimant for query(graph:"logs", mode:"metadata_stats",
	// name:<query_id>) — the shape the tool description documents — and
	// resolveLogs refuses without the name.
	armMetadataStats: {
		operation: "query",
		handler:   "InterceptQueryMetadataStats",
		consumed:  qparams(qkeys("graph", "mode", "name", "repo", "account", "language", "format")),
		rejected: qparams(
			qgIdentity, qgPaging, qgCode, qgThought, qgSimulate,
			qgTopology, qgPivot, qgStats, qgCloud, qgRules,
			qkeys("branch", "text", "queries", "query_vector"),
		),
		deliberatelyIgnored: queryFieldsIgnored(),
	},

	// InterceptTopology runs every analyzer client-side. topologyInstanceName
	// reads Name/Repo/Account in that precedence, and the analyzer Request takes
	// Language, PathPrefix, TopK and the Extra knob map. `branch` is REJECTED:
	// foundation.Request carries no Branch field, so no analyzer read is ever
	// branch-scoped. `path_prefix` is CONSUMED — it rides Request.PathPrefix,
	// and the dispatcher refuses it for any analyzer not on its declared
	// honoring list rather than routing a control that analyzer ignores. Its
	// empty-graph and empty-algorithm refusals read params only to refuse and
	// issue zero reads, so they are this arm's precondition rather than arms of
	// their own.
	armTopology: {
		operation: "query",
		handler:   "InterceptTopology",
		consumed: qparams(qgTopology,
			qkeys("graph", "mode", "name", "repo", "account", "language", "path_prefix")),
		rejected: qparams(
			qgIdentity, qgPaging, qgThought, qgSimulate, qgPivot, qgStats, qgCloud, qgRules,
			// qgCode cannot be named as a whole set here: it is a frozen
			// twelve-member group and path_prefix is now CONSUMED, so the
			// eleven remaining members are spelled out — the same arrangement
			// armCodeSearch already uses when it consumes path_prefix while
			// rejecting its siblings. Do NOT remove path_prefix from qgCode
			// itself: roughly twenty arms name that group and every one of them
			// would silently stop rejecting the param.
			qkeys(
				"path_prefixes", "file_path", "file_paths", "repos",
				"include_source", "include_comments", "include_tests", "test_kinds",
				"group_by_file", "caller_depth", "callee_depth",
			),
			qkeys("branch", "text", "queries", "query_vector"),
		),
		rejectionReasons: map[string]string{"branch": justifyTopologyBranchUnrouted},
		// FIELD ORDER IS LOCKED — operation, handler, consumed, rejected,
		// rejectionReasons, deliberatelyIgnored. Go permits any order in a keyed
		// literal, but the registry gates on this file region-extract the cell
		// with awk ranges keyed on those field names, so a rejectionReasons
		// written below deliberatelyIgnored falls outside the extracted region
		// and turns a correct edit red.
		deliberatelyIgnored: queryRenderIgnored(),
	},

	// The engine-dispatch tail: InterceptQuery embeds, then hands the payload to
	// engine.Dispatch → compileQuery, which lowers the reducible read shapes
	// (ids[] / id / text / plural-types / type / meta-only) and builds its Target
	// from all five selector fields — the one arm where `name` and `branch` ride
	// a graph-agnostic buildTarget, so both stay consumed. `queries` is REJECTED:
	// compileQuery reads only a.Text. `since` is rejected here as everywhere,
	// having no reader on the query surface at all.
	armEngineDispatch: {
		operation: "query",
		handler:   "InterceptQuery engine.Dispatch compileQuery",
		consumed: qparams(qgSelector, qgPaging, qgRender,
			qkeys(
				"id", "ids", "text", "type", "types", "status", "mode", "meta",
				"include_edges", "include_cross_links", "include_tombstones", "query_vector",
			)),
		rejected: qparams(
			qgCode, qgThought, qgSimulate, qgTopology, qgPivot, qgStats, qgCloud, qgRules,
			qkeys("since", "queries"),
		),
		deliberatelyIgnored: map[string]string{},
	},

	// plan_tree walks the contains hierarchy. It reads `limit` as the DEPTH
	// (mirroring the server shortcut) and the singular edge_type list as the
	// structure edge override.
	//
	// `fields` is CONSUMED, not ignored. The shared justification for ignoring it —
	// "the fields projection applies to the json render path this arm does not
	// take" — was FALSE here: format is consumed on this very cell and format:json
	// returns a json tree, so the arm does take that path. It now validates the
	// projection through engine.ValidateNodeProjection and projects every row,
	// which leaves this cell with an EMPTY deliberatelyIgnored map — a legal shape
	// armEngineDispatch already carries.
	armPlanTree: {
		operation: "query",
		handler:   "InterceptQueryPlanTree",
		consumed:  qparams(qkeys("graph", "mode", "id", "limit", "edge_type", "format", "fields")),
		rejected: qparams(
			qgCode, qgThought, qgSimulate, qgTopology, qgStats, qgCloud, qgRules,
			qkeys(
				"name", "repo", "account", "language", "branch",
				"ids", "type", "types", "status", "meta", "since",
				"include_tombstones", "include_edges", "include_cross_links",
				"text", "queries", "query_vector", "offset",
				"rows", "cols", "time_field",
			),
		),
		deliberatelyIgnored: map[string]string{},
	},

	// evidence follows informed-by → references from one decision id.
	armEvidence: {
		operation: "query",
		handler:   "InterceptQueryEvidence",
		consumed:  qparams(qkeys("graph", "mode", "id", "format")),
		rejected: qparams(
			qgPaging, qgCode, qgThought, qgSimulate, qgTopology, qgPivot, qgStats, qgCloud, qgRules,
			qkeys(
				"name", "repo", "account", "language", "branch",
				"ids", "type", "types", "status", "meta", "since",
				"include_tombstones", "include_edges", "include_cross_links",
				"text", "queries", "query_vector",
			),
		),
		deliberatelyIgnored: queryFieldsIgnored(),
	},

	// lineage walks provenance upward from one id at a fixed depth of 10 — the
	// depth is a constant, so `limit` is not a knob here the way it is on
	// plan_tree.
	armLineage: {
		operation: "query",
		handler:   "InterceptQueryLineage",
		consumed:  qparams(qkeys("graph", "mode", "id", "format")),
		rejected: qparams(
			qgPaging, qgCode, qgThought, qgSimulate, qgTopology, qgPivot, qgStats, qgCloud, qgRules,
			qkeys(
				"name", "repo", "account", "language", "branch",
				"ids", "type", "types", "status", "meta", "since",
				"include_tombstones", "include_edges", "include_cross_links",
				"text", "queries", "query_vector",
			),
		),
		deliberatelyIgnored: queryFieldsIgnored(),
	},

	// The project-domain examine variant, claimed first in the rendering
	// cluster for the eleven project-domain node types.
	armExamineProjects: {
		operation: "query",
		handler:   "InterceptQueryExamineProjects",
		consumed:  qparams(qkeys("graph", "mode", "id", "format")),
		rejected: qparams(
			qgPaging, qgCode, qgThought, qgSimulate, qgTopology, qgPivot, qgStats, qgCloud, qgRules,
			qkeys(
				"name", "repo", "account", "language", "branch",
				"ids", "type", "types", "status", "meta", "since",
				"include_tombstones", "include_edges", "include_cross_links",
				"text", "queries", "query_vector",
			),
		),
		deliberatelyIgnored: queryFieldsIgnored(),
	},

	// The general examine arm, claiming every OTHER knowledge node type after
	// the project-domain variant declines.
	armExamine: {
		operation: "query",
		handler:   "InterceptQueryExamine composeInspectData",
		consumed:  qparams(qkeys("graph", "mode", "id", "format")),
		rejected: qparams(
			qgPaging, qgCode, qgThought, qgSimulate, qgTopology, qgPivot, qgStats, qgCloud, qgRules,
			qkeys(
				"name", "repo", "account", "language", "branch",
				"ids", "type", "types", "status", "meta", "since",
				"include_tombstones", "include_edges", "include_cross_links",
				"text", "queries", "query_vector",
			),
		),
		deliberatelyIgnored: queryFieldsIgnored(),
	},
}
