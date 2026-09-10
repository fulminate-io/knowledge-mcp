// SPDX-License-Identifier: Apache-2.0

package crossgraph

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// proxy_id_vector_test.go — the CLIENT half of the two-module proxy-id parity
// vector. The server half is cmd/knowledge-server/internal/store's own reader of
// the same file.
//
// WHY A DATA FILE AND NOT A DIRECT COMPARISON. cmd/knowledge and
// cmd/knowledge-server are separate modules and no hand-written package may be
// shared between them, so neither builder can call the other. Holding both to
// one CHECKED-IN answer key is stronger than comparing them to each other in any
// case: two builders that drifted together would agree with each other and
// disagree with the convention, and only an external expectation catches that.
// The vector's ids were authored from the convention, never from a run.
//
// READING IT THROUGH THIS PACKAGE'S OWN testdata LINK IS ALSO THE CACHE FENCE:
// the go tool drops any opened name that resolves outside the tested module's
// root, so a vector read by a walked-up relative path would be absent from the
// cache key and an edited vector would be served a stored PASS.

const proxyIDVectorLink = "testdata/cross_graph_proxy_id_vector.json"

type proxyIDCase struct {
	Label          string            `json:"label"`
	GraphType      string            `json:"graph_type"`
	Name           string            `json:"name"`
	NodeID         string            `json:"node_id"`
	SourceType     string            `json:"source_type"`
	SourceMetadata map[string]string `json:"source_metadata"`
	WantID         string            `json:"want_id"`
	WantSource     string            `json:"want_source"`
	WantMetadata   map[string]string `json:"want_metadata"`
	WantError      bool              `json:"want_error"`
}

type proxyIDVector struct {
	Cases []proxyIDCase `json:"cases"`
	Pairs []struct {
		Why string `json:"why"`
		A   string `json:"a"`
		B   string `json:"b"`
	} `json:"distinct_id_pairs"`
}

func loadProxyIDVector(t *testing.T) proxyIDVector {
	t.Helper()
	// THE SENTINEL IS PART OF THE FENCE. A checkout without symlink support
	// materializes the link as a one-line text stub, which would decode as a JSON
	// error here rather than silently reading nothing — but an EMPTY case list
	// would pass every loop below vacuously, so the count is asserted too.
	info, err := os.Lstat(proxyIDVectorLink)
	require.NoError(t, err, "%s must exist — it is the only artifact holding the two builders to one answer", proxyIDVectorLink)
	require.NotZero(t, info.Mode()&os.ModeSymlink,
		"%s is not a symlink; a materialized copy fences nothing and drifts from the server's reader", proxyIDVectorLink)
	target, err := filepath.EvalSymlinks(proxyIDVectorLink)
	require.NoError(t, err, "%s must resolve; a dangling link fences nothing", proxyIDVectorLink)
	require.Equal(t, "testdata/cross_graph_proxy_id_vector.json", filepath.ToSlash(filepath.Join(filepath.Base(filepath.Dir(target)), filepath.Base(target))))

	raw, err := os.ReadFile(proxyIDVectorLink)
	require.NoError(t, err)
	var v proxyIDVector
	require.NoError(t, json.Unmarshal(raw, &v))
	require.NotEmpty(t, v.Cases, "an empty vector passes every assertion below vacuously")
	return v
}

// TestBuildCrossGraphProxy_MatchesTheSharedIDVector drives every case in the
// shared vector through the CLIENT builder.
func TestBuildCrossGraphProxy_MatchesTheSharedIDVector(t *testing.T) {
	v := loadProxyIDVector(t)
	for _, tc := range v.Cases {
		t.Run(tc.Label, func(t *testing.T) {
			src := &knowledgev1.Node{Type: tc.SourceType, Metadata: tc.SourceMetadata}
			proxy, err := BuildCrossGraphProxy(&knowledgev1.ProxyTarget{
				GraphType: tc.GraphType,
				Name:      tc.Name,
				NodeId:    tc.NodeID,
			}, src)

			if tc.WantError {
				require.Error(t, err, "this input has no deterministic id and must be refused, not stamped")
				assert.Nil(t, proxy, "a refused build returns no proxy")
				return
			}
			require.NoError(t, err)
			require.NotNil(t, proxy)
			assert.Equal(t, tc.WantID, proxy.GetId(), "proxy id")
			assert.Equal(t, tc.WantSource, proxy.GetSource(), "proxy Source provenance")
			assert.Equal(t, "proxy", proxy.GetType())
			assert.Equal(t, tc.WantMetadata, proxy.GetMetadata(),
				"the WHOLE metadata map is compared, so an arm that added or dropped a key is caught rather than only a changed one")
		})
	}
}

// TestBuildCrossGraphProxy_TheGenericArmCannotCollideWithABuiltinArm is the
// reviewer's known positive, driven from the vector's own declared pairs so the
// colliding inputs live beside the ids they produce.
func TestBuildCrossGraphProxy_TheGenericArmCannotCollideWithABuiltinArm(t *testing.T) {
	v := loadProxyIDVector(t)
	byLabel := make(map[string]proxyIDCase, len(v.Cases))
	for _, c := range v.Cases {
		byLabel[c.Label] = c
	}
	require.NotEmpty(t, v.Pairs, "the vector declares no distinct-id pair, so this test would assert nothing")

	for _, p := range v.Pairs {
		a, okA := byLabel[p.A]
		b, okB := byLabel[p.B]
		require.True(t, okA, "distinct_id_pairs names a case %q the vector does not carry", p.A)
		require.True(t, okB, "distinct_id_pairs names a case %q the vector does not carry", p.B)
		assert.NotEqual(t, a.WantID, b.WantID, p.Why)

		// The ids are then re-derived from the BUILDER rather than trusted from
		// the vector's own fields: a vector that declared two equal ids would fail
		// the assertion above, and a builder that produced two equal ids from
		// distinct declared ones fails here.
		gotA, errA := BuildCrossGraphProxy(&knowledgev1.ProxyTarget{GraphType: a.GraphType, Name: a.Name, NodeId: a.NodeID}, &knowledgev1.Node{Type: a.SourceType})
		gotB, errB := BuildCrossGraphProxy(&knowledgev1.ProxyTarget{GraphType: b.GraphType, Name: b.Name, NodeId: b.NodeID}, &knowledgev1.Node{Type: b.SourceType})
		require.NoError(t, errA)
		require.NoError(t, errB)
		assert.NotEqual(t, gotA.GetId(), gotB.GetId(), p.Why)
	}
}
