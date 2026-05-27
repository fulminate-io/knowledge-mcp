// SPDX-License-Identifier: Apache-2.0

package exposure

// k8s_public_exposure.go implements the K8sPublicExposureAnalyzer — the
// Kubernetes-family sibling to AWSPublicExposureAnalyzer. It walks paths
// from public K8s entry points (LoadBalancer Services, Ingresses) to
// sensitive terminal resources (Secrets, IRSA-admin ServiceAccounts)
// through pre-resolved NetworkPolicy edges, Service selectors, and
// workload→SA relationships.
//
// Architecture: thin wrapper. Shared BFS walker + scoring + sensitive
// classifier. Edge set restricted to K8s-specific types so an AWS-only
// chain (e.g. internet-facing ALB → SG → EC2) cannot accidentally match
// a K8s analyzer run.
//
// LAYERING. topology/ must not import cloud/k8s/ — NetworkPolicy rule
// resolution lives in cloud/k8s/postpopulate*.go.

import (
	"context"
	"fmt"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// K8sPublicExposureAnalyzer is the topology.Analyzer implementation for
// K8s public-exposure composition.
type K8sPublicExposureAnalyzer struct{}

// Name returns the analyzer's stable identifier.
func (K8sPublicExposureAnalyzer) Name() string { return "k8s_public_exposure" }

// k8sPublicExposureEdgeTypes is the edge-type set the K8s walker follows.
// Exposed at package scope so unified_public_exposure.go can combine it
// with the AWS set without duplicating the list.
var k8sPublicExposureEdgeTypes = []kgtypes.EdgeType{
	kgtypes.EdgeSelects,           // Service → pods
	kgtypes.EdgeRoutesTo,          // Ingress → Service
	kgtypes.EdgeAllowsIngressFrom, // NetworkPolicy-resolved ingress
	kgtypes.EdgeAllowsEgressTo,    // NetworkPolicy-resolved egress
	kgtypes.EdgeANPIngressFrom,    // AdminNetworkPolicy ingress
	kgtypes.EdgeANPEgressTo,       // AdminNetworkPolicy egress
	kgtypes.EdgeMountsSecret,      // workload → secret
	kgtypes.EdgeMountsConfigMap,   // workload → configmap
	kgtypes.EdgeUsesSA,            // workload → serviceaccount
	kgtypes.EdgeBindsRole,         // rolebinding → role/clusterrole
	kgtypes.EdgeBindsSubject,      // rolebinding → serviceaccount
	kgtypes.EdgeOwnedBy,           // child → parent (pod → ReplicaSet → Deployment)
}

// Run executes the K8s public-exposure walker. Same shape as
// AWSPublicExposureAnalyzer.Run — delegates to the shared walker with a
// cloud-filter of "k8s" and the K8s edge-type set.
func (a K8sPublicExposureAnalyzer) Run(ctx context.Context, req Request) ([]Finding, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("topology/k8s_public_exposure: %w", err)
	}
	if req.Graph != kgtypes.GraphCloud {
		return nil, nil
	}
	if req.Caller == nil {
		return nil, fmt.Errorf("topology/k8s_public_exposure: req.Caller must not be nil")
	}
	if req.Name == "" {
		return nil, fmt.Errorf("topology/k8s_public_exposure: req.Name (account) must not be empty")
	}

	scoped := newCloudReader(req.Caller, req.Name)

	seeds := enumerateSeeds(ctx, scoped, "k8s")
	if len(seeds) == 0 {
		return nil, nil
	}

	cfg := walkerConfig{
		scoped:     scoped,
		rootCaller: req.Caller,
		EdgeTypes:  k8sPublicExposureEdgeTypes,
		MaxDepth:   extractExtraInt(req.Extra, "max_depth", defaultMaxExposureDepth, maxExposureDepthCeiling),
		Account:    req.Name,
	}
	var paths []attackPath
	for _, seed := range seeds {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("topology/k8s_public_exposure: %w", err)
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

// init self-registers the analyzer with the topology registry.
func init() {
	Register(K8sPublicExposureAnalyzer{})
}
