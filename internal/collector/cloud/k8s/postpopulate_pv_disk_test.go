// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// pvResNode seeds a PersistentVolume-shaped cloud resource with disk_*
// metadata pre-extracted (matching what sub_persistentvolumes.go now
// emits). Tests write directly instead of running the fake-clientset
// subcollector because the subcollector path is exercised in
// TestExtractPVDiskMetadata below.
func pvResNode(name string, diskMeta map[string]string) *knowledgev1.Node {
	n := &knowledgev1.Node{
		Id:         resourceID("", "PersistentVolume", name),
		Type:       string(kgtypes.NodeCloudResource),
		SymbolName: name,
		Source:     "cloud",
	}
	kgtypes.SetValue(n, "resource_type", "PersistentVolume")
	for k, v := range diskMeta {
		kgtypes.SetValue(n, k, v)
	}
	return n
}

// collectUsesDiskTargets returns the set of USES_DISK edge targets
// originating from the given PV source ID in the named account graph.
func collectUsesDiskTargets(fake *k8sFake, account, from string) []string {
	var targets []string
	edges := fake.outgoingEdges(account, from, kgtypes.EdgeUsesDisk)
	for i := range edges {
		targets = append(targets, edges[i].ToId)
	}
	return targets
}

// TestResolvePVDiskLinkage_AWSEKS: PV backed by AWS EBS in an EKS graph
// gets a USES_DISK edge to a proxy in the {account} cloud graph with
// the canonical volume ARN. Region comes from the EKS cluster ARN.
func TestResolvePVDiskLinkage_AWSEKS(t *testing.T) {
	ctx := newCtx(t)

	const (
		eksGraph = "arn:aws:eks:us-west-2:123456789012:cluster/prod"
		volID    = "vol-0abcd1234"
		pvID     = "PersistentVolume/my-ebs-pv"
		wantTgt  = "arn:aws:ec2:us-west-2::volume/vol-0abcd1234"
		wantPrx  = "proxy:cloud:123456789012:" + wantTgt
	)

	fake := newK8sFake()
	pv := pvResNode("my-ebs-pv", map[string]string{
		"disk_provider": "aws",
		"disk_handle":   volID,
	})
	fake.seed(eksGraph, pv)

	require.NoError(t, resolvePVDiskLinkage(ctx, fake, eksGraph))

	proxy, ok := fake.nodeByID(eksGraph, wantPrx)
	require.True(t, ok, "AWS EBS PV must materialize an EC2 volume proxy in the {account} graph")
	assert.Equal(t, "aws:ebs:volume", kgtypes.Value(proxy, "resource_type"))
	assert.Equal(t, "us-west-2", kgtypes.Value(proxy, "region"))

	targets := collectUsesDiskTargets(fake, eksGraph, pvID)
	require.Len(t, targets, 1)
	assert.Equal(t, wantPrx, targets[0])

	// Idempotent re-run.
	require.NoError(t, resolvePVDiskLinkage(ctx, fake, eksGraph))
	assert.Len(t, collectUsesDiskTargets(fake, eksGraph, pvID), 1)
}

// TestResolvePVDiskLinkage_AWSLegacyDangling: PV with aws provider but
// non-EKS graph name (account not recoverable) gets a dangling proxy.
func TestResolvePVDiskLinkage_AWSLegacyDangling(t *testing.T) {
	ctx := newCtx(t)

	const (
		graph  = "plain-cluster" // not an EKS ARN
		volID  = "vol-0legacy"
		pvID   = "PersistentVolume/legacy-pv"
		wantID = "arn:aws:ec2:::volume/vol-0legacy"
	)

	fake := newK8sFake()
	pv := pvResNode("legacy-pv", map[string]string{
		"disk_provider": "aws",
		"disk_handle":   volID,
	})
	fake.seed(graph, pv)

	require.NoError(t, resolvePVDiskLinkage(ctx, fake, graph))

	danglingID := "proxy:cloud::" + wantID
	proxy, ok := fake.nodeByID(graph, danglingID)
	require.True(t, ok, "legacy AWS PV must produce a dangling proxy")
	assert.Equal(t, "true", kgtypes.Value(proxy, "dangling"))

	targets := collectUsesDiskTargets(fake, graph, pvID)
	require.Len(t, targets, 1)
	assert.Equal(t, danglingID, targets[0])
}

// TestResolvePVDiskLinkage_GCE: PV backed by a GCE PD in a GKE graph
// gets a USES_DISK edge to a proxy in the {project} cloud graph with
// the canonical compute disk selfLink.
func TestResolvePVDiskLinkage_GCE(t *testing.T) {
	ctx := newCtx(t)

	const (
		gkeGraph = "gke_my-project_us-central1-a_main"
		diskName = "gke-main-pvc-abc"
		pvID     = "PersistentVolume/my-pd-pv"
		wantTgt  = "https://www.googleapis.com/compute/v1/projects/my-project/zones/us-central1-a/disks/" + diskName
		wantPrx  = "proxy:cloud:my-project:" + wantTgt
	)

	fake := newK8sFake()
	pv := pvResNode("my-pd-pv", map[string]string{
		"disk_provider": "gcp",
		"disk_handle":   diskName,
	})
	fake.seed(gkeGraph, pv)

	require.NoError(t, resolvePVDiskLinkage(ctx, fake, gkeGraph))

	proxy, ok := fake.nodeByID(gkeGraph, wantPrx)
	require.True(t, ok, "GCE PD in GKE graph must produce a non-dangling proxy")
	assert.Equal(t, "gcp:compute:disk", kgtypes.Value(proxy, "resource_type"))

	targets := collectUsesDiskTargets(fake, gkeGraph, pvID)
	require.Len(t, targets, 1)
	assert.Equal(t, wantPrx, targets[0])
}

// TestResolvePVDiskLinkage_Azure: PV backed by an Azure Disk gets a
// USES_DISK edge to a proxy in the {subscription} cloud graph with
// the canonical disk resource ID.
func TestResolvePVDiskLinkage_Azure(t *testing.T) {
	ctx := newCtx(t)

	const (
		graph   = "aks-cluster"
		diskURI = "/subscriptions/sub1/resourceGroups/rg1/providers/Microsoft.Compute/disks/mydisk"
		pvID    = "PersistentVolume/my-az-pv"
		wantPrx = "proxy:cloud:sub1:" + diskURI
	)

	fake := newK8sFake()
	pv := pvResNode("my-az-pv", map[string]string{
		"disk_provider":     "azure",
		"disk_handle":       diskURI,
		"disk_subscription": "sub1",
	})
	fake.seed(graph, pv)

	require.NoError(t, resolvePVDiskLinkage(ctx, fake, graph))

	proxy, ok := fake.nodeByID(graph, wantPrx)
	require.True(t, ok, "Azure Disk PV must produce a {subscription} proxy")
	assert.Equal(t, "azure:compute:disk", kgtypes.Value(proxy, "resource_type"))

	targets := collectUsesDiskTargets(fake, graph, pvID)
	require.Len(t, targets, 1)
	assert.Equal(t, wantPrx, targets[0])
}

// TestResolvePVDiskLinkage_NonCloud: PV without disk_provider (local,
// NFS, etc) produces no edge.
func TestResolvePVDiskLinkage_NonCloud(t *testing.T) {
	ctx := newCtx(t)

	const graph = "local-cluster"

	fake := newK8sFake()
	pv := pvResNode("local-pv", nil) // no disk_provider
	fake.seed(graph, pv)

	require.NoError(t, resolvePVDiskLinkage(ctx, fake, graph))
	assert.Empty(t, collectUsesDiskTargets(fake, graph, "PersistentVolume/local-pv"))
}

// TestExtractPVDiskMetadata covers the subcollector helper for each of
// the four provider branches.
func TestExtractPVDiskMetadata(t *testing.T) {
	t.Run("aws-ebs-legacy", func(t *testing.T) {
		src := corev1.PersistentVolumeSource{
			AWSElasticBlockStore: &corev1.AWSElasticBlockStoreVolumeSource{VolumeID: "vol-123"},
		}
		m := extractPVDiskMetadata(src)
		assert.Equal(t, "aws", m["disk_provider"])
		assert.Equal(t, "vol-123", m["disk_handle"])
	})

	t.Run("gce-pd-legacy", func(t *testing.T) {
		src := corev1.PersistentVolumeSource{
			GCEPersistentDisk: &corev1.GCEPersistentDiskVolumeSource{PDName: "my-pd"},
		}
		m := extractPVDiskMetadata(src)
		assert.Equal(t, "gcp", m["disk_provider"])
		assert.Equal(t, "my-pd", m["disk_handle"])
	})

	t.Run("azure-disk", func(t *testing.T) {
		src := corev1.PersistentVolumeSource{
			AzureDisk: &corev1.AzureDiskVolumeSource{
				DiskName:    "mydisk",
				DataDiskURI: "/subscriptions/sub1/resourceGroups/rg1/providers/Microsoft.Compute/disks/mydisk",
			},
		}
		m := extractPVDiskMetadata(src)
		assert.Equal(t, "azure", m["disk_provider"])
		assert.Equal(t, "sub1", m["disk_subscription"])
		assert.Equal(t, "rg1", m["disk_resource_group"])
		assert.Equal(t, "mydisk", m["disk_name"])
	})

	t.Run("csi-ebs", func(t *testing.T) {
		src := corev1.PersistentVolumeSource{
			CSI: &corev1.CSIPersistentVolumeSource{
				Driver:       "ebs.csi.aws.com",
				VolumeHandle: "vol-0abc",
				VolumeAttributes: map[string]string{
					"region": "us-east-1",
				},
			},
		}
		m := extractPVDiskMetadata(src)
		assert.Equal(t, "aws", m["disk_provider"])
		assert.Equal(t, "vol-0abc", m["disk_handle"])
		assert.Equal(t, "us-east-1", m["disk_region"])
		assert.Equal(t, "ebs.csi.aws.com", m["disk_csi_driver"])
	})

	t.Run("csi-gcp-zone-fallback", func(t *testing.T) {
		src := corev1.PersistentVolumeSource{
			CSI: &corev1.CSIPersistentVolumeSource{
				Driver:       "pd.csi.storage.gke.io",
				VolumeHandle: "my-pd-handle",
				VolumeAttributes: map[string]string{
					"zone": "us-central1-a",
				},
			},
		}
		m := extractPVDiskMetadata(src)
		assert.Equal(t, "gcp", m["disk_provider"])
		assert.Equal(t, "us-central1-a", m["disk_zone"])
		assert.Equal(t, "us-central1-a", m["disk_region"], "zone should fall through to region when region is unset")
	})

	t.Run("csi-unknown-driver", func(t *testing.T) {
		src := corev1.PersistentVolumeSource{
			CSI: &corev1.CSIPersistentVolumeSource{
				Driver:       "topolvm.io",
				VolumeHandle: "tvm-vol-1",
			},
		}
		m := extractPVDiskMetadata(src)
		assert.Equal(t, "csi:topolvm.io", m["disk_provider"])
	})

	t.Run("non-cloud-source", func(t *testing.T) {
		src := corev1.PersistentVolumeSource{
			HostPath: &corev1.HostPathVolumeSource{Path: "/tmp"},
		}
		m := extractPVDiskMetadata(src)
		assert.Nil(t, m)
	})
}
