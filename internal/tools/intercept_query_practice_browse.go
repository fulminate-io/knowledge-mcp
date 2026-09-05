// SPDX-License-Identifier: Apache-2.0

package tools

// intercept_query_practice_browse.go holds the practice shapes that read WITHOUT
// a ranked text query — the browse arm, the stats body, and the notices that
// qualify a zero-hit ranked search — split out of
// intercept_query_practice_linkage.go, which owns the dispatch switch and the two
// ranked-search composers and is at its file-length budget.
//
// WHY A BROWSE ARM EXISTS AT ALL. Before it, every practice read that carried no
// text fell into the ranked-search arm, which searched for the empty string and
// rendered an empty Best Practices list; and the browse FILTERS (type, types,
// status, meta) were classified rejected, so a caller who supplied one was told
// the path does not apply it. The practice corpus was therefore reachable only by
// guessing text. This arm serves those filters with ONE Selection and ONE
// Execute, server-side filtered and server-side paged.

import (
	"context"
	"fmt"
	"runtime"
	"sort"
	"strings"
	"sync"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/graphsel"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// practiceRebuildHint is the invocation the segment-gap notice prints, with the
// graph name interpolated. A RAW string, deliberately: the ordinary double-quoted
// Go spelling would put the bytes \"name\":\" on disk, and the gate that checks
// this message names its key greps for the literal "name":" — so the escaped
// spelling makes the constant unverifiable by its own test.
//
// The key is "name", not "language": handleClientRebuildSegments refuses an empty
// a.Name, and it ACCEPTS graph="practice" because kgtypes.HasRebuildableSegments
// admits it. That predicate is no longer derived from sync-eligibility — it is an
// independent exclusion of logs and linkage, which is what lets the
// raw graphs carry segments without ever syncing. practice is admitted either way.
// A message naming language: instead would be an unactionable instruction.
//
// "reset":true IS LOAD-BEARING, NOT A FLOURISH — MEASURED 2026-08-25. Without it
// the command REFUSES in exactly the state that produces this message: the rebuild
// pages from a persisted watermark, so an already-drained graph scans zero nodes
// and handleClientRebuildSegments' out.Scanned==0 arm answers "has no embedded
// nodes to rebuild segments from — nothing to do", which is false for a graph with
// thousands of vectors. reset zeroes the watermark and rescans the whole corpus.
// A remediation that cannot run is worse than no remediation, so the hint prints
// the form that works. (The refusal's own wording is a separate defect in the
// rebuild path, reported rather than fixed here.)
const practiceRebuildHint = `manage({"operation":"rebuild_segments","graph":"practice","name":"%s","reset":true})`

// practiceBrowse serves every practice browse shape: the bare listing, the
// singular type filter, the plural types filter, the status filter and the meta
// predicates, paged by the caller's limit/offset.
//
// It mirrors resourceBrowse (intercept_query_cloud_cicd.go:253) — Selection +
// Limit/Offset + Execute + decode + render — keyed on Language instead of
// Account, with four deliberate differences:
//
//   - NodeType/NodeTypes ride the caller's filter rather than a pinned kind:
//     practice graphs hold four node types (pattern / use_case / example /
//     reference), so a bare browse must return all of them.
//   - The meta map lowers through engine.LowerMetaPredicates rather than a
//     hand-rolled mapping, because "*" is the key-presence sentinel (OP_EXISTS)
//     and an equality-against-a-literal-asterisk re-implementation would compile,
//     pass a key-only assertion and return nothing in production.
//   - The row cap defaults to engine.BrowseDefaultLimit rather than a local
//     literal, so the two spellings of "how a browse limit defaults" stay one
//     number.
//   - SkipTotal is FALSE, unlike resourceBrowse: renderBrowseResponse reads
//     resp.GetTotal() for the "_Use offset=N to see more._" footer, so skipping
//     the total would silently delete pagination.
func practiceBrowse(ctx context.Context, exec engine.ExecuteFn, a queryArgs) kgtools.ToolResult {
	limit := int(a.Limit)
	if limit <= 0 {
		limit = engine.BrowseDefaultLimit
	}
	offset := int(a.Offset)

	sel := &knowledgev1.Selection{
		NodeType:           a.Type,
		NodeTypes:          a.Types,
		MetadataPredicates: engine.LowerMetaPredicates(a.Meta),
	}
	if a.Status != "" {
		sel.Statuses = []string{a.Status}
	}
	resp, err := exec(ctx, &knowledgev1.ExecuteRequest{
		Plan: &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{
			Selection:         sel,
			Limit:             int32(limit),
			Offset:            int32(offset),
			IncludeTombstones: a.IncludeTombstones,
		}},
		Target: graphsel.GraphSelectorFor(kgtypes.GraphPractice, a.Language, false),
	})
	if err != nil {
		return errorResult("practice browse failed: " + err.Error())
	}
	res, rerr := engine.RenderBrowse(resp, engine.BrowseCtx{
		Label:    domainGraphLabel(a),
		NodeType: a.Type,
		Offset:   offset,
		Format:   a.Format,
		Fields:   a.Fields,
		MetaKeys: sortedMetaKeys(a.Meta),
		// This arm ROUTES the opt-in into its own plan above, so it carries the
		// caller's value through to the projection gate too. Passing false here
		// would refuse tombstoned_at on a read that genuinely returns tombstoned
		// rows — and false is what the compiler would have accepted silently.
		IncludeTombstones: a.IncludeTombstones,
	})
	if rerr != nil {
		return errorResult("practice browse decode failed: " + rerr.Error())
	}
	// This arm issues its own Execute and returns its own result, so it never
	// passes through engine.Render — the single place every COMPILED tool's
	// response picks up the notice. Without this call a browse the server row
	// ceiling clamped renders as a complete-looking list with rows missing.
	return engine.WithTruncationNotice(res, resp)
}

// sortedMetaKeys returns the meta filter keys in a stable order, so the inline
// per-node meta values renderBrowseResponse prints do not reorder between runs.
func sortedMetaKeys(meta map[string]string) []string {
	if len(meta) == 0 {
		return nil
	}
	keys := make([]string, 0, len(meta))
	for k := range meta {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// practiceStatsResult renders the practice stats body: Stats RPC →
// RenderStatsBreakdown under the per-language header, or the json envelope, plus
// the bounded sample names when samples=true. Split out of routePracticeClient so
// that router stays a flat gate-and-delegate sequence — the accounting gate added
// a nested block to each arm, and the stats arm was the one that carried enough
// body to tip the router over the nesting budget. It sits in this file rather
// than beside the router for the file-length budget; the router's other three
// arms are unaffected.
func practiceStatsResult(ctx context.Context, gc statsRPC, a queryArgs) kgtools.ToolResult {
	resp, err := gc.Stats(ctx, &knowledgev1.StatsRequest{
		Target: &knowledgev1.GraphSelector{Graph: "practice", Language: a.Language},
	})
	if err != nil {
		return errorResult(fmt.Sprintf("practice %q graph stats failed: %s", a.Language, err.Error()))
	}
	stats := resp.GetGraphStats()
	if a.Format == "json" {
		return jsonResult(map[string]any{
			"graph":               "practice",
			"language":            a.Language,
			"node_count":          stats.GetNodeCount(),
			"edge_count":          stats.GetEdgeCount(),
			"binary_vector_count": stats.GetBinaryVectorCount(),
			"nodes_by_type":       stats.GetNodesByType(),
			"edges_by_type":       stats.GetEdgesByType(),
		})
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "## Practice Graph: %s\n\n", a.Language)
	sb.WriteString(engine.RenderStatsBreakdown(stats))
	if a.Samples {
		samples := fetchPracticeSamples(ctx, gc.Execute, a.Language, stats)
		var sampleSB strings.Builder
		engine.RenderSampleNames(&sampleSB, stats, samples)
		sb.WriteString(sampleSB.String())
	}
	return textResult(sb.String())
}

// fetchPracticeSamples fetches up to 2 sample nodes per node type for the
// practice stats sample enrichment (bounded by node-type count). It sits beside
// practiceStatsResult, its only caller.
func fetchPracticeSamples(ctx context.Context, exec engine.ExecuteFn, language string, stats *knowledgev1.GraphStats) map[kgtypes.NodeType][]*knowledgev1.Node {
	byType := stats.GetNodesByType()
	samples := make(map[kgtypes.NodeType][]*knowledgev1.Node, len(byType))
	for nt := range byType {
		resp, err := exec(ctx, &knowledgev1.ExecuteRequest{
			Plan: &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{
				Selection: &knowledgev1.Selection{NodeType: nt},
				Limit:     2,
				SkipTotal: true,
			}},
			Target: &knowledgev1.GraphSelector{Graph: "practice", Language: language},
		})
		if err != nil {
			continue
		}
		nodes, derr := engine.DecodeNodes(resp)
		if derr != nil || len(nodes) == 0 {
			continue
		}
		samples[kgtypes.NodeType(nt)] = nodes
	}
	return samples
}

// practiceSegmentGapNotice qualifies a ZERO-HIT practice ranked search: is the
// ranked index MISSING, is the graph un-embedded, is it genuinely empty, or was
// the zero simply unqualifiable? Returns the message and whether it is LOUD (an
// error the caller must not mistake for data).
//
// THE TWO OPERANDS COME FROM DIFFERENT AUTHORITIES, and that is the whole
// correctness of the check: the LOCAL operand is this client's live-resident doc
// count, read from the engine, while nodes/embedded come from the SERVER node store.
// Two numbers about the same graph taken from two different places can disagree,
// which is what makes the comparison capable of finding anything.
//
// THE LOCAL OPERAND CHANGED AND THE PROPERTY DID NOT. It used to be the segment
// engine's SHIPPED doc count, read from a remote manifest. That manifest is gone,
// and ShippedSegmentDocCount now routes to a resident-pool read — so
// keeping the old call here would have quietly made both operands local-ish and, more
// to the point, would have inherited the evicted-pool false alarm the old doc warned
// about in the abstract. The fix is the ORDER below, not a different number.
//
// THE EVICTION FENCE COMES FIRST, AND IT IS THE ANSWER RATHER THAN A WORKAROUND. An
// evicted pool's presence genuinely is not determinable without re-materializing it,
// and re-materializing it here would undo the residency policy from a zero-hit search
// path. So an evicted pool gets the truthful-inability caveat, not a verdict.
//
// THE LOADING DECIDER, NOT THE REPORTER. LiveResidentDocCount takes no load and its
// own doc says "a graph whose engine has not loaded yet legitimately reads 0" — using
// it would emit the confident "the ranked index is missing" for a cold-but-healthy
// pool. LoadLiveResidentDocCount loads first and RETURNS its load error rather than
// swallowing it, so a caller that could not load declines instead of acting on an
// empty view.
//
// EVERY UNKNOWN READS AS "COULD NOT QUALIFY", never as a clean zero. The caveat
// branches are the truthful-inability answer: the response says the zero could
// not be qualified and why.
//
// It runs OFF the hot path — every operand read happens only once the hit set is
// already empty, and the node and vector counts cost ONE Stats call between them.
func practiceSegmentGapNotice(ctx context.Context, deps ClientDeps, language string) (string, bool) {
	sr := deps.SegmentCoverage()
	if sr == nil {
		return practiceZeroCaveat(language, "the segment-coverage seam is unwired"), false
	}
	// 1. EVICTION FENCE, ahead of every read.
	if poolEvictedFor(deps, kgtypes.GraphPractice, language) {
		return practiceZeroCaveat(language,
			"its segment pool is evicted, so presence is not determinable without re-materializing it"), false
	}
	// 2. THE SERVER OPERANDS.
	cov, err := graphCoverageFor(
		ctx, deps.GraphCaller(), graphsel.GraphSelectorFor(kgtypes.GraphPractice, language, false))
	nodes, embedded, ok := cov.Nodes, cov.Embedded, cov.Measurable
	if err != nil {
		return practiceZeroCaveat(language, "the graph stats read failed: "+err.Error()), false
	}
	if !ok {
		return practiceZeroCaveat(language, "the graph stats seam is unavailable"), false
	}
	// 3. THE LOCAL OPERAND, through the LOADING decider. Its error is routed into a
	// caveat rather than treated as a zero.
	covered, haveLocal, err := loadLiveResidentFor(ctx, deps, kgtypes.GraphPractice, language)
	if err != nil {
		return practiceZeroCaveat(language, "the live-resident probe failed: "+err.Error()), false
	}
	if !haveLocal {
		return practiceZeroCaveat(language, "the live-resident probe seam is unwired"), false
	}
	if covered > 0 {
		// A genuine no-match: the ranked index exists and was searched.
		return "", false
	}
	switch {
	case embedded > 0:
		return fmt.Sprintf(
			"practice search: graph %q has %d embedded nodes but 0 search segments — the ranked "+
				"index is missing, not empty. Rebuild it with %s and retry.",
			language, embedded, fmt.Sprintf(practiceRebuildHint, language)), true
	case nodes > 0:
		return practiceUnembeddedNotice(language, nodes), true
	default:
		// A genuinely empty graph: zero nodes, so zero results is the truth.
		return "", false
	}
}

// practiceUnembeddedNotice is the loud message for a graph that holds nodes but
// has no vectors: there is no ranked index AND none can be built yet, so pointing
// the caller at a rebuild would be an instruction that cannot succeed. It names
// the LLM-coverage column instead, which is where the drain progress shows.
func practiceUnembeddedNotice(language string, nodes int) string {
	return fmt.Sprintf(
		"practice search: graph %q holds %d nodes but 0 of them are embedded, so it has no ranked "+
			"index and none can be built yet — the client LLM pipeline has not drained this graph. "+
			"Check the LLM-coverage column of manage({\"operation\":\"status\"}).",
		language, nodes)
}

// practiceZeroCaveat is the truthful-inability answer: the zero stands, but the
// response says it could not be qualified and names the reason.
func practiceZeroCaveat(language, why string) string {
	return fmt.Sprintf(
		"Note: this zero could not be qualified for practice graph %q — %s, so a missing ranked "+
			"index cannot be told apart from a genuine no-match here.",
		language, why)
}

// practiceZeroHitNotice is practiceSegmentGapNotice plus the embedder-degrade
// disclosure. A failed EmbedBinary leaves the query vector nil and the search
// silently falls back to the BM25 arm alone; when the result set is EMPTY that
// degrade is load-bearing, because the caller otherwise cannot learn the semantic
// arm never ran. The disclosure fires only on an empty result set.
func practiceZeroHitNotice(ctx context.Context, deps ClientDeps, language string, embErr error) (string, bool) {
	notice, loud := practiceSegmentGapNotice(ctx, deps, language)
	if loud || embErr == nil {
		return notice, loud
	}
	degrade := fmt.Sprintf("The semantic arm did not run: %s; these results are BM25-only.", embErr.Error())
	return strings.TrimSpace(notice + " " + degrade), false
}

// practiceFanOutProbe searches ONE graph for the fan-out and classifies the
// outcome into the three states the caller buckets on: matched (results), could
// not be searched (err), or searched-but-has-no-ranked-index (gapped).
//
// THE GAP PROBE RUNS ONLY ON A ZERO-HIT GRAPH, and it runs HERE — inside the
// caller's bounded worker pool — rather than serially afterwards. A graph that
// matched is never probed, so a healthy fan-out pays nothing; only the graphs that
// returned nothing cost one Stats read each, which is exactly the set whose
// emptiness needs explaining.
func practiceFanOutProbe(
	ctx context.Context, deps ClientDeps, mgr SegmentSearcher, gc GraphCaller,
	language, query string, queryVec []byte, k int,
) (results []engine.SearchResult, err error, gapped bool) {
	results, err = practiceSearchOneGraph(ctx, mgr, gc, language, query, queryVec, k)
	if err != nil || len(results) > 0 {
		return results, err, false
	}
	_, gapped = practiceSegmentGapNotice(ctx, deps, language)
	return results, nil, gapped
}

// practiceFanOutGapNotice consults practiceSegmentGapNotice for every enumerated
// graph and joins the LOUD ones into a single message. It runs the per-graph
// checks under the same NumCPU-bounded pool shape the fan-out search itself uses,
// so a many-graph corpus does not serialize the probe.
func practiceFanOutGapNotice(ctx context.Context, deps ClientDeps, names []string) string {
	var (
		mu    sync.Mutex
		loud  []string
		wg    sync.WaitGroup
		guard = make(chan struct{}, max(1, runtime.NumCPU()))
	)
	for _, name := range names {
		wg.Add(1)
		go func(language string) {
			defer wg.Done()
			guard <- struct{}{}
			defer func() { <-guard }()
			if notice, isLoud := practiceSegmentGapNotice(ctx, deps, language); isLoud {
				mu.Lock()
				loud = append(loud, notice)
				mu.Unlock()
			}
		}(name)
	}
	wg.Wait()
	sort.Strings(loud)
	return strings.Join(loud, "\n")
}

// appendNotice appends a caveat paragraph to an already-rendered text result, so
// a non-loud zero still carries the reason it could not be qualified.
func appendNotice(res kgtools.ToolResult, notice string) kgtools.ToolResult {
	if notice == "" || len(res.Content) == 0 {
		return res
	}
	res.Content[0].Text += "\n\n" + notice
	return res
}

// practiceFanOutPartialLine names the graphs a partial fan-out could not draw
// results from, or "" when every graph was genuinely searched. It reports the two
// causes SEPARATELY because they need different actions from the reader: a failed
// graph is a fault to investigate, while an un-indexed one has a known remedy.
//
// WHY IT IS NOT OPTIONAL. The fan-out header claims "Searched N practice graphs"
// and names them. Without this line that sentence is false whenever any graph
// lacked an index — and since the fan-out is the primary way the corpus is read,
// the failure mode is a confident cross-graph ranking that quietly omits whole
// corpora. MEASURED: a header naming eight graphs while three had zero segments,
// which made the results look comprehensive and near-random at the same time.
func practiceFanOutPartialLine(failed, unindexed []string) string {
	var parts []string
	if len(failed) > 0 {
		parts = append(parts, "could not be searched: "+strings.Join(failed, "; "))
	}
	if len(unindexed) > 0 {
		parts = append(parts, "have no ranked index yet, so they contributed nothing: "+
			strings.Join(unindexed, ", ")+
			" (rebuild each with "+fmt.Sprintf(practiceRebuildHint, "<graph>")+")")
	}
	if len(parts) == 0 {
		return ""
	}
	return "_Incomplete result set — these practice graphs " + strings.Join(parts, "; and these ") + "._"
}

// practiceFanOutZeroResult qualifies an EMPTY fan-out merge, returning done=false
// when the zero needs no annotation and the caller should render it as usual.
//
// ORDER IS THE BEHAVIOUR: a FAILURE outranks a segment gap, because "no matches"
// is a lie when the search did not run, and only once every graph was searched
// successfully is the per-graph gap check meaningful.
//
// embErr is the fan-out's single embed failure, disclosed on the zero exactly as
// its sibling composer discloses it — one failed embed degraded every graph here,
// so an empty cross-graph result set must say the semantic arm never ran.
func practiceFanOutZeroResult(
	ctx context.Context, deps ClientDeps, names, failed []string, embErr error,
) (kgtools.ToolResult, bool) {
	if len(failed) > 0 {
		return errorResult("practice fan-out: no results, and these graphs could not be searched — " +
			strings.Join(failed, "; ")), true
	}
	notice := practiceFanOutGapNotice(ctx, deps, names)
	if notice != "" {
		return errorResult(notice), true
	}
	if embErr != nil {
		return errorResult(fmt.Sprintf(
			"practice fan-out: no results, and the semantic arm did not run: %s; "+
				"this search was BM25-only across every practice graph.", embErr.Error())), true
	}
	return kgtools.ToolResult{}, false
}

// practiceGraphResult is one graph's hydrated slice of a fan-out, carried from
// the per-graph goroutine to the merge.
type practiceGraphResult struct {
	graph   string
	results []engine.SearchResult
}

// mergePracticeFanOutHits tags every per-graph result with its source graph,
// sorts the union by score descending and caps it at the caller's resolved limit
// (mirrors mergeMultiRepoResults). The cap is applied HERE as well as at each
// per-graph Search: without it a caller asking for 25 would get 25 per graph and
// then silently the default back.
func mergePracticeFanOutHits(all []practiceGraphResult, k int) []engine.PracticeFanOutHit {
	merged := make([]engine.PracticeFanOutHit, 0)
	for _, gr := range all {
		for _, r := range gr.results {
			merged = append(merged, engine.PracticeFanOutHit{Graph: gr.graph, Result: r})
		}
	}
	sort.Slice(merged, func(i, j int) bool { return merged[i].Result.Score > merged[j].Result.Score })
	if len(merged) > k {
		merged = merged[:k]
	}
	return merged
}
