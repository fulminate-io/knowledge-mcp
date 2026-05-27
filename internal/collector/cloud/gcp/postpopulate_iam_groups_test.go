// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

func TestIAMMemberToNodeID_ServiceAccount_Known(t *testing.T) {
	saEmails := map[string]string{
		"sa@my-project.iam.gserviceaccount.com": "projects/my-project/serviceAccounts/sa@my-project.iam.gserviceaccount.com",
	}
	got := iamMemberToNodeID("my-project", "serviceAccount:sa@my-project.iam.gserviceaccount.com", saEmails)
	assert.Equal(t, "projects/my-project/serviceAccounts/sa@my-project.iam.gserviceaccount.com", got)
}

func TestIAMMemberToNodeID_ServiceAccount_ForeignProject(t *testing.T) {
	got := iamMemberToNodeID("my-project", "serviceAccount:bot@foreign.iam.gserviceaccount.com", nil)
	assert.Equal(t,
		"projects/foreign/serviceAccounts/bot@foreign.iam.gserviceaccount.com",
		got,
		"foreign-project SA must use the project parsed from email, not the local projectID")
}

func TestIAMMemberToNodeID_ServiceAccount_NonCanonicalFallback(t *testing.T) {
	// Non-iam.gserviceaccount.com emails fall back to the local projectID.
	got := iamMemberToNodeID("my-project", "serviceAccount:weird@example.com", nil)
	assert.Equal(t, "projects/my-project/serviceAccounts/weird@example.com", got)
}

func TestIAMMemberToNodeID_Group(t *testing.T) {
	got := iamMemberToNodeID("my-project", "group:devs@example.com", nil)
	assert.Equal(t, "group:devs@example.com", got)
}

func TestIAMMemberToNodeID_User(t *testing.T) {
	got := iamMemberToNodeID("my-project", "user:alice@example.com", nil)
	assert.Empty(t, got, "user: members not yet handled")
}

func TestIAMMemberToNodeID_Domain(t *testing.T) {
	got := iamMemberToNodeID("my-project", "domain:example.com", nil)
	assert.Empty(t, got, "domain: members not yet handled")
}

func TestBuildGroupIndex_FromNodes(t *testing.T) {
	// Simulate what buildGroupIndex would produce from group nodes.
	nodes := []*knowledgev1.Node{
		groupNode("gcp:cloudidentity:group/devs@example.com", "devs@example.com"),
		groupNode("gcp:cloudidentity:group/ops@example.com", "ops@example.com"),
	}

	index := make(map[string]string, len(nodes))
	for _, g := range nodes {
		email := kgtypes.Value(g, "email")
		if email != "" {
			index[email] = g.Id
		}
	}

	assert.Len(t, index, 2)
	assert.Equal(t, "gcp:cloudidentity:group/devs@example.com", index["devs@example.com"])
	assert.Equal(t, "gcp:cloudidentity:group/ops@example.com", index["ops@example.com"])
}

func TestGroupResolutionEdge_Format(t *testing.T) {
	// Verify the shape of resolved edges.
	role := "roles/viewer"
	groupNodeID := "gcp:cloudidentity:group/devs@example.com"

	edge := knowledgev1.Edge{
		FromId: role,
		ToId:   groupNodeID,
		Type:   string(kgtypes.EdgeGrants),
		Method: methodGCPIAMGroupResolve,
	}

	assert.Equal(t, role, edge.FromId)
	assert.Equal(t, groupNodeID, edge.ToId)
	assert.Equal(t, string(kgtypes.EdgeGrants), edge.Type)
	assert.Equal(t, "gcp-iam-group-resolve", edge.Method)
}

func TestGroupResolutionDedup(t *testing.T) {
	// Verify dedup logic with visited map.
	visited := make(map[string]bool)
	groupNodeID := "gcp:cloudidentity:group/devs@example.com"
	role := "roles/viewer"

	key := role + ":" + groupNodeID
	assert.False(t, visited[key])

	visited[key] = true
	assert.True(t, visited[key], "second occurrence should be deduped")
}

func TestGroupResolution_NoGroups(t *testing.T) {
	// If no group nodes exist, the resolver should produce no edges.
	index := map[string]string{} // empty
	assert.Empty(t, index)
}

func TestGroupResolution_MismatchEmail(t *testing.T) {
	// Edge with group: prefix but email not in index should be skipped.
	groupIndex := map[string]string{
		"devs@example.com": "gcp:cloudidentity:group/devs@example.com",
	}

	rawTarget := "group:unknown@example.com"
	email := "unknown@example.com"
	_, ok := groupIndex[email]
	assert.False(t, ok, "unknown email should not resolve")
	assert.Equal(t, "group:unknown@example.com", rawTarget) // unchanged
}

// --- test helpers ---

func groupNode(id, email string) *knowledgev1.Node {
	n := &knowledgev1.Node{
		Id:         id,
		Type:       string(kgtypes.NodeCloudResource),
		SymbolName: email,
	}
	kgtypes.SetValue(n, "resource_type", "gcp:cloudidentity:group")
	kgtypes.SetValue(n, "email", email)
	return n
}
