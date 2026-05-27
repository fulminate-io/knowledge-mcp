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

// buildPolicyNode is a test helper that builds a NetworkPolicy-shaped
// cloud resource node with the given namespace and raw JSON spec wrapped
// in a top-level {"spec": ...} envelope so resolveNetworkPolicyReachability
// can decode it the same way the collector would.
func buildPolicyNode(t *testing.T, id, namespace, specJSON string) *knowledgev1.Node {
	t.Helper()
	content := `{"spec":` + specJSON + `}`
	// Validate the envelope is well-formed JSON so tests fail fast on typos.
	var tmp map[string]any
	require.NoError(t, json.Unmarshal([]byte(content), &tmp), "policy JSON must be valid")

	n := &knowledgev1.Node{
		Id:      id,
		Type:    string(kgtypes.NodeCloudResource),
		Content: content,
	}
	kgtypes.SetValue(n, "resource_type", "NetworkPolicy")
	kgtypes.SetValue(n, "namespace", namespace)
	return n
}

// podEntryWithLabels constructs a podEntry for test fixtures.
func podEntryWithLabels(id, namespace string, lbls map[string]string) podEntry {
	return podEntry{id: id, namespace: namespace, labels: lbls}
}

// collectEdges turns a []knowledgev1.Edge into a set keyed by (from, to, type)
// to make assertions order-independent.
func collectEdges(edges []knowledgev1.Edge) map[string]bool {
	out := make(map[string]bool, len(edges))
	for i := range edges {
		e := &edges[i]
		out[e.FromId+"→"+e.ToId+":"+e.Type] = true
	}
	return out
}

func TestResolveNetworkPolicyReachability_DenyAllIngress(t *testing.T) {
	// podSelector: {} selects ALL pods in namespace; empty ingress: []
	// means "allow no sources". The collector should emit zero ALLOWS
	// edges (the RESTRICTS edges come from the legacy path).
	pods := []podEntry{
		podEntryWithLabels("default/Pod/a", "default", map[string]string{"app": "a"}),
		podEntryWithLabels("default/Pod/b", "default", map[string]string{"app": "b"}),
	}
	nsLabels := map[string]map[string]string{"default": {}}

	policy := buildPolicyNode(t, "default/NetworkPolicy/deny", "default",
		`{"podSelector":{},"policyTypes":["Ingress"],"ingress":[]}`)

	edges, err := resolveNetworkPolicyReachability(pods, nsLabels, nil, []*knowledgev1.Node{policy})
	require.NoError(t, err)
	assert.Empty(t, edges, "deny-all policy must not emit ALLOWS edges")
}

func TestResolveNetworkPolicyReachability_AllowFromSameNamespace(t *testing.T) {
	pods := []podEntry{
		podEntryWithLabels("default/Pod/backend", "default", map[string]string{"app": "backend"}),
		podEntryWithLabels("default/Pod/frontend", "default", map[string]string{"app": "frontend"}),
	}
	nsLabels := map[string]map[string]string{"default": {}}

	policy := buildPolicyNode(t, "default/NetworkPolicy/allow-fe", "default",
		`{"podSelector":{"matchLabels":{"app":"backend"}},"policyTypes":["Ingress"],`+
			`"ingress":[{"from":[{"podSelector":{"matchLabels":{"app":"frontend"}}}]}]}`)

	edges, err := resolveNetworkPolicyReachability(pods, nsLabels, nil, []*knowledgev1.Node{policy})
	require.NoError(t, err)

	set := collectEdges(edges)
	assert.True(t, set["default/Pod/backend→default/Pod/frontend:ALLOWS_INGRESS_FROM"],
		"expected backend → frontend ingress edge, got: %v", set)
	assert.Len(t, edges, 1, "exactly one edge for single-target/single-source")
}

func TestResolveNetworkPolicyReachability_AllowFromNamespaceSelector(t *testing.T) {
	pods := []podEntry{
		podEntryWithLabels("prod/Pod/backend", "prod", map[string]string{"app": "backend"}),
		podEntryWithLabels("monitoring/Pod/prom", "monitoring", map[string]string{"app": "prom"}),
		podEntryWithLabels("monitoring/Pod/grafana", "monitoring", map[string]string{"app": "grafana"}),
		podEntryWithLabels("other/Pod/rogue", "other", map[string]string{"app": "rogue"}),
	}
	nsLabels := map[string]map[string]string{
		"prod":       {"tier": "prod"},
		"monitoring": {"purpose": "monitoring"},
		"other":      {"purpose": "other"},
	}

	policy := buildPolicyNode(t, "prod/NetworkPolicy/allow-mon", "prod",
		`{"podSelector":{"matchLabels":{"app":"backend"}},"policyTypes":["Ingress"],`+
			`"ingress":[{"from":[{"namespaceSelector":{"matchLabels":{"purpose":"monitoring"}}}]}]}`)

	edges, err := resolveNetworkPolicyReachability(pods, nsLabels, nil, []*knowledgev1.Node{policy})
	require.NoError(t, err)

	set := collectEdges(edges)
	assert.True(t, set["prod/Pod/backend→monitoring/Pod/prom:ALLOWS_INGRESS_FROM"],
		"expected backend → prom, got: %v", set)
	assert.True(t, set["prod/Pod/backend→monitoring/Pod/grafana:ALLOWS_INGRESS_FROM"],
		"expected backend → grafana, got: %v", set)
	assert.False(t, set["prod/Pod/backend→other/Pod/rogue:ALLOWS_INGRESS_FROM"],
		"pods in other namespace must not match the monitoring selector")
	assert.Len(t, edges, 2)
}

func TestResolveNetworkPolicyReachability_EmptyNamespaceSelector_AllNamespaces(t *testing.T) {
	// namespaceSelector: {} matches EVERY namespace including unlabeled ones.
	pods := []podEntry{
		podEntryWithLabels("prod/Pod/backend", "prod", map[string]string{"app": "backend"}),
		podEntryWithLabels("default/Pod/a", "default", map[string]string{"app": "a"}),
		podEntryWithLabels("unlabeled/Pod/b", "unlabeled", map[string]string{"app": "b"}),
	}
	nsLabels := map[string]map[string]string{
		"prod":      {"tier": "prod"},
		"default":   {},
		"unlabeled": {}, // no labels at all
	}

	policy := buildPolicyNode(t, "prod/NetworkPolicy/allow-any-ns", "prod",
		`{"podSelector":{"matchLabels":{"app":"backend"}},"policyTypes":["Ingress"],`+
			`"ingress":[{"from":[{"namespaceSelector":{}}]}]}`)

	edges, err := resolveNetworkPolicyReachability(pods, nsLabels, nil, []*knowledgev1.Node{policy})
	require.NoError(t, err)

	set := collectEdges(edges)
	assert.True(t, set["prod/Pod/backend→default/Pod/a:ALLOWS_INGRESS_FROM"])
	assert.True(t, set["prod/Pod/backend→unlabeled/Pod/b:ALLOWS_INGRESS_FROM"],
		"empty namespaceSelector must match unlabeled namespaces too")
	// backend → itself is filtered (self edge), so we expect 2 edges total
	// (one to each of the two non-self pods).
	assert.Len(t, edges, 2)
}

func TestResolveNetworkPolicyReachability_MatchCriteria_SkipsUnlabeled(t *testing.T) {
	// A match-criteria namespaceSelector (matchLabels) must NOT match a
	// namespace with zero labels — this contrasts with the empty selector
	// {} case above.
	pods := []podEntry{
		podEntryWithLabels("prod/Pod/backend", "prod", map[string]string{"app": "backend"}),
		podEntryWithLabels("labeled/Pod/a", "labeled", map[string]string{"app": "a"}),
		podEntryWithLabels("unlabeled/Pod/b", "unlabeled", map[string]string{"app": "b"}),
	}
	nsLabels := map[string]map[string]string{
		"prod":      {"tier": "prod"},
		"labeled":   {"zone": "east"},
		"unlabeled": {}, // no labels
	}

	policy := buildPolicyNode(t, "prod/NetworkPolicy/strict", "prod",
		`{"podSelector":{"matchLabels":{"app":"backend"}},"policyTypes":["Ingress"],`+
			`"ingress":[{"from":[{"namespaceSelector":{"matchLabels":{"zone":"east"}}}]}]}`)

	edges, err := resolveNetworkPolicyReachability(pods, nsLabels, nil, []*knowledgev1.Node{policy})
	require.NoError(t, err)

	set := collectEdges(edges)
	assert.True(t, set["prod/Pod/backend→labeled/Pod/a:ALLOWS_INGRESS_FROM"])
	assert.False(t, set["prod/Pod/backend→unlabeled/Pod/b:ALLOWS_INGRESS_FROM"],
		"match-criteria selector must skip unlabeled namespaces")
	assert.Len(t, edges, 1)
}

func TestResolveNetworkPolicyReachability_MatchExpressionsExists(t *testing.T) {
	// matchExpressions with the Exists operator requires the label key to
	// be present on the pod/namespace, regardless of its value.
	pods := []podEntry{
		podEntryWithLabels("default/Pod/target", "default", map[string]string{"role": "target"}),
		podEntryWithLabels("default/Pod/has-audit", "default", map[string]string{"audit": "whatever"}),
		podEntryWithLabels("default/Pod/no-audit", "default", map[string]string{"other": "stuff"}),
	}
	nsLabels := map[string]map[string]string{"default": {}}

	policy := buildPolicyNode(t, "default/NetworkPolicy/audit", "default",
		`{"podSelector":{"matchLabels":{"role":"target"}},"policyTypes":["Ingress"],`+
			`"ingress":[{"from":[{"podSelector":{"matchExpressions":[`+
			`{"key":"audit","operator":"Exists"}]}}]}]}`)

	edges, err := resolveNetworkPolicyReachability(pods, nsLabels, nil, []*knowledgev1.Node{policy})
	require.NoError(t, err)

	set := collectEdges(edges)
	assert.True(t, set["default/Pod/target→default/Pod/has-audit:ALLOWS_INGRESS_FROM"],
		"Exists must match pods with the key present")
	assert.False(t, set["default/Pod/target→default/Pod/no-audit:ALLOWS_INGRESS_FROM"],
		"Exists must not match pods missing the key")
	assert.Len(t, edges, 1)
}

func TestResolveNetworkPolicyReachability_IpBlockSkipped(t *testing.T) {
	// A peer with only ipBlock set must not emit any pod-to-pod edges.
	// The analyzer handles ipBlock via a JSON re-parse per the layering rule.
	pods := []podEntry{
		podEntryWithLabels("default/Pod/a", "default", map[string]string{"app": "a"}),
		podEntryWithLabels("default/Pod/b", "default", map[string]string{"app": "b"}),
	}
	nsLabels := map[string]map[string]string{"default": {}}

	policy := buildPolicyNode(t, "default/NetworkPolicy/cidr", "default",
		`{"podSelector":{"matchLabels":{"app":"a"}},"policyTypes":["Ingress"],`+
			`"ingress":[{"from":[{"ipBlock":{"cidr":"10.0.0.0/8"}}]}]}`)

	edges, err := resolveNetworkPolicyReachability(pods, nsLabels, nil, []*knowledgev1.Node{policy})
	require.NoError(t, err)
	assert.Empty(t, edges, "ipBlock-only peer must produce no pod-to-pod edges")
}

func TestResolveNetworkPolicyReachability_Egress(t *testing.T) {
	// Egress rule: backend is allowed to egress to database pods in the
	// same namespace. Expected edge: backend → db (ALLOWS_EGRESS_TO).
	pods := []podEntry{
		podEntryWithLabels("default/Pod/backend", "default", map[string]string{"app": "backend"}),
		podEntryWithLabels("default/Pod/db", "default", map[string]string{"app": "db"}),
		podEntryWithLabels("default/Pod/cache", "default", map[string]string{"app": "cache"}),
	}
	nsLabels := map[string]map[string]string{"default": {}}

	policy := buildPolicyNode(t, "default/NetworkPolicy/egress-to-db", "default",
		`{"podSelector":{"matchLabels":{"app":"backend"}},"policyTypes":["Egress"],`+
			`"egress":[{"to":[{"podSelector":{"matchLabels":{"app":"db"}}}]}]}`)

	edges, err := resolveNetworkPolicyReachability(pods, nsLabels, nil, []*knowledgev1.Node{policy})
	require.NoError(t, err)

	set := collectEdges(edges)
	assert.True(t, set["default/Pod/backend→default/Pod/db:ALLOWS_EGRESS_TO"],
		"expected backend → db egress edge, got: %v", set)
	assert.False(t, set["default/Pod/backend→default/Pod/cache:ALLOWS_EGRESS_TO"],
		"cache must not match the db selector")
	assert.Len(t, edges, 1)
}
