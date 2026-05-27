// SPDX-License-Identifier: Apache-2.0

package content

// analyzer_title_shape.go — TitleShapeAnalyzer measures the fraction of
// page-like-node titles that match a caller-supplied regex. Conceptually a
// narrowed variant of keyword-density with "target=title", but with a
// different surface: the per-match Evidence is stored as "<id>::<title>"
// strings so downstream consumers can render the matching titles verbatim
// without a second round-trip to resolve names.
//
// The algorithm is the original pkg/topology body verbatim; only the node
// source swaps from the in-process store.IterateAll(pageType) to a single
// foundation.FetchNodesByType(pageType) wire fetch.
//
// Parameters (req.Extra):
//   - pattern: Go regexp (REQUIRED; empty → error).
//   - page_type: node type treated as a "page" container. Default "page".

import (
	"context"
	"fmt"
	"regexp"
	"sort"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// titleEvidencePreviewLimit caps how many "<id>::<title>" entries live in
// the Finding's Evidence slice. Mirrors keyword-density's cap.
const titleEvidencePreviewLimit = 10

// TitleShapeAnalyzer reports the fraction of page titles matching a
// caller-supplied regex. Zero-value usable; self-registers via init().
type TitleShapeAnalyzer struct{}

// Name returns the analyzer's stable identifier.
func (TitleShapeAnalyzer) Name() string { return "title-shape-distribution" }

// Run validates params, walks page-typed nodes, tallies title-regex
// matches, and emits a single aggregate Finding with match_fraction and
// the first N matching titles in Evidence.
func (a TitleShapeAnalyzer) Run(ctx context.Context, req foundation.Request) ([]foundation.Finding, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("topology/title-shape-distribution: %w", err)
	}

	raw := foundation.ExtraString(req, "pattern", "")
	if raw == "" {
		return nil, fmt.Errorf("topology/title-shape-distribution: req.Extra[\"pattern\"] is required")
	}
	re, err := regexp.Compile(raw)
	if err != nil {
		return nil, fmt.Errorf("topology/title-shape-distribution: invalid regex %q: %w", raw, err)
	}
	pageType := kgtypes.NodeType(foundation.ExtraString(req, "page_type", "page"))

	nodes, err := foundation.FetchNodesByType(ctx, req.Caller, req.Graph, req.Name, pageType)
	if err != nil {
		return nil, fmt.Errorf("topology/title-shape-distribution: fetch nodes %s/%s: %w", req.Graph, req.Name, err)
	}

	matched, total, evidence := scanTitles(nodes, re)
	if total == 0 {
		return nil, nil
	}
	return []foundation.Finding{buildTitleShapeFinding(matched, total, evidence, pageType, re.String())}, nil
}

// scanTitles walks the page-typed node slice and returns the match count, the
// total-title count, and the "<id>::<title>" evidence preview list. The title
// text falls back to SymbolName when Metadata has no "title" key, matching the
// web collector's emission shape (page nodes carry the <h1> text as
// SymbolName).
func scanTitles(nodes []*knowledgev1.Node, re *regexp.Regexp) (int, int, []string) {
	matched := 0
	total := 0
	evidence := make([]string, 0, titleEvidencePreviewLimit)

	for _, n := range nodes {
		if n == nil {
			continue
		}
		title := kgtypes.Value(n, "title")
		if title == "" {
			title = n.SymbolName
		}
		if title == "" {
			continue
		}
		total++
		if re.MatchString(title) {
			matched++
			if len(evidence) < titleEvidencePreviewLimit {
				evidence = append(evidence, n.Id+"::"+title)
			}
		}
	}
	return matched, total, evidence
}

// buildTitleShapeFinding packages the match-fraction + Evidence preview
// into a single Finding. Severity is percentile-ranked on the 0..100
// match_fraction scale so "70% of titles match a pattern" hits
// SeverityInfo, "99%+" hits SeverityCritical.
func buildTitleShapeFinding(matched, total int, evidence []string, pageType kgtypes.NodeType, pattern string) foundation.Finding {
	fraction := float64(matched) / float64(total)
	sev := foundation.SeverityFromPercentile(fraction * 100)

	sort.Strings(evidence) // deterministic ordering for dedup + snapshot tests

	return foundation.Finding{
		Algorithm: "title-shape-distribution",
		Severity:  sev,
		Title:     fmt.Sprintf("Title-shape match: %.1f%% (%d / %d)", fraction*100, matched, total),
		Summary: fmt.Sprintf(
			"Regex %q matched %d of %d %s-typed titles (match_fraction=%.4f).",
			pattern, matched, total, pageType, fraction,
		),
		Evidence: evidence,
		Metrics: map[string]float64{
			"match_fraction": fraction,
			"matched_count":  float64(matched),
			"total_titles":   float64(total),
		},
		Metadata: map[string]string{
			"pattern":   pattern,
			"page_type": string(pageType),
		},
	}
}

func init() {
	foundation.Register(TitleShapeAnalyzer{})
}

var _ foundation.Analyzer = TitleShapeAnalyzer{}
