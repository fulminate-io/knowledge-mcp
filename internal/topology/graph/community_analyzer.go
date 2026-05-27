// SPDX-License-Identifier: Apache-2.0

package graph

// community_analyzer.go — CommunityAnalyzer wraps the BuildAdjacency +
// RunLeiden pipeline behind the standard foundation.Analyzer interface so
// the registry can hand it out by name to callers that don't want to
// know about Leiden internals.
//
// Behavior is intentionally minimal:
//
//   - Run materializes (req.Caller, req.Graph, req.Name, req.Subset) into an
//     adjacency snapshot using BuildAdjacency with opts.NodeFilter set
//     from req.Subset.
//   - Runs RunLeiden at the gamma read from req.Extra["gamma"] (default
//     0.5).
//   - Emits one Finding per community whose size meets communityMinSize
//     (5 nodes). When req.TopK > 0 the findings are sorted by size
//     descending and truncated to TopK; when TopK == 0 every qualifying
//     community is emitted.
//   - Severity is fixed at SeverityInfo — community membership is an
//     organizational signal, not an alarm.

import (
	"context"
	"fmt"
	"sort"

	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

// communityMinSize is the smallest community that produces a Finding.
// Smaller communities are too noisy to surface as their own organizing
// signal — most graphs contain a long tail of two- and three-node
// clusters that contribute nothing to the user's mental map.
const communityMinSize = 5

// communityGamma is the default resolution parameter passed to RunLeiden
// by the CommunityAnalyzer. 0.5 is the same default used by every
// existing community-detection caller in the codebase. Callers that need
// a different resolution pass it via req.Extra["gamma"]; the constant
// remains the fallback when the Extra map is absent, empty, or carries
// a non-positive value that fails the validator.
const communityGamma = 0.5

// communityEvidencePreviewLimit caps how many member node IDs are
// stored in a Finding's Evidence slice. Communities can be very large
// (hundreds of nodes); persisting every member as a relates-to edge
// would balloon the knowledge graph and provide no additional signal
// once the renderer has truncated the preview anyway.
const communityEvidencePreviewLimit = 20

// CommunityAnalyzer wraps RunLeiden behind the foundation.Analyzer
// interface. Zero-value usable.
type CommunityAnalyzer struct{}

// Name returns the analyzer's stable identifier.
func (CommunityAnalyzer) Name() string { return "community" }

// Run materializes the request graph, runs Leiden over it, and emits
// one Finding per community of size >= communityMinSize. When
// req.TopK > 0 the result is sorted by size descending and truncated
// to TopK; otherwise every qualifying community is emitted.
func (a CommunityAnalyzer) Run(ctx context.Context, req foundation.Request) ([]foundation.Finding, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("topology/community: %w", err)
	}

	gamma := foundation.ExtraFloat(req, "gamma", communityGamma, func(v float64) bool { return v > 0 })

	nodeIDs, communityOf, err := Detect(
		ctx,
		req.Caller,
		req.Graph,
		req.Name,
		gamma,
		BuildAdjacencyOpts{NodeFilter: req.Subset},
	)
	if err != nil {
		return nil, fmt.Errorf("topology/community: detect: %w", err)
	}

	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("topology/community: %w", err)
	}

	groups := make(map[string][]string, len(communityOf))
	for _, id := range nodeIDs {
		c := communityOf[id]
		groups[c] = append(groups[c], id)
	}

	findings := buildCommunityFindings(groups, gamma)
	if req.TopK > 0 && len(findings) > req.TopK {
		findings = findings[:req.TopK]
	}
	return findings, nil
}

// buildCommunityFindings converts the (community ID → member IDs) map
// into a deterministic slice of Findings. Members are sorted within
// each community for stable evidence preview ordering, and the result
// slice is sorted by community size descending then by community ID
// ascending so that callers passing TopK get the largest communities
// first with stable tie-breaks.
func buildCommunityFindings(groups map[string][]string, gamma float64) []foundation.Finding {
	findings := make([]foundation.Finding, 0, len(groups))
	for commID, members := range groups {
		if len(members) < communityMinSize {
			continue
		}
		sortedMembers := make([]string, len(members))
		copy(sortedMembers, members)
		sort.Strings(sortedMembers)
		findings = append(findings, buildOneCommunityFinding(commID, sortedMembers, gamma))
	}
	sort.SliceStable(findings, func(i, j int) bool {
		si := int(findings[i].Metrics["size"])
		sj := int(findings[j].Metrics["size"])
		if si != sj {
			return si > sj
		}
		return primaryEvidence(findings[i]) < primaryEvidence(findings[j])
	})
	return findings
}

// buildOneCommunityFinding constructs the Finding for a single
// community. The Evidence slice holds at most communityEvidencePreviewLimit
// member IDs, the first of which is the deterministic primary evidence.
func buildOneCommunityFinding(commID string, sortedMembers []string, gamma float64) foundation.Finding {
	size := len(sortedMembers)
	preview := sortedMembers
	if size > communityEvidencePreviewLimit {
		preview = sortedMembers[:communityEvidencePreviewLimit]
	}
	evidence := make([]string, len(preview))
	copy(evidence, preview)

	title := fmt.Sprintf("Community %s: %d nodes", commID, size)
	summary := fmt.Sprintf(
		"Densely connected cluster of %d nodes detected by Leiden community "+
			"detection (γ=%.2f). Members may share an emergent topic or domain "+
			"that is not encoded by an explicit container node.",
		size, gamma,
	)

	return foundation.Finding{
		Algorithm: "community",
		Severity:  foundation.SeverityInfo,
		Title:     title,
		Summary:   summary,
		Evidence:  evidence,
		Metrics: map[string]float64{
			"size":  float64(size),
			"gamma": gamma,
		},
	}
}

// init self-registers the CommunityAnalyzer with the foundation registry.
func init() {
	foundation.Register(CommunityAnalyzer{})
}

// Compile-time interface assertion. The Analyzer interface is small and
// rarely changes, but a build-time check guarantees this file fails fast
// if either side drifts.
var _ foundation.Analyzer = CommunityAnalyzer{}
