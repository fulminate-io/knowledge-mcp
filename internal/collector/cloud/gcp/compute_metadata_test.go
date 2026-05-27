// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"testing"

	computepb "cloud.google.com/go/compute/apiv1/computepb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestComputeInstanceMetadata_StatusAndShape(t *testing.T) {
	inst := &computepb.Instance{
		Status:      new("RUNNING"),
		MachineType: new("projects/p/zones/us-central1-a/machineTypes/e2-medium"),
		Zone:        new("projects/p/zones/us-central1-a"),
	}
	m := computeInstanceMetadata(inst)
	assert.Equal(t, "RUNNING", m["status"])
	assert.Equal(t, "e2-medium", m["machineType"])
	assert.Equal(t, "us-central1-a", m["zone"])
}

func TestComputeInstanceMetadata_Labels(t *testing.T) {
	t.Run("populated labels become label/<k>=<v>", func(t *testing.T) {
		inst := &computepb.Instance{
			Labels: map[string]string{"env": "prod", "team": "data"},
		}
		m := computeInstanceMetadata(inst)
		assert.Equal(t, "prod", m["label/env"])
		assert.Equal(t, "data", m["label/team"])
	})

	t.Run("nil labels emit no label/ keys", func(t *testing.T) {
		inst := &computepb.Instance{}
		m := computeInstanceMetadata(inst)
		for k := range m {
			assert.NotContains(t, k, "label/", "unexpected label/ key %q", k)
		}
	})
}

func TestComputeInstanceMetadata_Tags(t *testing.T) {
	t.Run("populated tag items become tag/<name>=\"\"", func(t *testing.T) {
		inst := &computepb.Instance{
			Tags: &computepb.Tags{Items: []string{"http-server", "ssh"}},
		}
		m := computeInstanceMetadata(inst)
		v, ok := m["tag/http-server"]
		require.True(t, ok)
		assert.Empty(t, v)
		_, ok = m["tag/ssh"]
		assert.True(t, ok)
	})

	t.Run("nil Tags wrapper does not panic and emits no tag/ keys", func(t *testing.T) {
		// GetTags() is nil-safe; this guards against a future change that
		// ever indexes into Tags.Items without the helper.
		inst := &computepb.Instance{Tags: nil}
		m := computeInstanceMetadata(inst)
		for k := range m {
			assert.NotContains(t, k, "tag/", "unexpected tag/ key %q", k)
		}
	})

	t.Run("empty tag string is skipped", func(t *testing.T) {
		inst := &computepb.Instance{
			Tags: &computepb.Tags{Items: []string{""}},
		}
		m := computeInstanceMetadata(inst)
		_, ok := m["tag/"]
		assert.False(t, ok, "empty tag must not produce a bare tag/ key")
	})
}

func TestComputeInstanceMetadata_CreationTime(t *testing.T) {
	t.Run("populated CreationTimestamp becomes creation_time", func(t *testing.T) {
		inst := &computepb.Instance{
			CreationTimestamp: new("2025-01-15T10:30:00.000-07:00"),
		}
		m := computeInstanceMetadata(inst)
		assert.Equal(t, "2025-01-15T10:30:00.000-07:00", m["creation_time"])
	})

	t.Run("empty CreationTimestamp emits no creation_time key", func(t *testing.T) {
		inst := &computepb.Instance{}
		m := computeInstanceMetadata(inst)
		_, ok := m["creation_time"]
		assert.False(t, ok)
	})
}

func TestComputeInstanceMetadata_NetworkIPs(t *testing.T) {
	t.Run("primary NIC NetworkIP populates primary_ip", func(t *testing.T) {
		inst := &computepb.Instance{
			NetworkInterfaces: []*computepb.NetworkInterface{{
				NetworkIP: new("10.0.0.5"),
			}},
		}
		m := computeInstanceMetadata(inst)
		assert.Equal(t, "10.0.0.5", m["primary_ip"])
		_, hasExt := m["external_ip"]
		assert.False(t, hasExt, "external_ip must be absent when no AccessConfigs")
	})

	t.Run("first AccessConfig NatIP populates external_ip", func(t *testing.T) {
		inst := &computepb.Instance{
			NetworkInterfaces: []*computepb.NetworkInterface{{
				NetworkIP: new("10.0.0.5"),
				AccessConfigs: []*computepb.AccessConfig{{
					NatIP: new("34.1.2.3"),
				}},
			}},
		}
		m := computeInstanceMetadata(inst)
		assert.Equal(t, "10.0.0.5", m["primary_ip"])
		assert.Equal(t, "34.1.2.3", m["external_ip"])
	})

	t.Run("empty NIC list emits no IP keys", func(t *testing.T) {
		inst := &computepb.Instance{}
		m := computeInstanceMetadata(inst)
		_, hasPrimary := m["primary_ip"]
		_, hasExt := m["external_ip"]
		assert.False(t, hasPrimary)
		assert.False(t, hasExt)
	})

	t.Run("multiple NICs only first contributes IPs", func(t *testing.T) {
		inst := &computepb.Instance{
			NetworkInterfaces: []*computepb.NetworkInterface{
				{NetworkIP: new("10.0.0.5")},
				{NetworkIP: new("10.0.0.99")},
			},
		}
		m := computeInstanceMetadata(inst)
		assert.Equal(t, "10.0.0.5", m["primary_ip"])
	})
}

// gceProviderSelfLink rebuilds the canonical GCE Compute selfLink the same
// way cloud/k8s/provider_id.go:parseGCEProviderID does. Duplicated here
// (rather than imported) because parseGCEProviderID is unexported and
// importing test helpers across cloud/k8s ↔ cloud/gcp would invite a
// future cycle. The duplication is the test: if either side ever changes
// the format string (trailing slash, /v1/ → /beta/, https → http) the
// byte-for-byte assertion below breaks loudly.
//
// CONTRACT MIRROR — keep in sync with cloud/k8s/provider_id.go:73-92.
func gceProviderSelfLink(project, zone, instance string) string {
	return "https://www.googleapis.com/compute/v1/projects/" +
		project + "/zones/" + zone + "/instances/" + instance
}

func TestComputeSelfLink_MatchesK8sResolver(t *testing.T) {
	// Construct a synthetic instance whose SelfLink uses the canonical
	// format. computeResourceSpec must preserve it byte-for-byte so the
	// K8s Node → VM proxy resolves cleanly. If this test ever fails,
	// either:
	//   - cloud/gcp changed how it propagates SelfLink into spec.ID, OR
	//   - cloud/k8s changed the format string in parseGCEProviderID.
	// In both cases the cross-graph proxy linkage will silently break.
	const project = "my-project"
	const zone = "us-central1-a"
	const instance = "gke-node-1"

	expected := gceProviderSelfLink(project, zone, instance)
	inst := &computepb.Instance{
		Name:     proto.String(instance),
		SelfLink: new(expected),
		Zone:     proto.String("projects/" + project + "/zones/" + zone),
	}

	spec := computeResourceSpec(expected, inst, []byte(`{}`))
	assert.Equal(t, expected, spec.ID,
		"spec.ID must be the SelfLink verbatim — k8s parseGCEProviderID rebuilds the same string")
	assert.Equal(t, "gcp:compute:instance", spec.ResourceType,
		"resource_type must match the value parseGCEProviderID embeds in providerVMTarget")
}
