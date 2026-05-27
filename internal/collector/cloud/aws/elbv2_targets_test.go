// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"context"
	"testing"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	elbv2sdk "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbv2types "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockTargetHealthAPI implements elbv2TargetHealthAPI for testing.
type mockTargetHealthAPI struct {
	output *elbv2sdk.DescribeTargetHealthOutput
	err    error
}

func (m *mockTargetHealthAPI) DescribeTargetHealth(_ context.Context, _ *elbv2sdk.DescribeTargetHealthInput, _ ...func(*elbv2sdk.Options)) (*elbv2sdk.DescribeTargetHealthOutput, error) {
	return m.output, m.err
}

func TestCollectTargetHealth_InstanceTargets(t *testing.T) {
	api := &mockTargetHealthAPI{
		output: &elbv2sdk.DescribeTargetHealthOutput{
			TargetHealthDescriptions: []elbv2types.TargetHealthDescription{
				{
					Target: &elbv2types.TargetDescription{
						Id:   awssdk.String("i-0123456789abcdef0"),
						Port: awssdk.Int32(8080),
					},
					TargetHealth: &elbv2types.TargetHealth{
						State: elbv2types.TargetHealthStateEnumHealthy,
					},
				},
				{
					Target: &elbv2types.TargetDescription{
						Id:   awssdk.String("i-abcdef0123456789a"),
						Port: awssdk.Int32(8080),
					},
					TargetHealth: &elbv2types.TargetHealth{
						State: elbv2types.TargetHealthStateEnumUnhealthy,
					},
				},
			},
		},
	}

	tgARN := "arn:aws:elasticloadbalancing:us-east-1:111:targetgroup/my-tg/abc"
	edges, err := collectTargetHealth(context.Background(), api, tgARN,
		elbv2types.TargetTypeEnumInstance, "us-east-1", "111")
	require.NoError(t, err)
	require.Len(t, edges, 2)

	assert.Equal(t, tgARN, edges[0].SourceID)
	assert.Equal(t, ec2ARN("us-east-1", "111", "instance", "i-0123456789abcdef0"), edges[0].TargetID)
	assert.Equal(t, kgtypes.EdgeTargets, edges[0].Relationship)
	assert.Equal(t, "8080", edges[0].Metadata["port"])
	assert.Equal(t, "healthy", edges[0].Metadata["health_status"])
	assert.Equal(t, "instance", edges[0].Metadata["target_type"])

	assert.Equal(t, ec2ARN("us-east-1", "111", "instance", "i-abcdef0123456789a"), edges[1].TargetID)
	assert.Equal(t, "unhealthy", edges[1].Metadata["health_status"])
}

func TestCollectTargetHealth_LambdaTargets(t *testing.T) {
	lambdaARN := "arn:aws:lambda:us-east-1:111:function:my-func"
	api := &mockTargetHealthAPI{
		output: &elbv2sdk.DescribeTargetHealthOutput{
			TargetHealthDescriptions: []elbv2types.TargetHealthDescription{
				{
					Target: &elbv2types.TargetDescription{
						Id: awssdk.String(lambdaARN),
					},
					TargetHealth: &elbv2types.TargetHealth{
						State: elbv2types.TargetHealthStateEnumHealthy,
					},
				},
			},
		},
	}

	tgARN := "arn:aws:elasticloadbalancing:us-east-1:111:targetgroup/lambda-tg/xyz"
	edges, err := collectTargetHealth(context.Background(), api, tgARN,
		elbv2types.TargetTypeEnumLambda, "us-east-1", "111")
	require.NoError(t, err)
	require.Len(t, edges, 1)

	// Lambda targets are already ARNs — no transformation.
	assert.Equal(t, lambdaARN, edges[0].TargetID)
	assert.Equal(t, "lambda", edges[0].Metadata["target_type"])
}

func TestCollectTargetHealth_IPTargets(t *testing.T) {
	api := &mockTargetHealthAPI{
		output: &elbv2sdk.DescribeTargetHealthOutput{
			TargetHealthDescriptions: []elbv2types.TargetHealthDescription{
				{
					Target: &elbv2types.TargetDescription{
						Id:   awssdk.String("10.0.1.50"),
						Port: awssdk.Int32(443),
					},
					TargetHealth: &elbv2types.TargetHealth{
						State: elbv2types.TargetHealthStateEnumHealthy,
					},
				},
			},
		},
	}

	tgARN := "arn:aws:elasticloadbalancing:us-east-1:111:targetgroup/ip-tg/def"
	edges, err := collectTargetHealth(context.Background(), api, tgARN,
		elbv2types.TargetTypeEnumIp, "us-east-1", "111")
	require.NoError(t, err)
	require.Len(t, edges, 1)

	// IP targets are stored as raw IPs.
	assert.Equal(t, "10.0.1.50", edges[0].TargetID)
	assert.Equal(t, "443", edges[0].Metadata["port"])
	assert.Equal(t, "ip", edges[0].Metadata["target_type"])
}

func TestCollectTargetHealth_EmptyResult(t *testing.T) {
	api := &mockTargetHealthAPI{
		output: &elbv2sdk.DescribeTargetHealthOutput{},
	}

	tgARN := "arn:aws:elasticloadbalancing:us-east-1:111:targetgroup/empty-tg/ghi"
	edges, err := collectTargetHealth(context.Background(), api, tgARN,
		elbv2types.TargetTypeEnumInstance, "us-east-1", "111")
	require.NoError(t, err)
	assert.Empty(t, edges)
}

func TestResolveTargetID(t *testing.T) {
	tests := []struct {
		name       string
		rawID      string
		targetType elbv2types.TargetTypeEnum
		want       string
	}{
		{"instance", "i-abc123", elbv2types.TargetTypeEnumInstance,
			ec2ARN("us-east-1", "111", "instance", "i-abc123")},
		{"lambda", "arn:aws:lambda:us-east-1:111:function:f", elbv2types.TargetTypeEnumLambda,
			"arn:aws:lambda:us-east-1:111:function:f"},
		{"alb", "arn:aws:elasticloadbalancing:us-east-1:111:loadbalancer/app/x/y", elbv2types.TargetTypeEnumAlb,
			"arn:aws:elasticloadbalancing:us-east-1:111:loadbalancer/app/x/y"},
		{"ip", "10.0.0.1", elbv2types.TargetTypeEnumIp, "10.0.0.1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveTargetID(tt.rawID, tt.targetType, "us-east-1", "111")
			assert.Equal(t, tt.want, got)
		})
	}
}
