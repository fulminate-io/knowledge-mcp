// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// decodePortMetadata is a test helper that decodes an Edge.Evidence payload
// back into a portMetadata value. An empty string decodes to the zero value
// (fully-open), matching the production encode() contract.
func decodePortMetadata(t *testing.T, evidence string) portMetadata {
	t.Helper()
	if evidence == "" {
		return portMetadata{}
	}
	var pm portMetadata
	require.NoError(t, json.Unmarshal([]byte(evidence), &pm), "evidence must be valid JSON")
	return pm
}

// findEdgeByFromTo returns the first edge in the slice matching the given
// (from, to) endpoints, or nil if none. Used by port tests that expect a
// single edge per pair.
func findEdgeByFromTo(edges []knowledgev1.Edge, from, to string) *knowledgev1.Edge {
	for i := range edges {
		if edges[i].FromId == from && edges[i].ToId == to {
			return &edges[i]
		}
	}
	return nil
}

// TestResolveNetworkPolicyReachability_AllPortsFullyOpen asserts that a
// rule with no ports[] slice emits a SINGLE edge per (target, peer) pair
// whose Method is stamped with methodNetworkPolicy but whose Evidence
// payload is the empty string — the canonical "all ports, all protocols"
// (fully-open) signal documented in the edge metadata schema.
//
// Satisfies criterion 45d7865e366b50e8b85c46033515f403.
func TestResolveNetworkPolicyReachability_AllPortsFullyOpen(t *testing.T) {
	pods := []podEntry{
		podEntryWithLabels("default/Pod/backend", "default", map[string]string{"app": "backend"}),
		podEntryWithLabels("default/Pod/frontend", "default", map[string]string{"app": "frontend"}),
	}
	nsLabels := map[string]map[string]string{"default": {}}

	// Policy with no ports specified — means "allow all ports from frontend".
	policy := buildPolicyNode(t, "default/NetworkPolicy/open", "default",
		`{"podSelector":{"matchLabels":{"app":"backend"}},"policyTypes":["Ingress"],`+
			`"ingress":[{"from":[{"podSelector":{"matchLabels":{"app":"frontend"}}}]}]}`)

	edges, err := resolveNetworkPolicyReachability(pods, nsLabels, nil, []*knowledgev1.Node{policy})
	require.NoError(t, err)
	require.Len(t, edges, 1, "expected exactly one edge for (backend, frontend) fully-open pair")

	e := &edges[0]
	assert.Equal(t, "default/Pod/backend", e.FromId)
	assert.Equal(t, "default/Pod/frontend", e.ToId)
	assert.Equal(t, string(kgtypes.EdgeAllowsIngressFrom), e.Type)
	assert.Equal(t, methodNetworkPolicy, e.Method, "edge must be stamped with the NetworkPolicy method")
	assert.Empty(t, e.Evidence, "fully-open edges must have empty Evidence (canonical all-ports signal)")

	// Decoded metadata must be the zero value and must report itself as open.
	pm := decodePortMetadata(t, e.Evidence)
	assert.True(t, pm.isOpen(), "empty Evidence must decode to a fully-open portMetadata")
	assert.Empty(t, pm.Protocol)
	assert.Zero(t, pm.PortFrom)
	assert.Zero(t, pm.PortTo)
	assert.Empty(t, pm.NamedPort)
	assert.False(t, pm.PortUnresolved)
}

// TestResolveNetworkPolicyReachability_NumericPort verifies that a rule
// with a numeric port produces an edge whose Evidence carries the
// protocol and port_from==port_to for the single port.
func TestResolveNetworkPolicyReachability_NumericPort(t *testing.T) {
	pods := []podEntry{
		podEntryWithLabels("default/Pod/backend", "default", map[string]string{"app": "backend"}),
		podEntryWithLabels("default/Pod/frontend", "default", map[string]string{"app": "frontend"}),
	}
	nsLabels := map[string]map[string]string{"default": {}}

	policy := buildPolicyNode(t, "default/NetworkPolicy/port80", "default",
		`{"podSelector":{"matchLabels":{"app":"backend"}},"policyTypes":["Ingress"],`+
			`"ingress":[{"from":[{"podSelector":{"matchLabels":{"app":"frontend"}}}],`+
			`"ports":[{"protocol":"TCP","port":80}]}]}`)

	edges, err := resolveNetworkPolicyReachability(pods, nsLabels, nil, []*knowledgev1.Node{policy})
	require.NoError(t, err)
	require.Len(t, edges, 1)

	pm := decodePortMetadata(t, edges[0].Evidence)
	assert.Equal(t, "TCP", pm.Protocol)
	assert.Equal(t, 80, pm.PortFrom)
	assert.Equal(t, 80, pm.PortTo)
	assert.Empty(t, pm.NamedPort)
	assert.False(t, pm.PortUnresolved)
}

// TestResolveNetworkPolicyReachability_PortRange verifies endPort expands
// the port_to field so a port range is preserved as (from, to).
func TestResolveNetworkPolicyReachability_PortRange(t *testing.T) {
	pods := []podEntry{
		podEntryWithLabels("default/Pod/backend", "default", map[string]string{"app": "backend"}),
		podEntryWithLabels("default/Pod/frontend", "default", map[string]string{"app": "frontend"}),
	}
	nsLabels := map[string]map[string]string{"default": {}}

	policy := buildPolicyNode(t, "default/NetworkPolicy/range", "default",
		`{"podSelector":{"matchLabels":{"app":"backend"}},"policyTypes":["Ingress"],`+
			`"ingress":[{"from":[{"podSelector":{"matchLabels":{"app":"frontend"}}}],`+
			`"ports":[{"protocol":"TCP","port":8000,"endPort":8100}]}]}`)

	edges, err := resolveNetworkPolicyReachability(pods, nsLabels, nil, []*knowledgev1.Node{policy})
	require.NoError(t, err)
	require.Len(t, edges, 1)

	pm := decodePortMetadata(t, edges[0].Evidence)
	assert.Equal(t, "TCP", pm.Protocol)
	assert.Equal(t, 8000, pm.PortFrom)
	assert.Equal(t, 8100, pm.PortTo)
}

// TestResolveNetworkPolicyReachability_NamedPortResolved verifies that a
// policy referencing a named port "web" against a backend pod that
// declares containerPort 8080 name "web" emits an edge with
// port_from=port_to=8080.
//
// Satisfies criterion b296eccf0a68c1817f693a695f396797.
func TestResolveNetworkPolicyReachability_NamedPortResolved(t *testing.T) {
	pods := []podEntry{
		podEntryWithLabels("default/Pod/backend", "default", map[string]string{"app": "backend"}),
		podEntryWithLabels("default/Pod/frontend", "default", map[string]string{"app": "frontend"}),
	}
	nsLabels := map[string]map[string]string{"default": {}}

	// Backend declares containerPort 8080 name "web".
	podPortIndex := map[string][]podContainerPort{
		"default/Pod/backend": {
			{name: "web", port: 8080, protocol: "TCP"},
		},
	}

	// Policy targets backend, allows ingress from frontend on named port "web".
	policy := buildPolicyNode(t, "default/NetworkPolicy/named", "default",
		`{"podSelector":{"matchLabels":{"app":"backend"}},"policyTypes":["Ingress"],`+
			`"ingress":[{"from":[{"podSelector":{"matchLabels":{"app":"frontend"}}}],`+
			`"ports":[{"protocol":"TCP","port":"web"}]}]}`)

	edges, err := resolveNetworkPolicyReachability(pods, nsLabels, podPortIndex, []*knowledgev1.Node{policy})
	require.NoError(t, err)
	require.Len(t, edges, 1, "expected one edge: backend → frontend on resolved named port")

	e := findEdgeByFromTo(edges, "default/Pod/backend", "default/Pod/frontend")
	require.NotNil(t, e, "expected backend → frontend edge")

	pm := decodePortMetadata(t, e.Evidence)
	assert.Equal(t, "TCP", pm.Protocol)
	assert.Equal(t, 8080, pm.PortFrom, "named port web must resolve to 8080")
	assert.Equal(t, 8080, pm.PortTo)
	assert.Empty(t, pm.NamedPort, "resolved named ports must not leak the original name")
	assert.False(t, pm.PortUnresolved, "resolved named ports must not flag unresolved")
}

// TestResolveNetworkPolicyReachability_NamedPortUnresolved verifies that a
// policy referencing a named port not declared by any target pod emits an
// edge with port_unresolved=true and preserves the named port for
// diagnostics.
//
// Satisfies criterion 4c4294d0e50ced01ac32bc298847729a.
func TestResolveNetworkPolicyReachability_NamedPortUnresolved(t *testing.T) {
	pods := []podEntry{
		podEntryWithLabels("default/Pod/backend", "default", map[string]string{"app": "backend"}),
		podEntryWithLabels("default/Pod/frontend", "default", map[string]string{"app": "frontend"}),
	}
	nsLabels := map[string]map[string]string{"default": {}}

	// Backend does NOT declare any named "web" port — index is empty.
	podPortIndex := map[string][]podContainerPort{}

	policy := buildPolicyNode(t, "default/NetworkPolicy/bad-named", "default",
		`{"podSelector":{"matchLabels":{"app":"backend"}},"policyTypes":["Ingress"],`+
			`"ingress":[{"from":[{"podSelector":{"matchLabels":{"app":"frontend"}}}],`+
			`"ports":[{"protocol":"TCP","port":"web"}]}]}`)

	edges, err := resolveNetworkPolicyReachability(pods, nsLabels, podPortIndex, []*knowledgev1.Node{policy})
	require.NoError(t, err)
	require.Len(t, edges, 1, "expected one edge even when named port is unresolved")

	pm := decodePortMetadata(t, edges[0].Evidence)
	assert.Equal(t, "TCP", pm.Protocol)
	assert.Equal(t, 0, pm.PortFrom, "unresolved named ports must not carry a numeric port")
	assert.Equal(t, 0, pm.PortTo)
	assert.Equal(t, "web", pm.NamedPort, "named_port must be preserved for diagnostics")
	assert.True(t, pm.PortUnresolved, "port_unresolved must be true when the named port cannot be resolved")
}

// TestResolveNetworkPolicyReachability_NamedPortPerPodDifferentNumbers
// verifies that when the policy targets multiple pods that map the same
// named port to DIFFERENT numeric ports, separate edges are emitted per
// target pod with their respective resolved numbers.
func TestResolveNetworkPolicyReachability_NamedPortPerPodDifferentNumbers(t *testing.T) {
	pods := []podEntry{
		podEntryWithLabels("default/Pod/backend-a", "default", map[string]string{"app": "backend"}),
		podEntryWithLabels("default/Pod/backend-b", "default", map[string]string{"app": "backend"}),
		podEntryWithLabels("default/Pod/frontend", "default", map[string]string{"app": "frontend"}),
	}
	nsLabels := map[string]map[string]string{"default": {}}

	// Two backend pods bind the same name "web" to different numbers.
	podPortIndex := map[string][]podContainerPort{
		"default/Pod/backend-a": {{name: "web", port: 8080, protocol: "TCP"}},
		"default/Pod/backend-b": {{name: "web", port: 9090, protocol: "TCP"}},
	}

	policy := buildPolicyNode(t, "default/NetworkPolicy/named-split", "default",
		`{"podSelector":{"matchLabels":{"app":"backend"}},"policyTypes":["Ingress"],`+
			`"ingress":[{"from":[{"podSelector":{"matchLabels":{"app":"frontend"}}}],`+
			`"ports":[{"protocol":"TCP","port":"web"}]}]}`)

	edges, err := resolveNetworkPolicyReachability(pods, nsLabels, podPortIndex, []*knowledgev1.Node{policy})
	require.NoError(t, err)
	require.Len(t, edges, 2, "expected one edge per target pod for per-pod-different named ports")

	ea := findEdgeByFromTo(edges, "default/Pod/backend-a", "default/Pod/frontend")
	require.NotNil(t, ea)
	pma := decodePortMetadata(t, ea.Evidence)
	assert.Equal(t, 8080, pma.PortFrom)
	assert.Equal(t, 8080, pma.PortTo)

	eb := findEdgeByFromTo(edges, "default/Pod/backend-b", "default/Pod/frontend")
	require.NotNil(t, eb)
	pmb := decodePortMetadata(t, eb.Evidence)
	assert.Equal(t, 9090, pmb.PortFrom)
	assert.Equal(t, 9090, pmb.PortTo)
}

// TestResolveNetworkPolicyReachability_MultiplePortsPerRule verifies that
// a rule with multiple ports[] entries emits one edge per port entry
// per (target, peer) pair.
func TestResolveNetworkPolicyReachability_MultiplePortsPerRule(t *testing.T) {
	pods := []podEntry{
		podEntryWithLabels("default/Pod/backend", "default", map[string]string{"app": "backend"}),
		podEntryWithLabels("default/Pod/frontend", "default", map[string]string{"app": "frontend"}),
	}
	nsLabels := map[string]map[string]string{"default": {}}

	policy := buildPolicyNode(t, "default/NetworkPolicy/multi-port", "default",
		`{"podSelector":{"matchLabels":{"app":"backend"}},"policyTypes":["Ingress"],`+
			`"ingress":[{"from":[{"podSelector":{"matchLabels":{"app":"frontend"}}}],`+
			`"ports":[{"protocol":"TCP","port":80},{"protocol":"TCP","port":443}]}]}`)

	edges, err := resolveNetworkPolicyReachability(pods, nsLabels, nil, []*knowledgev1.Node{policy})
	require.NoError(t, err)
	require.Len(t, edges, 2, "expected one edge per port entry")

	var ports []int
	for i := range edges {
		pm := decodePortMetadata(t, edges[i].Evidence)
		ports = append(ports, pm.PortFrom)
	}
	assert.ElementsMatch(t, []int{80, 443}, ports)
}

// TestParsePodContainerPorts_SkipsUnnamedAndZero verifies parsing logic
// discards container ports without a name (cannot be referenced) and
// entries with zero containerPort (malformed).
func TestParsePodContainerPorts_SkipsUnnamedAndZero(t *testing.T) {
	raw := []byte(`{
		"spec": {
			"containers": [
				{"ports": [
					{"name": "web", "containerPort": 8080, "protocol": "TCP"},
					{"containerPort": 9000},
					{"name": "metrics", "containerPort": 0},
					{"name": "udp-dns", "containerPort": 53, "protocol": "UDP"}
				]}
			]
		}
	}`)

	ports, err := parsePodContainerPorts(raw)
	require.NoError(t, err)
	require.Len(t, ports, 2, "must skip unnamed and zero-port entries")
	assert.Equal(t, "web", ports[0].name)
	assert.Equal(t, 8080, ports[0].port)
	assert.Equal(t, "TCP", ports[0].protocol)
	assert.Equal(t, "udp-dns", ports[1].name)
	assert.Equal(t, 53, ports[1].port)
	assert.Equal(t, "UDP", ports[1].protocol)
}

// TestResolveNamedPort_ProtocolFallback verifies that an empty rule
// protocol matches any container port protocol, and an explicit protocol
// only matches when the container port matches (or is unspecified,
// treated as TCP).
func TestResolveNamedPort_ProtocolFallback(t *testing.T) {
	idx := map[string][]podContainerPort{
		"pod1": {
			{name: "web", port: 8080, protocol: "TCP"},
			{name: "udp-svc", port: 5353, protocol: "UDP"},
			{name: "default-tcp", port: 9999, protocol: ""}, // implicit TCP
		},
	}

	// Empty rule protocol matches any.
	p, ok := resolveNamedPort(idx, "pod1", "udp-svc", "")
	assert.True(t, ok)
	assert.Equal(t, 5353, p)

	// Explicit TCP matches a TCP port.
	p, ok = resolveNamedPort(idx, "pod1", "web", "TCP")
	assert.True(t, ok)
	assert.Equal(t, 8080, p)

	// Explicit UDP must NOT match a TCP port.
	_, ok = resolveNamedPort(idx, "pod1", "web", "UDP")
	assert.False(t, ok, "explicit UDP must not match a TCP container port")

	// Container port without explicit protocol is treated as TCP.
	p, ok = resolveNamedPort(idx, "pod1", "default-tcp", "TCP")
	assert.True(t, ok)
	assert.Equal(t, 9999, p)

	// Missing pod yields no match.
	_, ok = resolveNamedPort(idx, "missing", "web", "TCP")
	assert.False(t, ok)
}

// TestResolveNetworkPolicyReachability_EgressNamedPortResolvedAgainstDest
// verifies that egress rules resolve named ports against the DESTINATION
// pod (the service being connected to), not the source pod.
func TestResolveNetworkPolicyReachability_EgressNamedPortResolvedAgainstDest(t *testing.T) {
	pods := []podEntry{
		podEntryWithLabels("default/Pod/client", "default", map[string]string{"app": "client"}),
		podEntryWithLabels("default/Pod/server", "default", map[string]string{"app": "server"}),
	}
	nsLabels := map[string]map[string]string{"default": {}}

	// Only the SERVER declares named port "grpc".
	podPortIndex := map[string][]podContainerPort{
		"default/Pod/server": {{name: "grpc", port: 50051, protocol: "TCP"}},
	}

	policy := buildPolicyNode(t, "default/NetworkPolicy/egress-named", "default",
		`{"podSelector":{"matchLabels":{"app":"client"}},"policyTypes":["Egress"],`+
			`"egress":[{"to":[{"podSelector":{"matchLabels":{"app":"server"}}}],`+
			`"ports":[{"protocol":"TCP","port":"grpc"}]}]}`)

	edges, err := resolveNetworkPolicyReachability(pods, nsLabels, podPortIndex, []*knowledgev1.Node{policy})
	require.NoError(t, err)
	require.Len(t, edges, 1)

	e := &edges[0]
	assert.Equal(t, "default/Pod/client", e.FromId)
	assert.Equal(t, "default/Pod/server", e.ToId)
	assert.Equal(t, string(kgtypes.EdgeAllowsEgressTo), e.Type)
	pm := decodePortMetadata(t, e.Evidence)
	assert.Equal(t, 50051, pm.PortFrom, "named port must resolve against the destination pod for egress")
	assert.Equal(t, 50051, pm.PortTo)
	assert.False(t, pm.PortUnresolved)
}

// TestPortMetadata_EncodeIsOpen exercises the encode() contract: the
// zero value encodes to the empty string (the canonical fully-open
// signal), and a non-zero value encodes to a compact JSON object that
// round-trips through decodePortMetadata.
func TestPortMetadata_EncodeIsOpen(t *testing.T) {
	assert.Empty(t, portMetadata{}.encode(), "zero value must encode to empty string")

	pm := portMetadata{Protocol: "TCP", PortFrom: 80, PortTo: 80}
	enc := pm.encode()
	assert.NotEmpty(t, enc)
	round := decodePortMetadata(t, enc)
	assert.Equal(t, pm, round)
}
