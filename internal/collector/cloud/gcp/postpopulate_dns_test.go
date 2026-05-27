// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

func TestBuildIPIndex_GCE(t *testing.T) {
	nodes := []*knowledgev1.Node{
		gcpNode(t, "projects/p/zones/us-central1-a/instances/vm-1",
			"gcp:compute:instance",
			`{"networkInterfaces":[{"accessConfigs":[{"natIP":"1.2.3.4"}]}]}`),
		gcpNode(t, "projects/p/zones/us-central1-a/instances/vm-2",
			"gcp:compute:instance",
			`{"networkInterfaces":[{"accessConfigs":[{"natIP":"5.6.7.8"}]}]}`),
	}

	index := buildIPIndexFromNodes(nodes, "gce")
	require.Len(t, index, 2)
	assert.Equal(t, []string{"projects/p/zones/us-central1-a/instances/vm-1"}, index["1.2.3.4"])
	assert.Equal(t, []string{"projects/p/zones/us-central1-a/instances/vm-2"}, index["5.6.7.8"])
}

func TestBuildIPIndex_SQL(t *testing.T) {
	nodes := []*knowledgev1.Node{
		gcpNode(t, "projects/p/instances/sql-1",
			"gcp:sql:instance",
			`{"ipAddresses":[{"ipAddress":"10.0.0.1"}]}`),
	}

	index := buildIPIndexFromNodes(nodes, "sql")
	require.Len(t, index, 1)
	assert.Equal(t, []string{"projects/p/instances/sql-1"}, index["10.0.0.1"])
}

func TestBuildIPIndex_ForwardingRule(t *testing.T) {
	nodes := []*knowledgev1.Node{
		gcpNode(t, "projects/p/regions/us-central1/forwardingRules/fr-1",
			"gcp:compute:forwardingRule",
			`{"IPAddress":"34.120.0.1"}`),
	}

	index := buildIPIndexFromNodes(nodes, "fr")
	require.Len(t, index, 1)
	assert.Equal(t, []string{"projects/p/regions/us-central1/forwardingRules/fr-1"}, index["34.120.0.1"])
}

func TestResolveDNSRecordTargets_IPRewrite(t *testing.T) {
	// Simulate: DNS record ROUTES_TO raw IP, GCE instance has matching IP.
	ipIndex := map[string]string{
		"1.2.3.4": "projects/p/zones/us-central1-a/instances/vm-1",
	}

	recordID := "projects/p/managedZones/myzone/rrsets/app.example.com./A"
	toID := "1.2.3.4"

	// Not already a GCP resource path.
	assert.False(t, isGCPResourcePath(toID))

	// Matches in index.
	resolved := ipIndex[toID]
	assert.Equal(t, "projects/p/zones/us-central1-a/instances/vm-1", resolved)

	// Construct new edge.
	newEdge := knowledgev1.Edge{
		FromId:   recordID,
		ToId:     resolved,
		Type:     string(kgtypes.EdgeRoutesTo),
		Method:   "postpopulate:dns-resolve",
		Evidence: toID,
	}
	assert.Equal(t, recordID, newEdge.FromId)
	assert.Equal(t, "projects/p/zones/us-central1-a/instances/vm-1", newEdge.ToId)
}

func TestResolveDNSRecordTargets_AlreadyResolved(t *testing.T) {
	// ToID is already a resource path — no rewrite.
	toID := "projects/p/zones/us-central1-a/instances/vm-1"
	assert.True(t, isGCPResourcePath(toID))
}

func TestResolveDNSRecordTargets_NoMatch(t *testing.T) {
	ipIndex := map[string]string{
		"1.2.3.4": "projects/p/zones/us-central1-a/instances/vm-1",
	}

	// IP doesn't match any resource.
	resolved := ipIndex["9.9.9.9"]
	assert.Empty(t, resolved)
}

func TestParseGCEInstanceIPs(t *testing.T) {
	content := `{"networkInterfaces":[{"accessConfigs":[{"natIP":"1.2.3.4"},{"natIP":"5.6.7.8"}]}]}`
	ips := parseGCEInstanceIPs(content)
	require.Len(t, ips, 2)
	assert.Contains(t, ips, "1.2.3.4")
	assert.Contains(t, ips, "5.6.7.8")
}

func TestParseGCEInstanceIPs_Empty(t *testing.T) {
	assert.Nil(t, parseGCEInstanceIPs(""))
	assert.Nil(t, parseGCEInstanceIPs("{invalid"))
	assert.Empty(t, parseGCEInstanceIPs(`{"networkInterfaces":[]}`))
}

func TestParseSQLInstanceIPs(t *testing.T) {
	content := `{"ipAddresses":[{"ipAddress":"10.0.0.1"},{"ipAddress":"10.0.0.2"}]}`
	ips := parseSQLInstanceIPs(content)
	require.Len(t, ips, 2)
}

func TestParseForwardingRuleIP(t *testing.T) {
	assert.Equal(t, "34.120.0.1", parseForwardingRuleIP(`{"IPAddress":"34.120.0.1"}`))
	assert.Empty(t, parseForwardingRuleIP(`{"IPAddress":""}`))
	assert.Empty(t, parseForwardingRuleIP(""))
	assert.Empty(t, parseForwardingRuleIP("{bad"))
}

func TestDNSRecordEdgesFromContent_ARecord(t *testing.T) {
	const recordID = "projects/p/managedZones/z/rrsets/app.example.com./A"
	ipIndex := map[string][]string{
		"1.2.3.4": {"projects/p/zones/us-central1-a/instances/vm-1"},
		"5.6.7.8": {"projects/p/zones/us-central1-a/instances/vm-2"},
	}
	content := `{"name":"app.example.com.","type":"A","rrdatas":["1.2.3.4","5.6.7.8"]}`

	edges := dnsRecordEdgesFromContent(recordID, content, ipIndex)
	require.Len(t, edges, 2)
	assert.Equal(t, recordID, edges[0].FromId)
	assert.Equal(t, "projects/p/zones/us-central1-a/instances/vm-1", edges[0].ToId)
	assert.Equal(t, string(kgtypes.EdgeRoutesTo), edges[0].Type)
	assert.Equal(t, "1.2.3.4", edges[0].Evidence)
	assert.Equal(t, "postpopulate:dns-resolve", edges[0].Method)
	assert.Equal(t, "projects/p/zones/us-central1-a/instances/vm-2", edges[1].ToId)
}

func TestDNSRecordEdgesFromContent_SharedIPMultiTarget(t *testing.T) {
	// One IP, multiple owners (regional + global forwarding rule pair, or
	// active/passive failover) → emit one edge per resolved target.
	const recordID = "projects/p/managedZones/z/rrsets/app.example.com./A"
	ipIndex := map[string][]string{
		"34.120.0.1": {
			"projects/p/regions/us-central1/forwardingRules/regional-fr",
			"projects/p/global/forwardingRules/global-fr",
		},
	}
	content := `{"name":"app.example.com.","type":"A","rrdatas":["34.120.0.1"]}`

	edges := dnsRecordEdgesFromContent(recordID, content, ipIndex)
	require.Len(t, edges, 2, "shared IP must produce one edge per resolved target, no last-write-wins")
	targets := []string{edges[0].ToId, edges[1].ToId}
	assert.Contains(t, targets, "projects/p/regions/us-central1/forwardingRules/regional-fr")
	assert.Contains(t, targets, "projects/p/global/forwardingRules/global-fr")
}

func TestDNSRecordEdgesFromContent_UnresolvedIPProducesNoEdge(t *testing.T) {
	ipIndex := map[string][]string{}
	content := `{"name":"app.example.com.","type":"A","rrdatas":["9.9.9.9"]}`
	edges := dnsRecordEdgesFromContent("rec-id", content, ipIndex)
	assert.Empty(t, edges, "unresolved rdata must NOT produce a dangling edge")
}

func TestDNSRecordEdgesFromContent_CNAMEProducesNoEdge(t *testing.T) {
	// CNAMEs would need a hostname index; today they produce no edge.
	ipIndex := map[string][]string{}
	content := `{"name":"alias.example.com.","type":"CNAME","rrdatas":["target.example.com."]}`
	edges := dnsRecordEdgesFromContent("rec-id", content, ipIndex)
	assert.Empty(t, edges)
}

func TestDNSRecordEdgesFromContent_AAAARecord(t *testing.T) {
	ipIndex := map[string][]string{
		"2001:db8::1": {"projects/p/zones/us-central1-a/instances/vm-v6"},
	}
	content := `{"name":"v6.example.com.","type":"AAAA","rrdatas":["2001:db8::1"]}`
	edges := dnsRecordEdgesFromContent("rec-id", content, ipIndex)
	require.Len(t, edges, 1)
	assert.Equal(t, "projects/p/zones/us-central1-a/instances/vm-v6", edges[0].ToId)
}

func TestDNSRecordEdgesFromContent_NonRoutingTypesIgnored(t *testing.T) {
	ipIndex := map[string][]string{"1.2.3.4": {"vm-1"}}
	for _, typ := range []string{"MX", "TXT", "NS", "SOA", "SRV", "PTR"} {
		content := `{"name":"x.example.com.","type":"` + typ + `","rrdatas":["1.2.3.4"]}`
		edges := dnsRecordEdgesFromContent("rec-id", content, ipIndex)
		assert.Empty(t, edges, "type %s must produce no edges", typ)
	}
}

func TestDNSRecordEdgesFromContent_BadContent(t *testing.T) {
	assert.Empty(t, dnsRecordEdgesFromContent("rec-id", "", nil))
	assert.Empty(t, dnsRecordEdgesFromContent("rec-id", "{not-json", nil))
}

// --- test helpers ---

func gcpNode(t *testing.T, id, resourceType, content string) *knowledgev1.Node {
	t.Helper()
	n := &knowledgev1.Node{
		Id:         id,
		Type:       string(kgtypes.NodeCloudResource),
		SymbolName: "test",
		Content:    content,
	}
	kgtypes.SetValue(n, "resource_type", resourceType)
	return n
}

// buildIPIndexFromNodes exercises the IP parsing logic against a pre-built
// node slice for each resource type, without needing a DB.
func buildIPIndexFromNodes(nodes []*knowledgev1.Node, kind string) map[string][]string {
	index := make(map[string][]string)
	for _, n := range nodes {
		switch kind {
		case "gce":
			for _, ip := range parseGCEInstanceIPs(n.Content) {
				addIPMapping(index, ip, n.Id)
			}
		case "sql":
			for _, ip := range parseSQLInstanceIPs(n.Content) {
				addIPMapping(index, ip, n.Id)
			}
		case "fr":
			if ip := parseForwardingRuleIP(n.Content); ip != "" {
				addIPMapping(index, ip, n.Id)
			}
		}
	}
	return index
}
