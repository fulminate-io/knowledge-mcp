// SPDX-License-Identifier: Apache-2.0

package exposure

// public_exposure_seeds_azure.go holds the Azure seed rules that detect
// public-entry resources by re-parsing the cloud collector's node.Content
// envelopes. Each rule uses a LOCAL anonymous struct for parsing so we
// avoid importing cloud/azure — the only contract between this file and
// the collector is the resource_type strings and the JSON shapes.
//
// Rules (cloud family "azure"):
//
//   - Microsoft.Network/applicationGateways — has a frontend IP configuration
//     with a public IP address reference, score 0.9
//   - Microsoft.Cdn/profiles/afdEndpoints   — Azure Front Door endpoint,
//     always public by design, score 0.9

import (
	"context"
	"encoding/json"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

func init() {
	registerSeedRule("Microsoft.Network/applicationGateways", "azure", azureAppGatewaySeedRule)
	registerSeedRule("Microsoft.Cdn/profiles/afdEndpoints", "azure", azureFrontDoorEndpointSeedRule)
}

// azureAppGatewaySeedRule fires when the Application Gateway has at least
// one frontend IP configuration referencing a public IP address. The ARM
// JSON shape has Properties.FrontendIPConfigurations[].Properties.PublicIPAddress.
// A nil or missing PublicIPAddress on all frontend configs means internal-only.
func azureAppGatewaySeedRule(_ context.Context, _ *cloudReader, node *knowledgev1.Node) (*publicSeed, error) {
	var gw struct {
		Properties *struct {
			FrontendIPConfigurations []struct {
				Properties *struct {
					PublicIPAddress *struct {
						ID string `json:"id"`
					} `json:"publicIPAddress"`
				} `json:"properties"`
			} `json:"frontendIPConfigurations"`
		} `json:"properties"`
	}
	if err := json.Unmarshal([]byte(node.Content), &gw); err != nil {
		return nil, nil //nolint:nilerr // parse errors = not-a-seed per seedRule contract
	}
	if gw.Properties == nil {
		return nil, nil
	}
	for _, fip := range gw.Properties.FrontendIPConfigurations {
		if fip.Properties != nil && fip.Properties.PublicIPAddress != nil &&
			fip.Properties.PublicIPAddress.ID != "" {
			return &publicSeed{
				NodeID:     node.Id,
				EntryScore: 0.9,
				Reason:     "Azure Application Gateway with public frontend IP",
			}, nil
		}
	}
	return nil, nil
}

// azureFrontDoorEndpointSeedRule fires for every Azure Front Door AFD
// endpoint. Front Door endpoints are internet-facing by design — they
// expose content via Microsoft's global edge network. There is no
// private/internal mode for AFD endpoints, so the mere presence of the
// node is the signal (same pattern as k8sIngressSeedRule).
func azureFrontDoorEndpointSeedRule(_ context.Context, _ *cloudReader, node *knowledgev1.Node) (*publicSeed, error) {
	return &publicSeed{
		NodeID:     node.Id,
		EntryScore: 0.9,
		Reason:     "Azure Front Door endpoint (always public)",
	}, nil
}
