// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/dns/armdns"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

type dnsCollector struct {
	cred           azcore.TokenCredential
	subscriptionID string
}

func newDNSCollector(cred azcore.TokenCredential, subID string) *dnsCollector {
	return &dnsCollector{cred: cred, subscriptionID: subID}
}

func (c *dnsCollector) Name() string { return "azure-dns" }

func (c *dnsCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	zonesClient, err := armdns.NewZonesClient(c.subscriptionID, c.cred, nil)
	if err != nil {
		return cloud.SubCollectorResult{}, fmt.Errorf("azure-dns: zones client: %w", err)
	}

	recordsClient, err := armdns.NewRecordSetsClient(c.subscriptionID, c.cred, nil)
	if err != nil {
		return cloud.SubCollectorResult{}, fmt.Errorf("azure-dns: records client: %w", err)
	}

	var result cloud.SubCollectorResult

	pager := zonesClient.NewListPager(nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return result, fmt.Errorf("azure-dns: list zones: %w", err)
		}

		for _, zone := range page.Value {
			if zone.ID == nil || zone.Name == nil {
				continue
			}

			content, err := json.Marshal(zone)
			if err != nil {
				continue
			}

			result.Resources = append(result.Resources, dnsZoneResourceSpec(zone, content))
			c.collectRecordSets(ctx, recordsClient, zone, &result)
		}
	}

	return result, nil
}

func dnsZoneResourceSpec(zone *armdns.Zone, content []byte) cloud.ResourceSpec {
	spec := cloud.ResourceSpec{
		ID:           *zone.ID,
		Name:         *zone.Name,
		ResourceType: "Microsoft.Network/dnsZones",
		Content:      content,
		Metadata:     map[string]string{},
	}
	if zone.Location != nil {
		spec.Region = *zone.Location
	}
	if zone.Properties != nil {
		if zone.Properties.NumberOfRecordSets != nil {
			spec.Metadata["numberOfRecordSets"] = fmt.Sprintf("%d", *zone.Properties.NumberOfRecordSets)
		}
		if zone.Properties.MaxNumberOfRecordSets != nil {
			spec.Metadata["maxNumberOfRecordSets"] = fmt.Sprintf("%d", *zone.Properties.MaxNumberOfRecordSets)
		}
		if zone.Properties.ZoneType != nil {
			spec.Metadata["zoneType"] = string(*zone.Properties.ZoneType)
		}
	}
	return spec
}

func (c *dnsCollector) collectRecordSets(ctx context.Context, client *armdns.RecordSetsClient, zone *armdns.Zone, result *cloud.SubCollectorResult) {
	rg, zoneName := parseZoneID(*zone.ID)
	if rg == "" || zoneName == "" {
		return
	}

	rsPager := client.NewListAllByDNSZonePager(rg, zoneName, nil)
	for rsPager.More() {
		rsPage, err := rsPager.NextPage(ctx)
		if err != nil {
			break
		}

		for _, rs := range rsPage.Value {
			if rs.ID == nil || rs.Name == nil {
				continue
			}

			content, err := json.Marshal(rs)
			if err != nil {
				continue
			}

			result.Resources = append(result.Resources, dnsRecordResourceSpec(rs, content))
			result.Edges = append(result.Edges, dnsRecordEdges(rs, *zone.ID)...)
		}
	}
}

func dnsRecordResourceSpec(rs *armdns.RecordSet, content []byte) cloud.ResourceSpec {
	spec := cloud.ResourceSpec{
		ID:           *rs.ID,
		Name:         *rs.Name,
		ResourceType: "Microsoft.Network/dnsZones/recordSets",
		Content:      content,
		Metadata:     map[string]string{},
	}
	if rs.Type != nil {
		spec.Metadata["recordType"] = *rs.Type
	}
	if rs.Properties != nil && rs.Properties.TTL != nil {
		spec.Metadata["ttl"] = fmt.Sprintf("%d", *rs.Properties.TTL)
	}
	return spec
}

func dnsRecordEdges(rs *armdns.RecordSet, zoneID string) []cloud.EdgeSpec {
	edges := []cloud.EdgeSpec{
		// Edge: zone CONTAINS record set
		{
			SourceID:     zoneID,
			TargetID:     *rs.ID,
			Relationship: kgtypes.EdgeContains,
		},
	}

	// Edge: record set → target resource (ROUTES_TO) for alias records.
	// Only TargetResource-based edges point to real Azure resource IDs
	// (e.g., load balancers, Traffic Manager profiles).
	if rs.Properties != nil && rs.Properties.TargetResource != nil && rs.Properties.TargetResource.ID != nil {
		edges = append(edges, cloud.EdgeSpec{
			SourceID:     *rs.ID,
			TargetID:     *rs.Properties.TargetResource.ID,
			Relationship: kgtypes.EdgeRoutesTo,
		})
	}

	// A/AAAA/CNAME records: emit ROUTES_TO with raw IP/hostname targets.
	// These may dangle until PostPopulate resolves them against the IP index.
	edges = append(edges, dnsRDataEdges(rs)...)

	return edges
}

// dnsRDataEdges emits ROUTES_TO edges for A, AAAA, and CNAME record data.
// Target IDs are raw IP addresses or hostnames; PostPopulate resolves them.
func dnsRDataEdges(rs *armdns.RecordSet) []cloud.EdgeSpec {
	if rs.Properties == nil || rs.ID == nil {
		return nil
	}
	var edges []cloud.EdgeSpec
	for _, a := range rs.Properties.ARecords {
		if a.IPv4Address != nil && *a.IPv4Address != "" {
			edges = append(edges, cloud.EdgeSpec{
				SourceID: *rs.ID, TargetID: *a.IPv4Address, Relationship: kgtypes.EdgeRoutesTo,
			})
		}
	}
	for _, a := range rs.Properties.AaaaRecords {
		if a.IPv6Address != nil && *a.IPv6Address != "" {
			edges = append(edges, cloud.EdgeSpec{
				SourceID: *rs.ID, TargetID: *a.IPv6Address, Relationship: kgtypes.EdgeRoutesTo,
			})
		}
	}
	if c := rs.Properties.CnameRecord; c != nil && c.Cname != nil && *c.Cname != "" {
		edges = append(edges, cloud.EdgeSpec{
			SourceID: *rs.ID, TargetID: strings.TrimSuffix(*c.Cname, "."), Relationship: kgtypes.EdgeRoutesTo,
		})
	}
	return edges
}

// parseZoneID extracts the resource group name and zone name from an
// Azure DNS zone resource ID of the form:
// /subscriptions/{sub}/resourceGroups/{rg}/providers/Microsoft.Network/dnsZones/{name}
func parseZoneID(id string) (resourceGroup, zoneName string) {
	parts := strings.Split(strings.TrimPrefix(id, "/"), "/")
	for i := 0; i < len(parts)-1; i++ {
		if strings.EqualFold(parts[i], "resourceGroups") {
			resourceGroup = parts[i+1]
		}
		if strings.EqualFold(parts[i], "dnsZones") {
			zoneName = parts[i+1]
		}
	}
	return resourceGroup, zoneName
}
