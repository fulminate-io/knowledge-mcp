// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/enginetest"
	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// fakeBootstrapGC is a scripted instructionBootstrapGC for unit tests. The
// bootstrap rides the Execute carrier seam: the idempotency
// pre-flight and the create_batch seed both run through Execute. The fake
// returns a scripted typed Nodes carrier for the query (idempotency) Execute
// and a scripted response/error for the mutate (create_batch) Execute,
// discriminating by the plan kind on the request.
type fakeBootstrapGC struct {
	// queryNodeCount drives the idempotency pre-flight: the query Execute
	// returns a typed Nodes carrier with this many agent nodes.
	queryNodeCount int
	queryError     error
	mutateError    error

	calls []bootstrapCall
}

type bootstrapCall struct {
	tool string
	req  *knowledgev1.ExecuteRequest
	// op is the query-origin operation the call carried on its ctx. Recorded
	// because the bootstrap runs at boot with no originating tool call, so an
	// unstamped call reaches the wire as client.unstamped.
	op   graphclient.Operation
	opOK bool
}

// Call satisfies the interface; the bootstrap routes through Execute, so this
// is unused in practice.
func (f *fakeBootstrapGC) Call(_ context.Context, _ string, _ json.RawMessage) (kgtools.ToolResult, error) {
	return kgtools.ToolResult{}, nil
}

func (f *fakeBootstrapGC) Execute(ctx context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	op, opOK := graphclient.OperationFromContext(ctx)
	if req.GetQuery() != nil {
		f.calls = append(f.calls, bootstrapCall{tool: "query", req: req, op: op, opOK: opOK})
		if f.queryError != nil {
			return nil, f.queryError
		}
		nodes := make([]*knowledgev1.Node, f.queryNodeCount)
		for i := range nodes {
			nodes[i] = &knowledgev1.Node{Id: "a-" + string(rune('a'+i)), Type: string(kgtypes.NodeAgent)}
		}
		return enginetest.ResponseWithNodes(nodes...), nil
	}
	// mutate (create_batch) Execute.
	f.calls = append(f.calls, bootstrapCall{tool: "mutate", req: req, op: op, opOK: opOK})
	if f.mutateError != nil {
		return nil, f.mutateError
	}
	return &knowledgev1.ExecuteResponse{Ids: []string{"a", "b", "c"}}, nil
}

// TestInstructionBootstrap_Idempotent asserts that when any agent node
// already exists (the idempotency query Execute returns ≥1 node), the
// bootstrap short-circuits without issuing the create_batch.
func TestInstructionBootstrap_Idempotent(t *testing.T) {
	fc := &fakeBootstrapGC{queryNodeCount: 3}
	dir := t.TempDir()
	makeBootstrapDirs(t, dir, 2, 1)
	err := runInstructionBootstrap(context.Background(), fc, dir)
	require.NoError(t, err)
	// Only the pre-flight query Execute — no mutate.
	require.Len(t, fc.calls, 1)
	assert.Equal(t, "query", fc.calls[0].tool)
}

// TestInstructionBootstrap_FilesystemMiss_Silent asserts that when the
// .claude tree is empty, no mutate fires and slog.Info is emitted.
func TestInstructionBootstrap_FilesystemMiss_Silent(t *testing.T) {
	fc := &fakeBootstrapGC{queryNodeCount: 0}
	dir := t.TempDir() // no .claude tree
	err := runInstructionBootstrap(context.Background(), fc, dir)
	require.NoError(t, err)
	// Pre-flight query Execute, then nothing.
	require.Len(t, fc.calls, 1)
	assert.Equal(t, "query", fc.calls[0].tool)
}

// TestInstructionBootstrap_FreshRun_SeedsAgentsAndSkills asserts the
// bootstrap reads .claude/{agents,skills}/*.md and posts ONE create_batch
// MutationPlan carrying the union (with a bundle_id).
func TestInstructionBootstrap_FreshRun_SeedsAgentsAndSkills(t *testing.T) {
	fc := &fakeBootstrapGC{queryNodeCount: 0}
	dir := t.TempDir()
	makeBootstrapDirs(t, dir, 2, 1)
	err := runInstructionBootstrap(context.Background(), fc, dir)
	require.NoError(t, err)
	// Pre-flight query Execute + ONE mutate Execute.
	require.Len(t, fc.calls, 2)
	assert.Equal(t, "query", fc.calls[0].tool)
	assert.Equal(t, "mutate", fc.calls[1].tool)
	m := fc.calls[1].req.GetMutation()
	require.NotNil(t, m, "bootstrap compiles a create MutationPlan")
	assert.Equal(t, knowledgev1.MutationPlan_MUTATION_KIND_CREATE, m.GetKind())
	assert.NotEmpty(t, m.GetBundleId(), "bundle_id must be threaded onto bootstrap create_batch")
	assert.Len(t, m.GetNodeBodies(), 3, "2 agents + 1 skill = 3 node bodies")
}

// TestInstructionBootstrap_BootstrapFailureNonFatal asserts that mutate
// Execute failures bubble up as an error (so the caller can slog.Warn and
// continue).
func TestInstructionBootstrap_BootstrapFailureNonFatal(t *testing.T) {
	fc := &fakeBootstrapGC{queryNodeCount: 0, mutateError: errors.New("connect: refused")}
	dir := t.TempDir()
	makeBootstrapDirs(t, dir, 1, 0)
	err := runInstructionBootstrap(context.Background(), fc, dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "connect: refused")
}

// makeBootstrapDirs creates .claude/agents/*.md + .claude/skills/*.md
// fixtures under root. Each file has a minimal frontmatter block.
func makeBootstrapDirs(t *testing.T, root string, nAgents, nSkills int) {
	t.Helper()
	agentsDir := filepath.Join(root, ".claude", "agents")
	skillsDir := filepath.Join(root, ".claude", "skills")
	require.NoError(t, os.MkdirAll(agentsDir, 0o750))
	require.NoError(t, os.MkdirAll(skillsDir, 0o750))
	for i := range nAgents {
		path := filepath.Join(agentsDir, makeFilename("agent", i)+".md")
		require.NoError(t, os.WriteFile(path, fixtureMarkdownContent(), 0o600))
	}
	for i := range nSkills {
		path := filepath.Join(skillsDir, makeFilename("skill", i)+".md")
		require.NoError(t, os.WriteFile(path, fixtureMarkdownContent(), 0o600))
	}
}

func makeFilename(kind string, i int) string {
	return kind + "-" + string(rune('a'+i))
}

func fixtureMarkdownContent() []byte {
	return []byte("---\nname: fixture\ndescription: a fixture entry\n---\n\nBody paragraph.\n")
}
