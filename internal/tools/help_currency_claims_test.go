// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// help_currency_claims_test.go pins the OPERATIONAL claims a currency sweep
// found refuted — each one a statement the help made that the code contradicts,
// verified against current source and (where the surface allows it) against a
// live call. The census in the sibling file covers "is every operation named";
// this file covers "is what the help SAYS about them true".
//
// Every case follows the same shape as the earlier mutate-claims guard: a
// NEGATIVE naming the refuted wording that must stay gone, and a POSITIVE naming
// the replacement, so a later rewrite cannot satisfy the negative by deleting
// the block around it and leaving the reader with nothing.

// TestHelpQuery_ClustersModeTakesNoTypeFilter pins the refutation with the
// sharpest evidence in the sweep: help("query") shipped
// `query({ "mode": "clusters", "type": "all" })` as a worked example, and that
// call does not run. armReflectClusters consumes only graph/mode/format and
// rejects the qgIdentity group, of which `type` is a member
// (query_arm_registry_reflect.go, query_arm_registry.go:195-198), so the
// accounting gate refuses it BEFORE the handler — and handleReflectClusters
// (thought_format.go) is thought-only regardless, never reading `type` at all.
// The all-node view is thoughts(recall, mode:"clusters", all_types:true).
func TestHelpQuery_ClustersModeTakesNoTypeFilter(t *testing.T) {
	assert.NotContains(t, helpQuery, `"mode": "clusters", "type": "all"`,
		"helpQuery must not show a clusters call carrying a type filter — the param gate refuses it")
	assert.Contains(t, helpQuery, `"mode": "clusters", "all_types": true`,
		"helpQuery must point at the spelling that actually produces an all-node cluster view")
	assert.Contains(t, helpThoughts, "all_types",
		"helpThoughts must document all_types, since helpQuery now redirects readers to it")

	// The rejection is a property of the arm registry, not of the prose, so
	// assert it there too: if `type` ever becomes a consumed param on the
	// clusters arm, the help above is what should change, and this row is what
	// says so.
	spec, ok := queryArmRegistry[armReflectClusters]
	require.True(t, ok, "armReflectClusters is not registered — the claim above has no anchor")
	assert.Contains(t, spec.rejectedSorted, "type",
		"the clusters arm no longer rejects `type`; helpQuery's redirect needs revisiting")
}

// TestHelpQuery_DoesNotPromiseGraphAllFanOut pins the disposition of
// graph:"all". No GraphType "all" ever existed (kgtypes/graph_types.go) and no
// dispatch arm ever special-cased the string; the token was an unbacked enum
// value that two prose surfaces later invented a fan-out semantics for. It is
// now REFUSED like any other unknown graph type
// (validateRegisteredGraphSelector, tools/registered_graph_selector.go), so the
// help says refused rather than describing a zero.
//
// Two claims are pinned because they fail differently. The help must not promise
// the fan-out ("searches both knowledge and code graphs simultaneously" — the
// most expensive kind of wrong: it invites a reader to conclude "nothing exists"
// from a selector that never selected), and it must not describe the retired
// silent zero either, which would send a reader looking for empty results
// instead of an error.
func TestHelpQuery_DoesNotPromiseGraphAllFanOut(t *testing.T) {
	assert.NotContains(t, helpQuery, "searches both knowledge and code graphs",
		"helpQuery must not promise a graph:\"all\" fan-out that does not happen")
	assert.NotContains(t, helpQuery, "returns ZERO results rather than failing",
		"helpQuery must not describe the retired silent-zero behaviour")
	assert.Contains(t, helpQuery, `graph:"all" is not a graph type and is`,
		"helpQuery must tell the reader graph:\"all\" is refused")
	// The schema is the anchor: the help's claim is only true while the enum text
	// has actually dropped the token.
	assert.NotContains(t, QueryToolDef().InputSchema.Properties["graph"].Description, ", or all",
		"the query schema still advertises graph:\"all\"; helpQuery's claim contradicts it")
}

// TestHelpTraverse_StartIsNotUnconditionallyRequired pins the traverse
// required-ness fix. The tool schema declares NO required set, and an empty
// start is a graph-wide edge enumeration rather than an error — measured live:
// traverse(start:"", graph:"knowledge") returned the graph's node and edge
// totals. The help had listed "start (required)".
func TestHelpTraverse_StartIsNotUnconditionallyRequired(t *testing.T) {
	assert.NotContains(t, helpTraverse, "start (required)",
		"helpTraverse must not claim start is unconditionally required")
	assert.Contains(t, helpTraverse, "It is NOT unconditionally required",
		"helpTraverse must document the empty-start graph-wide enumeration")
	assert.Empty(t, TraverseToolDef().InputSchema.Required,
		"the traverse schema declares a required set; helpTraverse's wording assumes it does not")
}

// TestHelpCreators_StateTheRealRequiredSets pins the four create_* topics whose
// required-ness claims a caller would have hit as a hard error.
//
// create_project and create_ticket both marked schema-REQUIRED fields
// "(optional)"; their worked examples omitted fields the handler rejects on
// (validate.ClampSummary hard-errors an empty summary). create_plan's "Required
// fields" line omitted the architecture-pattern tristate, which
// projects.ValidatePatternFields enforces with "exactly one of pattern_ids,
// no_patterns_reason, or proposed_patterns must be set".
func TestHelpCreators_StateTheRealRequiredSets(t *testing.T) {
	// The schema is the anchor for the required set, so the assertions below
	// cannot drift from what the tool publishes without this loop failing first.
	for _, tc := range []struct {
		tool string
		def  func() []string
	}{
		{"create_project", func() []string { return CreateProjectToolDef().InputSchema.Required }},
		{"create_ticket", func() []string { return CreateTicketToolDef().InputSchema.Required }},
	} {
		req := tc.def()
		assert.Contains(t, req, "summary", "%s must still publish summary as required", tc.tool)
		assert.Contains(t, req, "description", "%s must still publish description as required", tc.tool)
	}

	assert.NotContains(t, helpCreateProject, "summary     — short search-optimized summary (optional)",
		"helpCreateProject must not mark the required summary optional")
	assert.NotContains(t, helpCreateTicket, "project_id  — parent project node ID (optional",
		"helpCreateTicket must not mark the required project_id optional")

	// The worked examples must carry every required field, since they are what a
	// reader copies. Parsing them keeps this honest about the JSON rather than
	// asserting on prose.
	for _, tc := range []struct {
		name     string
		help     string
		call     string
		required []string
	}{
		{"create_project", helpCreateProject, "create_project(", CreateProjectToolDef().InputSchema.Required},
		{"create_ticket", helpCreateTicket, "create_ticket(", CreateTicketToolDef().InputSchema.Required},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := firstWorkedCallPayload(t, tc.help, tc.call)
			for _, field := range tc.required {
				assert.Contains(t, payload, field,
					"the help(%q) worked example omits required field %q — a reader copying it gets an error",
					tc.name, field)
			}
		})
	}

	// The pattern tristate is handler-enforced rather than schema-declared, so it
	// gets its own row on all three surfaces that need it.
	assert.Contains(t, helpCreatePlan, "exactly one of pattern_ids",
		"helpCreatePlan must state the required architecture-pattern tristate")
	assert.Contains(t, helpCreateTicket, "EXACTLY ONE of pattern_ids",
		"helpCreateTicket must state the required architecture-pattern tristate")
	for _, tc := range []struct {
		name string
		help string
		call string
	}{
		{"create_ticket", helpCreateTicket, "create_ticket("},
		{"create_plan", helpCreatePlan, "create_plan("},
	} {
		payload := firstWorkedCallPayload(t, tc.help, tc.call)
		assert.True(t,
			strings.Contains(payload, "no_patterns_reason") ||
				strings.Contains(payload, "pattern_ids") ||
				strings.Contains(payload, "proposed_patterns"),
			"the help(%q) worked example satisfies none of the tristate — it cannot run", tc.name)
	}
}

// TestHelpCodeTools_DoNotPromiseARepoDefault pins the repo fix on the three
// surfaces that had promised one. repoProp (firstclass_schema.go:19) states repo
// is "REQUIRED for graph=code; it is never inferred from cwd", and
// resolveTopologyRepo (intercept_topology.go) returns "repo is required" for an
// empty value. The help had offered "default: active repo", "default: all repos"
// and "defaults to active repo" respectively.
func TestHelpCodeTools_DoNotPromiseARepoDefault(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"search", helpSearchCode},
		{"file_symbols", helpFileSymbols},
		{"topology", helpTopology},
	} {
		assert.NotContains(t, tc.body, "default: active repo",
			"help(%q) promises an active-repo default that does not exist", tc.name)
		assert.NotContains(t, tc.body, "defaults to active repo",
			"help(%q) promises an active-repo default that does not exist", tc.name)
		assert.Contains(t, tc.body, "REQUIRED",
			"help(%q) must say repo is required", tc.name)
	}
	assert.NotContains(t, helpFileSymbols, "default: all repos",
		"helpFileSymbols promises an all-repos default that does not exist")

	// The schema half, so the prose above stays anchored to what the tool
	// publishes rather than to a claim about it.
	assert.Contains(t, repoProp.Description, "never inferred from cwd",
		"repoProp no longer states the no-inference rule the help now repeats")

	// ast's repo is a DIFFERENT thing — a filesystem walk root, not a graph name —
	// and the help had called it "code graph name".
	assert.NotContains(t, helpAst, "repo             — code graph name",
		"helpAst must not describe repo as a graph name; ast walks a directory on disk")
	assert.Contains(t, helpAst, "ast is FILESYSTEM-based",
		"helpAst must describe repo as the directory the walk targets")
}

// TestHelpTopology_NamesTheRealRegistryAPI pins the analyzer-authoring
// instructions. The registry is package foundation
// (topology/foundation/registry.go: Register/Get/All, analyzer.go: Analyzer,
// Request; finding.go: Finding) — the help named topology.Register,
// topology.Analyzer, topology.Request and topology.Finding, none of which exist.
func TestHelpTopology_NamesTheRealRegistryAPI(t *testing.T) {
	for _, phantom := range []string{
		"topology.Register", "topology.Analyzer", "topology.Request", "topology.Finding",
	} {
		assert.NotContains(t, helpTopology, phantom,
			"helpTopology names %s, which does not exist — the registry is package foundation", phantom)
	}
	for _, real := range []string{
		"foundation.Register", "foundation.Analyzer", "foundation.Request", "foundation.Finding",
	} {
		assert.Contains(t, helpTopology, real, "helpTopology must name the real symbol %s", real)
	}

	// path_prefix is REFUSED for every analyzer outside the honoring set rather
	// than accepted-and-ignored, which is the opposite of what "code only —
	// restrict to nodes whose FilePath starts with prefix" implied.
	assert.Contains(t, helpTopology, "honored ONLY by the corpus-scan analyzer",
		"helpTopology must say path_prefix is refused for non-honoring analyzers")
	assert.NotEmpty(t, pathPrefixHonoringAnalyzers,
		"the honoring set is empty; helpTopology's claim would then be misleading in the other direction")
}

// TestHelpOverview_SummarySynthesisClaimMatchesTheValidators pins the
// summary-synthesis paragraph against the validators it describes. NO creator
// synthesizes a Summary any longer: record_decision was the last that did, from
// the choice, and it now runs validate.ClampSummary like rule
// (intercept_mutate_create.go), thoughts(think) (intercept_thoughts_think.go),
// thoughts(charge) (intercept_thoughts_charge.go) and criterion
// (upsertCriterionNode, intercept_add_criterion.go) — and ClampSummary
// hard-errors an empty summary.
//
// The negatives are cumulative rather than replaced: each names a claim the
// overview made at some point and must never make again, and the newest of them
// is the record_decision carve-out this paragraph carried until every creator
// took an author summary.
func TestHelpOverview_SummarySynthesisClaimMatchesTheValidators(t *testing.T) {
	assert.NotContains(t, helpOverview, "record_decision / criterion / rule keep their auto-synthesized Summary",
		"helpOverview must not list rule among the auto-synthesizing creators")
	assert.NotContains(t, helpOverview, "think keeps the SymbolName / first-line-of-content convention",
		"helpOverview must not claim think synthesizes its own summary")
	assert.NotContains(t, helpOverview, "criterion (derived from description",
		"helpOverview must not claim criterion synthesizes its own summary — it is author-supplied")
	assert.NotContains(t, helpOverview, "ONE creator still synthesizes its own Summary",
		"no creator synthesizes one; record_decision was the last and now requires an author summary")
	assert.Contains(t, helpOverview, "NO creator synthesizes a Summary any more",
		"helpOverview must state the rule positively, not merely drop the retired carve-out")
	assert.Contains(t, helpOverview, "criterion, rule, thoughts(think) and thoughts(charge) run the",
		"helpOverview must name the creators that require an author summary, charge among them")

	// The record_decision half, asserted against the SCHEMA rather than the
	// prose: the claim above is only true while the tool actually demands it.
	assert.Contains(t, RecordDecisionToolDef().InputSchema.Required, "summary",
		"record_decision no longer requires a summary; helpOverview's claim needs revisiting")

	// The schema half: thoughts(think) publishes summary as REQUIRED, so the
	// prose above cannot drift from the tool without this row failing.
	assert.Contains(t, ThoughtsToolDef().InputSchema.Properties["summary"].Description, "REQUIRED",
		"the thoughts schema no longer marks summary required; helpOverview's claim needs revisiting")
}

// TestHelpManage_CircuitBreakerDescriptionMatchesTheBreaker pins the auto-pause
// wording. The breaker is per-axis with TWO trip conditions —
// DefaultCircuitBreakerThreshold (20) consecutive errors, and
// DefaultDeterministicFastTripThreshold (2) consecutive same-class
// deterministic-terminal failures (pipeline/circuit_breaker.go, config.go). The
// help had described a single condition requiring zero successes "across both
// axes", which is neither the granularity nor the threshold.
func TestHelpManage_CircuitBreakerDescriptionMatchesTheBreaker(t *testing.T) {
	assert.NotContains(t, helpManage, "zero successes across both axes",
		"helpManage must not describe the per-axis breaker as a both-axes condition")
	for _, marker := range []string{
		"The circuit breaker is PER-AXIS",
		"20 consecutive errored LLM calls",
		"2 consecutive SAME-CLASS",
	} {
		assert.Contains(t, helpManage, marker, "helpManage must describe the real trip conditions: %q", marker)
	}
	// The surviving accurate claims, so a rewrite cannot satisfy the negative by
	// deleting the block.
	assert.Contains(t, helpManage, "resume_pipeline is the ONLY exit",
		"helpManage must keep the verified no-self-heal contract")
	assert.Contains(t, helpManage, "cleared on restart",
		"helpManage must keep the verified in-memory pause-state contract")
}

// firstWorkedCallPayload extracts the JSON object literal of the first
// `<tool>(` worked example in a help body, so an assertion about a required
// field reads the EXAMPLE rather than the prose describing it. It brace-matches
// rather than regexing, because the plan/test-plan examples nest several levels.
func firstWorkedCallPayload(t *testing.T, help, callPrefix string) string {
	t.Helper()
	idx := strings.Index(help, callPrefix)
	require.GreaterOrEqual(t, idx, 0, "no %s worked example found in the help body", callPrefix)
	rest := help[idx+len(callPrefix):]
	open := strings.Index(rest, "{")
	require.GreaterOrEqual(t, open, 0, "the %s example carries no JSON object", callPrefix)
	depth := 0
	for i := open; i < len(rest); i++ {
		switch rest[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				payload := rest[open : i+1]
				// Parse it: an example that is not valid JSON is itself a defect,
				// and a brace-matcher that silently returned a truncated span
				// would make every Contains below vacuous.
				var probe map[string]any
				require.NoError(t, json.Unmarshal([]byte(payload), &probe),
					"the %s worked example is not valid JSON:\n%s", callPrefix, payload)
				return payload
			}
		}
	}
	t.Fatalf("unbalanced braces in the %s worked example", callPrefix)
	return ""
}

// TestHelpAndSchemas_NoDerivationClaims pins the retirement of every claim that
// a creator composes a summary on the author's behalf. Nothing does any more, so
// each surface that said one did is checked in the NEGATIVE-plus-POSITIVE shape
// this file's header describes: the refuted wording must stay gone AND the
// replacement must be present, so a later rewrite cannot satisfy the negative by
// deleting the block and leaving the reader with nothing.
//
// A census regex can only assert the absence half. This is the other half, and
// it is why both instruments exist.
func TestHelpAndSchemas_NoDerivationClaims(t *testing.T) {
	t.Run("help(mutate) no longer promises a clamped derivation on update", func(t *testing.T) {
		assert.NotContains(t, helpMutate, "the derived one is clamped",
			"the update path composes nothing, so there is no derivation to clamp")
		assert.NotContains(t, helpMutate, "no derive source changed",
			"the derive-source disposition is retired along with the derivation")
		assert.Contains(t, helpMutate, "nothing composes a summary on any path",
			"the replacement must state the new rule, not merely omit the old one")
	})

	t.Run("help(overview) no longer names record_decision as a synthesizer", func(t *testing.T) {
		assert.NotContains(t, helpOverview, "derived from choice",
			"record_decision now requires an author summary and composes none")
		assert.Contains(t, helpOverview, "NO creator synthesizes a Summary any more",
			"the replacement must say so positively")
	})

	t.Run("help(create_research) requires the per-question summary", func(t *testing.T) {
		assert.NotContains(t, helpCreateResearch, "handler DERIVES one from question + context",
			"nothing composes a question summary")
		assert.NotContains(t, helpCreateResearch, "optional per-question search-optimized summary",
			"it is required, not optional")
		assert.Contains(t, helpCreateResearch, "REQUIRED per-question search-optimized summary",
			"the replacement must state the requirement")
	})

	t.Run("help(thoughts) requires a charge summary as well as a think summary", func(t *testing.T) {
		assert.NotContains(t, helpThoughts, "NOT auto-derived from content",
			"the think-scoped derivation denial is superseded by a rule covering both ops")
		// Asserted in two halves because the help text wraps between them.
		assert.Contains(t, helpThoughts, "Nothing composes one from content; charge requires",
			"the think entry must deny composition AND point at the charge requirement")
		assert.Contains(t, helpThoughts, "a summary of its own (see the charge section)",
			"the pointer must name where the charge requirement is documented")
		assert.Contains(t, helpThoughts, "summarizes the EVIDENCE THIS CHARGE RECORDS",
			"the charge entry must say what its summary is ABOUT, not merely that it is required")
	})

	t.Run("the create_research schema declares the question summary required", func(t *testing.T) {
		items := CreateResearchToolDef().InputSchema.Properties["questions"].Items
		require.NotNil(t, items, "the questions array must declare an item shape")
		summary, ok := items.Properties["summary"]
		require.True(t, ok, "the item object is closed, so an undeclared summary is unsatisfiable")
		assert.NotContains(t, summary.Description, "Optional",
			"the property description must not still call it optional")
		assert.Contains(t, summary.Description, "Required",
			"the property description is where a schema-reading author learns the requirement")
	})

	t.Run("the mutate schema covers the answer arm's summary", func(t *testing.T) {
		summary := MutateToolDef().InputSchema.Properties["summary"]
		assert.NotContains(t, summary.Description, "never derived from the description and command",
			"the retired wording spoke of a derivation")
		assert.Contains(t, summary.Description, "mutate(answer) requires it too",
			"the answer arm now takes a summary and the schema is where an author reads that")
	})
}

// helpTopicText returns one help topic's rendered text, read from the SAME
// registry help() dispatches through, so a test cannot assert about a topic the
// tool would not serve.
func helpTopicText(t *testing.T, topic string) string {
	t.Helper()
	text, ok := helpTopics[topic]
	require.Truef(t, ok, "help topic %q is not registered", topic)
	require.NotEmptyf(t, text, "help topic %q is empty", topic)
	return text
}

// TestHelp_AnnotationRulesAgreeAcrossTopics pins that the two help topics
// describing plan_annotation writes tell the same story, and that both match the
// code.
//
// WHY IT EXISTS. help("node_types") and help("mutate") each describe when an
// annotation write is refused, and they drifted: node_types kept asserting that
// create_batch's edges[] "cannot carry a method or evidence" — the premise a
// later commit retracted, and the one the batch guard was rebuilt to stop
// believing — and listed upsert under the metadata-key rule when the code refuses
// it by TYPE. A caller reading the two topics got two different systems.
//
// THE ASSERTIONS ARE AGAINST THE CODE'S OWN CONSTANTS AND SCHEMA where it can be,
// not against a transcription, so this cannot become a third copy that drifts.
func TestHelp_AnnotationRulesAgreeAcrossTopics(t *testing.T) {
	nodeTypes := helpTopicText(t, "node_types")
	mutate := helpTopicText(t, "mutate")

	t.Run("neither says create_batch edges cannot carry the severity", func(t *testing.T) {
		for name, text := range map[string]string{"node_types": nodeTypes, "mutate": mutate} {
			assert.NotContains(t, text, "cannot carry a method or evidence",
				"help(%q) states the retracted premise; engine.edgeBody decodes method and evidence "+
					"and TestCompileMutate_CreateBatchEdgeMetadata asserts they land", name)
		}
	})

	t.Run("both name the edge carriers create_batch actually accepts", func(t *testing.T) {
		edges, ok := MutateToolDef().InputSchema.Properties["edges"]
		require.True(t, ok)
		require.NotNil(t, edges.Items)
		for _, key := range []string{"method", "evidence"} {
			require.Contains(t, edges.Items.Properties, key,
				"the schema must declare %q, or the help below is describing something that does not exist", key)
		}
		assert.Contains(t, mutate, "method", "help(mutate) names the carriers a coherent annotation edge uses")
		assert.Contains(t, mutate, "evidence")
	})

	t.Run("both describe the upsert refusal as a TYPE rule", func(t *testing.T) {
		assert.Contains(t, nodeTypes, "refused BY TYPE",
			"help(node_types) must say upsert is refused by type, which is what the code does")
		assert.NotContains(t, nodeTypes, "bulk_update_metadata/upsert",
			"listing upsert among the metadata-key operations is the mis-statement this test closes")
	})

	t.Run("neither claims the replacement text lives in the body", func(t *testing.T) {
		for name, text := range map[string]string{"node_types": nodeTypes, "mutate": mutate} {
			assert.NotContains(t, text, "the text itself lives in the body",
				"help(%q) names a second carrier for the replacement text; the guard reads metadata.%s and "+
					"nothing reads a body", name, kgtypes.AnnotationReplacementKey)
		}
	})
}

// TestHelp_SectionReadsAreDocumentedPerFormat pins that wherever the two render
// formats of a chunked-plan read differ, the documentation SAYS which does what.
//
// THE DEFECT IT CLOSES was documentation with no format qualifier over behavior
// that had one: the schema, help(assemble) and the generated guide all said
// supplying neither section bound returns the plan's index and tree alone, and
// that held for text and not for json, where the same call returned every body —
// 76,093 bytes against 2,458 on a ten-section fixture of realistic size, and above
// the point where a result spills. A caller following the documentation got
// exactly the outcome the paging requirement exists to prevent.
//
// THE ASSERTIONS READ THE SCHEMA rather than a transcription of it, so this cannot
// become another copy that drifts from the tool.
func TestHelp_SectionReadsAreDocumentedPerFormat(t *testing.T) {
	assembleText := helpTopicText(t, "assemble")
	nodeTypes := helpTopicText(t, "node_types")

	t.Run("the schema names what each format does with no range", func(t *testing.T) {
		props := AssembleToolDef().InputSchema.Properties
		end, ok := props["section_end"]
		require.True(t, ok, "section_end must be declared, or the handler's read of it is undeclared")
		assert.Contains(t, end.Description, "BOTH FORMATS",
			"the default is documented without a format qualifier unless it says it holds for both")
		assert.Contains(t, end.Description, "body_omitted",
			"and names the marker a json reader sees, so an absent body is not read as an empty section")
	})

	t.Run("help(assemble) says the rules hold in both formats", func(t *testing.T) {
		assert.Contains(t, assembleText, "THE SAME RULES HOLD IN BOTH FORMATS")
		assert.Contains(t, assembleText, "body_omitted")
		assert.Contains(t, assembleText, "ANNOTATIONS REACH EVERY FORMAT",
			"annotation state is documented as format-independent because it now is")
	})

	t.Run("help(node_types) qualifies the section read by format", func(t *testing.T) {
		assert.Contains(t, nodeTypes, "in BOTH text and json",
			"a section read returns its body and annotations in either format, and the help must say so")
	})
}
