// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/applicationinsights/armapplicationinsights"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/operationalinsights/armoperationalinsights"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

type monitoringCollector struct {
	cred           azcore.TokenCredential
	subscriptionID string
}

func newMonitoringCollector(cred azcore.TokenCredential, subID string) *monitoringCollector {
	return &monitoringCollector{cred: cred, subscriptionID: subID}
}

func (c *monitoringCollector) Name() string { return "azure-monitoring" }

func (c *monitoringCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	var result cloud.SubCollectorResult

	// Collect Log Analytics workspaces.
	if err := c.collectWorkspaces(ctx, &result); err != nil {
		return result, err
	}

	// Collect App Insights components.
	if err := c.collectAppInsights(ctx, &result); err != nil {
		return result, err
	}

	// Collect subscription-level diagnostic settings (Activity Log routing).
	if err := c.collectDiagnosticSettings(ctx, &result); err != nil {
		return result, err
	}

	return result, nil
}

func (c *monitoringCollector) collectWorkspaces(ctx context.Context, result *cloud.SubCollectorResult) error {
	client, err := armoperationalinsights.NewWorkspacesClient(c.subscriptionID, c.cred, nil)
	if err != nil {
		return fmt.Errorf("azure-monitoring: workspaces client: %w", err)
	}

	pager := client.NewListPager(nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return fmt.Errorf("azure-monitoring: list workspaces: %w", err)
		}

		for _, ws := range page.Value {
			if ws.ID == nil || ws.Name == nil {
				continue
			}

			content, err := json.Marshal(buildWorkspaceContent(ws))
			if err != nil {
				return fmt.Errorf("azure-monitoring: marshal workspace content: %w", err)
			}

			spec := cloud.ResourceSpec{
				ID:           *ws.ID,
				Name:         *ws.Name,
				ResourceType: "Microsoft.OperationalInsights/workspaces",
				Content:      content,
				Metadata:     map[string]string{},
			}
			if ws.Location != nil {
				spec.Region = *ws.Location
			}
			if ws.Properties != nil {
				if ws.Properties.RetentionInDays != nil {
					spec.Metadata["retentionInDays"] = fmt.Sprintf("%d", *ws.Properties.RetentionInDays)
				}
				if ws.Properties.ProvisioningState != nil {
					spec.Metadata["provisioningState"] = string(*ws.Properties.ProvisioningState)
				}
			}

			result.Resources = append(result.Resources, spec)
		}
	}

	return nil
}

func (c *monitoringCollector) collectAppInsights(ctx context.Context, result *cloud.SubCollectorResult) error {
	client, err := armapplicationinsights.NewComponentsClient(c.subscriptionID, c.cred, nil)
	if err != nil {
		return fmt.Errorf("azure-monitoring: appinsights client: %w", err)
	}

	pager := client.NewListPager(nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return fmt.Errorf("azure-monitoring: list appinsights: %w", err)
		}

		for _, comp := range page.Value {
			if comp.ID == nil || comp.Name == nil {
				continue
			}

			content, err := json.Marshal(buildAppInsightsComponentContent(comp))
			if err != nil {
				return fmt.Errorf("azure-monitoring: marshal appinsights content: %w", err)
			}

			result.Resources = append(result.Resources, appInsightsResourceSpec(comp, content))
			result.Edges = append(result.Edges, appInsightsEdges(comp)...)
		}
	}

	return nil
}

func appInsightsResourceSpec(comp *armapplicationinsights.Component, content []byte) cloud.ResourceSpec {
	spec := cloud.ResourceSpec{
		ID:           *comp.ID,
		Name:         *comp.Name,
		ResourceType: "Microsoft.Insights/components",
		Content:      content,
		Metadata:     map[string]string{},
	}
	if comp.Location != nil {
		spec.Region = *comp.Location
	}
	if comp.Kind != nil {
		spec.Metadata["kind"] = *comp.Kind
	}
	if comp.Properties != nil {
		if comp.Properties.ApplicationType != nil {
			spec.Metadata["applicationType"] = string(*comp.Properties.ApplicationType)
		}
		if comp.Properties.RetentionInDays != nil {
			spec.Metadata["retentionInDays"] = fmt.Sprintf("%d", *comp.Properties.RetentionInDays)
		}
	}
	return spec
}

func appInsightsEdges(comp *armapplicationinsights.Component) []cloud.EdgeSpec {
	var edges []cloud.EdgeSpec
	if comp.Properties != nil && comp.Properties.WorkspaceResourceID != nil && *comp.Properties.WorkspaceResourceID != "" {
		edges = append(edges, cloud.EdgeSpec{
			SourceID:     *comp.ID,
			TargetID:     *comp.Properties.WorkspaceResourceID,
			Relationship: kgtypes.EdgeSinksTo,
		})
	}
	return edges
}

// workspaceContent is the curated wire shape for
// Microsoft.OperationalInsights/workspaces. Curated projection of
// *armoperationalinsights.Workspace (collector-owned, decoupled from SDK
// version). RetentionInDays kept as *int32 so nil and zero remain
// distinguishable for any future reader.
//
// Excluded: features, capping, customer ID — no reader.
type workspaceContent struct {
	ID         string                      `json:"id"`
	Name       string                      `json:"name"`
	Location   string                      `json:"location,omitempty"`
	Properties *workspaceContentProperties `json:"properties,omitempty"`
}

type workspaceContentProperties struct {
	ProvisioningState string `json:"provisioningState,omitempty"`
	RetentionInDays   *int32 `json:"retentionInDays,omitempty"`
}

// buildWorkspaceContent projects an *armoperationalinsights.Workspace into
// the workspaceContent wire shape. Nil-safe at every level.
func buildWorkspaceContent(ws *armoperationalinsights.Workspace) workspaceContent {
	out := workspaceContent{}
	if ws == nil {
		return out
	}
	if ws.ID != nil {
		out.ID = *ws.ID
	}
	if ws.Name != nil {
		out.Name = *ws.Name
	}
	if ws.Location != nil {
		out.Location = *ws.Location
	}
	if ws.Properties != nil {
		props := &workspaceContentProperties{}
		if ws.Properties.ProvisioningState != nil {
			props.ProvisioningState = string(*ws.Properties.ProvisioningState)
		}
		if ws.Properties.RetentionInDays != nil {
			v := *ws.Properties.RetentionInDays
			props.RetentionInDays = &v
		}
		out.Properties = props
	}
	return out
}

// appInsightsComponentContent is the curated wire shape for
// Microsoft.Insights/components. Curated projection of
// *armapplicationinsights.Component (collector-owned, decoupled from SDK
// version). WorkspaceResourceID surfaced into Content (also pre-Marshal at
// monitoring.go:157 for edge extraction).
//
// Excluded: instrumentationKey, connectionString — secrets-adjacent,
// deliberate omission.
type appInsightsComponentContent struct {
	ID         string                                 `json:"id"`
	Name       string                                 `json:"name"`
	Location   string                                 `json:"location,omitempty"`
	Kind       string                                 `json:"kind,omitempty"`
	Properties *appInsightsComponentContentProperties `json:"properties,omitempty"`
}

type appInsightsComponentContentProperties struct {
	ApplicationType     string `json:"applicationType,omitempty"`
	RetentionInDays     *int32 `json:"retentionInDays,omitempty"`
	WorkspaceResourceID string `json:"workspaceResourceId,omitempty"`
}

// buildAppInsightsComponentContent projects an *armapplicationinsights.Component
// into the appInsightsComponentContent wire shape. Nil-safe at every level.
func buildAppInsightsComponentContent(comp *armapplicationinsights.Component) appInsightsComponentContent {
	out := appInsightsComponentContent{}
	if comp == nil {
		return out
	}
	if comp.ID != nil {
		out.ID = *comp.ID
	}
	if comp.Name != nil {
		out.Name = *comp.Name
	}
	if comp.Location != nil {
		out.Location = *comp.Location
	}
	if comp.Kind != nil {
		out.Kind = *comp.Kind
	}
	if comp.Properties != nil {
		props := &appInsightsComponentContentProperties{}
		if comp.Properties.ApplicationType != nil {
			props.ApplicationType = string(*comp.Properties.ApplicationType)
		}
		if comp.Properties.RetentionInDays != nil {
			v := *comp.Properties.RetentionInDays
			props.RetentionInDays = &v
		}
		if comp.Properties.WorkspaceResourceID != nil {
			props.WorkspaceResourceID = *comp.Properties.WorkspaceResourceID
		}
		out.Properties = props
	}
	return out
}
