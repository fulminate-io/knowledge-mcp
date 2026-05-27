// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"context"
	"fmt"
	"maps"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// persistentVolumesSubCollector lists all PersistentVolumes (cluster-scoped).
type persistentVolumesSubCollector struct {
	clientset kubernetes.Interface
}

func (s *persistentVolumesSubCollector) Name() string { return "persistentvolumes" }

func (s *persistentVolumesSubCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	list, err := s.clientset.CoreV1().PersistentVolumes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return cloud.SubCollectorResult{}, fmt.Errorf("list persistentvolumes: %w", err)
	}

	var result cloud.SubCollectorResult

	for _, pv := range list.Items {
		// Cluster-scoped: PersistentVolume/name.
		id := resourceID("", "PersistentVolume", pv.Name)

		meta := labelsToMeta(pv.Labels)
		meta["phase"] = string(pv.Status.Phase)
		if capacity, ok := pv.Spec.Capacity["storage"]; ok {
			meta["capacity"] = capacity.String()
		}
		meta["reclaim_policy"] = string(pv.Spec.PersistentVolumeReclaimPolicy)
		if len(pv.Spec.AccessModes) > 0 {
			var modes []string
			for _, m := range pv.Spec.AccessModes {
				modes = append(modes, string(m))
			}
			meta["access_modes"] = strings.Join(modes, ",")
		}

		// Pre-extract backing-disk fields onto the resource metadata so the
		// postpopulate_pv_disk resolver can build USES_DISK edges without
		// re-unmarshaling pv.Spec.PersistentVolumeSource. Only one of the
		// source branches ever matches, so these fields are mutually
		// exclusive in practice.
		maps.Copy(meta, extractPVDiskMetadata(pv.Spec.PersistentVolumeSource))

		result.Resources = append(result.Resources, cloud.ResourceSpec{
			ID:           id,
			Name:         pv.Name,
			ResourceType: "PersistentVolume",
			Content:      marshalJSON(pv),
			Metadata:     meta,
		})

		// USES_STORAGE_CLASS edge.
		if pv.Spec.StorageClassName != "" {
			result.Edges = append(result.Edges, cloud.EdgeSpec{
				SourceID:     id,
				TargetID:     resourceID("", "StorageClass", pv.Spec.StorageClassName),
				Relationship: kgtypes.EdgeUsesStorageClass,
			})
		}

		// Detect backing storage and generate cascade targets.
		result.Targets = append(result.Targets, detectPVCascadeTargets(pv.Spec.PersistentVolumeSource)...)
	}

	return result, nil
}

// detectPVCascadeTargets inspects a PV source for cloud-backed storage.
func detectPVCascadeTargets(src corev1.PersistentVolumeSource) []cloud.CollectTarget {
	var targets []cloud.CollectTarget

	// Legacy AWS EBS.
	if src.AWSElasticBlockStore != nil {
		targets = append(targets, cloud.CollectTarget{Collector: "aws", ID: ""})
	}

	// Legacy GCE PD.
	if src.GCEPersistentDisk != nil {
		targets = append(targets, cloud.CollectTarget{Collector: "gcp", ID: ""})
	}

	// Azure Disk.
	if src.AzureDisk != nil {
		if subID := extractAzureSubscriptionFromDiskURI(src.AzureDisk.DataDiskURI); subID != "" {
			targets = append(targets, cloud.CollectTarget{Collector: "azure", ID: subID})
		} else {
			targets = append(targets, cloud.CollectTarget{Collector: "azure", ID: ""})
		}
	}

	// CSI drivers.
	if src.CSI != nil {
		switch {
		case strings.Contains(src.CSI.Driver, "ebs.csi.aws.com"):
			targets = append(targets, cloud.CollectTarget{Collector: "aws", ID: ""})
		case strings.Contains(src.CSI.Driver, "pd.csi.storage.gke.io"):
			targets = append(targets, cloud.CollectTarget{Collector: "gcp", ID: ""})
		case strings.Contains(src.CSI.Driver, "disk.csi.azure.com"):
			targets = append(targets, cloud.CollectTarget{Collector: "azure", ID: ""})
		}
	}

	return targets
}

// extractAzureSubscriptionFromDiskURI extracts subscription ID from an Azure disk URI.
// Format: /subscriptions/<sub-id>/resourceGroups/<rg>/providers/Microsoft.Compute/disks/<name>
func extractAzureSubscriptionFromDiskURI(uri string) string {
	parts := strings.Split(strings.ToLower(uri), "/")
	for i, p := range parts {
		if p == "subscriptions" && i+1 < len(parts) {
			return parts[i+1]
		}
	}
	return ""
}

// extractAzureResourceGroupFromDiskURI extracts the resourceGroup segment
// from an Azure disk URI. Mirrors extractAzureSubscriptionFromDiskURI but
// for the RG. Returns empty string when the URI doesn't follow the
// /subscriptions/.../resourceGroups/.../providers/... shape.
func extractAzureResourceGroupFromDiskURI(uri string) string {
	parts := strings.Split(strings.ToLower(uri), "/")
	for i, p := range parts {
		if p == "resourcegroups" && i+1 < len(parts) {
			return parts[i+1]
		}
	}
	return ""
}

// extractPVDiskMetadata returns a flat metadata map describing the cloud
// disk that backs a PersistentVolume, or nil when the source is not
// cloud-backed (local, NFS, iSCSI, etc).
//
// Emitted keys (only the ones that apply):
//
//	disk_provider         — "aws" | "gcp" | "azure" | "csi:{driver-name}"
//	disk_handle           — volumeID / pdName / CSI volumeHandle / Azure disk URI
//	disk_region           — AWS region / GCP zone / Azure region
//	disk_zone             — GCP zone (same value as disk_region for GCE PD)
//	disk_account          — AWS account if inferable from handle (rare — usually empty)
//	disk_subscription     — Azure subscription ID parsed from the disk URI
//	disk_resource_group   — Azure resource group parsed from the disk URI
//	disk_name             — final path segment (human-readable)
//	disk_csi_driver       — raw CSI driver string when source is CSI
//
// These are an agreed contract between this subcollector and
// postpopulate_pv_disk.go. The resolver MUST NOT re-unmarshal the PV JSON;
// if a new field is needed, add it here first.
func extractPVDiskMetadata(src corev1.PersistentVolumeSource) map[string]string {
	switch {
	case src.AWSElasticBlockStore != nil:
		return awsEBSDiskMetadata(src.AWSElasticBlockStore)
	case src.GCEPersistentDisk != nil:
		return gcePDDiskMetadata(src.GCEPersistentDisk)
	case src.AzureDisk != nil:
		return azureDiskMetadata(src.AzureDisk)
	case src.CSI != nil:
		return csiDiskMetadata(src.CSI)
	}
	return nil
}

// awsEBSDiskMetadata pulls fields from a legacy AWSElasticBlockStore
// source. AWS account is NOT available from the volume ID alone, so the
// resolver will emit a dangling proxy unless we can recover the account
// elsewhere (future work — not in this ticket).
func awsEBSDiskMetadata(s *corev1.AWSElasticBlockStoreVolumeSource) map[string]string {
	if s == nil || s.VolumeID == "" {
		return nil
	}
	return map[string]string{
		"disk_provider": "aws",
		"disk_handle":   s.VolumeID,
		"disk_name":     s.VolumeID,
		// disk_region is not set — the legacy AWSElasticBlockStore source
		// has no region field; callers fall back to the cloud graph name.
	}
}

// gcePDDiskMetadata pulls fields from a legacy GCEPersistentDisk source.
// Zone is not carried on the source type — it's inferred from the disk
// name's surrounding cluster context at resolver time.
func gcePDDiskMetadata(s *corev1.GCEPersistentDiskVolumeSource) map[string]string {
	if s == nil || s.PDName == "" {
		return nil
	}
	return map[string]string{
		"disk_provider": "gcp",
		"disk_handle":   s.PDName,
		"disk_name":     s.PDName,
	}
}

// azureDiskMetadata pulls fields from an AzureDisk source. The DataDiskURI
// carries subscription + resource group + disk name in a canonical form.
func azureDiskMetadata(s *corev1.AzureDiskVolumeSource) map[string]string {
	if s == nil || s.DataDiskURI == "" {
		return nil
	}
	m := map[string]string{
		"disk_provider": "azure",
		"disk_handle":   s.DataDiskURI,
		"disk_name":     s.DiskName,
	}
	if sub := extractAzureSubscriptionFromDiskURI(s.DataDiskURI); sub != "" {
		m["disk_subscription"] = sub
	}
	if rg := extractAzureResourceGroupFromDiskURI(s.DataDiskURI); rg != "" {
		m["disk_resource_group"] = rg
	}
	return m
}

// csiDiskMetadata pulls fields from a CSI source. Driver-specific parsing
// (e.g. AWS EBS volume ID extraction from a pd.csi.aws.com handle) lives
// downstream in postpopulate_pv_disk.go — here we just surface the raw
// handle + driver so the resolver has what it needs.
func csiDiskMetadata(s *corev1.CSIPersistentVolumeSource) map[string]string {
	if s == nil || s.VolumeHandle == "" {
		return nil
	}
	m := map[string]string{
		"disk_provider":   csiProvider(s.Driver),
		"disk_handle":     s.VolumeHandle,
		"disk_csi_driver": s.Driver,
	}
	// VolumeAttributes often carry zone / region / subscription for CSI
	// drivers — copy a few well-known keys (case-insensitive on driver
	// side but the attribute keys are stable per driver).
	for _, key := range []string{"zone", "region", "subscription", "resourceGroup"} {
		if v, ok := s.VolumeAttributes[key]; ok && v != "" {
			switch key {
			case "zone":
				m["disk_zone"] = v
				if _, have := m["disk_region"]; !have {
					m["disk_region"] = v
				}
			case "region":
				m["disk_region"] = v
			case "subscription":
				m["disk_subscription"] = v
			case "resourceGroup":
				m["disk_resource_group"] = v
			}
		}
	}
	return m
}

// csiProvider classifies a CSI driver into a high-level provider slug
// the resolver uses to decide which proxy ID scheme to emit. Unknown
// drivers get "csi:{driver}" so the edge is still emitted with an
// informative provider tag instead of being silently dropped.
func csiProvider(driver string) string {
	switch {
	case strings.Contains(driver, "ebs.csi.aws.com"):
		return "aws"
	case strings.Contains(driver, "pd.csi.storage.gke.io"):
		return "gcp"
	case strings.Contains(driver, "disk.csi.azure.com"):
		return "azure"
	default:
		return "csi:" + driver
	}
}
