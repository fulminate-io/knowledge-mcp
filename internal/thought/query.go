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
)

// nanosToTime converts an int64 unix-nanos value (the value-embed proto
// timestamp representation — decision f21640fb) to a time.Time, mapping 0 →
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
}

// ThoughtResult is a thought with its computed properties and search score.
type ThoughtResult struct {
	// Node is a *knowledgev1.Node — the typed wire node the client consumes
	// natively (T5/FUL-295 dropped the store.Node wrapper from the client read
	// path). Pointer element: knowledgev1.Node carries a noCopy so a value field
	// would make every []ThoughtResult append/range a copylocks violation.
	Node        *knowledgev1.Node
	Properties  ThoughtProperties
	Score       float64 // search relevance (0 if not semantic search)
	SessionName string
}

// RecallThoughts searches and filters thoughts with composable criteria.
// FUL-247 client-side: takes a graph client instead of the *Store receiver.
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

	if len(results) > opts.Limit {
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
	return iterateRecallCandidates(ctx, gc)
}

// searchRecallCandidates returns thought candidates from a semantic search
// query. One wire round-trip; thought-only filter applied client-side.
func searchRecallCandidates(ctx context.Context, gc Caller, opts RecallOptions) ([]ThoughtResult, error) {
	results, err := fetchQueryBySearch(ctx, gc, opts.Query, opts.Limit*5)
	if err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}
	candidates := make([]ThoughtResult, 0, len(results))
	for _, r := range results {
		if kgtypes.NodeType(r.Node.Type) != kgtypes.NodeThought {
			continue
		}
		candidates = append(candidates, r)
	}
	return candidates, nil
}

// iterateRecallCandidates returns all thought nodes via two round-trips: one
// type=thought ID-only query plus one bulk fetchNodesByIDs hydration. Avoids
// the N+1 per-node round-trip that the original pkg/thought/query.go was
// safe to do against the in-process singleton.
func iterateRecallCandidates(ctx context.Context, gc Caller) []ThoughtResult {
	nodes, err := fetchAllThoughtNodes(ctx, gc)
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
// round-trip (BCN4 v2 perf invariant: never per-thought N+1).
func applyRecallFilters(ctx context.Context, gc Caller, candidates []ThoughtResult, opts RecallOptions) []ThoughtResult {
	// Bulk-fetch charges for all candidate IDs in one round-trip.
	ids := make([]string, len(candidates))
	for i, c := range candidates {
		ids[i] = c.Node.Id
	}
	chargeMap := chargeMapForThoughts(ctx, gc, ids)

	var results []ThoughtResult
	for _, c := range candidates {
		if !thoughtMatchesFilters(ctx, gc, c.Node, opts) {
			continue
		}

		props := computePropertiesFromCharges(chargeMap[c.Node.Id])

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

// FormatRecallResults formats recall results based on mode. Verbatim from
// pkg/thought/query.go — pure rendering, no graph access.
func FormatRecallResults(results []ThoughtResult, mode string) string {
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
	}

	return sb.String()
}
