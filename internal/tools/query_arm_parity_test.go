// SPDX-License-Identifier: Apache-2.0

package tools

// query_arm_parity_test.go is the DRIVE-THROUGH parity harness for the query
// surface: it runs every dispatch arm through the fake once per schema param and
// asserts the OBSERVED behavior matches the class queryArmRegistry DECLARES. The
// sibling partition test (query_param_accounting_test.go) proves the table is
// structurally complete; the bijection test proves every arm is wired; this
// proves the classification is TRUE of the code. The grid is
// queryArmCount x the live schema — 47 x 61 = 2867 cells.
//
// Not parallel by construction: fakeGraphCaller accumulates call state on
// unsynchronised slices, so t.Parallel() would race it — the same constraint
// mutate_arm_parity_test.go carries.
//
// WHAT A ROW ASSERTS, per declared class:
//
//	classConsumed            → the arm still BEHAVES (per its behavior class below)
//	                           and the probe is observable in a captured read or in
//	                           the rendered text, except for the params no read can
//	                           show (discriminants, the five selector-routing params,
//	                           format, and the per-arm `opaque` list);
//	classRejected            → handled, IsError, the error names the param, and ZERO
//	                           reads were issued;
//	classDeliberatelyIgnored → the arm behaves AND the probe appears nowhere.
//
// FOUR BLIND SPOTS. Read them before trusting a green run. The first two are
// inherited from the mutate harness; the last two are specific to query.
//
// The consumed side, by contrast, is REAL evidence, and that was verified rather
// than assumed: moving `since` (which no query arm reads at all) from
// armExamine's rejected set into its consumed set makes this test red on exactly
// the armExamine/since cell while the partition test stays green.
//
// BLIND SPOT (1): classRejected rows are TAUTOLOGICAL with respect to the gate.
// accountQueryParams reads the SAME table the row asserts against, so moving a
// param into an arm's rejected set makes the gate reject it, which is exactly what
// the row then checks — a MIS-declared rejection passes here. What those rows do
// pin is the CONTRACT SHAPE: the error names the field and no read precedes it.
// Over-broad rejection is caught elsewhere, by the per-arm behavior suites that
// drive REAL payloads (the query and logs-query suites Phase 4's false-rejection
// criterion runs). The logs half of that pair is load-bearing rather than
// incidental: the log arms are the ones where `name` is genuinely CONSUMED, so a
// uniform rejection of the selector params would break exactly them.
//
// BLIND SPOT (2): selection-only rows cannot tell classConsumed from
// classDeliberatelyIgnored. A row whose param is a discriminant, one of the five
// selector-routing params, `format`, or a member of the arm's `opaque` list is
// asserted only as "the arm was still selected and behaved" — equally true of a
// consumed cell and an ignored one. Those rows pin SELECTION, not routing. Any
// cell that must distinguish the two needs a named behavior test, not this harness.
//
// BLIND SPOT (3): a query arm produces a READ, not a MutationPlan, so mutate's
// "the probe is observable in the write" has no analog. This harness observes a
// consumed param in the captured ExecuteRequest (its QueryPlan, Selection and
// Target), in the captured StatsRequest, or in the RENDERED result text — which is
// where the arms that render client-side put their inputs.
//
// BLIND SPOT (4), the selector-routing one, which would otherwise have shipped a
// false-consumed cell. Observing a probe in the captured ExecuteRequest proves the
// CLIENT routed the param; it says NOTHING about whether the SERVER'S resolver for
// that graph reads the field. name, repo, account, language and branch are copied
// onto the Target unconditionally, for every graph, by domainTarget
// (intercept_query_correlations_pivot.go) and by the engine's buildTarget
// (engine/compile.go) — while ResolveGraphDB's knowledge arm
// (cmd/knowledge-server/internal/tools/tools_graph_routing.go) returns
// store.StoreForContext(ctx) and never reads sel.Name. A naive consumed row for
// (name, any knowledge arm) would therefore be GREEN on a param that reaches
// nothing. The mitigation lives in the REGISTRY, not here: the resolver table
// decides the class for all five, and this harness must NOT infer classConsumed
// from Target observation for any of them. `branch` is the easiest to miss —
// exactly one resolver reads it (resolveCode, at the repo@branch overlay Scope)
// and no query arg struct in this package carries a Branch field at all, so its
// cells are pure table work with nothing observable to check against.
//
// PRECONDITION CLASSES — the arms whose drive is not the default shape, recorded
// here rather than skipped. The plan's single rule ("drive the base once, expect
// non-error plus >=1 captured read") does not describe six of these arms' honest
// behavior, and driving them under it would assert something false:
//
//	(a) CACHE-SERVED REFLECT ARMS (personality, tensions, blind_spots, summary,
//	    clusters) answer from the propagation loop's cache. Against a nil provider
//	    they return a NON-ERROR cold message and issue ZERO reads, so "behaves" for
//	    them is non-error-WITH-zero-reads (qBehavesWithoutRead).
//	(b) RETIRED SEARCH ARMS (armLinkageSearchRetired, armWebPDFSearchRetired) return
//	    a fixed retirement message and read nothing — same class as (a).
//	(c) SEARCH ARMS (knowledge, practice, practice fan-out, cloud/cicd, registered-
//	    custom, code) return the not-ready / degraded error unless deps supply
//	    PipelineReady AND a SegmentSearcher. Both are supplied here — see the
//	    searcher field on interceptTestDeps — so these run the default shape.
//	(d) armTopology needs the foundation analyzer registry, and its dead_code
//	    analyzer additionally needs a filesystem. The fixture drives a REGISTERED
//	    non-dead_code analyzer over the fake, keeping it on the default shape.
//	(e) armLogsQuery needs log engine state. Its gate runs BEFORE handleLogsQuery,
//	    so rejected rows are exact; the consumed/ignored rows are driven against a
//	    persisted log graph the fixture seeds.
//	(f) armEngineDispatch is UNREACHABLE through InterceptQuery: the
//	    intercept only claims a call when
//	    maybeEmbedQuery succeeds, and maybeEmbedQuery keys on the payload field
//	    "query", which QueryToolDef does not declare — so the unknown-key sweep
//	    rejects the only payload that reaches it. Its rows drive the GATE directly
//	    and observe the consumed class against engine.Compile("query", raw), the
//	    same substitution mutate makes for its DECLINING arms. This is the one arm
//	    whose rows are not an intercept drive, and it is labeled as such.
//	(g) armExamineProjects claims the call BELOW a read: it fetches the node to
//	    learn whether the id is a project-domain type and DECLINES to a later
//	    claimant when it is not, so its gate necessarily runs after one read.
//	    "Rejected probes issue zero reads" is therefore stated for it as an
//	    EQUALITY against that one ownership-deciding read (preGateReads), which is
//	    strictly stronger than an upper bound: a claim point that later drifts
//	    below a SERVING read fails the row instead of widening the exemption.
//	(h) DESELECTING cells — not an arm class but a per-cell one. Supplying the
//	    param routes the call to a different claimant before this arm's gate runs
//	    (the three knowledge/custom search arms bail to the recall surface on any
//	    thought-graph filter), so the cell is UNREACHABLE. The registry's own
//	    emptiness-gate rule sanctions classRejected for exactly that case, and the
//	    row asserts the re-route rather than a rejection the arm can never emit.
//	    Each such cell is named on its fixture's `deselecting` list, so a param
//	    that does NOT deselect fails the row instead of being quietly exempted.
//
// Each non-default arm carries a `precondition` label on its fixture, and
// TestQueryArmParity_CoversEveryArmAndParam asserts the label is present wherever
// the behavior class is not the default — so an arm cannot quietly opt out of the
// drive rule without saying why.

import (
	"encoding/json"
	"maps"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// queryParityCellID is one (arm, param) pair the harness drives.
type queryParityCellID struct{ arm, param string }

// queryParityCells enumerates the full cell grid from the TWO LIVE SOURCES — the
// arm registry and the live query schema — in a deterministic order. Both the
// harness and the coverage test iterate this same function, so the count one
// asserts is the count the other actually drove.
func queryParityCells() []queryParityCellID {
	arms := make([]string, 0, len(queryArmRegistry))
	for arm := range queryArmRegistry {
		arms = append(arms, string(arm))
	}
	sort.Strings(arms)
	schema := queryProperties()
	params := make([]string, 0, len(schema))
	for p := range schema {
		params = append(params, p)
	}
	sort.Strings(params)

	cells := make([]queryParityCellID, 0, len(arms)*len(params))
	for _, arm := range arms {
		for _, param := range params {
			cells = append(cells, queryParityCellID{arm: arm, param: param})
		}
	}
	return cells
}

// The disposition labels every cell is tallied under. Recorded by the cell
// driver itself rather than re-derived, so the tally cannot drift from the
// branch it counts.
const (
	qDispRejected      = "rejected (claimed, error names the param, no serving read)"
	qDispDeselecting   = "deselecting (unreachable: the probe re-routes the call)"
	qDispSelectionOnly = "selection-only (behaves; no read or render can show the value)"
	qDispConsumed      = "consumed (probe observed in a read or the render)"
	qDispIgnored       = "deliberately ignored (probe absent from every read and the render)"
	qDispGateOnly      = "gate-only (driven at the gate; consumed observed against the compiled plan)"
)

// queryParityDispositions tallies how each cell was asserted. Package-level and
// unsynchronised, which is safe for the same reason the fake is: this harness is
// not parallel by construction.
var queryParityDispositions = map[string]int{}

// TestQueryArmParity_DeclaredClassMatchesObservedBehavior drives every
// (arm, param) cell and asserts the declared class is the observed one.
func TestQueryArmParity_DeclaredClassMatchesObservedBehavior(t *testing.T) {
	schema := queryProperties()
	require.NotEmpty(t, schema, "QueryToolDef must declare params")
	fixtures := queryParityFixtures()

	queryParityDispositions = map[string]int{}
	evaluated := 0
	for _, cell := range queryParityCells() {
		fx, ok := fixtures[armID(cell.arm)]
		require.Truef(t, ok, "arm %q has no parity fixture — every arm must be driven", cell.arm)
		evaluated++
		t.Run(cell.arm+"/"+cell.param, func(t *testing.T) {
			queryParityCell(t, armID(cell.arm), fx, cell.param, schema[cell.param])
		})
	}
	assert.Equal(t, len(queryArmRegistry)*len(schema), evaluated,
		"every (arm, param) cell must be driven — a skipped cell is an unasserted claim")

	// KNOWN POSITIVE for the whole run. "2867 cells green" is also satisfied by a
	// grid where every cell fell into a weak disposition — every row selection-only
	// would prove nothing about routing at all. Asserting each disposition is
	// POPULATED is the control: it pins that the two REAL-evidence dispositions
	// (consumed observed in a read, ignored absent from one) actually ran, and the
	// tally makes the grid's shape reportable instead of inferred.
	tallied := 0
	for _, label := range []string{
		qDispRejected, qDispDeselecting, qDispSelectionOnly,
		qDispConsumed, qDispIgnored, qDispGateOnly,
	} {
		n := queryParityDispositions[label]
		t.Logf("%5d cells — %s", n, label)
		tallied += n
		assert.Positivef(t, n,
			"no cell was asserted as %q — a disposition with zero members means the harness "+
				"never exercised that branch, and a green run says less than it appears to", label)
	}
	assert.Equal(t, evaluated, tallied, "every driven cell must be tallied exactly once")
}

// TestQueryArmParity_CoversEveryArmAndParam is the NO-SKIP guard for its sibling:
// the harness could pass vacuously by driving a subset, so the cell grid is
// counted against both live sources independently. It also pins that every arm has
// a drive fixture, that every cell is classified, and that any arm departing from
// the default drive rule carries the precondition label saying why.
func TestQueryArmParity_CoversEveryArmAndParam(t *testing.T) {
	schema := queryProperties()
	require.NotEmpty(t, schema, "QueryToolDef must declare params")
	require.NotEmpty(t, queryArmRegistry, "the arm registry must declare arms")

	cells := queryParityCells()
	assert.Len(t, cells, len(queryArmRegistry)*len(schema),
		"the cell grid must be exactly every arm times every schema param")
	assert.Len(t, cells, queryArmCount*queryDeclaredParamCount,
		"the grid must also match the two plan-locked literals, so a registry edit and a schema "+
			"edit cannot move the target together")

	fixtures := queryParityFixtures()
	seen := map[queryParityCellID]bool{}
	armsSeen := map[string]bool{}
	paramsSeen := map[string]bool{}
	for _, cell := range cells {
		assert.Falsef(t, seen[cell], "cell %s/%s enumerated twice", cell.arm, cell.param)
		seen[cell] = true
		armsSeen[cell.arm] = true
		paramsSeen[cell.param] = true

		_, hasFixture := fixtures[armID(cell.arm)]
		assert.Truef(t, hasFixture, "arm %q has no drive fixture, so its cells cannot be evaluated", cell.arm)
		_, classified := queryParamClass(armID(cell.arm), cell.param)
		assert.Truef(t, classified, "cell %s/%s is unclassified", cell.arm, cell.param)
	}
	assert.Len(t, armsSeen, len(queryArmRegistry), "every registered arm must appear in the grid")
	assert.Len(t, paramsSeen, len(schema), "every schema param must appear in the grid")

	for _, arm := range sortedArmIDs() {
		fx := fixtures[arm]
		require.NotNilf(t, fx.entry, "arm %s has no entry point to drive", arm)
		if fx.behavior != qBehavesWithRead {
			assert.NotEmptyf(t, fx.precondition,
				"arm %s departs from the default drive rule but names no precondition class — the "+
					"harness header enumerates them, and an unlabelled departure is a silent skip", arm)
		}
	}
}

// queryParityCell drives one (arm, param) cell and asserts its declared class.
func queryParityCell(t *testing.T, arm armID, fx queryParityFixture, param string, prop kgtools.Property) {
	t.Helper()
	class, classified := queryParamClass(arm, param)
	require.Truef(t, classified, "param %q is unclassified for arm %q", param, arm)

	value, distinctive := queryParityProbe(param, prop, fx)
	payload := map[string]any{}
	// A per-param base override drives a row in the shape that actually exercises
	// the param, for the arms whose consumed set cannot be reached from one payload.
	base := fx.base
	if override, ok := fx.paramBase[param]; ok {
		base = override
	}
	maps.Copy(payload, base)
	payload[param] = value
	raw, err := json.Marshal(payload)
	require.NoError(t, err)

	if fx.behavior == qGateOnly {
		if fx.opaque[param] {
			distinctive = ""
		}
		queryParityGateOnlyCell(t, arm, class, param, distinctive, raw)
		return
	}

	fc := queryParitySeed(t)
	handled, res := fx.entry(opCtx(), queryParityDeps(fc), kgtools.CallToolParams{
		Name: "query", Arguments: raw,
	})

	// A DESELECTING param re-routes the call to a DIFFERENT claimant before this
	// arm's gate ever runs, so the cell is UNREACHABLE rather than mis-declared —
	// the case the registry header's emptiness-gate rule licenses ("if supplying it
	// routes the call to a DIFFERENT arm, the cell is unreachable and REJECTED
	// costs nothing"). The zero-value dodge does not help here: the accounting gate
	// counts a key as SUPPLIED only when its value is non-empty (isEmptyJSONValue),
	// so a zero-valued probe reaches neither the re-route nor the rejection. The
	// row therefore asserts the drive did NOT land here and read nothing — which is
	// what makes the fixture's claim falsifiable, since listing a param that does
	// NOT deselect fails right here.
	if fx.deselecting[param] {
		queryParityDispositions[qDispDeselecting]++
		assert.Falsef(t, handled,
			"arm %q lists %q as deselecting, but the entry point still claimed the call", arm, param)
		assert.Zerof(t, queryParityReads(fc),
			"a deselected call must issue no read for arm %q (param %q)", arm, param)
		return
	}

	if class == classRejected {
		queryParityDispositions[qDispRejected]++
		require.Truef(t, handled, "a rejected param must be CLAIMED, not fall through (%s/%s)", arm, param)
		require.Truef(t, res.IsError, "param %q must be rejected by arm %q", param, arm)
		assert.Containsf(t, toolResultText(res), param,
			"arm %q rejected %q without naming the field", arm, param)
		// An EQUALITY, not an upper bound: the reject must precede every read the
		// arm would issue to SERVE the call, and the only reads allowed before it
		// are the ones the arm needs to decide it OWNS the call. preGateReads is
		// zero for every arm but one, so a claim point that drifts below a read
		// fails here instead of quietly widening what "zero reads" means.
		assert.Equalf(t, fx.preGateReads, queryParityReads(fc),
			"arm %q read before rejecting %q — the reject must precede every serving read", arm, param)
		return
	}

	queryParityAssertBehaved(t, arm, fx, param, handled, res, fc)
	if distinctive == "" || querySelectionOnlyParams[param] || fx.opaque[param] {
		queryParityDispositions[qDispSelectionOnly]++
		return
	}
	observed := queryParityObserved(t, fc, res)
	switch class {
	case classConsumed:
		queryParityDispositions[qDispConsumed]++
		assert.Truef(t, queryParityContains(observed, distinctive),
			"arm %q declares %q CONSUMED but the probe %q is in neither the reads nor the render: %s",
			arm, param, distinctive, observed)
	case classDeliberatelyIgnored:
		queryParityDispositions[qDispIgnored]++
		assert.Falsef(t, queryParityContains(observed, distinctive),
			"arm %q declares %q deliberately IGNORED but the probe %q reached the read or the render",
			arm, param, distinctive)
	case classRejected:
		// Handled above; listed so the switch stays exhaustive over the class set.
	}
}

// queryParityGateOnlyCell asserts one cell of an arm whose intercept cannot be
// reached (precondition class (f)). The gate is driven directly, and the consumed
// class is observed against the compiled plan its handler would build.
func queryParityGateOnlyCell(t *testing.T, arm armID, class paramClass, param, distinctive string, raw []byte) {
	t.Helper()
	queryParityDispositions[qDispGateOnly]++
	err := accountQueryParams(arm, raw)
	if class == classRejected {
		require.Errorf(t, err, "arm %q must reject %q at the gate", arm, param)
		assert.Containsf(t, err.Error(), param, "arm %q rejected %q without naming the field", arm, param)
		return
	}
	require.NoErrorf(t, err, "arm %q must let %q through the gate", arm, param)
	if distinctive == "" || querySelectionOnlyParams[param] {
		return
	}
	compiled := queryParityCompiled(t, raw)
	switch class {
	case classConsumed:
		assert.Truef(t, queryParityContains(compiled, distinctive),
			"arm %q declares %q CONSUMED but the probe %q is nowhere in the compiled plan: %s",
			arm, param, distinctive, compiled)
	case classDeliberatelyIgnored:
		assert.Falsef(t, queryParityContains(compiled, distinctive),
			"arm %q declares %q deliberately IGNORED but the probe %q reached the compiled plan",
			arm, param, distinctive)
	case classRejected:
		// Handled above; listed so the switch stays exhaustive over the class set.
	}
}

// queryParityAssertBehaved asserts the arm was still selected and behaved as its
// class allows: every arm must CLAIM the call and return a non-error result, and
// the read count must match the behavior class — a cache-served or retired arm
// reads nothing, every other arm issues at least one. A silent re-route to a
// different arm shows up here rather than as a passing row that measured the
// wrong arm.
func queryParityAssertBehaved(
	t *testing.T, arm armID, fx queryParityFixture, param string,
	handled bool, res kgtools.ToolResult, fc *fakeGraphCaller,
) {
	t.Helper()
	require.Truef(t, handled, "arm %q must claim the call (param %q)", arm, param)
	require.Falsef(t, res.IsError, "arm %q errored on consumed/ignored %q: %s", arm, param, toolResultText(res))
	if fx.behavior == qBehavesWithoutRead {
		assert.Zerof(t, queryParityReads(fc),
			"arm %q is %s, so it must serve %q with zero reads", arm, fx.precondition, param)
		return
	}
	assert.Positivef(t, queryParityReads(fc),
		"arm %q issued no read for %q — an arm whose base fails its own precondition produces an "+
			"empty read list, which reads as a rejection and silently corrupts every row for that arm",
		arm, param)
}

// queryParityReads counts every read the drive issued across all three seams the
// query arms use: the Execute plan reads, the Stats RPC, and the MetadataStats
// RPC. Counting only Execute would score a stats-only arm as having read nothing.
func queryParityReads(fc *fakeGraphCaller) int {
	n := len(fc.execRequests) + len(fc.statsReqs)
	for _, c := range fc.calls {
		if c.tool == "metadata_stats" {
			n++
		}
	}
	return n
}
