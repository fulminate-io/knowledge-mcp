// SPDX-License-Identifier: Apache-2.0

package tools

// query_arm_parity_fixtures_test.go holds the parity harness's shared drive
// machinery — the fixture shape, the seeded fake, the deps wrapper — plus the
// per-arm fixtures for the GRAPH-FAMILY half of the registry (cloud/cicd,
// practice/linkage/web-pdf, the two knowledge search arms, and the four
// single-arm per-graph entry points). The composite-mode, code, rendering and
// reflect fixtures live in query_arm_parity_fixtures_modes_test.go; the harness
// and its probe rules live in query_arm_parity_test.go. The split is a
// file-length concern only, mirroring how the registry itself is split.
//
// One fixture per dispatch arm. `base` is the MINIMAL payload that selects that
// arm AND satisfies its own preconditions — an arm whose base fails its
// precondition produces an error result and an empty read list, which reads as a
// rejection and would silently corrupt every row for that arm. The harness
// asserts the base is sound on every row (queryParityAssertBehaved), so a broken
// base fails loudly rather than measuring nothing.

import (
	"context"
	"maps"
	"testing"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// queryArmBehavior is what "the arm still behaves" MEANS for one arm. The default
// is the plan's rule; the other two are the precondition classes the harness
// header enumerates, and each arm that uses one carries a `precondition` label.
type queryArmBehavior int

const (
	// qBehavesWithRead: claims the call, returns a non-error result, issues at
	// least one read. Every arm that touches the graph.
	qBehavesWithRead queryArmBehavior = iota
	// qBehavesWithoutRead: claims the call, returns a non-error result, issues
	// ZERO reads — the cache-served reflect arms and the retired search arms.
	qBehavesWithoutRead
	// qGateOnly: the arm's intercept cannot be reached, so the gate is driven
	// directly and the consumed class is observed against the compiled plan.
	qGateOnly
)

// Seeded fixture ids. Every id a base or probe references resolves in the fake,
// because an unresolved id turns a consumed-param row into a not-found error and
// the row then measures the error path rather than the arm.
const (
	qpSeedResource  = "parity-resource-node"
	qpSeedLinkage   = "parity-linkage-node"
	qpSeedKnowledge = "parity-knowledge-node"
	qpSeedProject   = "parity-project-node"
	qpSeedDecision  = "parity-decision-node"
	qpSeedThought   = "parity-thought-node"
	qpSeedCodeUnit  = "parity-code-symbol"
	qpSeedCodeOther = "parity-code-symbol-other"
	qpSeedCustom    = "parity-custom-node"
	qpSeedFilePath  = "parity/file.go"
	qpSeedFileOther = "parity/other.go"
)

// queryParityVectorB64 is a base64 probe for `query_vector`. The value must
// base64-DECODE or the arms that carry it drop it before the plan is built, which
// would fail a correctly-routed consumed row. "cHJvYmUtcXY=" decodes to
// "probe-qv"; the encoded form is what appears on the wire, so it is also the
// distinctive the observation looks for.
const queryParityVectorB64 = "cHJvYmUtcXY="

// queryParityFixture describes how to drive one arm through the fake.
type queryParityFixture struct {
	// entry is the intercept the arm lives in. Driven directly rather than
	// through the chain so a row measures THIS arm and not whichever claimant
	// happens to run first for the payload.
	entry func(context.Context, ClientDeps, kgtools.CallToolParams) (bool, kgtools.ToolResult)
	// base is the minimal arm-selecting, precondition-satisfying payload.
	base map[string]any
	// discriminants maps each arm-SELECTING param to an arm-PRESERVING probe. A
	// param listed here is observed as "the arm is still selected", never as a
	// literal — injecting an arbitrary value would deselect the arm and the row
	// would measure a different arm's behavior. An EMPTY value is the
	// arm-preserving probe for an EMPTINESS-GATE param (an arm selected by the
	// param being absent); the registry header's emptiness-gate rule is what makes
	// those cells consumed-as-discriminant rather than rejected.
	discriminants map[string]any
	// probeValues overrides the probe VALUE for one param while keeping the full
	// class assertion — unlike discriminants, which also drops the assertion to
	// selection-only. Needed where an ordinary probe would re-route the call
	// before the arm's gate runs: the knowledge arms bail to the recall surface on
	// a non-empty thought filter (hasThoughtQueryFilter), so those params are
	// probed with their zero value, which keeps the call on the arm and still
	// counts as SUPPLIED for the gate (accounting reads the raw KEY SET, not the
	// values).
	probeValues map[string]any
	// opaque names the params this arm consumes in a way no read and no render can
	// show — a bound that is never exceeded, a filter applied over already-fetched
	// rows, a map read by key. Their rows assert selection only, exactly like the
	// whole-surface members of querySelectionOnlyParams; listing them per arm is
	// what keeps the exemption auditable instead of a blanket escape.
	opaque map[string]bool
	// deselecting names the params whose supply routes the call to a DIFFERENT
	// claimant before this arm's gate runs, making the cell unreachable. See the
	// harness header's precondition class (h).
	deselecting map[string]bool
	// paramBase replaces `base` for one param, for an arm whose consumed set
	// cannot be exercised from a single payload shape.
	paramBase map[string]map[string]any
	// behavior is what "still behaves" means for this arm; precondition names the
	// class from the harness header whenever behavior is not the default.
	behavior     queryArmBehavior
	precondition string
	// preGateReads is the number of reads the arm legitimately issues BEFORE its
	// claim point, so a rejected row asserts that exact count rather than zero.
	// Non-zero for exactly one arm — see armExamineProjects, whose claim point is
	// deliberately below the node fetch that decides it owns the call.
	preGateReads int
}

// queryParitySearcher is the SegmentSearcher the six ranked-search arms need. It
// returns one hit whose id the fake resolves, so the arm reaches its hydrate read
// rather than short-circuiting on an empty result set — the hydrate is the read
// those arms' rows are asserted against.
type queryParitySearcher struct{}

func (queryParitySearcher) Search(
	_ context.Context, _ kgtypes.GraphType, _, _ string, _ []byte, _ int,
) ([]searchengine.Hit, error) {
	return []searchengine.Hit{{ID: qpSeedKnowledge, Score: 1}}, nil
}

// SearchOverlay makes this fake satisfy the two-pool seam as well, which the code
// search arm REQUIRES whenever a branch is set: that arm rejects a branch search
// outright when the engine cannot serve base and overlay together, so a probe row
// driving the branch param would otherwise observe that rejection rather than the
// param's declared class.
func (queryParitySearcher) SearchOverlay(
	_ context.Context, _ kgtypes.GraphType, _, _, _ string, _ []byte, _ int,
) ([]searchengine.Hit, error) {
	return []searchengine.Hit{{ID: qpSeedKnowledge, Score: 1}}, nil
}

// queryParityDeps wraps the fake with the two seams the search arms require:
// interceptTestDeps reports PipelineReady() true already, and the searcher knob
// supplies the segment engine those arms would otherwise report unavailable.
func queryParityDeps(fc *fakeGraphCaller) interceptTestDeps {
	return interceptTestDeps{gc: fc, searcher: queryParitySearcher{}}
}

// queryParitySeed builds the fake with every fixture node seeded, in every graph
// family the arms target. One shared seed keeps any fixture id resolvable from
// any arm's row.
func queryParitySeed(t *testing.T) *fakeGraphCaller {
	t.Helper()
	return &fakeGraphCaller{
		queryResponses: map[string]kgtools.ToolResult{
			qpSeedResource:  qpNode(t, qpSeedResource, string(kgtypes.NodeCloudResource)),
			qpSeedLinkage:   qpNode(t, qpSeedLinkage, string(kgtypes.NodeProxy)),
			qpSeedKnowledge: qpNode(t, qpSeedKnowledge, "finding"),
			qpSeedProject:   qpNode(t, qpSeedProject, "project"),
			qpSeedDecision:  qpNode(t, qpSeedDecision, "decision"),
			qpSeedThought:   qpNode(t, qpSeedThought, string(kgtypes.NodeThought)),
			qpSeedCodeUnit:  qpNode(t, qpSeedCodeUnit, "function"),
			qpSeedCustom:    qpNode(t, qpSeedCustom, "document"),
			// The file_symbols arm resolves a path as a FILE-NODE ID and walks
			// CONTAINS to its symbols, so the two seeded paths are node ids. Two of
			// them, because file_path and file_paths are CONSUMED and a probe must
			// carry a DIFFERENT resolvable path than the base for the row to observe
			// routing rather than restate the base.
			qpSeedFilePath:  qpNode(t, qpSeedFilePath, string(kgtypes.NodeFile)),
			qpSeedFileOther: qpNode(t, qpSeedFileOther, string(kgtypes.NodeFile)),
			qpSeedCodeOther: qpNode(t, qpSeedCodeOther, "function"),
		},
		nodeMatchResults: map[graphKey][]*knowledgev1.Node{
			{Type: "knowledge"}: {
				{Id: qpSeedKnowledge, Type: "finding", SymbolName: "parity finding"},
				{Id: "parity-rule-node", Type: string(kgtypes.NodeRule), SymbolName: "parity rule"},
			},
			{Type: "cloud", Name: qpParityAccount}: {
				{Id: qpSeedResource, Type: string(kgtypes.NodeCloudResource), SymbolName: "parity resource"},
			},
			{Type: "linkage"}: {{Id: qpSeedLinkage, Type: string(kgtypes.NodeProxy)}},
			// The code-graph symbol set the file_symbols arm collects per path. The
			// fake answers a Match scan from the (type,name) key regardless of the
			// probed path, so ONE seeded symbol keeps every file_path / file_paths /
			// path_prefix probe on the arm's success path — without it the arm
			// returns "no symbols found" and every one of its rows measures that
			// error instead of the declared class.
			{Type: "code", Name: qpParityRepo}: {
				{Id: qpSeedCodeUnit, Type: "function", SymbolName: "ParityFunc"},
			},
			// The log graphs the logs arm rebuilds its engine from — one under the
			// base's query_id and one under the `name` probe's, because `name` IS the
			// log graph selector and a probe on it targets a different graph.
			{Type: "logs", Name: qpParityLogGraph}: qpLogNodes(),
			{Type: "logs", Name: "probe-name"}:     qpLogNodes(),
		},
		edgesByID: map[string][]*knowledgev1.Edge{
			qpSeedKnowledge: {{FromId: qpSeedKnowledge, ToId: qpSeedDecision, Type: "relates-to"}},
			qpSeedDecision:  {{FromId: qpSeedDecision, ToId: qpSeedKnowledge, Type: "informed-by"}},
			qpSeedProject:   {{FromId: qpSeedProject, ToId: qpSeedKnowledge, Type: "contains"}},
		},
		traversalByRoot: map[string][]*knowledgev1.Node{
			qpSeedProject:   {{Id: qpSeedKnowledge, Type: "finding"}},
			qpSeedFilePath:  {{Id: qpSeedCodeUnit, Type: "function", SymbolName: "ParityFunc"}},
			qpSeedFileOther: {{Id: qpSeedCodeOther, Type: "function", SymbolName: "ParityOtherFunc"}},
		},
		statsResp: &knowledgev1.GraphStats{
			NodeCount: 2, EdgeCount: 1, NodesByType: map[string]int64{"finding": 2},
		},
	}
}

// qpParityAccount is the cloud/cicd account key every resource fixture targets;
// the fake seeds its node set under the same key. qpParityLogGraph is the
// query_id the logs fixture targets.
const (
	qpParityAccount  = "parity-account"
	qpParityLogGraph = "parity-logs"
)

// qpLogNodes is the seeded log-graph node set getOrFetchLogState rebuilds its
// engine from. The fake serves the same set for the template, stream and chunk
// scans, which is enough to get past the empty-graph guard and onto the arm's
// own render — the point here is to REACH the arm, not to reproduce a faithful
// log corpus.
func qpLogNodes() []*knowledgev1.Node {
	return []*knowledgev1.Node{
		{Id: "parity-log-template", Type: string(kgtypes.NodeLogTemplate), SymbolName: "parity <*> template"},
	}
}

// qpNode seeds one node body the fake re-decodes on a ByID read.
func qpNode(t *testing.T, id, typ string) kgtools.ToolResult {
	t.Helper()
	return nodeResultJSON(t, id, typ, map[string]string{})
}

// queryParityFixtures returns the drive fixture for every registered arm,
// assembled from the two sibling halves. One map, so the harness and the
// coverage guard read the same object.
func queryParityFixtures() map[armID]queryParityFixture {
	fx := map[armID]queryParityFixture{}
	maps.Copy(fx, queryParityGraphFixtures())
	maps.Copy(fx, queryParityModeFixtures())
	return fx
}

// queryParityGraphFixtures is the per-graph-family half.
func queryParityGraphFixtures() map[armID]queryParityFixture {
	return map[armID]queryParityFixture{
		// list-graphs fires only when account, id and text are all absent, so the
		// arm-preserving probe for each of those three is the EMPTY value.
		armCloudCICDListGraphs: {
			entry: InterceptQueryCloudCICD,
			base:  map[string]any{"graph": "cloud"},
			discriminants: map[string]any{
				"graph": "cloud", "mode": "", "account": "", "id": "", "text": "",
			},
		},

		armCloudCICDGetNode: {
			entry: InterceptQueryCloudCICD,
			base:  map[string]any{"graph": "cloud", "account": qpParityAccount, "id": qpSeedResource},
			discriminants: map[string]any{
				"graph": "cloud", "mode": "", "id": qpSeedResource,
			},
		},

		armCloudCICDStats: {
			entry: InterceptQueryCloudCICD,
			base: map[string]any{
				"graph": "cloud", "account": qpParityAccount, "mode": "stats",
			},
			discriminants: map[string]any{"graph": "cloud", "mode": "stats", "id": ""},
		},

		armCloudCICDSearch: {
			entry: InterceptQueryCloudCICD,
			base: map[string]any{
				"graph": "cloud", "account": qpParityAccount, "text": "probe-text",
			},
			discriminants: map[string]any{"graph": "cloud", "mode": "", "id": ""},
			// resourceQueryText picks text FIRST, so a queries[] probe is shadowed by
			// the base's text and cannot appear downstream.
			opaque: map[string]bool{"queries": true},
		},

		armCloudCICDBrowse: {
			entry: InterceptQueryCloudCICD,
			base:  map[string]any{"graph": "cloud", "account": qpParityAccount},
			discriminants: map[string]any{
				"graph": "cloud", "mode": "", "id": "", "text": "", "queries": []any{},
			},
		},

		// routePracticeClient checks Language FIRST, so an empty language reaches
		// list-graphs before mode is ever read.
		armPracticeListGraphs: {
			entry:         InterceptQueryPracticeLinkage,
			base:          map[string]any{"graph": "practice"},
			discriminants: map[string]any{"graph": "practice", "language": ""},
		},

		armPracticeStats: {
			entry:         InterceptQueryPracticeLinkage,
			base:          map[string]any{"graph": "practice", "language": "go", "mode": "stats"},
			discriminants: map[string]any{"graph": "practice", "mode": "stats"},
		},

		armPracticeSearchFanOut: {
			entry: InterceptQueryPracticeLinkage,
			base: map[string]any{
				"graph": "practice", "language": "all", "text": "probe-text",
			},
			discriminants: map[string]any{"graph": "practice", "mode": "", "language": "all"},
			opaque:        map[string]bool{"queries": true, "text": true},
		},

		armPracticeSearch: {
			entry: InterceptQueryPracticeLinkage,
			base: map[string]any{
				"graph": "practice", "language": "go", "text": "probe-text",
			},
			discriminants: map[string]any{"graph": "practice", "mode": ""},
			// resourceQueryText-style precedence: the base's text shadows a queries[]
			// probe, so that probe cannot reach the search downstream.
			opaque: map[string]bool{"queries": true},
		},

		// routeLinkageClient's list-graphs gate reads id, text, mode and queries in
		// that order; all four are emptiness-gated here.
		armLinkageListGraphs: {
			entry: InterceptQueryPracticeLinkage,
			base:  map[string]any{"graph": "linkage"},
			discriminants: map[string]any{
				"graph": "linkage", "id": "", "text": "", "mode": "", "queries": []any{},
			},
		},

		armLinkageStats: {
			entry:         InterceptQueryPracticeLinkage,
			base:          map[string]any{"graph": "linkage", "mode": "stats"},
			discriminants: map[string]any{"graph": "linkage", "mode": "stats"},
		},

		armLinkageGetNode: {
			entry:         InterceptQueryPracticeLinkage,
			base:          map[string]any{"graph": "linkage", "id": qpSeedLinkage},
			discriminants: map[string]any{"graph": "linkage", "mode": "", "id": qpSeedLinkage},
		},

		// The retired arms answer from a fixed string and touch nothing —
		// precondition class (b).
		armLinkageSearchRetired: {
			entry:         InterceptQueryPracticeLinkage,
			base:          map[string]any{"graph": "linkage", "text": "probe-text"},
			discriminants: map[string]any{"graph": "linkage", "mode": "", "id": ""},
			opaque:        map[string]bool{"text": true, "queries": true},
			behavior:      qBehavesWithoutRead,
			precondition:  "class (b): a retired ranked-search arm serving a fixed message",
		},

		armWebPDFSearchRetired: {
			entry:         InterceptQueryPracticeLinkage,
			base:          map[string]any{"graph": "web", "text": "probe-text"},
			discriminants: map[string]any{"graph": "web", "mode": "", "id": ""},
			opaque:        map[string]bool{"text": true, "queries": true},
			behavior:      qBehavesWithoutRead,
			precondition:  "class (b): a retired ranked-search arm serving a fixed message",
		},

		// Bare mode:recent (EMPTY text) is the temporal browse; a non-empty text
		// probe would route to the sibling search arm, so text is emptiness-gated.
		// The six thought-filter params are probed with their ZERO values: a
		// non-zero one trips hasThoughtQueryFilter and the intercept declines to the
		// recall surface BEFORE the gate runs, so the row would measure a decline
		// rather than this arm's declared rejection.
		armKnowledgeRecentBrowse: {
			entry:         InterceptQueryKnowledgeSearch,
			base:          map[string]any{"graph": "knowledge", "mode": "recent"},
			discriminants: map[string]any{"graph": "knowledge", "mode": "recent", "text": ""},
			deselecting:   queryParityThoughtFilterDeselects(),
			// The browse pages at a fixed size and applies the caller's limit AFTER
			// the temporal sort, so the limit never reaches a plan; `fields` projects
			// only on the json render path this probe does not take.
			opaque: map[string]bool{"limit": true, "fields": true},
		},

		armKnowledgeSearch: {
			entry: InterceptQueryKnowledgeSearch,
			base: map[string]any{
				"graph": "knowledge", "mode": "text", "text": "probe-text",
			},
			discriminants: map[string]any{"graph": "knowledge", "mode": "text"},
			deselecting:   queryParityThoughtFilterDeselects(),
			// The type/types/meta filters are applied over the ranked rows AFTER the
			// hydrate, the text and the limit ride a client-side segment search rather
			// than a plan field, and `fields` projects only on the json render path.
			opaque: map[string]bool{
				"type": true, "types": true, "meta": true,
				"limit": true, "fields": true,
			},
		},

		armKnowledgeStats: {
			entry:         InterceptQueryStats,
			base:          map[string]any{"graph": "knowledge", "mode": "stats"},
			discriminants: map[string]any{"graph": "knowledge", "mode": "stats"},
		},

		armRegisteredGraphSearch: {
			entry: InterceptQueryRegisteredGraphSearch,
			base: map[string]any{
				"graph": "parity-custom", "name": "parity-custom", "mode": "text", "text": "probe-text",
			},
			discriminants: map[string]any{"graph": "parity-custom", "mode": "text"},
			deselecting:   queryParityThoughtFilterDeselects(),
			opaque: map[string]bool{
				"type": true, "types": true, "meta": true,
				"limit": true, "fields": true,
			},
		},

		// PRECONDITION CLASS (e). handleLogsQuery serves from a pre-fetched log
		// state, and getOrFetchLogState builds it by reading templates, streams and
		// chunks over the wire; with none of them the arm returns
		// "no engine and no persisted graph" and every non-rejected row would
		// measure that error instead. The seed supplies a log-template node under
		// the (logs, <name>) key, which is why the fixture's `name` probe needs its
		// own seeded graph — `name` IS the log graph selector here.
		armLogsQuery: {
			entry:         InterceptLogsQuery,
			base:          map[string]any{"graph": "logs", "name": qpParityLogGraph},
			discriminants: map[string]any{"graph": "logs", "mode": "", "id": "", "text": ""},
			// The pivot axes and the extra map are read BY KEY off the log state, and
			// samples is a boolean flag on the stats body: none of them lands in a
			// graph read or is echoed by the overview render.
			opaque:       map[string]bool{"rows": true, "cols": true, "extra": true},
			precondition: "class (e): the logs arm needs a seeded log graph before it can serve",
		},

		// POST-FIX. `graph` is no longer a discriminant here: the arm REJECTS it, so
		// the row must probe it with a real value to observe the rejection — a
		// discriminant entry would probe the empty string, which accounting does not
		// count as supplied, and the row would assert nothing.
		//
		// THE PROBE VALUE MUST ALSO STAY OUTSIDE knowledgeGraphRedundantAliases. This
		// arm's rejection is keyed on the VALUE — a graph naming the family the arm
		// already pins is served as redundant — so a probeValues override of
		// "knowledge" here would make the row assert a rejection the arm correctly no
		// longer emits. The default probe ("probe-graph") is outside the set, and the
		// acceptance half is pinned by name in
		// intercept_query_rules_graph_alias_test.go rather than by this harness.
		armRules: {
			entry:         InterceptQueryRules,
			base:          map[string]any{"type": "rule"},
			discriminants: map[string]any{"type": "rule"},
			// `fields` projects only on the json render path this arm does not take.
			// `limit` and `offset` are consumed CLIENT-SIDE: they slice the page out
			// of the scope-filtered set, which exists only after the fetch, so the
			// probe value reaches no captured read — and the render prints the page
			// it selected, never the number the caller asked for. BLIND SPOT (2):
			// their rows pin selection, and the honest-total behavior tests
			// (TestInterceptQueryRules_RenderReportsHonestTotal,
			// TestInterceptQueryRules_BareBrowseStaysBounded) are what pin the routing.
			opaque: map[string]bool{"fields": true, "limit": true, "offset": true},
		},
	}
}

// queryParityThoughtFilterDeselects is the shared `deselecting` set for the three
// arms that bail to the recall surface on a non-empty thought filter
// (hasThoughtQueryFilter, intercept_query_knowledge_search.go). Each of the six
// re-routes the call BEFORE the arm's gate runs, so the cell is unreachable —
// harness header class (h). Probing with a ZERO value instead does not rescue the
// row: accounting counts a key as supplied only when its value is non-empty
// (isEmptyJSONValue), so a zero-valued probe produces neither the re-route nor
// the declared rejection.
func queryParityThoughtFilterDeselects() map[string]bool {
	return map[string]bool{
		"valence_min": true, "valence_max": true,
		"magnitude_min": true, "consistency_max": true,
		"session": true, "connected_to": true,
	}
}
