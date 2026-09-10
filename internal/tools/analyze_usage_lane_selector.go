// SPDX-License-Identifier: Apache-2.0

// analyze_usage_lane_selector.go — the scope:single selector's name arm, and the three
// distinguishable answers a run over the local cache can give.
//
// An operator knows a lane by the NAME it was spawned under; the cache keys it by
// "a<name>-<16 hex>". Accepting only the id made the commonest call a typo, and the typo's
// answer was the cold-cache hint — advice to re-seed a cache that was already full. The two
// halves below are that fix: resolve the name against the cache, and keep an empty
// POPULATION distinguishable from an empty CACHE.

package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/transcriptanalytics"
)

// resolveLaneSelector turns a scope:single agent NAME into the cache's own lane id, and
// returns (filters, _, false) to continue or (_, result, true) when the call is answered
// here. A cache lane id, any other scope, and an absent selector all pass through untouched,
// so the id form's report is unchanged byte for byte.
//
// ORDER IS THE CONTRACT. The cache's lane count is consulted BEFORE the name: on an empty
// cache no name can resolve, and answering "no lane named X" there would tell an operator to
// fix a selector when the real state is a cache that has never been populated. The hint owns
// that case; a name refusal owns only a cache that holds lanes.
//
// The resolved id is written into AgentID and the resolution-scope session is cleared, so
// what reaches the analyzer is exactly the by-id call: the report's selector renders the id
// and the two calls agree by construction rather than by a second code path kept in step.
func resolveLaneSelector(
	ctx context.Context, analyzer UsageAnalyzerAPI, f transcriptanalytics.Filters,
) (transcriptanalytics.Filters, kgtools.ToolResult, bool) {
	if f.Scope != transcriptanalytics.ScopeSingle || f.AgentID == "" || transcriptanalytics.IsLaneID(f.AgentID) {
		return f, kgtools.ToolResult{}, false
	}
	name, session := f.AgentID, f.SessionID
	res, err := analyzer.ResolveLaneName(ctx, name, session)
	if err != nil {
		return f, errorResult("analyze_usage: " + err.Error()), true
	}
	if res.LaneCount == 0 {
		return f, textResult(coldCacheHint), true
	}
	switch len(res.Candidates) {
	case 0:
		return f, errorResult(emptyPopulationRefusal(
			string(transcriptanalytics.ScopeSingle), laneSearchPhrase(name, session), res.LaneCount, 0)), true
	case 1:
		f.AgentID = res.Candidates[0].AgentID
		f.SessionID = ""
		return f, kgtools.ToolResult{}, false
	default:
		return f, errorResult(ambiguousLaneNameRefusal(name, session, res.Candidates)), true
	}
}

// emptyPopulationResult renders the answer for a report that folded no session, splitting the
// one branch that used to serve both cases.
//
// lane_count comes from the cache glob before any row filtering, so it says whether the CACHE
// is empty; the report's own emptiness says whether the POPULATION is. Zero lanes is the cold
// cache and keeps the --seed hint. Lanes with an empty population is a selector that matched
// nothing, which is a caller error and is refused as one — naming the scope, what was
// searched and the corpus block, so the three answers are told apart by their text.
func emptyPopulationResult(r *transcriptanalytics.DetectorReport) kgtools.ToolResult {
	if r == nil || r.Corpus.LaneCount == 0 {
		return textResult(coldCacheHint)
	}
	return errorResult(emptyPopulationRefusal(
		r.Corpus.Scope, reportSearchPhrase(r.Corpus.Scope, r.Corpus.Selector), r.Corpus.LaneCount, r.Corpus.RecordCount))
}

// emptyPopulationRefusal is the one wording for "the cache holds lanes and your selection is
// empty", used by both the resolver and the report path so the two cannot drift into two
// different explanations of one state. The corpus numbers are the report's own, never
// recomputed.
func emptyPopulationRefusal(scope, searched string, laneCount, recordCount int64) string {
	return fmt.Sprintf("analyze_usage: scope %q matched no records. Searched: %s. "+
		"Corpus: lane_count=%d, record_count=%d — the local cache holds lanes, so this selection "+
		"matched none of them rather than the cache being empty.", scope, searched, laneCount, recordCount)
}

// reportSearchPhrase names what a completed run narrowed by, from the selector the report
// already rendered. A scope that narrows by nothing says so rather than showing an empty
// pair of quotes.
func reportSearchPhrase(scope, selector string) string {
	if selector == "" {
		return fmt.Sprintf("the whole retained cache (scope %q takes no selector)", scope)
	}
	return fmt.Sprintf("the selector or bounds %q", selector)
}

// laneSearchPhrase names what a name lookup searched, including the scope it searched within
// — the session when one narrowed it, the whole cache otherwise. Without the scope, "no lane
// named planner" reads as a claim about the cache when it may be a claim about one session.
func laneSearchPhrase(name, session string) string {
	if session != "" {
		return fmt.Sprintf("the lane name %q in session %q", name, session)
	}
	return fmt.Sprintf("the lane name %q across the whole cache", name)
}

// ambiguousLaneNameRefusal lists every lane a name matched, one per line with its sessions.
//
// A name is reused across sessions while a lane's cache id is unique, so choosing one would
// analyze a different lane than the caller named and say nothing about having chosen. The
// candidates are relayed instead, which is also the answer: either id, pasted back, resolves.
func ambiguousLaneNameRefusal(name, session string, candidates []transcriptanalytics.LaneCandidate) string {
	var b strings.Builder
	fmt.Fprintf(&b, "analyze_usage: the lane name %q matches %d lanes %s. A lane name is reused across "+
		"sessions while a lane's cache id is unique, so this call is refused rather than resolved to one "+
		"of them. Re-run with one of these ids as agent, or narrow with session:",
		name, len(candidates), laneScopePhrase(session))
	for _, c := range candidates {
		fmt.Fprintf(&b, "\n  agent %q in session %s", c.AgentID, quotedList(c.SessionIDs))
	}
	return b.String()
}

// laneScopePhrase names the population a lookup ran over, for a message that already names
// the name it looked up.
func laneScopePhrase(session string) string {
	if session != "" {
		return fmt.Sprintf("in session %q", session)
	}
	return "in the local cache"
}

// quotedList renders a lane's parent sessions. All of them are listed: a lane whose rows name
// more than one session is still one lane, and printing only the first would hide that.
func quotedList(values []string) string {
	quoted := make([]string, 0, len(values))
	for _, v := range values {
		quoted = append(quoted, fmt.Sprintf("%q", v))
	}
	return strings.Join(quoted, ", ")
}
