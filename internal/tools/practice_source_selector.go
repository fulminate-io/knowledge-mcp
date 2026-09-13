// SPDX-License-Identifier: Apache-2.0

// practice_source_selector.go — the practice family's ONE selector, the `source`
// HUB narrowing, and the refusal of the `language` selector it replaced.
//
// WHY THEY SIT TOGETHER. They are the same question answered before and after
// the practice graphs were combined — "which subset of the practice corpus does
// this read address" — and every practice arm has to ask it. `language` used to
// be the other half of the answer: it named one of eight instance-keyed practice
// graphs, read-only, after the corpus was combined. Those graphs were retired,
// so the field names nothing and every arm refuses it here instead.
//
// NEITHER TOUCHED THE WIRE SHAPE. `source` lowers onto a metadata predicate on
// the node, and `language` rode a selector field that already existed and still
// exists. GraphSelector is byte-identical across both changes.

package tools

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"sort"
	"sync"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
	"github.com/fulminate-io/knowledge-mcp/internal/graphsel"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// practiceTarget builds the wire selector for a practice read or write. It is
// always the ONE combined graph, and graphsel is what says so — the family is
// FieldNone, so GraphSelectorFor puts no instance field on the selector at all
// and the server's practice arm opens "default".
//
// THERE IS NO SECOND BUILDER, and the absence is the change. A practiceReadTarget
// used to sit beside this one and take a language, setting GraphSelector.Language
// for a read of one of the eight instance-keyed graphs — a per-read override of
// the family fact graphsel declares. The graphs were retired, so the override has
// nothing to address and the two builders collapsed into this one. A read and a
// write compose the same selector now, which is what makes "practice is one
// graph" a property of the builder rather than of each arm's discipline.
func practiceTarget() *knowledgev1.GraphSelector {
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
func practiceHubMemberIDs(ctx context.Context, exec engine.ExecuteFn, hub string) (map[string]bool, error) {
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
			Target: practiceTarget(),
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
	if destinations := graphclient.SearchDestinations(ctx); len(destinations) > 1 && hub != "" {
		results := make([][]searchengine.Hit, len(destinations))
		errors := make([]error, len(destinations))
		var workers sync.WaitGroup
		for i, destination := range destinations {
			workers.Go(func() {
				leg := graphclient.WithDestination(graphclient.WithSearchDestinations(ctx, []graphclient.Destination{destination}), destination)
				results[i], errors[i] = practiceRankedHits(leg, deps, mgr, pool, hub, query, queryVec, k)
			})
		}
		workers.Wait()
		var hits []searchengine.Hit
		for i, result := range results {
			if errors[i] != nil {
				return nil, errors[i]
			}
			hits = append(hits, result...)
		}
		sort.Slice(hits, func(i, j int) bool {
			if hits[i].Score == hits[j].Score {
				return hits[i].ID < hits[j].ID
			}
			return hits[i].Score > hits[j].Score
		})
		if k > 0 && len(hits) > k {
			hits = hits[:k]
		}
		return hits, nil
	}
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

	members, err := practiceHubMemberIDs(ctx, deps.GraphCaller().Execute, hub)
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

// practiceLanguageRefusedLead is the RULE, spelled once. Both renderers below
// open with it, so a caller cannot tell from the first sentence which gate
// caught them — and a reader auditing the rule finds one statement of it rather
// than two that have to be compared.
const practiceLanguageRefusedLead = "graph:\"practice\" does not accept `language`: " +
	"practice is ONE combined graph now, so there is no per-language graph to address — " +
	"on a read or on a write. "

// practiceLanguageRefusedOnRead renders the refusal for a practice READ arm,
// naming the hub selector under the spelling that arm publishes.
//
// IT REFUSES RATHER THAN IGNORING, on the same reasoning the write half records:
// a dropped language would serve the combined graph while the caller believed it
// had read practice/go, a silent redirect the caller has no way to see. The
// field was the read-only selector for eight instance-keyed practice graphs
// until those were retired; it addresses nothing now, so bad input errors here.
//
// THE TAIL DIFFERS FROM THE WRITE'S BECAUSE THE ACTION DOES. A reader reaching
// for `language` wants a narrower corpus, and the answer is to omit the selector
// or to name a hub; the write's tail is about where a node LANDS, which a read
// has no version of.
func practiceLanguageRefusedOnRead(hubParam string) string {
	return practiceLanguageRefusedLead +
		"Omit it to read the whole practice corpus, or narrow to one origin with " +
		hubParam + ":\"<hub id>\", where the hub is a node of type \"source\" grouping the " +
		"nodes one origin contributed"
}

// practiceLanguageRefusedOnWrite renders the refusal for a practice WRITE arm,
// naming the hub selector under the spelling that arm publishes.
//
// IT STATES THE HUB PARAM'S THREE MEANINGS, because this one message serves every
// practice write arm and the param does not mean the same thing on all of them: a
// create GROUPS its node under the hub, while a link or an unlink SCOPES its
// endpoints to one (practice_hub_endpoints.go). A caller sent to `source_hub`
// from an edge arm by a sentence that only describes grouping would reasonably
// expect the edge to be grouped. The target arms — update, update_batch,
// bulk_update_metadata and an upsert of an existing node — read it the third way,
// as a scope over the ids the call names, and the message states that too.
func practiceLanguageRefusedOnWrite(hubParam string) string {
	return practiceLanguageRefusedLead +
		"Group the write under its origin instead — pass " + hubParam + ":\"<hub id>\", " +
		"where the hub is a node of type \"source\" you create once per origin. " +
		"On a link or an unlink that same param SCOPES THE ENDPOINTS rather than grouping anything: both " +
		"endpoints must already be grouped under the hub it names. " +
		"On an update, an update_batch, a bulk_update_metadata or an upsert of an existing node it SCOPES THE " +
		"TARGETS the same way: every id the call names must already be grouped under that hub"
}

// refusePracticeLanguageOnWrite and refusePracticeLanguageOnRead are the two
// gates every practice arm calls, one per intent.
//
// TWO FUNCTIONS RATHER THAN A CHECK PER ARM, because the arms are the population
// a reviewer has to enumerate to know the rule holds, and a rule spelled once is
// a rule they can find. Each self-filters on the family so a caller can drop it
// in front of a shared arm without the arm knowing which graph it serves.
//
// EVERY ARM MEANS EVERY ONE. The arms that CLAIM a practice CRUD mutation are not
// the write population: upsert, unlink, update_batch and bulk_update_metadata
// decline past that claim to the non-knowledge fallthrough, and the `delete` tool
// never touches the mutate paths at all. Each of those dropped the field and
// compiled a target with no language on it, which is the silent redirect these
// refusals exist to prevent, so the write gate sits on the fallthrough and on the
// delete tool's own guard as well. The READ population is the practice query
// router and the search tool's practice arm, each of which claims the payload
// before its own accounting gate runs.
func refusePracticeLanguageOnWrite(graph, language, hubParam string) error {
	if graph != string(kgtypes.GraphPractice) || language == "" {
		return nil
	}
	return errors.New(practiceLanguageRefusedOnWrite(hubParam))
}

func refusePracticeLanguageOnRead(graph, language, hubParam string) error {
	if graph != string(kgtypes.GraphPractice) || language == "" {
		return nil
	}
	return errors.New(practiceLanguageRefusedOnRead(hubParam))
}
