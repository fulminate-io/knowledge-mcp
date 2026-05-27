// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// buildANPNode builds an AdminNetworkPolicy-shaped cloud resource node with
// the given JSON spec wrapped in the ANP envelope.
func buildANPNode(t *testing.T, id, specJSON string) *knowledgev1.Node {
	t.Helper()
	content := `{"spec":` + specJSON + `}`
	var tmp map[string]any
	require.NoError(t, json.Unmarshal([]byte(content), &tmp), "ANP JSON must be valid")

	n := &knowledgev1.Node{
		Id:      id,
		Type:    string(kgtypes.NodeCloudResource),
		Content: content,
	}
	kgtypes.SetValue(n, "resource_type", "AdminNetworkPolicy")
	return n
}

// decodeAnpEvidence parses the Evidence JSON into anpPortMetadata so tests
// can assert the priority/action stamping is correct.
func decodeAnpEvidence(t *testing.T, evidence string) anpPortMetadata {
	t.Helper()
	var m anpPortMetadata
	require.NoError(t, json.Unmarshal([]byte(evidence), &m))
	return m
}

func TestResolveANPReachability_AllowIngress(t *testing.T) {
	// ANP at priority 10: every pod in namespaces with label tier=prod
	// allows ingress from pods in namespaces tier=monitoring on TCP/9090.
	pods := []podEntry{
		podEntryWithLabels("prod/Pod/api", "prod", map[string]string{"app": "api"}),
		podEntryWithLabels("monitoring/Pod/prom", "monitoring", map[string]string{"app": "prom"}),
		podEntryWithLabels("other/Pod/x", "other", nil),
	}
	nsLabels := map[string]map[string]string{
		"prod":       {"tier": "prod"},
		"monitoring": {"tier": "monitoring"},
		"other":      {"tier": "other"},
	}

	policy := buildANPNode(t, "AdminNetworkPolicy/allow-mon",
		`{
			"priority":10,
			"subject":{"namespaces":{"matchLabels":{"tier":"prod"}}},
			"ingress":[{
				"action":"Allow",
				"from":[{"namespaces":{"matchLabels":{"tier":"monitoring"}}}],
				"ports":[{"protocol":"TCP","port":9090}]
			}]
		}`)

	edges, err := resolveANPReachability(pods, nsLabels, nil, []*knowledgev1.Node{policy})
	require.NoError(t, err)
	require.Len(t, edges, 1, "exactly one prod/api ← monitoring/prom edge expected")

	e := &edges[0]
	assert.Equal(t, "prod/Pod/api", e.FromId)
	assert.Equal(t, "monitoring/Pod/prom", e.ToId)
	assert.Equal(t, string(kgtypes.EdgeANPIngressFrom), e.Type)
	assert.Equal(t, methodAdminNetworkPolicy, e.Method)

	meta := decodeAnpEvidence(t, e.Evidence)
	assert.True(t, meta.IsANP, "is_anp must be true on ANP edges")
	assert.Equal(t, 10, meta.ANPPriority)
	assert.Equal(t, "Allow", meta.ANPAction)
	assert.Equal(t, "TCP", meta.Protocol)
	assert.Equal(t, 9090, meta.PortFrom)
	assert.Equal(t, 9090, meta.PortTo)
}

func TestResolveANPReachability_DenyEgress(t *testing.T) {
	// ANP at priority 5: backend pods in tier=prod deny egress to ns
	// tier=untrusted, all ports.
	pods := []podEntry{
		podEntryWithLabels("prod/Pod/backend", "prod", map[string]string{"app": "backend"}),
		podEntryWithLabels("untrusted/Pod/x", "untrusted", nil),
	}
	nsLabels := map[string]map[string]string{
		"prod":      {"tier": "prod"},
		"untrusted": {"tier": "untrusted"},
	}

	policy := buildANPNode(t, "AdminNetworkPolicy/deny-untrusted",
		`{
			"priority":5,
			"subject":{
				"pods":{
					"namespaceSelector":{"matchLabels":{"tier":"prod"}},
					"podSelector":{"matchLabels":{"app":"backend"}}
				}
			},
			"egress":[{
				"action":"Deny",
				"to":[{"namespaces":{"matchLabels":{"tier":"untrusted"}}}]
			}]
		}`)

	edges, err := resolveANPReachability(pods, nsLabels, nil, []*knowledgev1.Node{policy})
	require.NoError(t, err)
	require.Len(t, edges, 1)

	e := &edges[0]
	assert.Equal(t, "prod/Pod/backend", e.FromId)
	assert.Equal(t, "untrusted/Pod/x", e.ToId)
	assert.Equal(t, string(kgtypes.EdgeANPEgressTo), e.Type)
	meta := decodeAnpEvidence(t, e.Evidence)
	assert.True(t, meta.IsANP)
	assert.Equal(t, 5, meta.ANPPriority)
	assert.Equal(t, "Deny", meta.ANPAction)
}

func TestResolveANPReachability_PassAction(t *testing.T) {
	// Pass falls through to NetworkPolicy at canReach time. The collector
	// still emits the edge with action="Pass" so the analyzer can recognize
	// the fallthrough.
	pods := []podEntry{
		podEntryWithLabels("ns/Pod/a", "ns", nil),
		podEntryWithLabels("ns/Pod/b", "ns", nil),
	}
	nsLabels := map[string]map[string]string{"ns": {"k": "v"}}

	policy := buildANPNode(t, "AdminNetworkPolicy/pass",
		`{
			"priority":50,
			"subject":{"namespaces":{"matchLabels":{"k":"v"}}},
			"ingress":[{
				"action":"Pass",
				"from":[{"namespaces":{"matchLabels":{"k":"v"}}}]
			}]
		}`)

	edges, err := resolveANPReachability(pods, nsLabels, nil, []*knowledgev1.Node{policy})
	require.NoError(t, err)
	// 2 targets × 1 source each (excluding self) = 2 edges.
	require.Len(t, edges, 2)
	for i := range edges {
		meta := decodeAnpEvidence(t, edges[i].Evidence)
		assert.Equal(t, "Pass", meta.ANPAction)
		assert.Equal(t, 50, meta.ANPPriority)
		assert.True(t, meta.IsANP)
	}
}

func TestResolveANPReachability_NoSubject(t *testing.T) {
	// A policy with no subject must produce zero edges (and not panic).
	pods := []podEntry{podEntryWithLabels("ns/Pod/a", "ns", nil)}
	nsLabels := map[string]map[string]string{"ns": {}}

	policy := buildANPNode(t, "AdminNetworkPolicy/empty",
		`{"priority":1,"subject":{},"ingress":[{"action":"Allow"}]}`)

	edges, err := resolveANPReachability(pods, nsLabels, nil, []*knowledgev1.Node{policy})
	require.NoError(t, err)
	assert.Empty(t, edges)
}
