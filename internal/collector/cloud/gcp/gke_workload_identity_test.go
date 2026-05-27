// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"context"
	"fmt"
	"testing"

	iampb "cloud.google.com/go/iam/apiv1/iampb"
	gax "github.com/googleapis/gax-go/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// fakeIAMPolicyClient implements saIAMPolicyGetter for tests.
type fakeIAMPolicyClient struct {
	policies map[string]*iampb.Policy
	err      error
}

func (f *fakeIAMPolicyClient) GetIamPolicy(
	_ context.Context,
	req *iampb.GetIamPolicyRequest,
	_ ...gax.CallOption,
) (*iampb.Policy, error) {
	if f.err != nil {
		return nil, f.err
	}
	p, ok := f.policies[req.GetResource()]
	if !ok {
		return &iampb.Policy{}, nil
	}
	return p, nil
}

func TestParseWorkloadIdentityMember(t *testing.T) {
	tests := []struct {
		name    string
		member  string
		wantNS  string
		wantKSA string
		wantOK  bool
	}{
		{
			name:    "valid GKE WI member",
			member:  "serviceAccount:my-project.svc.id.goog[default/my-ksa]",
			wantNS:  "default",
			wantKSA: "my-ksa",
			wantOK:  true,
		},
		{
			name:    "valid with custom namespace",
			member:  "serviceAccount:prod-proj.svc.id.goog[kube-system/dns-sa]",
			wantNS:  "kube-system",
			wantKSA: "dns-sa",
			wantOK:  true,
		},
		{
			name:   "wrong prefix (user: instead of serviceAccount:)",
			member: "user:foo.svc.id.goog[ns/sa]",
			wantOK: false,
		},
		{
			name:   "no square brackets",
			member: "serviceAccount:foo@bar.iam.gserviceaccount.com",
			wantOK: false,
		},
		{
			name:   "missing closing bracket",
			member: "serviceAccount:proj.svc.id.goog[ns/sa",
			wantOK: false,
		},
		{
			name:   "empty namespace",
			member: "serviceAccount:proj.svc.id.goog[/sa]",
			wantOK: false,
		},
		{
			name:   "empty ksa name",
			member: "serviceAccount:proj.svc.id.goog[ns/]",
			wantOK: false,
		},
		{
			name:   "no slash in payload",
			member: "serviceAccount:proj.svc.id.goog[just-a-name]",
			wantOK: false,
		},
		{
			name:   "empty string",
			member: "",
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ns, ksa, ok := parseWorkloadIdentityMember(tt.member)
			assert.Equal(t, tt.wantOK, ok, "ok mismatch")
			if ok {
				assert.Equal(t, tt.wantNS, ns)
				assert.Equal(t, tt.wantKSA, ksa)
			}
		})
	}
}

func TestGKEWorkloadIdentityEdges_HappyPath(t *testing.T) {
	gsaEmail := "wi-sa@my-project.iam.gserviceaccount.com"
	gsaResource := "projects/my-project/serviceAccounts/" + gsaEmail

	fake := &fakeIAMPolicyClient{
		policies: map[string]*iampb.Policy{
			gsaResource: {
				Bindings: []*iampb.Binding{
					{
						Role: "roles/iam.workloadIdentityUser",
						Members: []string{
							"serviceAccount:my-project.svc.id.goog[default/app-sa]",
						},
					},
				},
			},
		},
	}

	edges := gkeWorkloadIdentityEdges(context.Background(), fake, "my-project", []string{gsaEmail})

	require.Len(t, edges, 1)
	e := edges[0]
	assert.Equal(t, "default/ServiceAccount/app-sa", e.SourceID)
	assert.Equal(t, gsaResource, e.TargetID)
	assert.Equal(t, kgtypes.EdgeWorkloadIdentity, e.Relationship)
	assert.Equal(t, "my-project", e.Metadata["gcp_project"])
	assert.Equal(t, "roles/iam.workloadIdentityUser", e.Metadata["role"])
}

func TestGKEWorkloadIdentityEdges_DifferentRole(t *testing.T) {
	gsaEmail := "sa@proj.iam.gserviceaccount.com"
	gsaResource := "projects/proj/serviceAccounts/" + gsaEmail

	fake := &fakeIAMPolicyClient{
		policies: map[string]*iampb.Policy{
			gsaResource: {
				Bindings: []*iampb.Binding{
					{
						Role:    "roles/editor",
						Members: []string{"serviceAccount:proj.svc.id.goog[ns/ksa]"},
					},
				},
			},
		},
	}

	edges := gkeWorkloadIdentityEdges(context.Background(), fake, "proj", []string{gsaEmail})
	assert.Empty(t, edges)
}

func TestGKEWorkloadIdentityEdges_NonMatchingMember(t *testing.T) {
	gsaEmail := "sa@proj.iam.gserviceaccount.com"
	gsaResource := "projects/proj/serviceAccounts/" + gsaEmail

	fake := &fakeIAMPolicyClient{
		policies: map[string]*iampb.Policy{
			gsaResource: {
				Bindings: []*iampb.Binding{
					{
						Role: "roles/iam.workloadIdentityUser",
						Members: []string{
							"user:someone@example.com",
						},
					},
				},
			},
		},
	}

	edges := gkeWorkloadIdentityEdges(context.Background(), fake, "proj", []string{gsaEmail})
	assert.Empty(t, edges)
}

func TestGKEWorkloadIdentityEdges_MultipleBindings(t *testing.T) {
	gsaEmail := "shared@proj.iam.gserviceaccount.com"
	gsaResource := "projects/proj/serviceAccounts/" + gsaEmail

	fake := &fakeIAMPolicyClient{
		policies: map[string]*iampb.Policy{
			gsaResource: {
				Bindings: []*iampb.Binding{
					{
						Role: "roles/iam.workloadIdentityUser",
						Members: []string{
							"serviceAccount:proj.svc.id.goog[ns1/sa1]",
							"serviceAccount:proj.svc.id.goog[ns2/sa2]",
						},
					},
					{
						Role:    "roles/viewer",
						Members: []string{"serviceAccount:proj.svc.id.goog[ns3/sa3]"},
					},
				},
			},
		},
	}

	edges := gkeWorkloadIdentityEdges(context.Background(), fake, "proj", []string{gsaEmail})

	require.Len(t, edges, 2)
	assert.Equal(t, "ns1/ServiceAccount/sa1", edges[0].SourceID)
	assert.Equal(t, gsaResource, edges[0].TargetID)
	assert.Equal(t, "ns2/ServiceAccount/sa2", edges[1].SourceID)
	assert.Equal(t, gsaResource, edges[1].TargetID)
}

func TestGKEWorkloadIdentityEdges_IAMError(t *testing.T) {
	fake := &fakeIAMPolicyClient{err: fmt.Errorf("permission denied")}

	edges := gkeWorkloadIdentityEdges(
		context.Background(), fake, "proj",
		[]string{"sa@proj.iam.gserviceaccount.com"},
	)
	assert.Empty(t, edges)
}

func TestGKEWorkloadIdentityEdges_EmptySAList(t *testing.T) {
	fake := &fakeIAMPolicyClient{}
	edges := gkeWorkloadIdentityEdges(context.Background(), fake, "proj", nil)
	assert.Empty(t, edges)
}
