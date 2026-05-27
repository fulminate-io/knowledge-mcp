// SPDX-License-Identifier: Apache-2.0

package cloud

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

func TestBuildNode_Basic(t *testing.T) {
	content, err := json.Marshal(map[string]string{"instance_type": "m5.large"})
	require.NoError(t, err)

	spec := ResourceSpec{
		ID:           "arn:aws:ec2:us-east-1:123456789012:instance/i-0abcdef1234567890",
		Name:         "my-instance",
		ResourceType: "ec2:instance",
		Region:       "us-east-1",
		Content:      content,
		Metadata: map[string]string{
			"account": "123456789012",
			"vpc_id":  "vpc-abc123",
		},
	}

	n := BuildNode(spec)

	assert.Equal(t, spec.ID, n.Id)
	assert.Equal(t, string(kgtypes.NodeCloudResource), n.Type)
	assert.Equal(t, "my-instance", n.SymbolName)
	assert.Equal(t, string(content), n.Content)
	assert.Equal(t, "cloud", n.Source)

	// Required metadata fields.
	assert.Equal(t, "ec2:instance", kgtypes.Value(n, "resource_type"))
	assert.Equal(t, "us-east-1", kgtypes.Value(n, "region"))

	// Custom metadata preserved.
	assert.Equal(t, "123456789012", kgtypes.Value(n, "account"))
	assert.Equal(t, "vpc-abc123", kgtypes.Value(n, "vpc_id"))
}

func TestBuildNode_EmptyRegion(t *testing.T) {
	spec := ResourceSpec{
		ID:           "gs://my-bucket",
		Name:         "my-bucket",
		ResourceType: "gcs:bucket",
		Region:       "", // global resource, no region
		Content:      []byte(`{}`),
	}

	n := BuildNode(spec)

	assert.Equal(t, "gcs:bucket", kgtypes.Value(n, "resource_type"))
	_, hasRegion := n.Metadata["region"]
	assert.False(t, hasRegion, "region key should not be set when Region is empty")
}

func TestBuildNode_PreservesMetadata(t *testing.T) {
	spec := ResourceSpec{
		ID:           "projects/my-proj/zones/us-central1-a/instances/vm-1",
		Name:         "vm-1",
		ResourceType: "gce:instance",
		Region:       "us-central1-a",
		Content:      []byte(`{}`),
		Metadata: map[string]string{
			"project":   "my-proj",
			"labels":    "env=prod",
			"custom_id": "12345",
		},
	}

	n := BuildNode(spec)

	// Verify custom metadata is present.
	assert.Equal(t, "my-proj", kgtypes.Value(n, "project"))
	assert.Equal(t, "env=prod", kgtypes.Value(n, "labels"))
	assert.Equal(t, "12345", kgtypes.Value(n, "custom_id"))

	// Verify resource_type and region are also present (not overwritten by custom keys).
	assert.Equal(t, "gce:instance", kgtypes.Value(n, "resource_type"))
	assert.Equal(t, "us-central1-a", kgtypes.Value(n, "region"))
}

func TestBuildNode_IDIsUnmodified(t *testing.T) {
	tests := []struct {
		name string
		id   string
	}{
		{
			name: "AWS ARN",
			id:   "arn:aws:iam::123456789012:role/my-role",
		},
		{
			name: "GCP self-link",
			id:   "https://www.googleapis.com/compute/v1/projects/my-proj/zones/us-central1-a/instances/vm-1",
		},
		{
			name: "Azure resource ID",
			id:   "/subscriptions/sub-id/resourceGroups/rg/providers/Microsoft.Compute/virtualMachines/my-vm",
		},
		{
			name: "k8s namespace/kind/name",
			id:   "default/Deployment/nginx",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := ResourceSpec{
				ID:           tt.id,
				Name:         "test",
				ResourceType: "test:resource",
				Content:      []byte(`{}`),
			}

			n := BuildNode(spec)
			assert.Equal(t, tt.id, n.Id, "node ID must be the exact cloud provider ID with no transformation")
		})
	}
}

func TestBuildEdge_Basic(t *testing.T) {
	spec := EdgeSpec{
		SourceID:     "arn:aws:ec2:us-east-1:123456789012:instance/i-abc",
		TargetID:     "arn:aws:ec2:us-east-1:123456789012:security-group/sg-xyz",
		Relationship: kgtypes.EdgeContains,
	}

	e := BuildEdge(spec)

	assert.Equal(t, -1, e.FromIdx, "FromIdx must be -1 to use FromID")
	assert.Equal(t, -1, e.ToIdx, "ToIdx must be -1 to use ToID")
	assert.Equal(t, spec.SourceID, e.FromID)
	assert.Equal(t, spec.TargetID, e.ToID)
	assert.Equal(t, kgtypes.EdgeContains, e.Type)
}

func TestBuildEdge_RelationshipPreserved(t *testing.T) {
	spec := EdgeSpec{
		SourceID:     "vpc-123",
		TargetID:     "subnet-456",
		Relationship: kgtypes.EdgeDependsOn,
	}

	e := BuildEdge(spec)

	assert.Equal(t, kgtypes.EdgeDependsOn, e.Type, "edge type must match the EdgeSpec relationship exactly")
}

func TestBuildEdge_MetadataSerializedAsEvidence(t *testing.T) {
	spec := EdgeSpec{
		SourceID:     "arn:aws:ec2:us-east-1:123456789012:instance/i-abc",
		TargetID:     "arn:aws:iam::123456789012:role/my-role",
		Relationship: kgtypes.EdgeAssumesRole,
		Metadata: map[string]string{
			"role_source": "instance-profile",
			"profile_arn": "arn:aws:iam::123456789012:instance-profile/my-profile",
		},
	}

	e := BuildEdge(spec)

	assert.Equal(t, "cloud-collect", e.Method)
	assert.NotEmpty(t, e.Evidence)

	// Verify Evidence is valid JSON that round-trips back to the original map.
	var got map[string]string
	err := json.Unmarshal([]byte(e.Evidence), &got)
	require.NoError(t, err, "Evidence must be valid JSON")
	assert.Equal(t, "instance-profile", got["role_source"])
	assert.Equal(t, "arn:aws:iam::123456789012:instance-profile/my-profile", got["profile_arn"])
}

func TestBuildEdge_NilMetadataProducesEmptyEvidence(t *testing.T) {
	spec := EdgeSpec{
		SourceID:     "vpc-123",
		TargetID:     "subnet-456",
		Relationship: kgtypes.EdgeDependsOn,
		Metadata:     nil,
	}

	e := BuildEdge(spec)

	assert.Empty(t, e.Method, "Method must be empty when Metadata is nil")
	assert.Empty(t, e.Evidence, "Evidence must be empty when Metadata is nil")
}

func TestBuildEdge_EmptyMetadataProducesEmptyEvidence(t *testing.T) {
	spec := EdgeSpec{
		SourceID:     "vpc-123",
		TargetID:     "subnet-456",
		Relationship: kgtypes.EdgeDependsOn,
		Metadata:     map[string]string{},
	}

	e := BuildEdge(spec)

	assert.Empty(t, e.Method, "Method must be empty when Metadata is empty")
	assert.Empty(t, e.Evidence, "Evidence must be empty when Metadata is empty")
}

func TestBuildNode_NilMetadata(t *testing.T) {
	spec := ResourceSpec{
		ID:           "arn:aws:s3:::my-bucket",
		Name:         "my-bucket",
		ResourceType: "s3:bucket",
		Region:       "us-west-2",
		Content:      []byte(`{"name":"my-bucket"}`),
		Metadata:     nil,
	}

	n := BuildNode(spec)

	// Should not panic with nil metadata.
	assert.Equal(t, "s3:bucket", kgtypes.Value(n, "resource_type"))
	assert.Equal(t, "us-west-2", kgtypes.Value(n, "region"))
	assert.Len(t, n.Metadata, 2, "only resource_type and region should be set")
}
