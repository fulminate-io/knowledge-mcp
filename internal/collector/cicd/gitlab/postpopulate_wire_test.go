// SPDX-License-Identifier: Apache-2.0

package gitlab

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/enginetest"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// fakeGraphCaller is a package-local postpopulate.GraphCaller for the GitLab
// PostPopulate family. It resolves cloud reads by Target.Account (a miss on an
// empty/unknown account) and records every create_batch mutation so a test can
// assert the federation-edge write routes into the cicd graph by Target.Account
// (NOT Name). It replaces the real store.DB the resolver used to take — no
// store.Init / store.Store().
type fakeGraphCaller struct {
	// nodesByAccount[account] = the cloud nodes present in that cloud account
	// graph (IAM roles / federated identities scanned for the GitLab issuer).
	nodesByAccount map[string][]*knowledgev1.Node

	mutations []*knowledgev1.ExecuteRequest
}

func (f *fakeGraphCaller) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	if m := req.GetMutation(); m != nil {
		_ = m
		f.mutations = append(f.mutations, req)
		return &knowledgev1.ExecuteResponse{}, nil
	}
	q := req.GetQuery()
	if q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_GRAPH_NAMES {
		return f.graphNames(), nil
	}
	return f.browse(req.GetTarget(), q), nil
}

// graphNames enumerates every cloud account graph as a GraphInfo carrier.
func (f *fakeGraphCaller) graphNames() *knowledgev1.ExecuteResponse {
	var infos []*knowledgev1.GraphInfo
	for acct := range f.nodesByAccount {
		infos = append(infos, &knowledgev1.GraphInfo{Name: acct})
	}
	return &knowledgev1.ExecuteResponse{GraphNames: infos}
}

// browse serves a type-browse keyed by Target.Account. The GitLab scan filters
// resource_type in-code (isOIDCRelevantType), so this only honors the node-type
// predicate. An empty/unknown account is a miss.
func (f *fakeGraphCaller) browse(tgt *knowledgev1.GraphSelector, q *knowledgev1.QueryPlan) *knowledgev1.ExecuteResponse {
	acct := tgt.GetAccount()
	if acct == "" {
		return &knowledgev1.ExecuteResponse{}
	}
	wantType := q.GetSelection().GetNodeType()

	var matched []*knowledgev1.Node
	for _, n := range f.nodesByAccount[acct] {
		if wantType != "" && n.Type != wantType {
			continue
		}
		matched = append(matched, n)
	}
	return enginetest.ResponseWithNodes(matched...)
}

// createBatchTargets returns the Target selector of every captured create_batch
// mutation.
func (f *fakeGraphCaller) createBatchTargets() []*knowledgev1.GraphSelector {
	var out []*knowledgev1.GraphSelector
	for _, req := range f.mutations {
		if req.GetMutation().GetKind() == knowledgev1.MutationPlan_MUTATION_KIND_CREATE {
			out = append(out, req.GetTarget())
		}
	}
	return out
}

// createBatchEdgeToIDs returns the to_id of every edge in the captured
// create_batch mutations.
func (f *fakeGraphCaller) createBatchEdgeToIDs() []string {
	var out []string
	for _, req := range f.mutations {
		m := req.GetMutation()
		if m.GetKind() != knowledgev1.MutationPlan_MUTATION_KIND_CREATE {
			continue
		}
		for _, e := range m.GetEdges() {
			out = append(out, e.GetToId())
		}
	}
	return out
}

// TestGitLabPostPopulate_RoutesByAccount drives the real gitlabPostPopulate
// through the wire fake: a cloud IAM role whose content references the GitLab
// OIDC issuer must produce an EdgeFederates edge written into the cicd GitLab
// graph by Target.Account == "gitlab-<group>" (NOT Target.Name) — the FUL-288
// selector-field invariant for the cicd family.
func TestGitLabPostPopulate_RoutesByAccount(t *testing.T) {
	t.Setenv("GITLAB_URL", "")

	const (
		cloudAcct = "123456789012"
		group     = "acme"
		cicdGraph = "gitlab-" + group
		roleARN   = "arn:aws:iam::123456789012:role/gitlab-deploy"
	)

	issuer := gitlabOIDCIssuer() // https://gitlab.com
	role := &knowledgev1.Node{
		Id:         roleARN,
		Type:       string(kgtypes.NodeCloudResource),
		SymbolName: "gitlab-deploy",
		Content:    `{"trust":"` + issuer + `/oidc"}`,
	}
	kgtypes.SetValue(role, "resource_type", "iam-role")

	fc := &fakeGraphCaller{nodesByAccount: map[string][]*knowledgev1.Node{
		cloudAcct: {role},
	}}

	require.NoError(t, gitlabPostPopulate(context.Background(), fc, cicdGraph))

	tgts := fc.createBatchTargets()
	require.NotEmpty(t, tgts, "expected at least one create_batch federation-edge mutation")
	for _, tgt := range tgts {
		assert.Equal(t, "cicd", tgt.GetGraph(), "federation edge write must target the cicd graph")
		assert.Equal(t, cicdGraph, tgt.GetAccount(), "gitlab OIDC edges must route by Account==%s (NOT Name)", cicdGraph)
		assert.Empty(t, tgt.GetName(), "cicd write must NOT route by Name")
	}
	assert.Contains(t, fc.createBatchEdgeToIDs(), roleARN,
		"the federation edge must point at the trusted IAM role")
}

// TestGitLabPostPopulate_NonGitLabGraph_NoOp confirms a cicd graph whose name
// does not carry the "gitlab-" prefix (e.g. a github graph) is a silent no-op —
// no cloud scan, no write.
func TestGitLabPostPopulate_NonGitLabGraph_NoOp(t *testing.T) {
	t.Setenv("GITLAB_URL", "")

	const cloudAcct = "123456789012"
	issuer := gitlabOIDCIssuer()
	role := &knowledgev1.Node{
		Id:         "arn:aws:iam::123456789012:role/r",
		Type:       string(kgtypes.NodeCloudResource),
		SymbolName: "r",
		Content:    `{"trust":"` + issuer + `/oidc"}`,
	}
	kgtypes.SetValue(role, "resource_type", "iam-role")
	fc := &fakeGraphCaller{nodesByAccount: map[string][]*knowledgev1.Node{
		cloudAcct: {role},
	}}

	require.NoError(t, gitlabPostPopulate(context.Background(), fc, "myorg"))
	assert.Empty(t, fc.createBatchTargets(), "non-gitlab cicd graph must not write any federation edges")
}

// TestGitLabPostPopulate_NoMatch_NoWrite confirms that when no cloud resource
// references the GitLab OIDC issuer, no create_batch mutation is fired (the
// empty edges no-op).
func TestGitLabPostPopulate_NoMatch_NoWrite(t *testing.T) {
	t.Setenv("GITLAB_URL", "")

	const (
		cloudAcct = "123456789012"
		cicdGraph = "gitlab-acme"
	)
	role := &knowledgev1.Node{
		Id:         "arn:aws:iam::123456789012:role/other",
		Type:       string(kgtypes.NodeCloudResource),
		SymbolName: "other",
		Content:    `{"trust":"https://example.com/oidc"}`,
	}
	kgtypes.SetValue(role, "resource_type", "iam-role")
	fc := &fakeGraphCaller{nodesByAccount: map[string][]*knowledgev1.Node{
		cloudAcct: {role},
	}}

	require.NoError(t, gitlabPostPopulate(context.Background(), fc, cicdGraph))
	assert.Empty(t, fc.createBatchTargets(), "no trusting resource → no federation-edge write")
}

// TestGitLabPostPopulate_IrrelevantType_NoWrite confirms a resource that
// references the issuer but whose resource_type is not OIDC-relevant is skipped
// (isOIDCRelevantType gate).
func TestGitLabPostPopulate_IrrelevantType_NoWrite(t *testing.T) {
	t.Setenv("GITLAB_URL", "")

	const (
		cloudAcct = "123456789012"
		cicdGraph = "gitlab-acme"
	)
	issuer := gitlabOIDCIssuer()
	bucket := &knowledgev1.Node{
		Id:         "arn:aws:s3:::gitlab-bucket",
		Type:       string(kgtypes.NodeCloudResource),
		SymbolName: "gitlab-bucket",
		Content:    `{"note":"` + issuer + `"}`,
	}
	kgtypes.SetValue(bucket, "resource_type", "s3-bucket")
	fc := &fakeGraphCaller{nodesByAccount: map[string][]*knowledgev1.Node{
		cloudAcct: {bucket},
	}}

	require.NoError(t, gitlabPostPopulate(context.Background(), fc, cicdGraph))
	assert.Empty(t, fc.createBatchTargets(), "non-OIDC-relevant resource type must not produce a federation edge")
}
