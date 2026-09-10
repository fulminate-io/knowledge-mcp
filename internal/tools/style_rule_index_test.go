// SPDX-License-Identifier: Apache-2.0

package tools

// style_rule_index_test.go covers the style-rule index arm: its server-side
// predicates, its drain, its client-side scope filter, its render and its
// parameters.
//
// THE FAKE APPLIES THE PREDICATES IT IS GIVEN. A double that returned its whole
// corpus regardless of the plan would make every selection assertion below pass
// with the narrowing deleted, and the agreement test in particular would be
// measuring nothing. styleIndexStoreFake therefore filters by metadata
// predicate, by id and by offset/limit, the way the store it stands in for does.

import (
	"context"
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// styleIndexStoreFake is a recording Execute seam over a fixed node corpus. It
// APPLIES the plan's metadata predicates, id selection and paging.
type styleIndexStoreFake struct {
	corpus []*knowledgev1.Node
	plans  []*knowledgev1.QueryPlan
}

func (f *styleIndexStoreFake) exec(
	_ context.Context, req *knowledgev1.ExecuteRequest,
) (*knowledgev1.ExecuteResponse, error) {
	plan := req.GetQuery()
	f.plans = append(f.plans, plan)
	sel := plan.GetSelection()

	matched := make([]*knowledgev1.Node, 0, len(f.corpus))
	for _, n := range f.corpus {
		if !styleIndexFakeIDMatch(styleFakeReadIDs(plan), n.GetId()) {
			continue
		}
		if !styleIndexFakePredMatch(sel.GetMetadataPredicates(), n.GetMetadata()) {
			continue
		}
		matched = append(matched, n)
	}
	page := styleIndexFakePage(matched, int(plan.GetOffset()), int(plan.GetLimit()))
	return &knowledgev1.ExecuteResponse{Nodes: page, Total: int64(len(matched))}, nil
}

func styleIndexFakeIDMatch(ids []string, id string) bool {
	if len(ids) == 0 {
		return true
	}
	return slices.Contains(ids, id)
}

func styleIndexFakePredMatch(preds []*knowledgev1.MetadataPredicate, md map[string]string) bool {
	for _, p := range preds {
		v, ok := md[p.GetKey()]
		switch p.GetOp() {
		case knowledgev1.MetadataPredicate_OP_EXISTS:
			if !ok {
				return false
			}
		default:
			if !ok || v != p.GetValue() {
				return false
			}
		}
	}
	return true
}

func styleIndexFakePage(in []*knowledgev1.Node, offset, limit int) []*knowledgev1.Node {
	if offset >= len(in) {
		return nil
	}
	out := in[offset:]
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// styleRuleNode builds one style-rule fixture node.
func styleRuleNode(id, summary string, extra map[string]string) *knowledgev1.Node {
	md := map[string]string{
		kgtypes.MetaKeySourceHub:    "hub-go",
		kgtypes.MetaKeyPracticeKind: kgtypes.PracticeKindStyleRule,
		"severity":                  "warning",
	}
	maps.Copy(md, extra)
	return &knowledgev1.Node{
		Id: id, Type: "pattern", SymbolName: id, Summary: summary,
		Description: "the rule's full prose body, which the index must never render",
		Metadata:    md,
	}
}

// styleIndexRowsOf splits a rendered text body into its rows.
func styleIndexRowsOf(t *testing.T, res kgtools.ToolResult) []string {
	t.Helper()
	require.False(t, res.IsError, "the index must not error: %s", toolResultText(res))
	body := textBodyTools(res)
	if body == styleIndexEmpty {
		return nil
	}
	return strings.Split(body, "\n")
}

// styleIndexIDs returns the id column of each row.
func styleIndexIDs(rows []string) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, strings.Split(r, "\t")[0])
	}
	return out
}

// TestPracticeStyleIndex_NarrowsServerSideByHubAndKind pins the two predicates
// that ARE server-side, and that the read is one drained browse rather than an
// over-fetch.
func TestPracticeStyleIndex_NarrowsServerSideByHubAndKind(t *testing.T) {
	f := &styleIndexStoreFake{corpus: []*knowledgev1.Node{
		styleRuleNode("rule-a", "no bare http client", nil),
		// A style rule under a DIFFERENT hub, and a non-style-rule under this
		// one. Both must be excluded by the server-side predicates.
		styleRuleNode("rule-elsewhere", "another hub's rule",
			map[string]string{kgtypes.MetaKeySourceHub: "hub-python"}),
		{Id: "prose-node", Type: "pattern", Summary: "an ordinary practice pattern",
			Metadata: map[string]string{kgtypes.MetaKeySourceHub: "hub-go"}},
	}}

	res := practiceStyleIndex(opCtx(), f.exec, queryArgs{
		Graph: "practice", Mode: "style_index", Source: "hub-go",
	})

	require.Len(t, f.plans, 1, "one page drains this corpus — no over-fetch-then-filter")
	preds := f.plans[0].GetSelection().GetMetadataPredicates()
	byKey := map[string]*knowledgev1.MetadataPredicate{}
	for _, p := range preds {
		byKey[p.GetKey()] = p
	}
	require.Contains(t, byKey, kgtypes.MetaKeySourceHub, "the hub narrows server-side")
	require.Contains(t, byKey, kgtypes.MetaKeyPracticeKind, "the kind narrows server-side")
	assert.Equal(t, "hub-go", byKey[kgtypes.MetaKeySourceHub].GetValue())
	assert.Equal(t, knowledgev1.MetadataPredicate_OP_EQ, byKey[kgtypes.MetaKeySourceHub].GetOp())
	assert.Equal(t, kgtypes.PracticeKindStyleRule, byKey[kgtypes.MetaKeyPracticeKind].GetValue())
	assert.Equal(t, knowledgev1.MetadataPredicate_OP_EQ, byKey[kgtypes.MetaKeyPracticeKind].GetOp())

	assert.Equal(t, []string{"rule-a"}, styleIndexIDs(styleIndexRowsOf(t, res)))
	assert.True(t, f.plans[0].GetSkipTotal(),
		"the drain reads no total — it pages until a short page instead")
}

// TestPracticeStyleIndex_ScopeNarrowingIsClientSide is the guard that catches a
// scope key moved into the predicate map, in BOTH directions: the plan carries
// no scope predicate, and the unscoped rule survives selectors that exclude the
// scoped one.
func TestPracticeStyleIndex_ScopeNarrowingIsClientSide(t *testing.T) {
	f := &styleIndexStoreFake{corpus: []*knowledgev1.Node{
		styleRuleNode("rule-unscoped", "applies everywhere", nil),
		styleRuleNode("rule-scoped", "applies to one repo and path", map[string]string{
			kgtypes.MetaKeyStyleScopeRepo:  "knowledge",
			kgtypes.MetaKeyStyleScopePaths: `["cmd/knowledge"]`,
		}),
	}}

	res := practiceStyleIndex(opCtx(), f.exec, queryArgs{
		Graph: "practice", Mode: "style_index", Source: "hub-go",
		Repo: "some-other-repo", PathPrefixes: []string{"totally/elsewhere.go"},
	})

	for _, p := range f.plans[0].GetSelection().GetMetadataPredicates() {
		assert.NotEqual(t, kgtypes.MetaKeyStyleScopeRepo, p.GetKey(),
			"a server-side predicate on the repo scope would drop every UNSCOPED rule")
		assert.NotEqual(t, kgtypes.MetaKeyStyleScopePaths, p.GetKey(),
			"a server-side predicate on the path scope would drop every UNSCOPED rule, "+
				"and would compare a prefix against the serialized JSON array besides")
	}
	assert.Equal(t, []string{"rule-unscoped"}, styleIndexIDs(styleIndexRowsOf(t, res)),
		"the unscoped rule is PRESENT under a non-matching repo and a non-matching path list; "+
			"the scoped one is ABSENT")
}

// TestPracticeStyleIndex_Drains seeds more than one page and pins that every
// rule past the page boundary reaches the render.
func TestPracticeStyleIndex_Drains(t *testing.T) {
	const total = styleIndexPageSize + 7
	corpus := make([]*knowledgev1.Node, 0, total)
	for i := range total {
		corpus = append(corpus, styleRuleNode(styleIndexPadID(i), "a rule", nil))
	}
	f := &styleIndexStoreFake{corpus: corpus}

	rows := styleIndexRowsOf(t, practiceStyleIndex(opCtx(), f.exec, queryArgs{
		Graph: "practice", Mode: "style_index", Source: "hub-go",
	}))

	assert.Len(t, rows, total,
		"every rule reaches the render — a capped loop would stop at %d", styleIndexPageSize)
	require.Len(t, f.plans, 2, "a corpus past one page costs two reads")
	assert.EqualValues(t, styleIndexPageSize, f.plans[1].GetOffset(),
		"the second page is offset by the first page's size")
}

// styleIndexPadID renders a sortable fixture id.
func styleIndexPadID(i int) string {
	s := "0000" + string(rune('0'+i%10))
	return "rule-" + s[len(s)-4:] + "-" + string(rune('a'+i%26))
}

// TestPracticeStyleIndex_RowShape pins the five columns, the never-truncated id
// and the capped summary, and that NOTHING ELSE is rendered.
func TestPracticeStyleIndex_RowShape(t *testing.T) {
	longSummary := strings.Repeat("s", styleIndexColumnCap+40)
	// AN ID LONGER THAN THE COLUMN CAP. A 32-character id sits UNDER the cap, so
	// a fixture using one cannot tell "the id is never capped" from "the cap
	// happened not to fire" — and a cap wrongly applied to the id column would
	// pass unobserved. This id is over the cap, so the guarantee is testable.
	longID := "style-rule-" + strings.Repeat("z", styleIndexColumnCap)
	f := &styleIndexStoreFake{corpus: []*knowledgev1.Node{
		styleRuleNode(longID, longSummary, map[string]string{
			"severity":                    "critical",
			kgtypes.MetaKeyStyleScopeRepo: "knowledge",
			kgtypes.MetaKeySisterCheck:    "sister-check-yyyyyyyyyyyyyyyyyyy",
			// The inert check shape a shaped rule carries. The index must render
			// none of it.
			"dsl_pattern":       "http.DefaultClient",
			"check_where":       `{"kind":{"of":"X","is":"identifier"}}`,
			"check_fixture_bad": "fixture-bad-id",
		}),
	}}

	rows := styleIndexRowsOf(t, practiceStyleIndex(opCtx(), f.exec, queryArgs{
		Graph: "practice", Mode: "style_index", Source: "hub-go",
	}))
	require.Len(t, rows, 1)
	cols := strings.Split(rows[0], "\t")
	require.Len(t, cols, 5, "id, severity, scope, summary, sister check")

	assert.Equal(t, longID, cols[0],
		"the ID IS NEVER TRUNCATED — a truncated id does not resolve")
	assert.Greater(t, len(cols[0]), styleIndexColumnCap,
		"the probe id is OVER the column cap, so this assertion can fail")
	assert.NotContains(t, cols[0], styleIndexEllipsis)
	assert.Equal(t, "critical", cols[1])
	assert.Equal(t, "repo=knowledge", cols[2])
	assert.Len(t, cols[3], styleIndexColumnCap, "the summary column is capped")
	assert.True(t, strings.HasSuffix(cols[3], styleIndexEllipsis))
	assert.Equal(t, "sister-check-yyyyyyyyyyyyyyyyyyy", cols[4],
		"the sister-check id is rendered whole, for the same reason the rule id is")

	body := rows[0]
	assert.NotContains(t, body, "http.DefaultClient", "the index renders NO pattern")
	assert.NotContains(t, body, "check_where", "the index renders NO where-tree")
	assert.NotContains(t, body, "fixture-bad-id", "the index renders NO fixtures")
	assert.NotContains(t, body, "full prose body", "the index renders NO rule prose")
}

// TestPracticeStyleIndex_RenderedBlockIsWithinBound asserts the declared bound
// over the rendered block rather than restating the number.
func TestPracticeStyleIndex_RenderedBlockIsWithinBound(t *testing.T) {
	long := strings.Repeat("界", 200)
	corpus := []*knowledgev1.Node{
		styleRuleNode("style-rule-zzzzzzzzzzzzzzzzzzzzz", long, map[string]string{
			// A MAXIMAL SEVERITY and a MAXIMAL SISTER-CHECK ID. This test held
			// both at their short fixture values while calling itself the
			// declared bound's check, so the two columns whose stored width is
			// corpus data were the two it never varied.
			"severity":                     strings.Repeat("W", 4000),
			kgtypes.MetaKeyStyleScopeRepo:  long,
			kgtypes.MetaKeyStyleScopePaths: `["` + strings.Repeat("p", 137) + `"]`,
			kgtypes.MetaKeySisterCheck:     "go:" + strings.Repeat("c", 4000),
		}),
		styleRuleNode("style-rule2-zzzzzzzzzzzzzzzzzzzz", "short", nil),
	}
	f := &styleIndexStoreFake{corpus: corpus}
	rows := styleIndexRowsOf(t, practiceStyleIndex(opCtx(), f.exec, queryArgs{
		Graph: "practice", Mode: "style_index", Source: "hub-go",
	}))
	require.Len(t, rows, 2)

	// THE IDS ARE NAMED, IN ROW ORDER. The block bound is a function of them,
	// and lifting them back off the rendered rows would let the subject supply
	// its own answer key. Row order is id order: styleIndexRows sorts by id.
	ids := []string{"style-rule-zzzzzzzzzzzzzzzzzzzzz", "style-rule2-zzzzzzzzzzzzzzzzzzzz"}
	assert.True(t, styleIndexBlockIsWithinBound(ids, rows),
		"the rendered block must be at or under the sum of its rows' own bounds, got %d over %d rows",
		len(styleIndexBody(rows)), len(rows))
	for i, r := range rows {
		assert.LessOrEqual(t, len(r), styleIndexRowBound(ids[i]),
			"every individual row is at or under the bound for ITS OWN id too")
	}
}

// TestPracticeStyleIndex_Params covers the arm's parameter classes.
func TestPracticeStyleIndex_Params(t *testing.T) {
	t.Run("a hub with no members is an EMPTY index, not an error", func(t *testing.T) {
		f := &styleIndexStoreFake{}
		res := practiceStyleIndex(opCtx(), f.exec, queryArgs{
			Graph: "practice", Mode: "style_index", Source: "hub-with-nothing-in-it",
		})
		require.False(t, res.IsError, "an empty hub is a legal state")
		assert.Equal(t, styleIndexEmpty, textBodyTools(res))
	})

	t.Run("source plus an explicit meta[source_hub] is refused", func(t *testing.T) {
		f := &styleIndexStoreFake{}
		res := practiceStyleIndex(opCtx(), f.exec, queryArgs{
			Graph: "practice", Mode: "style_index", Source: "hub-go",
			Meta: map[string]string{kgtypes.MetaKeySourceHub: "hub-python"},
		})
		require.True(t, res.IsError, "two spellings of one filter must be refused, not adjudicated")
		assert.Contains(t, toolResultText(res), kgtypes.MetaKeySourceHub)
		assert.Empty(t, f.plans, "a refused call issues no read")
	})

	t.Run("a contradicting meta[practice_kind] is refused", func(t *testing.T) {
		f := &styleIndexStoreFake{}
		res := practiceStyleIndex(opCtx(), f.exec, queryArgs{
			Graph: "practice", Mode: "style_index", Source: "hub-go",
			Meta: map[string]string{kgtypes.MetaKeyPracticeKind: "something-else"},
		})
		require.True(t, res.IsError)
		assert.Contains(t, toolResultText(res), "something-else")
		assert.Empty(t, f.plans, "a refused call issues no read")
	})

	t.Run("a legacy language renders an EMPTY index plus the legacy notice", func(t *testing.T) {
		f := &styleIndexStoreFake{}
		res := practiceStyleIndex(opCtx(), f.exec, queryArgs{
			Graph: "practice", Mode: "style_index", Source: "hub-go", Language: "go",
		})
		require.False(t, res.IsError, "a legacy read is not a refusal on any other practice arm either")
		body := textBodyTools(res)
		assert.Contains(t, body, styleIndexEmpty)
		assert.Contains(t, body, "LEGACY practice graph",
			"the legacy notice says which corpus answered")
		require.Len(t, f.plans, 1)
	})

	t.Run("path_prefix and path_prefixes are both routed", func(t *testing.T) {
		f := &styleIndexStoreFake{corpus: []*knowledgev1.Node{
			styleRuleNode("rule-a", "scoped to cmd", map[string]string{
				kgtypes.MetaKeyStyleScopePaths: `["cmd"]`,
			}),
			styleRuleNode("rule-b", "scoped to internal", map[string]string{
				kgtypes.MetaKeyStyleScopePaths: `["internal"]`,
			}),
		}}
		rows := styleIndexRowsOf(t, practiceStyleIndex(opCtx(), f.exec, queryArgs{
			Graph: "practice", Mode: "style_index", Source: "hub-go",
			PathPrefix: "cmd/x.go", PathPrefixes: []string{"internal/y.go"},
		}))
		assert.ElementsMatch(t, []string{"rule-a", "rule-b"}, styleIndexIDs(rows),
			"the singular and plural spellings are unioned, not one shadowing the other")
	})

	t.Run("json renders the same rows", func(t *testing.T) {
		f := &styleIndexStoreFake{corpus: []*knowledgev1.Node{
			styleRuleNode("rule-a", "a rule", nil),
		}}
		res := practiceStyleIndex(opCtx(), f.exec, queryArgs{
			Graph: "practice", Mode: "style_index", Source: "hub-go", Format: "json",
		})
		require.False(t, res.IsError)
		var payload struct {
			Rows []string `json:"rows"`
		}
		require.NoError(t, json.Unmarshal([]byte(textBodyTools(res)), &payload))
		require.Len(t, payload.Rows, 1)
		assert.Equal(t, "rule-a", strings.Split(payload.Rows[0], "\t")[0],
			"the json arm carries the SAME row the text arm renders — a sibling arm, not a second render")
	})
}

// TestPracticeStyleIndex_ArmIsClaimedAndAccounted drives the real entry point,
// so the arm's registration and its param accounting are observed rather than
// assumed.
func TestPracticeStyleIndex_ArmIsClaimedAndAccounted(t *testing.T) {
	fc := &fakeGraphCaller{}
	deps := interceptTestDeps{gc: fc}

	t.Run("the entry point claims mode=style_index", func(t *testing.T) {
		handled, res := InterceptQueryPracticeLinkage(opCtx(), deps, kgtools.CallToolParams{
			Name:      "query",
			Arguments: json.RawMessage(`{"graph":"practice","mode":"style_index","source":"hub-go"}`),
		})
		require.True(t, handled, "the practice entry point claims the index mode")
		assert.False(t, res.IsError, "a well-formed index call is served: %s", toolResultText(res))
	})

	t.Run("limit is REJECTED, naming the drain", func(t *testing.T) {
		_, res := InterceptQueryPracticeLinkage(opCtx(), deps, kgtools.CallToolParams{
			Name:      "query",
			Arguments: json.RawMessage(`{"graph":"practice","mode":"style_index","source":"hub-go","limit":5}`),
		})
		require.True(t, res.IsError, "a paging window on a draining read must be refused")
		assert.Contains(t, toolResultText(res), "DRAINS",
			"the refusal must say why, and name the scope narrowing that does work")
	})
}

// TestPracticeStyleIndex_AgreesWithTheScopedBrowse is requirement 4's ONE
// agreement assertion, plus the divergence that shows why the index exists.
func TestPracticeStyleIndex_AgreesWithTheScopedBrowse(t *testing.T) {
	scopedOnly := []*knowledgev1.Node{
		styleRuleNode("rule-a", "one", map[string]string{kgtypes.MetaKeyStyleScopeRepo: "knowledge"}),
		styleRuleNode("rule-b", "two", map[string]string{kgtypes.MetaKeyStyleScopeRepo: "knowledge"}),
		styleRuleNode("rule-c", "three", map[string]string{kgtypes.MetaKeyStyleScopeRepo: "agent"}),
	}

	idx := &styleIndexStoreFake{corpus: scopedOnly}
	indexIDs := styleIndexIDs(styleIndexRowsOf(t, practiceStyleIndex(opCtx(), idx.exec, queryArgs{
		Graph: "practice", Mode: "style_index", Source: "hub-go", Repo: "knowledge",
	})))

	br := &styleIndexStoreFake{corpus: scopedOnly}
	browseRes := practiceBrowse(opCtx(), br.exec, queryArgs{
		Graph: "practice", Source: "hub-go", Meta: map[string]string{
			kgtypes.MetaKeyPracticeKind:   kgtypes.PracticeKindStyleRule,
			kgtypes.MetaKeyStyleScopeRepo: "knowledge",
		},
	})
	require.False(t, browseRes.IsError, toolResultText(browseRes))
	browseBody := textBodyTools(browseRes)

	assert.ElementsMatch(t, []string{"rule-a", "rule-b"}, indexIDs)
	for _, id := range indexIDs {
		assert.Contains(t, browseBody, id,
			"the hub-scoped browse narrowed by the scope keys returns exactly what the index listed")
	}
	assert.NotContains(t, browseBody, "rule-c", "and nothing the index left out")

	// THE DIVERGENCE, so nobody reads the agreement above as "the browse is a
	// substitute for the index". Add ONE unscoped rule and the two answers part
	// company: the index lists it (an unscoped rule applies everywhere) and the
	// metadata-predicate browse cannot, because its predicate is an equality on a
	// key that rule does not carry.
	withUnscoped := append([]*knowledgev1.Node{
		styleRuleNode("rule-everywhere", "applies to every repo", nil),
	}, scopedOnly...)

	idx2 := &styleIndexStoreFake{corpus: withUnscoped}
	indexIDs2 := styleIndexIDs(styleIndexRowsOf(t, practiceStyleIndex(opCtx(), idx2.exec, queryArgs{
		Graph: "practice", Mode: "style_index", Source: "hub-go", Repo: "knowledge",
	})))
	assert.Contains(t, indexIDs2, "rule-everywhere",
		"the index lists the unscoped rule — that is the disjunction the predicates cannot express")

	br2 := &styleIndexStoreFake{corpus: withUnscoped}
	browseBody2 := textBodyTools(practiceBrowse(opCtx(), br2.exec, queryArgs{
		Graph: "practice", Source: "hub-go", Meta: map[string]string{
			kgtypes.MetaKeyPracticeKind:   kgtypes.PracticeKindStyleRule,
			kgtypes.MetaKeyStyleScopeRepo: "knowledge",
		},
	}))
	assert.NotContains(t, browseBody2, "rule-everywhere",
		"the scoped browse cannot reach it, which is why the index is a separate read")
}
