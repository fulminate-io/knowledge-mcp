// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// groupsAPI abstracts Cloud Identity group and membership listing for testability.
type groupsAPI interface {
	// SearchGroups returns all groups visible to the credential.
	SearchGroups(ctx context.Context) ([]*groupInfo, error)
	// ListMemberships returns members of the given group.
	ListMemberships(ctx context.Context, groupName string) ([]*memberInfo, error)
}

// groupInfo is a minimal representation of a Cloud Identity group.
type groupInfo struct {
	Name        string // resource name, e.g. "groups/abc123"
	Email       string // group email
	DisplayName string
	Description string
}

// memberInfo is a minimal representation of a Cloud Identity membership.
type memberInfo struct {
	Email string // member email (from PreferredMemberKey.Id)
	Type  string // USER, SERVICE_ACCOUNT, GROUP, etc.
}

// iamGroupsSubCollector discovers Google groups via Cloud Identity API
// and emits EdgeHasMember / EdgeMemberOf edges between groups and members.
type iamGroupsSubCollector struct {
	api       groupsAPI
	projectID string
}

func newIAMGroupsSubCollector(api groupsAPI, projectID string) *iamGroupsSubCollector {
	return &iamGroupsSubCollector{api: api, projectID: projectID}
}

// Name returns the subcollector identifier.
func (c *iamGroupsSubCollector) Name() string { return "gcp-iam-groups" }

// Collect discovers groups and their memberships, emitting group resource nodes
// and bidirectional membership edges.
func (c *iamGroupsSubCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	var result cloud.SubCollectorResult

	groups, err := c.api.SearchGroups(ctx)
	if err != nil {
		if isPermissionDenied(err) {
			slog.Warn("gcp-iam-groups: Cloud Identity permission denied, skipping",
				"project", c.projectID, "err", err)
			return result, nil
		}
		return result, fmt.Errorf("gcp-iam-groups: search groups: %w", err)
	}

	for _, g := range groups {
		resources, edges := c.processGroup(ctx, g)
		result.Resources = append(result.Resources, resources...)
		result.Edges = append(result.Edges, edges...)
	}

	slog.Debug("gcp-iam-groups: collected",
		"groups", len(groups), "resources", len(result.Resources), "edges", len(result.Edges))
	return result, nil
}

// processGroup emits a group resource node and membership edges for one group.
func (c *iamGroupsSubCollector) processGroup(
	ctx context.Context, g *groupInfo,
) ([]cloud.ResourceSpec, []cloud.EdgeSpec) {
	groupNodeID := "gcp:cloudidentity:group/" + g.Email

	content, err := json.Marshal(g)
	if err != nil {
		content = []byte("{}")
	}

	resources := []cloud.ResourceSpec{{
		ID:           groupNodeID,
		Name:         g.Email,
		ResourceType: "gcp:cloudidentity:group",
		Content:      content,
		Metadata: map[string]string{
			"email":         g.Email,
			"display_name":  g.DisplayName,
			"description":   g.Description,
			"resource_name": g.Name,
		},
	}}

	members, err := c.api.ListMemberships(ctx, g.Name)
	if err != nil {
		if isPermissionDenied(err) {
			slog.Warn("gcp-iam-groups: membership listing denied, skipping group",
				"group", g.Email, "err", err)
			return resources, nil
		}
		slog.Warn("gcp-iam-groups: list memberships failed",
			"group", g.Email, "err", err)
		return resources, nil
	}

	var edges []cloud.EdgeSpec
	for _, m := range members {
		memberNodeID := c.memberNodeID(m)
		if memberNodeID == "" {
			continue
		}
		// group -> member (EdgeHasMember)
		edges = append(edges, cloud.EdgeSpec{
			SourceID:     groupNodeID,
			TargetID:     memberNodeID,
			Relationship: kgtypes.EdgeHasMember,
		})
		// member -> group (EdgeMemberOf)
		edges = append(edges, cloud.EdgeSpec{
			SourceID:     memberNodeID,
			TargetID:     groupNodeID,
			Relationship: kgtypes.EdgeMemberOf,
		})
	}

	return resources, edges
}

// memberNodeID returns the graph node ID for a group member based on type.
func (c *iamGroupsSubCollector) memberNodeID(m *memberInfo) string {
	if m.Email == "" {
		return ""
	}
	switch m.Type {
	case "SERVICE_ACCOUNT":
		// Cloud Identity groups are tenant-scoped, not project-scoped. Use
		// the project parsed from the SA email rather than the local
		// projectID; otherwise foreign-project SA members fabricate a path
		// like projects/<local>/serviceAccounts/<foreign-email> that doesn't
		// exist anywhere.
		saProject := projectFromSAEmail(m.Email)
		if saProject == "" {
			saProject = c.projectID
		}
		return saResourceName(saProject, m.Email)
	case "GROUP":
		return "gcp:cloudidentity:group/" + m.Email
	default:
		// USER and other types — use email as identifier.
		return m.Email
	}
}
