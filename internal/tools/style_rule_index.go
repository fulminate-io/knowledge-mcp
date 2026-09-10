// SPDX-License-Identifier: Apache-2.0

// style_rule_index.go — the compact style-rule index read,
// query(graph:"practice", mode:"style_index").
//
// WHAT IT IS FOR. A lane about to write code under a repo and a set of paths
// needs to know which style rules bind there, and it needs that as a list it can
// scan rather than as the rules themselves: the prose is read on demand, by id,
// once a row looks relevant. So this render is a DIFFERENT SERIALIZATION rather
// than a filtered browse — one tab-separated line per rule, with all FOUR of its
// variable-width columns capped (severity, scope, summary and the sister-check
// id), so the block's size is a property of this renderer plus the ids it
// resolves. The ids are the one term the corpus supplies, which is why the row
// bound is a FUNCTION of the id rather than a figure that budgets one. That is
// the shape corpusscan.CompactLine established for the same problem one layer
// down.
//
// THE NARROWING IS SPLIT, AND WHERE EACH HALF RUNS IS THE DESIGN. The hub and
// the practice kind are SERVER-SIDE metadata predicates on one drained browse.
// The repo and path scope is CLIENT-SIDE, because a rule's scope is optional and
// the selection is therefore "scope absent OR scope matches" — a disjunction the
// conjunctive metadata predicates cannot express. style_rule.go's
// styleScopeMatches carries the full reasoning.

package tools

import (
	"context"
	"fmt"
	"maps"
	"sort"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/corpus"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// styleIndexPageSize is the drain's page size, the same 1000 practiceHubMemberIDs
// pages at and for the same reason: a browse that supplies no limit is stamped
// with the LLM-facing default, so a loader in that shape reads only the first
// page and the index silently omits every rule past it.
const styleIndexPageSize = 1000

// styleIndexEmpty is the zero-row body. An index that matched nothing renders
// this one line rather than nothing at all, so an empty answer is
// distinguishable from a call that produced no output.
const styleIndexEmpty = "No style rules match."

// practiceStyleIndex serves the style-rule index arm.
func practiceStyleIndex(ctx context.Context, exec engine.ExecuteFn, a queryArgs) kgtools.ToolResult {
	meta, merr := styleIndexPredicates(a)
	if merr != nil {
		return errorResult("practice style_index: " + merr.Error())
	}
	nodes, err := drainStyleRules(ctx, exec, a.Language, meta)
	if err != nil {
		return errorResult("practice style_index: " + err.Error())
	}
	rows, rerr := styleIndexRows(nodes, a.Repo, styleIndexPaths(a))
	if rerr != nil {
		return errorResult("practice style_index: " + rerr.Error())
	}
	if a.Format == "json" {
		return jsonResult(map[string]any{
			"graph": "practice", "mode": "style_index",
			"source": a.Source, "repo": a.Repo, "rows": rows,
		})
	}
	res := textResult(styleIndexBody(rows))
	// THE LEGACY NOTICE, ON THE SAME TERMS EVERY OTHER PRACTICE READ CARRIES IT.
	// A `language` here addresses one of the pre-singleton graphs, which hold no
	// style rules, so the honest answer is an empty index that says which corpus
	// answered — not a refusal this arm invents for itself while every sibling
	// arm accepts the selector.
	if a.Language != "" && a.Format != "json" {
		res = appendNotice(res, practiceLegacyNotice(a.Language))
	}
	return res
}

// styleIndexPredicates builds the arm's SERVER-SIDE narrowing: the caller's meta
// map with the hub folded in and the style-rule kind pinned.
//
// THE HUB COLLISION IS REFUSED BY practiceMetaWithHub, the same function the
// browse arm uses, so `source` and an explicit meta[source_hub] stay one filter
// with one message. The KIND collision is refused here on the same principle: a
// caller naming practice_kind on the arm whose whole identity is that key has
// asked two questions at once, and picking a winner would either return rows the
// index has no render for or silently ignore what the caller asked.
func styleIndexPredicates(a queryArgs) (map[string]string, error) {
	meta, err := practiceMetaWithHub(a.Meta, a.Source)
	if err != nil {
		return nil, err
	}
	if existing, ok := meta[kgtypes.MetaKeyPracticeKind]; ok && existing != kgtypes.PracticeKindStyleRule {
		return nil, fmt.Errorf(
			"mode=\"style_index\" pins meta[%s]=%q and this call also supplied meta[%s]=%q - "+
				"they are two answers to one question; drop the meta key, or use the browse arm to read another practice kind",
			kgtypes.MetaKeyPracticeKind, kgtypes.PracticeKindStyleRule,
			kgtypes.MetaKeyPracticeKind, existing)
	}
	// SIZED FROM ONE LENGTH, NOT A SUM: a make() capacity is a hint —
	// the one practice-kind key costs at most a growth — while a size built by
	// addition is a value the allocator has to take on trust.
	out := make(map[string]string, len(meta))
	maps.Copy(out, meta)
	out[kgtypes.MetaKeyPracticeKind] = kgtypes.PracticeKindStyleRule
	return out, nil
}

// styleIndexPaths folds the singular and plural path selectors into one list.
// Both spellings are accepted for the same reason the file_symbols arm accepts
// both: a caller with one path should not have to wrap it.
func styleIndexPaths(a queryArgs) []string {
	if a.PathPrefix == "" {
		return a.PathPrefixes
	}
	return append([]string{a.PathPrefix}, a.PathPrefixes...)
}

// drainStyleRules pages the hub-and-kind browse to exhaustion.
//
// IT DRAINS, AND A PARTIAL DRAIN IS AN ERROR RATHER THAN A SHORT SET. This is
// practiceHubMemberIDs's loop with one more predicate: an index a reader trusts
// to say which rules bind cannot quietly stop at a page boundary, because the
// rules past it become rules nobody was told about. A page that cannot be read
// fails loudly.
func drainStyleRules(
	ctx context.Context, exec engine.ExecuteFn, language string, meta map[string]string,
) ([]*knowledgev1.Node, error) {
	var out []*knowledgev1.Node
	for offset := 0; ; offset += styleIndexPageSize {
		resp, err := exec(ctx, &knowledgev1.ExecuteRequest{
			Plan: &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{
				Selection: &knowledgev1.Selection{
					MetadataPredicates: engine.LowerMetaPredicates(meta),
				},
				Limit:     int32(styleIndexPageSize),
				Offset:    int32(offset),
				SkipTotal: true,
			}},
			Target: practiceReadTarget(language),
		})
		if err != nil {
			return nil, fmt.Errorf("read style rules: %w", err)
		}
		nodes, derr := engine.DecodeNodes(resp)
		if derr != nil {
			return nil, fmt.Errorf("read style rules: %w", derr)
		}
		out = append(out, nodes...)
		if len(nodes) < styleIndexPageSize {
			return out, nil
		}
	}
}

// styleIndexRows applies the client-side scope filter and renders the survivors,
// sorted by id so two runs over one corpus produce the same block.
func styleIndexRows(nodes []*knowledgev1.Node, repo string, paths []string) ([]string, error) {
	kept := make([]*knowledgev1.Node, 0, len(nodes))
	for _, n := range nodes {
		sc, err := styleScopeFromMetadata(n.GetMetadata())
		if err != nil {
			return nil, fmt.Errorf("style rule %s: %w", n.GetId(), err)
		}
		if styleScopeMatches(sc, repo, paths) {
			kept = append(kept, n)
		}
	}
	sort.Slice(kept, func(i, j int) bool { return kept[i].GetId() < kept[j].GetId() })
	rows := make([]string, 0, len(kept))
	for _, n := range kept {
		sc, _ := styleScopeFromMetadata(n.GetMetadata()) // already parsed above
		rows = append(rows, styleIndexRow(n, sc))
	}
	return rows, nil
}

// styleIndexRow renders ONE rule as its line:
//
//	<id>\t<severity>\t<scope>\t<summary, capped>\t<sister check id or empty>
//
// THE ROW'S OWN ID IS NEVER CAPPED. A truncated id does not resolve, and that id
// is the whole point of the row — every other column exists to help a reader
// decide whether to fetch it. Every other column IS capped, and each is
// recoverable in full from the by-id lookup, which is what makes capping it cost
// nothing.
//
// THE SEVERITY IS RENDERED AS STORED, not re-validated, and its WIDTH IS CAPPED
// ALL THE SAME. The ladder is enforced where a rule is WRITTEN; refusing a read
// because one stored value is off the ladder would make the whole index
// unreadable over a corpus that already holds the older practice-side spellings.
// Capping is not validating: a read that admits arbitrary VALUES has no basis
// for assuming a bounded WIDTH.
//
// THE COLUMN CENSUS for this render, which the width tests drive one column at a
// time (style_rule_index_columns_test.go):
//
//	column        capped?              why
//	id            UNCAPPED BY DESIGN   a truncated id does not resolve, and resolving it is what the row is for; StyleIndexRowBound is a FUNCTION of it and budgets nothing
//	severity      CAPPED               rendered as stored, never ladder-validated, so its width is corpus data
//	scope         CAPPED               a repo name and JSON path array, both caller-supplied
//	summary       CAPPED               the server admits a 500-rune summary, up to 2000 bytes in one column
//	sister_check  CAPPED               a check id admitted through the mutate entry point is caller-supplied and namespaced `<language>:`, and the rule's by-id read returns it in full
func styleIndexRow(n *knowledgev1.Node, sc styleRuleScope) string {
	md := n.GetMetadata()
	return strings.Join([]string{
		n.GetId(),
		capStyleColumn(md[corpus.MetaSeverity]),
		capStyleColumn(styleScopeColumn(sc)),
		capStyleColumn(n.GetSummary()),
		capStyleColumn(md[kgtypes.MetaKeySisterCheck]),
	}, "\t")
}

// styleIndexBody joins the rows, or renders the empty state.
func styleIndexBody(rows []string) string {
	if len(rows) == 0 {
		return styleIndexEmpty
	}
	return strings.Join(rows, "\n")
}

// styleIndexBlockBound is the declared byte bound for a rendered index block, as
// a FUNCTION of the ids its rows carry.
//
// IT TAKES THE IDS RATHER THAN THE ROWS, and that is the point of the signature.
// Every other term of a row is capped by this render; the id is not, and lifting
// it back off the rendered row would let the subject supply its own answer key.
// The caller names the ids it rendered and the bound is the sum of each row's
// own bound.
//
// THE ZERO-ROW RENDER IS MEASURED AGAINST ITS OWN BOUND, and that arm is why
// this is not a one-liner. A render with no rows is not an empty string — it is
// styleIndexEmpty, one line, deliberately, so an empty answer stays
// distinguishable from a call that produced no output. Measuring that line
// against a sum over zero rows gives it a budget of ZERO and reports a
// legitimate render as over budget, which made this helper answer FALSE for the
// FIRST input class the render defines while no assertion was asking it.
func styleIndexBlockBound(ids []string) int {
	if len(ids) == 0 {
		return styleIndexEmptyBound
	}
	bound := 0
	for _, id := range ids {
		bound += styleIndexRowBound(id)
	}
	return bound
}

// styleIndexBlockIsWithinBound reports whether a rendered block is at or under
// its declared bound. It exists so the bound is computed in one place rather
// than in each assertion that reads it, exactly as corpusscan's
// compactLineIsWithinBound does for its own block.
//
// ids ARE THE IDS OF rows, IN ORDER. They are passed rather than parsed back out
// of the rows for the reason styleIndexBlockBound documents.
func styleIndexBlockIsWithinBound(ids, rows []string) bool {
	return len(styleIndexBody(rows)) <= styleIndexBlockBound(ids)
}
