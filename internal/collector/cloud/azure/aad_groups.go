// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

const (
	graphGroupsURL    = "https://graph.microsoft.com/v1.0/groups"
	graphGroupsSelect = "$select=id,displayName,mail,groupTypes,securityEnabled,membershipRule"
	graphScope        = "https://graph.microsoft.com/.default"
)

// aadGroupsCollector discovers Azure AD groups and their members via the
// Microsoft Graph REST API. It uses direct HTTP calls with an azcore token
// credential rather than the heavy msgraph-sdk-go dependency.
type aadGroupsCollector struct {
	cred azcore.TokenCredential
	// baseURL overrides the Graph API base URL for testing.
	baseURL string
	// httpClient is injectable for testing; defaults to http.DefaultClient.
	httpClient *http.Client
}

func newAADGroupsCollector(cred azcore.TokenCredential) *aadGroupsCollector {
	return &aadGroupsCollector{
		cred:       cred,
		httpClient: http.DefaultClient,
	}
}

func (c *aadGroupsCollector) Name() string { return "azure-aad-groups" }

// Collect lists all AAD groups and their members, emitting group resource
// nodes and bidirectional membership edges.
func (c *aadGroupsCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	token, err := c.cred.GetToken(ctx, policy.TokenRequestOptions{
		Scopes: []string{graphScope},
	})
	if err != nil {
		slog.Warn("azure-aad-groups: cannot get Graph token (skipping)", "err", err)
		return cloud.SubCollectorResult{}, nil
	}

	groups, err := c.listGroups(ctx, token.Token)
	if err != nil {
		return cloud.SubCollectorResult{}, err
	}

	var result cloud.SubCollectorResult
	for _, g := range groups {
		if g.ID == "" {
			continue
		}
		result.Resources = append(result.Resources, groupResourceSpec(g))
		members, err := c.listGroupMembers(ctx, token.Token, g.ID)
		if err != nil {
			slog.Debug("azure-aad-groups: list members", "group", g.ID, "err", err)
			continue
		}
		result.Edges = append(result.Edges, groupMemberEdges(g.ID, members)...)
	}

	return result, nil
}

// listGroups fetches all AAD groups with pagination.
func (c *aadGroupsCollector) listGroups(ctx context.Context, token string) ([]graphGroup, error) {
	url := c.graphURL() + "?" + graphGroupsSelect
	var all []graphGroup
	for url != "" {
		groups, next, err := c.fetchGroupsPage(ctx, token, url)
		if isForbidden(err) {
			slog.Warn("azure-aad-groups: 403 listing groups (skipping)")
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		all = append(all, groups...)
		url = next
	}
	return all, nil
}

// listGroupMembers fetches all members of a group with pagination.
func (c *aadGroupsCollector) listGroupMembers(
	ctx context.Context, token, groupID string,
) ([]graphMember, error) {
	url := c.graphURL() + "/" + groupID + "/members"
	var all []graphMember
	for url != "" {
		members, next, err := c.fetchMembersPage(ctx, token, url)
		if isForbidden(err) {
			slog.Warn("azure-aad-groups: 403 listing members", "group", groupID)
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		all = append(all, members...)
		url = next
	}
	return all, nil
}

// graphURL returns the base Graph groups URL, allowing override for tests.
func (c *aadGroupsCollector) graphURL() string {
	if c.baseURL != "" {
		return c.baseURL
	}
	return graphGroupsURL
}

// graphGroup represents a subset of the Microsoft Graph Group resource.
type graphGroup struct {
	ID              string   `json:"id"`
	DisplayName     string   `json:"displayName"`
	Mail            string   `json:"mail"`
	GroupTypes      []string `json:"groupTypes"`
	SecurityEnabled bool     `json:"securityEnabled"`
	MembershipRule  string   `json:"membershipRule"`
}

// graphMember represents a group member returned by the members endpoint.
type graphMember struct {
	ODataType   string `json:"@odata.type"`
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
}

// groupResourceSpec builds a ResourceSpec for an AAD group.
func groupResourceSpec(g graphGroup) cloud.ResourceSpec {
	content, err := json.Marshal(g)
	if err != nil {
		content = []byte("{}")
	}
	meta := map[string]string{
		"displayName":     g.DisplayName,
		"securityEnabled": fmt.Sprintf("%t", g.SecurityEnabled),
	}
	if g.Mail != "" {
		meta["mail"] = g.Mail
	}
	if g.MembershipRule != "" {
		meta["membershipRule"] = g.MembershipRule
	}
	if len(g.GroupTypes) > 0 {
		data, err := json.Marshal(g.GroupTypes)
		if err != nil {
			data = []byte("[]")
		}
		meta["groupTypes"] = string(data)
	}
	return cloud.ResourceSpec{
		ID:           "azure:aad:group/" + g.ID,
		Name:         g.DisplayName,
		ResourceType: "azure:aad:group",
		Content:      content,
		Metadata:     meta,
	}
}

// groupMemberEdges emits bidirectional membership edges between a group and
// its members. Direction: group -HAS_MEMBER-> member, member -MEMBER_OF-> group.
func groupMemberEdges(groupID string, members []graphMember) []cloud.EdgeSpec {
	groupNodeID := "azure:aad:group/" + groupID
	var edges []cloud.EdgeSpec
	for _, m := range members {
		if m.ID == "" {
			continue
		}
		memberNodeID := aadMemberNodeID(m)
		edges = append(edges,
			cloud.EdgeSpec{
				SourceID:     groupNodeID,
				TargetID:     memberNodeID,
				Relationship: kgtypes.EdgeHasMember,
				Metadata:     map[string]string{"member_type": m.ODataType},
			},
			cloud.EdgeSpec{
				SourceID:     memberNodeID,
				TargetID:     groupNodeID,
				Relationship: kgtypes.EdgeMemberOf,
				Metadata:     map[string]string{"member_type": m.ODataType},
			},
		)
	}
	return edges
}

// aadMemberNodeID returns a node ID for a group member. Users and service
// principals get their AAD object ID; nested groups get the group ID format.
func aadMemberNodeID(m graphMember) string {
	switch m.ODataType {
	case "#microsoft.graph.group":
		return "azure:aad:group/" + m.ID
	default:
		// Users, service principals, and other types use the raw object ID.
		// These may match existing identity nodes via principalId metadata.
		return "azure:aad:principal/" + m.ID
	}
}
