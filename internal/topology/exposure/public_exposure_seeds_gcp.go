// SPDX-License-Identifier: Apache-2.0

package exposure

// public_exposure_seeds_gcp.go holds the GCP seed rules that detect
// public-entry resources by re-parsing the cloud collector's node.Content
// envelopes or reading collector-written metadata. Each rule uses a LOCAL
// anonymous struct for parsing so we avoid importing cloud/gcp — the only
// contract between this file and the collector is the resource_type
// strings and the JSON shapes.
//
// Rules (cloud family "gcp"):
//
//   - gcp:compute:forwardingRule   — loadBalancingScheme is EXTERNAL or
//     EXTERNAL_MANAGED, score 0.9
//   - gcp:compute:securityPolicy   — Cloud Armor policy presence implies
//     the protected backend is public-facing, score 0.7
//
// Per the open-question decision (accuracy over noise), Cloud Armor is
// included: policies can technically be applied to internal LBs, but the
// dominant use case is protecting public backends. The lower score (0.7)
// reflects this uncertainty.

import (
	"context"
	"encoding/json"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

func init() {
	registerSeedRule("gcp:compute:forwardingRule", "gcp", gcpForwardingRuleSeedRule)
	registerSeedRule("gcp:compute:securityPolicy", "gcp", gcpCloudArmorSeedRule)
}

// gcpForwardingRuleSeedRule fires when the forwarding rule's
// loadBalancingScheme is EXTERNAL or EXTERNAL_MANAGED. Internal
// forwarding rules (INTERNAL, INTERNAL_MANAGED) are not public seeds.
// The collector stores loadBalancingScheme both in metadata and in the
// raw JSON; we prefer metadata (faster), with a content fallback.
func gcpForwardingRuleSeedRule(_ context.Context, _ *cloudReader, node *knowledgev1.Node) (*publicSeed, error) {
	scheme := nodeMeta(node, "loadBalancingScheme")
	if scheme == "" {
		// Content fallback for older collector versions.
		var rule struct {
			LoadBalancingScheme *string `json:"loadBalancingScheme"`
		}
		if err := json.Unmarshal([]byte(node.Content), &rule); err != nil {
			return nil, nil //nolint:nilerr // parse errors = not-a-seed per seedRule contract
		}
		if rule.LoadBalancingScheme != nil {
			scheme = *rule.LoadBalancingScheme
		}
	}
	if scheme != "EXTERNAL" && scheme != "EXTERNAL_MANAGED" {
		return nil, nil
	}
	return &publicSeed{
		NodeID:     node.Id,
		EntryScore: 0.9,
		Reason:     "GCP forwarding rule with external load balancing scheme",
	}, nil
}

// gcpCloudArmorSeedRule fires for every Cloud Armor security policy node.
// Cloud Armor policies are primarily applied to public-facing backend
// services. Their presence is a strong signal that the protected backend
// is internet-exposed. Score is 0.7 (lower than direct public entry
// points) to account for the possibility of internal-only attachment.
func gcpCloudArmorSeedRule(_ context.Context, _ *cloudReader, node *knowledgev1.Node) (*publicSeed, error) {
	return &publicSeed{
		NodeID:     node.Id,
		EntryScore: 0.7,
		Reason:     "GCP Cloud Armor policy (implies public backend)",
	}, nil
}
