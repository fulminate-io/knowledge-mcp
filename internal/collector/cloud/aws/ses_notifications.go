// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"context"
	"fmt"
	"log/slog"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ses"
	sestypes "github.com/aws/aws-sdk-go-v2/service/ses/types"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// collectNotificationEdges uses the v1 SES API to fetch SNS notification
// topics (Bounce, Complaint, Delivery) for each identity and emits
// EdgeNotifiesVia edges. This API is only available in v1 — sesv2 does
// not expose notification attributes.
func (c *sesCollector) collectNotificationEdges(ctx context.Context, identityNames []string) []cloud.EdgeSpec {
	if len(identityNames) == 0 {
		return nil
	}

	out, err := c.v1client.GetIdentityNotificationAttributes(ctx, &ses.GetIdentityNotificationAttributesInput{
		Identities: identityNames,
	})
	if err != nil {
		slog.Warn("ses: get identity notification attributes", "error", err)
		return nil
	}
	if out == nil {
		return nil
	}

	var edges []cloud.EdgeSpec
	for _, name := range identityNames {
		attrs, ok := out.NotificationAttributes[name]
		if !ok {
			continue
		}
		identityARN := fmt.Sprintf("arn:aws:ses:%s:%s:identity/%s", c.region, c.accountID, name)
		for _, topicARN := range notificationTopics(attrs) {
			edges = append(edges, cloud.EdgeSpec{
				SourceID:     identityARN,
				TargetID:     topicARN,
				Relationship: kgtypes.EdgeNotifiesVia,
			})
		}
	}
	return edges
}

// notificationTopics extracts non-empty SNS topic ARNs from v1 SES
// notification attributes. Deduplicates — a common pattern is all three
// notifications pointing at the same SNS topic.
func notificationTopics(attrs sestypes.IdentityNotificationAttributes) []string {
	seen := make(map[string]struct{}, 3)
	var topics []string
	for _, arn := range []string{
		awssdk.ToString(attrs.BounceTopic),
		awssdk.ToString(attrs.ComplaintTopic),
		awssdk.ToString(attrs.DeliveryTopic),
	} {
		if arn == "" {
			continue
		}
		if _, ok := seen[arn]; ok {
			continue
		}
		seen[arn] = struct{}{}
		topics = append(topics, arn)
	}
	return topics
}
