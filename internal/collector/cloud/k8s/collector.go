// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/fulminate-io/knowledge-mcp/internal/collector"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/collectorwire"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

func init() {
	collector.Register(&K8sCollector{})
}

// K8sCollector discovers Kubernetes resources and builds a cloud infrastructure graph.
// It collects workloads, RBAC, networking, storage, config, batch, and CRD resources,
// then wires them together via PostPopulate for label-selector-based edges.
type K8sCollector struct{}

// Name returns the collector registration key.
func (c *K8sCollector) Name() string { return "k8s" }

// Collect enumerates all resources in a Kubernetes cluster and returns them as graph nodes.
// If id is non-empty, it is treated as a kubeconfig context name (used for cascade from
// EKS/GKE/AKS). Otherwise, KUBECONFIG env is used with default loading rules.
// The graph name is the resolved kubeconfig context name.
func (c *K8sCollector) Collect(ctx context.Context, id string, opts collector.CollectOptions) (*collectorwire.CollectResult, error) {
	bundle, err := buildClient(id)
	if err != nil {
		return nil, fmt.Errorf("k8s: %w", err)
	}

	subs := buildSubCollectors(bundle)

	nodes, edges, targets, subErr := cloud.RunSubCollectors(ctx, subs, cloud.RunOptions{
		OnProgress: opts.OnProgress,
	})
	if subErr != nil {
		if len(nodes) == 0 {
			return nil, fmt.Errorf("k8s: all subcollectors failed: %w", subErr)
		}
		slog.Warn("k8s: partial collection errors", "err", subErr)
	}

	result := &collectorwire.CollectResult{
		GraphType: kgtypes.GraphCloud,
		GraphName: bundle.contextName,
		Nodes:     nodes,
		Edges:     edges,
		// The enumeration was complete only if no subcollector failed. A partial
		// enumeration must never assert a complete walk: walk_complete is what arms
		// the server's whole-remainder deletion basis, so a resource this run failed
		// to READ would be named as deleted. The Warn above stays the operator signal.
		WalkComplete: subErr == nil,
	}

	// Emit AKS+EKS cluster linkage proxy + RUNS_IN_CLUSTER edges
	// client-side, before the result is shipped via Sink.WriteResult.
	// GKE keeps using the existing server-side resolveClusterLinkage path.
	emitClusterLinkageClientSide(ctx, bundle.contextName, result)

	// Cascade to cloud providers for discovered targets.
	cs := cloud.CascadeSetFrom(ctx)
	rm := cloud.ResolutionMapFrom(ctx)
	for _, t := range targets {
		if cs != nil && !cs.Mark(t.Collector, t.ID) {
			continue // already visited
		}
		if rm != nil {
			rm.Record(t.ID, t.ResolutionID)
		}
		if cascadeErr := collector.Collect(ctx, t.Collector, t.ID, opts); cascadeErr != nil {
			// Best-effort: log and continue to the next cascade target so
			// one bad provider doesn't sink the whole collection.
			slog.Warn("k8s: cascade failed",
				"collector", t.Collector, "id", t.ID, "err", cascadeErr)
			continue
		}
	}

	return result, nil
}

// buildSubCollectors creates all k8s subcollectors from a client bundle.
func buildSubCollectors(bundle *clientBundle) []cloud.SubCollector {
	cs := bundle.clientset
	return []cloud.SubCollector{
		// Workloads (Phase 2)
		&deploymentsSubCollector{clientset: cs},
		&statefulSetsSubCollector{clientset: cs},
		&daemonSetsSubCollector{clientset: cs},
		&replicaSetsSubCollector{clientset: cs},
		&podsSubCollector{clientset: cs},

		// RBAC (Phase 3)
		&serviceAccountsSubCollector{clientset: cs},
		&rolesSubCollector{clientset: cs},
		&roleBindingsSubCollector{clientset: cs},

		// Networking (Phase 4)
		&servicesSubCollector{clientset: cs},
		&endpointSlicesSubCollector{clientset: cs},
		&ingressesSubCollector{clientset: cs},
		&networkPoliciesSubCollector{clientset: cs},
		&adminNetworkPoliciesSubCollector{dynamicClient: bundle.dynamicClient},

		// Storage (Phase 5)
		&persistentVolumesSubCollector{clientset: cs},
		&pvcsSubCollector{clientset: cs},
		&storageClassesSubCollector{clientset: cs},

		// Config and Batch (Phase 6)
		&configMapsSubCollector{clientset: cs},
		&secretsSubCollector{clientset: cs},
		&jobsSubCollector{clientset: cs},

		// Cluster
		&namespacesSubCollector{clientset: cs},
		&nodesSubCollector{clientset: cs},

		// Autoscaling
		&hpaSubCollector{clientset: cs},
		&pdbSubCollector{clientset: cs},

		// Gateway API (dynamic client — CRDs may not be installed)
		&gatewayAPISubCollector{dynamicClient: bundle.dynamicClient},

		// CRDs (Phase 7)
		&crdsSubCollector{
			clientset:     cs,
			dynamicClient: bundle.dynamicClient,
			crdLister:     &apiextensionsCRDLister{client: bundle.apiextensionsCS},
		},
	}
}
