// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"encoding/json"
	"testing"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Helper unit tests ---

func TestExtractOIDCIssuer(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{"valid", `{"Identity":{"Oidc":{"Issuer":"https://oidc.eks.us-east-1.amazonaws.com/id/EXAMPLE"}}}`, "https://oidc.eks.us-east-1.amazonaws.com/id/EXAMPLE"},
		{"no identity", `{"Name":"cluster"}`, ""},
		{"no oidc", `{"Identity":{}}`, ""},
		{"null issuer", `{"Identity":{"Oidc":{"Issuer":null}}}`, ""},
		{"empty content", "", ""},
		{"invalid json", "not-json", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, extractOIDCIssuer(tt.content))
		})
	}
}

func TestOIDCProviderARN(t *testing.T) {
	got := oidcProviderARN("111111111111", "https://oidc.eks.us-east-1.amazonaws.com/id/EXAMPLE")
	assert.Equal(t, "arn:aws:iam::111111111111:oidc-provider/oidc.eks.us-east-1.amazonaws.com/id/EXAMPLE", got)
}

func TestIRSASubjectToNodeID(t *testing.T) {
	tests := []struct {
		subject string
		cluster string
		want    string
	}{
		{"system:serviceaccount:default:my-sa", "arn:aws:eks:us-east-1:111:cluster/c1", "default/ServiceAccount/my-sa"},
		{"system:serviceaccount:kube-system:aws-node", "arn:aws:eks:us-east-1:111:cluster/c1", "kube-system/ServiceAccount/aws-node"},
		{"system:serviceaccount:*:*", "arn:aws:eks:us-east-1:111:cluster/c1", "aws:eks:irsa-wildcard/arn:aws:eks:us-east-1:111:cluster/c1/system:serviceaccount:*:*"},
		{"system:serviceaccount:ns:*", "arn:aws:eks:us-east-1:111:cluster/c1", "aws:eks:irsa-wildcard/arn:aws:eks:us-east-1:111:cluster/c1/system:serviceaccount:ns:*"},
		{"invalid", "cluster", "aws:eks:irsa-wildcard/cluster/invalid"},
	}
	for _, tt := range tests {
		t.Run(tt.subject, func(t *testing.T) {
			assert.Equal(t, tt.want, irsaSubjectToNodeID(tt.subject, tt.cluster))
		})
	}
}

func TestExtractIRSASubjects(t *testing.T) {
	issuer := "https://oidc.eks.us-east-1.amazonaws.com/id/EXAMPLE"
	subKey := "oidc.eks.us-east-1.amazonaws.com/id/EXAMPLE:sub"

	tests := []struct {
		name string
		cond map[string]map[string]stringOrSlice
		want []string
	}{
		{
			"StringEquals single",
			map[string]map[string]stringOrSlice{
				"StringEquals": {subKey: {"system:serviceaccount:default:my-sa"}},
			},
			[]string{"system:serviceaccount:default:my-sa"},
		},
		{
			"StringLike wildcard",
			map[string]map[string]stringOrSlice{
				"StringLike": {subKey: {"system:serviceaccount:*:*"}},
			},
			[]string{"system:serviceaccount:*:*"},
		},
		{
			"multiple subjects",
			map[string]map[string]stringOrSlice{
				"StringEquals": {subKey: {"system:serviceaccount:ns1:sa1", "system:serviceaccount:ns2:sa2"}},
			},
			[]string{"system:serviceaccount:ns1:sa1", "system:serviceaccount:ns2:sa2"},
		},
		{
			"non-matching key",
			map[string]map[string]stringOrSlice{
				"StringEquals": {"other-key:sub": {"system:serviceaccount:default:sa"}},
			},
			nil,
		},
		{"nil condition", nil, nil},
		{
			"non-sa value filtered",
			map[string]map[string]stringOrSlice{
				"StringEquals": {subKey: {"sts.amazonaws.com"}},
			},
			nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, extractIRSASubjects(tt.cond, issuer))
		})
	}
}

// --- Trust policy parser extension tests ---

func TestTrustPrincipal_Federated(t *testing.T) {
	raw := `{"Effect":"Allow","Principal":{"Federated":"arn:aws:iam::111111111111:oidc-provider/oidc.eks.us-east-1.amazonaws.com/id/EXAMPLE"}}`
	var stmt trustStatement
	require.NoError(t, json.Unmarshal([]byte(raw), &stmt))
	require.NotNil(t, stmt.Principal)
	assert.Equal(t, []string{"arn:aws:iam::111111111111:oidc-provider/oidc.eks.us-east-1.amazonaws.com/id/EXAMPLE"}, stmt.Principal.Federated)
	assert.Empty(t, stmt.Principal.AWS)
}

func TestTrustPrincipal_FederatedArray(t *testing.T) {
	raw := `{"Effect":"Allow","Principal":{"Federated":["arn:aws:iam::111:oidc-provider/a","arn:aws:iam::222:oidc-provider/b"]}}`
	var stmt trustStatement
	require.NoError(t, json.Unmarshal([]byte(raw), &stmt))
	require.NotNil(t, stmt.Principal)
	assert.Equal(t, []string{"arn:aws:iam::111:oidc-provider/a", "arn:aws:iam::222:oidc-provider/b"}, stmt.Principal.Federated)
}

func TestTrustStatement_Condition(t *testing.T) {
	raw := `{"Effect":"Allow","Principal":{"Federated":"arn:aws:iam::111:oidc-provider/x"},"Condition":{"StringEquals":{"x:sub":"system:serviceaccount:ns:sa"}}}`
	var stmt trustStatement
	require.NoError(t, json.Unmarshal([]byte(raw), &stmt))
	require.NotNil(t, stmt.Condition)
	require.Contains(t, stmt.Condition, "StringEquals")
	assert.Equal(t, stringOrSlice{"system:serviceaccount:ns:sa"}, stmt.Condition["StringEquals"]["x:sub"])
}

func TestTrustStatement_ConditionArray(t *testing.T) {
	raw := `{"Effect":"Allow","Principal":{"Federated":"arn:aws:iam::111:oidc-provider/x"},"Condition":{"StringEquals":{"x:sub":["system:serviceaccount:ns1:sa1","system:serviceaccount:ns2:sa2"]}}}`
	var stmt trustStatement
	require.NoError(t, json.Unmarshal([]byte(raw), &stmt))
	require.NotNil(t, stmt.Condition)
	vals := stmt.Condition["StringEquals"]["x:sub"]
	assert.Equal(t, stringOrSlice{"system:serviceaccount:ns1:sa1", "system:serviceaccount:ns2:sa2"}, vals)
}

func TestTrustPolicy_AWSStillWorks(t *testing.T) {
	// Backward compat: AWS-only trust policy still parses.
	policy := `{"Statement":[{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::222:role/x"}}]}`
	tp, err := parseTrustPolicyJSON([]byte(policy))
	require.NoError(t, err)
	require.Len(t, tp.Statements, 1)
	assert.Equal(t, []string{"arn:aws:iam::222:role/x"}, tp.Statements[0].Principal.AWS)
	assert.Empty(t, tp.Statements[0].Principal.Federated)
}

// --- matchRoleFederatedPrincipals integration tests ---

func TestMatchRoleFederatedPrincipals_Match(t *testing.T) {
	issuer := "https://oidc.eks.us-east-1.amazonaws.com/id/EXAMPLE"
	clusterARN := "arn:aws:eks:us-east-1:111111111111:cluster/my-cluster"
	providerARN := oidcProviderARN("111111111111", issuer)

	oidcMap := map[string]oidcMapping{
		providerARN: {issuerURL: issuer, clusterARN: clusterARN},
	}

	trustDoc := buildFederatedTrustPolicy(providerARN, issuer, "system:serviceaccount:default:my-sa")
	role := buildIAMRoleNode(t, "irsa-role", trustDoc)

	edges := matchRoleFederatedPrincipals(role, oidcMap)
	require.Len(t, edges, 1)
	assert.Equal(t, "default/ServiceAccount/my-sa", edges[0].FromId)
	assert.Equal(t, role.Id, edges[0].ToId)
	assert.Equal(t, string(kgtypes.EdgeWorkloadIdentity), edges[0].Type)
	assert.Equal(t, methodIRSA, edges[0].Method)
}

func TestMatchRoleFederatedPrincipals_NoMatch(t *testing.T) {
	oidcMap := map[string]oidcMapping{
		"arn:aws:iam::111:oidc-provider/other": {issuerURL: "https://other", clusterARN: "arn:cluster"},
	}
	trustDoc := buildFederatedTrustPolicy(
		"arn:aws:iam::111:oidc-provider/nonexistent", "https://nonexistent", "system:serviceaccount:default:sa",
	)
	role := buildIAMRoleNode(t, "no-match-role", trustDoc)

	edges := matchRoleFederatedPrincipals(role, oidcMap)
	assert.Empty(t, edges)
}

func TestMatchRoleFederatedPrincipals_MultipleClusters(t *testing.T) {
	issuer1 := "https://oidc.eks.us-east-1.amazonaws.com/id/AAA"
	issuer2 := "https://oidc.eks.us-west-2.amazonaws.com/id/BBB"
	prov1 := oidcProviderARN("111111111111", issuer1)
	prov2 := oidcProviderARN("111111111111", issuer2)

	oidcMap := map[string]oidcMapping{
		prov1: {issuerURL: issuer1, clusterARN: "arn:aws:eks:us-east-1:111111111111:cluster/c1"},
		prov2: {issuerURL: issuer2, clusterARN: "arn:aws:eks:us-west-2:111111111111:cluster/c2"},
	}

	// Role trusts cluster 2's OIDC provider.
	trustDoc := buildFederatedTrustPolicy(prov2, issuer2, "system:serviceaccount:app:worker")
	role := buildIAMRoleNode(t, "multi-role", trustDoc)

	edges := matchRoleFederatedPrincipals(role, oidcMap)
	require.Len(t, edges, 1)
	assert.Equal(t, "app/ServiceAccount/worker", edges[0].FromId)
}

func TestMatchRoleFederatedPrincipals_WildcardSubject(t *testing.T) {
	issuer := "https://oidc.eks.us-east-1.amazonaws.com/id/WILD"
	clusterARN := "arn:aws:eks:us-east-1:111111111111:cluster/wildcard-cluster"
	provARN := oidcProviderARN("111111111111", issuer)

	oidcMap := map[string]oidcMapping{
		provARN: {issuerURL: issuer, clusterARN: clusterARN},
	}
	trustDoc := buildFederatedTrustPolicy(provARN, issuer, "system:serviceaccount:*:*")
	role := buildIAMRoleNode(t, "wildcard-role", trustDoc)

	edges := matchRoleFederatedPrincipals(role, oidcMap)
	require.Len(t, edges, 1)
	assert.Contains(t, edges[0].FromId, "aws:eks:irsa-wildcard/")
}

func TestMatchRoleFederatedPrincipals_NoTrustPolicy(t *testing.T) {
	oidcMap := map[string]oidcMapping{
		"arn:aws:iam::111:oidc-provider/x": {issuerURL: "https://x", clusterARN: "arn:cluster"},
	}
	role := &knowledgev1.Node{Id: "arn:aws:iam::111111111111:role/empty", Content: ""}
	edges := matchRoleFederatedPrincipals(role, oidcMap)
	assert.Empty(t, edges)
}

// --- Test helper ---

// buildFederatedTrustPolicy creates a JSON trust policy document with a
// Federated principal and a StringEquals condition on the :sub key.
func buildFederatedTrustPolicy(federatedARN, issuerURL, subject string) string {
	issuerKey := issuerURL
	issuerKey = cutPrefix(issuerKey, "https://")
	issuerKey = cutPrefix(issuerKey, "http://")
	subKey := issuerKey + ":sub"

	policy := map[string]any{
		"Version": "2012-10-17",
		"Statement": []map[string]any{
			{
				"Effect":    "Allow",
				"Principal": map[string]any{"Federated": federatedARN},
				"Action":    "sts:AssumeRoleWithWebIdentity",
				"Condition": map[string]any{
					"StringEquals": map[string]any{subKey: subject},
				},
			},
		},
	}
	data, err := json.Marshal(policy)
	if err != nil {
		return "{}"
	}
	return string(data)
}

// cutPrefix removes the prefix from s if present (strings.CutPrefix backport).
func cutPrefix(s, prefix string) string {
	if len(s) >= len(prefix) && s[:len(prefix)] == prefix {
		return s[len(prefix):]
	}
	return s
}
