// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// fakeTokenCredential returns a fixed token for testing.
type fakeTokenCredential struct{}

func (f *fakeTokenCredential) GetToken(_ context.Context, _ policy.TokenRequestOptions) (azcore.AccessToken, error) {
	return azcore.AccessToken{Token: "fake-token"}, nil
}

func TestAADGroupsCollect_HappyPath(t *testing.T) {
	groups := []graphGroup{
		{ID: "g1", DisplayName: "Engineering", SecurityEnabled: true},
		{ID: "g2", DisplayName: "Marketing", Mail: "mktg@contoso.com"},
	}
	membersG1 := []graphMember{
		{ODataType: "#microsoft.graph.user", ID: "u1", DisplayName: "Alice"},
		{ODataType: "#microsoft.graph.servicePrincipal", ID: "sp1", DisplayName: "App1"},
	}
	membersG2 := []graphMember{
		{ODataType: "#microsoft.graph.user", ID: "u2", DisplayName: "Bob"},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer fake-token", r.Header.Get("Authorization"))

		switch r.URL.Path {
		case "/groups":
			writeJSON(t, w, map[string]any{"value": groups})
		case "/groups/g1/members":
			writeJSON(t, w, map[string]any{"value": membersG1})
		case "/groups/g2/members":
			writeJSON(t, w, map[string]any{"value": membersG2})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := &aadGroupsCollector{
		cred:       &fakeTokenCredential{},
		baseURL:    srv.URL + "/groups",
		httpClient: srv.Client(),
	}
	result, err := c.Collect(context.Background())
	require.NoError(t, err)

	// 2 groups
	require.Len(t, result.Resources, 2)
	assert.Equal(t, "azure:aad:group/g1", result.Resources[0].ID)
	assert.Equal(t, "azure:aad:group", result.Resources[0].ResourceType)
	assert.Equal(t, "Engineering", result.Resources[0].Name)

	// g1 has 2 members (4 edges: 2 HAS_MEMBER + 2 MEMBER_OF)
	// g2 has 1 member (2 edges: 1 HAS_MEMBER + 1 MEMBER_OF)
	assert.Len(t, result.Edges, 6)

	// Check edge types
	var hasMember, memberOf int
	for _, e := range result.Edges {
		switch e.Relationship {
		case kgtypes.EdgeHasMember:
			hasMember++
		case kgtypes.EdgeMemberOf:
			memberOf++
		}
	}
	assert.Equal(t, 3, hasMember)
	assert.Equal(t, 3, memberOf)
}

func TestAADGroupsCollect_Pagination(t *testing.T) {
	page1Groups := []graphGroup{{ID: "g1", DisplayName: "Group1"}}
	page2Groups := []graphGroup{{ID: "g2", DisplayName: "Group2"}}
	callCount := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/groups" && callCount == 0:
			callCount++
			writeJSON(t, w, map[string]any{
				"value":           page1Groups,
				"@odata.nextLink": "http://" + r.Host + "/groups?page=2",
			})
		case r.URL.Path == "/groups" && r.URL.Query().Get("page") == "2":
			writeJSON(t, w, map[string]any{"value": page2Groups})
		case r.URL.Path == "/groups/g1/members",
			r.URL.Path == "/groups/g2/members":
			writeJSON(t, w, map[string]any{"value": []graphMember{}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := &aadGroupsCollector{
		cred:       &fakeTokenCredential{},
		baseURL:    srv.URL + "/groups",
		httpClient: srv.Client(),
	}
	result, err := c.Collect(context.Background())
	require.NoError(t, err)
	require.Len(t, result.Resources, 2)
	assert.Equal(t, "azure:aad:group/g1", result.Resources[0].ID)
	assert.Equal(t, "azure:aad:group/g2", result.Resources[1].ID)
}

func TestAADGroupsCollect_Forbidden(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	c := &aadGroupsCollector{
		cred:       &fakeTokenCredential{},
		baseURL:    srv.URL + "/groups",
		httpClient: srv.Client(),
	}
	result, err := c.Collect(context.Background())
	require.NoError(t, err)
	assert.Empty(t, result.Resources)
	assert.Empty(t, result.Edges)
}

func TestAADGroupsCollect_NestedGroup(t *testing.T) {
	groups := []graphGroup{{ID: "parent", DisplayName: "ParentGroup"}}
	members := []graphMember{
		{ODataType: "#microsoft.graph.group", ID: "child", DisplayName: "ChildGroup"},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/groups":
			writeJSON(t, w, map[string]any{"value": groups})
		case "/groups/parent/members":
			writeJSON(t, w, map[string]any{"value": members})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := &aadGroupsCollector{
		cred:       &fakeTokenCredential{},
		baseURL:    srv.URL + "/groups",
		httpClient: srv.Client(),
	}
	result, err := c.Collect(context.Background())
	require.NoError(t, err)

	// Nested group member should use group ID format.
	require.Len(t, result.Edges, 2)
	assert.Equal(t, "azure:aad:group/child", result.Edges[0].TargetID) // HAS_MEMBER
	assert.Equal(t, "azure:aad:group/child", result.Edges[1].SourceID) // MEMBER_OF
}

func TestAADGroupsCollect_EmptyID(t *testing.T) {
	groups := []graphGroup{{ID: "", DisplayName: "NoID"}}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/groups" {
			writeJSON(t, w, map[string]any{"value": groups})
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	c := &aadGroupsCollector{
		cred:       &fakeTokenCredential{},
		baseURL:    srv.URL + "/groups",
		httpClient: srv.Client(),
	}
	result, err := c.Collect(context.Background())
	require.NoError(t, err)
	assert.Empty(t, result.Resources)
}

func TestGroupResourceSpec(t *testing.T) {
	g := graphGroup{
		ID:              "abc123",
		DisplayName:     "Engineering",
		Mail:            "eng@contoso.com",
		GroupTypes:      []string{"Unified"},
		SecurityEnabled: true,
		MembershipRule:  "user.department -eq \"Eng\"",
	}
	spec := groupResourceSpec(g)
	assert.Equal(t, "azure:aad:group/abc123", spec.ID)
	assert.Equal(t, "Engineering", spec.Name)
	assert.Equal(t, "azure:aad:group", spec.ResourceType)
	assert.Equal(t, "eng@contoso.com", spec.Metadata["mail"])
	assert.Equal(t, "true", spec.Metadata["securityEnabled"])
	assert.Contains(t, spec.Metadata["membershipRule"], "Eng")
}

func TestGroupMemberEdges(t *testing.T) {
	members := []graphMember{
		{ODataType: "#microsoft.graph.user", ID: "u1"},
		{ID: ""}, // should be skipped
	}
	edges := groupMemberEdges("g1", members)
	require.Len(t, edges, 2) // 1 valid member * 2 directions
	assert.Equal(t, kgtypes.EdgeHasMember, edges[0].Relationship)
	assert.Equal(t, "azure:aad:group/g1", edges[0].SourceID)
	assert.Equal(t, "azure:aad:principal/u1", edges[0].TargetID)
	assert.Equal(t, kgtypes.EdgeMemberOf, edges[1].Relationship)
	assert.Equal(t, "azure:aad:principal/u1", edges[1].SourceID)
}

func TestAADMemberNodeID(t *testing.T) {
	assert.Equal(t, "azure:aad:group/g1", aadMemberNodeID(graphMember{
		ODataType: "#microsoft.graph.group", ID: "g1",
	}))
	assert.Equal(t, "azure:aad:principal/u1", aadMemberNodeID(graphMember{
		ODataType: "#microsoft.graph.user", ID: "u1",
	}))
	assert.Equal(t, "azure:aad:principal/sp1", aadMemberNodeID(graphMember{
		ODataType: "#microsoft.graph.servicePrincipal", ID: "sp1",
	}))
}

// writeJSON is a test helper that writes a JSON response.
func writeJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	require.NoError(t, json.NewEncoder(w).Encode(v))
}
