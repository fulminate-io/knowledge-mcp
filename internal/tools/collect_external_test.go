// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// stubGraphTypeCRUD is a minimal GraphTypeCRUDAPI whose ByName returns a fixed
// GraphTypeDef. Only ByName is exercised by the collect dispatch; the other
// methods are unused here.
type stubGraphTypeCRUD struct {
	def *knowledgev1.GraphTypeDef
}

func (s *stubGraphTypeCRUD) List(context.Context) ([]*knowledgev1.GraphTypeDef, error) {
	if s.def == nil {
		return nil, nil
	}
	return []*knowledgev1.GraphTypeDef{s.def}, nil
}

func (s *stubGraphTypeCRUD) ByName(_ context.Context, name string) (*knowledgev1.GraphTypeDef, bool, error) {
	if s.def != nil && s.def.GetName() == name {
		return s.def, true, nil
	}
	return nil, false, nil
}

func (s *stubGraphTypeCRUD) Create(context.Context, *knowledgev1.GraphTypeDef) error { return nil }
func (s *stubGraphTypeCRUD) Update(context.Context, *knowledgev1.GraphTypeDef) error { return nil }
func (s *stubGraphTypeCRUD) Delete(context.Context, string) error                    { return nil }

// capturingSink (declared in ingest_test.go) records every CollectResult
// written; .last() returns the most recent one.

// writeExternalStub writes an executable shell-script stub collector into
// t.TempDir() and returns its absolute path. The stub echoes the supplied
// envelope JSON; bodyPrefix runs first (e.g. to consume stdin).
func writeExternalStub(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "registered-collector")
	require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o700)) //nolint:gosec // test fixture must be executable
	return path
}

// registeredDef builds a GraphTypeDef named graphType pointing at binPath with a
// single optional "repo" param.
func registeredDef(graphType, binPath string) *knowledgev1.GraphTypeDef {
	return &knowledgev1.GraphTypeDef{
		Name: graphType,
		Collector: &knowledgev1.CollectorSpec{
			BinaryPath:     binPath,
			ParamTransport: "stdin",
			ParamSchema: map[string]*knowledgev1.ParamSpec{
				"repo": {Type: "string"},
			},
		},
	}
}

func callCollect(deps ClientDeps, args string) (bool, kgtools.ToolResult) {
	return InterceptCollect(deps, kgtools.CallToolParams{
		Name:      "collect",
		Arguments: json.RawMessage(args),
	})
}

// TestInterceptCollect_RegisteredType_WithID confirms a registered-type collect
// (with a top-level id) resolves the def via GraphTypeCRUD().ByName, drives the
// stub binary, and ships the stub's nodes through the capturing Sink.
func TestInterceptCollect_RegisteredType_WithID(t *testing.T) {
	bin := writeExternalStub(t, `cat > /dev/null
echo '{"graph_type":"jira","graph_name":"board","nodes":[{"id":"ISSUE-1","type":"issue"}],"edges":[]}'`)
	sink := &capturingSink{}
	deps := &fakeDeps{sink: sink, crud: &stubGraphTypeCRUD{def: registeredDef("jira", bin)}}

	handled, res := callCollect(deps, `{"type":"jira","id":"board","params":{"repo":"x"}}`)
	require.True(t, handled)
	require.False(t, res.IsError, resultText(res))

	got := sink.last()
	require.NotNil(t, got, "Sink.WriteResult should have captured a CollectResult")
	require.Len(t, got.Nodes, 1)
	assert.Equal(t, "ISSUE-1", got.Nodes[0].GetId())
}

// TestInterceptCollect_RegisteredType_EmptyID confirms a params-only registered
// collect (empty top-level id) is ACCEPTED — it does NOT return the
// "collect <type>: 'id' is required" error; instead it resolves via ByName and
// drives the stub. Verifies the relaxed id guard for registered types.
func TestInterceptCollect_RegisteredType_EmptyID(t *testing.T) {
	bin := writeExternalStub(t, `cat > /dev/null
echo '{"graph_type":"jira","graph_name":"board","nodes":[{"id":"ISSUE-1","type":"issue"}],"edges":[]}'`)
	sink := &capturingSink{}
	deps := &fakeDeps{sink: sink, crud: &stubGraphTypeCRUD{def: registeredDef("jira", bin)}}

	handled, res := callCollect(deps, `{"type":"jira","id":"","params":{"repo":"x"}}`)
	require.True(t, handled)
	require.False(t, res.IsError, resultText(res))
	assert.NotContains(t, resultText(res), "'id' is required")

	got := sink.last()
	require.NotNil(t, got)
	require.Len(t, got.Nodes, 1)
	assert.Equal(t, "ISSUE-1", got.Nodes[0].GetId())
}

// TestInterceptCollect_RegisteredType_ParamsForwarded confirms the params object
// reaches the stub: the stub echoes a stdin param into a node's metadata on the
// captured CollectResult.
func TestInterceptCollect_RegisteredType_ParamsForwarded(t *testing.T) {
	// The stub reads stdin (the marshaled params JSON) and embeds a marker into
	// a node's metadata so we can confirm the params were delivered. The params
	// value is asserted to contain the repo key.
	bin := writeExternalStub(t, `IN=$(cat)
case "$IN" in
  *repo*) GOT=yes ;;
  *) GOT=no ;;
esac
cat <<EOF
{"graph_type":"jira","graph_name":"board","nodes":[{"id":"ISSUE-1","type":"issue","metadata":{"got_repo":"$GOT"}}],"edges":[]}
EOF`)
	sink := &capturingSink{}
	deps := &fakeDeps{sink: sink, crud: &stubGraphTypeCRUD{def: registeredDef("jira", bin)}}

	handled, res := callCollect(deps, `{"type":"jira","id":"board","params":{"repo":"acme"}}`)
	require.True(t, handled)
	require.False(t, res.IsError, resultText(res))

	got := sink.last()
	require.NotNil(t, got)
	require.Len(t, got.Nodes, 1)
	assert.Equal(t, "yes", got.Nodes[0].GetMetadata()["got_repo"])
}

// TestInterceptCollect_UnknownType_NotRegistered confirms an unknown type that
// is neither builtin nor registered still hits the existing 'id' required guard
// (the registered probe finds nothing and control falls through unchanged).
func TestInterceptCollect_UnknownType_NotRegistered(t *testing.T) {
	deps := &fakeDeps{sink: &capturingSink{}, crud: &stubGraphTypeCRUD{def: nil}}

	handled, res := callCollect(deps, `{"type":"not-a-real-type","id":""}`)
	require.True(t, handled)
	require.True(t, res.IsError)
	assert.Contains(t, resultText(res), "'id' is required")
}
