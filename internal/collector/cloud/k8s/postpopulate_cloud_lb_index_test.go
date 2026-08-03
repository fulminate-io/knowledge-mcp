// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// --- Pure helper unit tests (no store, no fixtures). -----------------------

func TestForwardingRuleIPFromNode(t *testing.T) {
	cases := []struct {
		name, ipMeta, content, want string
	}{
		{"metadata path", "203.0.113.5", "", "203.0.113.5"},
		{"content fallback", "", `{"IPAddress":"198.51.100.7"}`, "198.51.100.7"},
		{"metadata wins over content", "203.0.113.5", `{"IPAddress":"198.51.100.7"}`, "203.0.113.5"},
		{"empty everything", "", "", ""},
		{"malformed json", "", "{not-json", ""},
		{"content missing field", "", `{"name":"fr"}`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			n := forwardingRuleNode("https://example/"+tc.name, tc.ipMeta, tc.content)
			assert.Equal(t, tc.want, forwardingRuleIPFromNode(n))
		})
	}
}

func TestELBDNSFromNode(t *testing.T) {
	cases := []struct {
		name, content, want string
	}{
		{"happy path", `{"DNSName":"my-lb-abc.us-east-1.elb.amazonaws.com"}`,
			"my-lb-abc.us-east-1.elb.amazonaws.com"},
		{"empty content", "", ""},
		{"malformed json", "{bad", ""},
		{"missing dns field", `{"LoadBalancerName":"my-lb"}`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			n := elbResNode("arn:"+tc.name, "elbv2-loadbalancer", tc.content)
			assert.Equal(t, tc.want, elbDNSFromNode(n))
		})
	}
}

func TestMergeForwardingRuleNodes_LowercasesAndDeduplicates(t *testing.T) {
	index := map[string][]cloudLBRef{}
	mergeForwardingRuleNodes(index, "proj-a", []*knowledgev1.Node{
		forwardingRuleNode("fr-1", "203.0.113.5", ""),
		forwardingRuleNode("fr-2", "198.51.100.7", ""),
		// Empty IP — must be skipped, not stored as "".
		forwardingRuleNode("fr-3", "", ""),
		// Mixed-case IPs are unusual but the helper must lowercase keys
		// so the consuming resolver can do a case-insensitive lookup.
		forwardingRuleNode("fr-4", "2001:DB8::1", ""),
	})
	require.Len(t, index, 3)
	assert.Equal(t, []cloudLBRef{{GraphName: "proj-a", NodeID: "fr-1"}}, index["203.0.113.5"])
	assert.Equal(t, []cloudLBRef{{GraphName: "proj-a", NodeID: "fr-2"}}, index["198.51.100.7"])
	assert.Equal(t, []cloudLBRef{{GraphName: "proj-a", NodeID: "fr-4"}}, index["2001:db8::1"])
	_, hasEmpty := index[""]
	assert.False(t, hasEmpty, "empty IP must not produce an index entry")
}

// TestMergeForwardingRuleNodes_MultiRulesPerIP locks in the GCP Gateway
// fix: a single VIP normally has 2+ forwardingRules (one per protocol —
// HTTP:80, HTTPS:443 for GKE Gateway). Every rule must land in the slice
// so each gets its own EXPOSED_BY proxy at resolution time, not just the
// last one iterated.
func TestMergeForwardingRuleNodes_MultiRulesPerIP(t *testing.T) {
	index := map[string][]cloudLBRef{}
	mergeForwardingRuleNodes(index, "proj-a", []*knowledgev1.Node{
		forwardingRuleNode("fr-https", "203.0.113.5", ""),
		forwardingRuleNode("fr-http", "203.0.113.5", ""),
	})
	require.Len(t, index, 1, "single IP key")
	refs := index["203.0.113.5"]
	require.Len(t, refs, 2, "both forwardingRules must be recorded")
	assert.Contains(t, refs, cloudLBRef{GraphName: "proj-a", NodeID: "fr-https"})
	assert.Contains(t, refs, cloudLBRef{GraphName: "proj-a", NodeID: "fr-http"})
}

func TestMergeELBDNSNodes_LowercasesAndSkipsMissingDNS(t *testing.T) {
	index := map[string][]cloudLBRef{}
	// The helper trusts its input — the store's Q.Meta predicate now
	// filters by resource_type at the executor level, so the merge fn
	// only needs to skip rows with no DNSName and lowercase the key.
	nodes := []*knowledgev1.Node{
		elbResNode("arn:lb-1", "elbv2-loadbalancer",
			`{"DNSName":"my-LB.us-east-1.elb.amazonaws.com"}`),
		// Missing DNS — must be skipped.
		elbResNode("arn:lb-3", "elbv2-loadbalancer", `{"LoadBalancerName":"x"}`),
	}
	mergeELBDNSNodes(index, "aws-prod", "elbv2-loadbalancer", nodes)

	require.Len(t, index, 1)
	assert.Equal(t, []cloudLBRef{{GraphName: "aws-prod", NodeID: "arn:lb-1"}},
		index["my-lb.us-east-1.elb.amazonaws.com"])
}

// Note: the helpers used to defensively re-filter by resource_type because
// the metadata predicates were silently unenforced. That bug is fixed at the
// server's query-executor metadata predicate, so the merge helpers now trust
// their input. The integration tests below cover end-to-end correctness via a
// real store + Q.Meta query.

// --- Integration tests against a real store with seeded cloud graphs. ------

func TestBuildGCPForwardingRuleIndex_Integration(t *testing.T) {
	ctx := newCtx(t)

	fake := newK8sFake()
	fake.seed("proj-a",
		forwardingRuleNode("https://compute/projects/proj-a/global/forwardingRules/fr-1",
			"203.0.113.5", ""),
		forwardingRuleNode("https://compute/projects/proj-a/global/forwardingRules/fr-2",
			"", `{"IPAddress":"198.51.100.7"}`),
	)
	fake.seed("proj-b",
		forwardingRuleNode("https://compute/projects/proj-b/global/forwardingRules/fr-3",
			"192.0.2.10", ""),
		// Non-forwardingRule node must NOT appear in the index.
		clusterResNode("default", "Service", "web"),
	)

	// The index builder iterates the store-wide cloud-graph list via the
	// wire ListGraphNames; the fake serves every seeded account graph.
	index, err := buildGCPForwardingRuleIndex(ctx, fake)
	require.NoError(t, err)

	require.Len(t, index, 3, "every forwardingRule across every cloud graph must appear")
	assert.Equal(t, []cloudLBRef{{
		GraphName: "proj-a",
		NodeID:    "https://compute/projects/proj-a/global/forwardingRules/fr-1",
	}}, index["203.0.113.5"])
	assert.Equal(t, []cloudLBRef{{
		GraphName: "proj-a",
		NodeID:    "https://compute/projects/proj-a/global/forwardingRules/fr-2",
	}}, index["198.51.100.7"])
	assert.Equal(t, []cloudLBRef{{
		GraphName: "proj-b",
		NodeID:    "https://compute/projects/proj-b/global/forwardingRules/fr-3",
	}}, index["192.0.2.10"])
}

func TestBuildAWSELBDNSIndex_Integration(t *testing.T) {
	ctx := newCtx(t)

	fake := newK8sFake()
	fake.seed("aws-prod",
		elbResNode("arn:aws:elasticloadbalancing:us-east-1:111:loadbalancer/app/web/abc",
			"elbv2-loadbalancer",
			`{"DNSName":"web-abc.us-east-1.elb.amazonaws.com"}`),
		// elbv2-targetgroup is in the same graph but must not match.
		&knowledgev1.Node{
			Id:         "arn:aws:elasticloadbalancing:us-east-1:111:targetgroup/web/def",
			Type:       string(kgtypes.NodeCloudResource),
			SymbolName: "web-tg",
			Content:    `{"DNSName":"ignored"}`,
			Metadata:   map[string]string{"resource_type": "elbv2-targetgroup"},
		},
	)
	fake.seed("aws-staging",
		elbResNode("arn:aws:elasticloadbalancing:us-west-2:222:loadbalancer/net/api/xyz",
			"elbv2-loadbalancer",
			`{"DNSName":"API-xyz.us-west-2.elb.amazonaws.com"}`),
		// Future classic-ELB row exercising the second resource_type.
		elbResNode("arn:aws:elasticloadbalancing:us-west-2:222:loadbalancer/legacy",
			"elb-loadbalancer",
			`{"DNSName":"legacy.us-west-2.elb.amazonaws.com"}`),
	)

	index, err := buildAWSELBDNSIndex(ctx, fake)
	require.NoError(t, err)

	require.Len(t, index, 3,
		"index must include every elbv2 + classic ELB DNS name across cloud graphs and skip target groups")
	assert.Equal(t, []cloudLBRef{{
		GraphName: "aws-prod",
		NodeID:    "arn:aws:elasticloadbalancing:us-east-1:111:loadbalancer/app/web/abc",
	}}, index["web-abc.us-east-1.elb.amazonaws.com"])
	assert.Equal(t, []cloudLBRef{{
		GraphName: "aws-staging",
		NodeID:    "arn:aws:elasticloadbalancing:us-west-2:222:loadbalancer/net/api/xyz",
	}}, index["api-xyz.us-west-2.elb.amazonaws.com"], "DNS keys must be lowercased")
	assert.Equal(t, []cloudLBRef{{
		GraphName: "aws-staging",
		NodeID:    "arn:aws:elasticloadbalancing:us-west-2:222:loadbalancer/legacy",
	}}, index["legacy.us-west-2.elb.amazonaws.com"])
}

func TestBuildIndexes_NoCloudGraphsLoaded(t *testing.T) {
	ctx := newCtx(t)

	// Don't seed any cloud graphs. Both helpers must return an empty,
	// non-nil map without erroring — the resolver callers rely on this
	// "silent no-op when nothing is loaded" contract for non-GKE / no-cloud
	// kubeconfig flows.
	fake := newK8sFake()
	gcpIndex, err := buildGCPForwardingRuleIndex(ctx, fake)
	require.NoError(t, err)
	assert.NotNil(t, gcpIndex)
	assert.Empty(t, gcpIndex)

	awsIndex, err := buildAWSELBDNSIndex(ctx, fake)
	require.NoError(t, err)
	assert.NotNil(t, awsIndex)
	assert.Empty(t, awsIndex)
}

// --- test helpers ----------------------------------------------------------

// forwardingRuleNode constructs a NodeCloudResource shaped the way the GCP
// loadbalancer subcollector emits forwardingRule rows. ipMeta and content
// are independently optional so tests can exercise the metadata-first /
// content-fallback / both-present / neither paths.
func forwardingRuleNode(id, ipMeta, content string) *knowledgev1.Node {
	n := &knowledgev1.Node{
		Id:         id,
		Type:       string(kgtypes.NodeCloudResource),
		SymbolName: lastSlashSegment(id),
		Source:     "cloud",
		Content:    content,
	}
	kgtypes.SetValue(n, "resource_type", gcpForwardingRuleResourceType)
	if ipMeta != "" {
		kgtypes.SetValue(n, "ipAddress", ipMeta)
	}
	return n
}

// elbResNode constructs a NodeCloudResource for an AWS ELB row. resourceType
// is parameterized so tests can cover both elbv2-loadbalancer and the
// future-facing elb-loadbalancer (classic).
func elbResNode(id, resourceType, content string) *knowledgev1.Node {
	n := &knowledgev1.Node{
		Id:         id,
		Type:       string(kgtypes.NodeCloudResource),
		SymbolName: lastSlashSegment(id),
		Source:     "cloud",
		Content:    content,
	}
	kgtypes.SetValue(n, "resource_type", resourceType)
	return n
}

// lastSlashSegment returns the substring after the final '/'. Used only as a
// SymbolName placeholder for fixture nodes — not load-bearing.
func lastSlashSegment(s string) string {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '/' {
			return s[i+1:]
		}
	}
	return s
}
