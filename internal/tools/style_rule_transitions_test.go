// SPDX-License-Identifier: Apache-2.0

package tools

// style_rule_transitions_test.go drives the STATE TRANSITIONS a style rule
// passes through: write it, read it back by id, delete its whole hub, and change
// its scope and watch the index follow.
//
// A TRANSITION IS NOT A SECOND SPELLING OF A UNIT TEST. Each one below runs the
// real lowering for BOTH halves — engine.Compile for the read and for the
// delete, handleImportStyleRules for the write — against one fake corpus that
// carries state between them, so a rule written by one path is read by another.
// A per-half unit test cannot see the class of defect where two halves each
// behave correctly and disagree about the state in between.
//
// THE FAKE APPLIES THE SELECTION IT IS GIVEN, delete included. A double that
// dropped its whole corpus on any delete would make the by-hub row below pass
// with the hub predicate deleted.

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// styleRuleLongPathBytes is the length of the LONGEST repo-relative path this
// repository actually carries, measured over the tree with
//
//	git ls-files | awk '{print length"\t"$0}' | sort -rn | head -1
//
// It is the number the scope column's cap has to survive: one real path already
// exceeds the 60-byte column, which is why that column truncates by design.
const styleRuleLongPathBytes = 137

// styleRuleLongPath is a repo-relative path of exactly that length.
//
// IT IS COMPOSED RATHER THAN QUOTED. The measured longest path names a
// build-flavor directory the shipped-surface gate refuses to carry, and the
// property under test is the LENGTH in bytes — the cap is a byte cap, so the
// path's spelling changes nothing and its length changes everything. The
// composition is asserted against the measured number at the point of use.
var styleRuleLongPath = styleRuleScopeFixturePath()

func styleRuleScopeFixturePath() string {
	const prefix, suffix = "scripts/testdata/style_rule_scope/", "/rule_scope_fixture.go"
	return prefix + strings.Repeat("d", styleRuleLongPathBytes-len(prefix)-len(suffix)) + suffix
}

// styleRuleByID issues the by-id read a caller makes with
// query(graph:"practice", id:<rule id>), through engine.Compile's real lowering.
func styleRuleByID(t *testing.T, f *styleRuleStoreFake, id string) *knowledgev1.Node {
	t.Helper()
	args, err := json.Marshal(map[string]any{"graph": "practice", "id": id})
	require.NoError(t, err)
	req, ok := engine.Compile("query", args)
	require.True(t, ok, "query(graph:\"practice\", id:...) must lower to a QueryPlan")
	resp, err := f.Execute(opCtx(), req)
	require.NoError(t, err)
	nodes, derr := engine.DecodeNodes(resp)
	require.NoError(t, derr)
	require.Len(t, nodes, 1, "the by-id read must resolve the rule")
	return nodes[0]
}

// styleRuleIndexRows runs the index arm over the fake for one repo.
func styleRuleIndexRows(t *testing.T, f *styleRuleStoreFake, repo string) []string {
	t.Helper()
	return styleIndexRowsOf(t, practiceStyleIndex(opCtx(), f.Execute, queryArgs{
		Graph: "practice", Mode: "style_index", Source: "hub-go", Repo: repo,
	}))
}

// TestStyleRuleTransition_ImportThenByIDReturnsTheFullScope is requirement 4's
// first half and the JUSTIFICATION THE COLUMN CAP RESTS ON.
//
// The index truncates the scope column at 60 bytes, and one real path in this
// tree is 137. That is only legitimate because the truncated value is
// recoverable WHOLE from the by-id lookup — so the recoverability is an
// assertion, not an aside, and until now it was two comment lines.
//
// THE MUTATION, either half: drop styleIndexEllipsis from capStyleColumn and the
// truncation stops being visible; or store the capped value instead of the whole
// one (encode capStyleColumn's output into style_scope_paths) and the by-id read
// stops returning it. Each turns this red.
func TestStyleRuleTransition_ImportThenByIDReturnsTheFullScope(t *testing.T) {
	f := newStyleRuleStoreFake()
	longSummary := strings.Repeat("s", 120)
	body := styleRuleListJSON(t, map[string]any{"rules": []any{map[string]any{
		"id": "style-rule-longscope-00000000001", "name": "long-scope",
		"summary": longSummary, "text": "the rule's whole prose body", "severity": "warning",
		"scope": map[string]any{"repo": "knowledge", "paths": []string{styleRuleLongPath}},
	}}})
	msg, isErr := importRules(t, f, body, false)
	require.False(t, isErr, msg)
	require.Len(t, styleRuleLongPath, styleRuleLongPathBytes,
		"the fixture must be as long as the longest path the tree carries — the cap is a BYTE cap")

	// (a) THE INDEX ROW TRUNCATES, VISIBLY. Both variable-width columns are over
	// the cap, so both must be cut and both must say so.
	rows := styleRuleIndexRows(t, f, "knowledge")
	require.Len(t, rows, 1)
	cols := strings.Split(rows[0], "\t")
	require.Len(t, cols, 5)

	assert.Len(t, cols[2], styleIndexColumnCap, "the scope column is capped at the declared width")
	assert.True(t, strings.HasSuffix(cols[2], styleIndexEllipsis),
		"a truncated scope must be VISIBLY truncated — a silent cut reads as the whole value")
	assert.NotContains(t, cols[2], "rule_scope_fixture.go",
		"the tail of the over-cap path is genuinely absent from the row, not merely elided in display")
	assert.Len(t, cols[3], styleIndexColumnCap)
	assert.True(t, strings.HasSuffix(cols[3], styleIndexEllipsis))

	// (b) AND THE BY-ID READ RETURNS IT WHOLE. This is the half the cap's own doc
	// cites as its justification.
	node := styleRuleByID(t, f, "style-rule-longscope-00000000001")
	sc, err := styleScopeFromMetadata(node.GetMetadata())
	require.NoError(t, err)
	require.True(t, sc.PathsSet)
	assert.Equal(t, []string{styleRuleLongPath}, sc.Paths,
		"the by-id lookup returns the FULL scope the index row truncated — the recoverability "+
			"the 60-byte column cap is justified by")
	assert.Equal(t, "knowledge", sc.Repo)
	assert.Equal(t, longSummary, node.GetSummary(),
		"and the full summary, which the index also capped")
	assert.Equal(t, "the rule's whole prose body", node.GetDescription(),
		"and the rule's prose, which the index renders not at all")
}

// TestStyleRuleTransition_ImportThenByHubDeleteThenIndexIsEmpty is the
// create-then-delete-then-read transition, over the landed delete tool's own
// `source` arm.
//
// THE DELETE'S LOWERING IS REAL: engine.Compile("delete", ...) is what turns
// source:<hub> into the single source_hub OP_EQ predicate the store selects on,
// and the same fake that served the index applies it.
//
// THE MUTATION: point compileDelete's by-hub predicate at any other key, or
// remove the by-hub arm so `source` falls through to the prune shape. The
// surviving-rules assertion goes red.
func TestStyleRuleTransition_ImportThenByHubDeleteThenIndexIsEmpty(t *testing.T) {
	f := newStyleRuleStoreFake()
	msg, isErr := importRules(t, f, `{"rules":[
		{"name":"a","summary":"sa","text":"t","severity":"warning"},
		{"name":"b","summary":"sb","text":"t","severity":"info"}
	]}`, false)
	require.False(t, isErr, msg)

	// A rule under ANOTHER hub, so the delete's selection has something it must
	// NOT take. Without it a delete that dropped the whole corpus would pass.
	f.seedRule("style-rule-otherhub-0000000000001", "hub-python", "a python rule")

	require.Len(t, styleRuleIndexRows(t, f, ""), 2, "precondition: the go hub's two rules are indexed")

	args, err := json.Marshal(map[string]any{"graph": "practice", "source": "hub-go", "hard": true})
	require.NoError(t, err)
	req, ok := engine.Compile("delete", args)
	require.True(t, ok, "delete(source:<hub>) must lower to a DELETE MutationPlan")
	require.Equal(t, knowledgev1.MutationPlan_MUTATION_KIND_DELETE, req.GetMutation().GetKind())
	preds := req.GetMutation().GetSelection().GetMetadataPredicates()
	require.Len(t, preds, 1, "the by-hub delete selects on ONE predicate")
	assert.Equal(t, kgtypes.MetaKeySourceHub, preds[0].GetKey(),
		"and it is the hub key the import stamped, which is what ties the two operations together")

	_, err = f.Execute(opCtx(), req)
	require.NoError(t, err)

	assert.Empty(t, styleRuleIndexRows(t, f, ""),
		"every rule the hub grouped is gone, so the index for that hub renders no rows")
	assert.Equal(t, styleIndexEmpty, styleIndexBody(styleRuleIndexRows(t, f, "")),
		"and the zero-row render is the empty-state line, not an empty body")

	// THE CONTROL: the other hub's rule survived, so the delete selected rather
	// than emptied.
	assert.Contains(t, f.nodes, "style-rule-otherhub-0000000000001",
		"a rule under another hub must survive — otherwise the assertions above "+
			"would pass just as well against a delete that ignored its selection")
}

// TestStyleRuleTransition_ARuleGainsAScopeAndLeavesTheIndex is the
// write-then-rewrite-then-read transition.
//
// IT GOES THROUGH THE RE-IMPORT'S UPDATE ARM rather than editing the fake's node
// directly, so the metadata merge that carries the new scope is the real one.
//
// THE MUTATION: make styleScopeMatches admit a rule whose style_scope_repo
// DIFFERS from the caller's repo. The disappearance assertion goes red.
func TestStyleRuleTransition_ARuleGainsAScopeAndLeavesTheIndex(t *testing.T) {
	f := newStyleRuleStoreFake()
	const id = "style-rule-gains-scope-000000001"

	unscoped := styleRuleListJSON(t, map[string]any{"rules": []any{map[string]any{
		"id": id, "name": "a", "summary": "applies everywhere", "text": "t", "severity": "warning",
	}}})
	msg, isErr := importRules(t, f, unscoped, false)
	require.False(t, isErr, msg)
	require.Len(t, styleRuleIndexRows(t, f, "knowledge"), 1,
		"precondition: an unscoped rule is in the index for every repo")
	require.NotContains(t, f.nodes[id].GetMetadata(), kgtypes.MetaKeyStyleScopeRepo,
		"and it carries no scope key at all — absent, not empty")

	scoped := styleRuleListJSON(t, map[string]any{"rules": []any{map[string]any{
		"id": id, "name": "a", "summary": "applies everywhere", "text": "t", "severity": "warning",
		"scope": map[string]any{"repo": "another-repo"},
	}}})
	msg, isErr = importRules(t, f, scoped, false)
	require.False(t, isErr, msg)
	require.Equal(t, knowledgev1.MutationPlan_MUTATION_KIND_UPDATE_ITEMS, f.plans[1].GetKind(),
		"the second import UPDATES the existing rule rather than re-creating it")
	require.Equal(t, "another-repo", f.nodes[id].GetMetadata()[kgtypes.MetaKeyStyleScopeRepo],
		"and the scope key landed")

	assert.Empty(t, styleRuleIndexRows(t, f, "knowledge"),
		"the rule now excludes this repo, so it disappears from this repo's index")
	assert.Len(t, styleRuleIndexRows(t, f, "another-repo"), 1,
		"and appears in the index for the repo it now names — the control that makes the "+
			"absence above a narrowing rather than the rule being gone")
	assert.Len(t, styleRuleIndexRows(t, f, ""), 1,
		"a caller naming NO repo has nothing to exclude by, so the scoped rule is listed")
}

// TestStyleRuleTransition_ReimportIntoAPopulatedHubStillUpdates is the
// idempotence transition over a hub that already holds OTHER rules.
//
// WHY THE POPULATED CORPUS IS THE WHOLE TEST. The re-import decides between its
// update arm and its create arm by READING which of the caller's ids already
// resolve, and a create over an existing node stores the body whole and clears
// every field the file omitted. Over a corpus of one that read cannot be wrong:
// any answer that returns rows returns the rule. The defect it hides only
// appears once the corpus is larger than the read's own limit — which is every
// real hub.
//
// THE MUTATION: put the existence read's ids back on Selection.ids (the WRITE
// target set) instead of QueryPlan.ids (the READ bulk-hydrate carrier). The read
// stops selecting, pages a match-all browse, misses the rule, and the import
// takes its CREATE arm over an existing node — so the surviving-key assertion
// goes red and the update-kind assertion goes red with it.
func TestStyleRuleTransition_ReimportIntoAPopulatedHubStillUpdates(t *testing.T) {
	f := newStyleRuleStoreFake()
	const id = "style-rule-existing-00000000001"

	// The hub already holds other rules, and they are ahead of this one in the
	// corpus — so a read that does not select returns THEM.
	for _, other := range []string{"other-rule-aaa", "other-rule-bbb", "other-rule-ccc"} {
		f.seedRule(other, "hub-go", "a rule that is not the one being re-imported")
	}

	first := styleRuleListJSON(t, map[string]any{"rules": []any{map[string]any{
		"id": id, "name": "a", "summary": "first summary", "text": "first text", "severity": "warning",
		"linter": map[string]any{"name": "golangci-lint", "rule_id": "modernize"},
	}}})
	msg, isErr := importRules(t, f, first, false)
	require.False(t, isErr, msg)
	require.Equal(t, knowledgev1.MutationPlan_MUTATION_KIND_CREATE, f.plans[0].GetKind())
	require.Equal(t, "golangci-lint", f.nodes[id].GetMetadata()[kgtypes.MetaKeyLinterName])

	// The second file OMITS the linter block. Only the update arm preserves it.
	second := styleRuleListJSON(t, map[string]any{"rules": []any{map[string]any{
		"id": id, "name": "a", "summary": "second summary", "text": "second text", "severity": "warning",
	}}})
	msg, isErr = importRules(t, f, second, false)
	require.False(t, isErr, msg)

	require.Len(t, f.plans, 2, "the re-import is one more write, not a duplicate create")
	assert.Equal(t, knowledgev1.MutationPlan_MUTATION_KIND_UPDATE_ITEMS, f.plans[1].GetKind(),
		"the existence read must have RESOLVED the id, over a corpus larger than its own limit")
	assert.Equal(t, "golangci-lint", f.nodes[id].GetMetadata()[kgtypes.MetaKeyLinterName],
		"a key the second file omitted survives the per-key merge — a second CREATE would have cleared it")
	assert.Equal(t, "second summary", f.nodes[id].GetSummary(), "and the named fields did update")
	assert.Len(t, f.order, 5, "no duplicate node was created — four rules plus the fake's seeded hub")
}

// seedRule puts a style rule into the fake's corpus directly, for the fixtures a
// transition needs to exist WITHOUT having been imported — the control rules a
// selection must leave alone.
func (f *styleRuleStoreFake) seedRule(id, hub, summary string) {
	if _, existed := f.nodes[id]; !existed {
		f.order = append(f.order, id)
	}
	f.nodes[id] = &knowledgev1.Node{
		Id: id, Type: styleRuleNodeType, SymbolName: id, Summary: summary,
		Metadata: map[string]string{
			kgtypes.MetaKeySourceHub:    hub,
			kgtypes.MetaKeyPracticeKind: kgtypes.PracticeKindStyleRule,
			"severity":                  "info",
		},
	}
}

// deleteMatching applies a DELETE plan's metadata-predicate selection, removing
// every node it selects and NOTHING else.
//
// IT APPLIES THE SELECTION rather than emptying the corpus, which is what makes
// the by-hub delete transition an observation: a fake that dropped everything on
// any delete would pass with the hub predicate deleted from the lowering.
func (f *styleRuleStoreFake) deleteMatching(m *knowledgev1.MutationPlan) *knowledgev1.ExecuteResponse {
	sel := m.GetSelection()
	kept := make([]string, 0, len(f.order))
	removed := 0
	for _, id := range f.order {
		n := f.nodes[id]
		byID := len(sel.GetIds()) > 0 && slices.Contains(sel.GetIds(), id)
		byMeta := len(sel.GetMetadataPredicates()) > 0 &&
			styleIndexFakePredMatch(sel.GetMetadataPredicates(), n.GetMetadata())
		if byID || byMeta {
			delete(f.nodes, id)
			removed++
			continue
		}
		kept = append(kept, id)
	}
	f.order = kept
	return &knowledgev1.ExecuteResponse{AffectedCount: int64(removed)}
}
