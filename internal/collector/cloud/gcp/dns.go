// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	dns "google.golang.org/api/dns/v1"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// --- Wire structs (FUL-88: curated content envelope for cloud dns) ---

// managedZoneContent is the curated wire shape for gcp:dns:managedZone.
// Field set frozen in Phase 1 audit (session ful-88-gcp-planning).
// Note: googleapi dns.ManagedZone has no SelfLink field; the synthetic
// ResourceSpec.ID `projects/<proj>/managedZones/<name>` is composed by the
// collector and lives outside this Content blob.
type managedZoneContent struct {
	Name       string `json:"name,omitempty"`
	DnsName    string `json:"dnsName,omitempty"`
	Visibility string `json:"visibility,omitempty"`
}

// resourceRecordSetContent is the curated wire shape for gcp:dns:recordSet.
type resourceRecordSetContent struct {
	Name    string   `json:"name,omitempty"`
	Type    string   `json:"type,omitempty"`
	TTL     int64    `json:"ttl,omitempty"`
	Rrdatas []string `json:"rrdatas,omitempty"`
}

// buildManagedZoneContent projects a *dns.ManagedZone into the curated wire shape.
func buildManagedZoneContent(z *dns.ManagedZone) managedZoneContent {
	return managedZoneContent{
		Name:       z.Name,
		DnsName:    z.DnsName,
		Visibility: z.Visibility,
	}
}

// buildResourceRecordSetContent projects a *dns.ResourceRecordSet into the curated wire shape.
func buildResourceRecordSetContent(r *dns.ResourceRecordSet) resourceRecordSetContent {
	return resourceRecordSetContent{
		Name:    r.Name,
		Type:    r.Type,
		TTL:     r.Ttl,
		Rrdatas: r.Rrdatas,
	}
}

// dnsSubCollector collects Cloud DNS managed zones and record sets.
// Uses the REST-based google.golang.org/api (not gRPC), same pattern as sqlSubCollector.
type dnsSubCollector struct {
	service   *dns.Service
	projectID string
}

func newDNSSubCollector(service *dns.Service, projectID string) *dnsSubCollector {
	return &dnsSubCollector{service: service, projectID: projectID}
}

func (c *dnsSubCollector) Name() string { return "gcp-cloud-dns" }

func (c *dnsSubCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	var result cloud.SubCollectorResult

	pageToken := ""
	for {
		call := c.service.ManagedZones.List(c.projectID).Context(ctx)
		if pageToken != "" {
			call = call.PageToken(pageToken)
		}

		resp, err := call.Do()
		if err != nil {
			return result, fmt.Errorf("dns: list managed zones: %w", err)
		}

		for _, zone := range resp.ManagedZones {
			zoneSpec, err := dnsZoneSpec(c.projectID, zone)
			if err != nil {
				return result, fmt.Errorf("gcp dns: marshal managed zone content: %w", err)
			}
			result.Resources = append(result.Resources, zoneSpec)

			records, edges, err := c.collectZoneRecords(ctx, zone)
			if err != nil {
				return result, err
			}
			result.Resources = append(result.Resources, records...)
			result.Edges = append(result.Edges, edges...)
		}

		pageToken = resp.NextPageToken
		if pageToken == "" {
			break
		}
	}

	return result, nil
}

// collectZoneRecords lists record sets for a zone and returns resources + edges.
func (c *dnsSubCollector) collectZoneRecords(
	ctx context.Context, zone *dns.ManagedZone,
) ([]cloud.ResourceSpec, []cloud.EdgeSpec, error) {
	var (
		resources []cloud.ResourceSpec
		edges     []cloud.EdgeSpec
	)

	zoneID := fmt.Sprintf("projects/%s/managedZones/%s", c.projectID, zone.Name)

	pageToken := ""
	for {
		call := c.service.ResourceRecordSets.List(c.projectID, zone.Name).Context(ctx)
		if pageToken != "" {
			call = call.PageToken(pageToken)
		}

		resp, err := call.Do()
		if err != nil {
			return nil, nil, fmt.Errorf("dns: list record sets for zone %s: %w", zone.Name, err)
		}

		for _, rs := range resp.Rrsets {
			spec, err := dnsRecordSpec(zoneID, rs)
			if err != nil {
				return nil, nil, fmt.Errorf("gcp dns: marshal record set content: %w", err)
			}
			resources = append(resources, spec)

			// Zone -> record set.
			edges = append(edges, cloud.EdgeSpec{
				SourceID:     zoneID,
				TargetID:     spec.ID,
				Relationship: kgtypes.EdgeContains,
			})
			// ROUTES_TO edges are emitted by resolveDNSRecordTargets in
			// postpopulate_dns.go using a typed IP index — never as raw rdata
			// strings here. Records that don't resolve to a collected node
			// produce no edge.
		}

		pageToken = resp.NextPageToken
		if pageToken == "" {
			break
		}
	}

	return resources, edges, nil
}

func dnsZoneSpec(projectID string, zone *dns.ManagedZone) (cloud.ResourceSpec, error) {
	content, err := json.Marshal(buildManagedZoneContent(zone))
	if err != nil {
		return cloud.ResourceSpec{}, err
	}

	id := fmt.Sprintf("projects/%s/managedZones/%s", projectID, zone.Name)

	return cloud.ResourceSpec{
		ID:           id,
		Name:         zone.Name,
		ResourceType: "gcp:dns:managedZone",
		Content:      content,
		Metadata: map[string]string{
			"dnsName":    zone.DnsName,
			"visibility": zone.Visibility,
		},
	}, nil
}

func dnsRecordSpec(zoneID string, rs *dns.ResourceRecordSet) (cloud.ResourceSpec, error) {
	content, err := json.Marshal(buildResourceRecordSetContent(rs))
	if err != nil {
		return cloud.ResourceSpec{}, err
	}

	id := fmt.Sprintf("%s/rrsets/%s/%s", zoneID, rs.Name, rs.Type)

	return cloud.ResourceSpec{
		ID:           id,
		Name:         rs.Name,
		ResourceType: "gcp:dns:recordSet",
		Content:      content,
		Metadata: map[string]string{
			"type":    rs.Type,
			"ttl":     fmt.Sprintf("%d", rs.Ttl),
			"rrdatas": strings.Join(rs.Rrdatas, ","),
		},
	}, nil
}
