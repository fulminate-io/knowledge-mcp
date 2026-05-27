// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"context"
	"fmt"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	elbv2sdk "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbv2types "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// elbv2TargetHealthAPI is the subset of the ELBv2 client surface needed for
// target health collection. Defined as an interface for testability.
// The concrete *elbv2.Client satisfies this interface.
type elbv2TargetHealthAPI interface {
	DescribeTargetHealth(ctx context.Context, params *elbv2sdk.DescribeTargetHealthInput, optFns ...func(*elbv2sdk.Options)) (*elbv2sdk.DescribeTargetHealthOutput, error)
}

// collectTargetHealth calls DescribeTargetHealth for a target group and returns
// TARGETS edges from the TG to each registered target (EC2 instance, Lambda
// function, IP address, or ALB).
func collectTargetHealth(
	ctx context.Context,
	api elbv2TargetHealthAPI,
	tgARN string,
	targetType elbv2types.TargetTypeEnum,
	region, accountID string,
) ([]cloud.EdgeSpec, error) {
	out, err := api.DescribeTargetHealth(ctx, &elbv2sdk.DescribeTargetHealthInput{
		TargetGroupArn: awssdk.String(tgARN),
	})
	if err != nil {
		return nil, fmt.Errorf("elbv2: describe target health for %s: %w", tgARN, err)
	}

	var edges []cloud.EdgeSpec
	for _, desc := range out.TargetHealthDescriptions {
		if desc.Target == nil || desc.Target.Id == nil {
			continue
		}
		targetID := resolveTargetID(*desc.Target.Id, targetType, region, accountID)
		meta := map[string]string{
			"target_type": string(targetType),
		}
		if desc.Target.Port != nil {
			meta["port"] = fmt.Sprintf("%d", *desc.Target.Port)
		}
		if desc.TargetHealth != nil && desc.TargetHealth.State != "" {
			meta["health_status"] = string(desc.TargetHealth.State)
		}
		edges = append(edges, cloud.EdgeSpec{
			SourceID:     tgARN,
			TargetID:     targetID,
			Relationship: kgtypes.EdgeTargets,
			Metadata:     meta,
		})
	}
	return edges, nil
}

// resolveTargetID converts a raw target ID from DescribeTargetHealth into a
// proper node ID based on the target group's target type.
func resolveTargetID(rawID string, targetType elbv2types.TargetTypeEnum, region, accountID string) string {
	switch targetType {
	case elbv2types.TargetTypeEnumInstance:
		return ec2ARN(region, accountID, "instance", rawID)
	case elbv2types.TargetTypeEnumLambda, elbv2types.TargetTypeEnumAlb:
		// Lambda and ALB targets are already ARNs.
		return rawID
	default:
		// IP targets and unknown types: return raw value.
		return rawID
	}
}
