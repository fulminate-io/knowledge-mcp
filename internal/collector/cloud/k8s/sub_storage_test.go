// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

func TestPVSubCollector_EBS(t *testing.T) {
	cs := fake.NewSimpleClientset(&corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: "pv-ebs-001"},
		Spec: corev1.PersistentVolumeSpec{
			Capacity: corev1.ResourceList{
				"storage": resource.MustParse("100Gi"),
			},
			AccessModes:                   []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimRetain,
			StorageClassName:              "gp3",
			PersistentVolumeSource: corev1.PersistentVolumeSource{
				AWSElasticBlockStore: &corev1.AWSElasticBlockStoreVolumeSource{
					VolumeID: "aws://us-east-1a/vol-0123456789abcdef0",
				},
			},
		},
		Status: corev1.PersistentVolumeStatus{
			Phase: corev1.VolumeBound,
		},
	})

	sub := &persistentVolumesSubCollector{clientset: cs}
	result, err := sub.Collect(context.Background())
	require.NoError(t, err)
	require.Len(t, result.Resources, 1)

	res := result.Resources[0]
	assert.Equal(t, "PersistentVolume/pv-ebs-001", res.ID)
	assert.Equal(t, "100Gi", res.Metadata["capacity"])
	assert.Equal(t, "Bound", res.Metadata["phase"])
	assert.Equal(t, "Retain", res.Metadata["reclaim_policy"])
	assert.Equal(t, "ReadWriteOnce", res.Metadata["access_modes"])

	// USES_STORAGE_CLASS edge.
	require.Len(t, result.Edges, 1)
	assert.Equal(t, kgtypes.EdgeUsesStorageClass, result.Edges[0].Relationship)
	assert.Equal(t, "StorageClass/gp3", result.Edges[0].TargetID)

	// AWS cascade target.
	require.Len(t, result.Targets, 1)
	assert.Equal(t, "aws", result.Targets[0].Collector)
}

func TestPVSubCollector_CSI_Azure(t *testing.T) {
	cs := fake.NewSimpleClientset(&corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: "pv-azure-001"},
		Spec: corev1.PersistentVolumeSpec{
			PersistentVolumeSource: corev1.PersistentVolumeSource{
				CSI: &corev1.CSIPersistentVolumeSource{
					Driver:       "disk.csi.azure.com",
					VolumeHandle: "/subscriptions/sub-123/resourceGroups/rg/providers/Microsoft.Compute/disks/disk-1",
				},
			},
		},
	})

	sub := &persistentVolumesSubCollector{clientset: cs}
	result, err := sub.Collect(context.Background())
	require.NoError(t, err)
	require.Len(t, result.Targets, 1)
	assert.Equal(t, "azure", result.Targets[0].Collector)
}

func TestPVCSubCollector_BoundToPV(t *testing.T) {
	cs := fake.NewSimpleClientset(&corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "data-0",
			Namespace: "default",
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			VolumeName:       "pv-ebs-001",
			StorageClassName: new("gp3"),
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
		},
		Status: corev1.PersistentVolumeClaimStatus{
			Phase: corev1.ClaimBound,
			Capacity: corev1.ResourceList{
				"storage": resource.MustParse("100Gi"),
			},
		},
	})

	sub := &pvcsSubCollector{clientset: cs}
	result, err := sub.Collect(context.Background())
	require.NoError(t, err)
	require.Len(t, result.Resources, 1)

	res := result.Resources[0]
	assert.Equal(t, "default/PersistentVolumeClaim/data-0", res.ID)
	assert.Equal(t, "Bound", res.Metadata["phase"])
	assert.Equal(t, "100Gi", res.Metadata["capacity"])

	// BOUND_TO edge to PV.
	edgeMap := make(map[kgtypes.EdgeType][]string)
	for _, e := range result.Edges {
		edgeMap[e.Relationship] = append(edgeMap[e.Relationship], e.TargetID)
	}
	assert.Contains(t, edgeMap[kgtypes.EdgeBoundTo], "PersistentVolume/pv-ebs-001")

	// USES_STORAGE_CLASS edge.
	assert.Contains(t, edgeMap[kgtypes.EdgeUsesStorageClass], "StorageClass/gp3")
}

func TestStorageClassesSubCollector(t *testing.T) {
	reclaimPolicy := corev1.PersistentVolumeReclaimDelete
	bindingMode := storagev1.VolumeBindingWaitForFirstConsumer
	allowExpansion := true

	cs := fake.NewSimpleClientset(&storagev1.StorageClass{
		ObjectMeta:           metav1.ObjectMeta{Name: "gp3"},
		Provisioner:          "ebs.csi.aws.com",
		ReclaimPolicy:        &reclaimPolicy,
		VolumeBindingMode:    &bindingMode,
		AllowVolumeExpansion: &allowExpansion,
	})

	sub := &storageClassesSubCollector{clientset: cs}
	result, err := sub.Collect(context.Background())
	require.NoError(t, err)
	require.Len(t, result.Resources, 1)

	res := result.Resources[0]
	assert.Equal(t, "StorageClass/gp3", res.ID)
	assert.Equal(t, "ebs.csi.aws.com", res.Metadata["provisioner"])
	assert.Equal(t, "Delete", res.Metadata["reclaim_policy"])
	assert.Equal(t, "WaitForFirstConsumer", res.Metadata["volume_binding_mode"])
	assert.Equal(t, "true", res.Metadata["allow_expansion"])
}

func TestExtractAzureSubscriptionFromDiskURI(t *testing.T) {
	tests := []struct {
		uri    string
		expect string
	}{
		{"/subscriptions/sub-123/resourceGroups/rg/providers/Microsoft.Compute/disks/disk-1", "sub-123"},
		{"/Subscriptions/SUB-456/resourceGroups/rg/providers/Microsoft.Compute/disks/disk-2", "sub-456"},
		{"invalid-uri", ""},
		{"", ""},
	}
	for _, tc := range tests {
		assert.Equal(t, tc.expect, extractAzureSubscriptionFromDiskURI(tc.uri), "uri=%s", tc.uri)
	}
}
