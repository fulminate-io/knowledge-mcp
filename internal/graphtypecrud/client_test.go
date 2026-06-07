// SPDX-License-Identifier: Apache-2.0

package graphtypecrud

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/enginetest"
	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
)

// fakeExec is a graphTypeCRUDClient for unit tests. Every CRUD op appends the
// compiled *ExecuteRequest and returns the next queued response, so a test body
// asserts on the compiled plan shape and feeds canned ExecuteResponses back.
type fakeExec struct {
	execs     []*knowledgev1.ExecuteRequest
	responses []execResponse
}

type execResponse struct {
	Resp *knowledgev1.ExecuteResponse
	Err  error
}

func (f *fakeExec) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	f.execs = append(f.execs, req)
	if len(f.responses) == 0 {
		return &knowledgev1.ExecuteResponse{}, nil
	}
	resp := f.responses[0]
	f.responses = f.responses[1:]
	return resp.Resp, resp.Err
}

func (f *fakeExec) queueResp(resp *knowledgev1.ExecuteResponse) {
	f.responses = append(f.responses, execResponse{Resp: resp})
}

func (f *fakeExec) queueErr(err error) {
	f.responses = append(f.responses, execResponse{Err: err})
}

// queueDefs appends an ExecuteResponse whose typed Nodes carrier holds each
// GraphTypeDef encoded via ToNode — the List decode path.
func (f *fakeExec) queueDefs(t *testing.T, defs ...*knowledgev1.GraphTypeDef) {
	t.Helper()
	nodes := make([]*knowledgev1.Node, 0, len(defs))
	for _, d := range defs {
		n, err := ToNode(d, d.GetName())
		if err != nil {
			t.Fatalf("queueDefs: ToNode: %v", err)
		}
		nodes = append(nodes, n)
	}
	f.queueResp(enginetest.ResponseWithNodes(nodes...))
}

// sampleDef returns a valid novel GraphTypeDef for the lifecycle tests.
func sampleDef(name string) *knowledgev1.GraphTypeDef {
	return &knowledgev1.GraphTypeDef{
		Name: name,
		Collector: &knowledgev1.CollectorSpec{
			BinaryPath:     "/usr/local/bin/" + name + "-collector",
			ParamTransport: "stdin",
		},
	}
}

func TestClient_List_HappyPath(t *testing.T) {
	fake := &fakeExec{}
	fake.queueDefs(t, sampleDef("jira"), sampleDef("notion"))

	got, err := New(fake).List(context.Background())
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, "jira", got[0].GetName())
	assert.Equal(t, "notion", got[1].GetName())

	require.Len(t, fake.execs, 1)
	q := fake.execs[0].GetQuery()
	require.NotNil(t, q, "List compiles a QueryPlan")
	assert.Equal(t, "graph_type_def", q.GetSelection().GetNodeType())
	assert.Equal(t, int32(graphTypeListLimit), q.GetLimit())
	assert.Greater(t, int(q.GetLimit()), 20, "limit must exceed any default cap")
}

func TestClient_List_EmptyReturnsNil(t *testing.T) {
	fake := &fakeExec{}
	fake.queueDefs(t)

	got, err := New(fake).List(context.Background())
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestClient_List_WireErrorSurfaces(t *testing.T) {
	fake := &fakeExec{}
	fake.queueErr(errors.New("query failed: bad selector"))

	_, err := New(fake).List(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bad selector")
}

func TestClient_ByName_FoundAndMissing(t *testing.T) {
	fake := &fakeExec{}
	fake.queueDefs(t, sampleDef("jira"))
	fake.queueDefs(t, sampleDef("jira"))

	client := New(fake)

	d, ok, err := client.ByName(context.Background(), "jira")
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, "jira", d.GetName())

	_, ok, err = client.ByName(context.Background(), "missing")
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestClient_ByName_EmptyShortCircuits(t *testing.T) {
	fake := &fakeExec{}
	_, ok, err := New(fake).ByName(context.Background(), "")
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Empty(t, fake.execs, "ByName(\"\") must not dispatch any wire call")
}

func TestClient_Create_EmitsUpsertArgs(t *testing.T) {
	fake := &fakeExec{}
	fake.queueResp(&knowledgev1.ExecuteResponse{Ids: []string{"jira"}})

	err := New(fake).Create(context.Background(), sampleDef("jira"))
	require.NoError(t, err)

	require.Len(t, fake.execs, 1)
	m := fake.execs[0].GetMutation()
	require.NotNil(t, m, "Create compiles a MutationPlan")
	assert.Equal(t, knowledgev1.MutationPlan_MUTATION_KIND_UPSERT, m.GetKind())
	require.Len(t, m.GetNodeBodies(), 1)
	body := m.GetNodeBodies()[0]
	assert.Equal(t, "graph_type_def", body.GetType())
	assert.Equal(t, "jira", body.GetId())
	assert.Equal(t, "jira", body.GetName())
	assert.Equal(t, graphTypeSource, body.GetSource())
	// The body rides as the single blob key.
	_, ok := body.GetMetadata()[MetaGraphTypeDefPB]
	assert.True(t, ok, "upsert metadata must carry the proto blob key")
}

func TestClient_Update_EmitsSameUpsertArgs(t *testing.T) {
	fake := &fakeExec{}
	fake.queueResp(&knowledgev1.ExecuteResponse{Ids: []string{"jira"}})

	err := New(fake).Update(context.Background(), sampleDef("jira"))
	require.NoError(t, err)

	require.Len(t, fake.execs, 1)
	m := fake.execs[0].GetMutation()
	require.NotNil(t, m)
	assert.Equal(t, knowledgev1.MutationPlan_MUTATION_KIND_UPSERT, m.GetKind())
	require.Len(t, m.GetNodeBodies(), 1)
	assert.Equal(t, graphTypeSource, m.GetNodeBodies()[0].GetSource())
}

func TestClient_Delete_EmitsDeleteArgs(t *testing.T) {
	fake := &fakeExec{}
	fake.queueResp(&knowledgev1.ExecuteResponse{AffectedCount: 1})

	err := New(fake).Delete(context.Background(), "jira")
	require.NoError(t, err)

	require.Len(t, fake.execs, 1)
	m := fake.execs[0].GetMutation()
	require.NotNil(t, m, "Delete compiles a MutationPlan")
	assert.Equal(t, knowledgev1.MutationPlan_MUTATION_KIND_DELETE, m.GetKind())
	assert.Equal(t, []string{"jira"}, m.GetSelection().GetIds())
}

func TestClient_Delete_NotFoundMapsToErrNotFound(t *testing.T) {
	fake := &fakeExec{}
	fake.queueErr(connect.NewError(connect.CodeNotFound, errors.New("node jira not found")))

	err := New(fake).Delete(context.Background(), "jira")
	require.Error(t, err)
	assert.ErrorIs(t, err, graphclient.ErrNotFound, "Delete should map CodeNotFound to graphclient.ErrNotFound")
}

func TestClient_Delete_OtherWireErrorSurfaces(t *testing.T) {
	fake := &fakeExec{}
	fake.queueErr(connect.NewError(connect.CodePermissionDenied, errors.New("delete failed: permission denied")))

	err := New(fake).Delete(context.Background(), "jira")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "permission denied")
	assert.NotErrorIs(t, err, graphclient.ErrNotFound)
}

func TestClient_NilGraphClient_ReturnsErrors(t *testing.T) {
	c := New(nil)
	_, err := c.List(context.Background())
	require.Error(t, err)
	require.Error(t, c.Create(context.Background(), sampleDef("a")))
	require.Error(t, c.Update(context.Background(), sampleDef("a")))
	require.Error(t, c.Delete(context.Background(), "a"))
}
