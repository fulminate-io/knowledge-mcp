// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork/v6"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// nsgRuleSpecs emits one ResourceSpec per security rule (both user-defined and
// default) plus EdgeContains from the NSG to each rule node.
func nsgRuleSpecs(
	nsgID string,
	props *armnetwork.SecurityGroupPropertiesFormat,
) ([]cloud.ResourceSpec, []cloud.EdgeSpec) {
	if props == nil {
		return nil, nil
	}
	var resources []cloud.ResourceSpec
	var edges []cloud.EdgeSpec
	for _, rule := range props.SecurityRules {
		r, e := buildNSGRuleSpec(nsgID, rule, false)
		if r != nil {
			resources = append(resources, *r)
			edges = append(edges, e)
		}
	}
	for _, rule := range props.DefaultSecurityRules {
		r, e := buildNSGRuleSpec(nsgID, rule, true)
		if r != nil {
			resources = append(resources, *r)
			edges = append(edges, e)
		}
	}
	return resources, edges
}

// buildNSGRuleSpec creates a ResourceSpec and an EdgeContains for a single
// security rule. Returns nil if the rule has no Name.
func buildNSGRuleSpec(
	nsgID string,
	rule *armnetwork.SecurityRule,
	isDefault bool,
) (*cloud.ResourceSpec, cloud.EdgeSpec) {
	if rule == nil || rule.Name == nil {
		return nil, cloud.EdgeSpec{}
	}

	ruleID := nsgID + "/securityRules/" + *rule.Name
	content, err := json.Marshal(rule)
	if err != nil {
		content = []byte("{}")
	}

	md := map[string]string{"is_default": fmt.Sprintf("%t", isDefault)}
	if rule.Name != nil {
		md["name"] = *rule.Name
	}
	nsgRulePropsMetadata(rule.Properties, md)

	spec := cloud.ResourceSpec{
		ID:           ruleID,
		Name:         *rule.Name,
		ResourceType: "azure:nsg:rule",
		Content:      content,
		Metadata:     md,
	}
	edge := cloud.EdgeSpec{
		SourceID:     nsgID,
		TargetID:     ruleID,
		Relationship: kgtypes.EdgeContains,
	}
	return &spec, edge
}

// nsgRulePropsMetadata extracts rule properties into string metadata.
func nsgRulePropsMetadata(p *armnetwork.SecurityRulePropertiesFormat, md map[string]string) {
	if p == nil {
		return
	}
	if p.Access != nil {
		md["access"] = string(*p.Access)
	}
	if p.Direction != nil {
		md["direction"] = string(*p.Direction)
	}
	if p.Protocol != nil {
		md["protocol"] = string(*p.Protocol)
	}
	if p.Priority != nil {
		md["priority"] = fmt.Sprintf("%d", *p.Priority)
	}
	nsgRuleAddressMeta(p, md)
	nsgRulePortMeta(p, md)
}

// nsgRuleAddressMeta extracts source/destination address metadata.
func nsgRuleAddressMeta(p *armnetwork.SecurityRulePropertiesFormat, md map[string]string) {
	if p.SourceAddressPrefix != nil {
		md["source_address_prefix"] = *p.SourceAddressPrefix
	}
	if len(p.SourceAddressPrefixes) > 0 {
		md["source_address_prefixes"] = joinPtrStrings(p.SourceAddressPrefixes)
	}
	if p.DestinationAddressPrefix != nil {
		md["destination_address_prefix"] = *p.DestinationAddressPrefix
	}
	if len(p.DestinationAddressPrefixes) > 0 {
		md["destination_address_prefixes"] = joinPtrStrings(p.DestinationAddressPrefixes)
	}
}

// nsgRulePortMeta extracts source/destination port metadata.
func nsgRulePortMeta(p *armnetwork.SecurityRulePropertiesFormat, md map[string]string) {
	if p.SourcePortRange != nil {
		md["source_port_range"] = *p.SourcePortRange
	}
	if len(p.SourcePortRanges) > 0 {
		md["source_port_ranges"] = joinPtrStrings(p.SourcePortRanges)
	}
	if p.DestinationPortRange != nil {
		md["destination_port_range"] = *p.DestinationPortRange
	}
	if len(p.DestinationPortRanges) > 0 {
		md["destination_port_ranges"] = joinPtrStrings(p.DestinationPortRanges)
	}
}

// joinPtrStrings joins a slice of *string as comma-separated values.
func joinPtrStrings(ss []*string) string {
	var out strings.Builder
	for i, s := range ss {
		if s == nil {
			continue
		}
		if i > 0 {
			out.WriteString(",")
		}
		out.WriteString(*s)
	}
	return out.String()
}
