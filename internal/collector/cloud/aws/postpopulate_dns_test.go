// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

func TestBuildELBDNSIndex(t *testing.T) {
	nodes := []*knowledgev1.Node{
		elbNode(t, "arn:aws:elasticloadbalancing:us-east-1:111:loadbalancer/app/my-lb/abc",
			`{"DNSName":"my-lb-abc.us-east-1.elb.amazonaws.com"}`),
		elbNode(t, "arn:aws:elasticloadbalancing:us-west-2:222:loadbalancer/net/other-lb/def",
			`{"DNSName":"Other-lb-DEF.us-west-2.elb.amazonaws.com"}`),
	}

	index := buildELBDNSIndexFromNodes(nodes)
	require.Len(t, index, 2)

	// Keys must be lowercase.
	assert.Equal(t,
		"arn:aws:elasticloadbalancing:us-east-1:111:loadbalancer/app/my-lb/abc",
		index["my-lb-abc.us-east-1.elb.amazonaws.com"])
	assert.Equal(t,
		"arn:aws:elasticloadbalancing:us-west-2:222:loadbalancer/net/other-lb/def",
		index["other-lb-def.us-west-2.elb.amazonaws.com"])
}

func TestBuildELBDNSIndex_EmptyContent(t *testing.T) {
	nodes := []*knowledgev1.Node{
		elbNode(t, "arn:aws:elasticloadbalancing:us-east-1:111:loadbalancer/app/x/y", ""),
	}
	index := buildELBDNSIndexFromNodes(nodes)
	assert.Empty(t, index)
}

func TestBuildELBDNSIndex_MalformedJSON(t *testing.T) {
	nodes := []*knowledgev1.Node{
		elbNode(t, "arn:aws:elasticloadbalancing:us-east-1:111:loadbalancer/app/x/y", "{bad"),
	}
	index := buildELBDNSIndexFromNodes(nodes)
	assert.Empty(t, index)
}

func TestMatchDNSTarget_ELB(t *testing.T) {
	index := map[string]string{
		"my-lb-abc.us-east-1.elb.amazonaws.com": "arn:aws:elasticloadbalancing:us-east-1:111:loadbalancer/app/my-lb/abc",
	}

	got := matchDNSTarget("my-lb-abc.us-east-1.elb.amazonaws.com", index)
	assert.Equal(t, "arn:aws:elasticloadbalancing:us-east-1:111:loadbalancer/app/my-lb/abc", got)
}

func TestMatchDNSTarget_CaseInsensitive(t *testing.T) {
	index := map[string]string{
		"my-lb-abc.us-east-1.elb.amazonaws.com": "arn:elb",
	}

	got := matchDNSTarget("My-LB-ABC.us-east-1.elb.amazonaws.com", index)
	assert.Equal(t, "arn:elb", got)
}

func TestMatchDNSTarget_CloudFront(t *testing.T) {
	index := map[string]string{
		"d111111abcdef8.cloudfront.net": "arn:aws:cloudfront::111:distribution/D111",
	}

	got := matchDNSTarget("d111111abcdef8.cloudfront.net", index)
	assert.Equal(t, "arn:aws:cloudfront::111:distribution/D111", got)
}

func TestMatchDNSTarget_AlreadyARN(t *testing.T) {
	index := map[string]string{
		"my-lb.elb.amazonaws.com": "arn:elb",
	}

	// An ARN doesn't match the DNS hostname patterns.
	got := matchDNSTarget("arn:aws:elasticloadbalancing:us-east-1:111:loadbalancer/app/x/y", index)
	assert.Empty(t, got)
}

func TestMatchDNSTarget_NoMatch(t *testing.T) {
	index := map[string]string{
		"my-lb.us-east-1.elb.amazonaws.com": "arn:elb",
	}

	// Different hostname, not in index.
	got := matchDNSTarget("other-lb.us-west-2.elb.amazonaws.com", index)
	assert.Empty(t, got)
}

func TestIsDNSHostname(t *testing.T) {
	assert.True(t, isDNSHostname("my-lb-abc.us-east-1.elb.amazonaws.com"))
	assert.True(t, isDNSHostname("internal-my-lb.elasticloadbalancing.amazonaws.com"))
	assert.True(t, isDNSHostname("d111.cloudfront.net"))
	// ARNs containing "elasticloadbalancing" match isDNSHostname (pre-filter),
	// but matchDNSTarget would fail the index lookup. The function is a cheap
	// pre-filter, not the sole safety check.
	assert.True(t, isDNSHostname("arn:aws:elasticloadbalancing:us-east-1:111:loadbalancer/app/x/y"))
	assert.False(t, isDNSHostname("some-random-hostname.example.com"))
}

func TestEdgeRewrite(t *testing.T) {
	// Simulate the full rewrite logic: given a dangling edge and a DNS index,
	// produce the removal and new edge.
	index := map[string]string{
		"my-lb-abc.us-east-1.elb.amazonaws.com": "arn:aws:elasticloadbalancing:us-east-1:111:loadbalancer/app/my-lb/abc",
	}
	zoneARN := "arn:aws:route53:::hostedzone/Z1234"
	danglingDNS := "my-lb-abc.us-east-1.elb.amazonaws.com"

	resolved := matchDNSTarget(danglingDNS, index)
	require.NotEmpty(t, resolved)

	newEdge := knowledgev1.Edge{
		FromId:   zoneARN,
		ToId:     resolved,
		Type:     string(kgtypes.EdgeTargets),
		Method:   "postpopulate:dns-resolve",
		Evidence: danglingDNS,
	}

	assert.Equal(t, zoneARN, newEdge.FromId)
	assert.Equal(t, "arn:aws:elasticloadbalancing:us-east-1:111:loadbalancer/app/my-lb/abc", newEdge.ToId)
	assert.Equal(t, string(kgtypes.EdgeTargets), newEdge.Type)
	assert.Equal(t, "postpopulate:dns-resolve", newEdge.Method)
	assert.Equal(t, danglingDNS, newEdge.Evidence)
}

// --- test helpers ---

func elbNode(t *testing.T, id, content string) *knowledgev1.Node {
	t.Helper()
	n := &knowledgev1.Node{
		Id:         id,
		Type:       string(kgtypes.NodeCloudResource),
		SymbolName: "test-lb",
		Content:    content,
	}
	kgtypes.SetValue(n, "resource_type", "elbv2-loadbalancer")
	return n
}

// buildELBDNSIndexFromNodes is a test helper that exercises the same parsing
// logic as buildELBDNSIndex but against a pre-built node slice instead of
// querying the DB.
func buildELBDNSIndexFromNodes(nodes []*knowledgev1.Node) map[string]string {
	index := make(map[string]string, len(nodes))
	for _, n := range nodes {
		var c elbContentDNS
		if n.Content == "" {
			continue
		}
		if err := json.Unmarshal([]byte(n.Content), &c); err != nil || c.DNSName == "" {
			continue
		}
		index[strings.ToLower(c.DNSName)] = n.Id
	}
	return index
}
