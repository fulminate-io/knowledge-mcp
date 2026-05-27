// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseProviderID(t *testing.T) {
	cases := []struct {
		name       string
		providerID string
		wantOK     bool
		want       providerVMTarget
	}{
		{
			name:       "gce happy path",
			providerID: "gce://my-project/us-central1-a/gke-main-default-pool-abc-123",
			wantOK:     true,
			want: providerVMTarget{
				Provider:     "gcp",
				Account:      "my-project",
				ID:           "https://www.googleapis.com/compute/v1/projects/my-project/zones/us-central1-a/instances/gke-main-default-pool-abc-123",
				ResourceType: "gcp:compute:instance",
				Region:       "us-central1-a",
			},
		},
		{
			name:       "gce missing instance",
			providerID: "gce://my-project/us-central1-a",
			wantOK:     false,
		},
		{
			name:       "gce missing zone",
			providerID: "gce:///instance-only",
			wantOK:     false,
		},
		{
			name:       "aws with az — account deferred to caller",
			providerID: "aws:///us-east-1a/i-0abc123def456",
			wantOK:     true,
			want: providerVMTarget{
				Provider:     "aws",
				ResourceType: "ec2-instance",
				Region:       "us-east-1",
				InstanceID:   "i-0abc123def456",
			},
		},
		{
			name:       "aws legacy no-az form",
			providerID: "aws:///i-0legacy999",
			wantOK:     true,
			want: providerVMTarget{
				Provider:     "aws",
				ResourceType: "ec2-instance",
				InstanceID:   "i-0legacy999",
			},
		},
		{
			name:       "aws empty",
			providerID: "aws:///",
			wantOK:     false,
		},
		{
			name:       "aws too many segments",
			providerID: "aws:///us-east-1a/i-abc/extra",
			wantOK:     false,
		},
		{
			name:       "azure VM",
			providerID: "azure:///subscriptions/sub-123/resourceGroups/mc_rg/providers/Microsoft.Compute/virtualMachines/aks-node-vm",
			wantOK:     true,
			want: providerVMTarget{
				Provider:     "azure",
				Account:      "sub-123",
				ID:           "/subscriptions/sub-123/resourceGroups/mc_rg/providers/Microsoft.Compute/virtualMachines/aks-node-vm",
				ResourceType: "Microsoft.Compute/virtualMachines",
			},
		},
		{
			name:       "azure VMSS instance (dangling proxy expected)",
			providerID: "azure:///subscriptions/sub-456/resourceGroups/mc_rg/providers/Microsoft.Compute/virtualMachineScaleSets/aks-vmss/virtualMachines/0",
			wantOK:     true,
			want: providerVMTarget{
				Provider:     "azure",
				Account:      "sub-456",
				ID:           "/subscriptions/sub-456/resourceGroups/mc_rg/providers/Microsoft.Compute/virtualMachineScaleSets/aks-vmss/virtualMachines/0",
				ResourceType: "Microsoft.Compute/virtualMachines",
			},
		},
		{
			name:       "azure missing subscription segment",
			providerID: "azure:///resourceGroups/mc_rg/providers/Microsoft.Compute/virtualMachines/foo",
			wantOK:     false,
		},
		{
			name:       "azure non-compute resource",
			providerID: "azure:///subscriptions/sub-123/resourceGroups/rg/providers/Microsoft.Storage/storageAccounts/foo",
			wantOK:     false,
		},
		{
			name:       "empty",
			providerID: "",
			wantOK:     false,
		},
		{
			name:       "unknown scheme",
			providerID: "openstack:///instance-1",
			wantOK:     false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseProviderID(tc.providerID)
			assert.Equal(t, tc.wantOK, ok)
			if !tc.wantOK {
				return
			}
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestFinalizeAWSTarget(t *testing.T) {
	partial := providerVMTarget{
		Provider:     "aws",
		ResourceType: "ec2-instance",
		Region:       "us-east-1",
		InstanceID:   "i-0abc",
	}

	final := finalizeAWSTarget(partial, "123456789012")
	assert.Equal(t, "123456789012", final.Account)
	assert.Equal(t, "arn:aws:ec2:us-east-1:123456789012:instance/i-0abc", final.ID)

	// No account ⇒ input returned unchanged.
	unchanged := finalizeAWSTarget(partial, "")
	assert.Equal(t, partial, unchanged)

	// Non-AWS targets pass through untouched.
	gcp := providerVMTarget{Provider: "gcp", Account: "proj", ID: "self-link"}
	assert.Equal(t, gcp, finalizeAWSTarget(gcp, "any"))

	// Already-finalized target: second call is idempotent.
	final2 := finalizeAWSTarget(final, "999")
	assert.Equal(t, final, final2, "finalizeAWSTarget must be idempotent once Account is set")
}

func TestParseEKSClusterARN(t *testing.T) {
	cases := []struct {
		name        string
		graphName   string
		wantRegion  string
		wantAccount string
		wantCluster string
		wantOK      bool
	}{
		{
			name:        "standard EKS ARN",
			graphName:   "arn:aws:eks:us-east-1:123456789012:cluster/prod",
			wantRegion:  "us-east-1",
			wantAccount: "123456789012",
			wantCluster: "prod",
			wantOK:      true,
		},
		{
			name:      "GovCloud partition is not arn:aws (not supported today)",
			graphName: "arn:aws-us-gov:eks:us-gov-west-1:555:cluster/x",
			wantOK:    false,
		},
		{
			name:      "GKE graph name",
			graphName: "gke_my-project_us-central1_main",
			wantOK:    false,
		},
		{
			name:      "AKS simple name",
			graphName: "aks-dev",
			wantOK:    false,
		},
		{
			name:      "EKS ARN missing cluster suffix",
			graphName: "arn:aws:eks:us-east-1:123456789012:nodegroup/foo",
			wantOK:    false,
		},
		{
			name:      "EKS ARN missing account",
			graphName: "arn:aws:eks:us-east-1::cluster/foo",
			wantOK:    false,
		},
		{
			name:      "EKS ARN empty cluster name",
			graphName: "arn:aws:eks:us-east-1:123456789012:cluster/",
			wantOK:    false,
		},
		{
			name:      "empty",
			graphName: "",
			wantOK:    false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			region, account, cluster, ok := parseEKSClusterARN(tc.graphName)
			assert.Equal(t, tc.wantOK, ok)
			if !tc.wantOK {
				return
			}
			assert.Equal(t, tc.wantRegion, region)
			assert.Equal(t, tc.wantAccount, account)
			assert.Equal(t, tc.wantCluster, cluster)
		})
	}
}

func TestEKSClusterARN_RoundTrip(t *testing.T) {
	cases := []string{
		"arn:aws:eks:us-east-1:123456789012:cluster/prod",
		"arn:aws:eks:eu-west-1:999999999999:cluster/staging",
		"arn:aws:eks:us-west-2:123456789012:cluster/multi-word-cluster",
	}
	for _, arn := range cases {
		t.Run(arn, func(t *testing.T) {
			region, account, cluster, ok := parseEKSClusterARN(arn)
			assert.True(t, ok)
			assert.Equal(t, arn, eksClusterARN(region, account, cluster))
		})
	}
}
