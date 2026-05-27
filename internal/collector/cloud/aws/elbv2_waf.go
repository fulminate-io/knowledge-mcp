// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"context"
	"log/slog"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/wafv2"
	wafv2types "github.com/aws/aws-sdk-go-v2/service/wafv2/types"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// lookupWebACL checks whether a load balancer has an associated WAFv2 WebACL
// and returns a PROTECTS edge if found. Best-effort: returns nil on any
// error (WAF permissions may not be granted).
func (c *elbv2Collector) lookupWebACL(ctx context.Context, lbARN string) []cloud.EdgeSpec {
	if c.wafClient == nil {
		return nil
	}
	out, err := c.wafClient.GetWebACLForResource(ctx, &wafv2.GetWebACLForResourceInput{
		ResourceArn: awssdk.String(lbARN),
	})
	if err != nil {
		// Fail-open: WAF access may be restricted or no WebACL associated.
		slog.Debug("elbv2: lookupWebACL failed (best-effort)", "lb", lbARN, "err", err)
		return nil
	}
	if out.WebACL == nil || out.WebACL.ARN == nil {
		return nil
	}
	aclARN := *out.WebACL.ARN
	meta := map[string]string{
		"scope": string(wafv2types.ScopeRegional),
	}
	if out.WebACL.Name != nil {
		meta["acl_name"] = *out.WebACL.Name
	}
	return []cloud.EdgeSpec{{
		SourceID:     aclARN,
		TargetID:     lbARN,
		Relationship: kgtypes.EdgeProtects,
		Metadata:     meta,
	}}
}
