// SPDX-License-Identifier: Apache-2.0

package tools

// query_arm_registry.go declares the per-arm param classification for the
// `query` tool: which params each dispatch arm CONSUMES, which it REJECTS, and
// which it DELIBERATELY IGNORES. It is the query-side twin of
// mutate_arm_registry.go, and it reuses that file's armSpec/armID/paramClass
// types and its accountParams gate rather than declaring a second mechanism.
//
// WHAT AN ARM IS HERE, and why it differs from mutate's. mutate's arms map onto
// ten operations in ONE decision tree. query's do not: the dispatch is a CHAIN
// of 22 self-gating intercepts invoked from
// cmd/knowledge/internal/bootstrap/dream.go (runInterceptChainInner,
// runQueryDomainIntercepts, runQueryRenderingIntercepts), several of which serve
// multiple shapes with different read sets. An arm is a point where an entry
// point commits to serving the call by returning handled==true with a HANDLER
// that reads params and produces a result. The completeness contract is
// therefore a BIJECTION between armIDs and gate call sites, not a switch over an
// operation enum.
//
// NOT AN ARM: (1) an early error return preceding every param read — a missing
// graph client, an absent stats seam; those are infrastructure refusals,
// identical across every shape of the entry point. (2) a POST-PARAM-READ
// VALIDATION REFUSAL issuing ZERO reads: the timeline arm's
// "timeline requires time_field when graph is not logs", the logs
// resolver-requires-logs-graph refusal (intercept_logs_query.go:44-48), the
// file_symbols required-path refusal, and InterceptTopology's empty-graph and
// empty-algorithm refusals (intercept_topology.go:65-70). Each belongs to its
// arm's precondition — it reads params only to refuse, so it routes nothing and
// has no read set.
//
// FILE SPLIT, and where the assembly lives. The table is split across four
// sibling files purely to stay inside the repo's 500-line file convention:
// query_arm_registry_graphs.go (the per-graph-family arms),
// query_arm_registry_modes.go (the composite-mode, code and rendering arms),
// query_arm_registry_reflect.go (the ten reflect arms) and
// query_arm_registry_stats.go (the selector-driven stats arms). The registry is
// ONE object — queryArmRegistry below is assembled from all four in a single init,
// so the classification the gate enforces and the classification the tests
// assert can never drift apart. mutate makes the same split and puts its init in
// mutate_param_accounting.go (the file that owns the types and the gate);
// query's init lives HERE instead, because query has no accounting file of its
// own — the gate it uses is mutate's registry-agnostic accountParams.
//
// THE TWO-LAYER CONTRACT, and the order the two layers run in. Param accounting
// on this surface is TWO questions, not one, and each layer answers only its
// own:
//
//	Layer 1 — VOCABULARY. Is this key part of the query tool's declared param set
//	at all? A key the schema does not declare is a typo or a param belonging to
//	another tool, and it is unknown for EVERY arm, so the check keys on the
//	SCHEMA and lives in ONE place rather than as a cell on each of the 51 arms.
//	Its helper has the shape func(supplied map[string]bool, schema map[string]T)
//	[]string — named by signature rather than by symbol because it is shared with
//	the mutate surface and either side may move it.
//
//	Layer 2 — APPLICABILITY. Does the DISPATCHED ARM apply a key the schema does
//	declare? That is what this registry answers, per arm, through accountParams.
//
// ORDER IS PART OF THE BEHAVIOR: applicability runs FIRST. A DECLARED but
// unrouted param keeps its specific message naming the arm and the handler that
// does not route it, which is far more actionable than the generic
// unknown-key form; reversing the order would mask every arm's reason text
// behind that generic message. The two layers cannot disagree about what the
// caller supplied, because both read the SAME verbatim key set through the same
// reader (suppliedMutateParams) rather than each re-deriving it.
//
// THE STRUCTURAL DIFFERENCE FROM MUTATE, which is what shapes this whole file.
// mutate accounts ONE decision tree keyed on ten operations, so its completeness
// contract is a partition over that operation enum: every operation has an arm,
// and the enum is the closed list. query has no such enum. Its dispatch is a
// CHAIN of self-gating intercepts, and the claim points inside those intercepts
// ARE the arms — a single entry point can hold five of them, and nothing in the
// type system enumerates them. So the completeness contract here is a BIJECTION
// between armIDs and gate call sites, asserted by scanning production source:
// every declared arm must have exactly one gate call, and every gate call must
// name a declared arm. A bijection alone is satisfiable by a degenerate registry
// that collapsed several shapes into one arm, which is why the per-entry-point
// arm FLOORS are locked alongside it.
//
// AUTHORING RULES for every cell, inherited verbatim from
// mutate_arm_registry.go's authoring block. A param is CONSUMED when the arm
// demonstrably uses it as (1) ROUTING onto the Execute Target, (2) a value
// reaching the read plan or the render, (3) the DISPATCH DISCRIMINANT whose
// value selects the arm, or (4) a DERIVATION driving a derived value. Anything
// else is REJECTED, except a render param on an arm that renders its own result.
//
// THE ONE CLARIFICATION query needs that mutate did not. An EMPTINESS GATE is
// not consumption. Several arms are selected by a param being absent
// (list-graphs shapes, browse shapes). The operative question for such a cell is
// whether a caller can SUPPLY the param, still land on THIS arm, and have it
// dropped: if yes the cell is REJECTED, because that drop is exactly what this
// registry exists to make loud; if supplying it routes the call to a DIFFERENT
// arm, the cell is unreachable and REJECTED costs nothing. Cells were derived by
// reading each handler, not its doc comment.
//
// `graph` IS CONSUMED ON NEARLY EVERY ARM, including the arms that pin their
// graph family and never read the selector at all (file_symbols, evidence,
// lineage, plan_tree). This mirrors mutate, where graph is consumed on nearly
// every arm because the family guard reads it to decide whether the arm is
// reachable. Rejecting it would false-reject the canonical documented spellings
// — query({graph:"code", mode:"file_symbols"}) and
// query({graph:"knowledge", mode:"lineage"}) both work today and must keep
// working. The residual (a MISMATCHED graph on a pinned arm is still silently
// ignored) is a routing defect, not a param-accounting one.
//
// THE ONE ARM THAT REJECTS `graph` IS armRules, and it is the mutate criterion
// arm's case rather than an exception to the rule above. The rule tolerates a
// consumed cell where a graph selector is at least MEANINGFUL for the family the
// arm pins; for rule nodes there is no other family and never will be, so there
// is no documented spelling to protect and no residual to tolerate — a rule
// browse naming ANOTHER graph family is a caller misunderstanding, and saying so
// is more useful than ignoring it. The wording is justifyRulesKnowledgeOnly
// below, the twin of mutate's justifyCriterionKnowledgeOnly.
//
// THAT REJECTION IS KEYED ON THE VALUE, NOT ON PRESENCE. A graph value naming
// the family the arm already pins is REDUNDANT rather than wrong, so armRules
// declares knowledgeGraphRedundantAliases and serves those calls — which is what
// keeps the strictness honest for hosts that attach their graph selector to
// every query they send. The cell stays classRejected: the class describes what
// the arm ROUTES (nothing), and a redundant value routes nothing either. See
// param_accounting_redundant_values.go for the convention.
//
// THE FIVE SELECTOR PARAMS, per the resolver. "Routing onto the Execute Target"
// counts as consumption ONLY where the SERVER'S RESOLVER FOR THAT GRAPH reads
// the field the client sets. Read off ResolveGraphDB
// (cmd/knowledge-server/internal/tools/tools_graph_routing.go): the knowledge
// arm returns store.StoreForContext and states outright that sel.Name is not
// consulted; linkage passes a hardcoded "default"; code keys on Repo; cloud and
// cicd key on Account; practice keys on Language. Only logs, web, pdf,
// transformers and registered-custom genuinely read sel.Name. So `name` is
// CONSUMED only on the arms that can target those families, and REJECTED
// elsewhere. `branch` is read by exactly one resolver (resolveCode, at the
// repo@branch overlay Scope), so it is REJECTED on every arm that does not
// target the code graph — and note that domainTarget
// (intercept_query_correlations_pivot.go:412-419) does not carry Branch at all,
// unlike the engine's buildTarget, so on the composite-mode arms branch is
// dropped by the CLIENT before the resolver is even reached.
//
// THE DELIBERATELY-IGNORED CLASS IS A CLOSED ALLOWLIST: its members are exactly
// `format` and `fields`. Both are render params, so an arm that renders its own
// result and never reads them ignores them WITH a justification rather than
// rejecting a schema-advertised render shape. Nothing else may be parked there —
// without that closure, "this entry point serves multiple shapes" becomes a
// non-empty justification for every routing param an arm drops, which is the
// exact route by which a fully green registry still silently drops
// arm-inapplicable params.
//
// NO RUNTIME COMPLEMENT. Every arm names every schema key explicitly across its
// three sets, via the thirteen frozen param GROUPS below plus loose keys. The
// groups are literals, not a complement computed against the live schema: a
// param newly added to query_schema.go appears in NO group, therefore in no
// arm's sets, and the partition assertion in query_param_accounting_test.go
// fails until someone classifies it.
//
// `since` HAS NO READER BUT ONE. It is declared by QueryToolDef and carried by
// queryArgs, and exactly one arm on the query surface reads it:
// armKnowledgeRecentBrowse, where it lowers to an updated_at GTE field predicate
// on the fetch selection. Every OTHER arm REJECTS it — the honest classification,
// and the one that turns what was a silent drop into an error.
//
// THE RULES ARM'S CELLS ARE POST-FIX. InterceptQueryRules now drains the rule
// corpus in keyset pages and slices the caller's page out of the scope-filtered
// set, so limit, offset, status, meta and include_tombstones are all routed and
// classified CONSUMED; graph is rejected per the paragraph above. limit and
// offset are consumed CLIENT-SIDE — they select a page of a set that only exists
// after the client-side scope filter — so neither reaches a captured read, and
// the parity fixture lists both as opaque for that reason rather than claiming an
// observability the arm cannot have.
//
// ARM COUNT: 51, against the plan's seven locked per-entry-point FLOORS. Two
// entry points EXCEED their floor, which the floors permit (they are minima):
// InterceptQueryCloudCICD takes 5 against a floor of 4 (the ranked-text search
// via composeResourceSearchClient is a distinct read set from the browse), and
// InterceptQueryPracticeLinkage takes 10 against a floor of 8 (the practice
// language:"all" scatter-gather is a distinct read set the floor folded into
// "practice search", and the text-less practice BROWSE is a third — one
// Selection + one Execute, sharing nothing with either ranked arm).

import (
	"maps"
	"sort"
)

// The thirteen frozen param groups. Together they name each of the 61 params
// QueryToolDef declares EXACTLY ONCE — 6+10+4+2+2+12+11+4+3+4+1+1+1 = 61 — which
// is what lets an arm compose its three sets from group names plus loose keys
// without any set ever being a complement. TestQueryParamGroups_PartitionSchema
// asserts the partition, so a schema addition that lands in no group is caught
// at the group level as well as at the per-arm level.
var (
	// qgSelector — the graph-family selector and its per-family instance keys.
	qgSelector = []string{"graph", "name", "repo", "account", "language", "branch"}
	// qgIdentity — by-id lookup, type/status/metadata browse filters, and the
	// per-read absorption flags.
	qgIdentity = []string{
		"id", "ids", "type", "types", "status", "meta", "since",
		"include_tombstones", "include_edges", "include_cross_links",
	}
	// qgText — the ranked-search inputs and the mode discriminant.
	qgText = []string{"text", "queries", "query_vector", "mode"}
	// qgPaging — the two paging knobs.
	qgPaging = []string{"limit", "offset"}
	// qgRender — the closed deliberately-ignored allowlist.
	qgRender = []string{"format", "fields"}
	// qgCode — the code-graph presentation and file-scoping params.
	qgCode = []string{
		"path_prefix", "path_prefixes", "file_path", "file_paths", "repos",
		"include_source", "include_comments", "include_tests", "test_kinds",
		"group_by_file", "caller_depth", "callee_depth",
	}
	// qgThought — the thought-graph filters and the reflect display knobs.
	qgThought = []string{
		"valence_min", "valence_max", "magnitude_min", "consistency_max",
		"session", "connected_to", "cluster", "cluster_a", "cluster_b",
		"sort", "granularity",
	}
	// qgSimulate — the four mode=simulate inputs.
	qgSimulate = []string{"action", "target", "polarity", "weight"}
	// qgTopology — the analyzer selector and its knobs.
	qgTopology = []string{"algorithm", "top_k", "extra"}
	// qgPivot — the pivot/timeline/correlations axis params.
	qgPivot = []string{"rows", "cols", "edge_type", "time_field"}
	// qgStats — the stats sample enrichment opt-in.
	qgStats = []string{"samples"}
	// qgCloud — the cloud/cicd resource-type browse prefix.
	qgCloud = []string{"resource_type"}
	// qgRules — the rule-browse scope substring filter.
	qgRules = []string{"scope"}
)

// justifyRulesKnowledgeOnly is the rejection explanation for `graph` on the rule
// browse, and the ONE place that wording is written. The generic message ends
// "drop it or issue a separate call that does" — but for this param there IS no
// such call: rules are knowledge-graph nodes, so the arm states the contract
// instead of inviting the caller to look for the other graph. Declared here and
// referenced from the arm (intercept_query_rules.go) so the gate's message and
// the arm's own contract cannot drift into two different claims.
const justifyRulesKnowledgeOnly = "rule nodes are knowledge-graph nodes — this path routes no graph " +
	"selector at all, and no other graph family carries rules; drop the param"

// justifyTopologyBranchUnrouted is the rejection explanation for `branch` on
// the topology arm. The generic message ("this path does not route it") is true
// but leaves a reader unable to tell an unroutable param from an unrouted one,
// and branch is the second: resolveCode DOES resolve repo@branch and its Scope
// returns a composite of base plus overlay
// (cmd/knowledge-server/internal/tools/tools_graph_routing.go:236-262), so a
// branch-scoped topology read would be well-defined if anything routed it.
// Nothing does — the dispatch builds foundation.Request from
// Caller/Graph/Name/RepoRoot/PathPrefix/TopK/Language/Extra and that struct has
// no Branch field — and threading a third scope component through foundation's
// wire helpers is a 61-call-site change this changeset does not make. So the
// caller is told rather than ignored. Do not overstate this as "branch is
// meaningless for topology": that would stop a future reader reconsidering it.
const justifyTopologyBranchUnrouted = "foundation.Request carries no Branch field, so no analyzer read is " +
	"branch-scoped and the value would be silently dropped; drop the param, or scope with path_prefix"

// knowledgeGraphRedundantAliases are the `graph` values a knowledge-only arm
// accepts as VALID-BUT-REDUNDANT rather than refusing: a caller naming the one
// family the arm already pins has restated the arm's contract, not asked for a
// routing it cannot perform. Every other value keeps the rejection above, which
// is what keeps the message honest — a rule browse carrying graph:"code" is a
// real caller error and still reads as one.
//
// The empty string is a member for the CONTRACT rather than for the mechanism:
// accounting already counts a key as supplied only when its value is non-empty
// (isEmptyJSONValue), so graph:"" never reaches the rejection loop. Listing it
// states the accepted set completely instead of leaving a reader to reconstruct
// it from two files.
//
// "default" is deliberately ABSENT, though the selector-layer precedent this
// follows (knowledgeRootNameAliases, cmd/knowledge-server/internal/tools/
// tools_graph_routing_selector.go) carries it. That set answers a different
// question — which NAME labels the root knowledge graph — and "default" is a
// graph-name label no caller sends as a graph FAMILY. Do not add it to make the
// two sets look alike; they are complementary layers, not copies.
var knowledgeGraphRedundantAliases = []string{"", "knowledge"}

// qkeys is the identity spelling for a loose key list, so an arm's sets read as
// a uniform list of group names: qparams(qgThought, qkeys("id", "ids")).
func qkeys(keys ...string) []string { return keys }

// qparams unions the named groups and loose keys into one membership set. It
// COPIES: the group vars are shared package-level slices and must never be
// mutated by a caller.
func qparams(groups ...[]string) map[string]bool {
	out := make(map[string]bool)
	for _, g := range groups {
		for _, k := range g {
			out[k] = true
		}
	}
	return out
}

// justifyQueryClientRendered is the shared justification for `format` on an arm
// that returns its own rendered result and never consults the format switch.
// Deliberately ignored rather than rejected: the schema advertises format for
// these shapes, so rejecting it would itself be a defect.
const justifyQueryClientRendered = "this arm renders its own result and never reads the format switch"

// justifyQueryNoProjection is the shared justification for `fields` on an arm
// whose render path takes no projection — either it never emits the json
// envelope, or it calls the json renderer with a nil projection. Same reasoning
// as format: a render param is ignored with a reason, never rejected.
const justifyQueryNoProjection = "the fields projection applies to the json render path this arm does not take"

// queryRenderIgnored is the two-entry deliberately-ignored map shared by every
// arm that reads NEITHER render param.
func queryRenderIgnored() map[string]string {
	return map[string]string{
		"format": justifyQueryClientRendered,
		"fields": justifyQueryNoProjection,
	}
}

// queryFieldsIgnored is the one-entry deliberately-ignored map for an arm that
// DOES read format (so format is consumed) but takes no projection.
func queryFieldsIgnored() map[string]string {
	return map[string]string{"fields": justifyQueryNoProjection}
}

// The 51 query dispatch arms. Each armID names one claim point in the intercept
// chain; together they cover every path a host-originated `query` call can take
// through the client intercept layer. Phase 4 wires exactly one gate call per
// armID, and the bijection test holds the two sets equal.
const (
	// InterceptQueryCloudCICD — 5 arms (locked floor 4).
	armCloudCICDListGraphs armID = "armCloudCICDListGraphs"
	armCloudCICDGetNode    armID = "armCloudCICDGetNode"
	armCloudCICDStats      armID = "armCloudCICDStats"
	armCloudCICDSearch     armID = "armCloudCICDSearch"
	armCloudCICDBrowse     armID = "armCloudCICDBrowse"

	// InterceptQueryPracticeLinkage — 11 arms (locked floor 8).
	armPracticeListGraphs   armID = "armPracticeListGraphs"
	armPracticeStats        armID = "armPracticeStats"
	armPracticeBrowse       armID = "armPracticeBrowse"
	armPracticeSearchFanOut armID = "armPracticeSearchFanOut"
	armPracticeSearch       armID = "armPracticeSearch"
	armLinkageListGraphs    armID = "armLinkageListGraphs"
	armLinkageStats         armID = "armLinkageStats"
	armLinkageGetNode       armID = "armLinkageGetNode"
	armLinkageSearchRetired armID = "armLinkageSearchRetired"
	armWebPDFSearch         armID = "armWebPDFSearch"
	armWebPDFStats          armID = "armWebPDFStats"

	// InterceptQueryKnowledgeSearch — 2 arms (locked floor 2).
	armKnowledgeRecentBrowse armID = "armKnowledgeRecentBrowse"
	armKnowledgeSearch       armID = "armKnowledgeSearch"

	// Single-arm per-graph entry points.
	armKnowledgeStats        armID = "armKnowledgeStats"
	armRegisteredGraphSearch armID = "armRegisteredGraphSearch"
	armLogsQuery             armID = "armLogsQuery"
	armRules                 armID = "armRules"
	// armUnrankedBuiltinSearchRefused covers BOTH transformers and checks. One
	// arm, not two, because an armID names a CLAIM POINT in the chain rather than
	// a graph: InterceptQueryUnrankedBuiltin is a single gate whose two branches
	// differ only in which fixed message they return, and splitting it would
	// declare two arms against one accountQueryParams call — the exact
	// registry/wiring mismatch the bijection test exists to catch.
	armUnrankedBuiltinSearchRefused armID = "armUnrankedBuiltinSearchRefused"
	// armBuiltinGraphStats covers BOTH checks and transformers, for the same
	// reason armUnrankedBuiltinSearchRefused does: InterceptQueryBuiltinStats is a
	// single gate whose two branches share one read set (the selector-driven stats
	// body) and differ only in their header and their `name` policy. Two armIDs
	// against one accountQueryParams call is the mismatch the bijection catches.
	armBuiltinGraphStats armID = "armBuiltinGraphStats"

	// InterceptQueryCorrelationsPivot / InterceptQueryExplainTimeline — 2 each.
	armCorrelations armID = "armCorrelations"
	armPivot        armID = "armPivot"
	armExplain      armID = "armExplain"
	armTimeline     armID = "armTimeline"

	// InterceptQueryModulesCodeStats — 2 arms (locked floor 2).
	armCodeModules armID = "armCodeModules"
	armCodeStats   armID = "armCodeStats"

	// Single-arm code, composite and rendering entry points.
	armAnalyzeNode     armID = "armAnalyzeNode"
	armCodeSearch      armID = "armCodeSearch"
	armFileSymbols     armID = "armFileSymbols"
	armMetadataStats   armID = "armMetadataStats"
	armTopology        armID = "armTopology"
	armEngineDispatch  armID = "armEngineDispatch"
	armPlanTree        armID = "armPlanTree"
	armEvidence        armID = "armEvidence"
	armLineage         armID = "armLineage"
	armExamineProjects armID = "armExamineProjects"
	armExamine         armID = "armExamine"

	// interceptQueryReflect — 10 arms (locked floor 10).
	armReflectPersonality    armID = "armReflectPersonality"
	armReflectInfluence      armID = "armReflectInfluence"
	armReflectTensions       armID = "armReflectTensions"
	armReflectBlindSpots     armID = "armReflectBlindSpots"
	armReflectSummary        armID = "armReflectSummary"
	armReflectEvolution      armID = "armReflectEvolution"
	armReflectClusters       armID = "armReflectClusters"
	armReflectThoughtExamine armID = "armReflectThoughtExamine"
	armReflectSimulate       armID = "armReflectSimulate"
	armReflectRecall         armID = "armReflectRecall"
)

// queryArmCount is the plan-locked number of arms. A literal, not a len() of the
// registry, so a table edit that silently drops or duplicates an arm fails
// instead of moving the target with it.
const queryArmCount = 51

// queryArmRegistry is the complete per-arm param classification for the query
// surface, assembled from the four sibling table files. The split is a
// file-length concern only: the registry is ONE object, so the classification
// the gate enforces and the classification the tests assert cannot drift apart.
var queryArmRegistry = map[armID]armSpec{}

// init assembles the registry from its four groups and precomputes each arm's
// sorted rejected-key slice. Package-level vars are initialized before init
// runs, so the groups are populated by the time this executes. The gate runs on
// every host-originated query call, so the ordering that makes its error
// deterministic is built once here rather than per call — the same arrangement
// mutate uses at mutate_param_accounting.go's init.
func init() {
	maps.Copy(queryArmRegistry, queryGraphArmSpecs)
	maps.Copy(queryArmRegistry, queryModeArmSpecs)
	maps.Copy(queryArmRegistry, queryReflectArmSpecs)
	maps.Copy(queryArmRegistry, queryStatsArmSpecs)
	for arm, spec := range queryArmRegistry {
		sorted := make([]string, 0, len(spec.rejected))
		for key := range spec.rejected {
			sorted = append(sorted, key)
		}
		sort.Strings(sorted)
		spec.rejectedSorted = sorted
		queryArmRegistry[arm] = spec
	}
}

// queryParamClass is registryParamClass bound to the query registry — the twin
// of paramClassFor, which binds it to mutate's.
func queryParamClass(arm armID, param string) (paramClass, bool) {
	return registryParamClass(queryArmRegistry, arm, param)
}
