// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"encoding/json"
	"net/url"
	"testing"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

func TestExtractTrustPolicy_PlainJSON(t *testing.T) {
	policy := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::222222222222:role/cross-role"},"Action":"sts:AssumeRole"}]}`
	role := buildIAMRoleNode(t, "my-role", policy)

	tp := extractTrustPolicy(role)
	require.NotNil(t, tp)
	require.Len(t, tp.Statements, 1)
	assert.Equal(t, "Allow", tp.Statements[0].Effect)
	require.NotNil(t, tp.Statements[0].Principal)
	assert.Equal(t, []string{"arn:aws:iam::222222222222:role/cross-role"}, tp.Statements[0].Principal.AWS)
}

func TestExtractTrustPolicy_URLEncoded(t *testing.T) {
	policy := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::222222222222:root"},"Action":"sts:AssumeRole"}]}`
	encoded := url.QueryEscape(policy)
	role := buildIAMRoleNode(t, "my-role", encoded)

	tp := extractTrustPolicy(role)
	require.NotNil(t, tp)
	require.Len(t, tp.Statements, 1)
	assert.Equal(t, []string{"arn:aws:iam::222222222222:root"}, tp.Statements[0].Principal.AWS)
}

func TestExtractTrustPolicy_EmptyContent(t *testing.T) {
	role := &knowledgev1.Node{Id: "arn:aws:iam::111111111111:role/empty", Content: ""}
	assert.Nil(t, extractTrustPolicy(role))
}

func TestExtractTrustPolicy_NoTrustDoc(t *testing.T) {
	role := &knowledgev1.Node{Id: "arn:aws:iam::111111111111:role/no-trust", Content: `{"RoleName":"no-trust"}`}
	assert.Nil(t, extractTrustPolicy(role))
}

func TestTrustPolicyPrincipals_AllowOnly(t *testing.T) {
	policy := &trustPolicy{
		Statements: []trustStatement{
			{Effect: "Allow", Principal: &trustPrincipal{AWS: []string{"arn:aws:iam::222222222222:role/a"}}},
			{Effect: "Deny", Principal: &trustPrincipal{AWS: []string{"arn:aws:iam::333333333333:role/b"}}},
		},
	}
	principals := trustPolicyPrincipals(policy)
	require.Len(t, principals, 1)
	assert.Equal(t, "arn:aws:iam::222222222222:role/a", principals[0])
}

func TestTrustPolicyPrincipals_MultiplePrincipals(t *testing.T) {
	policy := &trustPolicy{
		Statements: []trustStatement{
			{
				Effect: "Allow",
				Principal: &trustPrincipal{AWS: []string{
					"arn:aws:iam::222222222222:role/a",
					"arn:aws:iam::333333333333:user/b",
				}},
			},
		},
	}
	principals := trustPolicyPrincipals(policy)
	assert.Len(t, principals, 2)
}

func TestTrustPolicyPrincipals_NilPolicy(t *testing.T) {
	assert.Nil(t, trustPolicyPrincipals(nil))
}

func TestParseTrustPrincipals_CrossAccount(t *testing.T) {
	policy := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":["arn:aws:iam::222222222222:role/cross","arn:aws:iam::111111111111:role/same"]},"Action":"sts:AssumeRole"}]}`
	role := buildIAMRoleNode(t, "my-role", policy)

	principals := parseTrustPrincipals(role)
	require.Len(t, principals, 2)
	assert.Equal(t, "arn:aws:iam::222222222222:role/cross", principals[0])
	assert.Equal(t, "arn:aws:iam::111111111111:role/same", principals[1])
}

func TestExtractAccountFromARN(t *testing.T) {
	tests := []struct {
		arn  string
		want string
	}{
		{"arn:aws:iam::222222222222:role/cross", "222222222222"},
		{"arn:aws:iam::111111111111:user/admin", "111111111111"},
		{"arn:aws:ec2:us-east-1:333333333333:instance/i-123", "333333333333"},
		{"invalid", ""},
		{"", ""},
	}
	for _, tt := range tests {
		t.Run(tt.arn, func(t *testing.T) {
			assert.Equal(t, tt.want, extractAccountFromARN(tt.arn))
		})
	}
}

func TestDetectCurrentAccount(t *testing.T) {
	roles := []*knowledgev1.Node{
		{Id: "arn:aws:iam::111111111111:role/a"},
		{Id: "arn:aws:iam::111111111111:role/b"},
	}
	assert.Equal(t, "111111111111", detectCurrentAccount(roles))
}

func TestDetectCurrentAccount_Empty(t *testing.T) {
	assert.Empty(t, detectCurrentAccount(nil))
}

func TestTrustPrincipal_StarPrincipal(t *testing.T) {
	raw := `{"Effect":"Allow","Principal":"*","Action":"sts:AssumeRole"}`
	var stmt trustStatement
	require.NoError(t, json.Unmarshal([]byte(raw), &stmt))
	require.NotNil(t, stmt.Principal)
	assert.True(t, stmt.Principal.All)
	assert.Empty(t, stmt.Principal.AWS)
}

func TestTrustPrincipal_SingleString(t *testing.T) {
	raw := `{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::222222222222:role/a"}}`
	var stmt trustStatement
	require.NoError(t, json.Unmarshal([]byte(raw), &stmt))
	require.NotNil(t, stmt.Principal)
	assert.Equal(t, []string{"arn:aws:iam::222222222222:role/a"}, stmt.Principal.AWS)
}

// buildIAMRoleNode creates an iam-role node with a trust policy embedded in Content.
func buildIAMRoleNode(t *testing.T, roleName, trustPolicyDoc string) *knowledgev1.Node {
	t.Helper()
	const accountID = "111111111111"
	content := map[string]any{
		"RoleName":                 roleName,
		"Arn":                      "arn:aws:iam::" + accountID + ":role/" + roleName,
		"AssumeRolePolicyDocument": trustPolicyDoc,
	}
	data, err := json.Marshal(content)
	require.NoError(t, err)

	n := &knowledgev1.Node{
		Id:         "arn:aws:iam::" + accountID + ":role/" + roleName,
		Type:       string(kgtypes.NodeCloudResource),
		SymbolName: roleName,
		Content:    string(data),
	}
	kgtypes.SetValue(n, "resource_type", "iam-role")
	return n
}
