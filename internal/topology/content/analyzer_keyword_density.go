// SPDX-License-Identifier: Apache-2.0

package content

// analyzer_keyword_density.go — KeywordDensityAnalyzer measures how
// frequently a caller-supplied regex matches a chosen "target" field across
// every node in a scoped graph. A single analyzer with four target modes —
// heading / attribute / title / content — rather than four separate
// analyzers, so the same registration surface and Finding schema serves
// every density pattern the consuming pipeline needs.
//
// The algorithm is the original pkg/topology body verbatim; only the node
// source swaps from the in-process store reads to the foundation wire helpers:
// the typed targets (heading → [heading, section], title → [page]) fetch their
// nodes via FetchNodesByType, and the every-type targets (attribute, content)
// fetch via FetchAllNodes.
//
// Parameters (req.Extra):
//   - keyword_regex: Go regexp pattern to match (REQUIRED; empty → error).
//     The regex is applied verbatim to the target field; callers wanting
//     case-insensitive matching must pass "(?i)..." themselves.
//   - target: one of "heading" | "attribute" | "title" | "content" (REQUIRED;
//     empty or unknown → error). Chooses which node field participates:
//     heading  → nodes of Type "heading" OR "section" (SymbolName carries the
//                heading text in the web collector's emission shape).
//     attribute→ every node; matches Metadata["class"], ["id"], or ["role"].
//     title    → nodes of Type "page"; matches SymbolName / Metadata["title"].
//     content  → every node with non-empty Content; matches Content.
//
// Emits a single aggregate Finding with Metrics["density"],
// Metrics["matched_count"], Metrics["total_scanned"], and Evidence holding
// up to keywordEvidencePreviewLimit matching node IDs. Severity is
// percentile-ranked against density (100 * density as the rank).

import (
	"context"
	"fmt"
	"regexp"
	"sort"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

// keywordEvidencePreviewLimit caps how many matching node IDs are
// stored in the Finding's Evidence slice. Large graphs can produce
// hundreds of matches; persisting every match as a relates-to edge
// would balloon the knowledge graph and provide no additional signal.
const keywordEvidencePreviewLimit = 10

// Valid target-mode values. Keeping these as a constant set lets
// validateKeywordDensityParams reject unknown targets with a helpful
// error message.
const (
	keywordTargetHeading   = "heading"
	keywordTargetAttribute = "attribute"
	keywordTargetTitle     = "title"
	keywordTargetContent   = "content"
)

// KeywordDensityAnalyzer measures the fraction of target-field values that
// match a caller-supplied regex. Zero-value usable; self-registers via
// init().
type KeywordDensityAnalyzer struct{}

// Name returns the analyzer's stable identifier.
func (KeywordDensityAnalyzer) Name() string { return "keyword-density" }

// Run validates the required params, compiles the regex, iterates the
// scoped graph counting matches on the chosen target field, and emits a
// single aggregate Finding. Empty graphs produce zero findings (the caller
// can distinguish "no nodes" from "no matches" via Metrics["total_scanned"]).
func (a KeywordDensityAnalyzer) Run(ctx context.Context, req foundation.Request) ([]foundation.Finding, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("topology/keyword-density: %w", err)
	}

	re, target, err := validateKeywordDensityParams(req)
	if err != nil {
		return nil, err
	}

	matched, totalScanned, matchIDs, err := scanKeywordDensity(ctx, req, re, target)
	if err != nil {
		return nil, err
	}
	if totalScanned == 0 {
		return nil, nil
	}

	return []foundation.Finding{buildKeywordDensityFinding(matched, totalScanned, matchIDs, target, re.String())}, nil
}

// validateKeywordDensityParams returns the compiled regex and validated
// target mode, or an error explaining which parameter is wrong. Empty
// keyword_regex, unknown target, and regex syntax errors each produce a
// distinct error string so the caller can react appropriately.
func validateKeywordDensityParams(req foundation.Request) (*regexp.Regexp, string, error) {
	raw := foundation.ExtraString(req, "keyword_regex", "")
	if raw == "" {
		return nil, "", fmt.Errorf("topology/keyword-density: req.Extra[\"keyword_regex\"] is required")
	}
	re, err := regexp.Compile(raw)
	if err != nil {
		return nil, "", fmt.Errorf("topology/keyword-density: invalid regex %q: %w", raw, err)
	}

	target := foundation.ExtraString(req, "target", "")
	switch target {
	case keywordTargetHeading, keywordTargetAttribute, keywordTargetTitle, keywordTargetContent:
		return re, target, nil
	case "":
		return nil, "", fmt.Errorf("topology/keyword-density: req.Extra[\"target\"] is required (heading|attribute|title|content)")
	default:
		return nil, "", fmt.Errorf("topology/keyword-density: unknown target %q (want heading|attribute|title|content)", target)
	}
}

// scanKeywordDensity fetches the nodes the target mode participates in and
// tallies regex matches against the target-specific text. The typed targets
// (heading / title) restrict to specific NodeTypes via FetchNodesByType; the
// attribute / content targets cross every type and use FetchAllNodes. Returns
// (matched, totalScanned, matchIDs, err).
func scanKeywordDensity(ctx context.Context, req foundation.Request, re *regexp.Regexp, target string) (int, int, []string, error) {
	nodes, err := fetchKeywordTargetNodes(ctx, req, target)
	if err != nil {
		return 0, 0, nil, err
	}

	matched := 0
	totalScanned := 0
	matchIDs := make([]string, 0, keywordEvidencePreviewLimit)
	for _, n := range nodes {
		if n == nil {
			continue
		}
		text, ok := keywordTargetText(n, target)
		if !ok {
			continue
		}
		totalScanned++
		if re.MatchString(text) {
			matched++
			if len(matchIDs) < keywordEvidencePreviewLimit {
				matchIDs = append(matchIDs, n.Id)
			}
		}
	}
	return matched, totalScanned, matchIDs, nil
}

// fetchKeywordTargetNodes fetches the node set the target mode scans: the
// heading / title targets route through FetchNodesByType (one Execute per
// participating type — heading covers both "heading" and "section"); the
// attribute / content targets scan every type via FetchAllNodes. This mirrors
// the original keywordTargetTypes typed-index fast path vs full scan split.
func fetchKeywordTargetNodes(ctx context.Context, req foundation.Request, target string) ([]*knowledgev1.Node, error) {
	types := keywordTargetTypes(target)
	if len(types) == 0 {
		nodes, err := foundation.FetchAllNodes(ctx, req.Caller, req.Graph, req.Name)
		if err != nil {
			return nil, fmt.Errorf("topology/keyword-density: fetch nodes %s/%s: %w", req.Graph, req.Name, err)
		}
		return nodes, nil
	}
	var nodes []*knowledgev1.Node
	for _, t := range types {
		typed, err := foundation.FetchNodesByType(ctx, req.Caller, req.Graph, req.Name, t)
		if err != nil {
			return nil, fmt.Errorf("topology/keyword-density: fetch nodes-by-type %s %s/%s: %w", t, req.Graph, req.Name, err)
		}
		nodes = append(nodes, typed...)
	}
	return nodes, nil
}

// keywordTargetTypes returns the set of NodeTypes that participate in
// the named target mode, or nil when the target needs to scan every
// type (attribute / content modes). Used by fetchKeywordTargetNodes to
// route through the typed-fetch fast path when possible.
func keywordTargetTypes(target string) []kgtypes.NodeType {
	switch target {
	case keywordTargetHeading:
		return []kgtypes.NodeType{"heading", "section"}
	case keywordTargetTitle:
		return []kgtypes.NodeType{"page"}
	}
	return nil
}

// keywordTargetText returns (text, true) when the node participates in the
// chosen target mode, or ("", false) when it should be skipped. Centralizing
// the per-target selection rule keeps scanKeywordDensity under the function-
// length cap and makes it trivial to add a new target mode later.
func keywordTargetText(n *knowledgev1.Node, target string) (string, bool) {
	switch target {
	case keywordTargetHeading:
		if n.Type != "heading" && n.Type != "section" {
			return "", false
		}
		return n.SymbolName, true
	case keywordTargetAttribute:
		parts := make([]string, 0, 3)
		for _, k := range []string{"class", "id", "role"} {
			if v := kgtypes.Value(n, k); v != "" {
				parts = append(parts, v)
			}
		}
		if len(parts) == 0 {
			return "", false
		}
		return joinAttrs(parts), true
	case keywordTargetTitle:
		if n.Type != "page" {
			return "", false
		}
		if t := kgtypes.Value(n, "title"); t != "" {
			return t, true
		}
		if n.SymbolName == "" {
			return "", false
		}
		return n.SymbolName, true
	case keywordTargetContent:
		if n.Content == "" {
			return "", false
		}
		return n.Content, true
	}
	return "", false
}

// joinAttrs concatenates attribute values with a single space separator so
// a caller's regex can match across class + id + role in one pass without
// this analyzer having to run the regex three times per node.
func joinAttrs(parts []string) string {
	// Small slice (≤3 entries by construction) — a manual join avoids
	// importing strings for a single operation and keeps the analyzer's
	// imports minimal.
	switch len(parts) {
	case 1:
		return parts[0]
	case 2:
		return parts[0] + " " + parts[1]
	case 3:
		return parts[0] + " " + parts[1] + " " + parts[2]
	}
	return ""
}

// buildKeywordDensityFinding packages the matched / totalScanned counts and
// up-to-10-match Evidence into a single Finding. Severity ranks the density
// on the 0..100 scale (density * 100) through SeverityFromPercentile so a
// "top 1% of pages match" density hits SeverityCritical.
func buildKeywordDensityFinding(matched, totalScanned int, matchIDs []string, target, pattern string) foundation.Finding {
	density := float64(matched) / float64(totalScanned)
	sev := foundation.SeverityFromPercentile(density * 100)

	preview := matchIDs
	if len(preview) > keywordEvidencePreviewLimit {
		preview = preview[:keywordEvidencePreviewLimit]
	}
	evidence := make([]string, len(preview))
	copy(evidence, preview)

	sort.Strings(evidence) // deterministic primary-evidence ordering

	return foundation.Finding{
		Algorithm: "keyword-density",
		Severity:  sev,
		Title:     fmt.Sprintf("Keyword density on %s: %.1f%% (%d / %d)", target, density*100, matched, totalScanned),
		Summary: fmt.Sprintf(
			"Regex %q matched %d of %d %s-target nodes (density=%.4f).",
			pattern, matched, totalScanned, target, density,
		),
		Evidence: evidence,
		Metrics: map[string]float64{
			"density":       density,
			"matched_count": float64(matched),
			"total_scanned": float64(totalScanned),
		},
		Metadata: map[string]string{
			"target":        target,
			"keyword_regex": pattern,
		},
	}
}

func init() {
	foundation.Register(KeywordDensityAnalyzer{})
}

var _ foundation.Analyzer = KeywordDensityAnalyzer{}
