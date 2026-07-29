// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"fmt"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/enginetest"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// pagingFake is a cursor-HONORING type-browse backend: it serves ids strictly
// after the cursor, ascending, capped at Limit, in whichever carrier the plan's
// return mode asks for. Honoring the cursor is what makes these tests discriminate
// — against a fake that returns everything regardless, a single un-paged read is
// indistinguishable from a correct drain.
type pagingFake struct {
	byType map[string][]*knowledgev1.Node
	plans  []*knowledgev1.QueryPlan
}

func (f *pagingFake) exec(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	q := req.GetQuery()
	f.plans = append(f.plans, q)

	nodes := append([]*knowledgev1.Node(nil), f.byType[q.GetSelection().GetNodeType()]...)
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].GetId() < nodes[j].GetId() })
	if cursor := q.GetAfterId(); cursor != "" {
		kept := nodes[:0]
		for _, n := range nodes {
			if n.GetId() > cursor {
				kept = append(kept, n)
			}
		}
		nodes = kept
	}
	if lim := int(q.GetLimit()); lim > 0 && len(nodes) > lim {
		nodes = nodes[:lim]
	}

	if q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_IDS {
		ids := make([]string, 0, len(nodes))
		for _, n := range nodes {
			ids = append(ids, n.GetId())
		}
		return &knowledgev1.ExecuteResponse{Ids: ids}, nil
	}
	return enginetest.ResponseWithNodes(nodes...), nil
}

// Stats satisfies statsRPC for the linkage breakdown, which never calls it.
func (f *pagingFake) Stats(context.Context, *knowledgev1.StatsRequest) (*knowledgev1.StatsResponse, error) {
	return &knowledgev1.StatsResponse{}, nil
}

func (f *pagingFake) Execute(ctx context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	return f.exec(ctx, req)
}

// TestListModulesForRepo_PagedBrowse asserts the modules rollup sees EVERY file
// across more than two pages, and that each arm uses the carrier its consumption
// justifies. Against the unfixed single-Execute read this returns only the first
// page's files, so the file count comes out short.
func TestListModulesForRepo_PagedBrowse(t *testing.T) {
	const files = 2*engine.BrowsePageSize + 7
	f := &pagingFake{byType: map[string][]*knowledgev1.Node{
		string(kgtypes.NodePackage): {
			{Id: "pkg/alpha", Type: string(kgtypes.NodePackage), Summary: "the alpha package"},
			{Id: "pkg/beta", Type: string(kgtypes.NodePackage), Summary: "the beta package"},
		},
	}}
	for i := range files {
		p := fmt.Sprintf("pkg/alpha/f%05d.go", i)
		f.byType[string(kgtypes.NodeFile)] = append(f.byType[string(kgtypes.NodeFile)],
			&knowledgev1.Node{Id: p, Type: string(kgtypes.NodeFile), FilePath: p})
	}

	body := listModulesForRepo(context.Background(), f.exec, "knowledge", "")

	assert.Contains(t, body, fmt.Sprintf("**pkg/alpha** (%d files)", files),
		"every file across every page must reach the rollup")
	assert.Contains(t, body, "**pkg/beta** (0 files)")
	assert.Contains(t, body, "the alpha package", "packages stay hydrated — the listing renders Summary")

	var idsPlans, nodePlans int
	for _, p := range f.plans {
		switch p.GetSelection().GetNodeType() {
		case string(kgtypes.NodeFile):
			assert.Equal(t, knowledgev1.ReturnMode_RETURN_MODE_IDS, p.GetReturnMode(),
				"the file arm reads only paths — it must not hydrate 21 columns per file")
			idsPlans++
		case string(kgtypes.NodePackage):
			assert.NotEqual(t, knowledgev1.ReturnMode_RETURN_MODE_IDS, p.GetReturnMode(),
				"the package arm renders Summary, which no ids carrier serves")
			nodePlans++
		}
		require.NotNil(t, p.AfterId, "after_id must be PRESENT on every page — presence selects the keyset browse")
		assert.True(t, p.GetSkipTotal(), "a drain page never reads Total")
	}
	assert.Equal(t, files/engine.BrowsePageSize+1, idsPlans, "the file browse pages rather than reading the type in one Execute")
	assert.Equal(t, 1, nodePlans, "two packages fit in one page")
}

// TestRenderLinkageProxyBreakdown_PagedBrowse asserts every seeded foreign_graph
// appears with its full count across more than two pages. Against the unfixed
// single-Execute read the later pages' proxies are simply absent from the counts.
func TestRenderLinkageProxyBreakdown_PagedBrowse(t *testing.T) {
	const proxies = 2*engine.BrowsePageSize + 3
	f := &pagingFake{byType: map[string][]*knowledgev1.Node{}}
	want := map[string]int{}
	for i := range proxies {
		fg := []string{"code", "cloud", "practice"}[i%3]
		n := &knowledgev1.Node{Id: fmt.Sprintf("proxy-%05d", i), Type: string(kgtypes.NodeProxy)}
		kgtypes.SetValue(n, "foreign_graph", fg)
		f.byType[string(kgtypes.NodeProxy)] = append(f.byType[string(kgtypes.NodeProxy)], n)
		want[fg]++
	}

	body := renderLinkageProxyBreakdown(context.Background(), f)

	for fg, count := range want {
		assert.Contains(t, body, fmt.Sprintf("%s", fg))
		assert.Contains(t, body, fmt.Sprintf("%d", count),
			"the count for %s must reflect every page, not just the first", fg)
	}
	assert.Len(t, f.plans, proxies/engine.BrowsePageSize+1,
		"the proxy browse pages rather than reading the whole type in one Execute")
	for _, p := range f.plans {
		require.NotNil(t, p.AfterId, "after_id must be PRESENT on every page")
		assert.True(t, p.GetSkipTotal(), "a drain page never reads Total")
	}
}
