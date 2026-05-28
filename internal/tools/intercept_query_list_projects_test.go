// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/enginetest"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// listProjectsFakeGc answers the query(type:X) read over the Execute carrier
// seam (T-GTB6) with the seeded nodes for type X via the nodes_json carrier.
type listProjectsFakeGc struct {
	byType map[string][]*knowledgev1.Node
}

// Call satisfies the interface; the list-projects intercept routes through Execute.
func (g *listProjectsFakeGc) Call(_ context.Context, _ string, _ json.RawMessage) (kgtools.ToolResult, error) {
	return kgtools.ToolResult{}, nil
}

func (g *listProjectsFakeGc) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	typ := req.GetQuery().GetSelection().GetNodeType()
	nodes := g.byType[typ]
	resp := enginetest.ResponseWithNodes(nodes...)
	resp.Total = int64(len(nodes))
	return resp, nil
}

// seedListProjectsFixture creates the same project+ticket+plan shape
// the goldengen capture used, with deterministic IDs/timestamps so
// the scrubbers produce stable bytes.
func seedListProjectsFixture() *listProjectsFakeGc {
	t1 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC).UnixNano()
	// Built with the same fields handleListProjectsJSON's
	// knowledgev1.Node would carry post-CreateProject/CreateTicket/CreatePlan.
	project := knowledgev1.Node{
		Id:          "00000000000000000000000000000001",
		Type:        string(kgtypes.NodeProject),
		SymbolName:  "ls-project",
		Description: "ls p desc",
		Summary:     "ls p sum",
		Source:      "llm:claude",
		Status:      "active",
		CreatedAt:   t1, UpdatedAt: t1,
	}
	ticket := knowledgev1.Node{
		Id:          "00000000000000000000000000000002",
		Type:        string(kgtypes.NodeTicket),
		SymbolName:  "ls-ticket",
		Description: "ls t desc",
		Summary:     "ls t sum",
		Source:      "llm:claude",
		Status:      "open",
		Metadata:    map[string]string{"no_patterns_reason": "fixture"},
		CreatedAt:   t1, UpdatedAt: t1,
	}
	plan := knowledgev1.Node{
		Id:          "00000000000000000000000000000003",
		Type:        string(kgtypes.NodePlan),
		SymbolName:  "ls-plan",
		Description: "ls plan goal",
		Summary:     "ls plan sum",
		Source:      "llm:claude",
		Status:      "active",
		Metadata:    map[string]string{"no_patterns_reason": "fixture"},
		CreatedAt:   t1, UpdatedAt: t1,
	}
	return &listProjectsFakeGc{
		byType: map[string][]*knowledgev1.Node{
			"project": {&project},
			"ticket":  {&ticket},
			"plan":    {&plan},
		},
	}
}

// listProjectsDeps wraps a GraphCaller into ClientDeps. Reuses
// logE2EDeps via alias so we don't redeclare the surface.
type listProjectsDeps = logE2EDeps

func TestInterceptQueryListProjects_TextFormat_Plan(t *testing.T) {
	gc := seedListProjectsFixture()
	deps := &listProjectsDeps{gc: gc}

	args := mustMarshal(t, map[string]any{"type": "plan"})
	handled, res := InterceptQueryListProjects(deps, kgtools.CallToolParams{Name: "query", Arguments: args})
	require.True(t, handled)
	require.False(t, res.IsError, "intercept error: %v", res.Content)

	got := scrubForParity(extractText(res))
	want := readGolden(t, "list_projects_plan")
	assert.Equal(t, want, got)
}

func TestInterceptQueryListProjects_TextFormat_Project(t *testing.T) {
	gc := seedListProjectsFixture()
	deps := &listProjectsDeps{gc: gc}

	args := mustMarshal(t, map[string]any{"type": "project"})
	handled, res := InterceptQueryListProjects(deps, kgtools.CallToolParams{Name: "query", Arguments: args})
	require.True(t, handled)
	require.False(t, res.IsError)

	got := scrubForParity(extractText(res))
	want := readGolden(t, "list_projects_project")
	assert.Equal(t, want, got)
}

func TestInterceptQueryListProjects_TextFormat_Ticket(t *testing.T) {
	gc := seedListProjectsFixture()
	deps := &listProjectsDeps{gc: gc}

	args := mustMarshal(t, map[string]any{"type": "ticket"})
	handled, res := InterceptQueryListProjects(deps, kgtools.CallToolParams{Name: "query", Arguments: args})
	require.True(t, handled)
	require.False(t, res.IsError)

	got := scrubForParity(extractText(res))
	want := readGolden(t, "list_projects_ticket")
	assert.Equal(t, want, got)
}

func TestInterceptQueryListProjects_JSONFormat_Plan(t *testing.T) {
	gc := seedListProjectsFixture()
	deps := &listProjectsDeps{gc: gc}

	args := mustMarshal(t, map[string]any{"type": "plan", "format": "json"})
	handled, res := InterceptQueryListProjects(deps, kgtools.CallToolParams{Name: "query", Arguments: args})
	require.True(t, handled)
	require.False(t, res.IsError)

	got := scrubForParity(extractText(res))
	want := readGolden(t, "list_projects_plan.json")
	assert.Equal(t, want, got)
}

func TestInterceptQueryListProjects_JSONFormat_Project(t *testing.T) {
	gc := seedListProjectsFixture()
	deps := &listProjectsDeps{gc: gc}

	args := mustMarshal(t, map[string]any{"type": "project", "format": "json"})
	handled, res := InterceptQueryListProjects(deps, kgtools.CallToolParams{Name: "query", Arguments: args})
	require.True(t, handled)
	require.False(t, res.IsError)

	got := scrubForParity(extractText(res))
	want := readGolden(t, "list_projects_project.json")
	assert.Equal(t, want, got)
}

func TestInterceptQueryListProjects_JSONFormat_Ticket(t *testing.T) {
	gc := seedListProjectsFixture()
	deps := &listProjectsDeps{gc: gc}

	args := mustMarshal(t, map[string]any{"type": "ticket", "format": "json"})
	handled, res := InterceptQueryListProjects(deps, kgtools.CallToolParams{Name: "query", Arguments: args})
	require.True(t, handled)
	require.False(t, res.IsError)

	got := scrubForParity(extractText(res))
	want := readGolden(t, "list_projects_ticket.json")
	assert.Equal(t, want, got)
}

// TestInterceptQueryListProjects_JSONFormat_FieldsProjection asserts the
// container-listing JSON arm honors the tool-wide `fields` projection: with
// fields=[id,name,status] the rendered nodes carry id+name+status and OMIT
// description/summary/metadata (the heavy fields the fixture node carries),
// reusing the engine.ProjectNodeJSON grammar. Empty fields = full raw nodes
// (no regression). A metadata.<key> projection returns just that key.
func TestInterceptQueryListProjects_JSONFormat_FieldsProjection(t *testing.T) {
	gc := seedListProjectsFixture()
	deps := &listProjectsDeps{gc: gc}

	t.Run("projected omits heavy fields", func(t *testing.T) {
		args := mustMarshal(t, map[string]any{"type": "ticket", "format": "json", "fields": []string{"id", "name", "status"}})
		handled, res := InterceptQueryListProjects(deps, kgtools.CallToolParams{Name: "query", Arguments: args})
		require.True(t, handled)
		require.False(t, res.IsError)
		var payload struct {
			Total int              `json:"total"`
			Nodes []map[string]any `json:"nodes"`
		}
		require.NoError(t, json.Unmarshal([]byte(extractText(res)), &payload))
		require.Len(t, payload.Nodes, 1)
		row := payload.Nodes[0]
		assert.Equal(t, "00000000000000000000000000000002", row["id"])
		assert.Equal(t, "ls-ticket", row["name"])
		assert.Equal(t, "open", row["status"])
		assert.NotContains(t, row, "description")
		assert.NotContains(t, row, "summary")
		assert.NotContains(t, row, "metadata")
	})

	t.Run("empty fields returns full nodes (no regression)", func(t *testing.T) {
		args := mustMarshal(t, map[string]any{"type": "ticket", "format": "json"})
		handled, res := InterceptQueryListProjects(deps, kgtools.CallToolParams{Name: "query", Arguments: args})
		require.True(t, handled)
		require.False(t, res.IsError)
		var payload struct {
			Nodes []knowledgev1.Node `json:"nodes"`
		}
		require.NoError(t, json.Unmarshal([]byte(extractText(res)), &payload))
		require.Len(t, payload.Nodes, 1)
		assert.Equal(t, "ls t desc", payload.Nodes[0].Description)
		assert.Equal(t, "ls t sum", payload.Nodes[0].Summary)
	})

	t.Run("metadata.<key> projection returns just that key", func(t *testing.T) {
		args := mustMarshal(t, map[string]any{"type": "ticket", "format": "json", "fields": []string{"id", "metadata.no_patterns_reason"}})
		handled, res := InterceptQueryListProjects(deps, kgtools.CallToolParams{Name: "query", Arguments: args})
		require.True(t, handled)
		require.False(t, res.IsError)
		var payload struct {
			Nodes []map[string]any `json:"nodes"`
		}
		require.NoError(t, json.Unmarshal([]byte(extractText(res)), &payload))
		require.Len(t, payload.Nodes, 1)
		assert.Equal(t, "fixture", payload.Nodes[0]["metadata.no_patterns_reason"])
		assert.NotContains(t, payload.Nodes[0], "metadata")
	})
}

func TestInterceptQueryListProjects_WrongTool_FallsThrough(t *testing.T) {
	gc := seedListProjectsFixture()
	deps := &listProjectsDeps{gc: gc}
	args := mustMarshal(t, map[string]any{"type": "plan"})
	handled, _ := InterceptQueryListProjects(deps, kgtools.CallToolParams{Name: "search", Arguments: args})
	assert.False(t, handled)
}

func TestInterceptQueryListProjects_NonContainerType_FallsThrough(t *testing.T) {
	gc := seedListProjectsFixture()
	deps := &listProjectsDeps{gc: gc}
	args := mustMarshal(t, map[string]any{"type": "finding"})
	handled, _ := InterceptQueryListProjects(deps, kgtools.CallToolParams{Name: "query", Arguments: args})
	assert.False(t, handled, "non-container type must fall through to handleBrowse")
}

func TestInterceptQueryListProjects_TextSearch_FallsThrough(t *testing.T) {
	gc := seedListProjectsFixture()
	deps := &listProjectsDeps{gc: gc}
	args := mustMarshal(t, map[string]any{"type": "plan", "text": "search term"})
	handled, _ := InterceptQueryListProjects(deps, kgtools.CallToolParams{Name: "query", Arguments: args})
	assert.False(t, handled, "text search must fall through to InterceptSearch")
}
