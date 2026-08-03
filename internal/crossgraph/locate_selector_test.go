// SPDX-License-Identifier: Apache-2.0

package crossgraph

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/enginetest"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// resolverFake serves by-id reads ONLY when the request's GraphSelector carries
// the instance name in the field that graph family's server-side resolver
// actually keys on. It mirrors ResolveGraphDB's per-family requirements
// (cmd/knowledge-server/internal/tools/tools_graph_routing.go): code requires
// Repo, cloud/cicd require Account, practice requires Language. A selector that
// carries the name in the wrong field is rejected before any lookup — exactly as
// the server rejects it — so a client that builds the wrong shape cannot fetch.
type resolverFake struct {
	nodesByGraph map[string]map[string]*knowledgev1.Node // graphType → id → node
}

func (f *resolverFake) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	sel := req.GetTarget()
	if err := requireInstanceKey(sel); err != nil {
		return nil, err
	}
	var nodes []*knowledgev1.Node
	if n, ok := f.nodesByGraph[sel.GetGraph()][req.GetQuery().GetById()]; ok {
		nodes = []*knowledgev1.Node{n}
	}
	return enginetest.ResponseWithNodes(nodes...), nil
}

// requireInstanceKey rejects a selector whose instance name landed in a field
// the family's resolver does not read.
func requireInstanceKey(sel *knowledgev1.GraphSelector) error {
	switch sel.GetGraph() {
	case "code":
		if sel.GetRepo() == "" {
			return fmt.Errorf("graph=code requires repo")
		}
	case "cloud", "cicd":
		if sel.GetAccount() == "" {
			return fmt.Errorf("graph=%s requires account", sel.GetGraph())
		}
	case "practice":
		if sel.GetLanguage() == "" {
			return fmt.Errorf("graph=practice requires language")
		}
	}
	return nil
}

// errorFake fails every Execute — used to drive the probe-failure arm.
type errorFake struct{ err error }

func (f *errorFake) Execute(_ context.Context, _ *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	return nil, f.err
}

// TestLocateForeignNode_PerFamilyInstanceKeyReachesTheGraph proves the located
// selector addresses each family by the field its resolver keys on. The cloud
// and cicd arms are the subject: their instance names must ride Account, not
// Name — a Name-keyed cloud selector is rejected server-side and the location
// silently returns "not found". The code and practice arms are the known-
// positive controls: they resolved before this fix, so a cloud/cicd failure here
// is a real per-family gap rather than a broken fake.
func TestLocateForeignNode_PerFamilyInstanceKeyReachesTheGraph(t *testing.T) {
	node := func(id string) *knowledgev1.Node {
		return &knowledgev1.Node{Id: id, Type: string(kgtypes.NodeCloudResource), SymbolName: id}
	}
	f := &resolverFake{nodesByGraph: map[string]map[string]*knowledgev1.Node{
		"code":     {"repo-node": node("repo-node")},
		"practice": {"prac-node": node("prac-node")},
		"cloud":    {"cloud-node": node("cloud-node")},
		"cicd":     {"cicd-node": node("cicd-node")},
	}}

	for _, tc := range []struct {
		graphType string
		graphName string
		id        string
	}{
		{"code", "knowledge", "repo-node"}, // control — Repo-keyed, worked before
		{"practice", "go", "prac-node"},    // control — Language-keyed, worked before
		{"cloud", "prod", "cloud-node"},    // subject — Account-keyed
		{"cicd", "github", "cicd-node"},    // subject — Account-keyed
	} {
		t.Run(tc.graphType, func(t *testing.T) {
			gt, name, n, found := LocateForeignNode(
				context.Background(), f,
				[]ForeignGraph{{GraphType: tc.graphType, GraphName: tc.graphName}}, tc.id)
			require.True(t, found,
				"%s-family location must reach the graph — a selector carrying %q in the wrong field is rejected server-side and the location silently no-ops",
				tc.graphType, tc.graphName)
			assert.Equal(t, kgtypes.GraphType(tc.graphType), gt)
			assert.Equal(t, tc.graphName, name)
			require.NotNil(t, n)
			assert.Equal(t, tc.id, n.GetId())
		})
	}
}

// TestLocateForeignNode_ProbeFailureIsLoggedNotSwallowed pins the disposition of
// the per-graph probe error: the scan still degrades gracefully (no error
// returned, remaining graphs still probed) but the failure is no longer
// invisible — it is logged at WARN with the graph family and name, so a whole
// family failing every probe is operator-visible instead of a silent no-op.
func TestLocateForeignNode_ProbeFailureIsLoggedNotSwallowed(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	gt, name, n, found := LocateForeignNode(
		context.Background(), &errorFake{err: errors.New("graph=cloud requires account")},
		[]ForeignGraph{{GraphType: "cloud", GraphName: "prod"}}, "some-node")

	assert.False(t, found, "a failed probe still degrades to not-found")
	assert.Empty(t, gt)
	assert.Empty(t, name)
	assert.Nil(t, n)

	logged := buf.String()
	assert.Contains(t, logged, "level=WARN", "the probe failure must be logged loudly, not swallowed")
	assert.Contains(t, logged, "cloud", "the warning names the graph family")
	assert.Contains(t, logged, "prod", "the warning names the graph instance")
	assert.Contains(t, logged, "requires account", "the warning carries the underlying error")
}
