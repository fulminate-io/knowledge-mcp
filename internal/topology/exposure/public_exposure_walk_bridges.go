// SPDX-License-Identifier: Apache-2.0

package exposure

// public_exposure_walk_bridges.go carries the cross-graph composition
// helpers for the unified public_exposure walker: linkage-graph bridge
// edge lookup and the inline IRSA shortcut.
//
// Separation rationale: bfsFromSeed in public_exposure_walk.go must stay
// under the topology 80-line production-function cap. Splitting the
// bridge helpers out also means the AWS-only and K8s-only wrappers can
// build without pulling in any linkage-graph code at all.

import (
	"context"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

// outgoingLinkageBridges returns cross-graph bridge edges from the
// linkage graph that originate at nodeID. The linkage graph stores proxy
// nodes with deterministic IDs "proxy:cloud:<account>:<foreign_id>" (see
// store/proxy.go for the canonical construction). When the walker is
// currently at a cloud node with foreign_id=nodeID inside the <account>,
// it materializes the corresponding proxy ID and walks the linkage
// graph's forward edges out of that proxy.
//
// The helper follows every outgoing edge type, not a restricted set:
// once we're in the linkage graph we accept any cross-domain edge
// (BUILDS, DEPLOYS, MANAGES, CONFIGURES, SERVES, uses, relates-to) as a
// valid composition step. Returning the target's foreign_id flips the
// walker back into the cloud-graph namespace, so the next BFS iteration
// can look up the target node in its own scoped reader.
//
// Returns nil on any error — linkage graph absence is a normal
// "partial-coverage" state, not a fatal condition.
func outgoingLinkageBridges(ctx context.Context, rootCaller foundation.GraphCaller, account, nodeID string) []walkEdge {
	if rootCaller == nil || nodeID == "" {
		return nil
	}
	linkReader := newLinkageReader(rootCaller)
	// Deterministic proxy ID mirrors the one createBridgeProxyPair
	// writes. When account is non-empty we use the cloud-proxy convention;
	// otherwise (tests, degraded state) we fall back to a name-only lookup
	// which will miss but not crash.
	proxyID := "proxy:cloud:" + account + ":" + nodeID
	edges, err := linkReader.iterEdges(ctx, proxyID, outgoingEdges, nil)
	if err != nil || len(edges) == 0 {
		return nil
	}
	out := make([]walkEdge, 0, len(edges))
	for _, e := range edges {
		targetForeignID := resolveLinkageProxyForeignID(ctx, linkReader, e.ToId)
		if targetForeignID == "" {
			continue
		}
		out = append(out, walkEdge{
			ToID: targetForeignID,
			Kind: kgtypes.EdgeType(e.Type),
			Metadata: map[string]string{
				"cross_graph":      "true",
				"linkage_edge":     e.Type,
				"linkage_proxy_to": e.ToId,
			},
		})
	}
	return out
}

// resolveLinkageProxyForeignID returns the foreign_id metadata of a
// linkage-graph proxy node, or "" if the node doesn't exist or isn't a
// proxy. This is the inverse of the proxy-ID convention: given a
// "proxy:*:<foreign_id>" node, we read the foreign_id back out of its
// metadata rather than parsing the ID string — that keeps us resilient
// to future changes in the proxy ID format.
func resolveLinkageProxyForeignID(ctx context.Context, linkReader *cloudReader, proxyID string) string {
	if linkReader == nil || proxyID == "" {
		return ""
	}
	n, err := linkReader.nodeByID(ctx, proxyID)
	if err != nil || n == nil {
		return ""
	}
	return nodeMeta(n, "foreign_id")
}

// outgoingIRSAInline returns a synthetic cross-graph edge from a K8s
// ServiceAccount node to the IAM role named by its "irsa_role_arn"
// metadata. This is a PRE-PERSISTENCE shortcut: the store-side dream
// bridge may or may not have materialized the linkage-graph proxy pair
// yet, but the annotation-based relationship is always available on the
// SA node itself (see cloud/k8s/sub_serviceaccounts.go). Following it
// here means the walker can compose AWS ↔ K8s paths today, and the
// proxy-pair code path in outgoingLinkageBridges layers on top without
// conflict.
//
// Returns an empty slice for non-ServiceAccount nodes or SAs without
// the annotation. Never returns an error — missing metadata is not a
// fatal condition.
func outgoingIRSAInline(ctx context.Context, scoped *cloudReader, nodeID string) []walkEdge {
	if scoped == nil || nodeID == "" {
		return nil
	}
	node, err := scoped.nodeByID(ctx, nodeID)
	if err != nil || node == nil {
		return nil
	}
	if nodeMeta(node, "resource_type") != "ServiceAccount" {
		return nil
	}
	roleARN := nodeMeta(node, "irsa_role_arn")
	if roleARN == "" {
		return nil
	}
	return []walkEdge{{
		ToID: roleARN,
		Kind: kgtypes.EdgeAssumesRole,
		Metadata: map[string]string{
			"cross_graph": "true",
			"bridge":      "irsa-inline",
		},
	}}
}
