// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork/v6"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

type nsgCollector struct {
	cred           azcore.TokenCredential
	subscriptionID string
}

func newNSGCollector(cred azcore.TokenCredential, subID string) *nsgCollector {
	return &nsgCollector{cred: cred, subscriptionID: subID}
}

func (c *nsgCollector) Name() string { return "azure-nsgs" }

func (c *nsgCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	client, err := armnetwork.NewSecurityGroupsClient(c.subscriptionID, c.cred, nil)
	if err != nil {
		return cloud.SubCollectorResult{}, fmt.Errorf("azure-nsgs: client: %w", err)
	}

	var result cloud.SubCollectorResult

	pager := client.NewListAllPager(nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return result, fmt.Errorf("azure-nsgs: list: %w", err)
		}

		for _, nsg := range page.Value {
			if nsg.ID == nil || nsg.Name == nil {
				continue
			}

			content, err := json.Marshal(nsg)
			if err != nil {
				continue
			}

			spec := cloud.ResourceSpec{
				ID:           *nsg.ID,
				Name:         *nsg.Name,
				ResourceType: "Microsoft.Network/networkSecurityGroups",
				Content:      content,
				Metadata:     map[string]string{},
			}
			if nsg.Location != nil {
				spec.Region = *nsg.Location
			}
			if nsg.Properties != nil && nsg.Properties.SecurityRules != nil {
				spec.Metadata["ruleCount"] = fmt.Sprintf("%d", len(nsg.Properties.SecurityRules))
			}

			result.Resources = append(result.Resources, spec)

			rules, edges := nsgRuleSpecs(*nsg.ID, nsg.Properties)
			result.Resources = append(result.Resources, rules...)
			result.Edges = append(result.Edges, edges...)
		}
	}

	return result, nil
}
