// SPDX-License-Identifier: Apache-2.0

package tools

// query_arm_parity_seed_test.go holds the parity harness's SHARED DRIVE
// MACHINERY: the segment searcher the ranked arms need, the deps wrapper, the
// seeded fake with every fixture node and the collected-graph catalog, and the
// log-graph node set.
//
// SPLIT FROM query_arm_parity_fixtures_test.go FOR THE LINE BUDGET, which is the
// same reason that file was already split once (its own header says so) and the
// same reason intercept_deps_stub_test.go exists. The lefthook file-length gate
// globs *.go with only vendor/** and gen/** excluded, so a test file hits the
// identical 500-line commit block a source file does. This block is the largest
// self-contained unit in that file and is used by BOTH fixture halves rather
// than by either alone, so it is the right thing to lift out.

import (
	"context"
	"testing"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

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

// queryParityDeps wraps the fake with the three seams the search arms require:
// interceptTestDeps reports PipelineReady() true already, the searcher knob
// supplies the segment engine those arms would otherwise report unavailable, and
// the graph-type registry knows "parity-custom" so the custom-graph arm's
// selector gate resolves instead of refusing the row before the param under test
// is ever read. The COLLECTED half of that gate rides queryParitySeed's
// listGraphsResult.
func queryParityDeps(fc *fakeGraphCaller) interceptTestDeps {
	return interceptTestDeps{
		gc: fc, searcher: queryParitySearcher{},
		crud: registeredGraphTypes(qpParityCustomGraph),
	}
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
		// The collected-graph catalog the custom-graph arm's selector gate reads.
		// TWO instances, for the same reason the logs fixture seeds a "probe-name"
		// log graph above: `name` IS this arm's instance selector, so the `name`
		// row probes a DIFFERENT instance than the base, and both have to exist for
		// that row to measure routing rather than the not-found path.
		// THE WEB/PDF ROWS NEED THE SAME PAIR, for the same reason and one more:
		// the segment-backed raw-graph arm gained a collected-graph EXISTENCE gate,
		// so an uncollected instance is refused by name before the param under test
		// is read, and every one of its cells measures that refusal instead of the
		// declared class. Both families and both instances, because `name` is the
		// raw arm's instance selector too and its probe row drives a different one.
		listGraphsResult: listGraphsResultJSON(t,
			[2]string{qpParityCustomGraph, qpParityCustomGraph},
			[2]string{qpParityCustomGraph, "probe-name"},
			[2]string{"web", qpParityWebGraph},
			[2]string{"pdf", qpParityWebGraph},
			[2]string{"web", "probe-name"},
			[2]string{"pdf", "probe-name"}),
	}
}

// qpParityAccount is the cloud/cicd account key every resource fixture targets;
// the fake seeds its node set under the same key. qpParityLogGraph is the
// query_id the logs fixture targets.
const (
	qpParityAccount  = "parity-account"
	qpParityLogGraph = "parity-logs"
	// qpParityWebGraph is the raw-graph instance the web and pdf rows target. It
	// was a bare literal in the row fixtures; naming it is what keeps the catalog
	// seed above and those rows from drifting apart silently.
	qpParityWebGraph = "parity-web-graph"
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
