// SPDX-License-Identifier: Apache-2.0

package content

// analyzer_degree_histogram.go — DegreeHistogramAnalyzer bins nodes by
// in-degree / out-degree / total-degree into fixed buckets and emits a
// single Finding holding all three distributions. Distinct from the
// ranked fan_in / fan_out / degree_total analyzers (graph family) — those
// surface one per-node Finding for each god-object candidate, ranked by
// degree. This analyzer describes GRAPH SHAPE in aggregate (catalog-heavy
// vs glossary-heavy vs long-tail), which the consuming profiler uses to
// seed chunk-classification rules.
//
// The histogram math is the original pkg/topology body verbatim. The degree
// SOURCE changes: the original consumed the graph family's shared
// computeDegrees pass (degree.go), but that lives in the disjoint parallel
// graph package this one cannot import. Per the plan, degree counts here come
// from ONE bulk edge read — the match-all read when no subset predicate narrows
// the node set, else a pivot read over the surviving ids — feeding a slim local
// degree-row compute (in/out totals only; the histogram never reads the
// per-edge-type breakdown).
//
// Default buckets (inclusive lower, inclusive upper unless noted):
//
//	bucket_0   : degree == 0
//	bucket_1   : degree == 1
//	bucket_2   : degree == 2
//	bucket_3_5 : 3..5
//	bucket_6_10: 6..10
//	bucket_11_25 : 11..25
//	bucket_26_100 : 26..100
//	bucket_101_1000 : 101..1000
//	bucket_1001_plus : 1001+
//
// Metrics keys carry the dimension prefix "in:", "out:", "total:" so
// callers can read all three distributions from one Finding.

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

// degreeRow captures the per-node aggregate in/out counts for one node. Unlike
// the graph family's degreeRow it carries no per-edge-type breakdown — the
// histogram only reads FanIn / FanOut / total / StringID.
type degreeRow struct {
	StringID string
	FanIn    int
	FanOut   int
}

// total returns the combined in-degree + out-degree for the row.
func (r degreeRow) total() int { return r.FanIn + r.FanOut }

// degreeBucket describes one histogram bucket and its inclusive-inclusive
// range. Upper == -1 means "no upper bound" for the open-ended top bucket.
type degreeBucket struct {
	Label string
	Lower int
	Upper int
}

// defaultDegreeBuckets are the fixed buckets used when req.Extra does not
// override them. Keeping them as a package-level var rather than an
// Extra-driven list means every call site produces the same distribution
// shape, which is the point: the histogram is meant to be comparable across
// graphs.
var defaultDegreeBuckets = []degreeBucket{
	{Label: "bucket_0", Lower: 0, Upper: 0},
	{Label: "bucket_1", Lower: 1, Upper: 1},
	{Label: "bucket_2", Lower: 2, Upper: 2},
	{Label: "bucket_3_5", Lower: 3, Upper: 5},
	{Label: "bucket_6_10", Lower: 6, Upper: 10},
	{Label: "bucket_11_25", Lower: 11, Upper: 25},
	{Label: "bucket_26_100", Lower: 26, Upper: 100},
	{Label: "bucket_101_1000", Lower: 101, Upper: 1000},
	{Label: "bucket_1001_plus", Lower: 1001, Upper: -1},
}

// DegreeHistogramAnalyzer emits graph-shape distributions for in / out /
// total degree. Zero-value usable; self-registers via init().
type DegreeHistogramAnalyzer struct{}

// Name returns the analyzer's stable identifier.
func (DegreeHistogramAnalyzer) Name() string { return "degree-histogram" }

// Run computes per-node degree rows from one bulk edge fetch, tallies each
// row's fan_in / fan_out / total into the fixed bucket grid, and emits one
// aggregate Finding holding all three distributions.
func (a DegreeHistogramAnalyzer) Run(ctx context.Context, req foundation.Request) ([]foundation.Finding, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("topology/degree-histogram: %w", err)
	}
	rows, err := computeDegreeRows(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("topology/degree-histogram: %w", err)
	}
	if len(rows) == 0 {
		return nil, nil
	}

	inHist := tallyHistogram(rows, func(r degreeRow) int { return r.FanIn })
	outHist := tallyHistogram(rows, func(r degreeRow) int { return r.FanOut })
	totalHist := tallyHistogram(rows, func(r degreeRow) int { return r.total() })

	return []foundation.Finding{buildDegreeHistogramFinding(inHist, outHist, totalHist, rows)}, nil
}

// computeDegreeRows fetches every node in the scoped graph (applying the subset
// filter), fetches the edges in ONE bulk Execute, and tallies in/out degree per
// node in memory. It mirrors the original
// computeDegrees semantics exactly: an edge contributes to FanOut on its source
// only when the source is a materialized (subset-passing) node, and to FanIn on
// its destination only when BOTH endpoints are materialized — i.e. it counts
// the outgoing edges of materialized nodes, the same loop the original ran over
// scoped.IterEdges per materialized node. Self-loops are preserved (the
// original read the raw store edges, not the self-loop-filtered gonum graph).
func computeDegreeRows(ctx context.Context, req foundation.Request) ([]degreeRow, error) {
	nodes, err := foundation.FetchAllNodes(ctx, req.Caller, req.Graph, req.Name)
	if err != nil {
		return nil, fmt.Errorf("build node set %s/%s: %w", req.Graph, req.Name, err)
	}

	rowByID := make(map[string]*degreeRow, len(nodes))
	ids := make([]string, 0, len(nodes))
	for _, n := range nodes {
		if n == nil {
			continue
		}
		if req.Subset != nil && !req.Subset(n) {
			continue
		}
		if _, dup := rowByID[n.Id]; dup {
			continue
		}
		rowByID[n.Id] = &degreeRow{StringID: n.Id}
		ids = append(ids, n.Id)
	}
	if len(ids) == 0 {
		return nil, nil
	}

	// With no subset predicate the id set IS every node of the graph, so ask for
	// the graph's edges directly instead of sending all those ids back as a pivot
	// set. A subset build keeps the pivot read so it pulls only usable edges.
	// Either way the tally below ignores an edge whose source is not a
	// materialized row, so the two reads produce identical rows.
	var edges []knowledgev1.Edge
	if req.Subset == nil {
		edges, err = foundation.FetchAllEdges(ctx, req.Caller, req.Graph, req.Name, nil)
	} else {
		edges, err = foundation.FetchEdges(ctx, req.Caller, req.Graph, req.Name, ids, nil)
	}
	if err != nil {
		return nil, fmt.Errorf("fetch edges %s/%s: %w", req.Graph, req.Name, err)
	}
	for i := range edges {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		e := &edges[i]
		fromRow, ok := rowByID[e.FromId]
		if !ok {
			// Source not materialized — the original never iterated this
			// node's outgoing edges, so it contributes nothing.
			continue
		}
		fromRow.FanOut++
		if toRow, ok := rowByID[e.ToId]; ok {
			toRow.FanIn++
		}
	}

	rows := make([]degreeRow, 0, len(rowByID))
	for _, r := range rowByID {
		rows = append(rows, *r)
	}
	return rows, nil
}

// tallyHistogram walks the rows once, extracts each row's score via the scorer
// closure, and returns a label→count map with every defaultDegreeBuckets entry
// present (zero-count buckets keep the label so Metrics always has a consistent
// shape).
func tallyHistogram(rows []degreeRow, scorer func(degreeRow) int) map[string]int {
	hist := make(map[string]int, len(defaultDegreeBuckets))
	for _, b := range defaultDegreeBuckets {
		hist[b.Label] = 0
	}
	for _, r := range rows {
		label := bucketLabel(scorer(r))
		hist[label]++
	}
	return hist
}

// bucketLabel maps a raw degree value to the label of the bucket it lands in.
// Unknown / negative values (shouldn't occur) fall into bucket_0 for safety.
func bucketLabel(degree int) string {
	if degree < 0 {
		return "bucket_0"
	}
	for _, b := range defaultDegreeBuckets {
		if b.Upper == -1 && degree >= b.Lower {
			return b.Label
		}
		if degree >= b.Lower && degree <= b.Upper {
			return b.Label
		}
	}
	return defaultDegreeBuckets[len(defaultDegreeBuckets)-1].Label
}

// buildDegreeHistogramFinding assembles the aggregate Finding. Metrics carry
// "in:<label>", "out:<label>", "total:<label>" for every bucket so a downstream
// reader can read all three histograms from a single node. Evidence holds up to
// 3 representative node IDs from the highest non-empty total-degree bucket — a
// quick "example of the shape" handle for human reviewers.
func buildDegreeHistogramFinding(in, out, total map[string]int, rows []degreeRow) foundation.Finding {
	metrics := make(map[string]float64, len(defaultDegreeBuckets)*3+1)
	metrics["total_nodes"] = float64(len(rows))
	for _, b := range defaultDegreeBuckets {
		metrics["in:"+b.Label] = float64(in[b.Label])
		metrics["out:"+b.Label] = float64(out[b.Label])
		metrics["total:"+b.Label] = float64(total[b.Label])
	}

	return foundation.Finding{
		Algorithm: "degree-histogram",
		Severity:  foundation.SeverityInfo,
		Title:     fmt.Sprintf("Degree histogram: %d nodes", len(rows)),
		Summary:   summarizeHistogram(in, out, total),
		Evidence:  pickHistogramEvidence(rows, total),
		Metrics:   metrics,
		Metadata:  map[string]string{"scope": "aggregate"},
	}
}

// summarizeHistogram renders one line per dimension showing non-zero buckets
// with their counts, sorted by the bucket's declared order.
func summarizeHistogram(in, out, total map[string]int) string {
	render := func(name string, hist map[string]int) string {
		var parts []string
		for _, b := range defaultDegreeBuckets {
			if n := hist[b.Label]; n > 0 {
				parts = append(parts, fmt.Sprintf("%s=%d", b.Label, n))
			}
		}
		return name + ": " + joinCommaSpace(parts)
	}
	return render("in", in) + "; " + render("out", out) + "; " + render("total", total)
}

// joinCommaSpace is a tiny helper to avoid importing strings just for one Join
// call.
func joinCommaSpace(parts []string) string {
	if len(parts) == 0 {
		return ""
	}
	var s strings.Builder
	s.WriteString(parts[0])
	for i := 1; i < len(parts); i++ {
		s.WriteString(", ")
		s.WriteString(parts[i])
	}
	return s.String()
}

// pickHistogramEvidence returns up to 3 node IDs from the highest non-empty
// total-degree bucket, sorted ascending by ID for deterministic
// primary-evidence dedup.
func pickHistogramEvidence(rows []degreeRow, total map[string]int) []string {
	topLabel := ""
	for _, v := range slices.Backward(defaultDegreeBuckets) {
		label := v.Label
		if total[label] > 0 {
			topLabel = label
			break
		}
	}
	if topLabel == "" {
		return nil
	}
	var ids []string
	for _, r := range rows {
		if bucketLabel(r.total()) == topLabel {
			ids = append(ids, r.StringID)
			if len(ids) >= 3 {
				break
			}
		}
	}
	sort.Strings(ids)
	return ids
}

func init() {
	foundation.Register(DegreeHistogramAnalyzer{})
}

var _ foundation.Analyzer = DegreeHistogramAnalyzer{}
