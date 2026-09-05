// SPDX-License-Identifier: Apache-2.0

package tools

// query_arm_registry_stats.go holds the SELECTOR-DRIVEN stats arms and the
// raw-graph ranked read they sit beside: the web/pdf pair, and the
// checks stats arm.
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
	// The web/pdf ranked text read: served from the CLIENT SEGMENT ENGINE
	// (composeRawGraphSegmentSearch). `name` IS consumed, and the reason is
	// stronger here than anywhere else: resolveBySourceName reads sel.Name for
	// both families, so the source slug is the ONLY selector that identifies
	// which document is being searched. `limit` is consumed as the rank cutoff k;
	// `format` and `fields` are consumed because the json arm renders through
	// RenderForCaller, which reads both. `offset` stays rejected — a ranked read
	// has nowhere to put one.
	armWebPDFSearch: {
		operation: "query",
		handler:   "InterceptQueryPracticeLinkage routeWebPDFClient composeRawGraphSegmentSearch",
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

	// The raw modules listing enumerates the collected graphs of ONE family and
	// reads two stamps off each one's root, so `graph` and `mode` are the whole
	// input. `name` is REJECTED rather than consumed — this arm is the surface a
	// caller reaches BECAUSE it does not yet know a slug, and silently ignoring a
	// supplied one would answer a narrower question than the caller asked.
	// `samples` is rejected for the same reason it is on any non-stats arm: there
	// is no stats body here to enrich. Both render params are deliberately
	// ignored — the listing renders one markdown body and consults neither.
	armWebPDFModules: {
		operation: "query",
		handler:   "InterceptQueryPracticeLinkage routeWebPDFClient webPDFModules",
		consumed:  qparams(qkeys("graph", "mode")),
		rejected: qparams(
			qgIdentity, qgPaging, qgStats, qgCode, qgThought, qgSimulate,
			qgTopology, qgPivot, qgCloud, qgRules,
			qkeys("name", "repo", "account", "language", "branch", "text", "queries", "query_vector"),
		),
		deliberatelyIgnored: queryRenderIgnored(),
	},

	// The checks stats arm — the same selector-driven body the web/pdf arm above
	// uses, so the read set is identical and so is this cell.
	//
	// `name` IS REJECTED, NOT CONSUMED, and the move is the arm's membership
	// rather than a policy change. This arm served two graphs and only one of them
	// carried a real instance name; checks is a singleton whose selector policy
	// admits no instance field, so with the other graph gone there is no member
	// left that reads `name`. The cell can now say it outright — the note that
	// "one cell cannot say per graph" applied to a split that no longer exists.
	armBuiltinGraphStats: {
		operation: "query",
		handler:   "InterceptQueryBuiltinStats renderGraphStatsBody",
		consumed:  qparams(qkeys("graph", "mode", "format", "samples")),
		rejected: qparams(
			qgIdentity, qgPaging, qgCode, qgThought, qgSimulate,
			qgTopology, qgPivot, qgCloud, qgRules,
			qkeys("name", "repo", "account", "language", "branch", "text", "queries", "query_vector"),
		),
		deliberatelyIgnored: queryFieldsIgnored(),
	},
}
