// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"

	"google.golang.org/api/googleapi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// fakeGroupsAPI implements groupsAPI for testing.
type fakeGroupsAPI struct {
	groups     []*groupInfo
	members    map[string][]*memberInfo // keyed by group resource name
	searchErr  error
	memberErrs map[string]error
}

func (f *fakeGroupsAPI) SearchGroups(_ context.Context) ([]*groupInfo, error) {
	if f.searchErr != nil {
		return nil, f.searchErr
	}
	return f.groups, nil
}

func (f *fakeGroupsAPI) ListMemberships(_ context.Context, groupName string) ([]*memberInfo, error) {
	if f.memberErrs != nil {
		if err, ok := f.memberErrs[groupName]; ok {
			return nil, err
		}
	}
	return f.members[groupName], nil
}

func TestIAMGroupsSubCollector_Name(t *testing.T) {
	c := &iamGroupsSubCollector{}
	assert.Equal(t, "gcp-iam-groups", c.Name())
}

func TestIAMGroupsSubCollector_HappyPath(t *testing.T) {
	api := &fakeGroupsAPI{
		groups: []*groupInfo{
			{Name: "groups/g1", Email: "devs@example.com", DisplayName: "Developers"},
			{Name: "groups/g2", Email: "ops@example.com", DisplayName: "Operations"},
		},
		members: map[string][]*memberInfo{
			"groups/g1": {
				{Email: "alice@example.com", Type: "USER"},
				{Email: "sa@my-project.iam.gserviceaccount.com", Type: "SERVICE_ACCOUNT"},
			},
			"groups/g2": {
				{Email: "bob@example.com", Type: "USER"},
			},
		},
	}

	c := newIAMGroupsSubCollector(api, "my-project")
	result, err := c.Collect(context.Background())
	require.NoError(t, err)

	// 2 group nodes.
	assert.Len(t, result.Resources, 2)
	assert.Equal(t, "gcp:cloudidentity:group/devs@example.com", result.Resources[0].ID)
	assert.Equal(t, "gcp:cloudidentity:group", result.Resources[0].ResourceType)
	assert.Equal(t, "devs@example.com", result.Resources[0].Metadata["email"])

	// group g1: 2 members * 2 edges (HasMember + MemberOf) = 4
	// group g2: 1 member * 2 edges = 2
	// total = 6
	assert.Len(t, result.Edges, 6)

	// Verify edge types and directions for g1.
	hasMemberCount := 0
	memberOfCount := 0
	for _, e := range result.Edges {
		switch e.Relationship {
		case kgtypes.EdgeHasMember:
			hasMemberCount++
		case kgtypes.EdgeMemberOf:
			memberOfCount++
		}
	}
	assert.Equal(t, 3, hasMemberCount) // 2 from g1 + 1 from g2
	assert.Equal(t, 3, memberOfCount)

	// Verify SA member gets project-qualified node ID.
	saEdgeFound := false
	for _, e := range result.Edges {
		if e.Relationship == kgtypes.EdgeHasMember &&
			e.TargetID == "projects/my-project/serviceAccounts/sa@my-project.iam.gserviceaccount.com" {
			saEdgeFound = true
			break
		}
	}
	assert.True(t, saEdgeFound, "service account member should use saResourceName format")
}

func TestIAMGroupsSubCollector_ForeignProjectSAMember(t *testing.T) {
	// Cloud Identity groups are tenant-scoped; a group can include SAs from
	// other projects. The node ID must use the SA's actual project, not
	// the local projectID.
	api := &fakeGroupsAPI{
		groups: []*groupInfo{
			{Name: "groups/g1", Email: "platform@example.com"},
		},
		members: map[string][]*memberInfo{
			"groups/g1": {
				{Email: "bot@foreign.iam.gserviceaccount.com", Type: "SERVICE_ACCOUNT"},
			},
		},
	}
	c := newIAMGroupsSubCollector(api, "my-project")
	result, err := c.Collect(context.Background())
	require.NoError(t, err)

	var foundForeign bool
	for _, e := range result.Edges {
		if e.Relationship == kgtypes.EdgeHasMember &&
			e.TargetID == "projects/foreign/serviceAccounts/bot@foreign.iam.gserviceaccount.com" {
			foundForeign = true
		}
	}
	assert.True(t, foundForeign,
		"foreign-project SA must use parsed project (foreign), not local projectID (my-project)")
}

func TestIAMGroupsSubCollector_PermissionDenied(t *testing.T) {
	api := &fakeGroupsAPI{
		searchErr: &googleapi.Error{Code: http.StatusForbidden, Message: "forbidden"},
	}

	c := newIAMGroupsSubCollector(api, "my-project")
	result, err := c.Collect(context.Background())
	require.NoError(t, err, "403 should not propagate as error")
	assert.Empty(t, result.Resources)
	assert.Empty(t, result.Edges)
}

func TestIAMGroupsSubCollector_OtherError(t *testing.T) {
	api := &fakeGroupsAPI{
		searchErr: fmt.Errorf("network timeout"),
	}

	c := newIAMGroupsSubCollector(api, "my-project")
	_, err := c.Collect(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "network timeout")
}

func TestIAMGroupsSubCollector_MembershipDenied(t *testing.T) {
	api := &fakeGroupsAPI{
		groups: []*groupInfo{
			{Name: "groups/g1", Email: "devs@example.com", DisplayName: "Developers"},
		},
		members: map[string][]*memberInfo{},
		memberErrs: map[string]error{
			"groups/g1": &googleapi.Error{Code: http.StatusForbidden, Message: "forbidden"},
		},
	}

	c := newIAMGroupsSubCollector(api, "my-project")
	result, err := c.Collect(context.Background())
	require.NoError(t, err)

	// Group node should still be emitted even if memberships fail.
	assert.Len(t, result.Resources, 1)
	assert.Empty(t, result.Edges)
}

func TestIAMGroupsSubCollector_EmptyGroups(t *testing.T) {
	api := &fakeGroupsAPI{groups: nil}

	c := newIAMGroupsSubCollector(api, "my-project")
	result, err := c.Collect(context.Background())
	require.NoError(t, err)
	assert.Empty(t, result.Resources)
	assert.Empty(t, result.Edges)
}

func TestIAMGroupsSubCollector_GroupMember(t *testing.T) {
	// A group that contains another group as a member.
	api := &fakeGroupsAPI{
		groups: []*groupInfo{
			{Name: "groups/g1", Email: "parent@example.com"},
		},
		members: map[string][]*memberInfo{
			"groups/g1": {
				{Email: "child@example.com", Type: "GROUP"},
			},
		},
	}

	c := newIAMGroupsSubCollector(api, "my-project")
	result, err := c.Collect(context.Background())
	require.NoError(t, err)

	// The child group member should get a group-formatted node ID.
	childGroupID := "gcp:cloudidentity:group/child@example.com"
	found := false
	for _, e := range result.Edges {
		if e.Relationship == kgtypes.EdgeHasMember && e.TargetID == childGroupID {
			found = true
			break
		}
	}
	assert.True(t, found, "nested group member should use group node ID format")
}

func TestIAMGroupsSubCollector_EmptyEmail(t *testing.T) {
	api := &fakeGroupsAPI{
		groups: []*groupInfo{
			{Name: "groups/g1", Email: "devs@example.com"},
		},
		members: map[string][]*memberInfo{
			"groups/g1": {
				{Email: "", Type: "USER"}, // no email — should be skipped
			},
		},
	}

	c := newIAMGroupsSubCollector(api, "my-project")
	result, err := c.Collect(context.Background())
	require.NoError(t, err)

	assert.Len(t, result.Resources, 1)
	assert.Empty(t, result.Edges, "member with empty email should be skipped")
}

func TestIsPermissionDenied(t *testing.T) {
	t.Run("googleapi 403 → true", func(t *testing.T) {
		assert.True(t, isPermissionDenied(&googleapi.Error{Code: http.StatusForbidden}))
	})
	t.Run("googleapi 404 → false", func(t *testing.T) {
		assert.False(t, isPermissionDenied(&googleapi.Error{Code: http.StatusNotFound}))
	})
	t.Run("grpc PermissionDenied → true", func(t *testing.T) {
		assert.True(t, isPermissionDenied(status.Error(codes.PermissionDenied, "forbidden")))
	})
	t.Run("grpc NotFound → false", func(t *testing.T) {
		assert.False(t, isPermissionDenied(status.Error(codes.NotFound, "missing")))
	})
	t.Run("generic error → false", func(t *testing.T) {
		assert.False(t, isPermissionDenied(fmt.Errorf("generic error")))
	})
	t.Run("nil → false", func(t *testing.T) {
		assert.False(t, isPermissionDenied(nil))
	})
}
