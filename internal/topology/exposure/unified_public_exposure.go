// SPDX-License-Identifier: Apache-2.0

package exposure

// unified_public_exposure.go implements the UnifiedPublicExposureAnalyzer —
// the cross-cloud composer that stitches AWS and K8s attack paths into
// a single walker pass. This is the canonical analyzer for the
// "public ALB → EKS LoadBalancer Service → pod → IRSA IAM admin role"
// scenario the ticket calls out as the v2 motivating example.
//
// Architecture: thin wrapper, same pattern as aws_public_exposure.go and
// k8s_public_exposure.go, with two differences:
//
//  1. Seed enumeration uses an empty cloud-filter, so both AWS and K8s
//     public entries seed the walker.
//  2. walkerConfig.FollowLinkageBridge is true, which enables the
//     cross-graph IRSA bridge in public_exposure_walk_bridges.go. Paths
//     that traverse the K8s↔AWS boundary get cross_graph=true metadata
//     on the bridged edge, which is propagated into the finding metadata
//     by exposureFindingMetadata.
//
// LAYERING. topology/ must not import cloud/ — every cross-graph
// composition primitive lives in public_exposure_walk_bridges.go and
// reads the linkage graph via the generic wire API.

import (
	"context"
	"fmt"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// UnifiedPublicExposureAnalyzer implements topology.Analyzer for the
// cross-cloud public-exposure composer.
type UnifiedPublicExposureAnalyzer struct{}

// Name returns the analyzer's stable identifier.
func (UnifiedPublicExposureAnalyzer) Name() string { return "unified_public_exposure" }

// unifiedEdgeTypes merges the AWS, K8s, and additional cross-cloud edge
// types. The walker needs all of them because a unified path can cross
// provider boundaries: an ALB → EKS LB → pod path walks AWS edges
// first, then K8s edges, then (via a bridge) AWS/GCP/Azure edges again.
// Duplicates across the sets are tolerated by the wire query layer.
//
// Additional edge types beyond AWS + K8s:
//   - EdgeProtects: Cloud Armor security policy → backend service (GCP).
//     Required so the walker can traverse from a Cloud Armor seed to the
//     backend it protects and continue the path to downstream resources.
var unifiedEdgeTypes = append(
	append(append([]kgtypes.EdgeType{}, awsPublicExposureEdgeTypes...), k8sPublicExposureEdgeTypes...),
	kgtypes.EdgeProtects,
)

// Run executes the unified public-exposure walker. Follows both AWS and
// K8s seeds, composes across the linkage graph + the inline IRSA
// shortcut, and tags cross-graph findings in metadata.
func (a UnifiedPublicExposureAnalyzer) Run(ctx context.Context, req Request) ([]Finding, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("topology/unified_public_exposure: %w", err)
	}
	if req.Graph != kgtypes.GraphCloud {
		return nil, nil
	}
	if req.Caller == nil {
		return nil, fmt.Errorf("topology/unified_public_exposure: req.Caller must not be nil")
	}
	if req.Name == "" {
		return nil, fmt.Errorf("topology/unified_public_exposure: req.Name (account) must not be empty")
	}

	scoped := newCloudReader(req.Caller, req.Name)

	// Empty cloud-filter picks up both AWS and K8s seeds.
	seeds := enumerateSeeds(ctx, scoped, "")
	if len(seeds) == 0 {
		return nil, nil
	}

	cfg := walkerConfig{
		scoped:              scoped,
		rootCaller:          req.Caller,
		EdgeTypes:           unifiedEdgeTypes,
		MaxDepth:            extractExtraInt(req.Extra, "max_depth", defaultMaxExposureDepth, maxExposureDepthCeiling),
		FollowLinkageBridge: true,
		Account:             req.Name,
	}
	var paths []attackPath
	for _, seed := range seeds {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("topology/unified_public_exposure: %w", err)
		}
		paths = append(paths, bfsFromSeed(ctx, cfg, seed)...)
	}

	scored := scorePaths(paths)
	topN := extractExtraInt(req.Extra, "top_n", defaultExposureTopN, 10000)
	scored = pruneToTopN(scored, topN)

	findings := make([]Finding, 0, len(scored))
	for _, sp := range scored {
		findings = append(findings, buildExposureFinding(ctx, req, a.Name(), sp))
	}
	if req.TopK > 0 && len(findings) > req.TopK {
		findings = findings[:req.TopK]
	}
	return findings, nil
}

// init self-registers the unified analyzer.
func init() {
	Register(UnifiedPublicExposureAnalyzer{})
}
