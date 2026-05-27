// SPDX-License-Identifier: Apache-2.0

package exposure

// aws_public_exposure.go implements the AWSPublicExposureAnalyzer — a
// cloud-graph analyzer that detects multi-hop attack paths from public
// AWS entry points (internet-facing ALBs, Lambda function URLs, public
// S3 buckets, public API Gateway routes, EC2 with public IPs, RDS with
// PubliclyAccessible=true) to sensitive terminal resources (RDS,
// DynamoDB, KMS, Secrets Manager, admin-reachable IAM roles).
//
// Architecture: this file is a thin wrapper. It composes Phases 2-5:
//
//   - enumerateSeeds("aws") collects every public-entry AWS resource.
//   - bfsFromSeed walks the union of native cloud edges (SG attachment,
//     SG ingress/egress, IAM assume-role, LB target groups, cross-VPC)
//     until a sensitive terminal is reached.
//   - scorePaths turns path shape + sensitivity into a composite score.
//   - buildExposureFinding renders each scored path as a Finding.
//
// Cross-graph composition is OFF in this analyzer — if a path crosses
// into K8s (e.g. ALB → EKS LB → pod → IRSA IAM), the unified analyzer
// is responsible for that chain. AWS-only paths stay AWS-only.
//
// LAYERING. topology/ must not import cloud/aws/ — all AWS-specific
// resolution lives in cloud/aws/postpopulate*.go and the seed rules'
// local JSON parsers.

import (
	"context"
	"fmt"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// AWSPublicExposureAnalyzer implements topology.Analyzer for the AWS
// family of the public-exposure composer. Zero-value usable.
type AWSPublicExposureAnalyzer struct{}

// Name returns the analyzer's stable identifier. Findings emitted by
// Run carry this in their Algorithm field.
func (AWSPublicExposureAnalyzer) Name() string { return "aws_public_exposure" }

// awsPublicExposureEdgeTypes is the edge-type set the AWS walker follows.
// Pulled out as a package-level var so the unified analyzer can reuse
// it alongside the K8s set without re-declaring the list.
var awsPublicExposureEdgeTypes = []kgtypes.EdgeType{
	kgtypes.EdgeUsesSecurityGroup,
	kgtypes.EdgeAllowsIngressFrom,
	kgtypes.EdgeAllowsEgressTo,
	kgtypes.EdgeAssumesRole,
	kgtypes.EdgeTargets,
	kgtypes.EdgeRoutesTo,
	kgtypes.EdgePeeredWith,
	kgtypes.EdgeRoutesVia,
	kgtypes.EdgeExposedVia,
	kgtypes.EdgeEncryptsWith,
	kgtypes.EdgeMountsSecret,
	kgtypes.EdgeBoundTo,
	kgtypes.EdgeUsesSubnet,
	kgtypes.EdgeUsesNetwork,
}

// Run executes the AWS public-exposure walker against a single cloud
// account (req.Name). The method is deliberately short — it composes the
// shared walker / scorer / classifier modules and does almost no direct
// work itself.
func (a AWSPublicExposureAnalyzer) Run(ctx context.Context, req Request) ([]Finding, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("topology/aws_public_exposure: %w", err)
	}
	if req.Graph != kgtypes.GraphCloud {
		return nil, nil
	}
	if req.Caller == nil {
		return nil, fmt.Errorf("topology/aws_public_exposure: req.Caller must not be nil")
	}
	if req.Name == "" {
		return nil, fmt.Errorf("topology/aws_public_exposure: req.Name (account) must not be empty")
	}

	scoped := newCloudReader(req.Caller, req.Name)

	seeds := enumerateSeeds(ctx, scoped, "aws")
	if len(seeds) == 0 {
		return nil, nil
	}

	cfg := walkerConfig{
		scoped:     scoped,
		rootCaller: req.Caller,
		EdgeTypes:  awsPublicExposureEdgeTypes,
		MaxDepth:   extractExtraInt(req.Extra, "max_depth", defaultMaxExposureDepth, maxExposureDepthCeiling),
		Account:    req.Name,
	}
	var paths []attackPath
	for _, seed := range seeds {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("topology/aws_public_exposure: %w", err)
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

// init self-registers the analyzer with the topology registry so the
// dream topology phase picks it up automatically without an explicit
// import at the call site.
func init() {
	Register(AWSPublicExposureAnalyzer{})
}
