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
// Repo, and a SINGLETON family requires no instance
// key at all — practice moved onto that arm when its eight per-language graphs
// became one combined graph, so requireInstanceKey below no longer demands a
// Language for it and TestRequireInstanceKey_AgreesWithTheServerOnPractice pins
// that. A selector that carries the name in the wrong field is rejected before
// any lookup — exactly as the server rejects it — so a client that builds the
// wrong shape cannot fetch.
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
//
// PRACTICE IS DELIBERATELY ABSENT and used to be here. It required a language,
// which was true while the family held eight per-language graphs; it holds one
// now, an unselected practice selector is the NORMAL shape, and the real server
// resolves it to the combined graph. A double that kept the old requirement
// refused a selector its subject serves — which is the same defect as serving
// one its subject refuses, in the direction that is harder to notice because
// nothing drives it.
func requireInstanceKey(sel *knowledgev1.GraphSelector) error {
	switch sel.GetGraph() {
	case "code":
		if sel.GetRepo() == "" {
			return fmt.Errorf("graph=code requires repo")
		}
	}
	return nil
}

// TestRequireInstanceKey_AgreesWithTheServerOnPractice is what makes the double
// above load-bearing rather than merely present.
//
// NOTHING DROVE IT BEFORE, which is why it kept a rule the server had dropped
// and the suite stayed green through the whole change. The rows below are the
// disagreement, stated: an unselected practice selector must PASS, and the one
// family that genuinely requires an instance field must still fail.
func TestRequireInstanceKey_AgreesWithTheServerOnPractice(t *testing.T) {
	require.NoError(t, requireInstanceKey(&knowledgev1.GraphSelector{Graph: "practice"}),
		"practice holds ONE graph: an unselected selector is the normal shape, and the server resolves it")
	require.NoError(t, requireInstanceKey(&knowledgev1.GraphSelector{Graph: "practice", Language: "go"}),
		"and a legacy read still names one of the pre-singleton graphs")

	// THE CONTROL: the one family that DOES require an instance key must still be
	// refused, or the rows above are satisfied by a guard that stopped guarding.
	// It is a single control because code is the only instance-addressed family
	// left — the account-keyed inventory families retired with their built-in
	// collectors, and a retired name is refused by the real resolver ahead of any
	// field policy, which is a different rule from this one.
	require.Error(t, requireInstanceKey(&knowledgev1.GraphSelector{Graph: "code"}),
		"a code selector with no repo names no graph")
}

// errorFake fails every Execute — used to drive the probe-failure arm.
type errorFake struct{ err error }

func (f *errorFake) Execute(_ context.Context, _ *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	return nil, f.err
}

// TestLocateForeignNode_PerFamilyInstanceKeyReachesTheGraph proves the located
// selector addresses each family by the field its resolver keys on. Both
// surviving foreign families are here: a selector carrying the instance name in
// the wrong field is rejected server-side and the location silently returns
// "not found", so each row proves its own family reaches the graph.
//
// THE ACCOUNT-KEYED ROWS THIS TEST WAS WRITTEN FOR ARE GONE with their families.
// foreignScanGraphTypes enumerates code and practice alone, so such a row would
// be asserting that a family nothing scans can be located — an input that cannot
// occur.
func TestLocateForeignNode_PerFamilyInstanceKeyReachesTheGraph(t *testing.T) {
	node := func(id string) *knowledgev1.Node {
		return &knowledgev1.Node{Id: id, Type: string(kgtypes.NodePattern), SymbolName: id}
	}
	f := &resolverFake{nodesByGraph: map[string]map[string]*knowledgev1.Node{
		"code":     {"repo-node": node("repo-node")},
		"practice": {"prac-node": node("prac-node")},
	}}

	for _, tc := range []struct {
		graphType string
		graphName string
		id        string
	}{
		{"code", "knowledge", "repo-node"}, // Repo-keyed
		{"practice", "go", "prac-node"},    // singleton — no instance key
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
