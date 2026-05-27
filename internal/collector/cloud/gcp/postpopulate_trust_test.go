// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"testing"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"

	"github.com/stretchr/testify/assert"
)

func TestProjectFromSAEmail(t *testing.T) {
	tests := []struct {
		name  string
		email string
		want  string
	}{
		{
			name:  "standard SA email",
			email: "my-sa@project-a.iam.gserviceaccount.com",
			want:  "project-a",
		},
		{
			name:  "long project name",
			email: "default@my-long-project-name.iam.gserviceaccount.com",
			want:  "my-long-project-name",
		},
		{
			name:  "empty",
			email: "",
			want:  "",
		},
		{
			name:  "no at sign",
			email: "noemail",
			want:  "",
		},
		{
			name:  "regular email, not SA",
			email: "user@example.com",
			want:  "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, projectFromSAEmail(tt.email))
		})
	}
}

func TestProjectFromSAResourceName(t *testing.T) {
	tests := []struct {
		name string
		id   string
		want string
	}{
		{
			name: "standard",
			id:   "projects/my-project/serviceAccounts/sa@my-project.iam.gserviceaccount.com",
			want: "my-project",
		},
		{
			name: "not a project path",
			id:   "some-other-id",
			want: "",
		},
		{
			name: "projects prefix only",
			id:   "projects/",
			want: "",
		},
		{
			name: "projects with no slash after",
			id:   "projects/my-project",
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, projectFromSAResourceName(tt.id))
		})
	}
}

func TestDetectProjectFromSAs_HappyPath(t *testing.T) {
	sas := []stubNode{
		{id: "projects/my-project/serviceAccounts/sa@my-project.iam.gserviceaccount.com"},
		{id: "projects/my-project/serviceAccounts/other@my-project.iam.gserviceaccount.com"},
	}
	nodes := stubNodesToStoreNodes(sas)
	assert.Equal(t, "my-project", detectProjectFromSAs(nodes))
}

func TestDetectProjectFromSAs_Empty(t *testing.T) {
	assert.Empty(t, detectProjectFromSAs(nil))
}

func TestEdgeMetaRole(t *testing.T) {
	tests := []struct {
		name     string
		evidence string
		want     string
	}{
		{
			name:     "valid JSON with role_name",
			evidence: `{"role_name":"roles/iam.serviceAccountTokenCreator"}`,
			want:     "roles/iam.serviceAccountTokenCreator",
		},
		{
			name:     "empty evidence",
			evidence: "",
			want:     "",
		},
		{
			name:     "invalid JSON",
			evidence: "not-json",
			want:     "",
		},
		{
			name:     "JSON without role_name",
			evidence: `{"other":"value"}`,
			want:     "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := storeEdge(tt.evidence)
			assert.Equal(t, tt.want, edgeMetaRole(&e))
		})
	}
}

func TestImpersonationRoles(t *testing.T) {
	assert.True(t, impersonationRoles["roles/iam.serviceAccountTokenCreator"])
	assert.True(t, impersonationRoles["roles/iam.serviceAccountUser"])
	assert.False(t, impersonationRoles["roles/iam.serviceAccountAdmin"])
	assert.False(t, impersonationRoles["roles/editor"])
}

// --- test helpers ---

type stubNode struct {
	id string
}

func stubNodesToStoreNodes(stubs []stubNode) []*knowledgev1.Node {
	nodes := make([]*knowledgev1.Node, len(stubs))
	for i, s := range stubs {
		nodes[i] = &knowledgev1.Node{Id: s.id}
	}
	return nodes
}

func storeEdge(evidence string) knowledgev1.Edge {
	return knowledgev1.Edge{Evidence: evidence}
}
