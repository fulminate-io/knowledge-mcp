// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// manage_status_coverage_custom_test.go — R6 and R7: a REGISTERED custom family
// is a coverage-table row wherever a builtin family is one, under the same
// filter, and the three surfaces that read the registration catalog each apply
// their OWN documented rule to the same three families.

// customFamilyDef builds a registration record carrying only the behavior the
// coverage filter reads. The collector spec is irrelevant here — the walk never
// dials a provider — so it is deliberately absent.
func customFamilyDef(name string, syncable, embeddable *bool, nodeTypeEmbeddable map[string]bool) *knowledgev1.GraphTypeDef {
	def := &knowledgev1.GraphTypeDef{
		Name:     name,
		Behavior: &knowledgev1.BehaviorDefaults{Syncable: syncable, Embeddable: embeddable},
	}
	if len(nodeTypeEmbeddable) > 0 {
		def.NodeTypes = map[string]*knowledgev1.NodeTypeOverride{}
		for nt, emb := range nodeTypeEmbeddable {
			def.NodeTypes[nt] = &knowledgev1.NodeTypeOverride{Embeddable: new(emb)}
		}
	}
	return def
}

// mustCoverageTargets walks and fails the test on the walk error, for the rows
// whose subject is WHICH families are walked rather than what a failure does.
// The failure path has its own test below.
func mustCoverageTargets(t *testing.T, ctx context.Context, deps ClientDeps) []coverageTarget {
	t.Helper()
	targets, err := coverageTargets(ctx, deps)
	require.NoError(t, err)
	return targets
}

// targetLabels projects the walked rows down to their labels, which is the only
// thing a caller of coverageTargets can see.
func targetLabels(targets []coverageTarget) []string {
	out := make([]string, 0, len(targets))
	for _, t := range targets {
		out = append(out, t.label)
	}
	return out
}

// TestCoverageWalk_RegisteredFamilyWithAnInstanceRendersItsRow is R6's first
// clause: a registered eligible family renders a row with the same columns and
// the same label shape a builtin instance row carries.
//
// THE SAME-RUN KNOWN-POSITIVE is in the same assertion: code/myrepo, a BUILTIN
// family with an instance, renders in the same call. A walk that produced no rows
// at all would satisfy an omission assertion but not this one.
func TestCoverageWalk_RegisteredFamilyWithAnInstanceRendersItsRow(t *testing.T) {
	fake := &coverageFake{baseNamesByType: map[string][]string{
		"jira": {"board-a"},
	}}
	deps := &coverageDeps{gc: fake, crud: &stubGraphTypeCRUD{defs: []*knowledgev1.GraphTypeDef{
		customFamilyDef("jira", new(true), nil, nil),
	}}}

	labels := targetLabels(mustCoverageTargets(t, context.Background(), deps))

	assert.Contains(t, labels, "jira/board-a",
		"a registered syncable family with a collected instance must render its row, labeled family/instance exactly as a builtin is")
	assert.Contains(t, labels, "code/myrepo",
		"same-run known-positive: the builtin rows are still there, so the assertion above is about the custom row and not about the walk running at all")
}

// TestCoverageWalk_RegisteredFamilyWithNoInstanceIsStillEnumerated is R6's
// zero-instance clause. THE LOAD-BEARING ASSERTION IS THE ENUMERATION, not the
// row's shape: a walk that never asked about jira at all would satisfy a bare
// label assertion by accident. What must be true is that the walk ASKS, and then
// that the family is on the table whatever the answer was.
//
// THE ROW IT PRODUCES IS THE BARE FAMILY NAME, which is the fix for the live
// finding: the family is on this machine's inventory from the moment its
// registration exists, and the instance half of the label is empty because there
// is no graph to name. See manage_status_coverage_registered_test.go for the row
// itself and for what its cells say.
func TestCoverageWalk_RegisteredFamilyWithNoInstanceIsStillEnumerated(t *testing.T) {
	fake := &coverageFake{baseNamesByType: map[string][]string{
		"jira": {},
	}}
	deps := &coverageDeps{gc: fake, crud: &stubGraphTypeCRUD{defs: []*knowledgev1.GraphTypeDef{
		customFamilyDef("jira", new(true), nil, nil),
	}}}

	labels := targetLabels(mustCoverageTargets(t, context.Background(), deps))

	asked := false
	for _, req := range fake.execReqs {
		if req.GetTarget().GetGraph() == "jira" {
			asked = true
		}
	}
	assert.True(t, asked,
		"a registered family with no collected instance must still be ENUMERATED; asserting only the label passes against a walk that never asked")
	assert.Contains(t, labels, "jira",
		"and it renders a row naming the family, because the registration is what puts it on the inventory")
	for _, label := range labels {
		assert.False(t, strings.HasPrefix(label, "jira/"),
			"with no instance half: there is no graph to name, and inventing one would put an unaddressable identity on the table: %s", label)
	}
}

// TestCoverageWalk_BuiltinRowOrderIsUnchangedByARegisteredFamily is R6's "beside
// the builtin ones" clause, and it is the assertion no existing coverage test
// can make: every one of them runs with GraphTypeCRUD() nil, so none observes the
// combined list. A merge-or-sort implementation would pass all of them while
// moving every builtin row and changing codeTypeIndex's answer.
func TestCoverageWalk_BuiltinRowOrderIsUnchangedByARegisteredFamily(t *testing.T) {
	ctx := context.Background()
	seed := func() *coverageFake {
		return &coverageFake{baseNamesByType: map[string][]string{"jira": {"board-a"}}}
	}

	withoutCatalog := targetLabels(mustCoverageTargets(t, ctx, &coverageDeps{gc: seed()}))
	withCatalog := targetLabels(mustCoverageTargets(t, ctx, &coverageDeps{gc: seed(), crud: &stubGraphTypeCRUD{defs: []*knowledgev1.GraphTypeDef{
		customFamilyDef("jira", new(true), nil, nil),
	}}}))

	require.Greater(t, len(withCatalog), len(withoutCatalog), "the registered family must add rows, or the prefix comparison below is vacuous")
	assert.Equal(t, withoutCatalog, withCatalog[:len(withoutCatalog)],
		"the builtin rows are a byte-identical PREFIX: registered families are APPENDED, never merged or sorted into the builtin order")
	assert.Equal(t, "jira/board-a", withCatalog[len(withoutCatalog)], "and the registered family's rows follow them")
}

// TestCoverageWalk_EveryRegisteredFamilyIsWalkedWhateverItDeclares is R6's "the
// table inventories what is here" clause, and it REPLACES a row that asserted the
// opposite. The walk used to filter a registered family through a union mirroring
// the builtin one — syncable, OR embedding declared anywhere in the cascade — and
// the live confirmation showed what that cost: a family declaring neither was
// collected, landed fifteen nodes, and was absent from the inventory while every
// other surface rendered it.
//
// THE FOUR DECLARATION SHAPES ARE KEPT, because what they now decide is the
// SEGMENT CELL rather than the row's existence, and a family embedding through a
// single node-type override is the shape that keeps that read off the graph-level
// flag alone.
func TestCoverageWalk_EveryRegisteredFamilyIsWalkedWhateverItDeclares(t *testing.T) {
	fake := &coverageFake{baseNamesByType: map[string][]string{
		"syncable-only": {"inst"},
		"override-only": {"inst"},
		"graph-embed":   {"inst"},
		"neither":       {"inst"},
	}}
	deps := &coverageDeps{gc: fake, crud: &stubGraphTypeCRUD{defs: []*knowledgev1.GraphTypeDef{
		customFamilyDef("syncable-only", new(true), new(false), nil),
		customFamilyDef("override-only", new(false), nil, map[string]bool{"issue": true}),
		customFamilyDef("graph-embed", new(false), new(true), nil),
		customFamilyDef("neither", new(false), new(false), map[string]bool{"issue": false}),
	}}}

	labels := targetLabels(mustCoverageTargets(t, context.Background(), deps))

	assert.Contains(t, labels, "syncable-only/inst", "a syncable family is on the inventory")
	assert.Contains(t, labels, "graph-embed/inst", "so is one declaring embedding at the graph level")
	assert.Contains(t, labels, "override-only/inst", "and one declaring it through a NODE-TYPE OVERRIDE alone")
	assert.Contains(t, labels, "neither/inst",
		"and so is one declaring NEITHER: its columns are structurally zero, which is a true statement about a graph that exists, not a reason to delete it from the inventory")

	// THE DECLARATION STILL DECIDES SOMETHING, and this is what: the no-instance
	// row's segment cell. A family declaring no embedding anywhere can carry no
	// pool, so its cell is the bare dash rather than an unread one.
	assert.False(t, declaresEmbeddingAnywhere(customFamilyDef("neither", new(false), new(false), map[string]bool{"issue": false})))
	assert.True(t, declaresEmbeddingAnywhere(customFamilyDef("override-only", new(false), nil, map[string]bool{"issue": true})),
		"a single node-type override turning embedding on is what stops this read collapsing to the graph-level flag")
}

// TestCoverageWalk_NoCatalogIsCapabilityAbsenceNotFailure pins the half of the
// landed idiom that is NOT a degrade: a client with no registration catalog
// wired has no optional capability to exercise, so the walk returns the builtin
// rows and NO error.
//
// THE BASELINE IS AN EMPTY CATALOG, NOT A SECOND NIL. Comparing a nil-crud walk
// against a nil-crud walk cannot fail for any implementation; comparing it
// against a client that HAS a catalog and finds it empty is a real distinction,
// and it is the one that says a missing capability and an empty answer agree.
func TestCoverageWalk_NoCatalogIsCapabilityAbsenceNotFailure(t *testing.T) {
	ctx := context.Background()
	emptyCatalog, err := coverageTargets(ctx, &coverageDeps{gc: &coverageFake{}, crud: &stubGraphTypeCRUD{}})
	require.NoError(t, err)
	baseline := targetLabels(emptyCatalog)
	require.NotEmpty(t, baseline)

	t.Run("nil crud", func(t *testing.T) {
		got, gotErr := coverageTargets(ctx, &coverageDeps{gc: &coverageFake{}})
		require.NoError(t, gotErr, "a client with no catalog wired has no capability to fail, so this is not an error")
		assert.Equal(t, baseline, targetLabels(got),
			"a degraded client renders exactly what a client with an empty catalog renders")
	})

	t.Run("a record with a nil Behavior is still on the inventory", func(t *testing.T) {
		got, gotErr := coverageTargets(ctx, &coverageDeps{
			gc:   &coverageFake{baseNamesByType: map[string][]string{"no-behavior": {"inst"}}},
			crud: &stubGraphTypeCRUD{defs: []*knowledgev1.GraphTypeDef{{Name: "no-behavior"}}},
		})
		require.NoError(t, gotErr)
		assert.Equal(t, append(append([]string{}, baseline...), "no-behavior/inst"), targetLabels(got),
			"a registered record declaring nothing at all still names a real collected graph, and the inventory renders it")
	})

	t.Run("a record with an empty name is never admitted to the walk", func(t *testing.T) {
		got, gotErr := coverageTargets(ctx, &coverageDeps{
			gc:   &coverageFake{},
			crud: &stubGraphTypeCRUD{defs: []*knowledgev1.GraphTypeDef{{Behavior: &knowledgev1.BehaviorDefaults{Syncable: new(true)}}}},
		})
		require.NoError(t, gotErr)
		assert.Equal(t, baseline, targetLabels(got), "an empty family name is bad input, not a family to walk")
	})
}

// TestCoverageWalk_CatalogReadFailureIsLoudNotASilentlyShorterTable is the fix
// for the review's T3. A registration-catalog read FAILURE is not a capability
// absence: the capability is there and the answer could not be obtained, so
// swallowing it renders a table that looks complete and silently omits every
// registered family — the exact shape AGENTS.md's "bad input always errors,
// never a silent degrade" and GOVERNANCE's "default on error is FAIL LOUDLY,
// naming the condition and what was dropped" forbid.
//
// IT FAILS LOUDLY WITHOUT FAILING THE COMMAND, and that split is the decision:
// the error names the condition and rides up to the status surface, while the
// builtin rows the walk DID obtain are still returned. manage(status) is the
// operator's inventory command, and denying them every builtin fact because an
// OPTIONAL catalog read failed would be worse than the silence it replaces —
// they are most likely running it because something is already wrong.
//
// THIS TEST FAILS IF THE SWALLOW IS RESTORED: returning nil for the error is
// exactly what the first two assertions refuse.
func TestCoverageWalk_CatalogReadFailureIsLoudNotASilentlyShorterTable(t *testing.T) {
	ctx := context.Background()
	deps := &coverageDeps{gc: &coverageFake{}, crud: &failingGraphTypeCRUD{}}

	got, err := coverageTargets(ctx, deps)

	require.Error(t, err,
		"a catalog read that FAILED is not an empty catalog; reporting it as one renders a table that looks complete and omits every registered family")
	assert.Contains(t, err.Error(), "registered custom graph families",
		"the error must name WHAT WAS DROPPED, not only that something failed")
	assert.Contains(t, err.Error(), "catalog read must not happen for a builtin graph type",
		"and it must carry the underlying condition rather than replacing it with a summary")

	// THE BUILTIN ROWS SURVIVE, which is what makes this loud rather than fatal.
	assert.NotEmpty(t, targetLabels(got),
		"the rows the walk did obtain are still returned; the operator loses the custom half and is TOLD so, rather than losing the whole inventory")
	assert.Contains(t, targetLabels(got), "code/myrepo")
}

// TestRenderLLMCoverage_NamesADroppedCatalogOnTheStatusSurface is the other half
// of the same fix: the error is not merely returned into a variable, it REACHES
// THE OPERATOR. A loud failure nothing renders is a swallow with extra steps.
func TestRenderLLMCoverage_NamesADroppedCatalogOnTheStatusSurface(t *testing.T) {
	ctx := context.Background()
	fake := &coverageFake{statsByKey: map[string]*knowledgev1.GraphStats{}}

	failed := renderLLMCoverage(ctx, &coverageDeps{gc: fake, crud: &failingGraphTypeCRUD{}})
	assert.Contains(t, failed, "registered custom graph families",
		"the status body must NAME the families it could not list")
	assert.Contains(t, failed, "catalog read must not happen for a builtin graph type",
		"and carry the condition that stopped it")

	// THE SAME-RUN CONTRAST: a healthy catalog renders the table with no such
	// line, so the assertion above is about the failure and not about a banner the
	// section always carries.
	healthy := renderLLMCoverage(ctx, &coverageDeps{gc: fake, crud: &stubGraphTypeCRUD{}})
	assert.NotContains(t, healthy, "registered custom graph families",
		"a healthy catalog renders no failure line")
}

// TestLocalDaemonJSON_CarriesTheCatalogFailureOnItsOwnKey is the MACHINE-READABLE
// half of the same fix, and it is the half nothing observed: deleting the
// coverage_error assignment left the whole package green, so the key could have
// been dropped by any later edit without a single test noticing.
//
// A JSON CONSUMER CANNOT SEE THE TEXT BODY'S FAILURE LINE. It reads coverage[],
// which under a catalog failure is a SHORTER LIST with nothing in it to say a
// family was dropped — the same silent degrade the text arm was fixed for. The
// key carries the condition, and it sits OUTSIDE coverage[] because that block's
// per-row shape is pinned to exactly ten keys.
//
// THREE ARMS, because the key's value is in when it is ABSENT as much as when it
// is present: a catalog FAILURE sets it; a nil GraphTypeCRUD is capability
// absence and must not; a healthy catalog must not.
func TestLocalDaemonJSON_CarriesTheCatalogFailureOnItsOwnKey(t *testing.T) {
	ctx := context.Background()
	seed := func() *coverageFake {
		return &coverageFake{statsByKey: map[string]*knowledgev1.GraphStats{
			"knowledge": {NodeCount: 42, NonProxyNodeCount: 10, SummarizedCount: 5, BinaryVectorCount: 5},
		}}
	}

	t.Run("a catalog List failure sets it", func(t *testing.T) {
		m := map[string]any{}
		addLocalDaemonJSON(ctx, &coverageDeps{gc: seed(), crud: &failingGraphTypeCRUD{}}, m)

		got, ok := m["coverage_error"]
		require.True(t, ok,
			"a JSON consumer reads coverage[], which under a catalog failure is a shorter list with nothing to say a family was dropped; the key is the only thing that tells it")
		require.IsType(t, "", got)
		assert.Contains(t, got.(string), "registered custom graph families",
			"the key must name WHAT WAS DROPPED")
		assert.Contains(t, got.(string), "catalog read must not happen for a builtin graph type",
			"and carry the condition that stopped it")

		// THE ROWS STILL SHIP, and their pinned shape is untouched: the failure is
		// reported BESIDE the inventory rather than instead of it.
		assert.Contains(t, m, "coverage", "the builtin rows are still rendered")
	})

	t.Run("a nil catalog does NOT set it: capability absence is not failure", func(t *testing.T) {
		m := map[string]any{}
		addLocalDaemonJSON(ctx, &coverageDeps{gc: seed()}, m)
		assert.NotContains(t, m, "coverage_error",
			"a client with no registration catalog wired has nothing to fail, so a consumer must not be told something was dropped")
		assert.Contains(t, m, "coverage")
	})

	t.Run("a healthy catalog does NOT set it", func(t *testing.T) {
		m := map[string]any{}
		addLocalDaemonJSON(ctx, &coverageDeps{gc: seed(), crud: &stubGraphTypeCRUD{}}, m)
		assert.NotContains(t, m, "coverage_error",
			"the key is a failure report, not a field the status always carries")
		assert.Contains(t, m, "coverage")
	})
}

// TestRegisteredFamilies_ThreeSurfacesEachUnderItsOwnFilter is R7. ONE fixture,
// three families, one run, and each surface asserted against ITS OWN documented
// rule rather than against the other two:
//
//	A — syncable=true WITH a collected instance.
//	B — syncable=true, NO collected instance.
//	C — syncable=false AND declaring no embedding anywhere in the cascade.
//
// THE THREE RULES ARE DIFFERENT BY CONSTRUCTION, so "the surfaces agree on the
// set" would have no basis. custom_collector(list) renders every registered
// record unfiltered; sync list renders the records whose Behavior.Syncable is
// true, so C is off it; the coverage table is an INVENTORY and renders every
// registered family, C included, because a family declaring nothing still holds
// whatever has been collected under it. C's presence on two surfaces and absence
// from the third is what makes sync list's rule a filter rather than a missing
// record.
func TestRegisteredFamilies_ThreeSurfacesEachUnderItsOwnFilter(t *testing.T) {
	ctx := context.Background()
	defs := []*knowledgev1.GraphTypeDef{
		customFamilyDef("alpha-a", new(true), nil, nil),
		customFamilyDef("bravo-b", new(true), nil, nil),
		customFamilyDef("charlie-c", new(false), new(false), nil),
	}
	fake := &coverageFake{baseNamesByType: map[string][]string{
		"alpha-a":   {"inst-a"},
		"bravo-b":   {},
		"charlie-c": {"inst-c"},
	}}
	deps := &coverageDeps{gc: fake, crud: &stubGraphTypeCRUD{defs: defs}}

	// SURFACE 1 — custom_collector(list): UNFILTERED. Every registered record.
	listed := resultText(handleGraphTypeList(ctx, deps, graphTypeArgs{}))
	for _, name := range []string{"alpha-a", "bravo-b", "charlie-c"} {
		assert.Contains(t, listed, name,
			"custom_collector(list) renders every registered record with no filter at all, so %q must be on it", name)
	}

	// SURFACE 2 — sync list: Behavior.Syncable. A and B, never C.
	synced := resultText(handleSyncList(ctx, deps))
	assert.Contains(t, synced, "alpha-a", "sync list renders a syncable family with an instance")
	assert.NotContains(t, synced, "charlie-c",
		"sync list's filter is Behavior.Syncable, and charlie-c declares syncable=false")

	// SURFACE 3 — the coverage table: the machine's INVENTORY, unfiltered by
	// behavior. All three are on it; what differs is the shape of the row.
	labels := targetLabels(mustCoverageTargets(t, ctx, deps))
	assert.Contains(t, labels, "alpha-a/inst-a", "a family with a collected instance renders that instance's row")
	assert.Contains(t, labels, "charlie-c/inst-c",
		"charlie-c declares neither axis and its columns are structurally zero, which is a fact about a graph that EXISTS — the inventory renders it")
	askedB := false
	for _, req := range fake.execReqs {
		if req.GetTarget().GetGraph() == "bravo-b" {
			askedB = true
		}
	}
	assert.True(t, askedB, "bravo-b is enumerated; the walk asks about every registered family")
	assert.Contains(t, labels, "bravo-b",
		"and with no collected instance it renders the bare family row, which is the fix for the family that was invisible before its first collect")
	for _, label := range labels {
		assert.False(t, strings.HasPrefix(label, "bravo-b/"), "bravo-b has no collected instance to name: %s", label)
	}

	// THE SAME-RUN KNOWN-POSITIVE for every omission above: a builtin family's row
	// is on the coverage table in this very call.
	assert.Contains(t, labels, "code/myrepo")
	assert.Contains(t, labels, string(kgtypes.GraphKnowledge))
}
