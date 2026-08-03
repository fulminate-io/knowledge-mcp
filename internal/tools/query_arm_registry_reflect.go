// SPDX-License-Identifier: Apache-2.0

package tools

// query_arm_registry_reflect.go holds the ten reflect arms of the query arm
// table — the modes interceptQueryReflect (thought.go:169-252) dispatches:
// personality, influence, tensions, blind_spots, summary, evolution, clusters,
// the thought-gated examine, simulate, and the recall route.
//
// Split out of query_arm_registry.go (which owns the param groups, the armIDs
// and the single init that assembles the registry) purely to keep both files
// inside the repo's file-length convention. The authoring rules live in that
// file's header.
//
// TWO CELLS ARE COMMON TO ALL TEN. `graph` is consumed because
// interceptQueryReflect's own guard reads it — a non-empty, non-knowledge graph
// declines the whole reflect surface — and `mode` is consumed because it is the
// switch discriminant. The reflect handlers take queryReflectArgs, a NARROWER
// struct than queryArgs: it has no Types, no Meta, no Repo/Account/Language, so
// every param outside its field set is dropped by the decode itself. That is
// precisely the class the Phase-1 reproduction pinned with
// query(mode:"charges", types:["thought"]) — the plural set is gone before the
// arm decides anything.

// queryReflectArmSpecs is the reflect group of the query arm registry.
var queryReflectArmSpecs = map[armID]armSpec{
	// ReflectPersonality takes the cluster filter and the granularity rollup.
	armReflectPersonality: {
		operation: "query",
		handler:   "interceptQueryReflect handleReflectPersonality",
		consumed:  qparams(qkeys("graph", "mode", "cluster", "granularity", "format")),
		rejected: qparams(
			qgIdentity, qgPaging, qgCode, qgSimulate, qgTopology, qgPivot, qgStats, qgCloud, qgRules,
			qkeys(
				"name", "repo", "account", "language", "branch",
				"text", "queries", "query_vector",
				"valence_min", "valence_max", "magnitude_min", "consistency_max",
				"session", "connected_to", "cluster_a", "cluster_b", "sort",
			),
		),
		deliberatelyIgnored: queryFieldsIgnored(),
	},

	// ReflectInfluence is the one reflect arm that pages: it takes the caller's
	// limit (defaulting to 10) and the `sort` display ordering.
	armReflectInfluence: {
		operation: "query",
		handler:   "interceptQueryReflect handleReflectInfluence",
		consumed:  qparams(qkeys("graph", "mode", "limit", "sort", "format")),
		rejected: qparams(
			qgIdentity, qgCode, qgSimulate, qgTopology, qgPivot, qgStats, qgCloud, qgRules,
			qkeys(
				"name", "repo", "account", "language", "branch",
				"text", "queries", "query_vector", "offset",
				"valence_min", "valence_max", "magnitude_min", "consistency_max",
				"session", "connected_to", "cluster", "cluster_a", "cluster_b", "granularity",
			),
		),
		deliberatelyIgnored: queryFieldsIgnored(),
	},

	// Tensions is served O(1) from the propagation loop's cache and issues no
	// graph reads at all, so the format switch is its only input.
	armReflectTensions: {
		operation: "query",
		handler:   "interceptQueryReflect handleReflectTensions",
		consumed:  qparams(qkeys("graph", "mode", "format")),
		rejected: qparams(
			qgIdentity, qgPaging, qgCode, qgThought, qgSimulate,
			qgTopology, qgPivot, qgStats, qgCloud, qgRules,
			qkeys("name", "repo", "account", "language", "branch", "text", "queries", "query_vector"),
		),
		deliberatelyIgnored: queryFieldsIgnored(),
	},

	// Blind spots is the same cached-report shape as tensions.
	armReflectBlindSpots: {
		operation: "query",
		handler:   "interceptQueryReflect handleReflectBlindSpots",
		consumed:  qparams(qkeys("graph", "mode", "format")),
		rejected: qparams(
			qgIdentity, qgPaging, qgCode, qgThought, qgSimulate,
			qgTopology, qgPivot, qgStats, qgCloud, qgRules,
			qkeys("name", "repo", "account", "language", "branch", "text", "queries", "query_vector"),
		),
		deliberatelyIgnored: queryFieldsIgnored(),
	},

	// Summary reads the granularity rollup but, unlike personality, takes no
	// cluster filter — its TopClusters slice is a fixed top-5.
	armReflectSummary: {
		operation: "query",
		handler:   "interceptQueryReflect handleReflectSummary",
		consumed:  qparams(qkeys("graph", "mode", "granularity", "format")),
		rejected: qparams(
			qgIdentity, qgPaging, qgCode, qgSimulate, qgTopology, qgPivot, qgStats, qgCloud, qgRules,
			qkeys(
				"name", "repo", "account", "language", "branch",
				"text", "queries", "query_vector",
				"valence_min", "valence_max", "magnitude_min", "consistency_max",
				"session", "connected_to", "cluster", "cluster_a", "cluster_b", "sort",
			),
		),
		deliberatelyIgnored: queryFieldsIgnored(),
	},

	// Evolution requires BOTH cluster_a and cluster_b; the 30-snapshot window is
	// a constant, so no limit rides here.
	armReflectEvolution: {
		operation: "query",
		handler:   "interceptQueryReflect handleReflectEvolution",
		consumed:  qparams(qkeys("graph", "mode", "cluster_a", "cluster_b", "format")),
		rejected: qparams(
			qgIdentity, qgPaging, qgCode, qgSimulate, qgTopology, qgPivot, qgStats, qgCloud, qgRules,
			qkeys(
				"name", "repo", "account", "language", "branch",
				"text", "queries", "query_vector",
				"valence_min", "valence_max", "magnitude_min", "consistency_max",
				"session", "connected_to", "cluster", "sort", "granularity",
			),
		),
		deliberatelyIgnored: queryFieldsIgnored(),
	},

	// Clusters surfaces the loop-persisted topology; format is its only input.
	armReflectClusters: {
		operation: "query",
		handler:   "interceptQueryReflect handleReflectClusters",
		consumed:  qparams(qkeys("graph", "mode", "format")),
		rejected: qparams(
			qgIdentity, qgPaging, qgCode, qgThought, qgSimulate,
			qgTopology, qgPivot, qgStats, qgCloud, qgRules,
			qkeys("name", "repo", "account", "language", "branch", "text", "queries", "query_vector"),
		),
		deliberatelyIgnored: queryFieldsIgnored(),
	},

	// mode:examine on a NodeThought id. The arm re-marshals a {thought, format}
	// payload for handleExamineClient, so those two plus the guards are its
	// whole read set; a non-thought id declines to the general examine arm.
	armReflectThoughtExamine: {
		operation: "query",
		handler:   "interceptQueryReflect handleExamineClient",
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

	// mode:simulate decodes its own simulateClientArgs off the raw payload —
	// action, target, polarity, weight, format.
	armReflectSimulate: {
		operation: "query",
		handler:   "interceptQueryReflect handleSimulateClient",
		consumed:  qparams(qgSimulate, qkeys("graph", "mode", "format")),
		rejected: qparams(
			qgIdentity, qgPaging, qgCode, qgThought,
			qgTopology, qgPivot, qgStats, qgCloud, qgRules,
			qkeys("name", "repo", "account", "language", "branch", "text", "queries", "query_vector"),
		),
		deliberatelyIgnored: queryFieldsIgnored(),
	},

	// The recall route: mode:timeline / mode:charges, or any thought-property
	// filter. recallParamsFromQuery forwards text→query, limit, mode, status,
	// the four scalar filters, session, connected_to and format, plus all_types
	// derived from the SINGULAR type. `types` is REJECTED and that rejection is
	// the Phase-1 reproduction made loud: queryReflectArgs has no Types field,
	// so a plural set changed nothing about the read.
	armReflectRecall: {
		operation: "query",
		handler:   "interceptQueryReflect handleRecallClient",
		consumed: qparams(qkeys(
			"graph", "mode", "text", "limit", "status", "type", "format",
			"session", "connected_to",
			"valence_min", "valence_max", "magnitude_min", "consistency_max",
		)),
		rejected: qparams(
			qgCode, qgSimulate, qgTopology, qgPivot, qgStats, qgCloud, qgRules,
			qkeys(
				"name", "repo", "account", "language", "branch",
				"id", "ids", "types", "meta", "since",
				"include_tombstones", "include_edges", "include_cross_links",
				"queries", "query_vector", "offset",
				"cluster", "cluster_a", "cluster_b", "sort", "granularity",
			),
		),
		deliberatelyIgnored: queryFieldsIgnored(),
	},
}
