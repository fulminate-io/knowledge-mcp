// SPDX-License-Identifier: Apache-2.0

package crossgraph

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// TestBuildCrossGraphProxy_DeterministicIDShapes asserts the relocated client
// builder stamps the SAME byte-identical deterministic proxy IDs the server-side
// store builder does. Drift here would desync client- and
// server-built proxy IDs, breaking dedup.
func TestBuildCrossGraphProxy_DeterministicIDShapes(t *testing.T) {
	src := &knowledgev1.Node{
		Type:       string(kgtypes.NodePattern),
		SymbolName: "app",
	}

	cases := []struct {
		name       string
		target     *knowledgev1.ProxyTarget
		wantID     string
		wantSource string
	}{
		{
			name:       "code",
			target:     &knowledgev1.ProxyTarget{GraphType: string(kgtypes.GraphCode), Name: "myrepo", NodeId: "n1"},
			wantID:     "proxy:myrepo:n1",
			wantSource: "proxy:myrepo",
		},
		{
			// THE RETIRED cicd FAMILY TAKES THE GENERIC ARM NOW, and it is here as
			// the negative control rather than as a supported shape: no builder
			// stamps proxy:cicd:… any more, so a re-added arm would break this row.
			name:       "a retired family falls to the generic arm",
			target:     &knowledgev1.ProxyTarget{GraphType: "cicd", Name: "acme", NodeId: "pipe-7"},
			wantID:     "proxy:custom/cicd:acme:pipe-7",
			wantSource: "proxy:custom/cicd:acme",
		},
		{
			name:       "practice with slug",
			target:     &knowledgev1.ProxyTarget{GraphType: string(kgtypes.GraphPractice), Name: "go", NodeId: "p1"},
			wantID:     "proxy:practice:go:p1",
			wantSource: "proxy:practice:go",
		},
		{
			name:       "practice slug-less fallback",
			target:     &knowledgev1.ProxyTarget{GraphType: string(kgtypes.GraphPractice), NodeId: "p2"},
			wantID:     "proxy:practice:p2",
			wantSource: "proxy:practice",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			proxy, err := BuildCrossGraphProxy(tc.target, src)
			require.NoError(t, err)
			require.NotNil(t, proxy)
			assert.Equal(t, tc.wantID, proxy.GetId(), "deterministic proxy ID must be byte-identical")
			assert.Equal(t, tc.wantSource, proxy.GetSource(), "proxy Source provenance")
			assert.Equal(t, string(kgtypes.NodeProxy), proxy.GetType(), "proxy node type")
			assert.Equal(t, tc.target.GetNodeId(), kgtypes.Value(proxy, "foreign_id"),
				"foreign_id metadata records the target node id")
			assert.Equal(t, tc.target.GetGraphType(), kgtypes.Value(proxy, "foreign_graph"),
				"foreign_graph metadata records the target graph type")
		})
	}
}

// TestBuildCrossGraphProxy_Guards covers the required-field + unsupported-graph
// guards.
func TestBuildCrossGraphProxy_Guards(t *testing.T) {
	src := &knowledgev1.Node{Type: "function"}

	_, err := BuildCrossGraphProxy(&knowledgev1.ProxyTarget{GraphType: string(kgtypes.GraphCode), Name: "r"}, src)
	require.Error(t, err, "missing NodeID is an error")

	_, err = BuildCrossGraphProxy(&knowledgev1.ProxyTarget{GraphType: string(kgtypes.GraphCode), NodeId: "n"}, src)
	require.Error(t, err, "code target requires a repo Name")

	_, err = BuildCrossGraphProxy(&knowledgev1.ProxyTarget{GraphType: string(kgtypes.GraphKnowledge), NodeId: "n"}, src)
	require.Error(t, err, "knowledge/generic targets are unsupported by the deterministic builder")
}
