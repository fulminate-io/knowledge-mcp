// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"testing"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	elbv2types "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

func TestListenerCertEdges_SingleCert(t *testing.T) {
	lbARN := "arn:aws:elasticloadbalancing:us-east-1:123456:loadbalancer/app/my-alb/abc"
	certARN := "arn:aws:acm:us-east-1:123456:certificate/xxx-yyy"

	listeners := []elbv2types.Listener{{
		Port:     awssdk.Int32(443),
		Protocol: elbv2types.ProtocolEnumHttps,
		Certificates: []elbv2types.Certificate{
			{CertificateArn: awssdk.String(certARN)},
		},
	}}

	edges := listenerCertEdges(lbARN, listeners)
	require.Len(t, edges, 1)
	assert.Equal(t, lbARN, edges[0].SourceID)
	assert.Equal(t, certARN, edges[0].TargetID)
	assert.Equal(t, kgtypes.EdgeUsesCert, edges[0].Relationship)
	assert.Equal(t, "443", edges[0].Metadata["listener_port"])
	assert.Equal(t, "HTTPS", edges[0].Metadata["protocol"])
}

func TestListenerCertEdges_MultipleCerts(t *testing.T) {
	lbARN := "arn:aws:elasticloadbalancing:us-east-1:123456:loadbalancer/app/my-alb/abc"
	cert1 := "arn:aws:acm:us-east-1:123456:certificate/aaa"
	cert2 := "arn:aws:acm:us-east-1:123456:certificate/bbb"

	listeners := []elbv2types.Listener{{
		Port:     awssdk.Int32(443),
		Protocol: elbv2types.ProtocolEnumHttps,
		Certificates: []elbv2types.Certificate{
			{CertificateArn: awssdk.String(cert1)},
			{CertificateArn: awssdk.String(cert2)},
		},
	}}

	edges := listenerCertEdges(lbARN, listeners)
	require.Len(t, edges, 2)
	assert.Equal(t, cert1, edges[0].TargetID)
	assert.Equal(t, cert2, edges[1].TargetID)
}

func TestListenerCertEdges_NoCerts(t *testing.T) {
	lbARN := "arn:aws:elasticloadbalancing:us-east-1:123456:loadbalancer/app/my-alb/abc"

	// HTTP listener with no certificates.
	listeners := []elbv2types.Listener{{
		Port:     awssdk.Int32(80),
		Protocol: elbv2types.ProtocolEnumHttp,
	}}

	edges := listenerCertEdges(lbARN, listeners)
	assert.Empty(t, edges)
}

func TestListenerCertEdges_DeduplicateAcrossListeners(t *testing.T) {
	lbARN := "arn:aws:elasticloadbalancing:us-east-1:123456:loadbalancer/app/my-alb/abc"
	certARN := "arn:aws:acm:us-east-1:123456:certificate/shared"

	// Same cert on two different listeners.
	listeners := []elbv2types.Listener{
		{
			Port:     awssdk.Int32(443),
			Protocol: elbv2types.ProtocolEnumHttps,
			Certificates: []elbv2types.Certificate{
				{CertificateArn: awssdk.String(certARN)},
			},
		},
		{
			Port:     awssdk.Int32(8443),
			Protocol: elbv2types.ProtocolEnumHttps,
			Certificates: []elbv2types.Certificate{
				{CertificateArn: awssdk.String(certARN)},
			},
		},
	}

	edges := listenerCertEdges(lbARN, listeners)
	require.Len(t, edges, 1, "duplicate cert should be deduplicated")
	assert.Equal(t, certARN, edges[0].TargetID)
	// Metadata comes from the first listener seen.
	assert.Equal(t, "443", edges[0].Metadata["listener_port"])
}
