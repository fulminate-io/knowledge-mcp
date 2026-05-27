// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"testing"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	cftypes "github.com/aws/aws-sdk-go-v2/service/cloudfront/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

func TestCloudfrontCollector_Name(t *testing.T) {
	c := &cloudfrontCollector{}
	assert.Equal(t, "cloudfront", c.Name())
}

func TestResolveOriginTarget(t *testing.T) {
	tests := []struct {
		name   string
		domain string
		want   string
	}{
		{
			name:   "S3 bucket origin",
			domain: "mybucket.s3.amazonaws.com",
			want:   "arn:aws:s3:::mybucket",
		},
		{
			name:   "S3 bucket regional origin",
			domain: "mybucket.s3.us-east-1.amazonaws.com",
			want:   "arn:aws:s3:::mybucket",
		},
		{
			name:   "ALB origin",
			domain: "my-alb-123.us-east-1.elb.amazonaws.com",
			want:   "my-alb-123.us-east-1.elb.amazonaws.com",
		},
		{
			name:   "API Gateway origin",
			domain: "abc123.execute-api.us-east-1.amazonaws.com",
			want:   "abc123.execute-api.us-east-1.amazonaws.com",
		},
		{
			name:   "unknown origin",
			domain: "example.com",
			want:   "",
		},
		{
			name:   "empty domain",
			domain: "",
			want:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveOriginTarget(tt.domain)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestDistributionEdges_UsesCert(t *testing.T) {
	distARN := "arn:aws:cloudfront::123456:distribution/EABC123"
	certARN := "arn:aws:acm:us-east-1:123456:certificate/xxx"

	dist := cftypes.DistributionSummary{
		ARN: awssdk.String(distARN),
		ViewerCertificate: &cftypes.ViewerCertificate{
			ACMCertificateArn: awssdk.String(certARN),
		},
		Origins: &cftypes.Origins{
			Items: []cftypes.Origin{
				{DomainName: awssdk.String("mybucket.s3.amazonaws.com")},
			},
		},
	}

	edges := distributionEdges(distARN, dist)
	require.Len(t, edges, 2) // USES_CERT + ROUTES_TO

	assert.Equal(t, kgtypes.EdgeUsesCert, edges[0].Relationship)
	assert.Equal(t, distARN, edges[0].SourceID)
	assert.Equal(t, certARN, edges[0].TargetID)

	assert.Equal(t, kgtypes.EdgeRoutesTo, edges[1].Relationship)
}

func TestDistributionEdges_NoCert(t *testing.T) {
	distARN := "arn:aws:cloudfront::123456:distribution/EABC123"

	dist := cftypes.DistributionSummary{
		ARN: awssdk.String(distARN),
		// No ViewerCertificate or CloudFront default cert.
		Origins: &cftypes.Origins{
			Items: []cftypes.Origin{
				{DomainName: awssdk.String("mybucket.s3.amazonaws.com")},
			},
		},
	}

	edges := distributionEdges(distARN, dist)
	require.Len(t, edges, 1)
	assert.Equal(t, kgtypes.EdgeRoutesTo, edges[0].Relationship)
}

func TestDistributionEdges_WebACL(t *testing.T) {
	distARN := "arn:aws:cloudfront::123456:distribution/EABC123"
	webACLARN := "arn:aws:wafv2:us-east-1:123456:global/webacl/my-acl/abc"

	dist := cftypes.DistributionSummary{
		ARN:      awssdk.String(distARN),
		WebACLId: awssdk.String(webACLARN),
	}

	edges := distributionEdges(distARN, dist)

	var found bool
	for _, e := range edges {
		if e.Relationship == kgtypes.EdgeProtects {
			assert.Equal(t, webACLARN, e.SourceID)
			assert.Equal(t, distARN, e.TargetID)
			found = true
		}
	}
	assert.True(t, found, "expected EdgeProtects edge from WebACL to distribution")
}

func TestDistributionEdges_CertOnly_NoOrigins(t *testing.T) {
	distARN := "arn:aws:cloudfront::123456:distribution/EABC123"
	certARN := "arn:aws:acm:us-east-1:123456:certificate/yyy"

	dist := cftypes.DistributionSummary{
		ARN: awssdk.String(distARN),
		ViewerCertificate: &cftypes.ViewerCertificate{
			ACMCertificateArn: awssdk.String(certARN),
		},
		// No origins.
	}

	edges := distributionEdges(distARN, dist)
	require.Len(t, edges, 1)
	assert.Equal(t, kgtypes.EdgeUsesCert, edges[0].Relationship)
	assert.Equal(t, certARN, edges[0].TargetID)
}
