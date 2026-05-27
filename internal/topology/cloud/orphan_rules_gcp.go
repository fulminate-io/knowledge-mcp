// SPDX-License-Identifier: Apache-2.0

package cloud

import (
	"context"
	"fmt"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

// orphan_rules_gcp.go registers the v1 GCP orphan rules. v1 covers the
// two L7 load-balancer pieces that are most likely to be left behind
// after a deployment rollback or failed migration:
//
//   - gcp:compute:forwardingRule  → no outgoing TARGETS edges (conf 1.0)
//   - gcp:compute:backendService  → no outgoing TARGETS edges (conf 0.9)
//
// FORWARDING RULE — cloud/gcp/loadbalancer.go emits forwardingRule →
// targetProxy edges via EdgeTargets. A forwarding rule with no target
// proxy is unambiguously routing traffic nowhere; confidence 1.0.
//
// BACKEND SERVICE — cloud/gcp/loadbalancer.go emits backendService →
// instanceGroup (or NEG) edges via EdgeTargets. A backend service with no
// backends has nothing to send traffic to. The 0.9 (not 1.0) confidence
// reflects a known v1 limitation: backend services can also exist purely
// as urlMap ROUTES_TO targets that route traffic via static responses or
// redirects without having any actual backends. v1 does not distinguish
// these cases.

// Confidence constants for the GCP rules.
const (
	confidenceGCPForwardingRule = 1.0
	confidenceGCPBackendService = 0.9
	confidenceGCPStorageBucket  = 0.8
	confidenceGCPDisk           = 0.9
	confidenceGCPFirestoreDB    = 0.8
	confidenceGCPARRepo         = 0.8
	confidenceGCPKMSKey         = 0.9
	confidenceGCPRouter         = 0.7
	confidenceGCPSSLCert        = 0.9
	confidenceGCPAlertPolicy    = 0.9
)

// gcpForwardingRuleRule reports a GCP global forwarding rule as orphaned
// when it has no outbound TARGETS edge to a target proxy. The collector
// emits forwardingRule → targetProxy edges from rule.GetTarget(), so a
// forwarding rule with zero outbound TARGETS edges has nowhere to send
// traffic.
func gcpForwardingRuleRule(
	_ context.Context,
	_ foundation.GraphCaller,
	_ string,
	graph *orphanGraph,
	node *knowledgev1.Node,
) (bool, float64, string, error) {
	if graph.edges.hasOutgoing(node.Id, kgtypes.EdgeTargets) {
		return false, confidenceGCPForwardingRule, "", nil
	}
	return true, confidenceGCPForwardingRule,
		fmt.Sprintf("Forwarding rule %s has no target proxy.", displayName(node)),
		nil
}

// gcpBackendServiceRule reports a GCP backend service as orphaned when it
// has no outbound TARGETS edge to any instance group or NEG. cloud/gcp/
// loadbalancer.go iterates each backend service's GetBackends() entries
// and emits one TARGETS edge per backend, so a service with zero outbound
// TARGETS has zero backends.
func gcpBackendServiceRule(
	_ context.Context,
	_ foundation.GraphCaller,
	_ string,
	graph *orphanGraph,
	node *knowledgev1.Node,
) (bool, float64, string, error) {
	if graph.edges.hasOutgoing(node.Id, kgtypes.EdgeTargets) {
		return false, confidenceGCPBackendService, "", nil
	}
	return true, confidenceGCPBackendService,
		fmt.Sprintf("Backend service %s has no backends (instance groups or NEGs).", displayName(node)),
		nil
}

// gcpStorageBucketRule reports a GCS bucket as orphaned when it has no
// outbound edges. A healthy bucket emits at least one of: EdgeGrants
// (IAM bindings), EdgeTriggers (Pub/Sub notifications), EdgeSinksTo
// (log destination), or EdgeEncryptsWith (CMEK). Confidence is 0.8
// because an empty bucket with no IAM beyond allUsers is legitimate in
// some hosting scenarios.
func gcpStorageBucketRule(
	_ context.Context,
	_ foundation.GraphCaller,
	_ string,
	graph *orphanGraph,
	node *knowledgev1.Node,
) (bool, float64, string, error) {
	for _, edge := range []kgtypes.EdgeType{
		kgtypes.EdgeGrants,
		kgtypes.EdgeTriggers,
		kgtypes.EdgeSinksTo,
		kgtypes.EdgeEncryptsWith,
	} {
		if graph.edges.hasOutgoing(node.Id, edge) {
			return false, confidenceGCPStorageBucket, "", nil
		}
	}
	return true, confidenceGCPStorageBucket,
		fmt.Sprintf("Storage bucket %s has no outbound edges (no IAM, notifications, logging, or encryption).", displayName(node)),
		nil
}

// gcpDiskRule reports a GCP persistent disk as orphaned when it has no
// outbound BOUND_TO edges (not attached to any instance) and no source
// lineage edges (not created from a snapshot or image). A disk with none
// of these edges is unused and has no provenance — likely left behind
// after VM deletion. Confidence is 0.9 because some disks exist as
// template images or staging volumes that legitimately sit unattached.
func gcpDiskRule(
	_ context.Context,
	_ foundation.GraphCaller,
	_ string,
	graph *orphanGraph,
	node *knowledgev1.Node,
) (bool, float64, string, error) {
	for _, edge := range []kgtypes.EdgeType{
		kgtypes.EdgeBoundTo,
		kgtypes.EdgeFromSnapshot,
		kgtypes.EdgeFromImage,
		kgtypes.EdgeEncryptsWith,
	} {
		if graph.edges.hasOutgoing(node.Id, edge) {
			return false, confidenceGCPDisk, "", nil
		}
	}
	return true, confidenceGCPDisk,
		fmt.Sprintf("Disk %s is unattached and has no source lineage or encryption.", displayName(node)),
		nil
}

// gcpFirestoreDBRule reports a Firestore database as orphaned when it has
// no outbound IAM grants, no backup schedules, and no encryption. A
// database with none of these edges likely has no configured access and
// no disaster recovery plan.
func gcpFirestoreDBRule(
	_ context.Context,
	_ foundation.GraphCaller,
	_ string,
	graph *orphanGraph,
	node *knowledgev1.Node,
) (bool, float64, string, error) {
	for _, edge := range []kgtypes.EdgeType{
		kgtypes.EdgeGrants,
		kgtypes.EdgeBackedUpBy,
		kgtypes.EdgeEncryptsWith,
	} {
		if graph.edges.hasOutgoing(node.Id, edge) {
			return false, confidenceGCPFirestoreDB, "", nil
		}
	}
	return true, confidenceGCPFirestoreDB,
		fmt.Sprintf("Firestore database %s has no IAM grants, backup schedules, or encryption.",
			displayName(node)),
		nil
}

// gcpARRepoRule reports an Artifact Registry repository as orphaned when
// it has no IAM grants, no encryption, and no upstream proxy edges.
func gcpARRepoRule(
	_ context.Context,
	_ foundation.GraphCaller,
	_ string,
	graph *orphanGraph,
	node *knowledgev1.Node,
) (bool, float64, string, error) {
	for _, edge := range []kgtypes.EdgeType{
		kgtypes.EdgeGrants,
		kgtypes.EdgeEncryptsWith,
		kgtypes.EdgeProxiesFrom,
	} {
		if graph.edges.hasOutgoing(node.Id, edge) {
			return false, confidenceGCPARRepo, "", nil
		}
	}
	return true, confidenceGCPARRepo,
		fmt.Sprintf("Artifact Registry repository %s has no IAM grants, encryption, or upstream.",
			displayName(node)),
		nil
}

// gcpKMSKeyRule reports a KMS CryptoKey as orphaned when it has no outbound
// IAM grants AND no incoming EdgeEncryptsWith edges (nobody is using it for
// encryption). A key that is not referenced by any resource and has no IAM
// policy is likely leftover.
func gcpKMSKeyRule(
	_ context.Context,
	_ foundation.GraphCaller,
	_ string,
	graph *orphanGraph,
	node *knowledgev1.Node,
) (bool, float64, string, error) {
	if graph.edges.hasOutgoing(node.Id, kgtypes.EdgeGrants) {
		return false, confidenceGCPKMSKey, "", nil
	}
	if graph.edges.hasIncoming(node.Id, kgtypes.EdgeEncryptsWith) {
		return false, confidenceGCPKMSKey, "", nil
	}
	return true, confidenceGCPKMSKey,
		fmt.Sprintf("KMS key %s has no IAM grants and no resources encrypting with it.",
			displayName(node)),
		nil
}

// gcpRouterRule reports a Cloud Router as orphaned when it has no outbound
// EdgeUsesNetwork and no outbound EdgeContains (no NAT configs). A router
// disconnected from any VPC and with no NAT sub-resources is unused.
func gcpRouterRule(
	_ context.Context,
	_ foundation.GraphCaller,
	_ string,
	graph *orphanGraph,
	node *knowledgev1.Node,
) (bool, float64, string, error) {
	if graph.edges.hasOutgoing(node.Id, kgtypes.EdgeUsesNetwork) {
		return false, confidenceGCPRouter, "", nil
	}
	if graph.edges.hasOutgoing(node.Id, kgtypes.EdgeContains) {
		return false, confidenceGCPRouter, "", nil
	}
	return true, confidenceGCPRouter,
		fmt.Sprintf("Router %s has no VPC network and no NAT configs.",
			displayName(node)),
		nil
}

// gcpSSLCertRule reports an SSL certificate as orphaned when it has no
// incoming EdgeUsesCert from any target proxy. SSL certs are pure leaf
// nodes — they emit no outbound edges. The only valid usage is being
// referenced by a target HTTP(S) proxy.
func gcpSSLCertRule(
	_ context.Context,
	_ foundation.GraphCaller,
	_ string,
	graph *orphanGraph,
	node *knowledgev1.Node,
) (bool, float64, string, error) {
	if graph.edges.hasIncoming(node.Id, kgtypes.EdgeUsesCert) {
		return false, confidenceGCPSSLCert, "", nil
	}
	return true, confidenceGCPSSLCert,
		fmt.Sprintf("SSL certificate %s is not used by any target proxy.",
			displayName(node)),
		nil
}

// gcpAlertPolicyRule reports an alert policy as orphaned when it has no
// outbound MONITORS edges and no outbound NOTIFIES_VIA edges. A policy
// with no condition targets and no notification destinations is dead.
func gcpAlertPolicyRule(
	_ context.Context,
	_ foundation.GraphCaller,
	_ string,
	graph *orphanGraph,
	node *knowledgev1.Node,
) (bool, float64, string, error) {
	if graph.edges.hasOutgoing(node.Id, kgtypes.EdgeMonitors) {
		return false, confidenceGCPAlertPolicy, "", nil
	}
	if graph.edges.hasOutgoing(node.Id, kgtypes.EdgeNotifiesVia) {
		return false, confidenceGCPAlertPolicy, "", nil
	}
	return true, confidenceGCPAlertPolicy,
		fmt.Sprintf("Alert policy %s has no monitoring targets and no notification channels.",
			displayName(node)),
		nil
}

// init self-registers the GCP orphan rules. Resource type strings
// match the values emitted by cloud/gcp/*.go subcollectors.
func init() {
	registerOrphanRule("gcp:compute:forwardingRule", gcpForwardingRuleRule)
	registerOrphanRule("gcp:compute:backendService", gcpBackendServiceRule)
	registerOrphanRule("gcp:storage:bucket", gcpStorageBucketRule)
	registerOrphanRule("gcp:compute:disk", gcpDiskRule)
	registerOrphanRule("gcp:firestore:database", gcpFirestoreDBRule)
	registerOrphanRule("gcp:artifactregistry:repository", gcpARRepoRule)
	registerOrphanRule("gcp:cloudkms:cryptoKey", gcpKMSKeyRule)
	registerOrphanRule("gcp:compute:router", gcpRouterRule)
	registerOrphanRule("gcp:compute:sslCertificate", gcpSSLCertRule)
	registerOrphanRule("gcp:monitoring:alertPolicy", gcpAlertPolicyRule)
}
