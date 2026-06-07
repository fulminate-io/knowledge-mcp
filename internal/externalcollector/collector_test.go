// SPDX-License-Identifier: Apache-2.0

package externalcollector

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// defForStub builds a GraphTypeDef named graphType pointing at a stub binary,
// with a single optional "repo" param so ValidateParams passes a {repo:...}
// params map.
func defForStub(name, binPath string) *knowledgev1.GraphTypeDef {
	return &knowledgev1.GraphTypeDef{
		Name: name,
		Collector: &knowledgev1.CollectorSpec{
			BinaryPath:     binPath,
			ParamTransport: "stdin",
			ParamSchema: map[string]*knowledgev1.ParamSpec{
				"repo": {Type: "string"},
			},
		},
	}
}

// TestRunExternal_GraphTypeMatch confirms a stub emitting graph_type == def.Name
// yields a *collectorwire.CollectResult carrying the stub's nodes.
func TestRunExternal_GraphTypeMatch(t *testing.T) {
	bin := writeStubBin(t, `cat > /dev/null
echo '{"graph_type":"jira","graph_name":"board","nodes":[{"id":"a","type":"issue"}],"edges":[]}'`)
	def := defForStub("jira", bin)

	cr, err := RunExternal(context.Background(), def, map[string]any{"repo": "x"})
	require.NoError(t, err)
	require.NotNil(t, cr)
	assert.Equal(t, "board", cr.GraphName)
	require.Len(t, cr.Nodes, 1)
	assert.Equal(t, "a", cr.Nodes[0].GetId())
}

// TestRunExternal_GraphTypeMismatch confirms a stub emitting a DIFFERENT
// graph_type than the registered name yields a non-nil mismatch error and a
// nil result — a plugin cannot write into another graph type.
func TestRunExternal_GraphTypeMismatch(t *testing.T) {
	bin := writeStubBin(t, `cat > /dev/null
echo '{"graph_type":"other","graph_name":"board","nodes":[{"id":"a","type":"issue"}],"edges":[]}'`)
	def := defForStub("jira", bin)

	cr, err := RunExternal(context.Background(), def, map[string]any{"repo": "x"})
	require.Error(t, err)
	assert.Nil(t, cr)
	assert.Contains(t, err.Error(), "graph_type")
}
