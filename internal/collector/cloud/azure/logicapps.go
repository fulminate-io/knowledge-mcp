// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/logic/armlogic"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

type logicAppsCollector struct {
	cred           azcore.TokenCredential
	subscriptionID string
}

func newLogicAppsCollector(cred azcore.TokenCredential, subID string) *logicAppsCollector {
	return &logicAppsCollector{cred: cred, subscriptionID: subID}
}

func (c *logicAppsCollector) Name() string { return "azure-logic-apps" }

func (c *logicAppsCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	client, err := armlogic.NewWorkflowsClient(c.subscriptionID, c.cred, nil)
	if err != nil {
		return cloud.SubCollectorResult{}, fmt.Errorf("azure-logic-apps: client: %w", err)
	}

	var result cloud.SubCollectorResult

	pager := client.NewListBySubscriptionPager(nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return result, fmt.Errorf("azure-logic-apps: list: %w", err)
		}

		for _, wf := range page.Value {
			if wf.ID == nil || wf.Name == nil {
				continue
			}

			content, err := json.Marshal(buildWorkflowContent(wf))
			if err != nil {
				return result, fmt.Errorf("azure-logic-apps: marshal workflow content: %w", err)
			}

			result.Resources = append(result.Resources, logicAppResourceSpec(wf, content))
			result.Edges = append(result.Edges, logicAppEdges(wf)...)
		}
	}

	return result, nil
}

// workflowContent is the curated wire shape for Microsoft.Logic/workflows.
// Curated projection of *armlogic.Workflow (collector-owned, decoupled from
// SDK version). No reader currently consumes Content for this resource type
// — fields enumerate the metadata-extractor field set
// (logicAppPropertiesMetadata) for symmetry. JSON tags use lowerCamelCase to
// match the Azure ARM JSON shape.
//
// Excluded: workflow definition body, integration account ID — no reader.
type workflowContent struct {
	ID         string                     `json:"id"`
	Name       string                     `json:"name"`
	Location   string                     `json:"location,omitempty"`
	Properties *workflowContentProperties `json:"properties,omitempty"`
}

type workflowContentProperties struct {
	State             string              `json:"state,omitempty"`
	ProvisioningState string              `json:"provisioningState,omitempty"`
	SKU               *workflowContentSKU `json:"sku,omitempty"`
}

type workflowContentSKU struct {
	Name string `json:"name,omitempty"`
}

// buildWorkflowContent projects an *armlogic.Workflow into the workflowContent
// wire shape. Nil-safe at every level.
func buildWorkflowContent(wf *armlogic.Workflow) workflowContent {
	out := workflowContent{}
	if wf == nil {
		return out
	}
	if wf.ID != nil {
		out.ID = *wf.ID
	}
	if wf.Name != nil {
		out.Name = *wf.Name
	}
	if wf.Location != nil {
		out.Location = *wf.Location
	}
	if wf.Properties != nil {
		props := &workflowContentProperties{}
		if wf.Properties.State != nil {
			props.State = string(*wf.Properties.State)
		}
		if wf.Properties.ProvisioningState != nil {
			props.ProvisioningState = string(*wf.Properties.ProvisioningState)
		}
		if wf.Properties.SKU != nil && wf.Properties.SKU.Name != nil {
			props.SKU = &workflowContentSKU{Name: string(*wf.Properties.SKU.Name)}
		}
		out.Properties = props
	}
	return out
}

func logicAppResourceSpec(wf *armlogic.Workflow, content []byte) cloud.ResourceSpec {
	spec := cloud.ResourceSpec{
		ID:           *wf.ID,
		Name:         *wf.Name,
		ResourceType: "Microsoft.Logic/workflows",
		Content:      content,
		Metadata:     map[string]string{},
	}
	if wf.Location != nil {
		spec.Region = *wf.Location
	}
	logicAppPropertiesMetadata(wf.Properties, spec.Metadata)
	return spec
}

func logicAppPropertiesMetadata(p *armlogic.WorkflowProperties, meta map[string]string) {
	if p == nil {
		return
	}
	if p.State != nil {
		meta["state"] = string(*p.State)
	}
	if p.ProvisioningState != nil {
		meta["provisioningState"] = string(*p.ProvisioningState)
	}
	if p.SKU != nil && p.SKU.Name != nil {
		meta["skuName"] = string(*p.SKU.Name)
	}
}

// logicAppEdges extracts ASSUMES_ROLE edges for user-assigned managed identities.
func logicAppEdges(wf *armlogic.Workflow) []cloud.EdgeSpec {
	if wf.Identity == nil || wf.Identity.UserAssignedIdentities == nil {
		return nil
	}

	var edges []cloud.EdgeSpec
	for identityID := range wf.Identity.UserAssignedIdentities {
		edges = append(edges, cloud.EdgeSpec{
			SourceID:     *wf.ID,
			TargetID:     identityID,
			Relationship: kgtypes.EdgeAssumesRole,
			Metadata:     map[string]string{"role_source": "managed_identity"},
		})
	}
	return edges
}
