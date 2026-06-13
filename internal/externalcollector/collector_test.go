// SPDX-License-Identifier: Apache-2.0

package externalcollector

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// defForStub builds a GraphTypeDef named "jira" pointing at a stub binary, with
// a single optional "repo" param so ValidateParams passes a {repo:...} params
// map. The fixed "jira" name is the graph_type the package's stub envelopes
// emit, so RunExternal's emitted-graph_type guard matches on the happy path.
func defForStub(binPath string) *knowledgev1.GraphTypeDef {
	return &knowledgev1.GraphTypeDef{
		Name: "jira",
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
	def := defForStub(bin)

	cr, err := RunExternal(context.Background(), def, map[string]any{"repo": "x"}, "board")
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
	def := defForStub(bin)

	cr, err := RunExternal(context.Background(), def, map[string]any{"repo": "x"}, "board")
	require.Error(t, err)
	assert.Nil(t, cr)
	assert.Contains(t, err.Error(), "graph_type")
}

// TestRunExternal_GraphNameDefaultsToID confirms the id→graph_name default: a
// stub whose envelope OMITS graph_name resolves graph_name to the defaultGraphName
// (the collect id) RunExternal was called with.
func TestRunExternal_GraphNameDefaultsToID(t *testing.T) {
	bin := writeStubBin(t, `cat > /dev/null
echo '{"graph_type":"jira","nodes":[{"id":"a","type":"issue"}],"edges":[]}'`)
	def := defForStub(bin)

	cr, err := RunExternal(context.Background(), def, map[string]any{"repo": "x"}, "resolved-id")
	require.NoError(t, err)
	require.NotNil(t, cr)
	assert.Equal(t, "resolved-id", cr.GraphName)
}

// TestRunExternal_EnvelopeGraphNameWins confirms an explicit envelope graph_name
// wins over the defaultGraphName: when the stub emits its own graph_name the
// default is NOT applied.
func TestRunExternal_EnvelopeGraphNameWins(t *testing.T) {
	bin := writeStubBin(t, `cat > /dev/null
echo '{"graph_type":"jira","graph_name":"board","nodes":[{"id":"a","type":"issue"}],"edges":[]}'`)
	def := defForStub(bin)

	cr, err := RunExternal(context.Background(), def, map[string]any{"repo": "x"}, "resolved-id")
	require.NoError(t, err)
	require.NotNil(t, cr)
	assert.Equal(t, "board", cr.GraphName)
}

// TestRunExternal_BothEmptyGraphNameFailsLoud confirms the both-empty path stays
// fail-loud: when the stub envelope OMITS graph_name AND no defaultGraphName is
// supplied (empty id), the convert.go ToCollectResult empty-graph_name guard
// fires — a non-nil error and a nil result, never a silent degenerate collect.
//
// This is one half of the locked fail-loud contract; the other half lives in
// exec_test.go: TestRun_MalformedJSONFailsLoud (a non-JSON stdout) and
// TestRun_NonZeroExitSurfacesStderr (a non-zero exit surfacing stderr). Those
// guards are left unmodified — together they pin the contract the docs describe.
func TestRunExternal_BothEmptyGraphNameFailsLoud(t *testing.T) {
	bin := writeStubBin(t, `cat > /dev/null
echo '{"graph_type":"jira","nodes":[{"id":"a","type":"issue"}],"edges":[]}'`)
	def := defForStub(bin)

	cr, err := RunExternal(context.Background(), def, map[string]any{"repo": "x"}, "")
	require.Error(t, err)
	assert.Nil(t, cr)
	assert.Contains(t, err.Error(), "graph_name")
}
