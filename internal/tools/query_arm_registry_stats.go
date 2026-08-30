// SPDX-License-Identifier: Apache-2.0

package tools

// query_arm_registry_stats.go holds the SELECTOR-DRIVEN stats arms and the
// raw-graph ranked read they sit beside: the web/pdf pair, and the
// checks/transformers stats arm.
//
// FOURTH SIBLING of the query arm table. query_arm_registry.go owns the param
// groups, the armIDs and the single init that assembles the registry; the other
// three siblings hold the per-graph, per-mode and reflect groups. The split is a
// file-length concern only — the registry is ONE object, so the classification
// the gate enforces and the classification the tests assert cannot drift apart.
// The authoring rules live in query_arm_registry.go's header; read them there.
//
// THESE ARMS SHARE A READ SET, which is why they are grouped: each is one Stats
// RPC rendered through renderGraphStatsBody, with the optional per-type sample
// enrichment bounded by node-type count. Their cells are consequently identical
// apart from the handler string.

// queryStatsArmSpecs is the selector-driven stats group of the query arm registry.
var queryStatsArmSpecs = map[armID]armSpec{
	// The web/pdf ranked text read: the raw graph is drained and ranked
	// client-side (composeRawGraphSearch). `name` IS consumed, and the reason is
	// stronger here than anywhere else: resolveBySourceName reads sel.Name for
	// both families, so the source slug is the ONLY selector that identifies
	// which document is being searched. `limit` is consumed as the rank cutoff k;
	// `format` and `fields` are consumed because the json arm renders through
	// RenderForCaller, which reads both. `offset` stays rejected — a ranked read
	// has nowhere to put one.
	armWebPDFSearch: {
		operation: "query",
		handler:   "InterceptQueryPracticeLinkage routeWebPDFClient composeRawGraphSearch",
		consumed: qparams(qkeys(
			"graph", "name", "mode", "id", "text", "queries", "format", "fields", "limit")),
		rejected: qparams(
			qgCode, qgThought, qgSimulate, qgTopology, qgPivot, qgStats, qgCloud, qgRules,
			qkeys(
				"repo", "account", "language", "branch",
				"ids", "type", "types", "status", "meta", "since",
				"include_tombstones", "include_edges", "include_cross_links",
				"query_vector", "offset",
			),
		),
	},

	// The web/pdf stats body reads Name, Format and Samples. `name` moves into
	// consumed relative to the practice sibling — a raw graph has no default
	// instance, so the slug is required rather than optional — and `language`
	// joins rejected for the same reason in reverse.
	armWebPDFStats: {
		operation: "query",
		handler:   "InterceptQueryPracticeLinkage routeWebPDFClient renderGraphStatsBody",
		consumed:  qparams(qkeys("graph", "name", "mode", "format", "samples")),
		rejected: qparams(
			qgIdentity, qgPaging, qgCode, qgThought, qgSimulate,
			qgTopology, qgPivot, qgCloud, qgRules,
			qkeys("repo", "account", "language", "branch", "text", "queries", "query_vector"),
		),
		deliberatelyIgnored: queryFieldsIgnored(),
	},

	// The checks / transformers stats arm — the same selector-driven body the
	// web/pdf arm above uses, so the read set is identical and so is this cell.
	// `name` is consumed for transformers (its real instance name) and must stay
	// absent for checks, whose selector policy admits none; the arm enforces that
	// split rather than the cell, because one cell cannot say "per graph".
	armBuiltinGraphStats: {
		operation: "query",
		handler:   "InterceptQueryBuiltinStats renderGraphStatsBody",
		consumed:  qparams(qkeys("graph", "name", "mode", "format", "samples")),
		rejected: qparams(
			qgIdentity, qgPaging, qgCode, qgThought, qgSimulate,
			qgTopology, qgPivot, qgCloud, qgRules,
			qkeys("repo", "account", "language", "branch", "text", "queries", "query_vector"),
		),
		deliberatelyIgnored: queryFieldsIgnored(),
	},
}
