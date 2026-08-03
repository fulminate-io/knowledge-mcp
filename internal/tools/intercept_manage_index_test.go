// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// TestManageGraphSelector guards the per-graph-type selector field routing: each
// graph lowers the operator name onto the field that graph REQUIRES, so a wire
// call is never rejected for a name on the wrong field (e.g. graph=code requires
// repo). Regression guard for the manage(prune)-on-code live failure where the
// name landed on Name instead of Repo.
func TestManageGraphSelector(t *testing.T) {
	t.Run("code routes name to Repo", func(t *testing.T) {
		sel := manageGraphSelector("code", "knowledge")
		assert.Equal(t, "knowledge", sel.GetRepo())
		assert.Empty(t, sel.GetName())
		assert.Empty(t, sel.GetAccount())
	})
	t.Run("cloud routes name to Account", func(t *testing.T) {
		sel := manageGraphSelector("cloud", "aws-prod")
		assert.Equal(t, "aws-prod", sel.GetAccount())
		assert.Empty(t, sel.GetName())
	})
	t.Run("cicd routes name to Account", func(t *testing.T) {
		assert.Equal(t, "gh-org", manageGraphSelector("cicd", "gh-org").GetAccount())
	})
	t.Run("practice routes name to Language", func(t *testing.T) {
		sel := manageGraphSelector("practice", "go")
		assert.Equal(t, "go", sel.GetLanguage())
		assert.Empty(t, sel.GetRepo())
	})
	t.Run("knowledge default leaves instance fields empty", func(t *testing.T) {
		sel := manageGraphSelector("knowledge", "knowledge")
		assert.Empty(t, sel.GetRepo())
		assert.Empty(t, sel.GetName())
		assert.Empty(t, sel.GetAccount())
	})
	t.Run("logs routes name to Name", func(t *testing.T) {
		assert.Equal(t, "q123", manageGraphSelector("logs", "q123").GetName())
	})
	t.Run("branch ops carry repo and no name", func(t *testing.T) {
		sel := branchGraphSelector(manageArgs{Name: "myrepo"})
		assert.Equal(t, "myrepo", sel.GetRepo())
		assert.Empty(t, sel.GetName())
	})
}

// fakeIndexer implements GraphCaller + Indexer (the production graphClientCaller
// shape) and records every Index RPC for assertions. listGraphsBody, when set,
// answers the pipeline_list_graphs Call used by the empty-name multi-graph
// resolution.
type fakeIndexer struct {
	mu             sync.Mutex
	reqs           []*knowledgev1.IndexRequest
	indexErr       error
	listGraphsBody string
	resultJSON     []byte
	branches       []*knowledgev1.GraphInfo
	prunedIDs      []string
	affectedCount  int64
	indexCalls     atomic.Int64
}

func (f *fakeIndexer) Call(_ context.Context, _ string, _ json.RawMessage) (kgtools.ToolResult, error) {
	return kgtools.ToolResult{}, nil
}

// Execute satisfies render.Executor so the rebuild fan-out's graph-list
// resolution (listGraphNamesOfType → fetchGraphNamesOfType) lands. It
// serves a per-type RETURN_MODE_GRAPH_NAMES read by filtering listGraphsBody
// (the {graphs:[{graph_type,graph_name}]} seed) to the requested Target graph
// type, projected to the graph_names_json []store.GraphInfo carrier. Non-graph-
// names plans return an empty response.
func (f *fakeIndexer) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	q := req.GetQuery()
	if q == nil || q.GetReturnMode() != knowledgev1.ReturnMode_RETURN_MODE_GRAPH_NAMES {
		return &knowledgev1.ExecuteResponse{}, nil
	}
	var infos []*knowledgev1.GraphInfo
	if f.listGraphsBody != "" {
		var decoded struct {
			Graphs []struct {
				GraphType string `json:"graph_type"`
				GraphName string `json:"graph_name"`
			} `json:"graphs"`
		}
		_ = json.Unmarshal([]byte(f.listGraphsBody), &decoded)
		for _, g := range decoded.Graphs {
			if g.GraphType == req.GetTarget().GetGraph() && g.GraphName != "" {
				infos = append(infos, &knowledgev1.GraphInfo{Name: g.GraphName})
			}
		}
	}
	return &knowledgev1.ExecuteResponse{GraphNames: infos}, nil
}

func (f *fakeIndexer) Index(_ context.Context, req *knowledgev1.IndexRequest) (*knowledgev1.IndexResponse, error) {
	f.indexCalls.Add(1)
	f.mu.Lock()
	f.reqs = append(f.reqs, req)
	f.mu.Unlock()
	if f.indexErr != nil {
		return nil, f.indexErr
	}
	return &knowledgev1.IndexResponse{
		ResultJson:    f.resultJSON,
		Branches:      f.branches,
		AffectedCount: f.affectedCount,
		PrunedIds:     f.prunedIDs,
	}, nil
}

func (f *fakeIndexer) requests() []*knowledgev1.IndexRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*knowledgev1.IndexRequest, len(f.reqs))
	copy(out, f.reqs)
	return out
}

// manageDeps wires a fakeIndexer as the GraphCaller (via the existing
// interceptTestDeps) so manageIndexer's type-assert lands.
func manageDeps(ix *fakeIndexer) ClientDeps { return interceptTestDeps{gc: ix} }

func manageCall(t *testing.T, ix *fakeIndexer, args string) (bool, kgtools.ToolResult) {
	t.Helper()
	return InterceptManage(opCtx(), manageDeps(ix), kgtools.CallToolParams{Name: "manage", Arguments: json.RawMessage(args)})
}

// manageDepsWithDeleter is manageDeps plus a SegmentDeleter, for the arms that
// carry a completed server-side removal into the shipped segment corpus. A nil
// deleter is the headless/router-less client, which every other fake keeps.
func manageDepsWithDeleter(ix *fakeIndexer, del SegmentDeleter) ClientDeps {
	return interceptTestDeps{gc: ix, deleter: del}
}

func manageCallWithDeleter(t *testing.T, ix *fakeIndexer, del SegmentDeleter, args string) (bool, kgtools.ToolResult) {
	t.Helper()
	return InterceptManage(opCtx(), manageDepsWithDeleter(ix, del), kgtools.CallToolParams{Name: "manage", Arguments: json.RawMessage(args)})
}

// manageCallWithDeleterAndShipper is the twin that ALSO supplies a SegmentShipper,
// for the arms that write the persisted tombstone record as well as the buckets.
func manageCallWithDeleterAndShipper(
	t *testing.T, ix *fakeIndexer, del SegmentDeleter, ship SegmentShipper, args string,
) (bool, kgtools.ToolResult) {
	t.Helper()
	return InterceptManage(opCtx(), interceptTestDeps{gc: ix, deleter: del, shipper: ship},
		kgtools.CallToolParams{Name: "manage", Arguments: json.RawMessage(args)})
}

// ---------------------------------------------------------------------------
// set_metadata_overrides
// ---------------------------------------------------------------------------

func TestInterceptManage_SetMetadataOverrides(t *testing.T) {
	ix := &fakeIndexer{}
	handled, res := manageCall(t, ix,
		`{"operation":"set_metadata_overrides","graph":"cloud","name":"acct-1","force_scalar":["region","az"],"force_edge":["owner"]}`)
	require.True(t, handled)
	require.False(t, res.IsError, "overrides: %s", toolResultText(res))

	reqs := ix.requests()
	require.Len(t, reqs, 1, "exactly one Index RPC")
	r := reqs[0]
	assert.Equal(t, knowledgev1.IndexRequest_INDEX_OP_SET_METADATA_OVERRIDES, r.GetOperation())
	require.NotNil(t, r.GetTarget())
	assert.Equal(t, "cloud", r.GetTarget().GetGraph())
	// cloud graphs route the name to the Account selector field (graph=cloud
	// requires account); manageGraphSelector lowers it there, not onto Name.
	assert.Equal(t, "acct-1", r.GetTarget().GetAccount())
	assert.Equal(t, "region,az", r.GetParams()["force_scalar"])
	assert.Equal(t, "owner", r.GetParams()["force_edge"])

	body := toolResultText(res)
	assert.Contains(t, body, "metadata override config saved for cloud/acct-1")
	assert.Contains(t, body, "force_scalar: [region, az]")
	assert.Contains(t, body, "force_edge:   [owner]")
}

func TestInterceptManage_SetMetadataOverrides_EmptyRejected(t *testing.T) {
	ix := &fakeIndexer{}
	handled, res := manageCall(t, ix,
		`{"operation":"set_metadata_overrides","graph":"cloud","name":"acct-1"}`)
	require.True(t, handled)
	assert.True(t, res.IsError, "empty force lists must be rejected")
	assert.Empty(t, ix.requests(), "no Index RPC on the empty-payload guard")
}

func TestInterceptManage_SetMetadataOverrides_KnowledgeDefaultName(t *testing.T) {
	ix := &fakeIndexer{}
	handled, res := manageCall(t, ix,
		`{"operation":"set_metadata_overrides","force_scalar":["k"]}`)
	require.True(t, handled)
	require.False(t, res.IsError, "overrides: %s", toolResultText(res))
	assert.Contains(t, toolResultText(res), "metadata override config saved for knowledge/default")
}

// ---------------------------------------------------------------------------
// delete_branch / list_branches
// ---------------------------------------------------------------------------

func TestInterceptManage_DeleteBranch(t *testing.T) {
	ix := &fakeIndexer{}
	handled, res := manageCall(t, ix,
		`{"operation":"delete_branch","name":"myrepo","branch":"feature-x"}`)
	require.True(t, handled)
	require.False(t, res.IsError, "delete_branch: %s", toolResultText(res))

	reqs := ix.requests()
	require.Len(t, reqs, 1)
	r := reqs[0]
	assert.Equal(t, knowledgev1.IndexRequest_INDEX_OP_DELETE_BRANCH, r.GetOperation())
	assert.Equal(t, "code", r.GetTarget().GetGraph())
	assert.Equal(t, "myrepo", r.GetParams()["repo"])
	assert.Equal(t, "feature-x", r.GetParams()["branch"])
	assert.Contains(t, toolResultText(res), `Branch graph "myrepo"/"feature-x" deleted.`)
}

func TestInterceptManage_DeleteBranch_NotFoundSurfaced(t *testing.T) {
	ix := &fakeIndexer{indexErr: errors.New(`Index: delete_branch: branch "nope" not found in code/myrepo`)}
	handled, res := manageCall(t, ix,
		`{"operation":"delete_branch","name":"myrepo","branch":"nope"}`)
	require.True(t, handled)
	assert.True(t, res.IsError, "missing overlay surfaces the server NotFound")
	assert.Contains(t, toolResultText(res), "not found")
}

func TestInterceptManage_ListBranches_Markdown(t *testing.T) {
	overlays := []*knowledgev1.GraphInfo{
		{Name: "main", Loaded: true, FileSize: 2048, Nodes: 10, Edges: 5},
		{Name: "feature-y", Loaded: false, FileSize: 1024},
	}
	ix := &fakeIndexer{branches: overlays}
	handled, res := manageCall(t, ix,
		`{"operation":"list_branches","name":"myrepo"}`)
	require.True(t, handled)
	require.False(t, res.IsError, "list_branches: %s", toolResultText(res))

	reqs := ix.requests()
	require.Len(t, reqs, 1)
	assert.Equal(t, knowledgev1.IndexRequest_INDEX_OP_LIST_BRANCHES, reqs[0].GetOperation())

	body := toolResultText(res)
	assert.Contains(t, body, `# Branch overlays for "myrepo" (2 available)`)
	assert.Contains(t, body, "| main | **loaded** | 2.0 KB | 10 | 5 |")
	assert.Contains(t, body, "| feature-y | on disk | 1.0 KB | - | - |")
}

func TestInterceptManage_ListBranches_JSON(t *testing.T) {
	overlays := []*knowledgev1.GraphInfo{{Name: "main", Loaded: true}}
	ix := &fakeIndexer{branches: overlays}
	handled, res := manageCall(t, ix,
		`{"operation":"list_branches","name":"myrepo","format":"json"}`)
	require.True(t, handled)
	require.False(t, res.IsError)
	var decoded struct {
		Repo     string `json:"repo"`
		Total    int    `json:"total"`
		Branches []struct {
			Name string `json:"name"`
		} `json:"branches"`
	}
	require.NoError(t, json.Unmarshal([]byte(toolResultText(res)), &decoded))
	assert.Equal(t, "myrepo", decoded.Repo)
	assert.Equal(t, 1, decoded.Total)
}

func TestInterceptManage_BranchOps_RequireRepo(t *testing.T) {
	ix := &fakeIndexer{}
	for _, op := range []string{"delete_branch", "list_branches"} {
		handled, res := manageCall(t, ix,
			`{"operation":"`+op+`","branch":"b"}`)
		require.True(t, handled, op)
		assert.True(t, res.IsError, "%s without repo must error", op)
	}
	assert.Empty(t, ix.requests(), "no Index RPC when repo is missing")
}
