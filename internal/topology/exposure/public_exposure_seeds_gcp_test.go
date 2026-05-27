// SPDX-License-Identifier: Apache-2.0

package exposure

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// public_exposure_seeds_gcp_test.go covers GCP seed rules:
// forwarding rule with EXTERNAL scheme and Cloud Armor security policy.

const gcpSeedsAccount = "gcp-seeds-test"

func buildGCPSeedsFixture(t *testing.T) *cloudFixture {
	t.Helper()
	fx := newCloudFixture(t)
	fx.account(gcpSeedsAccount)
	return fx
}

// TestGCPSeedRules_ForwardingRule_External verifies a forwarding rule
// with EXTERNAL loadBalancingScheme fires the seed rule.
func TestGCPSeedRules_ForwardingRule_External(t *testing.T) {
	fx := buildGCPSeedsFixture(t)
	addSeedNode(t, fx, gcpSeedsAccount, "gcp:fr:external",
		"gcp:compute:forwardingRule", map[string]any{
			"loadBalancingScheme": "EXTERNAL",
		})

	scoped := fx.reader(gcpSeedsAccount)
	seeds := enumerateSeeds(newTestCtx(t), scoped, "gcp")

	require.Len(t, seeds, 1)
	assert.Equal(t, "gcp:fr:external", seeds[0].NodeID)
	assert.InDelta(t, 0.9, seeds[0].EntryScore, 0.0001)
	assert.Contains(t, seeds[0].Reason, "external")
}

// TestGCPSeedRules_ForwardingRule_ExternalManaged verifies
// EXTERNAL_MANAGED also fires.
func TestGCPSeedRules_ForwardingRule_ExternalManaged(t *testing.T) {
	fx := buildGCPSeedsFixture(t)
	addSeedNode(t, fx, gcpSeedsAccount, "gcp:fr:ext-managed",
		"gcp:compute:forwardingRule", map[string]any{
			"loadBalancingScheme": "EXTERNAL_MANAGED",
		})

	scoped := fx.reader(gcpSeedsAccount)
	seeds := enumerateSeeds(newTestCtx(t), scoped, "gcp")

	require.Len(t, seeds, 1)
	assert.Equal(t, "gcp:fr:ext-managed", seeds[0].NodeID)
}

// TestGCPSeedRules_ForwardingRule_Internal verifies an INTERNAL
// forwarding rule does NOT fire.
func TestGCPSeedRules_ForwardingRule_Internal(t *testing.T) {
	fx := buildGCPSeedsFixture(t)
	addSeedNode(t, fx, gcpSeedsAccount, "gcp:fr:internal",
		"gcp:compute:forwardingRule", map[string]any{
			"loadBalancingScheme": "INTERNAL",
		})

	scoped := fx.reader(gcpSeedsAccount)
	seeds := enumerateSeeds(newTestCtx(t), scoped, "gcp")
	assert.Empty(t, seeds)
}

// TestGCPSeedRules_ForwardingRule_MetadataFallback verifies the seed
// rule checks metadata first (set by collector), then falls back to
// content JSON.
func TestGCPSeedRules_ForwardingRule_MetadataFallback(t *testing.T) {
	fx := buildGCPSeedsFixture(t)
	// Create node with loadBalancingScheme in metadata (collector path).
	fx.AddCloudResourceWithContent(gcpSeedsAccount, "gcp:fr:meta", "gcp:fr:meta",
		"gcp:compute:forwardingRule", "{}", map[string]string{
			"loadBalancingScheme": "EXTERNAL",
		})

	scoped := fx.reader(gcpSeedsAccount)
	seeds := enumerateSeeds(newTestCtx(t), scoped, "gcp")

	require.Len(t, seeds, 1)
	assert.Equal(t, "gcp:fr:meta", seeds[0].NodeID)
}

// TestGCPSeedRules_CloudArmor verifies Cloud Armor security policy
// always fires with score 0.7.
func TestGCPSeedRules_CloudArmor(t *testing.T) {
	fx := buildGCPSeedsFixture(t)
	addSeedNode(t, fx, gcpSeedsAccount, "gcp:armor:policy-1",
		"gcp:compute:securityPolicy", map[string]any{
			"name": "my-policy",
		})

	scoped := fx.reader(gcpSeedsAccount)
	seeds := enumerateSeeds(newTestCtx(t), scoped, "gcp")

	var armorSeed *publicSeed
	for i := range seeds {
		if seeds[i].NodeID == "gcp:armor:policy-1" {
			armorSeed = &seeds[i]
			break
		}
	}
	require.NotNil(t, armorSeed)
	assert.InDelta(t, 0.7, armorSeed.EntryScore, 0.0001)
	assert.Contains(t, armorSeed.Reason, "Cloud Armor")
}

// TestGCPSeedRules_MultipleGCPResources verifies multiple GCP resources
// are all enumerated correctly.
func TestGCPSeedRules_MultipleGCPResources(t *testing.T) {
	fx := buildGCPSeedsFixture(t)
	addSeedNode(t, fx, gcpSeedsAccount, "gcp:fr:ext1",
		"gcp:compute:forwardingRule", map[string]any{
			"loadBalancingScheme": "EXTERNAL",
		})
	addSeedNode(t, fx, gcpSeedsAccount, "gcp:armor:p1",
		"gcp:compute:securityPolicy", map[string]any{})

	scoped := fx.reader(gcpSeedsAccount)
	seeds := enumerateSeeds(newTestCtx(t), scoped, "gcp")
	assert.Len(t, seeds, 2)
}
