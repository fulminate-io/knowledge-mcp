// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"context"
	"log/slog"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/postpopulate"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// resolvePVDiskLinkage wires PersistentVolume nodes to cross-graph
// proxies of the underlying cloud disk (AWS EBS volume, GCE PD, Azure
// Disk). Each PV gets at most one USES_DISK edge.
//
// The disk fields are pre-extracted by sub_persistentvolumes.go into
// flat metadata keys (disk_provider, disk_handle, disk_region, disk_zone,
// disk_subscription, disk_resource_group, disk_csi_driver, disk_name).
// This resolver MUST NOT re-parse the PV JSON — if a field is missing,
// extend extractPVDiskMetadata in the subcollector first.
//
// Dangling proxies (empty account) are acceptable per plan decision:
// legacy AWSElasticBlockStore sources don't carry the account; CSI
// volumeHandles sometimes do (e.g. "vol-abc123") but not always. When
// the account is recoverable (e.g. Azure subscription in the URI, GKE
// project parsed from the cloud graph name) we emit a non-dangling
// proxy in the {account} cloud graph.
func resolvePVDiskLinkage(ctx context.Context, gc postpopulate.GraphCaller, graphName string) error {
	pvs, err := postpopulate.BrowseNodes(ctx, gc, kgtypes.GraphCloud, graphName, k8sResourceQuery("PersistentVolume"))
	if err != nil {
		return err
	}
	if len(pvs) == 0 {
		return nil
	}

	proxies := newProxyAccumulator()
	edges := make([]knowledgev1.Edge, 0, len(pvs))
	for _, pv := range pvs {
		next, err := buildPVDiskEdge(edges, pv, graphName, proxies)
		if err != nil {
			return err
		}
		edges = next
	}

	if len(edges) == 0 {
		return nil
	}
	if err := postpopulate.LinkNodesAndEdgesBatch(ctx, gc, kgtypes.GraphCloud, graphName, proxies.nodes(), edges); err != nil {
		slog.Debug("postPopulate: failed to create USES_DISK edges",
			"count", len(edges), "err", err)
		return err
	}
	slog.Debug("postPopulate: created USES_DISK edges", "count", len(edges))
	return nil
}

// pvDiskTarget is the resolver's intermediate form for a disk proxy
// target before it becomes a knowledgev1.ProxyTarget.
type pvDiskTarget struct {
	Account      string // proxy graph name; empty for dangling
	ID           string // foreign_id (canonical cloud disk resource ID)
	ResourceType string // "aws:ebs:volume", "gcp:compute:disk", "azure:compute:disk", "csi:{driver}:disk"
	Provider     string // "aws" | "gcp" | "azure" | "csi:{driver}" for unknown drivers
	Region       string // display-only; empty when not known
	Method       string // "pv-ebs", "pv-gce-pd", "pv-azure-disk", "pv-csi-{driver}"
}

// buildPVDiskEdge reads the pre-extracted disk metadata off a PV node and
// appends the USES_DISK edge to its proxy to out, returning the grown
// slice. PVs without a disk_provider tag (non-cloud sources) leave out
// unchanged. The edge is built as a fresh knowledgev1.Edge literal at the append
// site (in emitUsesDisk) so the embedded proto lock is never copied.
func buildPVDiskEdge(out []knowledgev1.Edge, pv *knowledgev1.Node, graphName string, proxies *proxyAccumulator) ([]knowledgev1.Edge, error) {
	provider := kgtypes.Value(pv, "disk_provider")
	handle := kgtypes.Value(pv, "disk_handle")
	if provider == "" || handle == "" {
		return out, nil
	}

	target, ok := buildPVDiskTarget(pv, graphName)
	if !ok {
		return out, nil
	}
	return emitUsesDisk(out, pv, target, proxies)
}

// buildPVDiskTarget classifies a PV's disk_provider metadata into a
// pvDiskTarget with the correct canonical resource ID per provider.
// Account recovery is best-effort: when only the handle is known and
// the cloud graph name doesn't help, Account stays empty and a
// dangling proxy will be emitted downstream.
func buildPVDiskTarget(pv *knowledgev1.Node, graphName string) (pvDiskTarget, bool) {
	provider := kgtypes.Value(pv, "disk_provider")
	handle := kgtypes.Value(pv, "disk_handle")
	switch {
	case provider == "aws":
		return buildAWSDiskTarget(pv, handle, graphName), true
	case provider == "gcp":
		return buildGCPDiskTarget(pv, handle, graphName), true
	case provider == "azure":
		return buildAzureDiskTarget(pv, handle), true
	case strings.HasPrefix(provider, "csi:"):
		// Unknown-CSI fallthrough — emit a dangling proxy tagged with
		// the driver so downstream tooling still sees there's a disk.
		return pvDiskTarget{
			Account:      "",
			ID:           "csi:" + handle,
			ResourceType: provider + ":disk",
			Provider:     provider,
			Method:       "pv-" + strings.ReplaceAll(provider, ":", "-"),
		}, true
	}
	return pvDiskTarget{}, false
}

// buildAWSDiskTarget constructs the canonical EBS volume ARN. AWS
// account is recovered from the enclosing graph name when it's an EKS
// cluster ARN; otherwise the proxy is dangling (per plan OQ).
//
// Canonical ID: arn:aws:ec2:{region}::volume/{volume_id}
//
// TODO(pv-disk): recover AWS account when it's present in the cloud
// graph name as a plain account-id (non-EKS AWS graphs). For now
// legacy AWSElasticBlockStore + bare-handle CSI both land as dangling.
func buildAWSDiskTarget(pv *knowledgev1.Node, handle, graphName string) pvDiskTarget {
	region := kgtypes.Value(pv, "disk_region")
	account := ""
	if r, acc, _, ok := parseEKSClusterARN(graphName); ok {
		account = acc
		if region == "" {
			region = r
		}
	}
	target := pvDiskTarget{
		Account:      account,
		ResourceType: "aws:ebs:volume",
		Provider:     "aws",
		Region:       region,
		Method:       "pv-ebs",
	}
	if region != "" {
		target.ID = "arn:aws:ec2:" + region + "::volume/" + handle
	} else {
		// Without region the ARN has an empty region segment — still
		// unique enough to proxy, but flagged for later enrichment.
		target.ID = "arn:aws:ec2:::volume/" + handle
	}
	return target
}

// buildGCPDiskTarget constructs the canonical compute disk selfLink.
// Project is recovered from the GKE graph name; zone from disk_zone or
// disk_region metadata (CSI drivers populate volumeAttributes.zone;
// legacy GCEPersistentDisk sources don't carry it, so for legacy the
// proxy is dangling on zone but still targets {project}).
func buildGCPDiskTarget(pv *knowledgev1.Node, handle, graphName string) pvDiskTarget {
	project, region, _, isGKE := parseGKEGraphName(graphName)
	if !isGKE {
		// Non-GKE graph — no project context, proxy is fully dangling.
		return pvDiskTarget{
			ID:           "gcp:disk/" + handle,
			ResourceType: "gcp:compute:disk",
			Provider:     "gcp",
			Method:       "pv-gce-pd",
		}
	}
	zone := kgtypes.Value(pv, "disk_zone")
	if zone == "" {
		zone = kgtypes.Value(pv, "disk_region")
	}
	if zone == "" {
		zone = region
	}
	selfLink := "https://www.googleapis.com/compute/v1/projects/" + project +
		"/zones/" + zone + "/disks/" + handle
	return pvDiskTarget{
		Account:      project,
		ID:           selfLink,
		ResourceType: "gcp:compute:disk",
		Provider:     "gcp",
		Region:       zone,
		Method:       "pv-gce-pd",
	}
}

// buildAzureDiskTarget constructs the canonical Azure disk resource ID
// from the pre-parsed URI. Subscription is the account.
func buildAzureDiskTarget(pv *knowledgev1.Node, handle string) pvDiskTarget {
	// Azure disk handles ARE the canonical resource ID already.
	sub := kgtypes.Value(pv, "disk_subscription")
	// The handle itself is the canonical full resource ID.
	return pvDiskTarget{
		Account:      sub,
		ID:           handle,
		ResourceType: "azure:compute:disk",
		Provider:     "azure",
		Method:       "pv-azure-disk",
	}
}

// emitUsesDisk creates the cross-graph proxy and appends the USES_DISK
// edge from the PV to it to out, returning the grown slice. When
// target.Account is empty we emit a dangling proxy via
// upsertDanglingDiskProxy — analogous to the dangling-Azure-WI case in
// postpopulate_workload_identity.go. The edge is a fresh knowledgev1.Edge
// literal at the append site so the embedded proto lock is never copied.
func emitUsesDisk(out []knowledgev1.Edge, pv *knowledgev1.Node, target pvDiskTarget, proxies *proxyAccumulator) ([]knowledgev1.Edge, error) {
	source := pvDiskProxySource(target)

	var proxyID string
	if target.Account == "" {
		proxyID = addDanglingDiskProxy(target, source, proxies)
	} else {
		id, err := proxies.proxy(&knowledgev1.ProxyTarget{
			GraphType: string(kgtypes.GraphCloud),
			Name:      target.Account,
			NodeId:    target.ID,
		}, source)
		if err != nil {
			return out, err
		}
		proxyID = id
	}

	return append(out, knowledgev1.Edge{
		FromId: pv.Id,
		ToId:   proxyID,
		Type:   string(kgtypes.EdgeUsesDisk),
		Method: target.Method,
	}), nil
}

// pvDiskProxySource carries display fields for readable traversal
// output. Same pattern as wiProxySource / vmProxySource.
func pvDiskProxySource(target pvDiskTarget) *knowledgev1.Node {
	name := lastSegmentAfterSlash(target.ID)
	n := &knowledgev1.Node{
		Type:       string(kgtypes.NodeCloudResource),
		SymbolName: name,
		Source:     "cloud",
		Summary:    target.ResourceType + " " + name,
	}
	kgtypes.SetValue(n, "resource_type", target.ResourceType)
	kgtypes.SetValue(n, "provider", target.Provider)
	if target.Region != "" {
		kgtypes.SetValue(n, "region", target.Region)
	}
	return n
}

// upsertDanglingDiskProxy writes a cloud proxy with empty account. Uses
// the same "proxy:cloud::<NodeID>" ID scheme as upsertDanglingCloudProxy
// (keep the two helpers separate so each can evolve its own metadata).
//
// TODO(pv-disk): enrich dangling AWS EBS proxies once the AWS account
// can be recovered from a non-EKS cloud graph name or from an IMDS
// tagging pass. The volume ID is stable in the proxy foreign_id so a
// future resolver can upgrade dangling → non-dangling by rewriting
// edges to the enriched proxy.
func addDanglingDiskProxy(target pvDiskTarget, source *knowledgev1.Node, proxies *proxyAccumulator) string {
	proxyID := "proxy:cloud::" + target.ID
	proxy := &knowledgev1.Node{
		Id:          proxyID,
		Type:        string(kgtypes.NodeProxy),
		SymbolName:  source.GetSymbolName(),
		Source:      "proxy:cloud:dangling",
		Description: source.GetDescription(),
	}
	kgtypes.SetValue(proxy, "foreign_graph", string(kgtypes.GraphCloud))
	kgtypes.SetValue(proxy, "foreign_id", target.ID)
	kgtypes.SetValue(proxy, "account", "")
	kgtypes.SetValue(proxy, "resource_type", target.ResourceType)
	kgtypes.SetValue(proxy, "provider", target.Provider)
	kgtypes.SetValue(proxy, "dangling", "true")
	if target.Region != "" {
		kgtypes.SetValue(proxy, "region", target.Region)
	}
	proxies.byID[proxyID] = proxy
	return proxyID
}
