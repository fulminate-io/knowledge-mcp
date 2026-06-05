// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"context"
	"encoding/json"
	"fmt"

	compute "cloud.google.com/go/compute/apiv1"
	computepb "cloud.google.com/go/compute/apiv1/computepb"
	"google.golang.org/api/iterator"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// --- Wire structs (curated content envelope for cloud armor) ---

// securityPolicyContent is the curated wire shape for gcp:compute:securityPolicy.
// Field set frozen in the Phase 1 audit. Rules are
// preserved as a flattened slice so the existing `len(rules)` metadata count
// continues to work; individual rule content is opaque (only count is used).
type securityPolicyContent struct {
	Name         string                             `json:"name,omitempty"`
	SelfLink     string                             `json:"selfLink,omitempty"`
	Type         string                             `json:"type,omitempty"`
	Rules        []securityPolicyContentRule        `json:"rules,omitempty"`
	Associations []securityPolicyContentAssociation `json:"associations,omitempty"`
}

type securityPolicyContentRule struct {
	Priority int32 `json:"priority,omitempty"`
}

type securityPolicyContentAssociation struct {
	AttachmentId string `json:"attachmentId,omitempty"`
}

// buildSecurityPolicyContent projects a *computepb.SecurityPolicy into the curated wire shape.
func buildSecurityPolicyContent(p *computepb.SecurityPolicy) securityPolicyContent {
	out := securityPolicyContent{
		Name:     p.GetName(),
		SelfLink: p.GetSelfLink(),
		Type:     p.GetType(),
	}
	for _, r := range p.GetRules() {
		if r == nil {
			continue
		}
		out.Rules = append(out.Rules, securityPolicyContentRule{
			Priority: r.GetPriority(),
		})
	}
	for _, a := range p.GetAssociations() {
		if a == nil {
			continue
		}
		out.Associations = append(out.Associations, securityPolicyContentAssociation{
			AttachmentId: a.GetAttachmentId(),
		})
	}
	return out
}

// cloudArmorSubCollector collects Cloud Armor security policies.
type cloudArmorSubCollector struct {
	client    *compute.SecurityPoliciesClient
	projectID string
}

func newCloudArmorSubCollector(client *compute.SecurityPoliciesClient, projectID string) *cloudArmorSubCollector {
	return &cloudArmorSubCollector{client: client, projectID: projectID}
}

func (c *cloudArmorSubCollector) Name() string { return "gcp-cloud-armor" }

func (c *cloudArmorSubCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	var result cloud.SubCollectorResult

	it := c.client.List(ctx, &computepb.ListSecurityPoliciesRequest{
		Project: c.projectID,
	})

	for {
		policy, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return result, fmt.Errorf("cloud armor: list security policies: %w", err)
		}

		selfLink := policy.GetSelfLink()
		if selfLink == "" {
			continue
		}

		content, err := json.Marshal(buildSecurityPolicyContent(policy))
		if err != nil {
			return result, fmt.Errorf("gcp cloud armor: marshal security policy content: %w", err)
		}

		spec := cloud.ResourceSpec{
			ID:           selfLink,
			Name:         policy.GetName(),
			ResourceType: "gcp:compute:securityPolicy",
			Content:      content,
			Metadata: map[string]string{
				"type":      policy.GetType(),
				"ruleCount": fmt.Sprintf("%d", len(policy.GetRules())),
			},
		}
		result.Resources = append(result.Resources, spec)

		// Security policy -> attached resources (org/folder-level policies).
		// For standard Cloud Armor policies attached to backend services,
		// the PROTECTS edge is emitted by the backend services subcollector
		// because the association is stored on the backend service side.
		for _, assoc := range policy.GetAssociations() {
			if target := assoc.GetAttachmentId(); target != "" {
				result.Edges = append(result.Edges, cloud.EdgeSpec{
					SourceID:     selfLink,
					TargetID:     target,
					Relationship: kgtypes.EdgeProtects,
				})
			}
		}
	}

	return result, nil
}
