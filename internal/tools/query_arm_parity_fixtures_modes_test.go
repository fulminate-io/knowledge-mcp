// SPDX-License-Identifier: Apache-2.0

package tools

// query_arm_parity_fixtures_modes_test.go holds the parity drive fixtures for the
// COMPOSITE-MODE, CODE, RENDERING and REFLECT halves of the query arm registry.
// The shared fixture shape, the seeded fake and the graph-family fixtures live in
// query_arm_parity_fixtures_test.go; the harness and its probe rules live in
// query_arm_parity_test.go. The split is a file-length concern only.

import (
	"context"
	"maps"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

// qpParityRepo is the code-graph repo every code fixture targets.
const qpParityRepo = "parity-repo"

// qpTopologyAnalyzer is the analyzer name the topology fixture drives.
//
// PRECONDITION CLASS (d), made concrete. InterceptTopology dispatches through the
// foundation registry, and the analyzer packages self-register from their own
// init() — none of which this package imports, so foundation.All() is EMPTY in
// this test binary and every real algorithm name returns "unknown analyzer". A
// fixture naming pagerank or dsm would therefore measure the registry miss rather
// than the arm. Registering a probe analyzer here is what makes the arm drivable
// as specified: it is a real registry entry, reached through the real dispatch,
// and it issues a real read through the Request.Caller seam so the arm's rows are
// asserted against an actual wire read.
const qpTopologyAnalyzer = "parity_probe"

// parityProbeAnalyzer is the registered probe analyzer. Its Run issues ONE read
// through the wire seam every analyzer uses, then returns a single finding, so
// the topology arm reaches a non-error render with a positive read count.
type parityProbeAnalyzer struct{}

func (parityProbeAnalyzer) Name() string { return qpTopologyAnalyzer }

// qpLastTopologyRequest records the Request the probe analyzer was last handed.
// It is what lets a dispatch test observe a field the probe otherwise honors by
// ignoring — path_prefix in particular, which reaches no plan and no render, so
// there is nowhere else to read it from. Written and read only from tests in
// this package, which run sequentially (no test here calls t.Parallel).
var qpLastTopologyRequest foundation.Request

func (parityProbeAnalyzer) Run(ctx context.Context, req foundation.Request) ([]foundation.Finding, error) {
	qpLastTopologyRequest = req
	if req.Caller != nil {
		_, _ = req.Caller.Execute(ctx, &knowledgev1.ExecuteRequest{
			Plan: &knowledgev1.ExecuteRequest_Query{
				Query: &knowledgev1.QueryPlan{ById: qpSeedKnowledge},
			},
		})
	}
	return []foundation.Finding{{
		Algorithm: qpTopologyAnalyzer,
		Title:     "parity probe finding",
		Summary:   "the drive-through harness's registered analyzer",
	}}, nil
}

func init() { foundation.Register(parityProbeAnalyzer{}) }

// queryParityModeFixtures is the composite-mode, code, rendering and reflect half
// of the fixture table.
func queryParityModeFixtures() map[armID]queryParityFixture {
	fx := map[armID]queryParityFixture{
		armCorrelations: {
			entry:         InterceptQueryCorrelationsPivot,
			base:          map[string]any{"graph": "knowledge", "mode": "correlations"},
			discriminants: map[string]any{"graph": "knowledge", "mode": "correlations"},
			// limit is the RENDER row cap, not a plan field: with no correlations to
			// render, a cap that is never reached leaves no literal anywhere.
			opaque: map[string]bool{"limit": true},
		},

		armPivot: {
			entry: InterceptQueryCorrelationsPivot,
			base: map[string]any{
				"graph": "knowledge", "mode": "pivot",
				"rows": "parity-rows", "cols": "parity-cols",
			},
			discriminants: map[string]any{"graph": "knowledge", "mode": "pivot"},
			// text seeds a client-side search rather than a plan field.
			opaque: map[string]bool{"text": true},
		},

		armExplain: {
			entry: InterceptQueryExplainTimeline,
			base: map[string]any{
				"graph": "knowledge", "mode": "explain", "id": qpSeedKnowledge,
			},
			discriminants: map[string]any{"graph": "knowledge", "mode": "explain", "id": qpSeedKnowledge},
			// extra carries the {a,b} node pair by key; a probe key the arm does not
			// read leaves the single-node form selected and lands nowhere.
			opaque: map[string]bool{"extra": true},
		},

		armTimeline: {
			entry: InterceptQueryExplainTimeline,
			base: map[string]any{
				"graph": "knowledge", "mode": "timeline", "time_field": "CreatedAt",
			},
			discriminants: map[string]any{"graph": "knowledge", "mode": "timeline"},
			// extra carries the bucket size by key, limit is a RETENTION cap and text
			// seeds the pivot's shared client-side search: none reaches the plan or
			// the render.
			opaque: map[string]bool{"extra": true, "limit": true, "text": true},
		},

		armCodeModules: {
			entry: InterceptQueryModulesCodeStats,
			base: map[string]any{
				"graph": "code", "mode": "modules", "repo": qpParityRepo,
			},
			discriminants: map[string]any{"graph": "code", "mode": "modules"},
			// The path filter is applied client-side while rolling packages up, so it
			// does not ride the per-repo plan.
			opaque: map[string]bool{"path_prefix": true},
		},

		armCodeStats: {
			entry: InterceptQueryModulesCodeStats,
			base: map[string]any{
				"graph": "code", "mode": "stats", "repo": qpParityRepo,
			},
			discriminants: map[string]any{"graph": "code", "mode": "stats"},
		},

		armAnalyzeNode: {
			entry: InterceptQueryAnalyzeNode,
			base: map[string]any{
				"graph": "code", "id": qpSeedCodeUnit, "repo": qpParityRepo,
			},
			discriminants: map[string]any{"graph": "code", "mode": "", "id": qpSeedCodeUnit},
			// Both depths are CLAMPED before they reach the traversal plan, so the
			// probe value is not the value that travels.
			opaque: map[string]bool{"caller_depth": true, "callee_depth": true},
		},

		armCodeSearch: {
			entry: InterceptQueryCodeSearch,
			base: map[string]any{
				"graph": "code", "repo": qpParityRepo, "text": "probe-text",
			},
			discriminants: map[string]any{"graph": "code", "mode": "", "id": ""},
			// The presentation group shapes the RENDER of rows the fake returns none
			// of; the caller vector is broadcast into the per-query vector slice
			// rather than onto a plan; and the limit bounds a client-side engine
			// search. `fields` projects only on the json render path this probe does
			// not take — this base carries no format, so the probe's distinctive
			// could not appear whatever the arm does with it. What the arm DOES with
			// it is proven instead by the seam tests, which drive both entry points
			// with a real projection and assert the rendered key set.
			opaque: map[string]bool{
				"query_vector": true, "limit": true,
				"path_prefix": true, "test_kinds": true, "fields": true,
			},
		},

		armFileSymbols: {
			entry: InterceptFileSymbols,
			base: map[string]any{
				"graph": "code", "mode": "file_symbols",
				"repo": qpParityRepo, "file_path": qpSeedFilePath,
			},
			discriminants: map[string]any{"graph": "code", "mode": "file_symbols"},
			// A path is resolved as a FILE-NODE ID, so an arbitrary probe resolves to
			// nothing and the arm returns "no symbols found" — the error path, not the
			// declared class. Both spellings are probed with a SECOND seeded path, so
			// the row observes real routing rather than restating the base.
			probeValues: map[string]any{
				"file_path": qpSeedFileOther, "file_paths": []any{qpSeedFileOther},
			},
			// path_prefix is only read as the FALLBACK spelling of file_path, which
			// the base already supplies, so a probe on it is shadowed. `limit` is a
			// RENDER cap: it selects how many rows are emitted rather than landing a
			// value in a plan or the render, so no captured read can carry the probe —
			// the same shape as the rules arm's client-side page, listed opaque rather
			// than claiming an observability the arm cannot have.
			// TestFileSymbols_LimitCapsSymbolRows witnesses the consumption instead.
			opaque: map[string]bool{"path_prefix": true, "limit": true},
		},

		armMetadataStats: {
			entry:         InterceptQueryMetadataStats,
			base:          map[string]any{"graph": "knowledge", "mode": "metadata_stats"},
			discriminants: map[string]any{"graph": "knowledge", "mode": "metadata_stats"},
		},

		armTopology: {
			entry: InterceptTopology,
			base: map[string]any{
				"graph": "knowledge", "mode": "topology", "algorithm": qpTopologyAnalyzer,
			},
			discriminants: map[string]any{
				"graph": "knowledge", "mode": "topology", "algorithm": qpTopologyAnalyzer,
			},
			// TopK, the Extra knob map and PathPrefix ride the analyzer Request, which
			// the probe analyzer honors by ignoring — none reaches a plan or the
			// findings render. The opaque entry suppresses the OBSERVATION assertion
			// only; the probe still has to reach a non-error render, which is why
			// intercept_topology_test.go's init adds this analyzer to the
			// path_prefix honoring set.
			opaque:       map[string]bool{"top_k": true, "extra": true, "path_prefix": true},
			precondition: "class (d): topology dispatches through the foundation registry, seeded here",
		},

		// PRECONDITION CLASS (f): unreachable through InterceptQuery. See the harness
		// header, precondition class (f).
		armEngineDispatch: {
			entry: InterceptQuery,
			base:  map[string]any{"graph": "knowledge", "text": "probe-text"},
			discriminants: map[string]any{
				"graph": "knowledge", "mode": "", "text": "probe-text",
			},
			// compileQuery lowers the browse-shape filters only when the payload is
			// NOT a text search — a text arm wins over the type/status/meta arms — so
			// those four rows are driven against a bare browse payload, which is the
			// shape that actually reads them.
			paramBase: map[string]map[string]any{
				"type":  {"graph": "knowledge"},
				"types": {"graph": "knowledge"},
				"meta":  {"graph": "knowledge"},
				// A bare status filter is not a reducible shape on its own — Compile
				// declines it — so status is driven on a TYPE browse, the shape whose
				// plan actually carries it.
				"status": {"graph": "knowledge", "type": "finding"},
			},
			// `fields` is a RENDER projection the compiler does not carry into the
			// plan; it is applied when the response is rendered for the caller.
			opaque:   map[string]bool{"fields": true},
			behavior: qGateOnly,
			precondition: "class (f): InterceptQuery's embed gate keys on the undeclared `query` field, " +
				"so the only payload reaching this arm is rejected by the unknown-key sweep",
		},

		armPlanTree: {
			entry:         InterceptQueryPlanTree,
			base:          map[string]any{"mode": "plan_tree", "id": qpSeedProject},
			discriminants: map[string]any{"graph": "knowledge", "mode": "plan_tree", "id": qpSeedProject},
			// `fields` is CONSUMED here — the arm validates the projection and
			// projects every row — but the harness's fields probe is the
			// per-metadata-key form (the only value the closed projection vocabulary
			// leaves freely distinctive), and a per-metadata-key projection is DEFINED
			// to be omitted from a node that does not carry the key. The seeded parity
			// nodes carry no metadata, so the probe cannot appear in the render even
			// under a fully correct implementation. Most fields-consuming arms list it
			// opaque for their own version of this reason.
			// TestPlanTree_FieldsProjectsAndRefusesUnknownKey witnesses the
			// consumption, driving real projections and asserting BOTH the requested
			// keys present and the unrequested ones absent.
			opaque: map[string]bool{"fields": true},
		},

		armEvidence: {
			entry:         InterceptQueryEvidence,
			base:          map[string]any{"mode": "evidence", "id": qpSeedDecision},
			discriminants: map[string]any{"graph": "knowledge", "mode": "evidence", "id": qpSeedDecision},
		},

		armLineage: {
			entry:         InterceptQueryLineage,
			base:          map[string]any{"mode": "lineage", "id": qpSeedKnowledge},
			discriminants: map[string]any{"graph": "knowledge", "mode": "lineage", "id": qpSeedKnowledge},
		},

		// The one arm whose claim point sits BELOW a read: it fetches the node to
		// learn whether the id is a project-domain type, and DECLINES to a later
		// claimant when it is not. The gate therefore runs after exactly one read,
		// which the rejected rows assert as an equality rather than a zero.
		armExamineProjects: {
			entry:         InterceptQueryExamineProjects,
			base:          map[string]any{"mode": "examine", "id": qpSeedProject},
			discriminants: map[string]any{"graph": "knowledge", "mode": "examine", "id": qpSeedProject},
			preGateReads:  1,
			precondition:  "class (g): the claim point sits below the node fetch that decides ownership",
		},

		armExamine: {
			entry:         InterceptQueryExamine,
			base:          map[string]any{"mode": "examine", "id": qpSeedKnowledge},
			discriminants: map[string]any{"graph": "knowledge", "mode": "examine", "id": qpSeedKnowledge},
		},
	}
	maps.Copy(fx, queryParityReflectFixtures())
	return fx
}

// queryParityReflectFixtures is the ten-arm reflect half. Every one enters through
// InterceptThoughts, whose query branch is interceptQueryReflect, and every one
// runs its gate ONCE at the top of that dispatcher (accountReflectArm) — which is
// why every reflect rejection is a zero-read rejection.
func queryParityReflectFixtures() map[armID]queryParityFixture {
	// PRECONDITION CLASS (a), MEASURED PER ARM rather than assumed for the whole
	// cache-served family. Driving them showed the family splits: personality,
	// tensions, blind_spots and summary answer cold from the propagation loop's
	// cache with ZERO reads against a nil provider, while clusters, influence and
	// evolution still read the thought corpus over the wire before they can report
	// on it. Only the first four get the zero-read behavior class.
	const coldClass = "class (a): a cache-served reflect arm answering cold with no provider"
	cacheServed := func(mode string) queryParityFixture {
		return queryParityFixture{
			entry:         InterceptThoughts,
			base:          map[string]any{"graph": "knowledge", "mode": mode},
			discriminants: map[string]any{"graph": "knowledge", "mode": mode},
			// The cluster filter and the granularity rollup select WHICH cached view
			// is reported; the cold message reports none of them back.
			opaque:       map[string]bool{"cluster": true, "granularity": true},
			behavior:     qBehavesWithoutRead,
			precondition: coldClass,
		}
	}

	// tensions additionally consumes `limit`, which caps the RENDERED rows. The cold
	// message renders no rows at all, so neither a read nor the render can witness
	// the probe here — the same reason cluster and granularity are opaque on this
	// family, and the same disposition armReflectInfluence gives its own limit.
	// TestReflectTensions_LimitCapsRenderedRows is what witnesses the consumption.
	tensions := cacheServed("tensions")
	tensions.opaque["limit"] = true

	return map[armID]queryParityFixture{
		armReflectPersonality: cacheServed("personality"),
		armReflectTensions:    tensions,
		armReflectBlindSpots:  cacheServed("blind_spots"),
		armReflectSummary:     cacheServed("summary"),

		armReflectClusters: {
			entry:         InterceptThoughts,
			base:          map[string]any{"graph": "knowledge", "mode": "clusters"},
			discriminants: map[string]any{"graph": "knowledge", "mode": "clusters"},
		},

		// Evolution requires BOTH cluster ids: without them the arm refuses before
		// reading, so they are preconditions in the base and arm-preserving probes.
		armReflectEvolution: {
			entry: InterceptThoughts,
			base: map[string]any{
				"graph": "knowledge", "mode": "evolution",
				"cluster_a": "parity-cluster-a", "cluster_b": "parity-cluster-b",
			},
			discriminants: map[string]any{
				"graph": "knowledge", "mode": "evolution",
				"cluster_a": "parity-cluster-a", "cluster_b": "parity-cluster-b",
			},
		},

		armReflectInfluence: {
			entry:         InterceptThoughts,
			base:          map[string]any{"graph": "knowledge", "mode": "influence"},
			discriminants: map[string]any{"graph": "knowledge", "mode": "influence"},
			// The influence report is ranked from the fetched corpus; limit and sort
			// shape the SELECTION and the display order rather than a plan field.
			opaque: map[string]bool{"limit": true, "sort": true},
		},

		armReflectThoughtExamine: {
			entry:         InterceptThoughts,
			base:          map[string]any{"graph": "knowledge", "mode": "examine", "id": qpSeedThought},
			discriminants: map[string]any{"graph": "knowledge", "mode": "examine", "id": qpSeedThought},
		},

		armReflectSimulate: {
			entry: InterceptThoughts,
			base: map[string]any{
				"graph": "knowledge", "mode": "simulate",
				"action": "invalidate_thought", "target": qpSeedThought,
			},
			discriminants: map[string]any{"graph": "knowledge", "mode": "simulate"},
			// action/target are VALIDATED inputs — an arbitrary probe is refused
			// ("unknown simulation action", "target not found") before the arm runs,
			// so both are probed with values the simulator accepts.
			probeValues: map[string]any{
				"action": "invalidate_thought", "target": qpSeedThought,
			},
			// action/polarity/weight are simulate inputs decoded off the raw payload
			// and applied to the in-memory projection, not to a plan.
			opaque: map[string]bool{"action": true, "polarity": true, "weight": true},
		},

		armReflectRecall: {
			entry:         InterceptThoughts,
			base:          map[string]any{"graph": "knowledge", "mode": "timeline"},
			discriminants: map[string]any{"graph": "knowledge", "mode": "timeline"},
			// The three bounded scalars are RANGE-VALIDATED by the recall handler
			// (valence in [-1,1], consistency in [0,1]), so the default numeric probe
			// is refused before the arm runs and each is probed in range instead.
			probeValues: map[string]any{
				"valence_min": 0.5, "valence_max": 0.5, "consistency_max": 0.5,
			},
			// recallParamsFromQuery forwards these onto the recall payload, which the
			// client recall handler applies over the fetched thought set rather than
			// pushing into the plan.
			opaque: map[string]bool{
				"text": true, "limit": true, "status": true, "type": true,
				"session": true, "connected_to": true,
				"valence_min": true, "valence_max": true,
				"magnitude_min": true, "consistency_max": true,
			},
		},
	}
}
