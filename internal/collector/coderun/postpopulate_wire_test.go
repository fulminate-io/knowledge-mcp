// SPDX-License-Identifier: Apache-2.0

package coderun

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/enginetest"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// fakeGraphCaller is a package-local postpopulate.GraphCaller for the codesync
// hierarchy / step-linker tests. It resolves code reads by Target.Repo (a miss
// when Repo is empty) and records every create_batch mutation so a test can
// assert the package-node + edge write routes into the per-repo code graph by
// Target.Repo==graphName (NOT Name, NOT Account). It is wire-only — no store
// engine: reads return the typed Nodes carrier the client decode layer expects.
type fakeGraphCaller struct {
	// nodesByRepo[repo] = the nodes present in that code graph.
	nodesByRepo map[string][]*knowledgev1.Node

	mutations []*knowledgev1.ExecuteRequest
}

func (f *fakeGraphCaller) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	if m := req.GetMutation(); m != nil {
		_ = m
		f.mutations = append(f.mutations, req)
		return &knowledgev1.ExecuteResponse{}, nil
	}
	q := req.GetQuery()
	if q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_GRAPH_NAMES {
		return f.graphNames(), nil
	}
	return f.browse(req.GetTarget(), q), nil
}

func (f *fakeGraphCaller) graphNames() *knowledgev1.ExecuteResponse {
	var infos []*knowledgev1.GraphInfo
	for repo := range f.nodesByRepo {
		infos = append(infos, &knowledgev1.GraphInfo{Name: repo})
	}
	return &knowledgev1.ExecuteResponse{GraphNames: infos}
}

// browse serves a type-browse keyed by Target.Repo. An empty repo is a miss —
// the code graph requires a repo selector, so a name:-routed read would land in
// the wrong (or no) graph.
func (f *fakeGraphCaller) browse(tgt *knowledgev1.GraphSelector, q *knowledgev1.QueryPlan) *knowledgev1.ExecuteResponse {
	repo := tgt.GetRepo()
	if repo == "" {
		return &knowledgev1.ExecuteResponse{} // miss on empty repo.
	}
	wantType := q.GetSelection().GetNodeType()
	var matched []*knowledgev1.Node
	for _, n := range f.nodesByRepo[repo] {
		if wantType != "" && n.Type != wantType {
			continue
		}
		matched = append(matched, n)
	}
	return enginetest.ResponseWithNodes(matched...)
}

func (f *fakeGraphCaller) createBatchMutations() []*knowledgev1.ExecuteRequest {
	var out []*knowledgev1.ExecuteRequest
	for _, req := range f.mutations {
		if req.GetMutation().GetKind() == knowledgev1.MutationPlan_MUTATION_KIND_CREATE {
			out = append(out, req)
		}
	}
	return out
}

// edgeKeys returns "from->to:type" for every edge across all create_batch
// mutations.
func (f *fakeGraphCaller) edgeKeys() map[string]bool {
	keys := map[string]bool{}
	for _, req := range f.createBatchMutations() {
		for _, e := range req.GetMutation().GetEdges() {
			keys[e.GetFromId()+"->"+e.GetToId()+":"+e.GetType()] = true
		}
	}
	return keys
}

// nodeIDs returns the set of node ids materialized across all create_batch
// mutations.
func (f *fakeGraphCaller) nodeIDs() map[string]bool {
	ids := map[string]bool{}
	for _, req := range f.createBatchMutations() {
		for _, n := range req.GetMutation().GetNodeBodies() {
			ids[n.GetId()] = true
		}
	}
	return ids
}

// fileNode builds a NodeFile for the given repo-relative path.
func fileNode(path string) *knowledgev1.Node {
	return &knowledgev1.Node{Id: path, Type: string(kgtypes.NodeFile)}
}

// TestBuildHierarchy_RoutesByRepo drives the real BuildHierarchy through the wire
// fake: package nodes + the dir→file / dir hierarchy edges must be emitted in a
// create_batch whose Target.Repo == the repo graph name (NOT Name, NOT Account)
// — the selector-field invariant for the code graph. File→chunk CONTAINS
// edges are NOT built here (the treesitter chunker emits them at collect time).
func TestBuildHierarchy_RoutesByRepo(t *testing.T) {
	const repo = "myrepo"

	seed := []*knowledgev1.Node{
		fileNode("pkg/a/x.go"),
		fileNode("pkg/a/y.go"),
		fileNode("pkg/b/z.go"),
	}

	fc := &fakeGraphCaller{nodesByRepo: map[string][]*knowledgev1.Node{
		repo: seed,
	}}

	require.NoError(t, BuildHierarchy(context.Background(), fc, repo))

	muts := fc.createBatchMutations()
	require.NotEmpty(t, muts, "expected a create_batch with package nodes + edges")
	for _, req := range muts {
		tgt := req.GetTarget()
		assert.Equal(t, "code", tgt.GetGraph(), "hierarchy write must target the code graph")
		assert.Equal(t, repo, tgt.GetRepo(), "code write must route by Repo==%s (NOT Name)", repo)
		assert.Empty(t, tgt.GetName(), "code write must NOT route by Name")
		assert.Empty(t, tgt.GetAccount(), "code write must NOT route by Account")
	}

	// Package nodes materialized for both directories + repo root.
	ids := fc.nodeIDs()
	assert.True(t, ids["pkg/a"], "package node pkg/a must be created")
	assert.True(t, ids["pkg/b"], "package node pkg/b must be created")
	assert.True(t, ids["pkg"], "intermediate package node pkg must be created")
	assert.True(t, ids["."], "repo root package node must be created")

	// Edges: dir→file, parent→child, root→top.
	keys := fc.edgeKeys()
	assert.True(t, keys["pkg/a->pkg/a/x.go:CONTAINS"], "pkg/a must contain x.go")
	assert.True(t, keys["pkg->pkg/a:CONTAINS"], "pkg must contain pkg/a")
	assert.True(t, keys[".->pkg:CONTAINS"], "root must contain pkg")
}

// TestLinkStepsToCode_RoutesByRepo drives LinkStepsToCode: a NodeStep whose
// file_paths metadata names an existing file node gets a KGImplements edge,
// written edges-only into the code graph by Target.Repo.
func TestLinkStepsToCode_RoutesByRepo(t *testing.T) {
	const repo = "myrepo"

	step := &knowledgev1.Node{Id: "step-1", Type: string(kgtypes.NodeStep)}
	kgtypes.SetValue(step, "file_paths", "pkg/a/x.go, pkg/missing/none.go")

	seed := []*knowledgev1.Node{
		step,
		fileNode("pkg/a/x.go"),
	}
	fc := &fakeGraphCaller{nodesByRepo: map[string][]*knowledgev1.Node{
		repo: seed,
	}}

	require.NoError(t, LinkStepsToCode(context.Background(), fc, repo))

	muts := fc.createBatchMutations()
	require.NotEmpty(t, muts, "expected one KGImplements edge write")
	for _, req := range muts {
		tgt := req.GetTarget()
		assert.Equal(t, "code", tgt.GetGraph())
		assert.Equal(t, repo, tgt.GetRepo(), "step→code edges must route by Repo==%s", repo)
		assert.Empty(t, tgt.GetName())
	}

	keys := fc.edgeKeys()
	assert.True(t, keys["step-1->pkg/a/x.go:implements"],
		"step must implement the existing file node")
	assert.False(t, keys["step-1->pkg/missing/none.go:implements"],
		"step must NOT link to a non-existent file node")
}

// TestLinkStepsToCode_NoSteps_NoWrite confirms a code graph with no steps fires
// no create_batch mutation (the empty-edges no-op).
func TestLinkStepsToCode_NoSteps_NoWrite(t *testing.T) {
	const repo = "myrepo"
	fc := &fakeGraphCaller{nodesByRepo: map[string][]*knowledgev1.Node{
		repo: {fileNode("a.go")},
	}}
	require.NoError(t, LinkStepsToCode(context.Background(), fc, repo))
	assert.Empty(t, fc.createBatchMutations(), "no steps → no edge write")
}
