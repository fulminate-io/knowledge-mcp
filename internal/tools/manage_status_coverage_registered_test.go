// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/collectorconfig"
)

// manage_status_coverage_registered_test.go — THE COVERAGE TABLE IS AN INVENTORY
// OF WHAT THIS MACHINE HOLDS OR HAS REGISTERED, not a list of what some arm owes
// work on.
//
// The live confirmation of the contrib-collector families found two ways a family
// an operator can see everywhere else was absent from this one surface:
//
//   - REGISTERED, NEVER COLLECTED. The rows came from the per-type INSTANCE
//     enumeration, and a family with no collect has no instance — so eight
//     registered families rendered zero rows while `collector list` rendered all
//     eight as "in effect: yes".
//   - COLLECTED, DECLARING NOTHING. A family whose behavior cascade declares
//     neither syncable nor embedding anywhere was filtered out of the walk, so a
//     graph that really exists and really holds nodes was missing from the
//     inventory the table exists to be.
//
// Both are the same defect in two directions: the table answered "what is there
// work to report on" when the question it is asked is "what is here".

// registeredEntry builds the CONFIG ENTRY half of a registration — the file is
// the registration record, so this is the shape an operator's own
// `collector add` leaves behind. The transport fields are irrelevant to the
// coverage walk, which never dials a provider; what the walk reads is the name
// and the behavior block.
func registeredEntry(name string, behavior *collectorconfig.Behavior) namedEntry {
	return namedEntry{name: name, entry: collectorconfig.Entry{
		Type:     collectorconfig.TransportHTTP,
		URL:      "http://127.0.0.1:1/never-dialed",
		Tool:     "collect_" + strings.ReplaceAll(name, "-", "_"),
		Behavior: behavior,
	}}
}

// TestCoverageWalk_ARegisteredFamilyRendersItsRowBeforeItsFirstCollect is the
// first half of the live finding, at the walk.
//
// THE ROW IS ABOUT COVERAGE, NOT ABOUT WORK OWED. A family an operator registered
// is part of what this machine holds from the moment the entry exists; the
// coverage table is the one surface that inventories that, and a family it cannot
// name is a family the operator cannot confirm the machine knows about.
//
// THE SAME-RUN KNOWN-POSITIVE is in the same assertion: code/myrepo, a builtin
// family WITH an instance, renders in the same call — so a walk that produced no
// rows at all could not satisfy this.
func TestCoverageWalk_ARegisteredFamilyRendersItsRowBeforeItsFirstCollect(t *testing.T) {
	fake := &coverageFake{baseNamesByType: map[string][]string{"jira": {}}}
	deps := &coverageDeps{gc: fake, crud: &stubGraphTypeCRUD{defs: []*knowledgev1.GraphTypeDef{
		customFamilyDef("jira", new(true), nil, nil),
	}}}

	labels := targetLabels(mustCoverageTargets(t, context.Background(), deps))

	assert.Contains(t, labels, "jira",
		"a registered family with no collected instance must render a row naming the family, with its instance half empty")
	for _, label := range labels {
		assert.False(t, strings.HasPrefix(label, "jira/"),
			"and it must NOT fabricate an instance name it has no graph for: %s", label)
	}
	assert.Contains(t, labels, "code/myrepo",
		"same-run known-positive: the builtin rows are still there, so the assertion above is about the custom row and not about the walk running at all")
}

// TestCoverageWalk_ACollectedFamilyDeclaringNoAxisStillGetsItsRow is the second
// half of the live finding: two families collected identically, one declaring
// embedding and one declaring nothing, and only the first appeared.
//
// THE FILTER WAS THE BUG. Omitting a family because every coverage column it
// could occupy is structurally zero deletes a real graph, holding real nodes,
// from the inventory manage(status) exists to show — which is the same argument
// the unmanaged row already makes for always rendering.
func TestCoverageWalk_ACollectedFamilyDeclaringNoAxisStillGetsItsRow(t *testing.T) {
	fake := &coverageFake{baseNamesByType: map[string][]string{
		"embeds-nothing": {"g"},
		"embeds":         {"g"},
	}}
	deps := &coverageDeps{gc: fake, crud: &stubGraphTypeCRUD{defs: []*knowledgev1.GraphTypeDef{
		// syncable=false, embeddable=false, and an override that declares embedding
		// OFF — the family the walk used to drop.
		customFamilyDef("embeds-nothing", new(false), new(false), map[string]bool{"row": false}),
		customFamilyDef("embeds", new(false), new(true), nil),
	}}}

	labels := targetLabels(mustCoverageTargets(t, context.Background(), deps))

	assert.Contains(t, labels, "embeds-nothing/g",
		"a COLLECTED graph is in the inventory whatever its behavior declares; dropping it silently deletes a graph the operator can see everywhere else")
	assert.Contains(t, labels, "embeds/g",
		"same-run control: the family declaring embedding was never in question")
}

// TestRenderLLMCoverage_NamesAFamilyRegisteredByENTRYWithNoCollect drives the
// live finding's own reproduction: the family is registered by a CONFIG FILE
// ENTRY — which is what `collector add` writes and what the finding's venue had —
// with no collect and NO server catalog at all, and the rendered table is read.
//
// THE ENTRY HALF IS THE ONE THE FINDING FAILED ON. A catalog-only test would pass
// against an implementation that learned families only at their first collect,
// which is precisely the behavior under repair: the server catalog holds a record
// only once something has been collected under it.
func TestRenderLLMCoverage_NamesAFamilyRegisteredByENTRYWithNoCollect(t *testing.T) {
	useTempCollectorScope(t,
		registeredEntry("t14-aws", &collectorconfig.Behavior{Embeddable: new(true)}),
		registeredEntry("t14-nosync", &collectorconfig.Behavior{Syncable: new(false)}),
	)
	fake := &coverageFake{statsByKey: map[string]*knowledgev1.GraphStats{}}

	out := renderLLMCoverage(context.Background(), &coverageDeps{gc: fake})

	require.NotEmpty(t, out)
	assert.Contains(t, out, "| t14-aws |",
		"a family registered by a config entry is on the coverage table before its first collect")
	assert.Contains(t, out, "| t14-nosync |",
		"including one whose behavior block declares no coverage axis at all")
	assert.Contains(t, out, "not collected",
		"and the row says WHY its cells are empty, rather than printing zeros a reader would take for measurements")

	// THE BEHAVIOR IS READ FROM THE ENTRY, which is the only record there is for a
	// family nothing has collected: the family declaring embedding renders a segment
	// cell, the one declaring nothing renders the no-pool dash.
	awsRow := coverageRowLine(t, out, "t14-aws")
	nosyncRow := coverageRowLine(t, out, "t14-nosync")

	// THE COUNTS CELL NAMES THE RIGHT ABSENCE. "not read (unmanaged)" is the
	// DECLINED row's sentence — a graph exists and a backend could not produce its
	// counts without materializing it — and printing it here would send an
	// operator diagnosing a backend when what they need is a collect.
	assert.Contains(t, awsRow, "not collected (registered, no graph yet)",
		"the counts cell says there is no graph yet, which is the actionable fact")
	assert.NotContains(t, awsRow, "not read (unmanaged)",
		"and not the declined row's sentence, which asserts a graph that does not exist: %s", awsRow)
	assert.Contains(t, awsRow, "not read [not collected]",
		"an entry declaring embeddable=true could carry a segment pool once collected, so the cell says nobody read one")
	assert.NotContains(t, nosyncRow, "not read [",
		"an entry declaring no embedding could carry no pool at all, so its segment cell is the bare dash: %s", nosyncRow)

	// THE BAND IS IN THE LEGEND. The bracketed term in every segment cell is a
	// vocabulary the table explains beneath its heading; a band that renders with
	// no legend entry is a word the operator has nowhere to look up. Keyed on the
	// CONSTANT so renaming the band without touching the legend turns this red.
	assert.Contains(t, out, "`"+DispositionNotCollected+"` is a REGISTERED FAMILY WITH NO GRAPH",
		"the legend must explain the band this row renders")

	// SAME-RUN KNOWN-POSITIVE for both: the builtin rows render in the same call.
	assert.Contains(t, out, "| code/myrepo |")
}

// TestLocalDaemonJSON_CarriesTheRegisteredFamilyRow is the SIBLING ARM. The text
// table and the JSON coverage[] block are two renders of one row set, and a
// consumer of the machine-readable arm has no other place to learn that a family
// is registered — so a fix that reached only the markdown would be a silent drop
// in the arm nothing reads by eye.
func TestLocalDaemonJSON_CarriesTheRegisteredFamilyRow(t *testing.T) {
	useTempCollectorScope(t, registeredEntry("t14-aws", &collectorconfig.Behavior{Embeddable: new(true)}))
	m := map[string]any{}

	addLocalDaemonJSON(context.Background(), &coverageDeps{gc: &coverageFake{}}, m)

	rows, ok := m["coverage"].([]CoverageRow)
	require.True(t, ok, "the coverage block must be the per-row list, not a summary: %T", m["coverage"])
	var graphs []string
	for _, r := range rows {
		graphs = append(graphs, r.Graph)
	}
	assert.Contains(t, graphs, "t14-aws",
		"the JSON arm carries the registered family too; a consumer reading coverage[] has nothing else to learn it from")
	assert.Contains(t, graphs, "code/myrepo", "same-run known-positive: the builtin rows are on the JSON arm as well")
}

// TestCollectCoverageRows_AsksNoStatsForAFamilyWithNoGraph observes the SKIP, not
// the row. A no-instance row would render identically whether or not the walk
// spent a Stats round trip discovering that the graph is absent — so without this
// assertion the skip is unobserved, and on a backend that creates on read it is
// worse than a wasted RPC.
func TestCollectCoverageRows_AsksNoStatsForAFamilyWithNoGraph(t *testing.T) {
	ctx := context.Background()
	fake := &coverageFake{baseNamesByType: map[string][]string{"jira": {}}}
	deps := &coverageDeps{gc: fake, crud: &stubGraphTypeCRUD{defs: []*knowledgev1.GraphTypeDef{
		customFamilyDef("jira", new(true), nil, nil),
	}}}

	rows, err := collectCoverageRows(ctx, deps)
	require.NoError(t, err)
	require.NotEmpty(t, rows)

	// The expectation is DERIVED FROM THE WALK rather than written as a literal, so
	// a future row added to the table does not red this test for the wrong reason.
	targets, err := coverageTargets(ctx, deps)
	require.NoError(t, err)
	addressable, noInstance := 0, 0
	for _, tg := range targets {
		if tg.noInstance {
			noInstance++
			continue
		}
		addressable++
	}
	require.Positive(t, noInstance, "the fixture must produce a no-instance row, or this test asserts nothing")
	require.Positive(t, addressable, "known-positive: the other rows DID issue their Stats RPC")
	assert.Len(t, fake.reqs, addressable,
		"exactly one Stats RPC per ADDRESSABLE row: a family with no graph is not asked about")
	for _, req := range fake.reqs {
		assert.NotNil(t, req.GetTarget(), "and no request was issued against a nil selector")
	}
}

// TestCollectSegProbes_DeclinesAFamilyWithNoGraph observes the OTHER skip, and it
// has to be driven directly: the assembly walk never populates a stats slot for a
// no-instance row, so through the whole call the probe's own guard is unreachable
// and deleting it would leave every test green.
//
// THE GUARD IS NOT REDUNDANT. The probe's real reader lazily CONSTRUCTS a
// per-graph manager and its cache directory for whatever key it is handed, so a
// status read reaching it with the name of a family that has no graph would
// create state for an instance that does not exist.
func TestCollectSegProbes_DeclinesAFamilyWithNoGraph(t *testing.T) {
	seg := &coverageSegReader{coveredByKey: map[string]int{"jira": 3, "code/myrepo": 4}}
	targets := []coverageTarget{
		{label: "jira", gt: "jira", noInstance: true, managed: true, declaredSegments: true},
		{label: "code/myrepo", gt: "code", name: "myrepo", managed: true},
	}
	// BOTH slots non-nil, so the nil-stats clause cannot be what declines the first
	// row — only the no-instance guard can.
	stats := []*knowledgev1.GraphStats{{}, {}}

	collectSegProbes(context.Background(), &coverageDeps{gc: &coverageFake{}, segCov: seg}, targets, stats)

	probed := seg.probedKeys()
	assert.NotContains(t, probed, "jira",
		"a family with no graph must never reach the probe: the reader would construct a manager and a cache directory for it")
	assert.Contains(t, probed, "code/myrepo",
		"same-run known-positive: the addressable row WAS probed, so the assertion above is about the guard and not about a wave that never ran")
}

// TestCustomCollectorDocs_DescribeTheRegisteredRowTheCodeRenders keeps the guide
// page and the renderer in step.
//
// THE PAGE CARRIED THE RETIRED RULE. It told a collector author their family is
// listed "under the same eligibility rule: it is listed when it declares
// syncable, or declares embeddable" — the filter this change removed — so a
// reader following it would have expected a family declaring neither to be
// absent, and would have read a correct table as a defect.
//
// THE CELL TEXT IS TAKEN OFF THE RENDERER FIRST, so this test cannot pin prose
// the code stopped producing: the require below reds if the renderer's wording
// moves, before the doc is looked at.
func TestCustomCollectorDocs_DescribeTheRegisteredRowTheCodeRenders(t *testing.T) {
	pages := guidePagesMentioningCustomCollectors(t)
	page, ok := pages["tools/custom_collector.md"]
	require.True(t, ok, "the custom collector guide must be among the pages the census reached")

	cell := "not collected (registered, no graph yet)"
	rendered := formatRegisteredCoverageRow(newRegisteredCoverageRow(
		coverageTarget{label: "acme", noInstance: true, declaredSegments: true}))
	require.Contains(t, rendered, cell,
		"the counts cell this test pins must be the one the renderer emits: %s", rendered)

	assert.Contains(t, page, cell,
		"the guide must show the cell an operator will actually read")
	assert.Contains(t, page, "`"+DispositionNotCollected+"`",
		"and name the band, keyed on the constant so a rename reds this")
	assert.NotContains(t, page, "it is listed when it declares",
		"and it must NOT still teach the eligibility rule this change removed, which would have a reader treating a correct table as a defect")
}

// coverageRowLine returns the rendered markdown row whose FIRST cell is exactly
// graph, failing the test when no such row rendered. It matches on the cell
// rather than on a substring so a row for "t14-aws" is never satisfied by a row
// for "t14-aws-extra".
func coverageRowLine(t *testing.T, table, graph string) string {
	t.Helper()
	for line := range strings.SplitSeq(table, "\n") {
		cells := strings.Split(strings.Trim(strings.TrimSpace(line), "|"), "|")
		if len(cells) > 1 && strings.TrimSpace(cells[0]) == graph {
			return line
		}
	}
	require.Failf(t, "no coverage row rendered", "no row whose graph cell is %q in:\n%s", graph, table)
	return ""
}
