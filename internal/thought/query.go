// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// ThoughtSearcher is the narrow consumer-side seam recall uses to source
// candidates from the CLIENT knowledge segment engines (BM25+HNSW → RRF)
// instead of dispatching a server search. *segmentdist.Manager satisfies it
// (Manager.Search). Declared here so the thought package stays import-clean of
// the higher-level tools/bootstrap packages and so tests can inject a fake. A
// nil searcher means "no client engine wired" — which happens in a degraded
// harness AND during the bind-first wiring window (bind-first startup), before the segment
// Manager is wired. Recall is UNGATED by design: a nil searcher yields zero
// candidates and gatherRecallCandidates degrades to full-corpus iteration
// (iterateRecallCandidates). There is no server-search gather.
type ThoughtSearcher interface {
	Search(ctx context.Context, gt kgtypes.GraphType, name, queryText string, queryVec []byte, k int) ([]searchengine.Hit, error)
}

// nanosToTime converts an int64 unix-nanos value (the value-embed proto
// timestamp representation) to a time.Time, mapping 0 →
// the zero time.Time. Shared by the thought package's timestamp-formatting and
// time-comparison sites.
func nanosToTime(nanos int64) time.Time {
	if nanos == 0 {
		return time.Time{}
	}
	return time.Unix(0, nanos)
}

// RecallOptions configures filters for RecallThoughts.
type RecallOptions struct {
	Query          string    // semantic search text (empty = iterate all)
	ValenceMin     *float64  // minimum valence filter
	ValenceMax     *float64  // maximum valence filter
	MagnitudeMin   float64   // minimum magnitude
	ConsistencyMax *float64  // maximum consistency (find contested thoughts)
	Status         string    // filter by thought status
	Session        string    // filter by session name
	ConnectedTo    string    // must be connected to this node
	TimeStart      time.Time // time range start
	TimeEnd        time.Time // time range end
	Limit          int       // max results (default 20)

	// QueryVec is the CLIENT-EMBEDDED query vector (32-byte binary). Set by the
	// recall interceptor (handleRecallClient) via deps.Embedder() so the client
	// knowledge HNSW arm is exercised; empty when no embedder is configured (the
	// search degrades to the BM25 arm). Mirrors the maybeEmbedQuery seam the
	// generic search arm uses.
	QueryVec []byte
	// Searcher routes candidate-gathering through the CLIENT knowledge segment
	// engines (Manager.Search) instead of a server search dispatch. nil → recall
	// degrades to full-corpus iteration (iterateRecallCandidates); nil happens in a
	// degraded harness AND during the bind-first wiring window (bind-first startup), before
	// the segment Manager is wired. There is no server-search gather.
	Searcher ThoughtSearcher

	// WidePool, when > 0, widens the candidate gather to this many hits AND makes
	// RecallThoughts return the filtered+sorted pool WITHOUT its final trim-to-Limit
	// — so the recall intercept can rerank the untrimmed wide pool and trim
	// afterward, mirroring search's widen->rerank->trim shape. 0 (the default, and
	// the bare Query=="" path) keeps the existing trim-to-Limit behavior verbatim.
	WidePool int

	// Source is the optional resident thought-corpus cache: the bare-recall
	// full-iteration fallback (iterateRecallCandidates) reads its NodeThought set from
	// a warm Source instead of re-draining. nil (a degraded harness, the reflection
	// loop not running in-process, or a test) drains — behavior-equivalent to the
	// pre-cache path. The recall intercept sets it from ClientDeps.CorpusProvider().
	Source CorpusSource
}

// ThoughtResult is a thought with its computed properties and search score.
type ThoughtResult struct {
	// Node is a *knowledgev1.Node — the typed wire node the client consumes
	// natively (T5 dropped the store.Node wrapper from the client read
	// path). Pointer element: knowledgev1.Node carries a noCopy so a value field
	// would make every []ThoughtResult append/range a copylocks violation.
	Node        *knowledgev1.Node
	Properties  ThoughtProperties
	Score       float64 // search relevance (0 if not semantic search)
	SessionName string
}

// RecallThoughts searches and filters thoughts with composable criteria.
// Client-side: takes a graph client instead of the *Store receiver.
// Every store.Store().Query call from the original pkg/thought/query.go is
// translated into a gc.Call("query"|"traverse"|...) wire call.
func RecallThoughts(ctx context.Context, gc Caller, opts RecallOptions) ([]ThoughtResult, error) {
	if gc == nil {
		return nil, errors.New("thought: RecallThoughts: graph client unavailable")
	}
	t0 := time.Now()
	if opts.Limit <= 0 {
		opts.Limit = 20
	}

	gatherStart := time.Now()
	candidates := gatherRecallCandidates(ctx, gc, opts)
	gatherDur := time.Since(gatherStart)

	filterStart := time.Now()
	results := applyRecallFilters(ctx, gc, candidates, opts)
	filterDur := time.Since(filterStart)

	if opts.Query != "" {
		sort.Slice(results, func(i, j int) bool { return results[i].Score > results[j].Score })
	} else {
		sortMag := func(r ThoughtResult) float64 {
			if m, ok := propagatedMagnitude(r.Node); ok {
				return m
			}
			return r.Properties.Magnitude
		}
		sort.Slice(results, func(i, j int) bool { return sortMag(results[i]) > sortMag(results[j]) })
	}

	// Skip the final trim when WidePool>0: the caller (recall intercept) reranks
	// the untrimmed filtered+sorted wide pool and trims to Limit afterward. The
	// sort above still runs, so the returned wide pool is score-sorted.
	if opts.WidePool == 0 && len(results) > opts.Limit {
		results = results[:opts.Limit]
	}
	slog.Debug("RecallThoughts", "query", opts.Query, "candidates", len(candidates), "filtered", len(results),
		"gather_dur", gatherDur.Round(time.Microsecond), "filter_dur", filterDur.Round(time.Microsecond),
		"total_dur", time.Since(t0).Round(time.Microsecond))
	return results, nil
}

// gatherRecallCandidates returns all thought candidates via semantic search
// or full iteration. Falls back to iteration on search failure or empty
// results.
func gatherRecallCandidates(ctx context.Context, gc Caller, opts RecallOptions) []ThoughtResult {
	if opts.Query != "" {
		candidates, err := searchRecallCandidates(ctx, gc, opts)
		if err == nil && len(candidates) > 0 {
			return candidates
		}
	}
	return iterateRecallCandidates(ctx, gc, opts.Source)
}

// searchRecallCandidates returns thought candidates from a semantic search
// query, thought-only filter applied client-side.
//
// Candidates come from the CLIENT knowledge engines (Manager.Search → RRF over
// the BM25 + HNSW arms, the latter driven by the client-embedded opts.QueryVec) +
// ONE bulk RETURN_MODE_NODES hydrate — NEVER a server search dispatch. Thoughts
// are GraphKnowledge nodes, so the same client knowledge segments back the corpus.
// The Searcher is wired by the real recall interceptor for the life of the daemon
// EXCEPT during the bind-first wiring window (bind-first startup); a nil Searcher
// (still-wiring, OR an empty/un-collected knowledge graph in a degraded harness)
// yields zero candidates, which gatherRecallCandidates falls back to full
// iteration over.
func searchRecallCandidates(ctx context.Context, gc Caller, opts RecallOptions) ([]ThoughtResult, error) {
	if opts.Searcher == nil {
		return nil, nil
	}
	return searchRecallCandidatesClient(ctx, gc, opts)
}

// searchRecallCandidatesClient gathers recall candidates from the CLIENT
// knowledge segment engines: Manager.Search returns RRF-fused ranked Hits (ID +
// fused score) which one bulk fetchNodesByIDs hydrates into nodes; rows join by
// id-map in ranked order, carry the fused score, and keep the thought-only type
// filter. The HNSW arm is exercised whenever opts.QueryVec is non-empty.
func searchRecallCandidatesClient(ctx context.Context, gc Caller, opts RecallOptions) ([]ThoughtResult, error) {
	// Widen the gather toward search's idiom when the intercept asks for it
	// (WidePool>0): a buried candidate can only be promoted by a downstream rerank
	// if it is in the pool. Default search-k is opts.Limit*5.
	k := opts.Limit * 5
	if opts.WidePool > 0 {
		k = opts.WidePool
	}
	hits, err := opts.Searcher.Search(ctx, kgtypes.GraphKnowledge, "default", opts.Query, opts.QueryVec, k)
	if err != nil {
		return nil, fmt.Errorf("client search: %w", err)
	}
	if len(hits) == 0 {
		return nil, nil
	}
	ids := make([]string, len(hits))
	for i, h := range hits {
		ids[i] = h.ID
	}
	byID := fetchNodesByIDs(ctx, gc, ids)

	results := make([]ThoughtResult, 0, len(hits))
	for _, h := range hits {
		n, ok := byID[h.ID]
		if !ok {
			continue // tombstoned/deleted between rank and hydrate — skip.
		}
		results = append(results, ThoughtResult{Node: n, Score: h.Score})
	}
	return filterThoughtCandidates(results), nil
}

// filterThoughtCandidates trims a candidate set to thought-typed nodes,
// preserving order. Shared by both the client-engine and server-search gather.
func filterThoughtCandidates(results []ThoughtResult) []ThoughtResult {
	candidates := make([]ThoughtResult, 0, len(results))
	for _, r := range results {
		if kgtypes.NodeType(r.Node.Type) != kgtypes.NodeThought {
			continue
		}
		candidates = append(candidates, r)
	}
	return candidates
}

// iterateRecallCandidates returns all thought nodes: served O(1) from a warm
// resident corpus cache (src) or drained via the paged type=thought browse
// (fetchAllThoughtNodes) when src is nil/cold. The bare-recall (empty-query)
// fallback candidate set.
func iterateRecallCandidates(ctx context.Context, gc Caller, src CorpusSource) []ThoughtResult {
	nodes, err := fetchAllThoughtNodes(ctx, gc, src)
	if err != nil {
		slog.Warn("thought: iterateRecallCandidates: fetchAllThoughtNodes failed", "err", err)
		return nil
	}
	candidates := make([]ThoughtResult, 0, len(nodes))
	for i := range nodes {
		candidates = append(candidates, ThoughtResult{Node: nodes[i]})
	}
	return candidates
}

// applyRecallFilters filters candidates using node-level and property-level
// criteria. Computes thought properties via a single bulk charges-fetch
// round-trip (perf invariant: never per-thought N+1).
func applyRecallFilters(ctx context.Context, gc Caller, candidates []ThoughtResult, opts RecallOptions) []ThoughtResult {
	// Bulk-fetch charges for all candidate IDs in one round-trip.
	ids := make([]string, len(candidates))
	for i, c := range candidates {
		ids[i] = c.Node.Id
	}
	chargeMap := chargeMapForThoughts(ctx, gc, ids)

	now := time.Now()
	var results []ThoughtResult
	for _, c := range candidates {
		if !thoughtMatchesFilters(ctx, gc, c.Node, opts) {
			continue
		}

		props := computePropertiesFromCharges(chargeMap[c.Node.Id], now)

		if !propertyFiltersMatch(props, opts, c.Node) {
			continue
		}

		c.Properties = props
		c.SessionName = kgtypes.Value(c.Node, "session")
		results = append(results, c)
	}
	return results
}

// propertyFiltersMatch checks valence, magnitude, and consistency filters.
// Verbatim from pkg/thought/query.go.
func propertyFiltersMatch(props ThoughtProperties, opts RecallOptions, n *knowledgev1.Node) bool {
	valence := props.Valence
	if v, ok := propagatedValence(n); ok {
		valence = v
	}
	magnitude := props.Magnitude
	if m, ok := propagatedMagnitude(n); ok {
		magnitude = m
	}

	if opts.ValenceMin != nil && valence < *opts.ValenceMin {
		return false
	}
	if opts.ValenceMax != nil && valence > *opts.ValenceMax {
		return false
	}
	if magnitude < opts.MagnitudeMin {
		return false
	}
	if opts.ConsistencyMax != nil && props.Consistency > *opts.ConsistencyMax {
		return false
	}
	return true
}

// thoughtMatchesFilters checks non-property filters (status, session, time,
// connected_to). The connected_to branch performs a single traverse wire
// call instead of the original in-process store.From(...).IDs() shape.
func thoughtMatchesFilters(ctx context.Context, gc Caller, n *knowledgev1.Node, opts RecallOptions) bool {
	if opts.Status != "" && n.Status != opts.Status {
		return false
	}
	if opts.Session != "" && kgtypes.Value(n, "session") != opts.Session {
		return false
	}
	if !opts.TimeStart.IsZero() && nanosToTime(n.CreatedAt).Before(opts.TimeStart) {
		return false
	}
	if !opts.TimeEnd.IsZero() && nanosToTime(n.CreatedAt).After(opts.TimeEnd) {
		return false
	}
	if opts.ConnectedTo != "" {
		targets, _ := fetchOutgoingTargets(ctx, gc, n.Id)
		if !slices.Contains(targets, opts.ConnectedTo) {
			return false
		}
	}
	return true
}

// propagatedValence returns the propagated_valence from node metadata when
// present and parseable. Verbatim from pkg/thought/query.go.
func propagatedValence(n *knowledgev1.Node) (float64, bool) {
	s := kgtypes.Value(n, "propagated_valence")
	if s == "" {
		return 0, false
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// propagatedMagnitude returns the propagated_magnitude from node metadata
// when present and parseable. Verbatim from pkg/thought/query.go.
func propagatedMagnitude(n *knowledgev1.Node) (float64, bool) {
	s := kgtypes.Value(n, "propagated_magnitude")
	if s == "" {
		return 0, false
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// rerankFloorThreshold is the reranked-relevance best-score below which recall
// emits the zero-overlap floor banner. 0.30 is just above the bottom of search's
// observed 0.27-0.80 reranked range — biases toward an honest "maybe noise" note
// for borderline-weak reranked results; first-cut heuristic to be tuned once live
// reranked recall scores are observed. NOT an empirically tuned cutoff: it is
// derived from a SINGLE observed reranked range and should be recalibrated at the
// deferred live-capstone calibration point against real recall distributions.
//
// Failure modes (heuristic, not exact): (a) a single highly-relevant result in an
// otherwise-empty corpus can rank-0 in both RRF arms and read as floor (2/61) on
// the no-rerank path — mitigated because the rerank path (when a Voyage key is
// configured) gives the truer signal, and the banner says "showing closest
// matches" not "nothing found"; (b) the threshold biases toward an honest
// "maybe noise" note rather than false confidence.
const rerankFloorThreshold = 0.30

// rrfFloorBannerThreshold is the RRF (rank-fusion) best-score at/below which the
// no-rerank semantic path emits the floor banner. The RRF floor is 2/61 = 0.03279
// when the top thought is rank-0 in both arms with no corroboration (1/(rrfK+rank+1),
// rrfK=60 — segmentdist/rrf.go:16,47). 0.04 sits just above 2/61 to absorb float
// wobble. Same first-cut-heuristic caveat as rerankFloorThreshold.
const rrfFloorBannerThreshold = 0.04

// recallFloorBanner returns a one-line additive banner when the best score is at
// the zero-overlap floor (so the rendered set reads as "closest matches, maybe
// noise" rather than confident hits), or "" otherwise. CRITICAL: the banner is
// ADDITIVE prose only — it NEVER suppresses a result row; a genuinely-relevant
// low-rank result is still rendered in full beneath it. results are score-sorted
// on the query path, so results[0].Score is the best score.
func recallFloorBanner(results []ThoughtResult, didRerank bool) string {
	if len(results) == 0 {
		return "" // the "No thoughts found." path already handles empty.
	}
	best := results[0].Score
	if didRerank {
		if best < rerankFloorThreshold {
			return "No strongly related thoughts — showing closest matches by relevance."
		}
		return ""
	}
	// No reranker: only the semantic (RRF) path carries a non-zero Score. The
	// bare magnitude path (Score==0) gets no floor banner.
	if best > 0 && best <= rrfFloorBannerThreshold {
		return "No strongly related thoughts — results are at the rank-fusion floor (no clear semantic match)."
	}
	return ""
}

// recallModeFooter returns the honest score-scale footer mirroring search's
// "_search mode: %s_" footer (render_search.go:306). didRerank true → the score
// is the Voyage reranked relevance; didRerank false with a non-zero best Score →
// the score is a raw RRF rank value (relevance not scored — no reranker); the
// bare magnitude path (Score==0) → magnitude rank. This makes the rendered score
// scale unambiguous to the consumer.
func recallModeFooter(results []ThoughtResult, didRerank bool) string {
	if didRerank {
		return "\n_recall mode: reranked relevance_\n"
	}
	if len(results) > 0 && results[0].Score > 0 {
		return "\n_recall mode: RRF rank (relevance not scored — no reranker)_\n"
	}
	return "\n_recall mode: magnitude rank_\n"
}

// FormatRecallResults formats recall results based on mode. didRerank threads
// the rerank-vs-RRF state from the recall intercept so the default (search/graph/
// clusters) render can label the score scale honestly and prepend a zero-overlap
// floor banner. Pure rendering, no graph access.
func FormatRecallResults(results []ThoughtResult, mode string, didRerank bool) string {
	if len(results) == 0 {
		return "No thoughts found."
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Found %d thoughts:\n\n", len(results))

	switch mode {
	case "timeline":
		sort.Slice(results, func(i, j int) bool {
			return nanosToTime(results[i].Node.CreatedAt).Before(nanosToTime(results[j].Node.CreatedAt))
		})
		for _, r := range results {
			ts := nanosToTime(r.Node.CreatedAt).Format("2006-01-02 15:04")
			fmt.Fprintf(&sb, "[%s] valence:%.2f mag:%.2f — %s\n", ts, r.Properties.Valence, r.Properties.Magnitude, r.Node.SymbolName)
			if r.SessionName != "" {
				fmt.Fprintf(&sb, "  session: %s\n", r.SessionName)
			}
		}

	case "charges":
		for _, r := range results {
			fmt.Fprintf(&sb, "■ %s [%s]\n", r.Node.SymbolName, r.Node.Status)
			fmt.Fprintf(&sb, "  valence:%.2f mag:%.2f consistency:%.2f charges:%d\n",
				r.Properties.Valence, r.Properties.Magnitude, r.Properties.Consistency, r.Properties.ChargeCount)
			fmt.Fprintf(&sb, "  ID: %s\n\n", r.Node.Id)
		}

	default: // "search", "graph", "clusters"
		// Zero-overlap floor banner — additive prose, prepended after the count
		// line. NEVER suppresses a row: every candidate below is still rendered.
		if banner := recallFloorBanner(results, didRerank); banner != "" {
			sb.WriteString(banner)
			sb.WriteString("\n\n")
		}
		for i, r := range results {
			scoreStr := ""
			if r.Score > 0 {
				scoreStr = fmt.Sprintf(" (score: %.3f)", r.Score)
			}
			fmt.Fprintf(&sb, "%d. %s [%s]%s\n", i+1, r.Node.SymbolName, r.Node.Status, scoreStr)
			fmt.Fprintf(&sb, "   valence:%.2f mag:%.2f | %s\n", r.Properties.Valence, r.Properties.Magnitude, r.Node.Id)
			if r.SessionName != "" {
				fmt.Fprintf(&sb, "   session: %s\n", r.SessionName)
			}
			sb.WriteString("\n")
		}
		// Honest score-scale footer (mirrors search's "_search mode: %s_") so the
		// rendered per-row score is never read as the wrong scale.
		sb.WriteString(recallModeFooter(results, didRerank))
	}

	return sb.String()
}
