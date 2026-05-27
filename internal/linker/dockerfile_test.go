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

func TestParseCopyDirectives_BasicCopy(t *testing.T) {
	content := `FROM golang:1.22 AS builder
COPY go.mod go.sum ./
COPY cmd/ /app/cmd/
COPY pkg/ pkg/
ADD https://example.com/file ./
COPY --from=builder /app/bin /usr/local/bin
`
	got := parseCopyDirectives(content)
	assert.ElementsMatch(t, []string{"go.mod", "go.sum", "cmd/", "pkg/"}, got,
		"COPY/ADD parse should yield local source paths only")
}

func TestParseCopyDirectives_SkipsURLsAndDotAndStar(t *testing.T) {
	content := `ADD http://example.com/foo /
ADD https://example.com/bar /
COPY . /app/
COPY * /tmp/
COPY ./relative.txt /app/
`
	got := parseCopyDirectives(content)
	assert.Equal(t, []string{"./relative.txt"}, got)
}

func TestIsDockerfile(t *testing.T) {
	for _, p := range []string{"Dockerfile", "deploy/Dockerfile", "Dockerfile.dev", "dev.dockerfile"} {
		assert.True(t, isDockerfile(p), "should detect %q as Dockerfile", p)
	}
	for _, p := range []string{"main.go", "Makefile", "compose.yaml"} {
		assert.False(t, isDockerfile(p), "should NOT detect %q as Dockerfile", p)
	}
}

func TestLinkDockerfiles_EmitsBuildsEdge(t *testing.T) {
	dfNode := &knowledgev1.Node{
		Id:       "myapp:Dockerfile",
		Type:     string(kgtypes.NodeFile),
		FilePath: "Dockerfile",
		Content:  "FROM scratch\nCOPY main.go /\n",
	}
	srcNode := &knowledgev1.Node{
		Id:       "myapp:main.go",
		Type:     string(kgtypes.NodeFile),
		FilePath: "main.go",
	}

	gc := &fakeGraphCaller{}
	gc.respond = func(tool string, args map[string]any) (kgtools.ToolResult, error) {
		switch tool {
		case "query":
			graph, _ := args["graph"].(string)
			if graph == "code" {
				if _, hasType := args["type"]; hasType {
					typ, _ := args["type"].(string)
					if typ == string(kgtypes.NodeFile) {
						return jsonResult(t, map[string]any{"nodes": []*knowledgev1.Node{
							dfNode,
							srcNode,
						}}), nil
					}
					if typ == string(kgtypes.NodePackage) {
						return jsonResult(t, map[string]any{"nodes": []*knowledgev1.Node{}}), nil
					}
				}
				return jsonResult(t, map[string]any{"graphs": []string{"myapp"}}), nil
			}
		}
		return kgtools.ToolResult{Content: []kgtools.ContentBlock{{Type: "text", Text: `{}`}}}, nil
	}
	// Both endpoints are code-graph nodes, so the crossgraph composer materializes
	// their deterministic code proxies as the linkage edge endpoints.
	gc.seedNode("code", dfNode)
	gc.seedNode("code", srcNode)

	n, err := LinkDockerfiles(context.Background(), gc, LinkOptions{})
	require.NoError(t, err)
	assert.Equal(t, 1, n)

	require.Len(t, gc.capturedLinks, 1, "expected one composed linkage LINK for Dockerfile COPY")
	link := gc.capturedLinks[0]
	assert.Equal(t, "BUILDS", link.Relationship)
	assert.Equal(t, "linkage", link.TargetGraph)
	assert.Equal(t, "tier1-dockerfile", link.Method)
	assert.InDelta(t, 0.95, link.Confidence, 0.0001)
	assert.Equal(t, "proxy:myapp:"+dfNode.Id, link.FromID, "code FROM → deterministic code proxy")
	assert.Equal(t, "proxy:myapp:"+srcNode.Id, link.ToID, "code TO → deterministic code proxy")
}
