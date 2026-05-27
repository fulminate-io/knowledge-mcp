// SPDX-License-Identifier: Apache-2.0

package exposure

// public_exposure_seeds_k8s.go holds the Kubernetes seed rules for the
// public_exposure analyzer family. Parses node.Content via local anonymous
// structs (topology/ cannot import cloud/k8s/) or reads collector-written
// metadata when the collector already flattened the relevant fields.
//
// Rules (cloud family "k8s"):
//
//   - Service — spec.type=LoadBalancer, score 0.9
//   - Ingress — present with at least one rule, score 0.8
//
// A K8s LoadBalancer Service provisions a cloud-provider load balancer
// (AWS NLB/ALB, GCP TCP LB, Azure Load Balancer) pointing at the backing
// pods, which is a direct public-internet entry. An Ingress routes
// external HTTP(S) traffic through an Ingress controller, also public.
//
// Cluster-internal Service types (ClusterIP, NodePort) are NOT seeds —
// NodePort is reachable from within the cluster network but not from the
// public internet by default, and cluster-only tools like kube-proxy
// expose it anyway.

import (
	"context"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

func init() {
	registerSeedRule("Service", "k8s", k8sServiceSeedRule)
	registerSeedRule("Ingress", "k8s", k8sIngressSeedRule)
}

// k8sServiceSeedRule fires when the Service is of type LoadBalancer. The
// k8s collector flattens spec.type into Metadata["type"] (see
// cloud/k8s/sub_services.go).
func k8sServiceSeedRule(_ context.Context, _ *cloudReader, node *knowledgev1.Node) (*publicSeed, error) {
	if nodeMeta(node, "type") != "LoadBalancer" {
		return nil, nil
	}
	return &publicSeed{
		NodeID:     node.Id,
		EntryScore: 0.9,
		Reason:     "Kubernetes Service of type LoadBalancer",
	}, nil
}

// k8sIngressSeedRule fires for every Ingress node. Unlike a Service, we
// can't tell from metadata alone whether the Ingress controller is
// actually reachable from the internet (that depends on how the cluster
// is wired), so we flag every Ingress at the slightly lower score 0.8 to
// reflect the uncertainty. Operators running private clusters will see
// these as false positives; the tradeoff is simple and tunable.
func k8sIngressSeedRule(_ context.Context, _ *cloudReader, node *knowledgev1.Node) (*publicSeed, error) {
	// Ingress nodes always exist because the collector only emits nodes
	// for real K8s objects. No content parsing needed — the presence of
	// an Ingress node IS the signal.
	return &publicSeed{
		NodeID:     node.Id,
		EntryScore: 0.8,
		Reason:     "Kubernetes Ingress — external HTTP entry",
	}, nil
}
