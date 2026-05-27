// SPDX-License-Identifier: Apache-2.0

package linker

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

func TestAwsAccountFromRoleARN(t *testing.T) {
	cases := map[string]string{
		"arn:aws:iam::123456789012:role/my-role":         "123456789012",
		"arn:aws:iam::987654321098:role/path/to/my-role": "987654321098",
		"arn:aws:s3:::bucket":                            "", // not iam
		"":                                               "",
		"arn:aws:iam::":                                  "",
	}
	for input, want := range cases {
		assert.Equal(t, want, awsAccountFromRoleARN(input), "input=%q", input)
	}
}

func TestGCPProjectFromSAEmail(t *testing.T) {
	assert.Equal(t, "my-project", gcpProjectFromSAEmail("sa@my-project.iam.gserviceaccount.com"))
	assert.Empty(t, gcpProjectFromSAEmail("not-an-email"))
	assert.Empty(t, gcpProjectFromSAEmail(""))
}

func TestLinkWorkloadIdentity_IRSAEmitsEdge(t *testing.T) {
	saNode := &knowledgev1.Node{
		Id:         "default/ServiceAccount/myapp-sa",
		Type:       string(kgtypes.NodeCloudResource),
		SymbolName: "myapp-sa",
		Metadata: map[string]string{
			"resource_type": "ServiceAccount",
			"irsa_role_arn": "arn:aws:iam::123456789012:role/myapp-role",
		},
	}

	gc := &fakeGraphCaller{}
	gc.respond = func(tool string, args map[string]any) (kgtools.ToolResult, error) {
		switch tool {
		case "query":
			graph, _ := args["graph"].(string)
			if graph == "cloud" {
				if _, hasType := args["type"]; hasType {
					return jsonResult(t, map[string]any{"nodes": []*knowledgev1.Node{saNode}}), nil
				}
				return jsonResult(t, map[string]any{"graphs": []string{"prod"}}), nil
			}
		}
		return kgtools.ToolResult{Content: []kgtools.ContentBlock{{Type: "text", Text: `{}`}}}, nil
	}
	// The ServiceAccount is a cloud node (→ proxy FROM); the IAM role ARN is not a
	// node anywhere (→ best-effort raw id TO, server ResolveOrProxy parity).
	gc.seedNode("cloud", saNode)

	n, err := LinkWorkloadIdentity(context.Background(), gc, LinkOptions{})
	require.NoError(t, err)
	assert.Equal(t, 1, n)

	require.Len(t, gc.capturedLinks, 1, "expected one composed linkage LINK for IRSA")
	link := gc.capturedLinks[0]
	assert.Equal(t, "WORKLOAD_IDENTITY", link.Relationship)
	assert.Equal(t, "linkage", link.TargetGraph)
	assert.Equal(t, "tier1-irsa", link.Method)
	assert.Equal(t, "proxy:cloud:prod:"+saNode.Id, link.FromID, "cloud SA FROM → cloud proxy")
	assert.Equal(t, "arn:aws:iam::123456789012:role/myapp-role", link.ToID, "IAM role ARN TO stays raw (best-effort)")
}
