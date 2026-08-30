// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
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

	// mu guards calls. The deferred instruction bootstrap runs on its own
	// goroutine, so the recorder is written from one goroutine and read from the
	// test's — read it through recorded() rather than touching the slice.
	mu    sync.Mutex
	calls []bootstrapCall
}

// recorded returns a copy of the recorded calls under the lock.
func (f *fakeBootstrapGC) recorded() []bootstrapCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]bootstrapCall(nil), f.calls...)
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
		f.mu.Lock()
		f.calls = append(f.calls, bootstrapCall{tool: "query", req: req, op: op, opOK: opOK})
		f.mu.Unlock()
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
	f.mu.Lock()
	f.calls = append(f.calls, bootstrapCall{tool: "mutate", req: req, op: op, opOK: opOK})
	f.mu.Unlock()
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

	// A skill node is named after its DIRECTORY, not its file stem. Every
	// skill file is called SKILL.md, so a stem-derived name would name
	// every skill "SKILL" — one indistinguishable node per skill, and the
	// name users actually type would resolve to nothing. Asserting the
	// count alone cannot catch that: three bodies arrive either way.
	byType := map[string][]string{}
	for _, b := range m.GetNodeBodies() {
		byType[b.GetType()] = append(byType[b.GetType()], b.GetName())
	}
	assert.Equal(t, []string{"skill-a"}, byType["skill"],
		"skill node is named after its directory, not the SKILL.md stem")
	assert.Equal(t, []string{"agent-a", "agent-b"}, byType["agent"],
		"agent nodes stay named after their file stem")
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

// makeBootstrapDirs creates .claude/agents/*.md + .claude/skills/*/SKILL.md
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
	// NESTED, because that is the layout the repo ships:
	// .claude/skills/<name>/SKILL.md. An earlier fixture wrote a FLAT
	// .claude/skills/<name>.md, which is the layout the globbing code
	// expected — so the fixture constructed the belief the code was
	// tested against and the real tree seeded zero skills for as long
	// as both agreed. A fixture that builds the input cannot also be
	// the evidence the input looks that way.
	for i := range nSkills {
		dir := filepath.Join(skillsDir, makeFilename("skill", i))
		require.NoError(t, os.MkdirAll(dir, 0o750))
		path := filepath.Join(dir, "SKILL.md")
		require.NoError(t, os.WriteFile(path, fixtureMarkdownContent(), 0o600))
	}
}

func makeFilename(kind string, i int) string {
	return kind + "-" + string(rune('a'+i))
}

func fixtureMarkdownContent() []byte {
	return []byte("---\nname: fixture\ndescription: a fixture entry\n---\n\nBody paragraph.\n")
}

// TestInstructionBootstrap_NoFrontmatterFileIsSkippedWithNamedWarning pins the
// warn-and-skip disposition for the two malformed file shapes, and the reason
// both are needed rather than one.
//
// agent and skill are embed-only-knowledge types, so a body with no summary is
// refused by the server — and every file rides ONE create_batch, so that refusal
// rolls back EVERY agent and skill node. One malformed file must cost itself.
//
// THE THIRD FIXTURE FILE IS THE POINT. parseInstructionFrontmatter reports ok
// for a well-formed block that carries no description: key, so that shape
// reaches the populated return and emits an empty Summary exactly as the
// no-frontmatter shape does. A two-file fixture greens against a fix that
// handles only the absent-frontmatter case.
//
// Asserting the SURVIVOR COUNT and not merely the skips is what stops this
// passing against an implementation that skips everything; asserting both paths
// BY NAME is what stops it passing against one that skips the right number for
// the wrong reason.
func TestInstructionBootstrap_NoFrontmatterFileIsSkippedWithNamedWarning(t *testing.T) {
	dir := t.TempDir()
	agentsDir := filepath.Join(dir, ".claude", "agents")
	require.NoError(t, os.MkdirAll(agentsDir, 0o750))

	require.NoError(t, os.WriteFile(filepath.Join(agentsDir, "agent-good.md"),
		fixtureMarkdownContent(), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(agentsDir, "agent-nofrontmatter.md"),
		[]byte("Just a body paragraph with no frontmatter block at all.\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(agentsDir, "agent-nodescription.md"),
		[]byte("---\nname: fixture\n---\n\nBody paragraph.\n"), 0o600))

	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	fc := &fakeBootstrapGC{queryNodeCount: 0}
	require.NoError(t, runInstructionBootstrap(context.Background(), fc, dir))

	require.Len(t, fc.calls, 2)
	m := fc.calls[1].req.GetMutation()
	require.NotNil(t, m)
	require.Len(t, m.GetNodeBodies(), 1, "only the well-formed file may reach the batch")
	assert.Equal(t, "agent-good", m.GetNodeBodies()[0].GetName())

	// The survivor's summary is the author's description, and EVERY body in the
	// batch carries one — the property whose violation rolls the batch back.
	for _, b := range m.GetNodeBodies() {
		assert.NotEmpty(t, b.GetSummary(), "node %q reached the batch with no summary", b.GetName())
	}
	assert.Equal(t, "a fixture entry", m.GetNodeBodies()[0].GetSummary())

	// BOTH skipped paths, each named with its own condition.
	out := logs.String()
	assert.Contains(t, out, "agent-nofrontmatter.md")
	assert.Contains(t, out, "no frontmatter block")
	assert.Contains(t, out, "agent-nodescription.md")
	assert.Contains(t, out, "no `description:` key")
	// The well-formed file must NOT be warned about — without this, a warn-on-
	// everything implementation satisfies both assertions above.
	assert.NotContains(t, out, "agent-good.md")
}
