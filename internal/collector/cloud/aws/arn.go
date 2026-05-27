// SPDX-License-Identifier: Apache-2.0

package aws

import "fmt"

// ec2ARN constructs an AWS ARN for EC2-family resources (VPCs, subnets, security groups, instances).
func ec2ARN(region, accountID, resourceType, resourceID string) string {
	return fmt.Sprintf("arn:aws:ec2:%s:%s:%s/%s", region, accountID, resourceType, resourceID)
}
