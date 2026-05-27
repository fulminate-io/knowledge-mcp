// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"context"

	cloudidentity "google.golang.org/api/cloudidentity/v1"
)

// cloudIdentityGroupsAPI wraps *cloudidentity.Service to implement groupsAPI.
// Groups are discovered via Search (requires no specific parent parameter
// beyond the query filter), and memberships are listed per group.
type cloudIdentityGroupsAPI struct {
	svc *cloudidentity.Service
}

// SearchGroups lists all groups visible to the credential using the Search
// endpoint. The query `parent == 'customers/my_customer'` is the standard
// discovery query for Workspace-managed groups; if the credential lacks
// Cloud Identity permissions this returns a 403 handled by the caller.
func (a *cloudIdentityGroupsAPI) SearchGroups(ctx context.Context) ([]*groupInfo, error) {
	var groups []*groupInfo

	err := a.svc.Groups.Search().
		Query("parent == 'customers/my_customer'").
		PageSize(200).
		Pages(ctx, func(resp *cloudidentity.SearchGroupsResponse) error {
			for _, g := range resp.Groups {
				groups = append(groups, toGroupInfo(g))
			}
			return nil
		})
	if err != nil {
		return nil, err
	}

	return groups, nil
}

// ListMemberships returns all members of the given group.
func (a *cloudIdentityGroupsAPI) ListMemberships(
	ctx context.Context, groupName string,
) ([]*memberInfo, error) {
	var members []*memberInfo

	err := a.svc.Groups.Memberships.List(groupName).
		PageSize(200).
		Pages(ctx, func(resp *cloudidentity.ListMembershipsResponse) error {
			for _, m := range resp.Memberships {
				members = append(members, toMemberInfo(m))
			}
			return nil
		})
	if err != nil {
		return nil, err
	}

	return members, nil
}

// toGroupInfo converts a Cloud Identity Group to our minimal representation.
func toGroupInfo(g *cloudidentity.Group) *groupInfo {
	email := ""
	if g.GroupKey != nil {
		email = g.GroupKey.Id
	}
	return &groupInfo{
		Name:        g.Name,
		Email:       email,
		DisplayName: g.DisplayName,
		Description: g.Description,
	}
}

// toMemberInfo converts a Cloud Identity Membership to our minimal representation.
func toMemberInfo(m *cloudidentity.Membership) *memberInfo {
	email := ""
	if m.PreferredMemberKey != nil {
		email = m.PreferredMemberKey.Id
	}
	return &memberInfo{
		Email: email,
		Type:  m.Type,
	}
}
