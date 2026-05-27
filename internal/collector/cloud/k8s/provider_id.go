// SPDX-License-Identifier: Apache-2.0

package k8s

import "strings"

// providerVMTarget describes the cross-graph cloud VM that a K8s Node's
// spec.providerID points at. Populated by parseProviderID.
//
// Account and ID hold exactly what knowledgev1.ProxyTarget expects:
//   - Account → ProxyTarget.Name (the enclosing cloud graph name)
//   - ID      → ProxyTarget.NodeId (the upstream node ID)
//
// ResourceType is the resource_type metadata string the upstream cloud
// collector actually emits, verified per the plan's open-question
// resolution:
//
//	gcp   : "gcp:compute:instance"           (not yet emitted by compute.go,
//	                                          but stable enough for proxies;
//	                                          the Compute collector uses
//	                                          resource_type="gcp_instance"
//	                                          today — the proxy's display
//	                                          resource_type only matters for
//	                                          UI, not for resolution)
//	aws   : "ec2-instance"                   (cloud/aws/ec2.go:59 emits)
//	azure : "Microsoft.Compute/virtualMachines" (cloud/azure/vms.go:102)
//
// For AWS, spec.ProviderID never carries the account ID. parseProviderID
// returns Account="" and ID="" for the AWS case; callers MUST supply the
// account (e.g. via parseEKSClusterARN on the enclosing graph name) and
// call finalizeAWSTarget to complete the target.
//
// Region is informational — AWS callers use it to build the ARN; GCP
// callers see the zone; Azure callers see "" (resource ID already
// contains the region implicitly via the resource group).
type providerVMTarget struct {
	Provider     string
	Account      string
	ID           string
	ResourceType string
	Region       string
	InstanceID   string // AWS-only: the i-xxxxxxx fragment, for finalizeAWSTarget.
}

// parseProviderID maps a K8s node.Spec.ProviderID to a cross-graph proxy
// target. Returns (zero, false) for unrecognized formats or malformed
// input.
//
// Accepted forms:
//
//	gce://{project}/{zone}/{instance}
//	aws:///{az}/{instance-id}          (triple-slash; host empty)
//	aws:///{instance-id}               (legacy kubelets; no zone)
//	azure:///subscriptions/{sub}/resourceGroups/{rg}/providers/Microsoft.Compute/virtualMachines/{name}
//	azure:///subscriptions/{sub}/resourceGroups/{rg}/providers/Microsoft.Compute/virtualMachineScaleSets/{vmss}/virtualMachines/{idx}
//
// The VMSS form returns the raw resource ID as-is; downstream resolution
// is a dangling proxy until AKS+VMSS handling lands in a follow-up ticket
// (see OQ6 in the plan).
func parseProviderID(providerID string) (providerVMTarget, bool) {
	switch {
	case strings.HasPrefix(providerID, "gce://"):
		return parseGCEProviderID(providerID)
	case strings.HasPrefix(providerID, "aws:///"):
		return parseAWSProviderID(providerID)
	case strings.HasPrefix(providerID, "azure:///"):
		return parseAzureProviderID(providerID)
	}
	return providerVMTarget{}, false
}

// parseGCEProviderID handles the GKE form "gce://{project}/{zone}/{instance}".
// Builds the canonical Compute Engine selfLink as the proxy target ID so
// the upstream GCP graph's Compute instance node matches.
func parseGCEProviderID(providerID string) (providerVMTarget, bool) {
	rest := strings.TrimPrefix(providerID, "gce://")
	parts := strings.Split(rest, "/")
	if len(parts) != 3 {
		return providerVMTarget{}, false
	}
	project, zone, instance := parts[0], parts[1], parts[2]
	if project == "" || zone == "" || instance == "" {
		return providerVMTarget{}, false
	}
	selfLink := "https://www.googleapis.com/compute/v1/projects/" +
		project + "/zones/" + zone + "/instances/" + instance
	return providerVMTarget{
		Provider:     "gcp",
		Account:      project,
		ID:           selfLink,
		ResourceType: "gcp:compute:instance",
		Region:       zone,
	}, true
}

// parseAWSProviderID handles the EKS forms "aws:///{az}/{instance-id}"
// and legacy "aws:///{instance-id}". Leaves Account and ID empty —
// providerID does not carry the AWS account and we cannot safely build
// an ARN without it. Callers resolve the account from the enclosing
// graph name and call finalizeAWSTarget.
func parseAWSProviderID(providerID string) (providerVMTarget, bool) {
	rest := strings.TrimPrefix(providerID, "aws:///")
	parts := strings.Split(rest, "/")

	var az, instanceID string
	switch len(parts) {
	case 1:
		instanceID = parts[0]
	case 2:
		az, instanceID = parts[0], parts[1]
	default:
		return providerVMTarget{}, false
	}
	if instanceID == "" {
		return providerVMTarget{}, false
	}

	return providerVMTarget{
		Provider:     "aws",
		ResourceType: "ec2-instance",
		Region:       azToRegion(az),
		InstanceID:   instanceID,
	}, true
}

// azToRegion strips the trailing single letter from an EC2 availability
// zone ("us-east-1a" → "us-east-1"). Returns empty for empty input or
// any string that does not end in an alphabetic suffix.
func azToRegion(az string) string {
	if az == "" {
		return ""
	}
	last := az[len(az)-1]
	if last < 'a' || last > 'z' {
		return ""
	}
	return az[:len(az)-1]
}

// finalizeAWSTarget fills in Account and the ARN-shaped ID on an AWS
// target returned by parseAWSProviderID. Returns the input unchanged if
// the target is not AWS or the account is empty. Idempotent: a target
// that already has Account set is returned unchanged.
func finalizeAWSTarget(t providerVMTarget, accountID string) providerVMTarget {
	if t.Provider != "aws" {
		return t
	}
	if t.Account != "" {
		return t
	}
	if accountID == "" {
		return t
	}
	// Build "arn:aws:ec2:{region}:{account}:instance/{id}" matching
	// cloud/aws/arn.go:6. Region may legitimately be empty for legacy
	// providerIDs; the resulting ARN is still deterministic and callers
	// can decide whether to skip.
	t.Account = accountID
	t.ID = "arn:aws:ec2:" + t.Region + ":" + accountID + ":instance/" + t.InstanceID
	return t
}

// parseAzureProviderID handles both VM and VMSS forms. The resource ID
// is the providerID's path ("/subscriptions/…"), which is exactly what
// the Azure VM collector uses as the node ID for a matching VM.
//
// VMSS pods produce a resource ID pointing at a VMSS-instance path;
// that path does not match any node in the Azure graph today, so the
// proxy will dangle. OQ6 defers proper AKS+VMSS handling to a separate
// ticket — we still emit the deterministic proxy so callers can
// surface the link once the upstream node exists.
//
// TODO(AKS+VMSS): once cloud/azure/vmss.go grows a VMSS-instance
// subcollector that emits nodes at the full scale-set-instance path,
// this function will start producing resolvable proxies automatically
// (the ID string is already correct).
func parseAzureProviderID(providerID string) (providerVMTarget, bool) {
	rest := strings.TrimPrefix(providerID, "azure://")
	if !strings.HasPrefix(rest, "/subscriptions/") {
		return providerVMTarget{}, false
	}
	// Extract subscription ID: "/subscriptions/{sub}/..." → sub.
	// Split into ["", "subscriptions", "{sub}", ...] (leading "" from the
	// leading slash).
	parts := strings.Split(rest, "/")
	if len(parts) < 4 || parts[1] != "subscriptions" {
		return providerVMTarget{}, false
	}
	subscription := parts[2]
	if subscription == "" {
		return providerVMTarget{}, false
	}
	// Require a virtualMachines or virtualMachineScaleSets segment so we
	// refuse to build proxies for unrelated Azure resource IDs that
	// happened to end up in a providerID (defensive; no known example).
	if !strings.Contains(rest, "/Microsoft.Compute/virtualMachines/") &&
		!strings.Contains(rest, "/Microsoft.Compute/virtualMachineScaleSets/") {
		return providerVMTarget{}, false
	}
	return providerVMTarget{
		Provider:     "azure",
		Account:      subscription,
		ID:           rest,
		ResourceType: "Microsoft.Compute/virtualMachines",
	}, true
}

// parseEKSClusterARN splits an EKS cluster ARN into (region, account,
// cluster) so callers can finalize AWS targets and emit cluster-shaped
// proxies. Returns ("", "", "", false) for any string that does not
// match the EKS ARN shape. The K8s collector uses the cluster ARN
// verbatim as the graph name (cloud/aws/eks.go:93 passes it as the
// cascade target ID, which becomes bundle.contextName in
// cloud/k8s/client.go:42).
//
// Accepted shape: "arn:aws:eks:{region}:{account}:cluster/{name}".
func parseEKSClusterARN(graphName string) (region, account, cluster string, ok bool) {
	if !strings.HasPrefix(graphName, "arn:aws:eks:") {
		return "", "", "", false
	}
	parts := strings.SplitN(graphName, ":", 6)
	if len(parts) != 6 {
		return "", "", "", false
	}
	// parts = ["arn", "aws", "eks", region, account, "cluster/<name>"]
	region, account = parts[3], parts[4]
	if region == "" || account == "" {
		return "", "", "", false
	}
	if !strings.HasPrefix(parts[5], "cluster/") {
		return "", "", "", false
	}
	cluster = strings.TrimPrefix(parts[5], "cluster/")
	if cluster == "" {
		return "", "", "", false
	}
	return region, account, cluster, true
}

// eksClusterARN is the inverse of parseEKSClusterARN.
// Returns "arn:aws:eks:{region}:{account}:cluster/{cluster}".
// Argument order matches parseEKSClusterARN's return order.
func eksClusterARN(region, account, cluster string) string {
	return "arn:aws:eks:" + region + ":" + account + ":cluster/" + cluster
}
