// SPDX-License-Identifier: Apache-2.0

package linker

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

func TestExtractChartName_NameField(t *testing.T) {
	content := "apiVersion: v2\nname: fulminate\nversion: 0.1.0\n"
	assert.Equal(t, "fulminate", extractChartName(content))
}

func TestExtractChartName_QuotedValue(t *testing.T) {
	content := `name: "my-chart"`
	assert.Equal(t, "my-chart", extractChartName(content))
}

func TestStripChartVersion(t *testing.T) {
	assert.Equal(t, "fulminate", stripChartVersion("fulminate-0.1.0"))
	assert.Equal(t, "fulminate", stripChartVersion("fulminate-1.0"))
	assert.Equal(t, "fulminate-noversion", stripChartVersion("fulminate-noversion"))
}

// TestLinkHelmCharts_EmitsDeploysEdge wires a fake graphCaller through a
// full LinkHelmCharts run: one code graph holds a Chart.yaml for chart
// "myapp"; one cloud graph holds a Deployment with the
// app.kubernetes.io/name=myapp label. The linker must emit one
// mutate(link, relationship:"DEPLOYS", link_graph:"linkage").
func TestLinkHelmCharts_EmitsDeploysEdge(t *testing.T) {
	chartNode := &knowledgev1.Node{
		Id:       "myapp/charts/myapp/Chart.yaml",
		Type:     string(kgtypes.NodeFile),
		FilePath: "charts/myapp/Chart.yaml",
		Content:  "name: myapp\nversion: 0.1.0\n",
	}
	cloudResource := &knowledgev1.Node{
		Id:         "default/Deployment/myapp-server",
		Type:       string(kgtypes.NodeCloudResource),
		SymbolName: "myapp-server",
		Metadata: map[string]string{
			"resource_type":                "Deployment",
			"label/app.kubernetes.io/name": "myapp",
		},
	}

	gc := &fakeGraphCaller{}
	gc.respond = func(tool string, args map[string]any) (kgtools.ToolResult, error) {
		switch tool {
		case "query":
			graph, _ := args["graph"].(string)
			switch graph {
			case "code":
				if _, hasType := args["type"]; hasType {
					return jsonResult(t, map[string]any{"nodes": []*knowledgev1.Node{chartNode}}), nil
				}
				return jsonResult(t, map[string]any{"graphs": []string{"myapp"}}), nil
			case "cloud":
				if _, hasType := args["type"]; hasType {
					return jsonResult(t, map[string]any{"nodes": []*knowledgev1.Node{cloudResource}}), nil
				}
				return jsonResult(t, map[string]any{"graphs": []string{"prod"}}), nil
			}
		}
		return kgtools.ToolResult{Content: []kgtools.ContentBlock{{Type: "text", Text: `{}`}}}, nil
	}
	// FROM is a code-graph chart file, TO a cloud Deployment → both materialize to
	// their deterministic proxies as the linkage edge endpoints.
	gc.seedNode("code", chartNode)
	gc.seedNode("cloud", cloudResource)

	n, err := LinkHelmCharts(context.Background(), gc, LinkOptions{})
	require.NoError(t, err)
	assert.Equal(t, 1, n)

	require.Len(t, gc.capturedLinks, 1, "expected one composed linkage LINK")
	link := gc.capturedLinks[0]
	assert.Equal(t, "DEPLOYS", link.Relationship)
	assert.Equal(t, "linkage", link.TargetGraph)
	assert.Equal(t, "tier1-helm", link.Method)
	assert.InDelta(t, 0.85, link.Confidence, 0.0001)
	assert.Equal(t, "proxy:myapp:"+chartNode.Id, link.FromID, "code FROM → code proxy")
	assert.Equal(t, "proxy:cloud:prod:"+cloudResource.Id, link.ToID, "cloud TO → cloud proxy")
}
