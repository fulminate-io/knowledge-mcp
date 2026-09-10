// SPDX-License-Identifier: Apache-2.0

package tools

// style_rule_import_test.go covers manage(import_style_rules): the rule-list
// parser's input classes, every refusal, the create-versus-update split, the
// no-check guarantee and the atomicity of a list carrying one bad rule.
//
// THE FAKE MODELS THE STORE'S OWN CREATE/UPDATE ASYMMETRY rather than storing
// whatever it is handed. That asymmetry is the whole reason the idempotent arm
// exists: a CREATE stores the node body WHOLE and copies nothing from the
// existing row, so it clears every field the payload omits, while an
// UPDATE_ITEMS merges metadata PER KEY and touches only the fields the item
// names. A fake that merged on both kinds would let a re-create pass the
// field-preservation test, and that test would then be measuring nothing.

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// styleRuleStoreFake is an Execute seam that answers reads from a node map and
// applies mutations the way the store documents.
// TestImportStyleRules_WritesPracticeNodesInOneBatch is the control and
// requirement 5's first observation.
func TestImportStyleRules_WritesPracticeNodesInOneBatch(t *testing.T) {
	f := newStyleRuleStoreFake()
	body, isErr := importRules(t, f, `{"rules":[
		{"name":"a","summary":"sa","text":"ta","severity":"warning"},
		{"name":"b","summary":"sb","text":"tb","severity":"critical",
		 "scope":{"repo":"knowledge","paths":["cmd/knowledge"]},
		 "linter":{"name":"golangci-lint","rule_id":"modernize"}}
	]}`, false)
	require.False(t, isErr, body)

	require.Len(t, f.plans, 1, "an N-rule list costs ONE batch, not one call per rule")
	assert.Equal(t, "practice", f.targets[0], "the batch is addressed to the practice graph")
	require.Len(t, f.plans[0].GetNodeBodies(), 2)

	// THE HUB IS RECORDED TWICE, and both recordings ride the SAME batch: the
	// metadata key every hub-scoped read predicates on, and the sourced-from edge
	// a traverse walks.
	for _, b := range f.plans[0].GetNodeBodies() {
		assert.Equal(t, styleRuleNodeType, b.GetType())
		assert.Equal(t, "hub-go", b.GetMetadata()[kgtypes.MetaKeySourceHub])
		assert.Equal(t, kgtypes.PracticeKindStyleRule,
			b.GetMetadata()[kgtypes.MetaKeyPracticeKind])
	}
	require.Len(t, f.edges, 2, "one sourced-from edge per created rule, in the same plan")
	for _, e := range f.edges {
		assert.Equal(t, "hub-go", e.GetToId())
		assert.Equal(t, string(kgtypes.EdgeSourcedFrom), e.GetType())
	}

	// The rule's text is the node's description and the summary is its own field.
	bodies := map[string]*knowledgev1.NodeBody{}
	for _, b := range f.plans[0].GetNodeBodies() {
		bodies[b.GetName()] = b
	}
	assert.Equal(t, "ta", bodies["a"].GetDescription())
	assert.Equal(t, "sa", bodies["a"].GetSummary())

	// The optional keys: present when supplied, ABSENT — never present-and-empty
	// — when not.
	mdB := bodies["b"].GetMetadata()
	assert.Equal(t, "knowledge", mdB[kgtypes.MetaKeyStyleScopeRepo])
	assert.JSONEq(t, `["cmd/knowledge"]`, mdB[kgtypes.MetaKeyStyleScopePaths])
	assert.Equal(t, "golangci-lint", mdB[kgtypes.MetaKeyLinterName])
	assert.Equal(t, "modernize", mdB[kgtypes.MetaKeyLinterRuleID])

	mdA := bodies["a"].GetMetadata()
	for _, absent := range []string{
		kgtypes.MetaKeyStyleScopeRepo, kgtypes.MetaKeyStyleScopePaths,
		kgtypes.MetaKeyLinterName, kgtypes.MetaKeyLinterRuleID,
		kgtypes.MetaKeySisterCheck,
	} {
		assert.NotContains(t, mdA, absent,
			"an optional key a rule did not supply is ABSENT, never present-and-empty")
	}
}

// TestImportStyleRules_CreatesNoCheck is requirement 5's hardest clause: no
// check node, no fixture node and no sister_check value, even for a rule that
// carries a whole check shape with both fixtures.
func TestImportStyleRules_CreatesNoCheck(t *testing.T) {
	f := newStyleRuleStoreFake()
	body, isErr := importRules(t, f, `{"rules":[{
		"name":"defer-close","summary":"always defer Close","text":"Close what you open.",
		"severity":"critical",
		"check":{"dsl_pattern":"defer $X.Close()","check_where":"{}",
		         "fixture_bad":"fixture-bad-node","fixture_good":"fixture-good-node"}
	}]}`, false)
	require.False(t, isErr, body)

	for i, target := range f.targets {
		assert.Equal(t, "practice", target,
			"mutation %d must address the practice graph — no write reaches the checks graph", i)
	}
	require.Len(t, f.plans, 1, "one batch: no second write for a check or its fixtures")
	require.Len(t, f.plans[0].GetNodeBodies(), 1,
		"ONE node — the practice rule. A check node and two fixture nodes would be four")

	md := f.plans[0].GetNodeBodies()[0].GetMetadata()
	assert.NotContains(t, md, kgtypes.MetaKeySisterCheck,
		"the cross-link is filled by whoever authors the sister check, never by the import")
	// The shape rides as INERT data, which is the other half of the same clause:
	// carried, and never compiled.
	assert.Equal(t, "defer $X.Close()", md["dsl_pattern"])
	assert.Equal(t, "fixture-bad-node", md["check_fixture_bad"])
	assert.NotContains(t, md, "check_type",
		"no check_type is stamped, so nothing in the tree reads this node as a check")

	assert.Contains(t, body, "No check and no fixture node was created",
		"the report says what it did NOT write, so a reader need not assume either way")
}

// TestImportStyleRules_UpdatesRatherThanRecreates is the idempotence clause.
func TestImportStyleRules_UpdatesRatherThanRecreates(t *testing.T) {
	f := newStyleRuleStoreFake()

	first := `{"rules":[{"id":"rule-fixed-id","name":"a","summary":"first summary",
		"text":"first text","severity":"warning",
		"linter":{"name":"golangci-lint","rule_id":"modernize"}}]}`
	body, isErr := importRules(t, f, first, false)
	require.False(t, isErr, body)
	require.Len(t, f.plans, 1)
	assert.Equal(t, knowledgev1.MutationPlan_MUTATION_KIND_CREATE, f.plans[0].GetKind(),
		"an id that resolves to nothing yet is CREATED, carrying that id")
	require.Contains(t, f.nodes, "rule-fixed-id")

	// The SECOND import of a list whose entry now omits the linter block.
	second := `{"rules":[{"id":"rule-fixed-id","name":"a","summary":"second summary",
		"text":"second text","severity":"warning"}]}`
	body, isErr = importRules(t, f, second, false)
	require.False(t, isErr, body)

	require.Len(t, f.plans, 2, "the re-import is ONE more write, not a duplicate create")
	assert.Equal(t, knowledgev1.MutationPlan_MUTATION_KIND_UPDATE_ITEMS, f.plans[1].GetKind(),
		"an id that already resolves is UPDATED — a second CREATE would store the body whole")
	assert.Equal(t, []string{"hub-go", "rule-fixed-id"}, f.order,
		"no duplicate node — the hub the import writes under is the fake's seed, not a second rule")

	node := f.nodes["rule-fixed-id"]
	assert.Equal(t, "second summary", node.GetSummary(), "the named fields are updated")
	assert.Equal(t, "second text", node.GetDescription())
	assert.Equal(t, "golangci-lint", node.GetMetadata()[kgtypes.MetaKeyLinterName],
		"a key the second file OMITTED survives — the update merges per key, a create would clear it")
}

// TestImportStyleRules_ValidatesBeforeWriting is the atomicity observation: one
// bad rule in the middle of a list leaves ZERO nodes written.
func TestImportStyleRules_ValidatesBeforeWriting(t *testing.T) {
	f := newStyleRuleStoreFake()
	body, isErr := importRules(t, f, `{"rules":[
		{"name":"a","summary":"s","text":"t","severity":"warning"},
		{"name":"b","summary":"s","text":"t","severity":"notice"},
		{"name":"c","summary":"s","text":"t","severity":"high"},
		{"name":"d","summary":"s","text":"t","severity":"info"},
		{"name":"e","summary":"s","text":"t","severity":"critical"}
	]}`, false)

	require.True(t, isErr, "an invalid rule refuses the whole import")
	assert.Contains(t, body, "rules[2]", "the refusal names WHICH rule, by its index in the file")
	assert.Contains(t, body, "high", "and the offending value")
	assert.Empty(t, f.plans, "ZERO writes — not two, and not four")
	assert.Len(t, f.nodes, 1, "and no rule landed: the only node is the hub the fake seeds")
}

// TestImportStyleRules_Refusals covers every refusal class the format defines,
// each asserted on the MESSAGE rather than on the error being non-nil.
func TestImportStyleRules_Refusals(t *testing.T) {
	// The known-positive control, on the same instrument in the same run.
	f := newStyleRuleStoreFake()
	_, isErr := importRules(t, f, oneRule, false)
	require.False(t, isErr, "control: a well-formed list is ACCEPTED, so the refusals below discriminate")

	cases := []struct {
		name, body string
		wants      []string
	}{
		{"no rules key", `{}`, []string{"empty"}},
		{"empty rules array", `{"rules":[]}`, []string{"empty"}},
		{"missing name", `{"rules":[{"summary":"s","text":"t","severity":"info"}]}`,
			[]string{"rules[0]", "name"}},
		{"missing summary", `{"rules":[{"name":"a","text":"t","severity":"info"}]}`,
			[]string{"rules[0]", "summary"}},
		{"missing text", `{"rules":[{"name":"a","summary":"s","severity":"info"}]}`,
			[]string{"rules[0]", "text"}},
		{"blank name", `{"rules":[{"name":"  ","summary":"s","text":"t","severity":"info"}]}`,
			[]string{"rules[0]", "name"}},
		{"severity off the ladder", `{"rules":[{"name":"a","summary":"s","text":"t","severity":"medium"}]}`,
			[]string{"rules[0]", "medium", "info", "critical"}},
		{"severity in the wrong case", `{"rules":[{"name":"a","summary":"s","text":"t","severity":"Warning"}]}`,
			[]string{"rules[0]", "Warning"}},
		{"absolute scope path",
			`{"rules":[{"name":"a","summary":"s","text":"t","severity":"info","scope":{"paths":["/etc/x"]}}]}`,
			[]string{"rules[0]", "scope.paths[0]", "absolute"}},
		{"dot-dot scope path",
			`{"rules":[{"name":"a","summary":"s","text":"t","severity":"info","scope":{"paths":["../x"]}}]}`,
			[]string{"scope.paths[0]", "`..` segment"}},
		{"non-canonical scope path",
			`{"rules":[{"name":"a","summary":"s","text":"t","severity":"info","scope":{"paths":["./cmd"]}}]}`,
			[]string{"scope.paths[0]", "repo-relative spelling"}},
		{"empty scope block",
			`{"rules":[{"name":"a","summary":"s","text":"t","severity":"info","scope":{}}]}`,
			[]string{"rules[0]", "empty `scope` block"}},
		{"empty linter block",
			`{"rules":[{"name":"a","summary":"s","text":"t","severity":"info","linter":{}}]}`,
			[]string{"rules[0]", "empty `linter` block"}},
		{"unknown key at the document level",
			`{"rules":[],"rulez":[]}`, []string{"rulez"}},
		{"unknown key at the rule level",
			`{"rules":[{"name":"a","summary":"s","text":"t","severity":"info","sevrity":"x"}]}`,
			[]string{"rules[0]", "sevrity"}},
		{"unknown key inside scope",
			`{"rules":[{"name":"a","summary":"s","text":"t","severity":"info","scope":{"repoz":"x"}}]}`,
			[]string{"rules[0]", "repoz"}},
		{"duplicate id within one file",
			`{"rules":[{"id":"dup","name":"a","summary":"s","text":"t","severity":"info"},` +
				`{"id":"dup","name":"b","summary":"s","text":"t","severity":"info"}]}`,
			[]string{"rules[0]", "rules[1]", "dup"}},
		{"not JSON at all", `this is not json`, []string{}},
		{"trailing content past the document", `{"rules":[]} {"rules":[]}`, []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := newStyleRuleStoreFake()
			msg, isErr := importRules(t, fake, tc.body, false)
			require.True(t, isErr, "must be refused, got: %s", msg)
			for _, want := range tc.wants {
				assert.Contains(t, msg, want)
			}
			assert.Empty(t, fake.plans, "a refused import writes nothing")
		})
	}
}

// TestImportStyleRules_InputRefusals covers the two params and the file itself.
func TestImportStyleRules_InputRefusals(t *testing.T) {
	f := newStyleRuleStoreFake()
	deps := interceptTestDeps{gc: f}

	t.Run("no hub", func(t *testing.T) {
		res := handleImportStyleRules(opCtx(), deps, manageArgs{
			Operation: OpStyleRulesImport, Path: writeRuleList(t, oneRule),
		})
		require.True(t, res.IsError)
		assert.Contains(t, toolResultText(res), "source")
	})
	t.Run("no path", func(t *testing.T) {
		res := handleImportStyleRules(opCtx(), deps, manageArgs{
			Operation: OpStyleRulesImport, Source: "hub-go",
		})
		require.True(t, res.IsError)
		assert.Contains(t, toolResultText(res), "path")
	})
	t.Run("a relative path is refused, never resolved", func(t *testing.T) {
		res := handleImportStyleRules(opCtx(), deps, manageArgs{
			Operation: OpStyleRulesImport, Source: "hub-go", Path: "rules.json",
		})
		require.True(t, res.IsError)
		assert.Contains(t, toolResultText(res), "not absolute")
	})
	t.Run("an unreadable file names the path", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "nope.json")
		res := handleImportStyleRules(opCtx(), deps, manageArgs{
			Operation: OpStyleRulesImport, Source: "hub-go", Path: missing,
		})
		require.True(t, res.IsError)
		assert.Contains(t, toolResultText(res), missing)
	})
	assert.Empty(t, f.plans, "no refused call reached the write path")
}

// TestImportStyleRules_DryRunWritesNothing pins the preview arm.
func TestImportStyleRules_DryRunWritesNothing(t *testing.T) {
	f := newStyleRuleStoreFake()
	body, isErr := importRules(t, f, oneRule, true)
	require.False(t, isErr, body)
	assert.Contains(t, body, "would import")
	assert.Empty(t, f.plans, "dry_run issues ZERO mutations")
}

// TestImportStyleRules_ThenIndexed is the source × observation matrix's second
// arm: an imported rule and a hand-written one are indistinguishable once
// written, so the index lists an imported rule exactly as it lists any other.
func TestImportStyleRules_ThenIndexed(t *testing.T) {
	f := newStyleRuleStoreFake()
	body, isErr := importRules(t, f, `{"rules":[
		{"name":"a","summary":"applies everywhere","text":"t","severity":"warning"},
		{"name":"b","summary":"scoped elsewhere","text":"t","severity":"info",
		 "scope":{"repo":"another-repo"}}
	]}`, false)
	require.False(t, isErr, body)

	rows := styleIndexRowsOf(t, practiceStyleIndex(opCtx(), f.Execute, queryArgs{
		Graph: "practice", Mode: "style_index", Source: "hub-go", Repo: "knowledge",
	}))
	require.Len(t, rows, 1, "the rule scoped to another repo is absent")
	cols := strings.Split(rows[0], "\t")
	assert.Equal(t, "warning", cols[1])
	assert.Equal(t, "-", cols[2], "an unscoped rule renders `-`")
	assert.Equal(t, "applies everywhere", cols[3])
	assert.Empty(t, cols[4], "no sister check was written, so the column is empty")
}

// TestImportStyleRules_CannotInjectAWriteSelector is why the import composes its
// own mutate payload rather than routing through the practice write dispatch.
//
// THE TWO GATES THAT ARM CARRIES HAVE NOTHING TO GUARD HERE. It refuses a
// `language` on a practice write (which would silently redirect the write into a
// pre-singleton graph) and refuses a caller-supplied metadata[source_hub] that
// disagrees with the hub param. Neither is reachable through this operation: the
// rule-list format declares no language field and no metadata passthrough, and
// the strict decode refuses any key it does not define. So the payload the
// import composes cannot express either fault, and that is the property this
// test pins — a stronger one than routing through a gate that would never fire.
func TestImportStyleRules_CannotInjectAWriteSelector(t *testing.T) {
	for _, tc := range []struct{ name, body, wants string }{
		{"a language field", `{"rules":[{"name":"a","summary":"s","text":"t",` +
			`"severity":"info","language":"go"}]}`, "language"},
		{"a metadata passthrough", `{"rules":[{"name":"a","summary":"s","text":"t",` +
			`"severity":"info","metadata":{"source_hub":"hub-python"}}]}`, "metadata"},
		{"a source_hub field", `{"rules":[{"name":"a","summary":"s","text":"t",` +
			`"severity":"info","source_hub":"hub-python"}]}`, "source_hub"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newStyleRuleStoreFake()
			msg, isErr := importRules(t, f, tc.body, false)
			require.True(t, isErr, "the format defines no such key, so it must be refused: %s", msg)
			assert.Contains(t, msg, tc.wants, "the refusal names the offending key")
			assert.Empty(t, f.plans)
		})
	}

	// And the hub the nodes DO carry comes from the operation's own param, on
	// every node, with no way for the file to contribute one.
	f := newStyleRuleStoreFake()
	_, isErr := importRules(t, f, oneRule, false)
	require.False(t, isErr)
	require.Len(t, f.plans, 1)
	assert.Equal(t, "hub-go",
		f.plans[0].GetNodeBodies()[0].GetMetadata()[kgtypes.MetaKeySourceHub])
}
