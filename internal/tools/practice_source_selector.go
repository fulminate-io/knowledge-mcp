// SPDX-License-Identifier: Apache-2.0

// practice_source_selector.go — the practice family's two selectors, in one
// place: the `source` HUB narrowing that replaced per-language graphs, and the
// LEGACY `language` read that still reaches the pre-singleton ones.
//
// WHY THEY SIT TOGETHER. They are the same question answered before and after
// the practice graphs were combined — "which subset of the practice corpus does
// this read address" — and every practice arm has to ask both. Splitting them
// across the arms is what produced the partition copies this change spent its
// budget removing.
//
// NEITHER TOUCHES THE WIRE SHAPE. `source` lowers onto a metadata predicate on
// the node, and `language` rides the selector field that already exists.
// GraphSelector is byte-identical before and after this change.

package tools

import (
	"context"
	"errors"
	"fmt"
	"maps"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/graphsel"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// practiceReadTarget builds the wire selector for a practice READ.
//
// An empty language addresses the ONE combined graph, and graphsel is what says
// so — the family is FieldNone now, so GraphSelectorFor puts no instance field
// on the selector at all and the server's practice arm opens "default".
//
// A NON-EMPTY LANGUAGE IS THE LEGACY SELECTOR and is the one thing this function
// adds on top: it names one of the eight pre-singleton graphs, and the server
// still resolves it AS GIVEN on a read. It is set here rather than through
// graphsel deliberately — graphsel answers "which field does this FAMILY use",
// and the answer for practice is now "none". This is a per-READ legacy override,
// not a family fact, and writing it through graphsel would put the retired
// partition back into the one file that stopped holding it.
//
// A WRITE NEVER CALLS THIS. `language` is refused on every practice write arm,
// and the server refuses it again in resolvePractice's ForWrite fence.
func practiceReadTarget(language string) *knowledgev1.GraphSelector {
	sel := graphsel.GraphSelectorFor(kgtypes.GraphPractice, "", false)
	if language != "" {
		sel.Language = language
	}
	return sel
}

// practiceWriteTarget builds the wire selector for a practice WRITE. It is
// always the combined graph: there is no legacy override, by construction rather
// than by omission.
func practiceWriteTarget() *knowledgev1.GraphSelector {
	return graphsel.GraphSelectorFor(kgtypes.GraphPractice, "", false)
}

// practiceMetaWithHub returns the caller's metadata predicates with the hub
// narrowing folded in.
//
// IT NO LONGER REPORTS WHETHER A HUB WAS NAMED. That second result was declared
// and never read: both callers discarded it, and a caller that needs the fact
// already holds the `source` value it would have been derived from.
//
// THE HUB IS A METADATA PREDICATE, WHICH IS WHY NO WIRE FIELD MOVED. The browse
// arm already lowers its meta map through engine.LowerMetaPredicates, and the
// server already resolves metadata predicates on both flavors and on the delete
// path, so a hub-scoped browse and a hub-scoped delete are the existing
// mechanism carrying one more key.
//
// IT REFUSES A COLLISION RATHER THAN PICKING A WINNER. A caller that supplies
// BOTH `source` and an explicit meta predicate on the hub key has asked two
// questions at once, and silently preferring either one is a coercion. The
// error names both spellings so the caller can drop the one it did not mean.
func practiceMetaWithHub(meta map[string]string, hub string) (map[string]string, error) {
	if hub == "" {
		return meta, nil
	}
	if existing, ok := meta[kgtypes.MetaKeySourceHub]; ok {
		return nil, fmt.Errorf(
			"source=%q and meta[%s]=%q both narrow by the source hub - they are two spellings of one filter and this call supplied both; drop whichever you did not mean",
			hub, kgtypes.MetaKeySourceHub, existing)
	}
	// SIZED FROM ONE LENGTH, NOT A SUM: a make() capacity is a hint —
	// the one hub key costs at most a growth — while a size built by
	// addition is a value the allocator has to take on trust.
	out := make(map[string]string, len(meta))
	maps.Copy(out, meta)
	out[kgtypes.MetaKeySourceHub] = hub
	return out, nil
}

// practiceLegacyNotice is the marker requirement 5 asks a legacy read to carry.
//
// IT IS APPENDED TO THE BODY RATHER THAN REPLACING ANYTHING, because the point
// is that the CONTENT is unchanged: a legacy read returns exactly what it
// returned before the graphs were combined, and the response says which corpus
// answered. The render header carries the same fact in one word
// (engine.queryGraphLabelFor renders "practice:<language>"); this is the
// sentence a human reads.
func practiceLegacyNotice(language string) string {
	return fmt.Sprintf(
		"_Read from the LEGACY practice graph %q. Practice is one combined graph now; `language` addresses the pre-singleton graphs and is read-only. "+
			"Omit it to read the combined graph, or pass `source:<hub id>` to narrow it._", language)
}

// practiceHubMemberIDs resolves the node ids grouped under one source hub.
//
// IT IS A SEPARATE READ BEFORE THE SEARCH, AND THAT IS THE DESIGN. The segment
// index carries an external id and five text fields and no node metadata at all,
// so the hub key cannot be asked of it; the alternative was to widen what a
// segment stores, which is a FORMAT change that invalidates every shipped
// practice segment and forces a rebuild of graphs this ticket must leave
// untouched. One metadata-predicate browse costs one wire read against a
// membership bounded by the graph's own size.
//
// IT DRAINS, AND A PARTIAL DRAIN IS AN ERROR RATHER THAN A SHORT SET. A browse
// that supplies no limit is stamped with the LLM-facing default, so a loader in
// that shape reads only the first page — and here a short page is worse than a
// short answer: the missing ids become nodes the accept predicate silently
// rejects, and the search reports a confident subset of the hub the caller asked
// for. So the pages are drained and a page that cannot be read fails loudly.
func practiceHubMemberIDs(ctx context.Context, exec engine.ExecuteFn, language, hub string) (map[string]bool, error) {
	const pageSize = 1000

	members := map[string]bool{}
	for offset := 0; ; offset += pageSize {
		resp, err := exec(ctx, &knowledgev1.ExecuteRequest{
			Plan: &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{
				Selection: &knowledgev1.Selection{
					MetadataPredicates: engine.LowerMetaPredicates(map[string]string{kgtypes.MetaKeySourceHub: hub}),
				},
				Limit:     int32(pageSize),
				Offset:    int32(offset),
				SkipTotal: true,
			}},
			Target: practiceReadTarget(language),
		})
		if err != nil {
			return nil, fmt.Errorf("resolve source hub %q membership: %w", hub, err)
		}
		nodes, derr := engine.DecodeNodes(resp)
		if derr != nil {
			return nil, fmt.Errorf("resolve source hub %q membership: %w", hub, derr)
		}
		for _, n := range nodes {
			members[n.GetId()] = true
		}
		if len(nodes) < pageSize {
			return members, nil
		}
	}
}

// practiceRankedHits runs the practice ranked search, hub-scoped when a hub is
// named and whole-graph otherwise.
//
// THE HUB NARROWING IS A PREDICATE THE ENGINE APPLIES DURING TOP-K, not a filter
// over the result. That is what makes a small hub return its full top-N instead
// of whatever few of its members placed in a corpus-wide ranking, and it is why
// the seam exists rather than the caller trimming.
//
// AN EMPTY HUB IS AN EMPTY RESULT, NOT AN ERROR, and that is requirement 8
// rather than a soft edge. A hub with no members is a legal state — an empty
// catalog, or a collection that has not landed yet — and nothing sweeps it, so a
// search scoped to one legitimately matches nothing.
//
// IT IS ALSO WHAT KEEPS THIS ARM CONSISTENT WITH THE BROWSE. A hub-scoped browse
// carrying an unknown hub id returns zero rows rather than erroring, because the
// hub rides a metadata predicate there; making the ranked arm error on the same
// input would give one selector two behaviors depending on whether the caller
// also supplied text. The zero is qualified by practiceZeroHitNotice on the way
// out, which is the mechanism that distinguishes an empty corpus from a missing
// index for every other practice search.
func practiceRankedHits(
	ctx context.Context, deps ClientDeps, mgr SegmentSearcher,
	pool, hub, query string, queryVec []byte, k int,
) ([]searchengine.Hit, error) {
	if hub == "" {
		hits, err := mgr.Search(ctx, kgtypes.GraphPractice, pool, query, queryVec, k)
		if err != nil {
			return nil, fmt.Errorf("client engine: %w", err)
		}
		return hits, nil
	}

	subset, ok := mgr.(SegmentSubsetSearcher)
	if !ok {
		return nil, fmt.Errorf(
			"source=%q narrows the ranked search inside the segment engine, and this engine does not offer the subset seam; drop `source` to search the whole practice graph, or browse by hub with query(graph:\"practice\", source:%q, type:...)",
			hub, hub)
	}

	members, err := practiceHubMemberIDs(ctx, deps.GraphCaller().Execute, "", hub)
	if err != nil {
		return nil, err
	}
	hits, err := subset.SearchAccepting(ctx, kgtypes.GraphPractice, pool, query, queryVec, k,
		func(id searchengine.ExternalID) bool { return members[id] })
	if err != nil {
		return nil, fmt.Errorf("client engine: %w", err)
	}
	return hits, nil
}

// practiceHubParamFree and practiceHubParamOnWrites are the two spellings the
// hub selector ships under, and which one an arm publishes is a property of that
// arm's schema rather than a choice.
//
// `source` is the spelling wherever the name is FREE — the query arms, traverse
// and the delete tool. It is taken on two schemas and cannot be reused there: on
// mutate, `source` is the node's own provenance field, which requirement 2
// leaves untouched; on search, `source` selects a logs provider. Those arms
// publish `source_hub`. A refusal that names the wrong one sends the caller to a
// param its arm does not declare, so the message follows the arm.
const (
	practiceHubParamFree     = "source"
	practiceHubParamOnWrites = "source_hub"
)

// practiceLanguageRefusedOnWrite renders the requirement-4 refusal: `language`
// on a practice WRITE arm, naming the hub selector under the spelling that arm
// publishes.
//
// IT REFUSES RATHER THAN IGNORING, and the difference is the whole point. A
// dropped language would land the write in the combined graph while the caller
// believed it had written to practice/go — a silent redirect the caller has no
// way to see. Bad input errors here.
//
// It names BOTH replacements because a caller reaching for `language` wants one
// of two different things: to group the write under an origin (the hub param),
// or to read one of the pre-singleton graphs (`language`, still accepted on
// reads).
//
// AND IT STATES THE HUB PARAM'S SECOND MEANING, because this one message serves
// every practice write arm and the param does not mean the same thing on all of
// them: a create GROUPS its node under the hub, while a link or an unlink SCOPES
// its endpoints to one (practice_hub_endpoints.go). A caller sent to `source_hub`
// from an edge arm by a sentence that only describes grouping would reasonably
// expect the edge to be grouped. The target arms — update, update_batch,
// bulk_update_metadata and an upsert of an existing node — read it the third way,
// as a scope over the ids the call names, and the message states that too.
func practiceLanguageRefusedOnWrite(hubParam string) string {
	return "graph:\"practice\" does not accept `language` on a write: " +
		"practice is ONE combined graph now, so a write has no per-language graph to land in. " +
		"Group the write under its origin instead — pass " + hubParam + ":\"<hub id>\", " +
		"where the hub is a node of type \"source\" you create once per origin. " +
		"On a link or an unlink that same param SCOPES THE ENDPOINTS rather than grouping anything: both " +
		"endpoints must already be grouped under the hub it names. " +
		"On an update, an update_batch, a bulk_update_metadata or an upsert of an existing node it SCOPES THE " +
		"TARGETS the same way: every id the call names must already be grouped under that hub. " +
		"`language` remains accepted on practice READ arms as the read-only selector for the pre-singleton graphs"
}

// refusePracticeLanguageOnWrite is the one gate every practice write arm calls.
//
// ONE FUNCTION RATHER THAN A CHECK PER ARM, because the arms are the population
// a reviewer has to enumerate to know the rule holds, and a rule spelled once is
// a rule they can find. It self-filters on the family so a caller can drop it in
// front of a shared arm without the arm knowing which graph it serves.
//
// EVERY WRITE ARM MEANS EVERY ONE. The arms that CLAIM a practice CRUD mutation
// are not the population: upsert, unlink, update_batch and bulk_update_metadata
// decline past that claim to the non-knowledge fallthrough, and the `delete`
// tool never touches the mutate paths at all. Each of those dropped the field
// and compiled a target with no language on it, which is the silent redirect
// this refusal exists to prevent, so the gate sits on the fallthrough and on the
// delete tool's own guard as well.
func refusePracticeLanguageOnWrite(graph, language, hubParam string) error {
	if graph != string(kgtypes.GraphPractice) || language == "" {
		return nil
	}
	return errors.New(practiceLanguageRefusedOnWrite(hubParam))
}
